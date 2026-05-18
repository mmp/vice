// sim/radio_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
)

func newSimWithRadio(t *testing.T) *Sim {
	t.Helper()
	s := &Sim{
		eventStream: NewEventStream(nil),
		State:       &CommonState{},
	}
	s.State.SimTime = NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	return s
}

func TestPostRadioTransmission_StampsPlayAtAtSimTimePlusBuffer(t *testing.T) {
	s := newSimWithRadio(t)
	sub := s.eventStream.Subscribe()
	defer sub.Unsubscribe()

	s.postRadioTransmission(Event{
		Type:                  RadioTransmissionEvent,
		ADSBCallsign:          av.ADSBCallsign("AAL123"),
		DestinationTCW:        "TCW-1",
		WrittenText:           "American 123, climb and maintain 8000",
		SpokenText:            "American 123, climb and maintain 8000",
		RadioTransmissionType: av.RadioTransmissionReadback,
	})

	events := sub.Get()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	want := s.State.SimTime.Add(playAtForwardBuffer)
	if !events[0].PlayAt.Equal(want) {
		t.Errorf("PlayAt = %v, want %v", events[0].PlayAt, want)
	}
}

func TestPostRadioTransmission_AdvancesRadioHoldUntil(t *testing.T) {
	s := newSimWithRadio(t)

	s.postRadioTransmission(Event{
		Type:                  RadioTransmissionEvent,
		DestinationTCW:        "TCW-1",
		SpokenText:            "test",
		RadioTransmissionType: av.RadioTransmissionReadback,
	})

	d := s.EnsureTCWDisplay("TCW-1")
	if d.RadioHoldUntil.IsZero() {
		t.Fatal("RadioHoldUntil not advanced")
	}
	playAt := s.State.SimTime.Add(playAtForwardBuffer)
	wantMin := playAt.Add(time.Duration(4) * msPerChar)
	if d.RadioHoldUntil.Before(wantMin) {
		t.Errorf("RadioHoldUntil %v earlier than minimum %v", d.RadioHoldUntil, wantMin)
	}
}

func TestPostRadioTransmission_BackToBackAnchorsToPrevious(t *testing.T) {
	s := newSimWithRadio(t)

	playAt1 := s.postRadioTransmission(Event{
		Type:                  RadioTransmissionEvent,
		DestinationTCW:        "TCW-1",
		SpokenText:            "first transmission",
		RadioTransmissionType: av.RadioTransmissionReadback,
	})
	playAt2 := s.postRadioTransmission(Event{
		Type:                  RadioTransmissionEvent,
		DestinationTCW:        "TCW-1",
		SpokenText:            "second transmission",
		RadioTransmissionType: av.RadioTransmissionReadback,
	})

	end1 := playAt1.Add(pilotTransmissionDurationEstimate("first transmission")).Add(postReadbackPad)
	if playAt2.Before(end1) {
		t.Errorf("second PlayAt %v should be at or after first end %v", playAt2, end1)
	}
}

func TestPostRadioTransmission_DifferentTCWsIndependent(t *testing.T) {
	s := newSimWithRadio(t)

	s.postRadioTransmission(Event{
		Type:                  RadioTransmissionEvent,
		DestinationTCW:        "TCW-1",
		SpokenText:            "tcw-1 traffic",
		RadioTransmissionType: av.RadioTransmissionReadback,
	})

	d2 := s.EnsureTCWDisplay("TCW-2")
	if !d2.RadioHoldUntil.IsZero() {
		t.Errorf("TCW-2 RadioHoldUntil should be untouched; got %v", d2.RadioHoldUntil)
	}
}

func TestPostRadioTransmission_DoesNotShrinkRadioHoldUntil(t *testing.T) {
	s := newSimWithRadio(t)

	// Pre-seed RadioHoldUntil far in the future (simulates a controller
	// PTT in progress, per Task 4 — extends to +60s).
	d := s.EnsureTCWDisplay("TCW-1")
	preseed := s.State.SimTime.Add(30 * time.Second)
	d.RadioHoldUntil = preseed

	// Post a short transmission. Its endTime (PlayAt + 4*70ms + 3s pad)
	// is well below 30s in the future, so RadioHoldUntil should not move.
	playAt := s.postRadioTransmission(Event{
		Type:                  RadioTransmissionEvent,
		DestinationTCW:        "TCW-1",
		SpokenText:            "test",
		RadioTransmissionType: av.RadioTransmissionReadback,
	})

	// PlayAt should be clamped to the existing RadioHoldUntil floor.
	if !playAt.Equal(preseed) {
		t.Errorf("PlayAt should clamp up to RadioHoldUntil; got %v want %v", playAt, preseed)
	}
	// And RadioHoldUntil must not have shrunk (it may advance due to the
	// transmission duration + pad, but must be >= preseed).
	if d.RadioHoldUntil.Before(preseed) {
		t.Errorf("RadioHoldUntil shrank: was %v, now %v", preseed, d.RadioHoldUntil)
	}
}

func TestPilotTransmissionDurationEstimate(t *testing.T) {
	cases := []struct {
		name string
		text string
		want time.Duration
	}{
		{"empty", "", 0},
		{"short", "test", 4 * 70 * time.Millisecond},
		{"hello", "hello", 5 * 70 * time.Millisecond},
		{"realistic readback", "American 123, climb maintain 8000", time.Duration(len("American 123, climb maintain 8000")) * 70 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pilotTransmissionDurationEstimate(tc.text)
			if got != tc.want {
				t.Errorf("pilotTransmissionDurationEstimate(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
