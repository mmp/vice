package sim

import (
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	vrand "github.com/mmp/vice/rand"
	"github.com/mmp/vice/wx"
)

// NewTestSim creates a minimal Sim suitable for command dispatch tests.
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
				METAR:                map[string]wx.METAR{},
				SimTime:              time.Now(),
				CurrentConsolidation: map[TCW]*TCPConsolidation{tcw: {PrimaryTCP: TCP(freq)}},
			},
			Airports: map[string]*av.Airport{},
		},
		Aircraft:        map[av.ADSBCallsign]*Aircraft{},
		PendingContacts: make(map[TCP][]PendingContact),
		PrivilegedTCWs:  map[TCW]bool{tcw: true},
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
			FlightState: nav.FlightState{
				Position:               [2]float32{0, 5.0 / 60}, // 5nm north
				Heading:                180,
				Altitude:               3000,
				NmPerLongitude:         52,
				MagneticVariation:      0,
				ArrivalAirport:         av.Waypoint{Fix: "KJFK"},
				ArrivalAirportLocation: [2]float32{0, 0},
				ArrivalAirportElevation: 13,
			},
			Approach: nav.NavApproach{
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
