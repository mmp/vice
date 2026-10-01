// eram/track_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

type trackTestHarness struct {
	ep    *Scope
	ctx   *scope.Context
	start time.Time
}

func makeTrackTestHarness() *trackTestHarness {
	ep := &Scope{
		prefSet:                &PrefrenceSet{Current: *makeDefaultPreferences()},
		TrackState:             make(map[av.ADSBCallsign]*TrackState),
		AckedPointOuts:         make(map[sim.ACID][]sim.ControlPosition),
		CRRGroups:              make(map[string]*CRRGroup),
		aircraftFixCoordinates: make(map[sim.ACID]aircraftFixCoordinates),
	}
	ctx := &scope.Context{Client: &client.ControlClient{}}
	ctx.Client.State.Tracks = make(map[av.ADSBCallsign]*sim.Track)
	ctx.Client.State.UserTCW = "TCW1"
	ctx.Client.State.CurrentConsolidation = map[sim.TCW]*sim.TCPConsolidation{
		"TCW1": {PrimaryTCP: "1A"},
	}
	return &trackTestHarness{ep: ep, ctx: ctx, start: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
}

// frame runs the per-frame track processing that Scope.Draw does, at the
// given sim time offset.
func (h *trackTestHarness) frame(t time.Duration, events ...sim.Event) {
	h.ctx.Client.State.SimTime = sim.NewSimTime(h.start.Add(t))
	h.ctx.InterpolatedSimTime = h.ctx.Client.State.SimTime
	h.ctx.Events = events
	h.ep.processEvents(h.ctx)
	h.ep.updateRadarTracks(h.ctx)
	h.ep.updateVisibleTracks(h.ctx)
}

func (h *trackTestHarness) addTrack(trk *sim.Track) {
	h.ctx.Client.State.Tracks[trk.ADSBCallsign] = trk
}

func TestRadarSampleUpdates(t *testing.T) {
	h := makeTrackTestHarness()
	trk := &sim.Track{
		RadarTrack: av.RadarTrack{
			ADSBCallsign:        "AAL1",
			Mode:                av.TransponderModeAltitude,
			TransponderAltitude: 20000,
			Location:            math.Point2LL{-73, 40},
			Groundspeed:         400,
		},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1", AssignedAltitude: 30000},
	}
	h.addTrack(trk)
	vfr := &sim.Track{
		RadarTrack: av.RadarTrack{
			ADSBCallsign:        "N123AB",
			Mode:                av.TransponderModeAltitude,
			Squawk:              0o1200,
			TransponderAltitude: 5500,
			Location:            math.Point2LL{-73.5, 40},
		},
	}
	h.addTrack(vfr)

	// The first shared scan samples all existing tracks.
	h.frame(0)
	state := h.ep.TrackState["AAL1"]
	if state == nil || state.Track.TransponderAltitude != 20000 || state.TrackTime.IsZero() {
		t.Fatalf("track not sampled on the first shared scan: %+v", state)
	}
	if len(h.ep.visibleTracks) != 2 {
		t.Fatalf("expected 2 visible tracks, got %d", len(h.ep.visibleTracks))
	}
	firstFormat := h.ep.getAltitudeFormat(*trk)
	vfrSymbol := h.ep.getTarget(*vfr, h.ep.TrackState["N123AB"])

	// The aircraft moves, climbs, and idents, but nothing the scope shows
	// changes until the next radar sample.
	trk.Location = math.Point2LL{-72.9, 40.1}
	trk.TransponderAltitude = 21000
	vfr.Ident = true
	h.frame(eramUpdateInterval - time.Second)
	if state.Track.Location != (math.Point2LL{-73, 40}) || state.Track.TransponderAltitude != 20000 {
		t.Errorf("track sample changed before the update interval: %+v", state.Track)
	}
	if got := h.ep.getAltitudeFormat(*trk); got != firstFormat {
		t.Errorf("datablock altitude changed before the update interval: %q -> %q", firstFormat, got)
	}
	if got := h.ep.getTarget(*vfr, h.ep.TrackState["N123AB"]); got != vfrSymbol {
		t.Errorf("target symbol changed before the update interval: %q -> %q", vfrSymbol, got)
	}

	h.frame(eramUpdateInterval)
	if state.Track.TransponderAltitude != 21000 || state.PreviousTrack.TransponderAltitude != 20000 {
		t.Errorf("track not resampled at the update interval: %+v", state)
	}
	if got, want := h.ep.getAltitudeFormat(*trk), "300"+upArrow+"210"; got != want {
		t.Errorf("datablock altitude after update: got %q, want %q", got, want)
	}
	if got := h.ep.getTarget(*vfr, h.ep.TrackState["N123AB"]); got != "\u0006" {
		t.Errorf("ident target symbol after update: got %q", got)
	}

	// The entry before the newest history entry is the previous sample.
	n := len(state.HistoryTracks)
	if prev := state.HistoryTracks[(state.HistoryTrackIndex-2+n)%n]; prev.Location != (math.Point2LL{-73, 40}) {
		t.Errorf("history does not hold the previous sample: %+v", prev)
	}
}

func TestTrackStateRemoval(t *testing.T) {
	h := makeTrackTestHarness()
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 20000, Location: math.Point2LL{-73, 40}},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1"},
	})
	h.frame(0)
	h.ep.AckedPointOuts["AAL1"] = []sim.ControlPosition{"2B"}
	h.ep.CRRGroups["A"] = &CRRGroup{Label: "A", Aircraft: map[av.ADSBCallsign]struct{}{"AAL1": {}}}

	delete(h.ctx.Client.State.Tracks, "AAL1")
	// A handoff accepted for an aircraft that is already gone must not
	// fail.
	h.frame(time.Second, sim.Event{Type: sim.AcceptedHandoffEvent, ACID: "AAL1",
		FromController: "2B", ToController: "1A"})

	if _, ok := h.ep.TrackState["AAL1"]; ok {
		t.Errorf("track state not deleted for a departed aircraft")
	}
	if len(h.ep.AckedPointOuts) != 0 {
		t.Errorf("point outs not pruned: %v", h.ep.AckedPointOuts)
	}
	if len(h.ep.CRRGroups["A"].Aircraft) != 0 {
		t.Errorf("CRR membership not pruned: %v", h.ep.CRRGroups["A"].Aircraft)
	}
	if len(h.ep.visibleTracks) != 0 {
		t.Errorf("departed aircraft still visible")
	}
}

