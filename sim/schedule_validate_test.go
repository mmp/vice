package sim

import "testing"

func TestValidateBuiltInSchedule(t *testing.T) {
	schedule := BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				ScheduledMinute: 600,
			},
		},
	}

	if err := validateBuiltInSchedule(schedule); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateBuiltInScheduleRejectsDuplicateRows(t *testing.T) {
	flight := ScheduledFlight{
		Callsign:        "DAL100",
		Origin:          "KMSP",
		Destination:     "KORD",
		AircraftType:    "A320",
		ScheduledMinute: 600,
	}

	schedule := BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{
			flight,
			flight,
		},
	}

	if err := validateBuiltInSchedule(schedule); err == nil {
		t.Fatal("expected duplicate row validation error")
	}
}
func TestValidateBuiltInScheduleAllowsCallsignReuse(t *testing.T) {
	schedule := BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				ScheduledMinute: 600,
			},
			{
				Callsign:        "DAL100",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				ScheduledMinute: 780,
			},
		},
	}

	if err := validateBuiltInSchedule(schedule); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
func TestValidateBuiltInScheduleRejectsOverlappingCallsignReuse(t *testing.T) {
	schedule := BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				ScheduledMinute: 9 * 60,
			},
			{
				Callsign:        "DAL100",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				ScheduledMinute: 9*60 + 20,
			},
		},
	}

	if err := validateBuiltInSchedule(schedule); err == nil {
		t.Fatal("expected overlapping callsign validation error")
	}
}
func TestValidateBuiltInScheduleRejectsCrossMidnightCallsignOverlap(t *testing.T) {
	schedule := BuiltInSchedule{
		Airport: "KMSP",
		Flights: []ScheduledFlight{
			{
				Callsign:        "DAL200",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				ScheduledMinute: 23*60 + 55,
			},
			{
				Callsign:        "DAL200",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				ScheduledMinute: 10,
			},
		},
	}

	if err := validateBuiltInSchedule(schedule); err == nil {
		t.Fatal("expected cross-midnight callsign validation error")
	}
}
