// aviation/db_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"testing"
)

func seedTestPerformance(t *testing.T) {
	perf := func(engine, weight string) AircraftPerformance {
		var p AircraftPerformance
		p.Engine.AircraftType = engine
		p.WeightClass = weight
		return p
	}

	oldDB := DB
	DB = &StaticDatabase{AircraftPerformance: map[string]AircraftPerformance{
		"B738": perf("J", "L"),
		"B77W": perf("J", "H"),
		"A388": perf("J", "J"),
		"C172": perf("P", "L"),
		"DH8D": perf("T", "L"),
	}}
	t.Cleanup(func() { DB = oldDB })
}

func TestAircraftClassMatches(t *testing.T) {
	seedTestPerformance(t)

	jet := AircraftClassHeavyJet | AircraftClassNonheavyJet
	for _, tc := range []struct {
		class  AircraftClass
		acType string
		want   bool
	}{
		{0, "B738", true},
		{0, "ZZZZ", true},
		{jet, "B738", true},
		{jet, "B77W", true},
		{jet, "C172", false},
		{AircraftClassHeavyJet, "B77W", true},
		{AircraftClassHeavyJet, "A388", true},
		{AircraftClassHeavyJet, "B738", false},
		{AircraftClassNonheavyJet, "B738", true},
		{AircraftClassNonheavyJet, "B77W", false},
		{AircraftClassProp, "C172", true},
		{AircraftClassProp, "DH8D", false},
		{AircraftClassTurboprop, "DH8D", true},
		{AircraftClassProp | AircraftClassTurboprop, "DH8D", true},
		{AircraftClassProp, "ZZZZ", false},
	} {
		if got := tc.class.Matches(tc.acType); got != tc.want {
			t.Errorf("class %b Matches(%s) = %v, want %v", tc.class, tc.acType, got, tc.want)
		}
	}
}

func TestAircraftClassJSON(t *testing.T) {
	for _, tc := range []struct {
		json string
		want AircraftClass
	}{
		{`"prop"`, AircraftClassProp},
		{`"jet"`, AircraftClassHeavyJet | AircraftClassNonheavyJet},
		{`"heavy"`, AircraftClassHeavyJet},
		{`"nonheavy"`, AircraftClassNonheavyJet},
		{`["prop", "turboprop"]`, AircraftClassProp | AircraftClassTurboprop},
		{`["heavy", "nonheavy"]`, AircraftClassHeavyJet | AircraftClassNonheavyJet},
	} {
		var c AircraftClass
		if err := json.Unmarshal([]byte(tc.json), &c); err != nil {
			t.Errorf("%s: %v", tc.json, err)
		} else if c != tc.want {
			t.Errorf("%s: got %b, want %b", tc.json, c, tc.want)
		}

		// The minimal marshaled form must unmarshal back to the same bits.
		b, err := json.Marshal(c)
		if err != nil {
			t.Errorf("%s: marshal: %v", tc.json, err)
			continue
		}
		var rt AircraftClass
		if err := json.Unmarshal(b, &rt); err != nil {
			t.Errorf("%s: round trip: %v", string(b), err)
		} else if rt != c {
			t.Errorf("%s: round trip via %s gave %b, want %b", tc.json, string(b), rt, c)
		}
	}

	var c AircraftClass
	if err := json.Unmarshal([]byte(`"floatplane"`), &c); err == nil {
		t.Errorf("unknown class did not error")
	}
	if err := json.Unmarshal([]byte(`["jet", "wrong"]`), &c); err == nil {
		t.Errorf("unknown class in list did not error")
	}
}
