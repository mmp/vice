// simlog/simlog.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Package simlog reads and writes session logs. A server records each sim it
// runs in one: the sim as the session started, then everything that changed
// it afterward in the order it happened, along with where every aircraft was
// each second. Replaying the log with later code and comparing where the
// aircraft went against the recorded flights shows what the code change
// altered.
package simlog

import (
	"bufio"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"

	"github.com/vmihailenco/msgpack/v5"
)

// A log file is the magic string followed by a flate stream holding the
// Header, the snapshot, and then the records, each preceded by its Kind.
const magic = "vice session log\n"

// FormatVersion is the version of the log format; a Header records the one
// its log was written with.
const FormatVersion = 1

// Header describes the session a log records.
type Header struct {
	Version       int
	Facility      string
	ScenarioGroup string
	Scenario      string
	Start         time.Time // wall clock time the session started
	SimStart      time.Time // sim time the snapshot was taken at
	Database      string    // hash of the static database the session ran on
	GOARCH        string
	Revision      string // revision of the code that wrote the log
}

// Kind identifies a record in a log.
type Kind uint8

const (
	KindWeather Kind = iota + 1
	KindRequest
	KindSpawn
	KindDelete
	KindTick
)

// Weather records an atmospheric grid the sim's weather model installed.
type Weather struct {
	Time   time.Time
	Update wx.AtmosUpdate
}

// Request records a controller's request that changed the sim: the RPC
// method they called and its arguments, without their token. Inputs holds
// what the request read from outside the sim, like the resources, in the
// order it read them, for a replay of the request to read in their place.
// Error is the sim's reason for refusing the request, or empty if it didn't.
// Aircraft names the aircraft the request was about, if any, and Summary
// describes it for a reader.
type Request struct {
	Time     time.Time
	TCW      string
	Method   string
	Args     msgpack.RawMessage
	Inputs   []msgpack.RawMessage
	Error    string
	Aircraft string
	Summary  string
}

// Spawn records an aircraft entering the sim, or one already in it when the
// log starts.
type Spawn struct {
	Time         time.Time
	Callsign     string
	AircraftType string
	Rules        string
	Flight       string // departure, arrival, or overflight
	Departure    string
	Arrival      string
	Route        string
}

// Delete records an aircraft leaving the sim and why.
type Delete struct {
	Time     time.Time
	Callsign string
	Reason   string
}

// Tick records where every flying aircraft was at the end of one second of
// the sim.
type Tick struct {
	Time     time.Time
	Aircraft []Sample
}

// Sample is an aircraft's state at a tick. There are a great many of them,
// so they are encoded as arrays rather than maps with the field names.
type Sample struct {
	//lint:ignore U1000 msgpack reads the field's tag
	_msgpack struct{} `msgpack:",as_array"`

	Callsign string
	Position math.Point2LL
	Altitude float32
	IAS      float32
	GS       float32
	Heading  float32
}

// Revision returns the version control revision the running binary was
// built from, marked if it had local modifications, or "" if it isn't known.
func Revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = "+modified"
			}
		}
	}
	if rev == "" {
		return ""
	}
	return rev + modified
}

///////////////////////////////////////////////////////////////////////////
// Writer

// ticksPerFlush is how many Tick records the Writer buffers before it
// flushes; a log whose writer never closes, as when the process exits,
// loses at most that many seconds of flight.
const ticksPerFlush = 30

// Writer writes a log. Its methods may be called concurrently. Write errors
// don't interrupt the sim: the first one stops the Writer writing anything
// more and is returned by Close.
type Writer struct {
	mu    sync.Mutex
	w     io.WriteCloser
	fw    *flate.Writer
	enc   *msgpack.Encoder
	err   error
	ticks int
}

// NewWriter starts a log in w with its header and the snapshot of the sim
// that the session starts from.
func NewWriter(w io.WriteCloser, h Header, snapshot []byte) (*Writer, error) {
	if _, err := io.WriteString(w, magic); err != nil {
		return nil, err
	}
	fw, err := flate.NewWriter(w, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	lw := &Writer{w: w, fw: fw, enc: msgpack.NewEncoder(fw)}
	lw.enc.UseCompactInts(true)

	h.Version = FormatVersion
	if err := lw.enc.Encode(h); err != nil {
		return nil, err
	}
	if err := lw.enc.EncodeBytes(snapshot); err != nil {
		return nil, err
	}
	if err := fw.Flush(); err != nil {
		return nil, err
	}
	return lw, nil
}

// Create creates a log file at path; it fails if the file exists.
func Create(path string, h Header, snapshot []byte) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	w, err := NewWriter(f, h, snapshot)
	if err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return w, nil
}

