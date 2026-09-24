// speech/intent_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package speech

import (
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/rand"
)

func renderIntentForTest(t *testing.T, intent CommandIntent, seed uint64) string {
	t.Helper()

	if db.DB == nil {
		db.DB = &db.StaticDatabase{
			Navaids:  map[string]db.Navaid{},
			Airports: map[av.ICAOAirportCode]db.Airport{},
		}
	}

	r := rand.Make()
	r.Seed(seed)
	written, err := RenderIntents([]CommandIntent{intent}, r).Written(r)
	if err != nil {
		t.Fatalf("seed %d: %v", seed, err)
	}
	return strings.ToLower(written)
}

func assertContainsAny(t *testing.T, readback string, values ...string) {
	t.Helper()

	for _, s := range values {
		if strings.Contains(readback, s) {
			return
		}
	}
	t.Fatalf("readback %q does not contain any of %v", readback, values)
}

func TestSpeedRestrictionReadbackIncludesQualifier(t *testing.T) {
	for _, test := range []struct {
		name       string
		intent     SpeedIntent
		qualifiers []string
	}{
		{
			name:       "bare or greater",
			intent:     SpeedIntent{Speed: 250, Type: SpeedAtOrAbove},
			qualifiers: []string{"or greater", "or more"},
		},
		{
			name:       "bare or less",
			intent:     SpeedIntent{Speed: 210, Type: SpeedAtOrBelow},
			qualifiers: []string{"or less", "do not exceed", "not exceeding"},
		},
		{
			name:       "after fix or greater",
			intent:     SpeedIntent{Speed: 250, Type: SpeedAtOrAbove, AfterFix: "ROSLY"},
			qualifiers: []string{"or greater"},
		},
		{
			name:       "after fix or less",
			intent:     SpeedIntent{Speed: 210, Type: SpeedAtOrBelow, AfterFix: "ROSLY"},
			qualifiers: []string{"or less", "do not exceed"},
		},
		{
			name:       "until fix or greater",
			intent:     SpeedIntent{Speed: 250, Type: SpeedAtOrAbove, Until: &SpeedUntil{Fix: "ROSLY"}},
			qualifiers: []string{"or greater"},
		},
		{
			name:       "until fix or less",
			intent:     SpeedIntent{Speed: 210, Type: SpeedAtOrBelow, Until: &SpeedUntil{Fix: "ROSLY"}},
			qualifiers: []string{"or less"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for seed := uint64(1); seed <= 20; seed++ {
				readback := renderIntentForTest(t, test.intent, seed)
				assertContainsAny(t, readback, test.qualifiers...)
				if !strings.Contains(readback, "knots") {
					t.Fatalf("speed readback missing speed: %q", readback)
				}
				if test.intent.Until != nil && !strings.Contains(readback, "until") {
					t.Fatalf("speed readback missing until: %q", readback)
				}
			}
		})
	}
}

// TestProcedureExceptReadback verifies that the altitude and speed excepted
// from a climb via SID or descend via STAR are read back with it, including
// a speed given as a separate command.
func TestProcedureExceptReadback(t *testing.T) {
	alt := float32(10000)
	current := float32(8000)
	for _, test := range []struct {
		name     string
		intents  []CommandIntent
		want     []string
		unwanted string
	}{
		{
			name: "climbing back with speed",
			intents: []CommandIntent{ProcedureIntent{Type: ProcedureDescendViaSTAR, ExceptAltitude: &alt, CurrentAltitude: &current},
				SpeedIntent{Speed: 250, Type: SpeedAssign}},
			want:     []string{"descend via the star, except maintain 10,000 and 250 knots", "okay, but we're", "at 8,000 currently"},
			unwanted: "up at",
		},
		{
			name: "descending back with speed",
			intents: []CommandIntent{ProcedureIntent{Type: ProcedureClimbViaSID, ExceptAltitude: &current, CurrentAltitude: &alt},
				SpeedIntent{Speed: 250, Type: SpeedAssign}},
			want:     []string{"climb via the sid, except maintain 8,000 and 250 knots", "okay, but we're", "at 10,000 currently"},
			unwanted: "down at",
		},
		{
			name:    "altitude",
			intents: []CommandIntent{ProcedureIntent{Type: ProcedureClimbViaSID, ExceptAltitude: &alt}},
			want:    []string{"climb via the sid, except maintain 10,000"},
		},
		{
			name: "speed",
			intents: []CommandIntent{ProcedureIntent{Type: ProcedureDescendViaSTAR},
				SpeedIntent{Speed: 250, Type: SpeedReduce}},
			want: []string{"descend via the star, except maintain 250 knots"},
		},
		{
			name: "altitude and speed or greater",
			intents: []CommandIntent{ProcedureIntent{Type: ProcedureClimbViaSID, ExceptAltitude: &alt},
				SpeedIntent{Speed: 250, Type: SpeedAtOrAbove}},
			want: []string{"climb via the sid, except maintain 10,000 and 250 knots or greater"},
		},
		{
			name: "mach",
			intents: []CommandIntent{ProcedureIntent{Type: ProcedureDescendViaSTAR},
				SpeedIntent{Speed: 0.78, Type: SpeedAssign, Mach: true}},
			want: []string{"descend via the star, except maintain mach .78"},
		},
		{
			name: "speed after a fix stays separate",
			intents: []CommandIntent{ProcedureIntent{Type: ProcedureDescendViaSTAR},
				SpeedIntent{Speed: 250, Type: SpeedAssign, AfterFix: "ROSLY"}},
			want:     []string{"descend via the star"},
			unwanted: "except",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for seed := uint64(1); seed <= 5; seed++ {
				r := rand.Make()
				r.Seed(seed)
				written, err := RenderIntents(test.intents, r).Written(r)
				if err != nil {
					t.Fatalf("seed %d: %v", seed, err)
				}
				readback := strings.ToLower(written)
				for _, w := range test.want {
					if !strings.Contains(readback, w) {
						t.Fatalf("readback %q does not contain %q", readback, w)
					}
				}
				if test.unwanted != "" && strings.Contains(readback, test.unwanted) {
					t.Fatalf("readback %q contains %q", readback, test.unwanted)
				}
			}
		})
	}
}

