// navtrace/navtrace.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Package navtrace records and reads traces of individual aircraft's
// flights: everything the aircraft's nav was told and everything it did,
// tick by tick, so that a flight from a real session can be flown again by
// later code and compared with how it went. A trace is a gzipped file of JSON
// records, one per line: the header, then the commands, lookups, and ticks in
// the order they happened, and the end. The sim writes one trace per aircraft
// through a Session; nav's tests read them with Load. A trace's file is named
// by facility, the UTC time its recording began, and callsign, so that traces
// from any number of sessions can be kept together.
package navtrace

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/util"
)

// FormatVersion is written in each trace's header; Load refuses others.
const FormatVersion = 1

// Ext is the extension of a trace file, and HumanExt that of one of an
// aircraft a human controller gave at least one command.
const (
	Ext      = ".jsonl.gz"
	HumanExt = ".human" + Ext
)

// Record is one line of a trace; exactly one of its fields is set.
type Record struct {
	Header  *Header  `json:"header,omitempty"`
	Command *Command `json:"cmd,omitempty"`
	Lookup  *Lookup  `json:"lookup,omitempty"`
	Tick    *Tick    `json:"tick,omitempty"`
	End     *End     `json:"end,omitempty"`
}

// Header describes the flight and holds its Nav, encoded as JSON, as it was
// before the first thing the trace records.
type Header struct {
	Version      int
	Callsign     string
	AircraftType string
	Rules        string // IFR or VFR
	Flight       string // departure, arrival, or overflight
	Departure    string
	Arrival      string
	Facility     string
	Scenario     string
	Recorded     time.Time // wall clock, when recording began
	Revision     string    // of the code that recorded the trace, if known
	Nav          json.RawMessage
}

// Command is a command the aircraft was given: a method of nav.Nav and its
// arguments as JSON, in the order the method declares them. Controller is
// who gave it, a human's TCW or a virtual controller's position, or "" when
// the sim gave it on its own, as it does a waypoint's actions.
type Command struct {
	Time       time.Time
	Name       string
	Args       []json.RawMessage
	Controller string `json:",omitempty"`
	Human      bool   `json:",omitempty"`
}

// Lookup is an answer the nav got about the world, like where a fix is; see
// nav.Lookups. Result is a nav.LookupResult encoded as JSON.
type Lookup struct {
	Time   time.Time
	Kind   string
	Key    string
	Result json.RawMessage
}

// Tick is one update of the aircraft: what the nav was given, and where the
// aircraft was once it had moved.
type Tick struct {
	Time        time.Time
	Wind        [2]float32 // nm/s, east and north
	Temperature float32    // Celsius
	Dewpoint    float32    // Celsius
	Pressure    float32    // millibars
	// METAR is the arrival airport's wind, recorded on the first tick and
	// whenever it changes.
	METAR      *METARWind `json:",omitempty"`
	VFR        bool       `json:",omitempty"`
	UnderBravo bool       `json:",omitempty"`
	Prespawn   bool       `json:",omitempty"`

	Position math.Point2LL
	Altitude float32
	IAS      float32
	GS       float32
	Heading  float32
	Passed   string        `json:",omitempty"` // the fix passed this tick
	Actions  []ActionEvent `json:",omitempty"`
}

// METARWind is the wind an airport's METAR reports; Direction is nil when
// it is variable.
type METARWind struct {
	Direction *int
	Speed     int
	Gust      *int
}

// ActionEvent is a waypoint's actions taking effect, encoded as JSON.
type ActionEvent struct {
	Fix     string
	Actions json.RawMessage
}

// End records the aircraft leaving the sim, or the session ending while it
// was still in it, and who had commanded it.
type End struct {
	Time        time.Time
	Reason      string
	Controllers []Controller
}

// Controller counts the commands one controller gave the aircraft.
type Controller struct {
	Name     string
	Human    bool
	Commands int
}

///////////////////////////////////////////////////////////////////////////
// Writing

// SessionDirName names the directory for the traces of a session that
// started at the given time: the UTC time, facility, and scenario, with
// characters unsafe in file names replaced.
func SessionDirName(start time.Time, facility, scenario string) string {
	return start.UTC().Format("2006-01-02T15-04-05Z") + "_" + fileNameComponent(facility) + "_" +
		fileNameComponent(scenario)
}

var reFileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func fileNameComponent(s string) string {
	return strings.Trim(reFileNameUnsafe.ReplaceAllString(s, "-"), "-")
}

// PruneSessions removes all but the newest keep session directories under
// root, going by their names, which start with when they began. A root that
// doesn't exist has nothing to prune.
func PruneSessions(root string, keep int) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	slices.Sort(dirs)
	for _, d := range dirs[:max(0, len(dirs)-keep)] {
		if err := os.RemoveAll(filepath.Join(root, d)); err != nil {
			return err
		}
	}
	return nil
}

// Session writes the traces of one sim's aircraft into a directory, one
// file per aircraft. Its methods may be called concurrently, though the
// Writers it returns may not.
type Session struct {
	dir                          string
	facility, scenario, revision string

	mu   sync.Mutex
	open map[*Writer]struct{}
}

// NewSession creates dir and returns a Session that writes traces into it,
// recording the facility and scenario the sim runs and the code revision in
// each trace's header.
func NewSession(dir, facility, scenario, revision string) (*Session, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Session{
		dir:      dir,
		facility: facility,
		scenario: scenario,
		revision: revision,
		open:     make(map[*Writer]struct{}),
	}, nil
}

// Dir returns the directory the session writes traces into.
func (s *Session) Dir() string { return s.dir }

// Aircraft describes a flight for its trace's header; Time is the sim time
// the aircraft entered the sim.
type Aircraft struct {
	Callsign     string
	AircraftType string
	Rules        av.FlightRules
	Flight       av.TypeOfFlight
	Departure    av.ICAOAirportCode
	Arrival      av.ICAOAirportCode
	Time         time.Time
}

// Begin returns the Writer that records an aircraft's flight, for its Nav
// to report to. Nothing is written until the Nav first reports.
func (s *Session) Begin(ac Aircraft) *Writer {
	w := &Writer{
		s: s,
		header: Header{
			Version:      FormatVersion,
			Callsign:     ac.Callsign,
			AircraftType: ac.AircraftType,
			Rules:        ac.Rules.String(),
			Flight:       ac.Flight.String(),
			Departure:    string(ac.Departure),
			Arrival:      string(ac.Arrival),
			Facility:     s.facility,
			Scenario:     s.scenario,
			Revision:     s.revision,
		},
		now:         ac.Time,
		controllers: make(map[string]*Controller),
	}
	s.mu.Lock()
	s.open[w] = struct{}{}
	s.mu.Unlock()
	return w
}

// Close ends every trace still being written, as when the session ends with
// aircraft still in the sim, and returns the first error ending them gave.
func (s *Session) Close(t time.Time) error {
	s.mu.Lock()
	open := slices.Collect(maps.Keys(s.open))
	s.mu.Unlock()

	var err error
	for _, w := range open {
		if e := w.End("session ended", t); err == nil {
			err = e
		}
	}
	return err
}

// create creates the file for a trace of the callsign whose recording begins
// at the given time, and returns it with its path. Two traces of one callsign
// can't begin in the same second in one sim, but a numbered name keeps a
// recycled callsign's from colliding if they do.
func (s *Session) create(callsign string, begun time.Time) (*os.File, string, error) {
	base := fileNameComponent(s.facility) + "_" + begun.UTC().Format("2006-01-02T15-04-05Z") + "_" +
		fileNameComponent(callsign)
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		path := filepath.Join(s.dir, name+Ext)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, path, err
	}
}

func (s *Session) forget(w *Writer) {
	s.mu.Lock()
	delete(s.open, w)
	s.mu.Unlock()
}

// ticksPerFlush is how many ticks a Writer buffers before flushing, so that
// a trace whose process exits loses at most that many seconds.
const ticksPerFlush = 30

// Writer records one aircraft's flight; it is the nav.Recorder its Nav
// reports to. Its file is created when the Nav first reports and finished by
// End. Write errors don't disturb the sim: the first one stops the Writer
// writing anything more and is returned by End.
type Writer struct {
	s      *Session
	header Header
	path   string
	f      *os.File
	enc    *encoder
	err    error

	now         time.Time // the sim time of the latest tick, or when the aircraft entered the sim
	controller  string    // who is giving the commands being recorded
	human       bool
	controllers map[string]*Controller
	metar       *METARWind
	ticks       int // since the last flush
}

