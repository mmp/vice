// eram/audio_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"encoding/binary"
	"testing"

	"github.com/mmp/vice/platform/audio"
)

// The alarm tone lasts half a second, peaks in proportion to its volume,
// and fades in and out rather than starting and stopping abruptly.
func TestAlarmTone(t *testing.T) {
	samples := func(pcm []byte) []int {
		s := make([]int, len(pcm)/2)
		for i := range s {
			s[i] = int(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
		}
		return s
	}
	peak := func(s []int) int {
		p := 0
		for _, v := range s {
			p = max(p, v, -v)
		}
		return p
	}

	loud, quiet := samples(makeAlarmTone(1)), samples(makeAlarmTone(0.2))
	if len(loud) != audio.SampleRate/2 {
		t.Errorf("tone is %d samples long, want %d", len(loud), audio.SampleRate/2)
	}
	if p := peak(loud); p < alarmPeakAmplitude*99/100 || p > alarmPeakAmplitude {
		t.Errorf("loudest tone peaks at %d, want %d", p, alarmPeakAmplitude)
	}
	if p, want := peak(quiet), alarmPeakAmplitude/5; p < want*99/100 || p > want {
		t.Errorf("quietest tone peaks at %d, want %d", p, want)
	}
	for _, i := range []int{0, len(loud) - 1} {
		if loud[i] != 0 {
			t.Errorf("sample %d is %d, want the tone faded to 0", i, loud[i])
		}
	}
}