func TestRadarSamplesSynchronized(t *testing.T) {
	h := makeTrackTestHarness()
	a := &sim.Track{RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Location: math.Point2LL{-73, 40}, TransponderAltitude: 30000}}
	b := &sim.Track{RadarTrack: av.RadarTrack{ADSBCallsign: "DAL2", Location: math.Point2LL{-74, 40}, TransponderAltitude: 30000}}
	h.addTrack(a)
	h.frame(0)
	h.addTrack(b)
	h.frame(10 * time.Second)
	if state := h.ep.TrackState[b.ADSBCallsign]; !state.TrackTime.IsZero() || state.HistoryTrackIndex != 0 {
		t.Fatalf("new target sampled before the shared scan: %+v", state)
	}
	if len(h.ep.visibleTracks) != 1 || h.ep.visibleTracks[0].ADSBCallsign != a.ADSBCallsign {
		t.Fatalf("new target visible before the shared scan: %+v", h.ep.visibleTracks)
	}

	for _, offset := range []time.Duration{12, 22, 24} {
		a.Location[1] += 0.01
		b.Location[1] += 0.01
		h.frame(offset * time.Second)
		if len(h.ep.visibleTracks) != 2 {
			t.Errorf("at %ds: expected both tracks to be visible, got %d", offset, len(h.ep.visibleTracks))
		}
		want := sim.NewSimTime(h.start.Add(12 * time.Second))
		if offset == 24 {
			want = h.ctx.Client.State.SimTime
		}
		for _, trk := range []*sim.Track{a, b} {
			if state := h.ep.TrackState[trk.ADSBCallsign]; state.TrackTime != want {
				t.Errorf("at %ds, %s sample time = %v, want %v", offset, trk.ADSBCallsign, state.TrackTime, want)
			}
		}
	}
}

