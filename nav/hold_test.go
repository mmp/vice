// nav/hold_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/wx"
)

func TestHoldTurningInboundDoesNotFlyAwayAfterOvershoot(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAYED/a18000 RACKI/a13000/ho PENNS SWEET/a7000 BWZ/h122",
		DepartureAirport: "KATL",
		ArrivalAirport:   "KEWR",
		AircraftType:     "B752",
		InitialAltitude:  11900,
		InitialSpeed:     200,
	})

	f.nav.FlightState.Position = math.Point2LL{-75.117279, 40.971386}
	f.nav.FlightState.Heading = 64.807312
	f.nav.FlightState.Altitude = 7281.493652
	f.nav.FlightState.IAS = 200
	f.nav.FlightState.GS = 254.992889
	f.nav.FlightState.BankAngle = 0
	f.simTime = NewTime(time.Date(2026, 1, 15, 11, 54, 12, 0, time.UTC))
	fixLocation := math.Point2LL{-75.109550, 40.880634}
	initialDistance := math.NMDistance2LL(f.nav.FlightState.Position, fixLocation)

	f.nav.Heading = NavHeading{Hold: &FlyHold{
		Hold: av.Hold{
			Fix:             "PENNS",
			InboundCourse:   122,
			TurnDirection:   av.TurnRight,
			LegMinutes:      1,
			MinimumAltitude: 3100,
			MaximumAltitude: 17500,
			HoldingSpeed:    200,
		},
		FixLocation: fixLocation,
		Entry:       av.HoldEntryDirect,
		Maneuvers: []LateralManeuver{
			turnToTrack(122, av.TurnRight),
			flyTowardFix(fixLocation),
		},
	}}

	f.nav.UpdateWithWeather(f.callsign, wx.MakeStandardSampleForAltitude(f.nav.FlightState.Altitude),
		&f.fp, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)
	if f.nav.Heading.Hold == nil {
		t.Fatal("hold unexpectedly ended")
	}
	wantStep := turnToTrack(122, av.TurnRight)
	if got := f.nav.Heading.Hold.currentStep(); got != wantStep.String() {
		t.Fatalf("inbound turn ended early while heading %.1f, step=%s",
			f.nav.FlightState.Heading, got)
	}

	for range 90 {
		f.nav.UpdateWithWeather(f.callsign, wx.MakeStandardSampleForAltitude(f.nav.FlightState.Altitude),
			&f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)
	}

	hold := f.nav.Heading.Hold
	if hold == nil {
		t.Fatal("hold unexpectedly ended")
	}
	dist := math.NMDistance2LL(f.nav.FlightState.Position, fixLocation)
	if dist >= initialDistance {
		t.Fatalf("aircraft kept flying away from hold: step=%s heading=%.1f distance=%.1f initial=%.1f position=%v",
			hold.currentStep(), f.nav.FlightState.Heading, dist, initialDistance, f.nav.FlightState.Position)
	}
}

func TestHoldInboundTurnDistanceMatchesOutboundTurn(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "PENNS SWEET/a7000 BWZ/h122",
		DepartureAirport: "KATL",
		ArrivalAirport:   "KEWR",
		AircraftType:     "B752",
		InitialAltitude:  7281,
		InitialSpeed:     200,
	}).SetWind(230, 10) // SetWind takes wind-from direction; this makes the wind vector track 050.

	fixLocation := math.Point2LL{-75.109550, 40.880634}
	f.nav.FlightState.Position = fixLocation
	f.nav.FlightState.Heading = 122
	f.nav.FlightState.Altitude = 7281
	f.nav.FlightState.IAS = 200
	f.nav.FlightState.GS = 255
	f.nav.FlightState.BankAngle = 0
	f.simTime = NewTime(time.Date(2026, 1, 15, 11, 52, 0, 0, time.UTC))

	hold := &FlyHold{
		Hold: av.Hold{
			Fix:             "PENNS",
			InboundCourse:   122,
			TurnDirection:   av.TurnRight,
			LegMinutes:      1,
			MinimumAltitude: 3100,
			MaximumAltitude: 17500,
			HoldingSpeed:    200,
		},
		FixLocation: fixLocation,
		Entry:       av.HoldEntryDirect,
	}
	wxs := f.weather(f.nav.FlightState.Altitude)
	hold.Maneuvers = hold.circuitManeuvers(f.nav, wxs)
	f.nav.Heading = NavHeading{Hold: hold}

	var outboundTurnStart math.Point2LL
	var inboundTurnStart math.Point2LL
	var outboundTurnDistance float32
	var inboundTurnDistance float32
	outboundTurnStep, outboundLegStep, inboundTurnStep := holdCircuitStepStrings(t, hold)
	previousStep := hold.currentStep()

	for tick := range 300 {
		f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)

		step := hold.currentStep()
		if tick == 0 {
			outboundTurnStart = fixLocation
		}
		if step != previousStep {
			t.Logf("tick=%d step %q -> %q hdg=%.1f gs=%.1f pos=%v distFix=%.2f",
				tick, previousStep, step, f.nav.FlightState.Heading, f.nav.FlightState.GS,
				f.nav.FlightState.Position, math.NMDistance2LL(f.nav.FlightState.Position, fixLocation))

			switch previousStep {
			case outboundTurnStep:
				outboundTurnDistance = math.NMDistance2LL(outboundTurnStart, f.nav.FlightState.Position)
			case outboundLegStep:
				inboundTurnStart = f.nav.FlightState.Position
			case inboundTurnStep:
				inboundTurnDistance = math.NMDistance2LL(inboundTurnStart, f.nav.FlightState.Position)
				t.Logf("outboundTurn=%.2f inboundTurn=%.2f ratio=%.2f pos=%v",
					outboundTurnDistance, inboundTurnDistance, inboundTurnDistance/outboundTurnDistance,
					f.nav.FlightState.Position)
				if inboundTurnDistance > outboundTurnDistance*1.35 {
					t.Fatalf("inbound turn distance %.2f nm is too large vs outbound %.2f nm", inboundTurnDistance, outboundTurnDistance)
				}
				return
			}
			previousStep = step
		}
	}

	t.Fatalf("inbound turn did not complete; step=%s heading=%.1f position=%v",
		hold.currentStep(), f.nav.FlightState.Heading, f.nav.FlightState.Position)
}

