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

// advance advances the pilot call with next published, failing the test if
// the step isn't the one wanted.
func advance(t *testing.T, tm *TransmissionManager, p audio.Engine, next *sim.PilotTransmission,
	want pilotCallStep) *pilotCall {
	t.Helper()
	step, pc := tm.AdvancePilotCall(p, next, false, false)
	if step != want {
		t.Fatalf("pilot call step %d, want %d", step, want)
	}
	return pc
}

func makeTestTransmissionManager() *TransmissionManager {
	return NewTransmissionManager(&log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// A pilot call is synthesized, checked with the server, played, and reported,
// in that order and only then.
func TestPilotCallPlays(t *testing.T) {
	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	pc := advance(t, tm, p, next, pilotCallSynthesize)
	advance(t, tm, p, next, pilotCallWait) // still synthesizing
	tm.PilotCallSynthesized(pc, []int16{1})
	if advance(t, tm, p, next, pilotCallCheck) != pc {
		t.Fatal("checking a different call than the one synthesized")
	}
	advance(t, tm, p, next, pilotCallWait) // waiting for the server
	tm.PilotCallChecked(pc, true)
	advance(t, tm, p, next, pilotCallReport)
	if len(p.played) != 1 || !tm.IsPlaying() {
		t.Fatalf("expected the call to be playing, played %d", len(p.played))
	}

	// Nothing else starts while it plays or in the pause after it.
	advance(t, tm, p, next, pilotCallWait)
	p.finish()
	advance(t, tm, p, next, pilotCallWait)
	if tm.LastTransmissionCallsign() != "AAL123" {
		t.Errorf("last transmission from %q, want AAL123", tm.LastTransmissionCallsign())
	}
}

// Using the radio at any point before a pilot call plays makes its speech
// stale; the client starts over once the radio is quiet again.
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
		for _, stage := range []pilotCallStage{pilotCallSynthesizing, pilotCallChecking, pilotCallApproved} {
			tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
			pc := advance(t, tm, p, next, pilotCallSynthesize)
			if stage >= pilotCallChecking {
				tm.PilotCallSynthesized(pc, []int16{1})
				advance(t, tm, p, next, pilotCallCheck)
			}
			if stage == pilotCallApproved {
				tm.PilotCallChecked(pc, true)
			}

			tc.use(tm)
			tm.holdUntil = time.Time{}
			tm.PilotCallSynthesized(pc, []int16{1}) // a late result for the stale call is ignored
			tm.PilotCallChecked(pc, true)
			if again := advance(t, tm, p, next, pilotCallSynthesize); again == pc {
				t.Errorf("%s at stage %d: kept the stale call", tc.name, stage)
			}
			if len(p.played) != 0 {
				t.Errorf("%s at stage %d: played the stale call", tc.name, stage)
			}
		}
	}
}

// A pilot call is dropped when the server publishes a different one or none,
// or answers that it is no longer next.
func TestPilotCallDroppedByServer(t *testing.T) {
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}
	other := &sim.PilotTransmission{ContactID: 2, ADSBCallsign: "UAL456"}

	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	advance(t, tm, p, next, pilotCallSynthesize)
	if pc := advance(t, tm, p, other, pilotCallSynthesize); pc.transmission.ContactID != 2 {
		t.Errorf("synthesizing contact %d, want 2", pc.transmission.ContactID)
	}

	tm = makeTestTransmissionManager()
	advance(t, tm, p, next, pilotCallSynthesize)
	advance(t, tm, p, nil, pilotCallWait)

	tm = makeTestTransmissionManager()
	pc := advance(t, tm, p, next, pilotCallSynthesize)
	tm.PilotCallSynthesized(pc, []int16{1})
	advance(t, tm, p, next, pilotCallCheck)
	tm.PilotCallChecked(pc, false)
	if again := advance(t, tm, p, next, pilotCallSynthesize); again == pc {
		t.Error("kept a call the server said isn't next")
	}
	if len(p.played) != 0 {
		t.Error("played a call the server said isn't next")
	}
}

// Without speech, a pilot call is reported for display and followed by the
// same pause as one that was played.
func TestPilotCallWithoutSpeech(t *testing.T) {
	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	pc := advance(t, tm, p, next, pilotCallSynthesize)
	tm.PilotCallSynthesized(pc, nil)
	advance(t, tm, p, next, pilotCallCheck)
	tm.PilotCallChecked(pc, true)
	advance(t, tm, p, next, pilotCallReport)
	if len(p.played) != 0 {
		t.Error("played a call that has no speech")
	}
	advance(t, tm, p, next, pilotCallWait)
}

// Readbacks play ahead of a pilot call, which waits for the radio to be quiet.
func TestReadbackPlaysBeforePilotCall(t *testing.T) {
	tm, p := makeTestTransmissionManager(), &fakeSpeaker{}
	next := &sim.PilotTransmission{ContactID: 1, ADSBCallsign: "AAL123"}

	tm.EnqueueReadbackPCM("UAL456", []int16{2})
	advance(t, tm, p, next, pilotCallWait)
	tm.Update(p, false, false)
	if len(p.played) != 1 || p.played[0][0] != 2 {
		t.Fatal("expected the readback to play")
	}
	advance(t, tm, p, next, pilotCallWait)
	p.finish()
	tm.holdUntil = time.Time{}
	advance(t, tm, p, next, pilotCallSynthesize)
}
