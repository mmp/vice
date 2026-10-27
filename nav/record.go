// nav/record.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

// A Recorder is told what happens to a Nav so that its flight can be replayed
// and compared later: every command it is given, what each update was given
// and what came of it, and every answer it gets from its Lookups. A Nav
// without one records nothing.
type Recorder interface {
	// Start is called once, before anything else is recorded, with the Nav
	// as it is then: the state the recorded flight starts from.
	Start(nav *Nav)
	// Command records a command method being called, with its arguments in
	// the order the method declares them.
	Command(name string, args ...any)
	// Lookup records the answer to a question asked of the Lookups: kind
	// names the question, like "waypoint", and key what it was asked about.
	Lookup(kind, key string, result any)
	// Update records one update of the aircraft.
	Update(u UpdateRecord)
}

// UpdateRecord is what one update of a Nav was given and what came of it.
type UpdateRecord struct {
	Time       Time
	Weather    wx.Sample
	METAR      *wx.METAR
	Rules      av.FlightRules
	UnderBravo bool
	Prespawn   bool
	Result     UpdateResult
	State      FlightState
}

// Lookups answers what nav asks about the world beyond the aircraft and its
// route. The sim answers from the database; a replayed trace answers from
// what the recording was told.
type Lookups interface {
	// Waypoint returns the location of a named fix or navaid.
	Waypoint(fix string) (math.Point2LL, bool)
	// Airport returns the location and elevation of an airport named by
	// its ICAO or FAA code.
	Airport(code string) (math.Point2LL, float32, bool)
	// Declination returns a VHF navaid's station declination.
	Declination(fix string) (float32, bool)
	Airways(name string) ([]av.Airway, bool)
	// Holds returns the holds published at a fix.
	Holds(fix string) ([]av.Hold, bool)
	Runway(airport av.ICAOAirportCode, runway string) (av.Runway, bool)
	OppositeRunway(airport av.ICAOAirportCode, runway string) (av.Runway, bool)
}

// LookupResult is how a recorded answer from the Lookups is encoded: what
// the method returned and whether it found anything.
type LookupResult[T any] struct {
	Value T
	OK    bool
}

// AirportLookup is the value of a recorded Airport answer.
type AirportLookup struct {
	Location  math.Point2LL
	Elevation float32
}

// SetRecorder sets the recorder the Nav reports to; nil stops it reporting.
func (nav *Nav) SetRecorder(r Recorder) {
	nav.recorder = r
	nav.recordStarted = false
}

// recording returns the recorder to report to, starting it with the Nav as
// it is now if this is the first report, or nil if the Nav isn't recording.
func (nav *Nav) recording() Recorder {
	if nav.recorder != nil && !nav.recordStarted {
		nav.recordStarted = true
		nav.recorder.Start(nav)
	}
	return nav.recorder
}

// SetLookups sets where the Nav's questions about the world are answered;
// nil restores the database.
func (nav *Nav) SetLookups(l Lookups) {
	nav.lookups = l
}

// record reports a command to the recorder, if there is one, and returns the
// function that marks the command's end. A command that another command or
// an update gives isn't recorded: replaying the outer one gives it again.
// Every command method starts with "defer nav.record(...)()", naming itself
// and all of its parameters, which TestCommandsRecorded checks.
func (nav *Nav) record(name string, args ...any) func() {
	rec := nav.recording()
	if rec == nil {
		return func() {}
	}
	if nav.recordDepth == 0 {
		rec.Command(name, args...)
	}
	nav.recordDepth++
	return func() { nav.recordDepth-- }
}

// lookup returns where the Nav's questions are answered: its Lookups, or the
// database if it has none, with every answer recorded if it is recording.
func (nav *Nav) lookup() Lookups {
	l := nav.lookups
	if l == nil {
		l = databaseLookups{}
	}
	if rec := nav.recording(); rec != nil {
		return recordedLookups{l, rec}
	}
	return l
}

// databaseLookups answers from the static database.
type databaseLookups struct{}

func (databaseLookups) Waypoint(fix string) (math.Point2LL, bool) {
	return db.DB.LookupWaypoint(fix)
}

func (databaseLookups) Airport(code string) (math.Point2LL, float32, bool) {
	if ap, ok := db.DB.LookupICAOAirport(av.ICAOAirportCode(code)); ok {
		return ap.Location, float32(ap.Elevation), true
	} else if ap, ok := db.DB.LookupFAAAirport(av.FAAAirportCode(code)); ok {
		return ap.Location, float32(ap.Elevation), true
	}
	return math.Point2LL{}, 0, false
}

func (databaseLookups) Declination(fix string) (float32, bool) {
	return db.DB.Declination(fix)
}

func (databaseLookups) Airways(name string) ([]av.Airway, bool) {
	a, ok := db.DB.Airways[name]
	return a, ok
}

func (databaseLookups) Holds(fix string) ([]av.Hold, bool) {
	h, ok := db.DB.EnrouteHolds[fix]
	return h, ok
}

func (databaseLookups) Runway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	return av.LookupRunway(db.Lookups{}, airport, runway)
}

func (databaseLookups) OppositeRunway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	return av.LookupOppositeRunway(db.Lookups{}, airport, runway)
}

// recordedLookups answers from another Lookups and records each answer.
type recordedLookups struct {
	l   Lookups
	rec Recorder
}

func (r recordedLookups) Waypoint(fix string) (math.Point2LL, bool) {
	p, ok := r.l.Waypoint(fix)
	r.rec.Lookup("waypoint", fix, LookupResult[math.Point2LL]{p, ok})
	return p, ok
}

func (r recordedLookups) Airport(code string) (math.Point2LL, float32, bool) {
	p, elev, ok := r.l.Airport(code)
	r.rec.Lookup("airport", code, LookupResult[AirportLookup]{AirportLookup{p, elev}, ok})
	return p, elev, ok
}

func (r recordedLookups) Declination(fix string) (float32, bool) {
	d, ok := r.l.Declination(fix)
	r.rec.Lookup("declination", fix, LookupResult[float32]{d, ok})
	return d, ok
}

func (r recordedLookups) Airways(name string) ([]av.Airway, bool) {
	a, ok := r.l.Airways(name)
	r.rec.Lookup("airways", name, LookupResult[[]av.Airway]{a, ok})
	return a, ok
}

func (r recordedLookups) Holds(fix string) ([]av.Hold, bool) {
	h, ok := r.l.Holds(fix)
	r.rec.Lookup("holds", fix, LookupResult[[]av.Hold]{h, ok})
	return h, ok
}

func (r recordedLookups) Runway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	rwy, ok := r.l.Runway(airport, runway)
	r.rec.Lookup("runway", string(airport)+"/"+runway, LookupResult[av.Runway]{rwy, ok})
	return rwy, ok
}

func (r recordedLookups) OppositeRunway(airport av.ICAOAirportCode, runway string) (av.Runway, bool) {
	rwy, ok := r.l.OppositeRunway(airport, runway)
	r.rec.Lookup("opposite_runway", string(airport)+"/"+runway, LookupResult[av.Runway]{rwy, ok})
	return rwy, ok
}
