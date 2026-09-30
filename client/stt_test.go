// client/stt_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform/audio"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// The whisper prompt carries each name of the reporting points ahead of the
// aircraft on the user's frequency, once however many of them are expecting
// the approach.
func TestWhisperPromptReportingPoints(t *testing.T) {
	oldDB := db.DB
	db.DB = &db.StaticDatabase{Callsigns: map[string]string{"AAL": "American"}}
	t.Cleanup(func() { db.DB = oldDB })

	bridge := av.ReportingPoint{Names: []string{"Dumbarton bridge", "Dumbarton"}}
	tower := av.ReportingPoint{Names: []string{"Sutro tower"}}
	track := func(callsign av.ADSBCallsign, freq sim.ControlPosition, rp av.ReportingPoint) *sim.Track {
		return &sim.Track{
			RadarTrack:          av.RadarTrack{ADSBCallsign: callsign},
			ControllerFrequency: freq,
			ReportingPoints:     []av.ReportingPoint{rp},
		}
	}

	var ss SimState
	ss.UserTCW = "TEST"
	ss.CurrentConsolidation = map[sim.TCW]*sim.TCPConsolidation{"TEST": {PrimaryTCP: "TEST"}}
	ss.Tracks = map[av.ADSBCallsign]*sim.Track{
		"AAL1": track("AAL1", "TEST", bridge),
		"AAL2": track("AAL2", "TEST", bridge),
		"AAL3": track("AAL3", "OTHER", tower),
	}

	parts := strings.Split(makeWhisperPrompt(ss), ", ")
	count := func(s string) int {
		return len(util.FilterSlice(parts, func(p string) bool { return p == s }))
	}
	for _, name := range bridge.Names {
		if n := count(name); n != 1 {
			t.Errorf("%q appears %d times in the prompt, want once", name, n)
		}
	}
	if count("Sutro tower") != 0 {
		t.Error("prompt includes the reporting point of an aircraft on another frequency")
	}
}

// fakeSpeaker is an audio engine that takes whatever speech it's given,
// remembering it and the callbacks to run when it finishes.
type fakeSpeaker struct {
	audio.Engine
	played   [][]int16
	finished []func()
}

func (f *fakeSpeaker) TryEnqueueSpeechPCM(pcm []int16, finished func()) error {
	f.played = append(f.played, pcm)
	f.finished = append(f.finished, finished)
	return nil
}

// finish has the speech playing now end.
func (f *fakeSpeaker) finish() {
	f.finished[len(f.finished)-1]()
}

// advance advances the pilot call with next published, speaking pilot
// calls, failing the test if the step isn't the one wanted.
func advance(t *testing.T, tm *TransmissionManager, next *sim.PilotTransmission, want pilotCallStep) *pilotCall {
	t.Helper()
	return advanceWith(t, tm, next, true, want)
}

// advanceWith is advance with pilot calls spoken or not.
func advanceWith(t *testing.T, tm *TransmissionManager, next *sim.PilotTransmission, speak bool,
	want pilotCallStep) *pilotCall {
	t.Helper()
	step, pc := tm.AdvancePilotCall(next, false, false, speak)
	if step != want {
		t.Fatalf("pilot call step %d, want %d", step, want)
	}
	return pc
}

