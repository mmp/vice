// nav/trace_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav_test

import (
	"bytes"
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/navtrace"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/wx"
)

var updateTraces = flag.Bool("update", false,
	"rewrite the recorded flight of each trace whose replay differs, making the replay the reference")

// tracesDir holds the traces TestTraces replays.
const tracesDir = "testdata/traces"

// How far the replay may stray from the recording before the aircraft
// counts as flying differently.
const (
	positionTolerance = 0.05 // nautical miles
	altitudeTolerance = 25   // feet
	speedTolerance    = 2    // knots, indicated and ground speed alike
	headingTolerance  = 2    // degrees
)

// TestTraces replays each trace under tracesDir with the current code and
// fails for each flight that differs from the recording: the Nav the trace
// starts with is given the recorded commands, lookups, and weather, tick by
// tick, and where it ends up is compared with where the recording had it.
func TestTraces(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(tracesDir, "*"+navtrace.Ext))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skipf("no traces in %s", tracesDir)
	}

	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), navtrace.Ext), func(t *testing.T) {
			tr, err := navtrace.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			r := replayTrace(t, tr)
			if r.divergence == nil {
				return
			}
			if *updateTraces {
				if err := r.save(tr, path); err != nil {
					t.Fatal(err)
				}
				t.Logf("updated the recording:\n%s", r.describe(tr))
				return
			}
			t.Error(r.describe(tr))
		})
	}
}

// replay is what came of replaying a trace: the replay's version of each of
// the trace's ticks, and the first tick that differed, if one did.
type replay struct {
	ticks      []navtrace.Tick
	divergence *divergence
	refusals   []string // what the replay's pilot refused, for the report
}

type divergence struct {
	record             int // index into the trace's records
	recorded, replayed navtrace.Tick
}

// replayTrace flies the trace again with the current code.
func replayTrace(t *testing.T, tr *navtrace.Trace) replay {
	t.Helper()

	var n nav.Nav
	if err := decodeStrict(tr.Header.Nav, &n); err != nil {
		t.Fatalf("decoding the header's Nav: %v (if the Nav's fields changed, rewrite the trace)", err)
	}
	n.SetLookups(recordedLookups{t: t, answers: lookupAnswers(tr)})

	var r replay
	var metar *wx.METAR
	for i, rec := range tr.Records {
		switch {
		case rec.Command != nil:
			if refusal := giveCommand(t, &n, rec.Command); refusal != "" {
				r.refusals = append(r.refusals, clock(rec.Command.Time)+" "+rec.Command.Name+": "+refusal)
			}

		case rec.Tick != nil:
			tick := *rec.Tick
			if tick.METAR != nil {
				metar = &wx.METAR{WindDir: tick.METAR.Direction, WindSpeed: tick.METAR.Speed, WindGust: tick.METAR.Gust}
			}
			rules := av.FlightRulesIFR
			if tick.VFR {
				rules = av.FlightRulesVFR
			}
			n.Prespawn = tick.Prespawn
			wxs := wx.MakeSample(tick.Wind, tick.Temperature, tick.Dewpoint, tick.Pressure)
			result := n.UpdateWithWeather(tr.Header.Callsign, wxs, metar, rules, nav.NewTime(tick.Time), tick.UnderBravo)

			replayed := tick
			fs := n.FlightState
			replayed.Position, replayed.Altitude = fs.Position, fs.Altitude
			replayed.IAS, replayed.GS, replayed.Heading = fs.IAS, fs.GS, float32(fs.Heading)
			replayed.Passed, replayed.Actions = "", nil
			if result.PassedWaypoint != nil {
				replayed.Passed = result.PassedWaypoint.Fix
			}
			for _, ev := range result.ActionEvents {
				actions, err := json.Marshal(ev.Actions)
				if err != nil {
					t.Fatal(err)
				}
				replayed.Actions = append(replayed.Actions, navtrace.ActionEvent{Fix: ev.Waypoint.Fix, Actions: actions})
			}
			r.ticks = append(r.ticks, replayed)
			if r.divergence == nil && exceeds(tick, replayed) {
				r.divergence = &divergence{record: i, recorded: tick, replayed: replayed}
			}
		}
	}
	return r
}

