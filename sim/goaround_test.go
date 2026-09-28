// sim/goaround_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"

	av "github.com/mmp/vice/aviation"
)

func TestGetGoAroundController(t *testing.T) {
	sidDepartures := map[string]TCP{"KIND/DAWNN1": "1E", "KIND/ROCKY1": "1W"}
	runwayDepartures := map[string]TCP{"KIND/5L": "1W", "KIND/23R": "1E"}

	tests := []struct {
		name       string
		goArounds  map[string]TCP
		departures map[string]TCP
		fp         *NASFlightPlan // associated with the aircraft
		stars      *NASFlightPlan // in the STARS computer, unassociated
		want       TCP
	}{
		{
			name:       "runway go-around assignment",
			goArounds:  map[string]TCP{"KIND/5L": "1S", "KIND": "1N"},
			departures: map[string]TCP{"KIND": "1E"},
			fp:         &NASFlightPlan{ACID: "DAL49", TrackingController: "1B"},
			want:       "1S",
		},
		{
			name:       "airport go-around assignment",
			goArounds:  map[string]TCP{"KIND/23R": "1S", "KIND": "1N"},
			departures: map[string]TCP{"KIND": "1E"},
			fp:         &NASFlightPlan{ACID: "DAL49", TrackingController: "1B"},
			want:       "1N",
		},
		{
			name:       "airport departure assignment",
			departures: map[string]TCP{"KIND": "1E"},
			fp:         &NASFlightPlan{ACID: "DAL49", TrackingController: "1B"},
			want:       "1E",
		},
		{
			name:       "runway departure assignment",
			departures: runwayDepartures,
			fp:         &NASFlightPlan{ACID: "DAL49", TrackingController: "1B"},
			want:       "1W",
		},
		{
			name:       "SID departure assignments: tracking controller",
			departures: sidDepartures,
			fp:         &NASFlightPlan{ACID: "DAL49", TrackingController: "1B"},
			want:       "1B",
		},
		{
			name:       "unassociated flight plan's tracking controller",
			departures: sidDepartures,
			stars:      &NASFlightPlan{ACID: "DAL49", TrackingController: "1B"},
			want:       "1B",
		},
		{
			name:       "no flight plan: frequency",
			departures: sidDepartures,
			want:       "_TOWER",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Sim{
				GoAroundAssignments:  tc.goArounds,
				DepartureAssignments: tc.departures,
				STARSComputer:        makeSTARSComputer("IND"),
			}
			if tc.stars != nil {
				s.STARSComputer.FlightPlans = append(s.STARSComputer.FlightPlans, tc.stars)
			}

			ac := &Aircraft{
				ADSBCallsign:        "DAL49",
				NASFlightPlan:       tc.fp,
				ControllerFrequency: "_TOWER", // after "contact tower"
			}
			ac.FlightPlan.ArrivalAirport = "KIND"
			ac.Nav.Approach.Assigned = &av.Approach{Runway: "5L"}

			if got := s.getGoAroundController(ac); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
