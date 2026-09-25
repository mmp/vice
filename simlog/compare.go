// simlog/compare.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package simlog

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// Tolerances are how far a replayed aircraft may be from where it was in the
// session before it counts as flying differently.
type Tolerances struct {
	Position float32 // nautical miles
	Altitude float32 // feet
	Speed    float32 // knots, indicated and ground speed alike
	Heading  float32 // degrees
}

// Report is what Compare found.
type Report struct {
	Header Header
	// Aircraft that flew differently in the replay, in the order they
	// started to.
	Aircraft []AircraftReport
	// Requests the replay's sim answered differently than the session's
	// did that were about no aircraft in particular.
	Requests []RequestOutcome
	// How many aircraft flew in either, and the ticks each ran.
	Compared                  int
	SessionTicks, ReplayTicks int
}

// AircraftReport describes an aircraft that flew differently in the replay.
type AircraftReport struct {
	Callsign string
	Spawn    Spawn // as the session recorded it, or the replay if it didn't

	// What became of it in each: how it left the sim and when, or "" if it
	// was still flying when the log ended. SessionOnly and ReplayOnly are
	// set if only one of them had the aircraft at all.
	SessionEnd, ReplayEnd   string
	SessionOnly, ReplayOnly bool

	// The requests the controllers made about it, with how each fared.
	Requests []RequestOutcome

	// Where it first flew differently, if it did while in both.
	Divergence *Divergence
}

// RequestOutcome is a request from the session's log with the sim's answer
// to it in the session and in the replay.
type RequestOutcome struct {
	Time          time.Time
	TCW           string
	Summary       string
	SessionError  string
	ReplayError   string
	ReplayMissing bool // the replay ended before it made the request
}

// Differs reports whether the replay's sim answered the request differently.
func (o RequestOutcome) Differs() bool {
	return o.ReplayMissing || o.SessionError != o.ReplayError
}

// Divergence is where an aircraft first flew differently: the samples of it
// at that tick in the session and in the replay, one of which is nil if it
// was flying in only one of them.
type Divergence struct {
	Time            time.Time
	Session, Replay *Sample
}

type track struct {
	spawn   *Spawn
	delete  *Delete
	samples map[int64]Sample // keyed by UnixNano
	times   []time.Time
}

func tracks(s *Session) map[string]*track {
	m := make(map[string]*track)
	get := func(callsign string) *track {
		if t, ok := m[callsign]; ok {
			return t
		}
		t := &track{samples: make(map[int64]Sample)}
		m[callsign] = t
		return t
	}

	for _, e := range s.Events {
		switch e.Kind {
		case KindSpawn:
			get(e.Spawn.Callsign).spawn = e.Spawn
		case KindDelete:
			get(e.Delete.Callsign).delete = e.Delete
		case KindTick:
			for _, sample := range e.Tick.Aircraft {
				t := get(sample.Callsign)
				t.samples[e.Tick.Time.UnixNano()] = sample
				t.times = append(t.times, e.Tick.Time)
			}
		}
	}
	return m
}

func requests(s *Session) []*Request {
	var r []*Request
	for _, e := range s.Events {
		if e.Kind == KindRequest {
			r = append(r, e.Request)
		}
	}
	return r
}

func countTicks(s *Session) int {
	n := 0
	for _, e := range s.Events {
		if e.Kind == KindTick {
			n++
		}
	}
	return n
}

// Compare compares the flights in a session's log with those in the log of
// its replay, reporting the aircraft that flew differently in the replay by
// more than tol allows.
func Compare(session, replay *Session, tol Tolerances) Report {
	r := Report{
		Header:       session.Header,
		SessionTicks: countTicks(session),
		ReplayTicks:  countTicks(replay),
	}

	// The replay makes each of the session's requests in turn.
	sessionRequests, replayRequests := requests(session), requests(replay)
	outcomes := make(map[string][]RequestOutcome)
	for i, sr := range sessionRequests {
		o := RequestOutcome{Time: sr.Time, TCW: sr.TCW, Summary: sr.Summary, SessionError: sr.Error}
		if i < len(replayRequests) {
			o.ReplayError = replayRequests[i].Error
		} else {
			o.ReplayMissing = true
		}
		if sr.Aircraft != "" {
			outcomes[sr.Aircraft] = append(outcomes[sr.Aircraft], o)
		} else if o.Differs() {
			r.Requests = append(r.Requests, o)
		}
	}

	st, rt := tracks(session), tracks(replay)
	callsigns := util.SortedMapKeys(st)
	for cs := range rt {
		if _, ok := st[cs]; !ok {
			callsigns = append(callsigns, cs)
		}
	}
	slices.Sort(callsigns)
	r.Compared = len(callsigns)

	for _, cs := range callsigns {
		if ar, ok := compareAircraft(cs, st[cs], rt[cs], outcomes[cs], tol); ok {
			r.Aircraft = append(r.Aircraft, ar)
		}
		delete(outcomes, cs)
	}
	// Requests about aircraft that never flew in either, like flight plans
	// that never associated, are reported with the rest.
	for _, o := range util.SortedMap(outcomes) {
		r.Requests = append(r.Requests, util.FilterSlice(o, RequestOutcome.Differs)...)
	}
	slices.SortStableFunc(r.Requests, func(a, b RequestOutcome) int { return a.Time.Compare(b.Time) })

	slices.SortStableFunc(r.Aircraft, func(a, b AircraftReport) int {
		return a.firstDifference().Compare(b.firstDifference())
	})
	return r
}