// decodeStrict decodes JSON into v, refusing fields v doesn't have so that a
// renamed field fails by name rather than silently decoding as zero.
func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// giveCommand gives the Nav a recorded command by calling the method it
// names with its recorded arguments, and returns what the pilot refused, if
// anything.
func giveCommand(t *testing.T, n *nav.Nav, c *navtrace.Command) string {
	t.Helper()

	m := reflect.ValueOf(n).MethodByName(c.Name)
	if !m.IsValid() {
		t.Fatalf("%s: Nav has no such command (if it was renamed, rewrite the trace)", c.Name)
	}
	mt := m.Type()
	if mt.NumIn() != len(c.Args) {
		t.Fatalf("%s: recorded with %d arguments but takes %d (rewrite the trace)", c.Name, len(c.Args), mt.NumIn())
	}
	args := make([]reflect.Value, len(c.Args))
	for i, raw := range c.Args {
		v := reflect.New(mt.In(i))
		if err := decodeStrict(raw, v.Interface()); err != nil {
			t.Fatalf("%s: argument %d: %v (rewrite the trace)", c.Name, i, err)
		}
		args[i] = v.Elem()
	}

	for _, out := range m.Call(args) {
		if unable, ok := out.Interface().(speech.UnableIntent); ok {
			return fmt.Sprintf("%v", unable)
		}
	}
	return ""
}

// exceeds reports whether the replay's tick differs from the recorded one by
// more than the tolerances allow. Two identical positions may not come out
// exactly zero apart where multiply-adds are fused, so they are compared for
// equality first.
func exceeds(a, b navtrace.Tick) bool {
	return (a.Position != b.Position && math.NMDistance2LL(a.Position, b.Position) > positionTolerance) ||
		math.Abs(a.Altitude-b.Altitude) > altitudeTolerance ||
		math.Abs(a.IAS-b.IAS) > speedTolerance ||
		math.Abs(a.GS-b.GS) > speedTolerance ||
		math.HeadingDifference(math.MagneticHeading(a.Heading), math.MagneticHeading(b.Heading)) > headingTolerance
}

// save writes the trace with the replay's ticks in place of the recorded
// ones, making the replay the reference.
func (r replay) save(tr *navtrace.Trace, path string) error {
	k := 0
	for _, rec := range tr.Records {
		if rec.Tick != nil {
			*rec.Tick = r.ticks[k]
			k++
		}
	}
	return tr.Save(path)
}

// describe says how the replay differed, for someone to read: the flight,
// where it first diverged and by how much, the commands just before, and
// what the replay's pilot refused.
func (r replay) describe(tr *navtrace.Trace) string {
	h := tr.Header
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s-%s %s %s, recorded %s by %s (%s %s)\n", h.Callsign, h.AircraftType, h.Departure,
		h.Arrival, h.Rules, h.Flight, h.Recorded.UTC().Format("2006-01-02 15:04Z"),
		cmp.Or(h.Revision, "an unknown revision"), h.Facility, h.Scenario)

	d := r.divergence
	var start time.Time
	for _, rec := range tr.Records {
		if rec.Tick != nil {
			start = rec.Tick.Time
			break
		}
	}
	fmt.Fprintf(&b, "  diverged at %s, %s into the trace: %s\n", clock(d.recorded.Time),
		durationString(d.recorded.Time.Sub(start)), describeDifference(d.recorded, d.replayed))

	var commands []string
	for i := d.record - 1; i >= 0 && len(commands) < 3; i-- {
		if c := tr.Records[i].Command; c != nil {
			commands = append(commands, describeCommand(c))
		}
	}
	if len(commands) > 0 {
		slices.Reverse(commands)
		fmt.Fprintf(&b, "  commands before it: %s\n", strings.Join(commands, "; "))
	}
	if len(r.refusals) > 0 {
		fmt.Fprintf(&b, "  the replay's pilot refused: %s\n", strings.Join(r.refusals, "; "))
	}
	return b.String()
}

