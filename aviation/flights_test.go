// aviation/flights_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func TestSplitCallsign(t *testing.T) {
	for _, tc := range []struct{ callsign, base, number string }{
		{"DAL1062", "DAL", "1062"},
		{"N484EM", "N", "484EM"},
		{"SWA2284", "SWA", "2284"},
		{"SKW775E", "SKW", "775E"},
		{"ABC", "ABC", ""},
		{"", "", ""},
	} {
		base, number := SplitCallsign(tc.callsign)
		if base != tc.base || number != tc.number {
			t.Errorf("SplitCallsign(%q) = %q, %q; expected %q, %q",
				tc.callsign, base, number, tc.base, tc.number)
		}
	}
}

func TestFlightDataDayNumber(t *testing.T) {
	for _, date := range []string{"1970-01-01", "2025-07-15", "2026-02-28", "2028-02-29", "2099-12-31"} {
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil {
			t.Fatal(err)
		}
		if got := FlightDataDate(FlightDataDayNumber(parsed)).Format("2006-01-02"); got != date {
			t.Errorf("%s round tripped to %s", date, got)
		}
	}
}

// A flight's day and minute are UTC, so its time is just the two put back
// together; no time zone is involved.
func TestFlightTime(t *testing.T) {
	f := Flight{Airport: "KMSP", Callsign: "DAL1062",
		Day:    FlightDataDayNumber(time.Date(2026, time.April, 15, 0, 0, 0, 0, time.UTC)),
		Minute: 13*60 + 45}
	if expected := time.Date(2026, time.April, 15, 13, 45, 0, 0, time.UTC); !f.Time().Equal(expected) {
		t.Errorf("got %v, expected %v", f.Time(), expected)
	}
}

// testFlights builds flight data with the awkward cases in it: more than one
// airport, a callsign that operates daily, one that doesn't, a registration
// callsign whose flight number isn't a number, a callsign that both departs and
// arrives, the same callsign at two of the facility's airports, flights either
// side of a year boundary, and flight numbers with leading zeros.
func testFlights() []Flight {
	var flights []Flight
	base := FlightDataDayNumber(time.Date(2025, time.July, 1, 0, 0, 0, 0, time.UTC))
	add := func(airport, callsign, other, acType string, day uint16, minute int, departure bool) {
		flights = append(flights, Flight{Airport: airport, Callsign: callsign, Other: other,
			AircraftType: acType, Day: base + day, Minute: minute, Departure: departure})
	}
	for day := uint16(0); day < 300; day++ {
		add("KMSP", "DAL1062", "KATL", "B753", day, 5*60+31+int(day)%7, true) // daily, wandering
		add("KMSP", "DAL1062", "KATL", "B753", day, 22*60+int(day)%11, false) // and back again
		add("KSTP", "DAL1062", "KATL", "B753", day, 9*60, true)               // same callsign, elsewhere
	}
	add("KMSP", "N484EM", "KEWR", "G280", 12, 23*60+55, true)
	add("KMSP", "N484EM", "KTEB", "G280", 200, 0, false)
	add("KSTP", "SKW775E", "KFWA", "CRJ2", 0, 8, true)
	add("KMSP", "UAL2041", "KDEN", "B738", 183, 23*60+59, true) // 2025-12-31
	add("KMSP", "UAL2041", "KDEN", "B739", 184, 0, true)        // 2026-01-01

	// Flight numbers with leading zeros must not be confused with the same
	// number without them: that would merge two callsigns into one run and
	// throw off every time that follows.
	for day := uint16(0); day < 5; day++ {
		add("KMSP", "AAL123", "KDFW", "A321", day, 7*60, true)
		add("KMSP", "AAL0123", "KDFW", "A321", day, 14*60, true)
		add("KMSP", "AAL00123", "KDFW", "A321", day, 19*60, true)
	}
	return flights
}

func TestEncodeFlightsRoundTrip(t *testing.T) {
	flights := testFlights()
	SortFlights(flights)

	encoded, err := EncodeFlights(flights)
	if err != nil {
		t.Fatalf("EncodeFlights: %v", err)
	}

	decoded, err := DecodeFlights(encoded)
	if err != nil {
		t.Fatalf("DecodeFlights: %v", err)
	}
	if len(decoded) != len(flights) {
		t.Fatalf("decoded %d flights, expected %d", len(decoded), len(flights))
	}
	for i, f := range flights {
		if decoded[i] != f {
			t.Errorf("flight %d: got %+v, expected %+v", i, decoded[i], f)
		}
	}
}

func TestFlightDataIntervalCoversTheData(t *testing.T) {
	flights := testFlights()
	SortFlights(flights)
	encoded, err := EncodeFlights(flights)
	if err != nil {
		t.Fatalf("EncodeFlights: %v", err)
	}

	interval, err := FlightDataInterval(encoded)
	if err != nil {
		t.Fatalf("FlightDataInterval: %v", err)
	}
	for _, f := range flights {
		if f.Date().Before(interval[0]) || !f.Date().Before(interval[1]) {
			t.Fatalf("%s on %v is outside %v..%v", f.Callsign, f.Date(), interval[0], interval[1])
		}
	}
	// The header must agree with the data without decompressing it.
	if expected := time.Date(2025, time.July, 1, 0, 0, 0, 0, time.UTC); !interval[0].Equal(expected) {
		t.Errorf("interval starts at %v, expected %v", interval[0], expected)
	}
}