func TestFQM3HoldInboundTurnCompletesNearExpectedTrack(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "HAYED/a18000 RACKI/a13000 PENNS SWEET/a7000 BWZ/h122",
		DepartureAirport: "KATL",
		ArrivalAirport:   "KEWR",
		AircraftType:     "B752",
		InitialAltitude:  11900,
		InitialSpeed:     200,
	}).SetWind(230, 10) // SetWind takes wind-from direction; this makes the wind vector track 050.

	f.nav.HoldAtFix(f.callsign, "PENNS", &av.Hold{
		Fix:             "PENNS",
		InboundCourse:   122,
		TurnDirection:   av.TurnRight,
		LegMinutes:      1,
		MinimumAltitude: 3100,
		MaximumAltitude: 17500,
		HoldingSpeed:    200,
	})

	var hold *FlyHold
	var previousStep string
	var outboundTurnStart math.Point2LL
	var inboundTurnStart math.Point2LL
	var outboundTurnDistance float32
	var inboundTurnDistance float32
	outboundTurnStep := ""
	outboundLegStep := ""
	inboundTurnStep := ""
	flyFixStep := "fly toward fix until fix"

	for tick := range 2000 {
		f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)

		if f.nav.Heading.Hold != nil && hold == nil {
			hold = f.nav.Heading.Hold
			previousStep = hold.currentStep()
			t.Logf("hold started tick=%d step=%q hdg=%.1f pos=%v", tick, previousStep,
				f.nav.FlightState.Heading, f.nav.FlightState.Position)
		}
		if hold == nil {
			continue
		}

		step := hold.currentStep()
		if step == previousStep {
			continue
		}

		t.Logf("fqm3 tick=%d step %q -> %q hdg=%.1f gs=%.1f pos=%v distFix=%.2f",
			tick, previousStep, step, f.nav.FlightState.Heading, f.nav.FlightState.GS,
			f.nav.FlightState.Position, math.NMDistance2LL(f.nav.FlightState.Position, hold.FixLocation))

		switch previousStep {
		case flyFixStep:
			outboundTurnStart = f.nav.FlightState.Position
			outboundTurnStep = step
			_, _, inboundTurnStep = holdCircuitStepStrings(t, hold)
		case outboundTurnStep:
			outboundTurnDistance = math.NMDistance2LL(outboundTurnStart, f.nav.FlightState.Position)
			outboundLegStep = step
		case outboundLegStep:
			inboundTurnStart = f.nav.FlightState.Position
		case inboundTurnStep:
			inboundTurnDistance = math.NMDistance2LL(inboundTurnStart, f.nav.FlightState.Position)
			t.Logf("fqm3 outboundTurn=%.2f inboundTurn=%.2f ratio=%.2f pos=%v distFix=%.2f",
				outboundTurnDistance, inboundTurnDistance, inboundTurnDistance/outboundTurnDistance,
				f.nav.FlightState.Position, math.NMDistance2LL(f.nav.FlightState.Position, hold.FixLocation))
			if inboundTurnDistance > outboundTurnDistance*1.35 {
				t.Fatalf("inbound turn distance %.2f nm is too large vs outbound %.2f nm", inboundTurnDistance, outboundTurnDistance)
			}
			return
		}

		previousStep = step
	}

	t.Fatalf("FQM3 hold inbound turn did not complete")
}

