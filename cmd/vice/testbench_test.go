// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
)

func TestMatchReadback(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		step   TestBenchStep
		expect string
	}{
		{
			name:   "no expectations passes",
			text:   "anything at all",
			step:   TestBenchStep{},
			expect: "pass",
		},
		{
			name:   "matching expect",
			text:   "descending to 5,000",
			step:   TestBenchStep{ExpectReadback: StringOrArray{"5,000"}},
			expect: "pass",
		},
		{
			name:   "no match returns empty",
			text:   "descending to 5,000",
			step:   TestBenchStep{ExpectReadback: StringOrArray{"6,000"}},
			expect: "",
		},
		{
			name:   "any of multiple expects",
			text:   "slowing as much as we can",
			step:   TestBenchStep{ExpectReadback: StringOrArray{"slowest practical speed", "slowing as much as we can"}},
			expect: "pass",
		},
		{
			name:   "reject overrides pass",
			text:   "traffic in sight, will maintain visual separation",
			step:   TestBenchStep{ExpectReadback: StringOrArray{"traffic in sight"}, RejectReadback: "visual separation"},
			expect: "fail",
		},
		{
			name:   "reject without match",
			text:   "traffic in sight",
			step:   TestBenchStep{ExpectReadback: StringOrArray{"traffic in sight"}, RejectReadback: "visual separation"},
			expect: "pass",
		},
		{
			name:   "case insensitive expect",
			text:   "DESCENDING TO 5,000",
			step:   TestBenchStep{ExpectReadback: StringOrArray{"5,000"}},
			expect: "pass",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchReadback(tt.text, tt.step)
			if got != tt.expect {
				t.Errorf("matchReadback(%q, ...) = %q, want %q", tt.text, got, tt.expect)
			}
		})
	}
}

func TestMatchWaitFor(t *testing.T) {
	tests := []struct {
		name    string
		waitFor string
		event   sim.Event
		expect  bool
	}{
		{
			name:    "check_in matches contact type",
			waitFor: "check_in",
			event:   sim.Event{RadioTransmissionType: av.RadioTransmissionContact},
			expect:  true,
		},
		{
			name:    "check_in rejects readback type",
			waitFor: "check_in",
			event:   sim.Event{RadioTransmissionType: av.RadioTransmissionReadback, WrittenText: "checking in"},
			expect:  false,
		},
		{
			name:    "approach_clearance_request",
			waitFor: "approach_clearance_request",
			event:   sim.Event{WrittenText: "looking for the approach"},
			expect:  true,
		},
		{
			name:    "go_around",
			waitFor: "go_around",
			event:   sim.Event{WrittenText: "going around"},
			expect:  true,
		},
		{
			name:    "traffic_in_sight matches",
			waitFor: "traffic_in_sight",
			event:   sim.Event{WrittenText: "we have the traffic"},
			expect:  true,
		},
		{
			name:    "traffic_in_sight rejects looking",
			waitFor: "traffic_in_sight",
			event:   sim.Event{WrittenText: "looking"},
			expect:  false,
		},
		{
			name:    "traffic_response matches looking",
			waitFor: "traffic_response",
			event:   sim.Event{WrittenText: "looking"},
			expect:  true,
		},
		{
			name:    "traffic_response matches in sight",
			waitFor: "traffic_response",
			event:   sim.Event{WrittenText: "traffic in sight"},
			expect:  true,
		},
		{
			name:    "traffic_response matches imc",
			waitFor: "traffic_response",
			event:   sim.Event{WrittenText: "we're in IMC"},
			expect:  true,
		},
		{
			name:    "unknown wait_for never matches",
			waitFor: "nonexistent",
			event:   sim.Event{WrittenText: "anything"},
			expect:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchWaitFor(tt.waitFor, tt.event)
			if got != tt.expect {
				t.Errorf("matchWaitFor(%q, ...) = %v, want %v", tt.waitFor, got, tt.expect)
			}
		})
	}
}

// newTestBench creates a minimal TestBench with the given case activated.
func newTestBench(tc *TestBenchCase) *TestBench {
	tb := &TestBench{
		activeTest:  tc,
		spawnedTest: tc,
		currentStep: 0,
		stepResults: make([]string, len(tc.Steps)),
		callsignMap: map[int]string{0: "AAL123"},
	}
	return tb
}

func readbackEvent(cs string, text string) sim.Event {
	return sim.Event{
		ADSBCallsign:          av.ADSBCallsign(cs),
		WrittenText:           text,
		RadioTransmissionType: av.RadioTransmissionReadback,
	}
}

func contactEvent(cs string, text string) sim.Event {
	return sim.Event{
		ADSBCallsign:          av.ADSBCallsign(cs),
		WrittenText:           text,
		RadioTransmissionType: av.RadioTransmissionContact,
	}
}