func TestEncodeStringsRoundTrip(t *testing.T) {
	for _, values := range [][]string{
		nil,
		{""},      // a callsign with no letters has an empty base
		{"", "N"}, // and can sit alongside ones that do
		{"DAL", "SKW", "N"},
		{"DAL", ""},
	} {
		decoded, err := decodeStrings(encodeStrings(values))
		if err != nil {
			t.Errorf("decodeStrings(%q): %v", values, err)
		} else if !slices.Equal(decoded, values) && !(len(values) == 0 && len(decoded) == 0) {
			t.Errorf("%q round tripped to %q", values, decoded)
		}
	}
}

func TestEncodeFlightsEmpty(t *testing.T) {
	encoded, err := EncodeFlights(nil)
	if err != nil {
		t.Fatalf("EncodeFlights: %v", err)
	}
	flights, err := DecodeFlights(encoded)
	if err != nil {
		t.Fatalf("DecodeFlights: %v", err)
	}
	if len(flights) != 0 {
		t.Errorf("decoded %d flights from empty data", len(flights))
	}
}

func TestDecodeFlightsRejectsGarbage(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		[]byte("nope"),
		[]byte("VFLT\x02"),                  // wrong version
		append([]byte("VFLT\x01"), 0xff, 1), // truncated
	} {
		if _, err := DecodeFlights(data); err == nil {
			t.Errorf("decoded %q without complaining", data)
		}
	}
}

// Flights cross the RPC boundary on their way to the sim, and msgpack rewrites
// a time.Time into the local zone; a calendar date stored that way comes back a
// day off. Flight carries a day number so that can't happen.
func TestFlightSurvivesMsgpack(t *testing.T) {
	f := Flight{Airport: "KJFK", Callsign: "DAL1", Other: "KATL", AircraftType: "B738",
		Day:    FlightDataDayNumber(time.Date(2025, time.October, 9, 0, 0, 0, 0, time.UTC)),
		Minute: 8 * 60, Departure: true}

	var buf bytes.Buffer
	if err := msgpack.NewEncoder(&buf).Encode([]Flight{f}); err != nil {
		t.Fatal(err)
	}
	var decoded []Flight
	if err := msgpack.NewDecoder(&buf).Decode(&decoded); err != nil {
		t.Fatal(err)
	}

	if len(decoded) != 1 || decoded[0] != f {
		t.Fatalf("got %+v, expected %+v", decoded, f)
	}
	if !decoded[0].Time().Equal(f.Time()) {
		t.Errorf("time moved from %v to %v", f.Time(), decoded[0].Time())
	}
}

func TestFlightsInWindow(t *testing.T) {
	day := FlightDataDayNumber(time.Date(2026, time.April, 15, 0, 0, 0, 0, time.UTC))
	flights := []Flight{
		{Airport: "KMSP", Callsign: "DAL1", Other: "KATL", AircraftType: "B738",
			Day: day, Minute: 8 * 60, Departure: true},
		{Airport: "KMSP", Callsign: "DAL2", Other: "KATL", AircraftType: "B738",
			Day: day, Minute: 9 * 60},
		{Airport: "KMSP", Callsign: "DAL3", Other: "KATL", AircraftType: "B738",
			Day: day, Minute: 20 * 60, Departure: true},
		{Airport: "KSTP", Callsign: "DAL4", Other: "KATL", AircraftType: "B738",
			Day: day, Minute: 9 * 60, Departure: true},
		{Airport: "KMSP", Callsign: "XXX5", Other: "KATL", AircraftType: "B738",
			Day: day, Minute: 8*60 + 30, Departure: true},
	}
	SortFlights(flights)
	encoded, err := EncodeFlights(flights)
	if err != nil {
		t.Fatalf("EncodeFlights: %v", err)
	}

	start := FlightDataDate(day).Add(8 * time.Hour)
	msp := map[string]bool{"KMSP": true}
	airlines := map[string]Airline{"DAL": {}}
	window, err := FlightsInWindow(encoded, msp, msp, airlines, start, start.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("FlightsInWindow: %v", err)
	}

	var callsigns []string
	for _, f := range window {
		callsigns = append(callsigns, f.Callsign)
	}
	// KSTP isn't wanted, DAL3 is outside the window, XXX5 isn't an airline, and
	// the rest come back in time order.
	if !slices.Equal(callsigns, []string{"DAL1", "DAL2"}) {
		t.Errorf("got %v, expected [DAL1 DAL2]", callsigns)
	}

	// An airport a scenario only departs contributes no arrivals.
	window, err = FlightsInWindow(encoded, msp, nil, airlines, start, start.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("FlightsInWindow: %v", err)
	}
	callsigns = nil
	for _, f := range window {
		callsigns = append(callsigns, f.Callsign)
	}
	if !slices.Equal(callsigns, []string{"DAL1"}) {
		t.Errorf("got %v, expected just the departure DAL1", callsigns)
	}
}
