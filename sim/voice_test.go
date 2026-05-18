// sim/voice_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
	"time"
)

func newSimWithVoice(t *testing.T) *Sim {
	t.Helper()
	s := &Sim{
		eventStream: NewEventStream(nil),
		State:       &CommonState{},
	}
	return s
}

func TestStartPTT_GrantsWhenIdle(t *testing.T) {
	s := newSimWithVoice(t)
	if !s.StartPTT("TCW-1", "tok-A") {
		t.Fatal("StartPTT should grant when idle")
	}
}

func TestStartPTT_DeniesWhenSomeoneElseTalking(t *testing.T) {
	s := newSimWithVoice(t)
	s.StartPTT("TCW-1", "tok-A")
	if s.StartPTT("TCW-1", "tok-B") {
		t.Fatal("StartPTT for tok-B should be denied while tok-A holds the slot")
	}
}

func TestStartPTT_AllowsSameTokenReentrant(t *testing.T) {
	s := newSimWithVoice(t)
	s.StartPTT("TCW-1", "tok-A")
	if !s.StartPTT("TCW-1", "tok-A") {
		t.Fatal("StartPTT for the active talker token should remain granted")
	}
}

func TestStartPTT_DifferentTCWsAreIndependent(t *testing.T) {
	s := newSimWithVoice(t)
	if !s.StartPTT("TCW-1", "tok-A") {
		t.Fatal("StartPTT TCW-1 tok-A should grant")
	}
	if !s.StartPTT("TCW-2", "tok-B") {
		t.Fatal("StartPTT TCW-2 tok-B should grant (different TCW)")
	}
}

func TestStopPTT_ClearsSlotForActiveTalker(t *testing.T) {
	s := newSimWithVoice(t)
	s.StartPTT("TCW-1", "tok-A")
	s.StopPTT("TCW-1", "tok-A")
	if !s.StartPTT("TCW-1", "tok-B") {
		t.Fatal("after StopPTT, TCW-1 should be free for tok-B")
	}
}

func TestStopPTT_NoOpForNonTalker(t *testing.T) {
	s := newSimWithVoice(t)
	s.StartPTT("TCW-1", "tok-A")
	s.StopPTT("TCW-1", "tok-B") // tok-B isn't the talker
	if s.StartPTT("TCW-1", "tok-C") {
		t.Fatal("tok-A should still hold TCW-1 (tok-B's StopPTT was a no-op)")
	}
}

func TestClearTalkerForToken_ClearsAllTCWsHeldByToken(t *testing.T) {
	s := newSimWithVoice(t)
	s.StartPTT("TCW-1", "tok-A")
	s.ClearTalkerForToken("tok-A")
	if !s.StartPTT("TCW-1", "tok-B") {
		t.Fatal("ClearTalkerForToken should free the slot")
	}
}

func TestRecordPTTChunk_PostsEventForActiveTalker(t *testing.T) {
	s := newSimWithVoice(t)
	sub := s.eventStream.Subscribe()
	defer sub.Unsubscribe()

	s.StartPTT("TCW-1", "tok-A")
	s.RecordPTTChunk("TCW-1", "tok-A", []int16{10, 20, 30})

	got := sub.Get()
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	e := got[0]
	if e.Type != PeerVoiceEvent {
		t.Fatalf("got Type=%v, want PeerVoiceEvent", e.Type)
	}
	if string(e.SourceTCW) != "TCW-1" || e.SenderToken != "tok-A" || len(e.VoiceChunk) != 3 || e.VoiceEnd {
		t.Errorf("event = %+v", e)
	}
}

func TestRecordPTTChunk_DropsIfNotActiveTalker(t *testing.T) {
	s := newSimWithVoice(t)
	sub := s.eventStream.Subscribe()
	defer sub.Unsubscribe()

	s.StartPTT("TCW-1", "tok-A")
	s.RecordPTTChunk("TCW-1", "tok-IMPOSTER", []int16{1, 2, 3})

	if got := sub.Get(); len(got) != 0 {
		t.Fatalf("expected no events for non-talker chunk, got %d", len(got))
	}
}

func TestStopPTT_PostsEndEvent(t *testing.T) {
	s := newSimWithVoice(t)
	sub := s.eventStream.Subscribe()
	defer sub.Unsubscribe()

	s.StartPTT("TCW-1", "tok-A")
	s.StopPTT("TCW-1", "tok-A")

	got := sub.Get()
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (end event)", len(got))
	}
	if got[0].Type != PeerVoiceEvent || !got[0].VoiceEnd {
		t.Errorf("event = %+v, want PeerVoiceEvent with VoiceEnd=true", got[0])
	}
}