func TestSpeedUntilFinalDirection(t *testing.T) {
	for _, test := range []struct {
		name        string
		intent      SpeedIntent
		contains    []string // at least one of these must appear
		notContains []string // none of these may appear
	}{
		{
			name:        "reduce",
			intent:      SpeedIntent{Speed: 190, Type: SpeedUntilFinal, UntilFinalDirection: SpeedReduce},
			contains:    []string{"slow", "reduce", "back to"},
			notContains: []string{"keep it at", "maintain"},
		},
		{
			name:        "increase",
			intent:      SpeedIntent{Speed: 230, Type: SpeedUntilFinal, UntilFinalDirection: SpeedIncrease},
			contains:    []string{"increase", "speed up", "on the speed"},
			notContains: []string{"keep it at"},
		},
		{
			name:        "assign",
			intent:      SpeedIntent{Speed: 210, Type: SpeedUntilFinal, UntilFinalDirection: SpeedAssign},
			contains:    []string{"for now"},
			notContains: []string{"keep it at"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for seed := uint64(1); seed <= 20; seed++ {
				readback := renderIntentForTest(t, test.intent, seed)
				assertContainsAny(t, readback, test.contains...)
				for _, bad := range test.notContains {
					if strings.Contains(readback, bad) {
						t.Fatalf("readback %q contains forbidden substring %q (seed %d)", readback, bad, seed)
					}
				}
			}
		})
	}
}

func TestContactTowerReadback(t *testing.T) {
	// No frequency — readback is bare "tower" with no digits.
	for seed := uint64(1); seed <= 20; seed++ {
		readback := renderIntentForTest(t, ContactTowerIntent{}, seed)
		if !strings.Contains(readback, "tower") {
			t.Fatalf("bare contact-tower readback missing 'tower': %q", readback)
		}
		if strings.ContainsAny(readback, "0123456789") {
			t.Fatalf("bare contact-tower readback unexpectedly contains digits: %q", readback)
		}
	}

	// With frequency — across seeds, the readback sometimes includes the
	// frequency and sometimes does not. When present it must match the
	// canonical Frequency formatting.
	freq := av.NewFrequency(118.9)
	// Readback uses the FrequencySnippetFormatter's Written form (2 decimal
	// places), not Frequency.String()'s 3-decimal form.
	expected, err := FrequencySnippetFormatter{}.Written(freq)
	if err != nil {
		t.Fatal(err)
	}
	withFreq, withoutFreq := 0, 0
	for seed := uint64(1); seed <= 60; seed++ {
		readback := renderIntentForTest(t, ContactTowerIntent{Frequency: freq}, seed)
		if !strings.Contains(readback, "tower") {
			t.Fatalf("contact-tower readback missing 'tower': %q", readback)
		}
		if strings.Contains(readback, expected) {
			withFreq++
		} else if strings.ContainsAny(readback, "0123456789") {
			t.Fatalf("contact-tower readback has digits but not the expected freq %q: %q", expected, readback)
		} else {
			withoutFreq++
		}
	}
	if withFreq == 0 {
		t.Fatalf("expected some readbacks to include the frequency; got none in 60 seeds")
	}
	if withoutFreq == 0 {
		t.Fatalf("expected some readbacks to omit the frequency; got none in 60 seeds")
	}
}

func TestCompoundSpeedReadbackIncludesQualifiers(t *testing.T) {
	above := av.MakeAtOrAboveSpeedRestriction(250)
	below := av.MakeAtOrBelowSpeedRestriction(210)
	intent := CompoundSpeedIntent{
		Segments: []CompoundSpeedSegment{
			{Speed: &above, UntilFix: "ROSLY"},
			{Speed: &below},
		},
	}

	for seed := uint64(1); seed <= 20; seed++ {
		readback := renderIntentForTest(t, intent, seed)
		assertContainsAny(t, readback, "or greater")
		assertContainsAny(t, readback, "or less")
		if !strings.Contains(readback, "rosly") {
			t.Fatalf("compound speed readback missing until fix: %q", readback)
		}
	}
}
