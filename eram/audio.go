// eram/audio.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"encoding/binary"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform/audio"
)

// The audible alarm is a 700 Hz tone of half a second that sounds for an
// input error or a value at its limit (1.4). The CURSOR menu's VOLUME
// button sets its volume from 1 to 5 and mutes it at 0 (3.5.3).
const (
	alarmToneHz       = 700
	alarmToneDuration = 500 * time.Millisecond
	maxAlarmVolume    = 5
	// alarmPeakAmplitude is the tone's peak sample at the loudest volume:
	// half of full scale, leaving room for speech playing at the same time.
	alarmPeakAmplitude = 16384
)

// alarm holds the alarm tone as registered with the audio engine at each
// volume. The engine's own volume also applies to the pilots' speech, so
// the tone's volume is set by its amplitude instead.
type alarm struct {
	engine  audio.Engine
	tones   [maxAlarmVolume]int
	sounded time.Time
}

func (ep *Scope) loadAlarm(e audio.Engine, lg *log.Logger) {
	if ep.alarm.engine != nil {
		return
	}
	for v := range maxAlarmVolume {
		id, err := e.AddPCM(makeAlarmTone(float32(v+1)/maxAlarmVolume), audio.SampleRate)
		if err != nil {
			lg.Errorf("alarm tone: %v", err)
			return
		}
		ep.alarm.tones[v] = id
	}
	ep.alarm.engine = e
}

// makeAlarmTone returns the alarm tone at the given fraction of its peak
// amplitude as 16-bit little-endian PCM. It fades in and out over a few
// milliseconds so that it starts and stops without a click.
func makeAlarmTone(gain float32) []byte {
	n := int(alarmToneDuration.Seconds() * audio.SampleRate)
	const fade = audio.SampleRate / 200
	pcm := make([]byte, 2*n)
	for i := range n {
		envelope := min(1, float32(i)/fade, float32(n-1-i)/fade)
		phase := 2 * math.Pi * alarmToneHz * float32(i) / audio.SampleRate
		s := int16(gain * envelope * alarmPeakAmplitude * math.Sin(phase))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(s))
	}
	return pcm
}

// soundAlarm sounds the alarm at the current volume. It doesn't sound again
// until the tone has finished, so that a button held at its limit doesn't
// queue up one tone after another.
func (ep *Scope) soundAlarm() {
	if ep.alarm.engine == nil || time.Since(ep.alarm.sounded) < alarmToneDuration {
		return
	}
	if v := ep.currentPrefs().AlarmVolume; v > 0 {
		ep.alarm.engine.PlayAudioOnce(ep.alarm.tones[v-1])
		ep.alarm.sounded = time.Now()
	}
}

// clickAlarmVolume adjusts the alarm volume and sounds the alarm at the new
// volume so that it can be heard (3.5.3), cutting off a tone still playing
// at the old one.
func clickAlarmVolume(ep *Scope, c toolbarClick) error {
	if err := adjust(&ep.currentPrefs().AlarmVolume, c, 0, maxAlarmVolume, 1); err != nil {
		return err
	}
	if ep.alarm.engine != nil {
		for _, id := range ep.alarm.tones {
			ep.alarm.engine.StopPlayAudio(id)
		}
	}
	ep.alarm.sounded = time.Time{}
	ep.soundAlarm()
	return nil
}
