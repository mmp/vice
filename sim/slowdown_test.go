// sim/slowdown_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	vrand "github.com/mmp/vice/rand"
)

func TestArrivalSpeedGate(t *testing.T) {
	cases := []struct {
		dist     float32
		wantGate float32
		wantOK   bool
	}{
		{25, 0, false},
		{20, 250, true},
		{19, 250, true},
		{18, 250, true},
		{17, 210, true},
		{14, 210, true},
		{13, 190, true},
		{8, 190, true},
		{7, 180, true},
		{2, 180, true},
	}
	for _, c := range cases {
		gate, ok := arrivalSpeedGate(c.dist)
		if ok != c.wantOK || (ok && gate != c.wantGate) {
			t.Errorf("arrivalSpeedGate(%.0f) = %v, %v; want %v, %v", c.dist, gate, ok, c.wantGate, c.wantOK)
		}
	}
}

// slowDownTestAircraft builds an associated arrival on a lateral route whose
// single waypoint is distNM ahead, so SlowDownDistanceNM returns ~distNM.
func slowDownTestAircraft(distNM float32, assigned *av.SpeedRestriction) *Aircraft {
	const nmPerLong = 60
	ac := &Aircraft{
		ADSBCallsign:        "AAL123",
		TypeOfFlight:        av.FlightTypeArrival,
		ControllerFrequency: "BOS_APP",
		NASFlightPlan:       &NASFlightPlan{},
	}
	ac.Nav.FlightState = nav.FlightState{
		Position:       math.NM2LL([2]float32{0, 0}, nmPerLong),
		NmPerLongitude: nmPerLong,
	}
	ac.Nav.Waypoints = av.WaypointArray{{Fix: "RWY", Location: math.NM2LL([2]float32{0, distNM}, nmPerLong)}}
	ac.Nav.Speed.Assigned = assigned
	return ac
}

func slowDownRequestCount(s *Sim, callsign av.ADSBCallsign) int {
	n := 0
	for _, pcs := range s.PendingContacts {
		for _, pc := range pcs {
			if pc.ADSBCallsign == callsign && pc.Type == PendingTransmissionRequestSlowDown {
				n++
			}
		}
	}
	return n
}

func at(speed float32) *av.SpeedRestriction {
	r := av.MakeAtSpeedRestriction(speed)
	return &r
}

func TestCheckSlowDownRequest(t *testing.T) {
	t.Run("fast and close asks once per band", func(t *testing.T) {
		s := &Sim{}
		ac := slowDownTestAircraft(12, at(250)) // 12nm -> 190kt gate, assigned 250
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 1 {
			t.Fatalf("after first check: %d requests, want 1", got)
		}
		if ac.SlowDownAskedGate != 190 {
			t.Fatalf("SlowDownAskedGate = %.0f, want 190", ac.SlowDownAskedGate)
		}
		// Same band: no repeat.
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 1 {
			t.Fatalf("after second check in same band: %d requests, want 1", got)
		}
	})

	t.Run("re-asks on crossing into a closer band once the prior request clears", func(t *testing.T) {
		s := &Sim{}
		ac := slowDownTestAircraft(12, at(250))
		s.checkSlowDownRequest(ac) // 190 band
		// The controller pops/answers the first request.
		s.PendingContacts = nil
		// Move inside 8nm -> 180kt gate; the closer band re-asks.
		ac.Nav.Waypoints[0].Location = math.NM2LL([2]float32{0, 6}, 60)
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 1 {
			t.Fatalf("after crossing into closer band: %d requests, want 1", got)
		}
	})

	t.Run("re-arms on a new speed assignment within the same band", func(t *testing.T) {
		s := &Sim{}
		ac := slowDownTestAircraft(12, at(250)) // 190 band
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 1 {
			t.Fatalf("after first check: %d requests, want 1", got)
		}
		// Controller acknowledges (the queued request is delivered) and re-assigns
		// a still-too-fast speed. Same distance/band, but the changed assignment
		// re-arms the request.
		s.PendingContacts = nil
		ac.Nav.Speed.Assigned = at(240)
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 1 {
			t.Fatalf("after new assignment in same band: %d requests, want 1 (re-armed)", got)
		}
	})

	t.Run("does not stack a duplicate while a request is still queued", func(t *testing.T) {
		s := &Sim{}
		ac := slowDownTestAircraft(14, at(250)) // 210 band
		s.checkSlowDownRequest(ac)
		// Cross into the 190 band before the controller pops the first request.
		ac.Nav.Waypoints[0].Location = math.NM2LL([2]float32{0, 12}, 60)
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 1 {
			t.Fatalf("queued duplicate: %d requests, want 1", got)
		}
	})

	t.Run("no request when assigned speed satisfies the gate", func(t *testing.T) {
		s := &Sim{}
		ac := slowDownTestAircraft(12, at(190)) // exactly the 190 gate
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 0 {
			t.Fatalf("%d requests, want 0 (not too fast)", got)
		}
	})

	t.Run("no request without a speed assignment", func(t *testing.T) {
		s := &Sim{}
		ac := slowDownTestAircraft(12, nil)
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 0 {
			t.Fatalf("%d requests, want 0 (no assignment)", got)
		}
	})

	t.Run("does not ask while moving away from the field", func(t *testing.T) {
		s := &Sim{}
		ac := slowDownTestAircraft(12, at(250))
		// Pretend the previous tick was closer in (10nm), so at 12nm the
		// aircraft is now receding — e.g. it overflew or went missed.
		ac.SlowDownLastDist = 10
		s.checkSlowDownRequest(ac)
		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 0 {
			t.Fatalf("%d requests, want 0 while moving away", got)
		}
	})

	t.Run("departures, unassociated, and off-frequency aircraft never ask", func(t *testing.T) {
		s := &Sim{}
		ac := slowDownTestAircraft(12, at(250))
		ac.TypeOfFlight = av.FlightTypeDeparture
		s.checkSlowDownRequest(ac)

		ac2 := slowDownTestAircraft(12, at(250))
		ac2.NASFlightPlan = nil // unassociated
		s.checkSlowDownRequest(ac2)

		ac3 := slowDownTestAircraft(12, at(250))
		ac3.ControllerFrequency = "" // off-frequency (e.g. radar services terminated)
		s.checkSlowDownRequest(ac3)

		if got := slowDownRequestCount(s, ac.ADSBCallsign); got != 0 {
			t.Fatalf("%d requests, want 0", got)
		}
		if got := slowDownRequestCount(s, ac3.ADSBCallsign); got != 0 {
			t.Fatalf("%d requests, want 0 while off-frequency", got)
		}
	})
}

