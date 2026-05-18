// client/stt_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
)

// fakePCMSink implements pcmSink for TM unit tests. It records the
// number of TryEnqueueSpeechPCM calls and synchronously fires the
// finished callback so the TM's playing flag clears immediately.
type fakePCMSink struct {
	enqueueCalls int
}

func (f *fakePCMSink) TryEnqueueSpeechPCM(pcm []int16, done func()) error {
	f.enqueueCalls++
	if done != nil {
		done()
	}
	return nil
}

func TestTransmissionManager_GatesOnRadioHoldUntil(t *testing.T) {
	tm := NewTransmissionManager(nil)
	tm.EnqueueReadbackPCM("AAL123", av.RadioTransmissionReadback, []int16{1, 2, 3}, sim.Time{})

	now := sim.NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	tcw := &sim.TCWDisplayState{RadioHoldUntil: now.Add(5 * time.Second)} // hold in future

	plat := &fakePCMSink{}
	tm.Update(plat, false, false, now, tcw)

	if plat.enqueueCalls != 0 {
		t.Errorf("playback should be gated by RadioHoldUntil; got %d enqueue calls", plat.enqueueCalls)
	}
}

func TestTransmissionManager_DefersUntilPlayAt(t *testing.T) {
	tm := NewTransmissionManager(nil)
	now := sim.NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	tm.EnqueueReadbackPCM("AAL123", av.RadioTransmissionReadback, []int16{1, 2, 3}, now.Add(5*time.Second))

	plat := &fakePCMSink{}
	tcw := &sim.TCWDisplayState{}
	tm.Update(plat, false, false, now, tcw)

	if plat.enqueueCalls != 0 {
		t.Errorf("playback should defer until PlayAt; got %d enqueue calls", plat.enqueueCalls)
	}

	tm.Update(plat, false, false, now.Add(10*time.Second), tcw)
	if plat.enqueueCalls != 1 {
		t.Errorf("playback should fire once SimTime > PlayAt; got %d", plat.enqueueCalls)
	}
}

func TestTransmissionManager_PlaysWhenRadioHoldExpired(t *testing.T) {
	tm := NewTransmissionManager(nil)
	tm.EnqueueReadbackPCM("AAL123", av.RadioTransmissionReadback, []int16{1, 2, 3}, sim.Time{})

	now := sim.NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	// RadioHoldUntil is in the past; playback should fire immediately.
	tcw := &sim.TCWDisplayState{RadioHoldUntil: now.Add(-1 * time.Second)}

	plat := &fakePCMSink{}
	tm.Update(plat, false, false, now, tcw)

	if plat.enqueueCalls != 1 {
		t.Errorf("playback should fire when RadioHoldUntil has expired; got %d enqueue calls", plat.enqueueCalls)
	}
}

func TestTransmissionManager_SkipsWhilePausedOrSTT(t *testing.T) {
	tm := NewTransmissionManager(nil)
	tm.EnqueueReadbackPCM("AAL123", av.RadioTransmissionReadback, []int16{1, 2, 3}, sim.Time{})

	now := sim.NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	plat := &fakePCMSink{}

	tm.Update(plat, true, false, now, nil) // paused
	if plat.enqueueCalls != 0 {
		t.Errorf("paused: expected 0 enqueue calls, got %d", plat.enqueueCalls)
	}
	tm.Update(plat, false, true, now, nil) // sttActive
	if plat.enqueueCalls != 0 {
		t.Errorf("sttActive: expected 0 enqueue calls, got %d", plat.enqueueCalls)
	}
}

func TestTransmissionManager_HoldCountBlocksPlayback(t *testing.T) {
	tm := NewTransmissionManager(nil)
	tm.Hold()
	tm.EnqueueTransmissionPCM("UAL45", av.RadioTransmissionContact, []int16{1, 2, 3}, sim.Time{})

	now := sim.NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	plat := &fakePCMSink{}
	tm.Update(plat, false, false, now, nil)
	if plat.enqueueCalls != 0 {
		t.Errorf("explicit hold should block playback; got %d", plat.enqueueCalls)
	}

	tm.Unhold()
	tm.Update(plat, false, false, now, nil)
	if plat.enqueueCalls != 1 {
		t.Errorf("after Unhold playback should proceed; got %d", plat.enqueueCalls)
	}
}

func TestTransmissionManager_ShouldRequestContactRespectsRadioHoldUntil(t *testing.T) {
	tm := NewTransmissionManager(nil)
	now := sim.NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))

	// Bus quiet: should request.
	if !tm.ShouldRequestContact(now, &sim.TCWDisplayState{}) {
		t.Errorf("idle TM with quiet bus should request contact")
	}
	// Bus busy: should not.
	tcwBusy := &sim.TCWDisplayState{RadioHoldUntil: now.Add(2 * time.Second)}
	if tm.ShouldRequestContact(now, tcwBusy) {
		t.Errorf("busy bus should defer contact request")
	}
}