// Start begins the trace with the Nav as it is now. The Nav calls it before
// the first command, lookup, or update it reports.
func (w *Writer) Start(n *nav.Nav) {
	var err error
	if w.header.Nav, err = json.Marshal(n); err != nil {
		w.err = fmt.Errorf("encoding nav: %w", err)
		return
	}
	w.header.Recorded = time.Now().UTC()
	if w.f, w.path, err = w.s.create(w.header.Callsign, w.header.Recorded); err != nil {
		w.err = err
		return
	}
	w.enc = newEncoder(w.f)
	w.write(Record{Header: &w.header})
	w.flush()
}

// SetOrigin names who is giving the commands recorded from now until
// ClearOrigin: a human controller's TCW, or a virtual controller's position.
func (w *Writer) SetOrigin(controller string, human bool) {
	w.controller, w.human = controller, human
}

// ClearOrigin marks the commands that follow as the sim's own.
func (w *Writer) ClearOrigin() {
	w.controller, w.human = "", false
}

// Command records a command the Nav was given; see nav.Recorder.
func (w *Writer) Command(name string, args ...any) {
	c := &Command{Time: w.now, Name: name, Controller: w.controller, Human: w.human}
	for _, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			w.fail(fmt.Errorf("%s: encoding argument: %w", name, err))
			return
		}
		c.Args = append(c.Args, b)
	}
	if w.controller != "" {
		ctrl, ok := w.controllers[w.controller]
		if !ok {
			ctrl = &Controller{Name: w.controller, Human: w.human}
			w.controllers[w.controller] = ctrl
		}
		ctrl.Commands++
	}
	w.write(Record{Command: c})
	w.flush()
}

// Lookup records an answer the Nav got from its Lookups; see nav.Recorder.
func (w *Writer) Lookup(kind, key string, result any) {
	b, err := json.Marshal(result)
	if err != nil {
		w.fail(fmt.Errorf("%s %s: encoding result: %w", kind, key, err))
		return
	}
	w.write(Record{Lookup: &Lookup{Time: w.now, Kind: kind, Key: key, Result: b}})
}

// Update records one update of the Nav; see nav.Recorder.
func (w *Writer) Update(u nav.UpdateRecord) {
	w.now = u.Time.Time()
	fs := &u.State
	t := &Tick{
		Time:        w.now,
		Wind:        u.Weather.WindVec(),
		Temperature: u.Weather.Temperature().Celsius(),
		Dewpoint:    u.Weather.Dewpoint().Celsius(),
		Pressure:    u.Weather.Pressure(),
		VFR:         u.Rules == av.FlightRulesVFR,
		UnderBravo:  u.UnderBravo,
		Prespawn:    u.Prespawn,
		Position:    fs.Position,
		Altitude:    fs.Altitude,
		IAS:         fs.IAS,
		GS:          fs.GS,
		Heading:     float32(fs.Heading),
	}
	if m := u.METAR; m != nil {
		wind := METARWind{Direction: m.WindDir, Speed: m.WindSpeed, Gust: m.WindGust}
		if w.metar == nil || !w.metar.equals(wind) {
			t.METAR = &wind
			w.metar = &wind
		}
	}
	if wp := u.Result.PassedWaypoint; wp != nil {
		t.Passed = wp.Fix
	}
	for _, ev := range u.Result.ActionEvents {
		b, err := json.Marshal(ev.Actions)
		if err != nil {
			w.fail(fmt.Errorf("encoding waypoint actions: %w", err))
			return
		}
		t.Actions = append(t.Actions, ActionEvent{Fix: ev.Waypoint.Fix, Actions: b})
	}
	w.write(Record{Tick: t})
	if w.ticks++; w.ticks >= ticksPerFlush {
		w.flush()
	}
}

func (m METARWind) equals(o METARWind) bool {
	return equalInts(m.Direction, o.Direction) && m.Speed == o.Speed && equalInts(m.Gust, o.Gust)
}