func TestConflictAlertsWithStaggeredArrivals(t *testing.T) {
	for _, tc := range []struct {
		name         string
		separation   float32
		leaderFirst  bool
		wantConflict bool
	}{
		{name: "no false conflict", separation: 5.5, leaderFirst: true},
		{name: "no missed conflict", separation: 4.5, wantConflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := makeTrackTestHarness()
			h.ctx.NmPerLongitude = 60
			h.ctx.Client.State.Controllers = map[sim.ControlPosition]*av.Controller{"1A": {}}
			makeTrack := func(callsign av.ADSBCallsign) *sim.Track {
				return &sim.Track{
					RadarTrack: av.RadarTrack{ADSBCallsign: callsign, Mode: av.TransponderModeAltitude,
						TransponderAltitude: 30000, Groundspeed: 480},
					FlightPlan: &sim.FlightPlan{ACID: sim.ACID(callsign), AssignedAltitude: 30000,
						TrackingController: "1A"},
				}
			}
			leader, follower := makeTrack("AAL1"), makeTrack("DAL2")
			first, second := follower, leader
			if tc.leaderFirst {
				first, second = leader, follower
			}
			h.addTrack(first)
			for seconds := 0; seconds <= 48; seconds++ {
				// Level aircraft fly north at 480 knots with constant separation.
				latitude := float32(40) + float32(seconds)*480/3600/60
				follower.Location = math.Point2LL{-73, latitude}
				leader.Location = math.Point2LL{-73, latitude + tc.separation/60}
				if seconds == 10 {
					h.addTrack(second)
				}
				h.frame(time.Duration(seconds) * time.Second)
				h.ep.updateConflictAlerts(h.ctx, h.ep.visibleTracks)
				// The new target is sampled at 12s and 24s, so both targets
				// have radar history by the detection pass at 25s.
				if seconds >= 25 && (len(h.ep.CAPairs) != 0) != tc.wantConflict {
					t.Errorf("at %ds: conflict = %v, want %v", seconds, h.ep.CAPairs, tc.wantConflict)
				}
			}
		})
	}
}

// headOnConflict flies two tracks toward each other at FL300 and 480
// knots, starting 40 nm apart, after configure sets them up, and reports
// whether conflict alert has flagged them once both have radar history.
// Flying straight, they would meet 150 seconds in.
func headOnConflict(configure func(northbound, southbound *sim.Track)) bool {
	h := makeTrackTestHarness()
	h.ctx.NmPerLongitude = 60
	h.ctx.Client.State.Controllers = map[sim.ControlPosition]*av.Controller{"1A": {}}
	makeTrack := func(callsign av.ADSBCallsign) *sim.Track {
		return &sim.Track{
			RadarTrack: av.RadarTrack{ADSBCallsign: callsign, Mode: av.TransponderModeAltitude,
				TransponderAltitude: 30000, Groundspeed: 480},
			FlightPlan: &sim.FlightPlan{ACID: sim.ACID(callsign), AssignedAltitude: 30000,
				TrackingController: "1A"},
		}
	}
	northbound, southbound := makeTrack("AAL1"), makeTrack("DAL2")
	configure(northbound, southbound)
	h.addTrack(northbound)
	h.addTrack(southbound)

	for seconds := 0; seconds <= 25; seconds++ {
		flown := float32(seconds) * 480 / 3600 / 60 // degrees of latitude
		northbound.Location = math.Point2LL{-73, 40 + flown}
		southbound.Location = math.Point2LL{-73, 40 + 40.0/60 - flown}
		h.frame(time.Duration(seconds) * time.Second)
		h.ep.updateConflictAlerts(h.ctx, h.ep.visibleTracks)
	}
	return len(h.ep.CAPairs) != 0
}

func TestConflictAlertsFollowRoutes(t *testing.T) {
	// The northbound aircraft's route turns east 10 nm ahead; the two then
	// come no closer than 14 nm.
	turnEast := []math.Point2LL{{-73, 40 + 10.0/60}, {-73 + 30.0/60, 40 + 10.0/60}}
	if !headOnConflict(func(n, s *sim.Track) {}) {
		t.Error("head-on tracks: want conflict")
	}
	if headOnConflict(func(n, s *sim.Track) { n.Route = turnEast }) {
		t.Error("route turns away before they meet: want no conflict")
	}
	if !headOnConflict(func(n, s *sim.Track) { n.Route, n.AssignedHeading = turnEast, 360 }) {
		t.Error("on an assigned heading rather than its route: want conflict")
	}
}

