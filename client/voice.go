// client/voice.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"sync"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
)

// PTTRelay coordinates a single controller's PTT-press lifecycle with the
// server: it asks the server for the talker slot on press, forwards mic
// chunks while granted, and releases the slot on release. When denied, the
// caller is responsible for playing the heterodyne tone — PTTRelay just
// reports state.
//
// All public methods are safe to call from the UI goroutine.
type PTTRelay struct {
	mu      sync.Mutex
	client  *RPCClient
	token   string
	pressed bool // PTT key is currently held
	granted bool // server granted us the talker slot for this press
	denied  bool // server denied this press; caller has played the tone
	lg      *log.Logger
}

// NewPTTRelay wires up a relay for a single client connection.
func NewPTTRelay(client *RPCClient, controllerToken string, lg *log.Logger) *PTTRelay {
	return &PTTRelay{
		client: client,
		token:  controllerToken,
		lg:     lg,
	}
}

// Press is called when the PTT key transitions from up to down.
// Returns:
//   - granted=true if the server gave us the talker slot. Caller should
//     start mic capture and feed chunks via SendChunk.
//   - granted=false if the server denied us. Caller should play the
//     heterodyne tone and skip mic capture for this press. SendChunk and
//     Release will be no-ops until the next Press.
func (r *PTTRelay) Press() (granted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.pressed {
		// Defensive: avoid double-press.
		return r.granted
	}
	r.pressed = true

	var reply server.StartPTTReply
	if err := r.client.callWithTimeout(server.StartPTTRPC, r.token, &reply); err != nil {
		r.lg.Errorf("StartPTT RPC failed: %v", err)
		r.denied = true
		return false
	}
	if reply.Granted {
		r.granted = true
		return true
	}
	r.denied = true
	return false
}

// SendChunk forwards a 20 ms PCM chunk to the server. No-op when the
// current press is denied or the relay is not pressed.
func (r *PTTRelay) SendChunk(samples []int16) {
	r.mu.Lock()
	if !r.pressed || !r.granted {
		r.mu.Unlock()
		return
	}
	token := r.token
	r.mu.Unlock()

	args := &server.StreamPTTAudioArgs{
		ControllerToken: token,
		Samples:         samples,
	}
	if err := r.client.callWithTimeout(server.StreamPTTAudioRPC, args, nil); err != nil {
		r.lg.Debugf("StreamPTTAudio RPC failed (chunk dropped): %v", err)
	}
}

// Release is called when the PTT key transitions from down to up.
// Sends StopPTT only when the current press was granted.
func (r *PTTRelay) Release() {
	r.mu.Lock()
	wasGranted := r.granted
	r.pressed = false
	r.granted = false
	r.denied = false
	r.mu.Unlock()

	if !wasGranted {
		return
	}
	if err := r.client.callWithTimeout(server.StopPTTRPC, r.token, nil); err != nil {
		r.lg.Errorf("StopPTT RPC failed: %v", err)
	}
}

// IsDenied reports whether the current press was denied by the server.
// Returns false once Release is called.
func (r *PTTRelay) IsDenied() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.denied
}

// PlaybackSink is the subset of platform.Platform that PeerVoicePlayback
// uses. Decoupled to keep client → platform light and the consumer
// trivially mockable.
type PlaybackSink interface {
	AppendSpeechPCM(pcm []int16)
}

// PeerVoicePlayback drains PeerVoiceEvents from the client-local event
// stream and pipes their PCM samples straight to the platform's speech
// queue. There is no internal queue — AppendSpeechPCM does the buffering.
type PeerVoicePlayback struct {
	mu     sync.Mutex
	events *sim.EventsSubscription
	lg     *log.Logger
}

func NewPeerVoicePlayback(lg *log.Logger) *PeerVoicePlayback {
	return &PeerVoicePlayback{lg: lg}
}

// SetEventStream binds the playback to the client-local event stream.
// Called from ControlClient.GetUpdates when the stream is first known,
// matching how TransmissionManager.SetEventStream works.
func (p *PeerVoicePlayback) SetEventStream(es *sim.EventStream) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.events != nil {
		p.events.Unsubscribe()
	}
	p.events = es.Subscribe()
}

// Update drains pending events and feeds PCM to the audio sink. Call once
// per frame.
func (p *PeerVoicePlayback) Update(plat PlaybackSink) {
	p.mu.Lock()
	sub := p.events
	p.mu.Unlock()
	if sub == nil {
		return
	}
	var chunkCount, sampleCount int
	for _, e := range sub.Get() {
		if e.Type != sim.PeerVoiceEvent {
			continue
		}
		if e.VoiceEnd || len(e.VoiceChunk) == 0 {
			continue
		}
		plat.AppendSpeechPCM(e.VoiceChunk)
		chunkCount++
		sampleCount += len(e.VoiceChunk)
	}
	if chunkCount > 0 && p.lg != nil {
		p.lg.Warnf("DBG_VOICE: PeerVoicePlayback drained chunks=%d samples=%d", chunkCount, sampleCount)
	}
}

// PilotVoicePlayback synthesizes pilot TTS for RadioTransmissionEvents
// observed on the local event stream that did NOT originate from this
// controller (the observer-side counterpart to the RPC-result-driven
// requester synthesis in ControlClient.synthesizeAndEnqueue*). One per
// ControlClient; subscribed lazily to the same EventStream as
// TransmissionManager and PeerVoicePlayback.
//
// The actual TTS call is held behind a function pointer (`synthesize`)
// so tests can substitute a stub.
type PilotVoicePlayback struct {
	mu         sync.Mutex
	events     *sim.EventsSubscription
	myToken    string
	lg         *log.Logger
	synthesize func(callsign av.ADSBCallsign, ty av.RadioTransmissionType, text, voice string, playAt sim.Time)
}

// NewPilotVoicePlayback creates an observer-side synthesizer.
// myToken is the local controller's RPC token, used to skip events
// whose RPC-result path already produced audio on this client.
// (Pass an empty string before sign-on; SetMyToken updates it later.)
func NewPilotVoicePlayback(lg *log.Logger, myToken string) *PilotVoicePlayback {
	return &PilotVoicePlayback{
		lg:      lg,
		myToken: myToken,
	}
}

// SetEventStream binds the playback to the local event stream.
func (p *PilotVoicePlayback) SetEventStream(es *sim.EventStream) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.events != nil {
		p.events.Unsubscribe()
	}
	p.events = es.Subscribe()
}

// SetMyToken updates the local controller's RPC token. Called after
// sign-on once the token is known.
func (p *PilotVoicePlayback) SetMyToken(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.myToken = token
}

// Update drains pending RadioTransmissionEvents and asks the
// synthesize callback to render audio for each one whose
// RequesterToken does not match the local controller's token. Call
// once per frame.
func (p *PilotVoicePlayback) Update() {
	p.mu.Lock()
	sub := p.events
	myToken := p.myToken
	syn := p.synthesize
	p.mu.Unlock()

	if sub == nil || syn == nil {
		return
	}
	for _, e := range sub.Get() {
		if e.Type != sim.RadioTransmissionEvent {
			continue
		}
		// Skip events the local RPC-result path will synthesize.
		// Empty RequesterToken means a server-internal event with no
		// originating client — every observer should synthesize it.
		if e.RequesterToken != "" && e.RequesterToken == myToken {
			continue
		}
		syn(e.ADSBCallsign, e.RadioTransmissionType, e.SpokenText, e.SpokenVoice, e.PlayAt)
	}
}
