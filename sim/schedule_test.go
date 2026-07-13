// sim/schedule_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"strings"
	"testing"
)

func TestLoadScheduleCSV(t *testing.T) {
	input := `destination,time,callsign,cargo,aircraft_type,origin,ignored
KATL,14:05,dal1045,false,A321,KMSP,value
KMSP,14:17,FDX1412,yes,B763,KMEM,value
`

	flights, err := LoadScheduleCSV(strings.NewReader(input))
	if err != nil {
		t.Fatalf("LoadScheduleCSV: %v", err)
	}
	if len(flights) != 2 {
		t.Fatalf("got %d flights, want 2", len(flights))
	}

	departure := flights[0]
	if departure.Callsign != "DAL1045" || departure.Origin != "KMSP" || departure.Destination != "KATL" ||
		departure.AircraftType != "A321" || departure.ScheduledMinute != 14*60+5 || departure.Cargo {
		t.Errorf("unexpected departure: %#v", departure)
	}
	if got := departure.OperationAt("kmsp"); got != ScheduleOperationDeparture {
		t.Errorf("departure OperationAt = %v, want %v", got, ScheduleOperationDeparture)
	}

	arrival := flights[1]
	if !arrival.Cargo {
		t.Errorf("cargo = false, want true")
	}
	if got := arrival.OperationAt("KMSP"); got != ScheduleOperationArrival {
		t.Errorf("arrival OperationAt = %v, want %v", got, ScheduleOperationArrival)
	}
}

func TestLoadScheduleCSVValidation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty",
			input: "",
			want:  "empty",
		},
		{
			name:  "missing column",
			input: "callsign,origin,destination,time\nDAL1,KMSP,KATL,14:05\n",
			want:  `missing required "aircraft_type"`,
		},
		{
			name:  "bad time",
			input: "callsign,origin,destination,aircraft_type,time\nDAL1,KMSP,KATL,A321,25:05\n",
			want:  "invalid hour",
		},
		{
			name:  "bad cargo",
			input: "callsign,origin,destination,aircraft_type,time,cargo\nDAL1,KMSP,KATL,A321,14:05,maybe\n",
			want:  "must be true or false",
		},
		{
			name:  "no flights",
			input: "callsign,origin,destination,aircraft_type,time\n",
			want:  "contains no flights",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadScheduleCSV(strings.NewReader(test.input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestScheduledFlightOperationAt(t *testing.T) {
	tests := []struct {
		name   string
		flight ScheduledFlight
		want   ScheduleOperation
	}{
		{"departure", ScheduledFlight{Origin: "KMSP", Destination: "KATL"}, ScheduleOperationDeparture},
		{"arrival", ScheduledFlight{Origin: "KATL", Destination: "KMSP"}, ScheduleOperationArrival},
		{"unrelated", ScheduledFlight{Origin: "KATL", Destination: "KDTW"}, ScheduleOperationUnknown},
		{"same airport", ScheduledFlight{Origin: "KMSP", Destination: "KMSP"}, ScheduleOperationUnknown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.flight.OperationAt("KMSP"); got != test.want {
				t.Errorf("OperationAt = %v, want %v", got, test.want)
			}
		})
	}
}