func compareAircraft(callsign string, s, r *track, outcomes []RequestOutcome, tol Tolerances) (AircraftReport, bool) {
	ar := AircraftReport{
		Callsign:    callsign,
		Requests:    outcomes,
		SessionOnly: r == nil,
		ReplayOnly:  s == nil,
	}
	// An aircraft only one of them had diverges where it first flew.
	if s == nil {
		s = &track{}
	}
	if r == nil {
		r = &track{}
	}
	ar.SessionEnd, ar.ReplayEnd = s.end(), r.end()
	if s.spawn != nil {
		ar.Spawn = *s.spawn
	} else if r.spawn != nil {
		ar.Spawn = *r.spawn
	}

	// Walk the ticks either one flew at, in order.
	times := slices.Concat(s.times, r.times)
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	times = slices.CompactFunc(times, func(a, b time.Time) bool { return a.Equal(b) })
	for _, t := range times {
		ss, sok := s.samples[t.UnixNano()]
		rs, rok := r.samples[t.UnixNano()]
		if sok && rok && !exceeds(ss, rs, tol) {
			continue
		}
		ar.Divergence = &Divergence{Time: t}
		if sok {
			ar.Divergence.Session = &ss
		}
		if rok {
			ar.Divergence.Replay = &rs
		}
		break
	}

	differs := ar.SessionOnly || ar.ReplayOnly || ar.Divergence != nil || ar.SessionEnd != ar.ReplayEnd ||
		slices.ContainsFunc(outcomes, RequestOutcome.Differs)
	return ar, differs
}

func exceeds(a, b Sample, tol Tolerances) bool {
	// The distance between two identical points may not come out exactly
	// zero: where the hardware has fused multiply-add, Go may compute one
	// product rounded and the other not.
	return (a.Position != b.Position && math.NMDistance2LL(a.Position, b.Position) > tol.Position) ||
		math.Abs(a.Altitude-b.Altitude) > tol.Altitude ||
		math.Abs(a.IAS-b.IAS) > tol.Speed ||
		math.Abs(a.GS-b.GS) > tol.Speed ||
		math.HeadingDifference(a.Heading, b.Heading) > tol.Heading
}

// end describes how the aircraft left the sim, or returns "" if it was still
// flying when the log ended.
func (t *track) end() string {
	if t.delete == nil {
		return ""
	}
	return t.delete.Reason + " at " + clock(t.delete.Time)
}

// firstDifference returns when the replay first differed for the aircraft:
// where it diverged or the first request answered differently, whichever
// came first. An aircraft only one of them had sorts by when it spawned.
func (ar AircraftReport) firstDifference() time.Time {
	var first time.Time
	consider := func(t time.Time) {
		if first.IsZero() || t.Before(first) {
			first = t
		}
	}
	if ar.Divergence != nil {
		consider(ar.Divergence.Time)
	}
	for _, o := range ar.Requests {
		if o.Differs() {
			consider(o.Time)
		}
	}
	if first.IsZero() {
		first = ar.Spawn.Time
	}
	return first
}

func clock(t time.Time) string {
	return t.UTC().Format("15:04:05")
}

///////////////////////////////////////////////////////////////////////////
// Writing the report

// Diverged reports whether the replay differed from the session at all.
func (r Report) Diverged() bool {
	return len(r.Aircraft) > 0 || len(r.Requests) > 0 || r.SessionTicks != r.ReplayTicks
}

// Write writes the report for someone to read.
func (r Report) Write(w io.Writer) {
	h := r.Header
	fmt.Fprintf(w, "%s %s (%s), recorded %s\n", h.Facility, h.Scenario, h.ScenarioGroup,
		h.Start.UTC().Format("2006-01-02 15:04Z"))

	for _, ar := range r.Aircraft {
		ar.write(w, h.SimStart)
	}

	if len(r.Requests) > 0 {
		fmt.Fprintln(w, "Requests answered differently:")
		for _, o := range r.Requests {
			fmt.Fprintln(w, "  "+o.String())
		}
	}

	if r.SessionTicks != r.ReplayTicks {
		fmt.Fprintf(w, "The session ran %d ticks but the replay ran %d.\n", r.SessionTicks, r.ReplayTicks)
	}
	fmt.Fprintf(w, "%d of %d aircraft flew differently over %d ticks.\n", len(r.Aircraft), r.Compared,
		r.SessionTicks)
}