func equalInts(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// End finishes the trace, recording why the aircraft left the sim at time
// t, and returns the first error the Writer met. A Writer whose Nav never
// reported writes nothing.
func (w *Writer) End(reason string, t time.Time) error {
	w.s.forget(w)
	if w.f == nil {
		return w.err
	}

	end := &End{Time: t, Reason: reason}
	for c := range util.SortedMapValues(w.controllers) {
		end.Controllers = append(end.Controllers, *c)
	}
	w.write(Record{End: end})

	if err := w.enc.close(); w.err == nil {
		w.err = err
	}
	if err := w.f.Close(); w.err == nil {
		w.err = err
	}
	w.f = nil
	if w.err != nil {
		os.Remove(w.path)
		return w.err
	}
	if end.human() {
		human := strings.TrimSuffix(w.path, Ext) + HumanExt
		if err := os.Rename(w.path, human); err != nil {
			return err
		}
		w.path = human
	}
	return nil
}

// Path returns where the trace was written, once Start has been called.
func (w *Writer) Path() string { return w.path }

func (e *End) human() bool {
	for _, c := range e.Controllers {
		if c.Human {
			return true
		}
	}
	return false
}

func (w *Writer) write(r Record) {
	if w.err != nil || w.f == nil {
		return
	}
	if err := w.enc.encode(r); err != nil {
		w.fail(err)
	}
}

func (w *Writer) flush() {
	if w.err != nil || w.f == nil {
		return
	}
	w.ticks = 0
	if err := w.enc.flush(); err != nil {
		w.fail(err)
	}
}

// encoder writes records as gzipped JSON lines.
type encoder struct {
	bw  *bufio.Writer
	gz  *gzip.Writer
	enc *json.Encoder
}

func newEncoder(w io.Writer) *encoder {
	bw := bufio.NewWriter(w)
	gz := gzip.NewWriter(bw)
	return &encoder{bw: bw, gz: gz, enc: json.NewEncoder(gz)}
}

func (e *encoder) encode(r Record) error { return e.enc.Encode(r) }

// flush writes everything encoded so far through to the underlying writer.
func (e *encoder) flush() error {
	if err := e.gz.Flush(); err != nil {
		return err
	}
	return e.bw.Flush()
}

// close finishes the gzip stream and flushes; the underlying writer is left
// to the caller.
func (e *encoder) close() error {
	if err := e.gz.Close(); err != nil {
		return err
	}
	return e.bw.Flush()
}

func (w *Writer) fail(err error) {
	if w.err == nil {
		w.err = err
	}
}

///////////////////////////////////////////////////////////////////////////
// Reading

// Trace is a trace read back from its file.
type Trace struct {
	Path    string
	Header  Header
	Records []Record // what follows the header, in order
}

// Load reads the trace at path. A trace whose Writer never finished it, as
// when the process recording it exited, ends partway through a record; Load
// returns the records before it.
func Load(path string) (*Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dec := json.NewDecoder(gz)

	t := &Trace{Path: path}
	var first Record
	if err := dec.Decode(&first); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if first.Header == nil {
		return nil, fmt.Errorf("%s: doesn't start with a header", path)
	}
	t.Header = *first.Header
	if t.Header.Version != FormatVersion {
		return nil, fmt.Errorf("%s: trace format version %d; this reads version %d", path,
			t.Header.Version, FormatVersion)
	}

	for {
		var r Record
		err := dec.Decode(&r)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return t, nil
		} else if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		t.Records = append(t.Records, r)
	}
}

// Save writes the trace to path, replacing any file there. It writes a
// temporary file first so that a reader never sees a partly written trace.
func (t *Trace) Save(path string) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	enc := newEncoder(f)
	err = enc.encode(Record{Header: &t.Header})
	for _, r := range t.Records {
		if err == nil {
			err = enc.encode(r)
		}
	}
	if e := enc.close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// End returns the trace's end record, or nil if the trace was never finished.
func (t *Trace) End() *End {
	if n := len(t.Records); n > 0 && t.Records[n-1].End != nil {
		return t.Records[n-1].End
	}
	return nil
}

// HumanControlled reports whether a human controller gave the aircraft any
// command.
func (t *Trace) HumanControlled() bool {
	if e := t.End(); e != nil {
		return e.human()
	}
	for _, r := range t.Records {
		if r.Command != nil && r.Command.Human {
			return true
		}
	}
	return false
}
