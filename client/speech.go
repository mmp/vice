// client/speech.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"slices"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/sim"
)

// queuedTransmission holds a transmission ready for playback with pre-decoded PCM audio.
type queuedTransmission struct {
	Callsign       av.ADSBCallsign
	Type           av.RadioTransmissionType
	PCM            []int16 // Pre-decoded PCM audio
	PTTReleaseTime time.Time
}

// TransmissionManager manages queuing and playback of radio transmissions.
// It centralizes the logic for playing MP3s in the correct order and handling
// playback state like holds after transmissions.
type TransmissionManager struct {
	mu           sync.Mutex
	queue        []queuedTransmission // pending transmissions
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

// EnqueueReadbackPCM adds a readback with pre-decoded PCM to the front of the queue (high priority).
// Readbacks come from WebSocket delivery with pre-decoded audio.
func (tm *TransmissionManager) EnqueueReadbackPCM(callsign av.ADSBCallsign, ty av.RadioTransmissionType, pcm []int16) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Readback arrived - release the hold that was set when command was sent
	if tm.holdCount > 0 {
		tm.holdCount--
	}

	if len(pcm) == 0 {
		tm.lg.Warnf("Skipping readback for %s due to empty PCM", callsign)
		return
	}

	// Drop any pending initial contact for this aircraft; the controller
	// has already talked to them so the check-in is stale.
	tm.queue = slices.DeleteFunc(tm.queue, func(qt queuedTransmission) bool {
		return qt.Callsign == callsign && qt.Type == av.RadioTransmissionContact
	})

	qt := queuedTransmission{
		Callsign: callsign,
		Type:     ty,
		PCM:      pcm,
	}
	// Insert at front - readbacks have priority
	tm.queue = append([]queuedTransmission{qt}, tm.queue...)
}

// EnqueueTransmissionPCM adds a pilot transmission with pre-decoded PCM to the queue.
// Used for contact and emergency transmissions where MP3 is decoded before enqueueing.
func (tm *TransmissionManager) EnqueueTransmissionPCM(callsign av.ADSBCallsign, ty av.RadioTransmissionType, pcm []int16) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(pcm) == 0 {
		tm.lg.Warnf("Skipping transmission for %s due to empty PCM", callsign)
		return
	}

	qt := queuedTransmission{
		Callsign: callsign,
		Type:     ty,
		PCM:      pcm,
	}
	tm.queue = append(tm.queue, qt)
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
	qt := tm.queue[0]
	tm.queue = tm.queue[1:]

	// Track whether this is a contact (vs readback)
	isContact := qt.Type == av.RadioTransmissionContact

	finishedCallback := func() {
		tm.mu.Lock()
		defer tm.mu.Unlock()

		tm.playing = false
		tm.lastCallsign = qt.Callsign
		tm.lastWasContact = isContact

		// Different hold times based on transmission type:
		// - After contact: 8 seconds (controller needs time to respond)
		// - After readback: 3 seconds (brief pause before next contact)
		if isContact {
			tm.holdUntil = time.Now().Add(8 * time.Second)
		} else {
			tm.holdUntil = time.Now().Add(3 * time.Second)
		}
	}

	// Enqueue pre-decoded PCM for playback
	err := p.TryEnqueueSpeechPCM(qt.PCM, finishedCallback)

	if err == nil {
		tm.playing = true

		// Post latency event if this is from an STT command (PTTReleaseTime is set)
		if !qt.PTTReleaseTime.IsZero() {
			latencyMs := int(time.Since(qt.PTTReleaseTime).Milliseconds())
			if tm.eventStream != nil {
				tm.eventStream.Post(sim.Event{
					Type:         sim.TTSPlaybackStartedEvent,
					ADSBCallsign: qt.Callsign,
					TTSLatencyMs: latencyMs,
				})
			}
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

// HoldAfterSilentContact sets a hold period after processing a contact without
// audio playback (when TTS is disabled). This maintains proper pacing of contacts.
func (tm *TransmissionManager) HoldAfterSilentContact(callsign av.ADSBCallsign) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.lastCallsign = callsign
	tm.lastWasContact = true
	// 8 seconds is the same hold time used after playing a contact transmission
	tm.holdUntil = time.Now().Add(8 * time.Second)
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
