// aviation/flights_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"bytes"
	"path"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
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
	for day := range uint16(300) {
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
	for day := range uint16(5) {
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

func TestFlightIntervalsCoverTheData(t *testing.T) {
	flights := testFlights()
	SortFlights(flights)

	intervals := FlightIntervals(flights)
	for _, f := range flights {
		if !slices.ContainsFunc(intervals, func(iv util.TimeInterval) bool {
			return iv.Contains(f.Time())
		}) {
			t.Fatalf("%s at %v is outside %v", f.Callsign, f.Time(), intervals)
		}
	}

	// These flights run every day with nothing like a day's break, so they are
	// one stretch: an ordinary cell's data must not come apart into pieces.
	if len(intervals) != 1 {
		t.Fatalf("got %d intervals, expected 1: %v", len(intervals), intervals)
	}
	// The first flight is SKW775E, eight minutes into the first day.
	expected := time.Date(2025, time.July, 1, 0, 8, 0, 0, time.UTC)
	if !intervals[0].Start().Equal(expected) {
		t.Errorf("interval starts at %v, expected %v", intervals[0].Start(), expected)
	}
}

// The source data has gone down for days at a time, and a sim started in the
// hole has nothing to fly. The stretches metadata.json records are what keeps
// those times from being offered, so they have to break in the right places --
// in particular at the exact time the data comes back, not at the following
// midnight.
func TestFlightIntervalsSplitAtGaps(t *testing.T) {
	base := FlightDataDayNumber(time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC))
	var flights []Flight
	add := func(day uint16, minute int) {
		flights = append(flights, Flight{Airport: "KMSP", Callsign: "DAL1062", Other: "KATL",
			AircraftType: "B753", Day: base + day, Minute: minute, Departure: true})
	}
	// Hourly flights for three days, nothing for the next two, then resuming at
	// 18:00 on the day after that.
	for day := range uint16(3) {
		for hour := range 24 {
			add(day, hour*60)
		}
	}
	for hour := 18; hour < 24; hour++ {
		add(5, hour*60)
	}

	SortFlights(flights)
	intervals := FlightIntervals(flights)

	expected := []util.TimeInterval{
		{time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, time.May, 3, 23, 0, 0, 0, time.UTC)},
		{time.Date(2026, time.May, 6, 18, 0, 0, 0, time.UTC),
			time.Date(2026, time.May, 6, 23, 0, 0, 0, time.UTC)},
	}
	if !slices.Equal(intervals, expected) {
		t.Errorf("got %v, expected %v", intervals, expected)
	}
}

// A quiet airport can sit idle most of the night. That is a lull, not a gap:
// treating it as one would break the data into stretches too short to start a
// sim in and leave such a cell with no time to offer at all.
func TestFlightIntervalsKeepOvernightLulls(t *testing.T) {
	base := FlightDataDayNumber(time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC))
	var flights []Flight
	// Two flights a day, twenty hours apart.
	for day := range uint16(10) {
		for _, minute := range []int{2 * 60, 22 * 60} {
			flights = append(flights, Flight{Airport: "KASE", Callsign: "SKW775", Other: "KDEN",
				AircraftType: "CRJ2", Day: base + day, Minute: minute, Departure: true})
		}
	}

	SortFlights(flights)
	if intervals := FlightIntervals(flights); len(intervals) != 1 {
		t.Errorf("got %d intervals, expected 1: %v", len(intervals), intervals)
	}
}

func TestFlightIntervalsSingleFlight(t *testing.T) {
	day := FlightDataDayNumber(time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC))
	intervals := FlightIntervals([]Flight{{Airport: "KMSP", Callsign: "DAL1062", Other: "KATL",
		AircraftType: "B753", Day: day, Minute: 9 * 60, Departure: true}})

	expected := time.Date(2026, time.May, 1, 9, 0, 0, 0, time.UTC)
	if len(intervals) != 1 || !intervals[0].Start().Equal(expected) ||
		!intervals[0].End().Equal(expected) {
		t.Errorf("got %v, expected one interval at %v", intervals, expected)
	}
}

