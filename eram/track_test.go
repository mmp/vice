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
		InboundPointOuts:       make(map[sim.ACID][]sim.ControlPosition),
		OutboundPointOuts:      make(map[sim.ACID][]outboundPointOut),
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
		FlightPlan: &sim.NASFlightPlan{ACID: "AAL1", AssignedAltitude: 30000},
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
		FlightPlan: &sim.NASFlightPlan{ACID: "AAL1"},
	})
	h.frame(0)
	h.ep.InboundPointOuts["AAL1"] = []sim.ControlPosition{"2B"}
	h.ep.OutboundPointOuts["AAL1"] = []outboundPointOut{{Receiver: "2B"}}
	h.ep.CRRGroups["A"] = &CRRGroup{Label: "A", Aircraft: map[av.ADSBCallsign]struct{}{"AAL1": {}}}

	delete(h.ctx.Client.State.Tracks, "AAL1")
	// A handoff accepted for an aircraft that is already gone must not
	// fail.
	h.frame(time.Second, sim.Event{Type: sim.AcceptedHandoffEvent, ACID: "AAL1",
		FromController: "2B", ToController: "1A"})

	if _, ok := h.ep.TrackState["AAL1"]; ok {
		t.Errorf("track state not deleted for a departed aircraft")
	}
	if len(h.ep.InboundPointOuts) != 0 || len(h.ep.OutboundPointOuts) != 0 {
		t.Errorf("point outs not pruned: %v %v", h.ep.InboundPointOuts, h.ep.OutboundPointOuts)
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
					FlightPlan: &sim.NASFlightPlan{ACID: sim.ACID(callsign), AssignedAltitude: 30000,
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

func TestAcceptedHandoffUsesACID(t *testing.T) {
	h := makeTrackTestHarness()
	h.addTrack(&sim.Track{
		RadarTrack: av.RadarTrack{ADSBCallsign: "N123AB", Mode: av.TransponderModeAltitude,
			TransponderAltitude: 8000, Location: math.Point2LL{-73, 40}},
		FlightPlan: &sim.NASFlightPlan{ACID: "LIFEGUARD1"},
	})
	h.frame(0, sim.Event{Type: sim.AcceptedHandoffEvent, ACID: "LIFEGUARD1",
		FromController: "2B", ToController: "1A"})
	if !h.ep.TrackState["N123AB"].EFDB {
		t.Errorf("accepted handoff did not force a full datablock for an ACID that differs from the callsign")
	}
}
