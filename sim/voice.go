// sim/voice.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import "time"

// activeTalker holds the controller token that is currently transmitting on
// each TCW. A TCW absent from the map has no active talker; only one
// controller may transmit per TCW at a time. The map is guarded by s.mu.

const (
	// pttHoldExtension is the upper bound the talker slot extends
	// RadioHoldUntil while a controller is mid-PTT. Generous (60s) so
	// pilot transmissions stay parked through any plausible PTT
	// duration; replaced with pttCooldown the moment the talker
	// releases or disconnects.
	pttHoldExtension = 60 * time.Second

	// pttCooldown is the post-PTT silence window applied when a
	// controller releases or disconnects mid-PTT.
	pttCooldown = 2 * time.Second
)

// StartPTT attempts to acquire the talker slot for tcw on behalf of the
// given controller token. Returns true when the caller is now the active
// talker (either because the slot was idle or because the caller already
// held it), false when another token holds the slot.
func (s *Sim) StartPTT(tcw TCW, token string) bool {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if s.activeTalker == nil {
		s.activeTalker = make(map[TCW]string)
	}
	if existing, ok := s.activeTalker[tcw]; ok && existing != token {
		s.lg.Warnf("DBG_VOICE: StartPTT denied tcw=%q token=%s existing=%s", tcw, token[:min(8, len(token))], existing[:min(8, len(existing))])
		return false
	}
	s.activeTalker[tcw] = token

	d := s.EnsureTCWDisplay(tcw)
	target := s.State.SimTime.Add(pttHoldExtension)
	if target.After(d.RadioHoldUntil) {
		d.RadioHoldUntil = target
		d.Rev++
	}

	s.lg.Warnf("DBG_VOICE: StartPTT granted tcw=%q token=%s", tcw, token[:min(8, len(token))])
	return true
}

// RecordPTTChunk fans out a single audio chunk to listeners on the same
// TCW. Chunks from any token other than the active talker are silently
// dropped (covers post-StopPTT stragglers and any spoofing).
func (s *Sim) RecordPTTChunk(tcw TCW, token string, samples []int16) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if s.activeTalker[tcw] != token {
		active := s.activeTalker[tcw]
		s.lg.Warnf("DBG_VOICE: RecordPTTChunk dropped (not active talker) tcw=%q token=%s active=%q", tcw, token[:min(8, len(token))], active)
		return
	}
	s.dbgVoiceChunkCount++
	if s.dbgVoiceChunkCount%25 == 1 {
		s.lg.Warnf("DBG_VOICE: RecordPTTChunk fanout tcw=%q token=%s samples=%d count=%d", tcw, token[:min(8, len(token))], len(samples), s.dbgVoiceChunkCount)
	}
	s.eventStream.Post(Event{
		Type:        PeerVoiceEvent,
		SourceTCW:   tcw,
		SenderToken: token,
		VoiceChunk:  samples,
	})
}

// StopPTT releases the talker slot for tcw if the caller currently holds
// it, and posts a final PeerVoiceEvent with VoiceEnd=true so listeners can
// finalize their playback state. No-op when the caller does not hold the
// slot.
func (s *Sim) StopPTT(tcw TCW, token string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if s.activeTalker[tcw] != token {
		return
	}
	delete(s.activeTalker, tcw)

	d := s.EnsureTCWDisplay(tcw)
	d.RadioHoldUntil = s.State.SimTime.Add(pttCooldown)
	d.Rev++

	s.eventStream.Post(Event{
		Type:        PeerVoiceEvent,
		SourceTCW:   tcw,
		SenderToken: token,
		VoiceEnd:    true,
	})
}

// ClearTalkerForToken frees any TCW slots held by token. Called from the
// server's sign-off / connection-cleanup path to avoid stranding a slot
// when a talker disconnects mid-PTT.
func (s *Sim) ClearTalkerForToken(token string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for tcw, holder := range s.activeTalker {
		if holder == token {
			delete(s.activeTalker, tcw)

			d := s.EnsureTCWDisplay(tcw)
			d.RadioHoldUntil = s.State.SimTime.Add(pttCooldown)
			d.Rev++

			s.eventStream.Post(Event{
				Type:        PeerVoiceEvent,
				SourceTCW:   tcw,
				SenderToken: token,
				VoiceEnd:    true,
			})
		}
	}
}
