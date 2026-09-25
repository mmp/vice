// stars/audio.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"slices"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

func (sp *Scope) initializeAudio(p platform.Platform, lg *log.Logger) {
	if sp.audioEffects == nil {
		sp.audioEffects = make(map[AudioType]int)

		loadMP3 := func(filename string) int {
			idx, err := p.AddMP3(util.LoadResourceBytes("audio/" + filename))
			if err != nil {
				lg.Errorf("%s: %v", filename, err)
			}
			return idx
		}

		sp.audioEffects[AudioConflictAlert] = loadMP3("CA_1000ms.mp3")
		sp.audioEffects[AudioSquawkSPC] = loadMP3("SPC_700ms.mp3")
		sp.audioEffects[AudioMinimumSafeAltitudeWarning] = loadMP3("MSAW_1000ms.mp3")
		sp.audioEffects[AudioModeCIntruder] = loadMP3("MCI_1000ms.mp3")
		sp.audioEffects[AudioTest] = loadMP3("TEST_250ms.mp3")
		sp.audioEffects[AudioInboundHandoff] = loadMP3("263124__pan14__sine-octaves-up-beep.mp3")
		sp.audioEffects[AudioCommandError] = loadMP3("ERROR.mp3")
		sp.audioEffects[AudioHandoffAccepted] = loadMP3("321104__nsstudios__blip2.mp3")
	}
}

func (sp *Scope) playOnce(p platform.Platform, a AudioType) {
	if sp.currentPrefs().AudioEffectEnabled[a] {
		p.PlayAudioOnce(sp.audioEffects[a])
	}
}

const AlertAudioDuration = 5 * time.Second

func (sp *Scope) updateAudio(ctx *scope.Context) {
	ps := sp.currentPrefs()

	if !sp.testAudioEndTime.IsZero() && time.Now().After(sp.testAudioEndTime) {
		ctx.Platform.StopPlayAudio(sp.audioEffects[AudioTest])
		sp.testAudioEndTime = time.Time{}
	}

	updateContinuous := func(play bool, effect AudioType) {
		if ps.AudioEffectEnabled[effect] && play {
			ctx.Platform.StartPlayAudioContinuous(sp.audioEffects[effect])
		} else {
			ctx.Platform.StopPlayAudio(sp.audioEffects[effect])
		}
	}

	// Play the CA sound if any CAs or MSAWs are unacknowledged
	playCASound := false
	if !ps.DisableCAWarnings {
		playCASound = slices.ContainsFunc(sp.CAAircraft,
			func(ca CAAircraft) bool {
				if ca.Acknowledged {
					return false
				}
				trk0, ok0 := ctx.GetTrackByCallsign(ca.ADSBCallsigns[0])
				trk1, ok1 := ctx.GetTrackByCallsign(ca.ADSBCallsigns[1])
				if !ok0 || !ok1 {
					return false
				}
				return trk0.IsAssociated() && !trk0.FlightPlan.DisableCA &&
					trk1.IsAssociated() && !trk1.FlightPlan.DisableCA &&
					ctx.InterpolatedSimTime.Before(ca.SoundEnd)
			})
		playCASound = playCASound || slices.ContainsFunc(sp.MCIAircraft,
			func(ca CAAircraft) bool {
				if ca.Acknowledged {
					return false
				}
				trk0, ok := ctx.GetTrackByCallsign(ca.ADSBCallsigns[0])
				return ok && trk0.IsAssociated() && !trk0.FlightPlan.DisableCA &&
					ctx.InterpolatedSimTime.Before(ca.SoundEnd)
			})
	}
	updateContinuous(playCASound, AudioConflictAlert)

	playMSAWSound := !ps.DisableMSAW && func() bool {
		for _, trk := range sp.visibleTracks {
			if trk.IsUnassociated() || trk.FlightPlan.DisableMSAW {
				continue
			}
			state := sp.TrackState[trk.ADSBCallsign]
			if state.MSAW && !state.MSAWAcknowledged && !state.InhibitMSAW &&
				ctx.InterpolatedSimTime.Before(state.MSAWSoundEnd) {
				return true
			}
		}
		return false
	}()
	updateContinuous(playMSAWSound, AudioMinimumSafeAltitudeWarning)

	// 2-100: play sound if:
	// - There is an unacknowledged SPC in a track's datablock
	// - [todo]: track is unassociated or is associated and was displaying FDB
	// - [todo]: if unassociated, is on-screen or within an adapted distance
	playSPCSound := func() bool {
		for _, trk := range sp.visibleTracks {
			state := sp.TrackState[trk.ADSBCallsign]
			ok, _ := state.track.Squawk.IsSPC()
			if ok && !state.SPCAcknowledged && ctx.InterpolatedSimTime.Before(state.SPCSoundEnd) {
				return true
			}
		}
		return false
	}()
	updateContinuous(playSPCSound, AudioSquawkSPC)
}