// Disconnect cleanup: ClearTalkerForToken should free the slot for any
// other token (covers the case where a talker's connection drops while
// they're still mid-transmission).
func TestClearTalkerForToken_FreesSlotForOtherTokens(t *testing.T) {
	s := newSimWithVoice(t)
	s.StartPTT("TCW-1", "tok-A")

	// Sanity: B is denied while A holds.
	if s.StartPTT("TCW-1", "tok-B") {
		t.Fatal("setup: B should be denied while A holds the slot")
	}

	s.ClearTalkerForToken("tok-A")

	if !s.StartPTT("TCW-1", "tok-B") {
		t.Fatal("after ClearTalkerForToken(tok-A), tok-B should be granted")
	}
}

func TestPrepareRadioTransmissionsForTCW_FiltersPeerVoice(t *testing.T) {
	s := newSimWithVoice(t)

	events := []Event{
		// Same TCW, different sender → keep
		{Type: PeerVoiceEvent, SourceTCW: "TCW-1", SenderToken: "tok-A", VoiceChunk: []int16{1, 2, 3}},
		// Same TCW, same sender → drop (don't echo to self)
		{Type: PeerVoiceEvent, SourceTCW: "TCW-1", SenderToken: "tok-SELF", VoiceChunk: []int16{4, 5, 6}},
		// Different TCW → drop
		{Type: PeerVoiceEvent, SourceTCW: "TCW-2", SenderToken: "tok-B", VoiceChunk: []int16{7, 8, 9}},
		// A non-voice event → keep, untouched
		{Type: StatusMessageEvent, WrittenText: "hello"},
	}

	out := s.PrepareRadioTransmissionsForTCWAndToken("TCW-1", "tok-SELF", events)

	if len(out) != 2 {
		t.Fatalf("got %d events, want 2 (PeerVoice from tok-A on TCW-1 + StatusMessage)", len(out))
	}
	if out[0].Type != PeerVoiceEvent || out[0].SenderToken != "tok-A" {
		t.Errorf("first kept event = %+v, want PeerVoice from tok-A", out[0])
	}
	if out[1].Type != StatusMessageEvent {
		t.Errorf("second kept event = %+v, want StatusMessage", out[1])
	}
}

func TestStartPTT_ExtendsRadioHoldUntil(t *testing.T) {
	s := newSimWithVoice(t)
	s.State.SimTime = NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))

	s.StartPTT("TCW-1", "tok-A")

	d := s.EnsureTCWDisplay("TCW-1")
	want := s.State.SimTime.Add(pttHoldExtension)
	if !d.RadioHoldUntil.Equal(want) {
		t.Errorf("RadioHoldUntil after StartPTT = %v, want %v", d.RadioHoldUntil, want)
	}
	if d.Rev == 0 {
		t.Errorf("StartPTT did not bump Rev on advance; got %d", d.Rev)
	}
}

func TestStartPTT_DoesNotShrinkRadioHoldUntil(t *testing.T) {
	s := newSimWithVoice(t)
	s.State.SimTime = NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))

	d := s.EnsureTCWDisplay("TCW-1")
	d.RadioHoldUntil = s.State.SimTime.Add(2 * time.Hour)
	revBefore := d.Rev

	s.StartPTT("TCW-1", "tok-A")

	if !d.RadioHoldUntil.Equal(s.State.SimTime.Add(2 * time.Hour)) {
		t.Errorf("StartPTT shrank RadioHoldUntil; got %v", d.RadioHoldUntil)
	}
	if d.Rev != revBefore {
		t.Errorf("StartPTT bumped Rev despite no-op extend; before=%d after=%d", revBefore, d.Rev)
	}
}

func TestStopPTT_SetsRadioHoldUntilToCooldown(t *testing.T) {
	s := newSimWithVoice(t)
	s.State.SimTime = NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))

	s.StartPTT("TCW-1", "tok-A")
	d := s.EnsureTCWDisplay("TCW-1")
	revAfterStart := d.Rev

	s.StopPTT("TCW-1", "tok-A")

	want := s.State.SimTime.Add(pttCooldown)
	if !d.RadioHoldUntil.Equal(want) {
		t.Errorf("RadioHoldUntil after StopPTT = %v, want %v", d.RadioHoldUntil, want)
	}
	if d.Rev <= revAfterStart {
		t.Errorf("StopPTT did not bump Rev; revAfterStart=%d revAfterStop=%d", revAfterStart, d.Rev)
	}
}

func TestClearTalkerForToken_SetsRadioHoldUntilToCooldown(t *testing.T) {
	s := newSimWithVoice(t)
	s.State.SimTime = NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))

	s.StartPTT("TCW-1", "tok-A")
	d := s.EnsureTCWDisplay("TCW-1")
	revAfterStart := d.Rev

	s.ClearTalkerForToken("tok-A")

	want := s.State.SimTime.Add(pttCooldown)
	if !d.RadioHoldUntil.Equal(want) {
		t.Errorf("RadioHoldUntil after ClearTalkerForToken = %v, want %v", d.RadioHoldUntil, want)
	}
	if d.Rev <= revAfterStart {
		t.Errorf("ClearTalkerForToken did not bump Rev; revAfterStart=%d revAfterClear=%d", revAfterStart, d.Rev)
	}
}