func describeDifference(a, b navtrace.Tick) string {
	var diffs []string
	if a.Position != b.Position {
		dist := math.NMDistance2LL(a.Position, b.Position)
		diffs = append(diffs, strconv.FormatFloat(float64(dist), 'g', 2, 32)+" nm apart")
	}
	pair := func(what string, x, y float32) {
		if x != y {
			sx, sy := distinguish(x, y)
			diffs = append(diffs, what+" "+sx+"/"+sy)
		}
	}
	pair("altitude", a.Altitude, b.Altitude)
	pair("IAS", a.IAS, b.IAS)
	pair("GS", a.GS, b.GS)
	pair("heading", a.Heading, b.Heading)
	s := strings.Join(diffs, ", ") + " (recorded/replay); recorded at " + a.Position.DDString()
	if a.Passed != b.Passed {
		s += fmt.Sprintf("; passed %q/%q", a.Passed, b.Passed)
	}
	return s
}

// distinguish formats two different values with as few decimal places as
// shows them to be different.
func distinguish(a, b float32) (string, string) {
	for prec := 0; ; prec++ {
		sa := strconv.FormatFloat(float64(a), 'f', prec, 32)
		sb := strconv.FormatFloat(float64(b), 'f', prec, 32)
		if sa != sb || prec == 9 {
			return sa, sb
		}
	}
}

func describeCommand(c *navtrace.Command) string {
	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		args[i] = string(a)
		if len(args[i]) > 40 {
			args[i] = args[i][:37] + "..."
		}
	}
	who := c.Controller
	if who == "" {
		who = "sim"
	}
	return fmt.Sprintf("%s %s %s(%s)", clock(c.Time), who, c.Name, strings.Join(args, ", "))
}

func clock(t time.Time) string {
	return t.UTC().Format("15:04:05")
}

func durationString(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

///////////////////////////////////////////////////////////////////////////
// Recorded lookups

// recordedLookups answers the Nav's questions about the world from what the
// trace recorded; see nav.Lookups.
type recordedLookups struct {
	t       *testing.T
	answers map[string]json.RawMessage // by kind and key
}

func lookupAnswers(tr *navtrace.Trace) map[string]json.RawMessage {
	m := make(map[string]json.RawMessage)
	for _, rec := range tr.Records {
		if l := rec.Lookup; l != nil {
			m[l.Kind+" "+l.Key] = l.Result
		}
	}
	return m
}

// answer decodes the recorded answer to a question into result, failing the
// test if the recording never asked it.
func (l recordedLookups) answer(kind, key string, result any) {
	raw, ok := l.answers[kind+" "+key]
	if !ok {
		l.t.Errorf("the replay asked about %s %s, which the recording never did; rewrite the trace or re-record it", kind, key)
		return
	}
	if err := decodeStrict(raw, result); err != nil {
		l.t.Errorf("recorded answer about %s %s: %v", kind, key, err)
	}
}

func (l recordedLookups) Waypoint(fix string) (math.Point2LL, bool) {
	var r nav.LookupResult[math.Point2LL]
	l.answer("waypoint", fix, &r)
	return r.Value, r.OK
}

func (l recordedLookups) Airport(code string) (math.Point2LL, float32, bool) {
	var r nav.LookupResult[nav.AirportLookup]
	l.answer("airport", code, &r)
	return r.Value.Location, r.Value.Elevation, r.OK
}

func (l recordedLookups) Declination(fix string) (float32, bool) {
	var r nav.LookupResult[float32]
	l.answer("declination", fix, &r)
	return r.Value, r.OK
}

func (l recordedLookups) Airways(name string) ([]av.Airway, bool) {
	var r nav.LookupResult[[]av.Airway]
	l.answer("airways", name, &r)
	return r.Value, r.OK
}

func (l recordedLookups) Holds(fix string) ([]av.Hold, bool) {
	var r nav.LookupResult[[]av.Hold]
	l.answer("holds", fix, &r)
	return r.Value, r.OK
}

func (l recordedLookups) Runway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	var r nav.LookupResult[av.Runway]
	l.answer("runway", string(airport)+"/"+runway, &r)
	return r.Value, r.OK
}

