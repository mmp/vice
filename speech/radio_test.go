// speech/radio_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package speech

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"

	"github.com/vmihailenco/msgpack/v5"
)

// A queued transmission is saved to the user's config and read back, so every
// argument type must survive JSON with its type intact; Args is []any, which
// would otherwise return numbers as float64 and named string types as strings.
// It also travels as msgpack, in a sim sent to a server or saved in a session
// log, and has to come back from that intact too.
func TestTransmissionArgsRoundTrip(t *testing.T) {
	db.DB = &db.StaticDatabase{
		Airports:  map[av.ICAOAirportCode]db.Airport{"KJFK": {Name: "John F Kennedy International"}},
		Navaids:   map[string]db.Navaid{"MERIT": {Name: "MERIT"}},
		Callsigns: map[string]string{"AAL": "American"},
	}

	ar := av.MakeAtAltitudeRestriction(8000)
	rt := RadioTransmission{
		Strings: []PhraseFormatString{"{alt} {num} {spd} {hdg} {gf} {mach}", "{airport} {fix} {ch}",
			"{beacon} {freq} {callsign} {altrest}", "{rp} {airway}"},
		Args: [][]any{
			{3000, 5, float32(210), math.MagneticHeading(90), 12, float32(0.75)},
			{av.ICAOAirportCode("KJFK"), "MERIT", "B"},
			{av.Squawk(0o1234), av.NewFrequency(118.9), CallsignArg{Callsign: "AAL123"}, &ar},
			{"Dumbarton bridge", "V1"},
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

	mb, err := msgpack.Marshal(rt)
	if err != nil {
		t.Fatalf("msgpack marshal: %v", err)
	}
	var mgot RadioTransmission
	if err := msgpack.Unmarshal(mb, &mgot); err != nil {
		t.Fatalf("msgpack unmarshal: %v", err)
	}
	if !reflect.DeepEqual(rt, mgot) {
		t.Errorf("msgpack round trip changed the transmission:\n got %#v\nwant %#v", mgot, rt)
	}

	// Rendering the recovered transmission must give the same text; a lost type
	// would leave a formatter with an argument it can't handle.
	for seed := uint64(1); seed <= 20; seed++ {
		r, rgot := rand.Make(), rand.Make()
		r.Seed(seed)
		rgot.Seed(seed)

		want, err := rt.Render(r)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		rd, err := got.Render(rgot)
		if err != nil {
			t.Fatalf("seed %d: recovered transmission: %v", seed, err)
		}
		if rd != want {
			t.Errorf("seed %d: recovered transmission rendered %+v, want %+v", seed, rd, want)
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

	rd, err := rt.Render(r)
	if rd != (Rendering{}) || err == nil {
		t.Errorf("Render with a bad argument = %+v, %v; want nothing and an error", rd, err)
	} else if !strings.Contains(err.Error(), "departing {airport}") {
		t.Errorf("Render error %q doesn't name the phrase that failed", err)
	}
}

// A directive with no argument left is reported rather than silently dropped.
func TestMissingArgReported(t *testing.T) {
	rt := MakeContactTransmission("climbing {alt} for {alt}", 3000)
	if rd, err := rt.Render(rand.Make()); rd != (Rendering{}) || err == nil {
		t.Errorf("Render with a missing argument = %+v, %v; want nothing and an error", rd, err)
	}
}

// The displayed and spoken forms of a transmission pick the same phrasing,
// including after a snippet whose spoken form made random choices of its own.
func TestRenderPicksOnePhrasing(t *testing.T) {
	rt := MakeContactTransmission("[what altitude should we maintain|what altitude do you want us at]")
	rt.Add("{spd}", 210)
	rt.Add("[alpha|bravo|charlie]")

	for seed := uint64(1); seed <= 100; seed++ {
		r := rand.Make()
		r.Seed(seed)
		rd, err := rt.Render(r)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		for _, alts := range [][]string{{"what altitude should we maintain", "what altitude do you want us at"},
			{"alpha", "bravo", "charlie"}} {
			for _, a := range alts {
				if strings.Contains(rd.Written, a) != strings.Contains(rd.Spoken, a) {
					t.Errorf("seed %d: written %q and spoken %q differ on %q", seed, rd.Written, rd.Spoken, a)
				}
			}
		}
	}
}

// An empty alternative in the middle of a phrase leaves no double space
// behind in either rendering.
func TestRenderCollapsesEmptyAlternative(t *testing.T) {
	rt := MakeContactTransmission("on the arrival [at|] {alt}", 12000)
	for seed := uint64(1); seed <= 20; seed++ {
		r := rand.Make()
		r.Seed(seed)
		rd, err := rt.Render(r)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if strings.Contains(rd.Written, "  ") || strings.Contains(rd.Spoken, "  ") {
			t.Errorf("seed %d: double space in written %q or spoken %q", seed, rd.Written, rd.Spoken)
		}
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
		rt := MakeContactTransmission(test.phrase, &av.Controller{RadioName: test.radioName})
		rd, err := rt.Render(rand.Make())
		if err != nil {
			t.Errorf("%s with %q: %v", test.phrase, test.radioName, err)
		} else if rd.Written != test.want {
			t.Errorf("%s with %q = %q, want %q", test.phrase, test.radioName, rd.Written, test.want)
		}
	}
}

// A nil controller is reported rather than panicking in the formatter.
func TestNilControllerArgReported(t *testing.T) {
	rt := MakeContactTransmission("{actrl}", (*av.Controller)(nil))
	if rd, err := rt.Render(rand.Make()); rd != (Rendering{}) || err == nil {
		t.Errorf("Render with a nil controller = %+v, %v; want nothing and an error", rd, err)
	}
}

func TestFrequencySpoken(t *testing.T) {
	// Each frequency should give exactly these spoken forms, in particular
	// keeping the leading zero of fractions below .10.
	for _, fs := range []struct {
		f       av.Frequency
		spokens []string
	}{
		{f: av.Frequency(127050), spokens: []string{"27 zero five", "one 27 point zero five",
			"27 point zero five", "one two seven point zero five"}},
		{f: av.Frequency(118075), spokens: []string{"18 zero seven", "one 18 point zero seven",
			"18 point zero seven", "one one eight point zero seven"}},
		{f: av.Frequency(121900), spokens: []string{"21 90", "one 21 point 9", "21 point 9",
			"one two one point niner"}},
		{f: av.Frequency(133450), spokens: []string{"33 45", "one 33 point 45", "33 point 45",
			"one three three point four five"}},
		{f: av.Frequency(128000), spokens: []string{"28 zero", "one 28 point zero", "28 point zero",
			"one two eight point zero"}},
	} {
		r := rand.Make()
		var got []string
		for seed := range 100 {
			r.Seed(uint64(seed))
			s, err := (FrequencySnippetFormatter{}).Spoken(r, fs.f)
			if err != nil {
				t.Fatalf("%v: %v", fs.f, err)
			}
			if !slices.Contains(got, s) {
				got = append(got, s)
			}
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(fs.spokens))
		if !slices.Equal(got, want) {
			t.Errorf("Frequency %s spoken forms %q; expected %q", fs.f, got, want)
		}
	}
}

func TestAirwaySpoken(t *testing.T) {
	r := rand.Make()
	for id, want := range map[string]string{
		"V1":   "victor one",
		"V16":  "victor 16",
		"J80":  "jay 80",
		"T123": "tango one 23",
		"Q42":  "cue 42",
		"V1R":  "victor one Romeo",
	} {
		got, err := AirwaySnippetFormatter{}.Spoken(r, id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
		} else if got != want {
			t.Errorf("%s: spoken as %q, want %q", id, got, want)
		}
	}
}
