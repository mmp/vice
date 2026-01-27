// client/speech.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/sim"
)

// TransmissionManager manages queuing and playback of radio transmissions.
// It centralizes the logic for playing MP3s in the correct order and handling
// playback state like holds after transmissions.
type TransmissionManager struct {
	mu           sync.Mutex
	queue        []sim.PilotSpeech // pending transmissions
	playing      bool
	holdCount    int       // explicit holds (e.g., during STT recording/processing)
	holdUntil    time.Time // time-based hold for post-transmission pauses
	lastCallsign av.ADSBCallsign
	eventStream  *sim.EventStream
	lg           *log.Logger

	// Contact request management
	lastWasContact   bool
	contactRequested bool
}

// NewTransmissionManager creates a new TransmissionManager.
func NewTransmissionManager(lg *log.Logger) *TransmissionManager {
	return &TransmissionManager{
		lg: lg,
	}
}

// SetEventStream sets the event stream for posting TTS latency events.
// This must be called before Update() will post latency events.
func (tm *TransmissionManager) SetEventStream(es *sim.EventStream) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.eventStream = es
}

// EnqueueReadback adds a readback to the front of the queue (high priority).
// Readbacks come directly from RunAircraftCommands RPC results and should
// be played immediately or as soon as possible.
func (tm *TransmissionManager) EnqueueReadback(ps sim.PilotSpeech) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(ps.MP3) == 0 {
		tm.lg.Warnf("Skipping readback for %s due to empty MP3", ps.Callsign)
		return
	}

	// Insert at front - readbacks have priority
	tm.queue = append([]sim.PilotSpeech{ps}, tm.queue...)
}

// EnqueueTransmission adds a pilot transmission to the queue.
func (tm *TransmissionManager) EnqueueTransmission(ps sim.PilotSpeech) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(ps.MP3) == 0 {
		tm.lg.Warnf("Skipping speech for %s due to empty MP3", ps.Callsign)
		return
	}
	tm.queue = append(tm.queue, ps)
}

// Update manages playback state, called each frame.
// It handles hold timeouts and initiates playback when appropriate.
func (tm *TransmissionManager) Update(p platform.Platform, paused, sttActive bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Don't play speech while paused or during STT recording
	if paused || sttActive {
		return
	}

	// Check if there's an explicit hold (e.g., STT processing)
	if tm.holdCount > 0 {
		return
	}

	// Check if we're in a time-based hold period (post-transmission pause)
	if time.Now().Before(tm.holdUntil) {
		return
	}

	// Can't play if already playing or nothing to play
	if tm.playing || len(tm.queue) == 0 {
		return
	}

	// Get next speech to play
	ps := tm.queue[0]
	tm.queue = tm.queue[1:]

	// Track whether this is a contact (vs readback)
	isContact := ps.Type == av.RadioTransmissionContact

	// Try to enqueue for playback
	if err := p.TryEnqueueSpeechMP3(ps.MP3, func() {
		tm.mu.Lock()
		defer tm.mu.Unlock()

		tm.playing = false
		tm.lastCallsign = ps.Callsign
		tm.lastWasContact = isContact

		// Different hold times based on transmission type:
		// - After contact: 8 seconds (controller needs time to respond)
		// - After readback: 3 seconds (brief pause before next contact)
		if isContact {
			tm.holdUntil = time.Now().Add(8 * time.Second)
		} else {
			tm.holdUntil = time.Now().Add(3 * time.Second)
		}
	}); err == nil {
		tm.playing = true

		// Post latency event if this is from an STT command (PTTReleaseTime is set)
		if !ps.PTTReleaseTime.IsZero() && tm.eventStream != nil {
			latencyMs := int(time.Since(ps.PTTReleaseTime).Milliseconds())
			tm.eventStream.Post(sim.Event{
				Type:         sim.TTSPlaybackStartedEvent,
				ADSBCallsign: ps.Callsign,
				TTSLatencyMs: latencyMs,
			})
		}
	}
}

// HoldAfterTransmission sets a hold period, used when the user initiates
// their own transmission and we should pause pilot transmissions briefly.
func (tm *TransmissionManager) HoldAfterTransmission() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.holdUntil = time.Now().Add(2 * time.Second)
}

// Hold increments the hold counter, preventing playback until Unhold is called.
// Used during STT recording/processing to prevent speech playback.
func (tm *TransmissionManager) Hold() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.holdCount++
}

// Unhold decrements the hold counter. Playback resumes when count reaches zero.
func (tm *TransmissionManager) Unhold() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.holdCount > 0 {
		tm.holdCount--
	}
}

// LastTransmissionCallsign returns the callsign of the last played transmission.
func (tm *TransmissionManager) LastTransmissionCallsign() av.ADSBCallsign {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	return tm.lastCallsign
}

// IsPlaying returns true if a transmission is currently playing.
func (tm *TransmissionManager) IsPlaying() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	return tm.playing
}

// ShouldRequestContact returns true if the client should request a contact from the server.
// It checks that we're not playing, not held, queue is empty, and no request is pending.
// It also returns true slightly early (2s before hold expires) to hide TTS latency.
func (tm *TransmissionManager) ShouldRequestContact() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.contactRequested || tm.playing || tm.holdCount > 0 || len(tm.queue) > 0 {
		return false
	}

	// Request early to hide latency: when hold expires in less than 2s
	// (or has already expired)
	return time.Until(tm.holdUntil) < 2*time.Second
}

// SetContactRequested marks that we've sent a contact request and are waiting.
func (tm *TransmissionManager) SetContactRequested(requested bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.contactRequested = requested
}

// IsContactRequested returns true if we're waiting for a contact response.
func (tm *TransmissionManager) IsContactRequested() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	return tm.contactRequested
}
