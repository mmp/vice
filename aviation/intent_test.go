// aviation/intent_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"strings"
	"testing"

	"github.com/mmp/vice/rand"
)

func renderIntentForTest(intent CommandIntent, seed uint64) string {
	if DB == nil {
		DB = &StaticDatabase{
			Navaids:  map[string]Navaid{},
			Airports: map[string]FAAAirport{},
		}
	}

	r := &rand.Rand{PCG32: rand.NewPCG32()}
	r.Seed(seed)
	return strings.ToLower(RenderIntents([]CommandIntent{intent}, r).Written(r))
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
				readback := renderIntentForTest(test.intent, seed)
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
				readback := renderIntentForTest(test.intent, seed)
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
		readback := renderIntentForTest(ContactTowerIntent{}, seed)
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
	freq := NewFrequency(118.9)
	// Readback uses the FrequencySnippetFormatter's Written form (2 decimal
	// places), not Frequency.String()'s 3-decimal form.
	expected := FrequencySnippetFormatter{}.Written(freq)
	withFreq, withoutFreq := 0, 0
	for seed := uint64(1); seed <= 60; seed++ {
		readback := renderIntentForTest(ContactTowerIntent{Frequency: freq}, seed)
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

type stubConditionalAction struct{ text string }

func (s stubConditionalAction) Render(rt *RadioTransmission, r *rand.Rand) {
	rt.Add(s.text)
}

func TestConditionalCommandIntentRender(t *testing.T) {
	t.Run("leaving", func(t *testing.T) {
		intent := ConditionalCommandIntent{
			Kind:     ConditionalLeaving,
			Altitude: 3000,
			Action:   stubConditionalAction{text: "fly heading 010"},
		}
		for seed := uint64(1); seed <= 20; seed++ {
			readback := renderIntentForTest(intent, seed)
			assertContainsAny(t, readback, "leaving", "passing")
			if !strings.Contains(readback, "fly heading 010") {
				t.Fatalf("leaving readback missing action text: %q", readback)
			}
		}
	})

	t.Run("reaching", func(t *testing.T) {
		intent := ConditionalCommandIntent{
			Kind:     ConditionalReaching,
			Altitude: 10000,
			Action:   stubConditionalAction{text: "fly heading 010"},
		}
		for seed := uint64(1); seed <= 20; seed++ {
			readback := renderIntentForTest(intent, seed)
			assertContainsAny(t, readback, "reaching", "level at", "on reaching")
			if !strings.Contains(readback, "fly heading 010") {
				t.Fatalf("reaching readback missing action text: %q", readback)
			}
		}
	})
}

func TestCompoundSpeedReadbackIncludesQualifiers(t *testing.T) {
	above := MakeAtOrAboveSpeedRestriction(250)
	below := MakeAtOrBelowSpeedRestriction(210)
	intent := CompoundSpeedIntent{
		Segments: []CompoundSpeedSegment{
			{Speed: &above, UntilFix: "ROSLY"},
			{Speed: &below},
		},
	}

	for seed := uint64(1); seed <= 20; seed++ {
		readback := renderIntentForTest(intent, seed)
		assertContainsAny(t, readback, "or greater")
		assertContainsAny(t, readback, "or less")
		if !strings.Contains(readback, "rosly") {
			t.Fatalf("compound speed readback missing until fix: %q", readback)
		}
	}
}
