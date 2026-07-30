package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
)

func TestResolveScheduledArrival(t *testing.T) {
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

	arrival, err := resolveScheduledArrival(arrivals, "KMSP", "KDEN")
	if err != nil {
		t.Fatalf("resolveScheduledArrival returned an error: %v", err)
	}
	if arrival != &arrivals[1] {
		t.Fatal("resolveScheduledArrival returned the wrong arrival route")
	}
}

func TestResolveScheduledArrivalRejectsUnknownOrigin(t *testing.T) {
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

	_, err := resolveScheduledArrival(arrivals, "KMSP", "KLAX")
	if err == nil {
		t.Fatal("resolveScheduledArrival accepted an unknown origin")
	}
}