func makeTestTransmissionManager() *TransmissionManager {
	return NewTransmissionManager(&log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// A pilot call is synthesized, taken from the server, and played, in that
// order and only then.
func TestPilotCallPlays(t *testing.T) {
	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	pc := advance(t, tm, next, pilotCallSynthesize)
	advance(t, tm, next, pilotCallWait) // still synthesizing
	tm.PilotCallSynthesized(pc, []int16{1})
	if advance(t, tm, next, pilotCallTake) != pc {
		t.Fatal("saying a different call than the one synthesized")
	}
	advance(t, tm, next, pilotCallWait) // waiting for the server
	if len(p.played) != 0 {
		t.Fatal("played the call before the server handed it over")
	}
	tm.PilotCallTaken(p, pc, true)
	if len(p.played) != 1 || !tm.IsPlaying() {
		t.Fatalf("expected the call to be playing, played %d", len(p.played))
	}

	// Nothing else starts while it plays or in the pause after it.
	advance(t, tm, next, pilotCallWait)
	p.finish()
	advance(t, tm, next, pilotCallWait)
	if tm.LastTransmissionCallsign() != "AAL123" {
		t.Errorf("last transmission from %q, want AAL123", tm.LastTransmissionCallsign())
	}
}

// A pilot call is synthesized shortly before the pause it is waiting out
// ends, so that it is ready to be taken when the pause does.
func TestPilotCallSynthesizedAheadOfPause(t *testing.T) {
	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	tm.holdUntil = time.Now().Add(time.Minute)
	advance(t, tm, next, pilotCallWait)

	tm.holdUntil = time.Now().Add(pilotCallLeadTime / 2)
	pc := advance(t, tm, next, pilotCallSynthesize)
	tm.PilotCallSynthesized(pc, []int16{1})
	advance(t, tm, next, pilotCallWait) // the pause isn't over

	tm.holdUntil = time.Time{}
	advance(t, tm, next, pilotCallTake)
	tm.PilotCallTaken(p, pc, true)
	if len(p.played) != 1 {
		t.Error("expected the call to play")
	}
}

// Using the radio at any point before the server is asked for a pilot call
// makes the call stale; the client starts over once the radio is quiet
// again.
func TestPilotCallDroppedWhenRadioUsed(t *testing.T) {
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	for _, tc := range []struct {
		name string
		use  func(*TransmissionManager)
	}{
		{"controller keys up", func(tm *TransmissionManager) { tm.Hold(); tm.Unhold() }},
		{"typed command", func(tm *TransmissionManager) { tm.HoldAfterTransmission() }},
		{"readback", func(tm *TransmissionManager) { tm.EnqueueReadbackPCM("UAL456", nil) }},
	} {
		for _, stage := range []pilotCallStage{pilotCallSynthesizing, pilotCallSynthesized} {
			tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
			pc := advance(t, tm, next, pilotCallSynthesize)
			if stage == pilotCallSynthesized {
				tm.PilotCallSynthesized(pc, []int16{1})
			}

			tc.use(tm)
			tm.holdUntil, tm.composingUntil = time.Time{}, time.Time{}
			again := advance(t, tm, next, pilotCallSynthesize)
			if again == pc {
				t.Errorf("%s at stage %d: kept the stale call", tc.name, stage)
			}
			tm.PilotCallSynthesized(pc, []int16{1}) // a late result for the stale call is ignored
			advance(t, tm, next, pilotCallWait)
			if len(p.played) != 0 {
				t.Errorf("%s at stage %d: played the stale call", tc.name, stage)
			}
		}
	}
}

// Once the server has been asked for a pilot call, its answer settles the
// call: it isn't dropped for the radio being used or for the server
// publishing another call in the meantime, and it plays if the server
// handed it over.
func TestPilotCallSettledOnceAsked(t *testing.T) {
	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}
	other := &sim.PilotTransmission{ContactID: 2, ADSBCallsign: "UAL456"}

	pc := advance(t, tm, next, pilotCallSynthesize)
	tm.PilotCallSynthesized(pc, []int16{1})
	advance(t, tm, next, pilotCallTake)

	tm.Hold()
	advance(t, tm, other, pilotCallWait)
	tm.Unhold()
	advance(t, tm, nil, pilotCallWait)
	tm.PilotCallTaken(p, pc, true)
	if len(p.played) != 1 {
		t.Errorf("played %d calls, want the one the server handed over", len(p.played))
	}
}

// A pilot call is dropped when the server publishes none, or doesn't hand
// it over.
func TestPilotCallDroppedByServer(t *testing.T) {
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	advance(t, tm, next, pilotCallSynthesize)
	advance(t, tm, nil, pilotCallWait)
	if tm.pilotCall != nil {
		t.Error("kept a call the server no longer publishes")
	}

	tm = makeTestTransmissionManager()
	pc := advance(t, tm, next, pilotCallSynthesize)
	tm.PilotCallSynthesized(pc, []int16{1})
	advance(t, tm, next, pilotCallTake)
	tm.PilotCallTaken(p, pc, false)
	if again := advance(t, tm, next, pilotCallSynthesize); again == pc {
		t.Error("kept a call the server didn't hand over")
	}
	if len(p.played) != 0 {
		t.Error("played a call the server didn't hand over")
	}
}

// Without speech, a pilot call is taken for display and followed by the
// same pause as one that was played.
func TestPilotCallWithoutSpeech(t *testing.T) {
	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	pc := advanceWith(t, tm, next, false, pilotCallTake)
	tm.PilotCallTaken(p, pc, true)
	if len(p.played) != 0 {
		t.Error("played a call that has no speech")
	}
	if tm.LastTransmissionCallsign() != "AAL123" {
		t.Errorf("last transmission from %q, want AAL123", tm.LastTransmissionCallsign())
	}
	advanceWith(t, tm, next, false, pilotCallWait)
}

// Readbacks play ahead of a pilot call, which waits for the radio to be quiet.
func TestReadbackPlaysBeforePilotCall(t *testing.T) {
	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	tm.EnqueueReadbackPCM("UAL456", []int16{2})
	advance(t, tm, next, pilotCallWait)
	tm.Update(p, false, false)
	if len(p.played) != 1 || p.played[0][0] != 2 {
		t.Fatal("expected the readback to play")
	}
	advance(t, tm, next, pilotCallWait)
	p.finish()
	tm.holdUntil = time.Time{}
	advance(t, tm, next, pilotCallSynthesize)
}

// While the controller is typing an instruction, no pilot call is prepared,
// since each keystroke would make it stale; one is once they stop.
func TestPilotCallWaitsForTyping(t *testing.T) {
	tm := makeTestTransmissionManager()
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	tm.HoldAfterTransmission()
	advance(t, tm, next, pilotCallWait)
	tm.composingUntil = time.Time{}
	advance(t, tm, next, pilotCallSynthesize)
}