func TestProcessCommandStep(t *testing.T) {
	t.Run("advances on matching readback", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "S210", ExpectReadback: StringOrArray{"210 knots"}},
			},
		}
		tb := newTestBench(tc)
		tb.processCommandStep(tc, tc.Steps[0], readbackEvent("AAL123", "210 knots"))
		if tb.stepResults[0] != "pass" {
			t.Errorf("step result = %q, want pass", tb.stepResults[0])
		}
		if tb.currentStep != 1 {
			t.Errorf("currentStep = %d, want 1", tb.currentStep)
		}
	})

	t.Run("ignores non-readback events", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "S210", ExpectReadback: StringOrArray{"210 knots"}},
			},
		}
		tb := newTestBench(tc)
		tb.processCommandStep(tc, tc.Steps[0], contactEvent("AAL123", "210 knots"))
		if tb.stepResults[0] != "" {
			t.Errorf("step result = %q, want empty", tb.stepResults[0])
		}
	})

	t.Run("ignores wrong callsign", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "S210", ExpectReadback: StringOrArray{"210 knots"}},
			},
		}
		tb := newTestBench(tc)
		tb.processCommandStep(tc, tc.Steps[0], readbackEvent("UAL456", "210 knots"))
		if tb.stepResults[0] != "" {
			t.Errorf("step result = %q, want empty", tb.stepResults[0])
		}
	})

	t.Run("ignores say again", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "S210", ExpectReadback: StringOrArray{"210 knots"}},
			},
		}
		tb := newTestBench(tc)
		tb.processCommandStep(tc, tc.Steps[0], readbackEvent("AAL123", "say again?"))
		if tb.stepResults[0] != "" {
			t.Errorf("step result = %q, want empty", tb.stepResults[0])
		}
	})

	t.Run("fire and forget advances immediately", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "E{approach}"},
			},
		}
		tb := newTestBench(tc)
		tb.processCommandStep(tc, tc.Steps[0], readbackEvent("AAL123", "expect ILS 28R"))
		if tb.stepResults[0] != "pass" {
			t.Errorf("step result = %q, want pass", tb.stepResults[0])
		}
	})

	t.Run("reject readback fails step", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "H090", ExpectReadback: StringOrArray{"090"}, RejectReadback: "unable"},
			},
		}
		tb := newTestBench(tc)
		tb.processCommandStep(tc, tc.Steps[0], readbackEvent("AAL123", "unable heading 090"))
		if tb.stepResults[0] != "fail" {
			t.Errorf("step result = %q, want fail", tb.stepResults[0])
		}
	})

	t.Run("non-matching readback leaves step pending", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "S210", ExpectReadback: StringOrArray{"210 knots"}},
			},
		}
		tb := newTestBench(tc)
		tb.processCommandStep(tc, tc.Steps[0], readbackEvent("AAL123", "looking for the approach"))
		if tb.stepResults[0] != "" {
			t.Errorf("step result = %q, want empty", tb.stepResults[0])
		}
		if tb.currentStep != 0 {
			t.Errorf("currentStep = %d, want 0", tb.currentStep)
		}
	})
}

func TestProcessWaitForStep(t *testing.T) {
	t.Run("advances on match", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{WaitFor: "check_in"},
			},
		}
		tb := newTestBench(tc)
		tb.processWaitForStep(tc.Steps[0], contactEvent("AAL123", "checking in at 5000"))
		if tb.stepResults[0] != "pass" {
			t.Errorf("step result = %q, want pass", tb.stepResults[0])
		}
		if tb.currentStep != 1 {
			t.Errorf("currentStep = %d, want 1", tb.currentStep)
		}
	})

	t.Run("stays on non-match", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{WaitFor: "traffic_in_sight"},
			},
		}
		tb := newTestBench(tc)
		tb.processWaitForStep(tc.Steps[0], readbackEvent("AAL123", "looking"))
		if tb.stepResults[0] != "" {
			t.Errorf("step result = %q, want empty", tb.stepResults[0])
		}
		if tb.currentStep != 0 {
			t.Errorf("currentStep = %d, want 0", tb.currentStep)
		}
	})
}