func (ar AircraftReport) write(w io.Writer, simStart time.Time) {
	sp := ar.Spawn
	spawned, since := "spawned "+clock(sp.Time), "after it spawned"
	if !sp.Time.After(simStart) {
		spawned, since = "in the sim when the session started", "into the session"
	}
	fmt.Fprintf(w, "%s %s %s-%s %s %s, %s\n", ar.Callsign, sp.AircraftType, sp.Departure, sp.Arrival,
		sp.Rules, sp.Flight, spawned)

	switch {
	case ar.SessionOnly:
		fmt.Fprintf(w, "  never spawned in the replay; in the session: %s\n", endString(ar.SessionEnd))
	case ar.ReplayOnly:
		fmt.Fprintf(w, "  spawned only in the replay; there: %s\n", endString(ar.ReplayEnd))
	case ar.SessionEnd != ar.ReplayEnd:
		fmt.Fprintf(w, "  session: %s; replay: %s\n", endString(ar.SessionEnd), endString(ar.ReplayEnd))
	}

	for _, o := range ar.Requests {
		fmt.Fprintln(w, "  "+o.String())
	}

	if d := ar.Divergence; d != nil {
		fmt.Fprintf(w, "  diverged at %s, %s %s: %s\n", clock(d.Time), durationString(d.Time.Sub(sp.Time)),
			since, d.describe())
	}
}

func (o RequestOutcome) String() string {
	s := fmt.Sprintf("%s %-5s %s", clock(o.Time), o.TCW, o.Summary)
	switch {
	case o.ReplayMissing:
		s += " (not replayed: the replay ended first)"
	case o.SessionError == o.ReplayError && o.SessionError != "":
		s += " (refused: " + o.SessionError + ")"
	case o.SessionError == o.ReplayError:
	case o.ReplayError == "":
		s += " (refused in the session: " + o.SessionError + "; accepted in the replay)"
	case o.SessionError == "":
		s += " (accepted in the session; refused in the replay: " + o.ReplayError + ")"
	default:
		s += " (refused in the session: " + o.SessionError + "; in the replay: " + o.ReplayError + ")"
	}
	return s
}

func (d Divergence) describe() string {
	switch {
	case d.Replay == nil:
		return "flying in the session but not in the replay; session at " + where(d.Session.Position)
	case d.Session == nil:
		return "flying in the replay but not in the session; replay at " + where(d.Replay.Position)
	}

	s, r := d.Session, d.Replay
	var diffs []string
	if s.Position != r.Position {
		dist := math.NMDistance2LL(s.Position, r.Position)
		diffs = append(diffs, strconv.FormatFloat(float64(dist), 'g', 2, 32)+" nm apart")
	}
	pair := func(what string, a, b float32) {
		if a != b {
			sa, sb := distinguish(a, b)
			diffs = append(diffs, what+" "+sa+"/"+sb)
		}
	}
	pair("altitude", s.Altitude, r.Altitude)
	pair("IAS", s.IAS, r.IAS)
	pair("GS", s.GS, r.GS)
	pair("heading", s.Heading, r.Heading)
	return strings.Join(diffs, ", ") + " (session/replay); session at " + where(s.Position)
}

// distinguish formats two different values with as few decimal places as
// shows them to be different, so that a small difference doesn't print as
// two equal numbers and a large one isn't buried in digits.
func distinguish(a, b float32) (string, string) {
	for prec := 0; ; prec++ {
		sa := strconv.FormatFloat(float64(a), 'f', prec, 32)
		sb := strconv.FormatFloat(float64(b), 'f', prec, 32)
		if sa != sb || prec == 9 {
			return sa, sb
		}
	}
}

// where describes a position by the nearest fix or navaid, if one is within
// a few miles.
func where(p math.Point2LL) string {
	s := p.DDString()
	if db.DB == nil {
		return s
	}

	const maxDistance = 10
	nearest, nearestDistance := "", float32(maxDistance)
	var nearestLocation math.Point2LL
	consider := func(name string, loc math.Point2LL) {
		// Ties go to the name that sorts first, so the report doesn't
		// depend on map order.
		if d := math.NMDistance2LL(p, loc); d < nearestDistance || (d == nearestDistance && name < nearest) {
			nearest, nearestDistance, nearestLocation = name, d, loc
		}
	}
	for name, fix := range db.DB.Fixes {
		consider(name, fix.Location)
	}
	for name, navaid := range db.DB.Navaids {
		consider(name, navaid.Location)
	}
	if nearest == "" {
		return s
	}
	bearing := math.Heading2LL(nearestLocation, p, math.NMPerLongitudeAt(p))
	return fmt.Sprintf("%s (%.1f nm %s of %s)", s, nearestDistance, math.ShortCompass(bearing), nearest)
}

func endString(end string) string {
	return util.Select(end == "", "still flying at the end", end)
}

func durationString(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}
