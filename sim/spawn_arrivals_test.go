package sim

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
)

func TestResolvePublishedArrival(t *testing.T) {
	arrivals := []av.Arrival{
		{
			Airlines: map[string][]av.ArrivalAirline{
				"KMSP": {
					{
						AirlineSpecifier: av.AirlineSpecifier{
							ICAO: "DAL",
						},
						Airport: "KORD",
					},
				},
			},
		},
		{
			Airlines: map[string][]av.ArrivalAirline{
				"KMSP": {
					{
						AirlineSpecifier: av.AirlineSpecifier{
							ICAO: "UAL",
						},
						Airport: "KDEN",
					},
				},
			},
		},
	}

	arrival, err := resolvePublishedArrival(arrivals, "KMSP", "KDEN")
	if err != nil {
		t.Fatalf("resolvePublishedArrival returned an error: %v", err)
	}
	if arrival != &arrivals[1] {
		t.Fatal("resolvePublishedArrival returned the wrong arrival route")
	}
}

func TestResolvePublishedArrivalRejectsUnknownOrigin(t *testing.T) {
	arrivals := []av.Arrival{
		{
			Airlines: map[string][]av.ArrivalAirline{
				"KMSP": {
					{
						Airport: "KORD",
					},
				},
			},
		},
	}

	_, err := resolvePublishedArrival(arrivals, "KMSP", "KLAX")
	if err == nil {
		t.Fatal("resolvePublishedArrival accepted an unknown origin")
	}
}

// A flight at an airport the scenario lands no traffic at must not hold up the
// arrivals behind it: the queue is shared across every airport a facility
// works, so one stuck at the head would stall all of them.
func TestZeroRateArrivalsDoNotBlock(t *testing.T) {
	day := av.FlightDataDayNumber(time.Date(2026, time.April, 15, 0, 0, 0, 0, time.UTC))
	arrival := func(airport, callsign, group string) publishedFlight {
		return publishedFlight{
			flight: av.Flight{Airport: airport, Callsign: callsign, Other: "KATL",
				AircraftType: "B738", Day: day, Minute: 8 * 60},
			group: group,
		}
	}

	p := &publishedTrafficProvider{arrivals: []publishedFlight{
		arrival("KFRG", "DAL1", "PUCKY1"), // PUCKY1 lands nothing at KFRG
		arrival("KFRG", "DAL2", "PUCKY1"),
		arrival("KJFK", "DAL3", "PUCKY1"), // but it does at KJFK
		arrival("KJFK", "DAL4", "CAMRN5"), // and this one belongs to another flow
	}}

	s := &Sim{State: &CommonState{DynamicState: DynamicState{
		LaunchConfig: LaunchConfig{InboundFlowEnabled: map[string]map[string]bool{
			"PUCKY1": {"KFRG": false, "KJFK": true},
			"CAMRN5": {"KJFK": true},
		}}}}}
	rates := map[string]float32{"KFRG": 0, "KJFK": 12}

	index := p.nextArrivalFor(s, "PUCKY1", rates)
	if index < 0 {
		t.Fatal("PUCKY1 found nothing to fly")
	}
	if got := p.arrivals[index].flight.Callsign; got != "DAL3" {
		t.Errorf("PUCKY1 picked %s, expected DAL3 with the KFRG arrivals discarded", got)
	}
	if p.discardedArrivals["KFRG"] != 2 {
		t.Errorf("discarded %v, expected both KFRG arrivals", p.discardedArrivals)
	}

	// The other flow steps over PUCKY1's rather than queueing behind it.
	if index := p.nextArrivalFor(s, "CAMRN5", rates); index < 0 ||
		p.arrivals[index].flight.Callsign != "DAL4" {
		t.Errorf("CAMRN5 did not find its own arrival")
	}
}
