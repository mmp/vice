package sim

import "testing"

func TestValidateTimetable(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				PublishedMinute: 600,
			},
		},
	}

	if err := validateTimetable(timetable); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateTimetableRejectsDuplicateRows(t *testing.T) {
	flight := TimetableFlight{
		Callsign:        "DAL100",
		Origin:          "KMSP",
		Destination:     "KORD",
		AircraftType:    "A320",
		PublishedMinute: 600,
	}

	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			flight,
			flight,
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected duplicate row validation error")
	}
}
func TestValidateTimetableAllowsCallsignReuse(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				PublishedMinute: 600,
			},
			{
				Callsign:        "DAL100",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				PublishedMinute: 780,
			},
		},
	}

	if err := validateTimetable(timetable); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
func TestValidateTimetableRejectsOverlappingCallsignReuse(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL100",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				PublishedMinute: 9 * 60,
			},
			{
				Callsign:        "DAL100",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				PublishedMinute: 9*60 + 20,
			},
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected overlapping callsign validation error")
	}
}
func TestValidateTimetableRejectsCrossMidnightCallsignOverlap(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL200",
				Origin:          "KMSP",
				Destination:     "KORD",
				AircraftType:    "A320",
				PublishedMinute: 23*60 + 55,
			},
			{
				Callsign:        "DAL200",
				Origin:          "KORD",
				Destination:     "KMSP",
				AircraftType:    "A320",
				PublishedMinute: 10,
			},
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected cross-midnight callsign validation error")
	}
}
func TestValidateTimetableRejectsUnknownOriginAirport(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL300",
				Origin:          "KZZZ",
				Destination:     "KMSP",
				AircraftType:    "A320",
				PublishedMinute: 12 * 60,
			},
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected unknown origin airport validation error")
	}
}

func TestValidateTimetableRejectsUnknownDestinationAirport(t *testing.T) {
	timetable := Timetable{
		Airport: "KMSP",
		Flights: []TimetableFlight{
			{
				Callsign:        "DAL301",
				Origin:          "KMSP",
				Destination:     "KZZZ",
				AircraftType:    "A320",
				PublishedMinute: 13 * 60,
			},
		},
	}

	if err := validateTimetable(timetable); err == nil {
		t.Fatal("expected unknown destination airport validation error")
	}
}
