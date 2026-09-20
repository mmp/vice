package sim

import (
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	vrand "github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

func TestMain(m *testing.M) {
	av.InitDB()
	os.Exit(m.Run())
}

// testLogger returns a logger that discards everything, for tests that have no use
// for the output.
func testLogger() *log.Logger {
	return &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// NewTestSim creates a minimal Sim suitable for command dispatch tests. Tests should
// build their sims with it rather than with a Sim literal, so that the maps and
// channels a Sim needs are always allocated.
// Exported only to _test packages via Go's export_test.go convention.
func NewTestSim(lg *log.Logger) *Sim {
	tcw := TCW("TEST")
	freq := ControlPosition("125.0")

	return &Sim{
		lg:          lg,
		Rand:        vrand.Make(),
		eventStream: NewEventStream(lg),
		State: &CommonState{
			DynamicState: DynamicState{
				METAR:                map[av.ICAOAirportCode]wx.METAR{},
				SimTime:              NewSimTime(time.Now()),
				CurrentConsolidation: map[TCW]*TCPConsolidation{tcw: {PrimaryTCP: TCP(freq)}},
			},
			Airports: map[av.ICAOAirportCode]*av.Airport{},
		},
		Aircraft:            map[av.ADSBCallsign]*Aircraft{},
		DepartureState:      make(map[av.ICAOAirportCode]map[av.RunwayID]*RunwayLaunchState),
		LastExitLaunch:      make(map[av.ICAOAirportCode]map[av.ExitID]Time),
		PatternState:        make(map[av.ICAOAirportCode]*PatternState),
		lastSTTCommands:     make(map[TCW]*lastSTTCommand),
		Handoffs:            make(map[ACID]Handoff),
		PendingContacts:     make(map[TCP][]PendingContact),
		PrivilegedTCWs:      map[TCW]bool{tcw: true},
		FutureFieldChecks:   make(map[av.ADSBCallsign]*FutureFieldCheck),
		FutureTrafficChecks: make(map[av.ADSBCallsign]*FutureTrafficCheck),
	}
}

// MakeTestAircraft creates a minimal arrival aircraft suitable for e2e tests.
func MakeTestAircraft(callsign av.ADSBCallsign, runway string) *Aircraft {
	return &Aircraft{
		ADSBCallsign:        callsign,
		TypeOfFlight:        av.FlightTypeArrival,
		ControllerFrequency: ControlPosition("125.0"),
		FlightPlan: av.FlightPlan{
			ArrivalAirport: "KJFK",
		},
		Nav: nav.Nav{
			Rand: vrand.Make(),
			FlightState: nav.FlightState{
				Position:                [2]float32{0, 5.0 / 60}, // 5nm north
				Heading:                 180,
				Altitude:                3000,
				NmPerLongitude:          52,
				MagneticVariation:       0,
				ArrivalAirport:          av.Waypoint{Fix: "KJFK"},
				ArrivalAirportLocation:  [2]float32{0, 0},
				ArrivalAirportElevation: 13,
			},
			Approach: nav.Approach{
				AssignedId: "I" + runway,
				Assigned: &av.Approach{
					Type:   av.ILSApproach,
					Runway: runway,
				},
			},
		},
	}
}

// E2ETCW returns the TCW used by NewTestSim.
func E2ETCW() TCW { return TCW("TEST") }

// LastAddressedCallsign returns what clients are told about the aircraft the controller
// at tcw last transmitted to, so that e2e tests can build the STT aircraft context the
// way stt.BuildAircraftContext does. (stt imports sim, so those tests cannot be in
// package sim and reach it directly.)
func LastAddressedCallsign(s *Sim, tcw TCW) av.ADSBCallsign {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.lastAddressedCallsign(tcw)
}