func TestConflictAlertsSkipVirtualPairs(t *testing.T) {
	if headOnConflict(func(n, s *sim.Track) { n.VirtuallyControlled, s.VirtuallyControlled = true, true }) {
		t.Error("both tracks worked by virtual controllers: want no conflict")
	}
	if !headOnConflict(func(n, s *sim.Track) { n.VirtuallyControlled = true }) {
		t.Error("one track worked by a human: want conflict")
	}
}

func TestAcceptedHandoffUsesACID(t *testing.T) {
	h := makeTrackTestHarness()
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "N123AB", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 8000, Location: math.Point2LL{-73, 40}},
		FlightPlan: &sim.FlightPlan{ACID: "LIFEGUARD1"},
	})
	h.frame(0, sim.Event{Type: sim.AcceptedHandoffEvent, ACID: "LIFEGUARD1",
		FromController: "2B", ToController: "1A"})
	if !h.ep.TrackState["N123AB"].EFDB {
		t.Errorf("accepted handoff did not force a full datablock for an ACID that differs from the callsign")
	}
}

// A pending point out is shown from the flight plan alone, so a scope that
// was not signed on when it was made--during prespawn, say--still shows it,
// and the receiver's FDB stays after it is acknowledged until QP <FLID>.
// Once the user's point out is acknowledged, its indicator stays as a white
// "A" until dismissed.
func TestPointOutIndicator(t *testing.T) {
	h := makeTrackTestHarness()
	inbound := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "AAL1", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 20000, Location: math.Point2LL{-73, 40}},
		FlightPlan: &sim.FlightPlan{ACID: "AAL1", OwningTCW: "TCW2",
			PointOuts: []sim.PointOut{{FromController: "2B", ToController: "1A"}}},
	}
	outbound := &sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "UAL2", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 20000, Location: math.Point2LL{-73.5, 40}},
		FlightPlan: &sim.FlightPlan{ACID: "UAL2",
			PointOuts: []sim.PointOut{{FromController: "1A", ToController: "2B"}}},
	}
	h.addTrack(inbound)
	h.addTrack(outbound)
	h.frame(0)

	glyph := func(trk *sim.Track) rune {
		ch, _, _ := h.ep.pointOutIndicatorGlyph(h.ctx, trk, 100)
		return ch
	}

	if g := glyph(inbound); g != 'P' {
		t.Errorf("inbound point out: got indicator %q, want 'P'", g)
	}
	if dt := h.ep.datablockType(h.ctx, *inbound); dt != FullDatablock {
		t.Errorf("inbound point out: got datablock type %v, want a full datablock", dt)
	}
	if g := glyph(outbound); g != 'P' {
		t.Errorf("outbound point out: got indicator %q, want 'P'", g)
	}
	if _, err := h.ep.clearPointOutLock(h.ctx, inbound); err == nil {
		t.Errorf("QP cleared the FDB lock of a pending point out")
	}

	h.ep.handlePointOutIndicatorClick(h.ctx, *outbound, math.Extent2D{})
	if h.ep.popup == nil {
		t.Errorf("clicking a pending outbound point out did not open the pop-up")
	}
	h.ep.popup = nil

	inbound.FlightPlan.PointOuts = nil
	outbound.FlightPlan.PointOuts = nil
	h.frame(time.Second, sim.Event{Type: sim.AcknowledgedPointOutEvent, ACID: "UAL2",
		FromController: "2B", ToController: "1A"})
	if g := glyph(outbound); g != 'A' {
		t.Errorf("acknowledged point out: got indicator %q, want 'A'", g)
	}

	if dt := h.ep.datablockType(h.ctx, *inbound); dt != FullDatablock {
		t.Errorf("acknowledged inbound point out: got datablock type %v, want a full datablock", dt)
	}
	if _, err := h.ep.clearPointOutLock(h.ctx, inbound); err != nil {
		t.Errorf("QP of an acknowledged point out: %v", err)
	}
	if dt := h.ep.datablockType(h.ctx, *inbound); dt != LimitedDatablock {
		t.Errorf("after QP: got datablock type %v, want a limited datablock", dt)
	}

	h.ep.handlePointOutIndicatorClick(h.ctx, *outbound, math.Extent2D{})
	if h.ep.pointOutIndicatorActive(h.ctx, outbound) {
		t.Errorf("dismissed acknowledgment is still shown")
	}
}