func (w *Writer) write(kind Kind, record any) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.err != nil {
		return
	}
	if err := w.enc.EncodeUint8(uint8(kind)); err != nil {
		w.err = err
		return
	}
	if err := w.enc.Encode(record); err != nil {
		w.err = err
		return
	}

	// Requests and weather are rare and a replay can't do without them, so
	// they go to the file right away.
	if kind == KindTick {
		w.ticks++
	}
	if kind == KindRequest || kind == KindWeather || w.ticks >= ticksPerFlush {
		w.err = w.fw.Flush()
		w.ticks = 0
	}
}

func (w *Writer) Weather(r Weather) { w.write(KindWeather, r) }
func (w *Writer) Request(r Request) { w.write(KindRequest, r) }
func (w *Writer) Spawn(r Spawn)     { w.write(KindSpawn, r) }
func (w *Writer) Delete(r Delete)   { w.write(KindDelete, r) }
func (w *Writer) Tick(r Tick)       { w.write(KindTick, r) }

// Close finishes the log and closes the file, returning the first error
// the Writer encountered.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.err == nil {
		w.err = w.fw.Close()
	}
	if err := w.w.Close(); w.err == nil {
		w.err = err
	}
	return w.err
}

///////////////////////////////////////////////////////////////////////////
// Reading

// Session is a log's contents.
type Session struct {
	Header   Header
	Snapshot []byte
	Events   []Event
}

// Event is one record from a log: Kind says which of the others is set.
type Event struct {
	Kind    Kind
	Weather *Weather
	Request *Request
	Spawn   *Spawn
	Delete  *Delete
	Tick    *Tick
}

// Time returns the sim time the event happened at.
func (e Event) Time() time.Time {
	switch e.Kind {
	case KindWeather:
		return e.Weather.Time
	case KindRequest:
		return e.Request.Time
	case KindSpawn:
		return e.Spawn.Time
	case KindDelete:
		return e.Delete.Time
	default:
		return e.Tick.Time
	}
}

func openLog(path string) (*os.File, *msgpack.Decoder, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	r := bufio.NewReader(f)
	m := make([]byte, len(magic))
	if _, err := io.ReadFull(r, m); err != nil || string(m) != magic {
		f.Close()
		return nil, nil, fmt.Errorf("%s: not a session log", path)
	}
	return f, msgpack.NewDecoder(flate.NewReader(r)), nil
}

// ReadHeader returns the header of the log at path.
func ReadHeader(path string) (Header, error) {
	f, dec, err := openLog(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()

	var h Header
	if err := dec.Decode(&h); err != nil {
		return Header{}, fmt.Errorf("%s: %w", path, err)
	}
	return h, nil
}

// Load reads the log at path. A log whose writer never closed it, as when
// the process running the session exits, ends partway through a record;
// Load returns the records up to it.
func Load(path string) (*Session, error) {
	f, dec, err := openLog(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var s Session
	if err := dec.Decode(&s.Header); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Header.Version != FormatVersion {
		return nil, fmt.Errorf("%s: log format version %d; this reads version %d", path,
			s.Header.Version, FormatVersion)
	}
	if s.Snapshot, err = dec.DecodeBytes(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	for {
		e, err := decodeEvent(dec)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &s, nil
		} else if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		s.Events = append(s.Events, e)
	}
}

func decodeEvent(dec *msgpack.Decoder) (Event, error) {
	kind, err := dec.DecodeUint8()
	if err != nil {
		return Event{}, err
	}

	e := Event{Kind: Kind(kind)}
	switch e.Kind {
	case KindWeather:
		e.Weather = &Weather{}
		err = dec.Decode(e.Weather)
	case KindRequest:
		e.Request = &Request{}
		err = dec.Decode(e.Request)
	case KindSpawn:
		e.Spawn = &Spawn{}
		err = dec.Decode(e.Spawn)
	case KindDelete:
		e.Delete = &Delete{}
		err = dec.Decode(e.Delete)
	case KindTick:
		e.Tick = &Tick{}
		err = dec.Decode(e.Tick)
	default:
		err = fmt.Errorf("unknown record kind %d", kind)
	}
	return e, err
}
