// aviation/radio_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
)

// A queued transmission is saved to the user's config and read back, so every
// argument type must survive JSON with its type intact; Args is []any, which
// would otherwise return numbers as float64 and named string types as strings.
func TestTransmissionArgsRoundTrip(t *testing.T) {
	DB = &StaticDatabase{
		Airports:  map[ICAOAirportCode]FAAAirport{"KJFK": {Name: "John F Kennedy International"}},
		Navaids:   map[string]Navaid{"MERIT": {Name: "MERIT"}},
		Callsigns: map[string]string{"AAL": "American"},
	}

	ar := MakeAtAltitudeRestriction(8000)
	rt := RadioTransmission{
		Strings: []PhraseFormatString{"{alt} {num} {spd} {hdg} {gf} {mach}", "{airport} {fix} {ch}",
			"{beacon} {freq} {callsign} {altrest}"},
		Args: [][]any{
			{3000, 5, float32(210), math.MagneticHeading(90), 12, float32(0.75)},
			{ICAOAirportCode("KJFK"), "MERIT", "B"},
			{Squawk(0o1234), NewFrequency(118.9), CallsignArg{Callsign: "AAL123"}, &ar},
		},
		Type: RadioTransmissionContact,
	}

	b, err := json.Marshal(rt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got RadioTransmission
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(rt, got) {
		t.Errorf("round trip changed the transmission:\n got %#v\nwant %#v", got, rt)
	}

	// Rendering the recovered transmission must give the same text; a lost type
	// would leave a formatter with an argument it can't handle.
	for seed := uint64(1); seed <= 20; seed++ {
		r, rgot := rand.Make(), rand.Make()
		r.Seed(seed)
		rgot.Seed(seed)

		want, err := rt.Written(r)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		s, err := got.Written(rgot)
		if err != nil {
			t.Fatalf("seed %d: recovered transmission: %v", seed, err)
		}
		if s != want {
			t.Errorf("seed %d: recovered transmission wrote %q, want %q", seed, s, want)
		}
	}
}

// Marshalling has to fail on a type it can't decode; writing something that
// won't read back would corrupt the saved sim instead.
func TestTransmissionUnsaveableArg(t *testing.T) {
	rt := RadioTransmission{
		Strings: []PhraseFormatString{"{num}"},
		Args:    [][]any{{int64(3)}},
	}
	if _, err := json.Marshal(rt); err == nil {
		t.Error("expected an error marshalling an int64 argument")
	}
}

// A mistyped argument is reported and renders nothing, rather than panicking in
// the formatter's type assertion. The error names the phrase that failed, which
// is what the sim shows the controller in place of the lost transmission.
func TestMistypedArgReported(t *testing.T) {
	rt := MakeContactTransmission("departing {airport}", "KFRG") // want an ICAOAirportCode
	r := rand.Make()

	s, err := rt.Spoken(r)
	if s != "" || err == nil {
		t.Errorf("Spoken with a bad argument = %q, %v; want \"\" and an error", s, err)
	} else if !strings.Contains(err.Error(), "departing {airport}") {
		t.Errorf("Spoken error %q doesn't name the phrase that failed", err)
	}

	if s, err := rt.Written(r); s != "" || err == nil {
		t.Errorf("Written with a bad argument = %q, %v; want \"\" and an error", s, err)
	}
}

// A directive with no argument left is reported rather than silently dropped.
func TestMissingArgReported(t *testing.T) {
	rt := MakeContactTransmission("climbing {alt} for {alt}", 3000)
	if s, err := rt.Written(rand.Make()); s != "" || err == nil {
		t.Errorf("Written with a missing argument = %q, %v; want \"\" and an error", s, err)
	}
}

// {dctrl} and {actrl} rename the position in a controller's radio name so that
// a departure is handed to "departure" and an arrival to "approach", whichever
// way the controller happens to be named.
func TestControllerPositionRenaming(t *testing.T) {
	for _, test := range []struct{ phrase, radioName, want string }{
		{"{dctrl}", "New York Approach", "New York Departure"},
		{"{dctrl}", "new york approach", "new york departure"},
		{"{dctrl}", "Boston Departure", "Boston Departure"},
		{"{actrl}", "Boston Departure", "Boston Approach"},
		{"{actrl}", "boston departure", "boston approach"},
		{"{actrl}", "New York Approach", "New York Approach"},
	} {
		rt := MakeContactTransmission(test.phrase, &Controller{RadioName: test.radioName})
		got, err := rt.Written(rand.Make())
		if err != nil {
			t.Errorf("%s with %q: %v", test.phrase, test.radioName, err)
		} else if got != test.want {
			t.Errorf("%s with %q = %q, want %q", test.phrase, test.radioName, got, test.want)
		}
	}
}

// A nil controller is reported rather than panicking in the formatter.
func TestNilControllerArgReported(t *testing.T) {
	rt := MakeContactTransmission("{actrl}", (*Controller)(nil))
	if s, err := rt.Written(rand.Make()); s != "" || err == nil {
		t.Errorf("Written with a nil controller = %q, %v; want \"\" and an error", s, err)
	}
}