// The intervals are worked out a cell at a time and merged, which has to give
// the same answer as looking at every flight's time at once: a stretch one cell
// sits out is only a gap if every other cell sits it out too.
func TestMergeFlightIntervals(t *testing.T) {
	day := func(d int, hour int) time.Time {
		return time.Date(2026, time.May, d, hour, 0, 0, 0, time.UTC)
	}
	merged := MergeFlightIntervals([]util.TimeInterval{
		// A cell that quit on the 3rd and came back on the 6th.
		{day(1, 0), day(3, 12)},
		{day(6, 0), day(9, 0)},
		// A quiet one that flew a day at a time in between, closing the hole.
		{day(4, 6), day(4, 6)},
		{day(5, 5), day(5, 5)},
		// And one that only ever flew on the 8th, inside what is already covered.
		{day(8, 3), day(8, 20)},
	})

	expected := []util.TimeInterval{{day(1, 0), day(9, 0)}}
	if !slices.Equal(merged, expected) {
		t.Errorf("got %v, expected %v", merged, expected)
	}

	// With nothing to bridge the hole it stays a gap.
	merged = MergeFlightIntervals([]util.TimeInterval{{day(1, 0), day(3, 12)}, {day(6, 0), day(9, 0)}})
	expected = []util.TimeInterval{{day(1, 0), day(3, 12)}, {day(6, 0), day(9, 0)}}
	if !slices.Equal(merged, expected) {
		t.Errorf("got %v, expected %v", merged, expected)
	}
}

func TestFlightDataMetadataRoundTrip(t *testing.T) {
	intervals := []util.TimeInterval{
		{time.Date(2025, time.July, 1, 0, 8, 0, 0, time.UTC),
			time.Date(2026, time.May, 4, 23, 51, 0, 0, time.UTC)},
	}
	encoded, err := EncodeFlightDataMetadata(intervals)
	if err != nil {
		t.Fatalf("EncodeFlightDataMetadata: %v", err)
	}

	resources := fstest.MapFS{
		path.Join(FlightDataDirectory, FlightDataMetadataName): &fstest.MapFile{Data: encoded},
	}
	decoded, err := FlightDataIntervals(resources)
	if err != nil {
		t.Fatalf("FlightDataIntervals: %v", err)
	}
	if !slices.Equal(decoded, intervals) {
		t.Errorf("got %v, expected %v", decoded, intervals)
	}

	// No metadata at all is how "there is no flight data" reads, not an error.
	if decoded, err := FlightDataIntervals(fstest.MapFS{}); err != nil || len(decoded) != 0 {
		t.Errorf("got %v, %v from empty resources, expected nothing", decoded, err)
	}
}

func TestFlightDataCell(t *testing.T) {
	for _, tc := range []struct {
		latitude, longitude float32
		expected            string
	}{
		{40.64, -73.78, "N40W074"},   // KJFK
		{33.94, -118.41, "N32W120"},  // KLAX
		{61.17, -150.0, "N60W150"},   // PANC, right on a cell boundary
		{21.32, -157.92, "N20W158"},  // PHNL
		{13.48, 144.80, "N12E144"},   // PGUM, the far side of the date line
		{-14.33, -170.71, "S16W172"}, // NSTU, south of the equator
		{0, 0, "N00E000"},
		{-0.5, -0.5, "S02W002"}, // and just the other side of both lines
	} {
		if cell := FlightDataCell(math.Point2LL{tc.longitude, tc.latitude}); cell != tc.expected {
			t.Errorf("(%g, %g): got %q, expected %q", tc.latitude, tc.longitude, cell, tc.expected)
		}
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
		[]byte("VFLT\x03"),               // an older version
		append([]byte("VFLT\x04"), 0xff), // truncated mid-varint
		append([]byte("VFLT\x04"), 0, 1), // no stream lengths follow the count
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

func TestSelectFlights(t *testing.T) {
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

	start := FlightDataDate(day).Add(8 * time.Hour)
	msp := map[string]bool{"KMSP": true}
	airlines := map[string]Airline{"DAL": {}}
	callsignsIn := func(window []Flight) []string {
		var callsigns []string
		for _, f := range window {
			callsigns = append(callsigns, f.Callsign)
		}
		return callsigns
	}

	// KSTP is in the cell but isn't wanted, DAL3 is outside the window, XXX5
	// isn't an airline, and the rest come back in time order.
	window := SelectFlights(flights, msp, msp, airlines, start, start.Add(2*time.Hour))
	if callsigns := callsignsIn(window); !slices.Equal(callsigns, []string{"DAL1", "DAL2"}) {
		t.Errorf("got %v, expected [DAL1 DAL2]", callsigns)
	}

	// An airport a scenario only departs contributes no arrivals.
	window = SelectFlights(flights, msp, nil, airlines, start, start.Add(2*time.Hour))
	if callsigns := callsignsIn(window); !slices.Equal(callsigns, []string{"DAL1"}) {
		t.Errorf("got %v, expected just the departure DAL1", callsigns)
	}
}