func TestDatablockDirection(t *testing.T) {
	ep := &Scope{prefSet: &PrefrenceSet{Current: *makeDefaultPreferences()}}
	pos := func(d math.CardinalOrdinalDirection) *math.CardinalOrdinalDirection { return &d }
	noLeader := 0

	for _, c := range []struct {
		name   string
		state  TrackState
		dbType DatablockType
		want   math.CardinalOrdinalDirection
	}{
		{"default FDB", TrackState{}, FullDatablock, math.NorthEast},
		{"default LDB", TrackState{}, LimitedDatablock, math.East},
		{"LDB positioned west", TrackState{LeaderLineDirection: pos(math.West)}, LimitedDatablock, math.West},
		{"E-LDB positioned west", TrackState{LeaderLineDirection: pos(math.West)}, EnhancedLimitedDatablock, math.West},
		{"LDB of a northwest FDB", TrackState{LeaderLineDirection: pos(math.NorthWest)}, LimitedDatablock, math.West},
		{"LDB of a southwest FDB", TrackState{LeaderLineDirection: pos(math.SouthWest)}, LimitedDatablock, math.West},
		{"LDB of a north FDB", TrackState{LeaderLineDirection: pos(math.North)}, LimitedDatablock, math.East},
		{"FDB positioned northwest", TrackState{LeaderLineDirection: pos(math.NorthWest)}, FullDatablock, math.NorthWest},
		{"northwest FDB without a leader", TrackState{LeaderLineDirection: pos(math.NorthWest), LeaderLineLength: &noLeader},
			FullDatablock, math.West},
		{"northeast FDB without a leader", TrackState{LeaderLineDirection: pos(math.NorthEast), LeaderLineLength: &noLeader},
			FullDatablock, math.East},
	} {
		if got := ep.datablockDirection(&c.state, c.dbType); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFDBLeaderLengthAppliesToAllTracks(t *testing.T) {
	individual := 3
	ep := &Scope{
		prefSet: &PrefrenceSet{Current: *makeDefaultPreferences()},
		TrackState: map[av.ADSBCallsign]*TrackState{
			"AAL1": {},
			"UAL2": {LeaderLineLength: &individual},
		},
	}

	want := ep.currentPrefs().FDBLdrLength + 1
	if err := clickFDBLeader(ep, tertiaryClick()); err != nil {
		t.Fatal(err)
	}
	for callsign, state := range ep.TrackState {
		if got := ep.leaderLineLength(state); got != want {
			t.Errorf("%s: got leader line length %d, want %d", callsign, got, want)
		}
	}
}

func TestPositionUnpairedLDB(t *testing.T) {
	InitCommands()
	ep := &Scope{
		prefSet:    &PrefrenceSet{Current: *makeDefaultPreferences()},
		TrackState: map[av.ADSBCallsign]*TrackState{"N810FR": {}},
	}
	ctx := &scope.Context{Client: &client.ControlClient{}}
	ctx.Client.State.Tracks = map[av.ADSBCallsign]*sim.Track{
		"N810FR": {RadarTrack: av.RadarTrack{ADSBCallsign: "N810FR", Squawk: 0o1200}},
	}
	click := func(cmd string) error {
		_, err, _ := ep.tryExecuteUserCommand(ctx, cmd+" "+locationSymbol, []math.Point2LL{{}}, []av.ADSBCallsign{"N810FR"})
		return err
	}

	if err := click("4"); err != nil {
		t.Fatalf("4: %v", err)
	}
	if dir := ep.datablockDirection(ep.TrackState["N810FR"], LimitedDatablock); dir != math.West {
		t.Errorf("after 4: LDB positioned %v, want West", dir)
	}
	if err := click("7"); err != ErrIllegalValue {
		t.Errorf("7: got %v, want %v", err, ErrIllegalValue)
	}
}