func (l recordedLookups) OppositeRunway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	var r nav.LookupResult[av.Runway]
	l.answer("opposite_runway", string(airport)+"/"+runway, &r)
	return r.Value, r.OK
}

///////////////////////////////////////////////////////////////////////////

// A flight recorded by the harness replays without differing; a recording
// altered to disagree is reported at the tick it disagrees; and a trace saved
// with the replay's ticks then replays cleanly.
func TestReplayRecordedFlight(t *testing.T) {
	dir := t.TempDir()
	session, err := navtrace.NewSession(dir, "N90", "test", "")
	if err != nil {
		t.Fatal(err)
	}

	// ROSLY is on the I22L approach but not on the route, so the direct
	// joins the approach; the hold-short runway is looked up.
	f := nav.NewArrivalFlight(t, nav.ArrivalConfig{
		Waypoints:        "HAUPT/a6000 LEFER/a4000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  5000,
		InitialSpeed:     210,
		AssignedAltitude: 5000,
	}).SetWind(270, 20)
	w := session.Begin(navtrace.Aircraft{Callsign: "TEST001", AircraftType: "A320", Rules: av.FlightRulesIFR,
		Flight: av.FlightTypeArrival, Departure: "KMCO", Arrival: "KJFK", Time: f.SimTime().Time()})
	f.Nav().SetRecorder(w)

	f.Step(20)
	f.ExpectApproach("I22L/LAHSO22R")
	f.DirectFix("ROSLY")
	f.Step(120)
	f.ClearedApproach("I22L")
	f.Step(300)
	if err := w.End("landed", f.SimTime().Time()); err != nil {
		t.Fatal(err)
	}

	tr, err := navtrace.Load(w.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupAnswers(tr)["runway KJFK/22R"]; !ok {
		t.Error("the hold-short runway's lookup wasn't recorded")
	}
	if r := replayTrace(t, tr); r.divergence != nil {
		t.Fatalf("the replay of a fresh recording diverged:\n%s", r.describe(tr))
	} else if len(r.refusals) > 0 {
		t.Errorf("the pilot refused: %v", r.refusals)
	}

	// Disagree with the recording partway through.
	altered := -1
	for i, rec := range tr.Records {
		if rec.Tick != nil && rec.Tick.Time.After(f.SimTime().Time().Add(-5*time.Minute)) {
			rec.Tick.Altitude += 100
			altered = i
			break
		}
	}
	r := replayTrace(t, tr)
	if r.divergence == nil || r.divergence.record != altered {
		t.Fatalf("divergence %+v, want one at record %d", r.divergence, altered)
	}
	if report := r.describe(tr); !strings.Contains(report, "altitude") || !strings.Contains(report, "ClearedApproach") {
		t.Errorf("report doesn't name the difference and the commands before it:\n%s", report)
	}

	blessed := filepath.Join(dir, "blessed"+navtrace.Ext)
	if err := r.save(tr, blessed); err != nil {
		t.Fatal(err)
	}
	tr, err = navtrace.Load(blessed)
	if err != nil {
		t.Fatal(err)
	}
	if r := replayTrace(t, tr); r.divergence != nil {
		t.Errorf("the blessed trace diverged:\n%s", r.describe(tr))
	}
}