// slowDownSim wraps ac in a minimal Sim sufficient to drive
// GenerateContactTransmission. With no Controllers configured the controller
// lookup is nil, so the function returns the unprefixed base text — exactly
// what we want to assert on.
func slowDownSim(ac *Aircraft, seed uint64) *Sim {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := vrand.Make()
	r.Seed(seed)
	return &Sim{
		lg:              lg,
		Rand:            r,
		State:           &CommonState{},
		Aircraft:        map[av.ADSBCallsign]*Aircraft{ac.ADSBCallsign: ac},
		PendingContacts: make(map[TCP][]PendingContact),
		eventStream:     NewEventStream(lg),
	}
}

func TestGenerateSlowDownTransmission(t *testing.T) {
	// dispatch renders the request once with a fixed seed so the random
	// phrasing choice is reproducible.
	dispatch := func(ac *Aircraft, seed uint64) (string, string) {
		s := slowDownSim(ac, seed)
		return s.GenerateContactTransmission(&PendingContact{
			ADSBCallsign: ac.ADSBCallsign,
			TCP:          ac.ControllerFrequency,
			Type:         PendingTransmissionRequestSlowDown,
		})
	}

	t.Run("references the assigned speed when still too fast", func(t *testing.T) {
		// 12nm -> 190kt gate, assigned 250: still too fast, so it always asks.
		// Only some of the phrasings name the speed, so sweep seeds: every
		// transmission must be non-empty, and the speed-naming branch must
		// render 250 (never a different number).
		sawSpeed := false
		for seed := uint64(0); seed < 50; seed++ {
			_, written := dispatch(slowDownTestAircraft(12, at(250)), seed)
			if written == "" {
				t.Fatalf("seed %d: expected a transmission, got none", seed)
			}
			if strings.Contains(written, "250") {
				sawSpeed = true
			} else if strings.ContainsAny(written, "0123456789") {
				t.Errorf("seed %d: written = %q references a speed other than 250", seed, written)
			}
		}
		if !sawSpeed {
			t.Error("no sampled phrasing referenced the assigned speed 250")
		}
	})

	t.Run("maintain maximum forward omits a speed", func(t *testing.T) {
		ac := slowDownTestAircraft(12, nil)
		ac.Nav.Speed.MaintainMaximumForward = true
		for seed := uint64(0); seed < 50; seed++ {
			spoken, written := dispatch(ac, seed)
			if spoken == "" || written == "" {
				t.Fatalf("seed %d: expected a transmission, got spoken=%q written=%q", seed, spoken, written)
			}
			if strings.ContainsAny(written, "0123456789") {
				t.Errorf("seed %d: written = %q should not name a speed", seed, written)
			}
		}
	})

	t.Run("drops the request when the assignment was removed", func(t *testing.T) {
		// Enqueued while fast, but the controller has since cleared the speed.
		if spoken, written := dispatch(slowDownTestAircraft(12, nil), 0); spoken != "" || written != "" {
			t.Errorf("expected no transmission, got spoken=%q written=%q", spoken, written)
		}
	})

	t.Run("drops the request when slowed to satisfy the gate", func(t *testing.T) {
		// 12nm -> 190kt gate; controller has since assigned exactly 190.
		if spoken, written := dispatch(slowDownTestAircraft(12, at(190)), 0); spoken != "" || written != "" {
			t.Errorf("expected no transmission, got spoken=%q written=%q", spoken, written)
		}
	})

	t.Run("drops the request for a mach assignment", func(t *testing.T) {
		mach := &av.SpeedRestriction{
			NavigationRestriction: av.NavigationRestriction{Range: [2]float32{0.74, 0.74}},
			IsMach:                true,
		}
		if spoken, written := dispatch(slowDownTestAircraft(12, mach), 0); spoken != "" || written != "" {
			t.Errorf("expected no transmission, got spoken=%q written=%q", spoken, written)
		}
	})
}