func TestSpeculativeAdvance(t *testing.T) {
	t.Run("does not advance fire-and-forget after command", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "S210", ExpectReadback: StringOrArray{"210 knots"}},
				{Command: "E{approach}"},                                        // fire-and-forget
				{Command: "D050", ExpectReadback: StringOrArray{"5,000"}},
			},
		}
		tb := newTestBench(tc)
		tb.stepResults[0] = "pass"
		tb.currentStep = 1

		tb.speculativeAdvance(tc, readbackEvent("AAL123", "210 knots"))

		// Should NOT advance: step 0 is a command, not a wait_for.
		if tb.stepResults[1] != "" {
			t.Errorf("step 1 result = %q, want empty", tb.stepResults[1])
		}
		if tb.currentStep != 1 {
			t.Errorf("currentStep = %d, want 1", tb.currentStep)
		}
	})

	t.Run("advances fire-and-forget after wait_for", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{WaitFor: "field_in_sight"},
				{Command: "CVA"},                                                // fire-and-forget after wait
				{Command: "D050", ExpectReadback: StringOrArray{"5,000"}},
			},
		}
		tb := newTestBench(tc)
		tb.stepResults[0] = "pass"
		tb.currentStep = 1

		tb.speculativeAdvance(tc, sim.Event{WrittenText: "field in sight"})

		// Should advance past fire-and-forget (step 1) since previous was wait_for.
		if tb.stepResults[1] != "pass" {
			t.Errorf("step 1 result = %q, want pass", tb.stepResults[1])
		}
		if tb.currentStep != 2 {
			t.Errorf("currentStep = %d, want 2", tb.currentStep)
		}
	})

	t.Run("advances through matching wait_for", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "TRAFFIC/12/3/50"},       // fire-and-forget
				{WaitFor: "traffic_in_sight"},
			},
		}
		tb := newTestBench(tc)
		tb.stepResults[0] = "pass"
		tb.currentStep = 1

		tb.speculativeAdvance(tc, sim.Event{WrittenText: "we have the traffic"})

		if tb.stepResults[1] != "pass" {
			t.Errorf("step 1 result = %q, want pass", tb.stepResults[1])
		}
		if tb.currentStep != 2 {
			t.Errorf("currentStep = %d, want 2", tb.currentStep)
		}
	})

	t.Run("stops at command with expect_readback", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{WaitFor: "traffic_in_sight"},
				{Command: "VISSEP", ExpectReadback: StringOrArray{"visual separation"}},
			},
		}
		tb := newTestBench(tc)
		tb.stepResults[0] = "pass"
		tb.currentStep = 1

		// The event text matches VISSEP's expect, but the command hasn't been
		// issued yet so speculative advance must not match it.
		tb.speculativeAdvance(tc, sim.Event{
			WrittenText: "we have the traffic, will maintain visual separation",
		})

		if tb.stepResults[1] != "" {
			t.Errorf("step 1 result = %q, want empty (should not advance)", tb.stepResults[1])
		}
		if tb.currentStep != 1 {
			t.Errorf("currentStep = %d, want 1", tb.currentStep)
		}
	})

	t.Run("stops at non-matching wait_for", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "E{approach}"},     // fire-and-forget
				{WaitFor: "go_around"},
			},
		}
		tb := newTestBench(tc)
		tb.stepResults[0] = "pass"
		tb.currentStep = 1

		tb.speculativeAdvance(tc, readbackEvent("AAL123", "expect ILS 28R"))

		if tb.stepResults[1] != "" {
			t.Errorf("step 1 result = %q, want empty", tb.stepResults[1])
		}
		if tb.currentStep != 1 {
			t.Errorf("currentStep = %d, want 1", tb.currentStep)
		}
	})

	t.Run("does not advance step 0", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{Command: "S210", ExpectReadback: StringOrArray{"210 knots"}},
			},
		}
		tb := newTestBench(tc)
		// Step 0 hasn't been evaluated by processEvents yet.
		tb.speculativeAdvance(tc, readbackEvent("AAL123", "210 knots"))

		if tb.stepResults[0] != "" {
			t.Errorf("step 0 result = %q, want empty", tb.stepResults[0])
		}
		if tb.currentStep != 0 {
			t.Errorf("currentStep = %d, want 0", tb.currentStep)
		}
	})

	t.Run("chains fire-and-forget after wait_for then wait_for", func(t *testing.T) {
		tc := &TestBenchCase{
			Steps: []TestBenchStep{
				{WaitFor: "field_in_sight"},
				{Command: "E{approach}"},                                  // fire-and-forget after wait
				{Command: "D{if}"},                                        // fire-and-forget after fire-and-forget
				{WaitFor: "approach_clearance_request"},
			},
		}
		tb := newTestBench(tc)
		tb.stepResults[0] = "pass"
		tb.currentStep = 1

		tb.speculativeAdvance(tc, sim.Event{WrittenText: "looking for the approach"})

		// Should chain: step 1 advances (prev is wait_for), step 2 advances
		// (prev is advanced fire-and-forget), step 3 advances (wait_for matches).
		if tb.currentStep != 4 {
			t.Errorf("currentStep = %d, want 4", tb.currentStep)
		}
		for i := 1; i <= 3; i++ {
			if tb.stepResults[i] != "pass" {
				t.Errorf("step %d result = %q, want pass", i, tb.stepResults[i])
			}
		}
	})
}