func TestHoldOutboundHeadingBiasesUpwindFromInboundCorrection(t *testing.T) {
	nav := &Nav{}
	nav.FlightState.GS = 200
	nav.FlightState.MagneticVariation = 0

	hold := &FlyHold{Hold: av.Hold{InboundCourse: 90}}
	windNorth := wx.MakeSample(
		math.Scale2f(math.SinCos(math.Radians(math.TrueHeading(0))), 40.0/3600.0),
		15, -5, 1013)
	outbound := hold.outboundHeading(nav, windNorth)
	correction := math.HeadingSignedTurn(math.MagneticHeading(270), outbound)
	if correction > -25 || correction < -45 {
		t.Fatalf("expected strong left outbound correction, got outbound %.1f correction %.1f",
			outbound, correction)
	}

	windSouth := wx.MakeSample(
		math.Scale2f(math.SinCos(math.Radians(math.TrueHeading(180))), 40.0/3600.0),
		15, -5, 1013)
	outbound = hold.outboundHeading(nav, windSouth)
	correction = math.HeadingSignedTurn(math.MagneticHeading(270), outbound)
	if correction < 25 || correction > 45 {
		t.Fatalf("expected strong right outbound correction, got outbound %.1f correction %.1f",
			outbound, correction)
	}
}

func TestHoldInboundTurnCompletesAfterHalfCircuitWithStrongWind(t *testing.T) {
	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        "PENNS SWEET/a7000 BWZ/h122",
		DepartureAirport: "KATL",
		ArrivalAirport:   "KEWR",
		AircraftType:     "B752",
		InitialAltitude:  7281,
		InitialSpeed:     200,
	}).SetWind(195, 45)

	fixLocation := math.Point2LL{-75.109550, 40.880634}
	f.nav.FlightState.Position = math.Point2LL{-75.10486, 40.87529}
	f.nav.FlightState.Heading = 176.6
	f.nav.FlightState.Altitude = 7281
	f.nav.FlightState.IAS = 200
	f.nav.FlightState.GS = 202
	f.nav.FlightState.BankAngle = 0
	f.simTime = NewTime(time.Date(2026, 1, 15, 4, 28, 40, 0, time.UTC))

	hold := &FlyHold{
		Hold: av.Hold{
			Fix:             "PENNS",
			InboundCourse:   122,
			TurnDirection:   av.TurnRight,
			LegMinutes:      1,
			MinimumAltitude: 3100,
			MaximumAltitude: 17500,
			HoldingSpeed:    200,
		},
		FixLocation: fixLocation,
		Entry:       av.HoldEntryDirect,
	}
	wxs := f.weather(f.nav.FlightState.Altitude)
	hold.Maneuvers = hold.circuitManeuvers(f.nav, wxs)
	f.nav.Heading = NavHeading{Hold: hold}

	outboundTurnStep, outboundLegStep, inboundTurnStep := holdCircuitStepStrings(t, hold)
	previousStep := hold.currentStep()

	outboundTurnStartTick := 0
	outboundTurnEndTick := -1
	inboundTurnStartTick := -1

	for tick := range 240 {
		f.nav.UpdateWithWeather(f.callsign, f.weather(f.nav.FlightState.Altitude), &f.fp, f.simTime, nil)
		f.simTime = f.simTime.Add(time.Second)

		step := hold.currentStep()
		if step == previousStep {
			continue
		}

		switch previousStep {
		case outboundTurnStep:
			outboundTurnEndTick = tick
		case outboundLegStep:
			inboundTurnStartTick = tick
		case inboundTurnStep:
			if outboundTurnEndTick < 0 || inboundTurnStartTick < 0 {
				t.Fatalf("missing prior transition: outboundEnd=%d inboundStart=%d",
					outboundTurnEndTick, inboundTurnStartTick)
			}

			outboundTurnSeconds := outboundTurnEndTick - outboundTurnStartTick
			inboundTurnSeconds := tick - inboundTurnStartTick
			t.Logf("outboundTurn=%ds inboundTurn=%ds pos=%v distFix=%.2f",
				outboundTurnSeconds, inboundTurnSeconds, f.nav.FlightState.Position,
				math.NMDistance2LL(f.nav.FlightState.Position, fixLocation))

			if inboundTurnSeconds > 45 {
				t.Fatalf("inbound turn took %d seconds; want about 40 seconds for the partial inbound turn",
					inboundTurnSeconds)
			}
			return
		}

		previousStep = step
	}

	t.Fatalf("inbound turn did not complete; step=%s heading=%.1f position=%v",
		hold.currentStep(), f.nav.FlightState.Heading, f.nav.FlightState.Position)
}

func holdCircuitStepStrings(t *testing.T, hold *FlyHold) (string, string, string) {
	t.Helper()
	if len(hold.Maneuvers) < 3 {
		t.Fatalf("hold circuit has %d maneuvers, want at least 3", len(hold.Maneuvers))
	}
	return hold.Maneuvers[0].String(), hold.Maneuvers[1].String(), hold.Maneuvers[2].String()
}
