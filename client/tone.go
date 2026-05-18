// client/tone.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

// HeterodyneSampleRate is the sample rate used for generated tones; it
// matches the rate vice uses everywhere else for speech / mic capture.
const HeterodyneSampleRate = 16000

// GenerateHeterodyne returns a 1 kHz square-wave PCM tone of the given
// duration, suitable for the "stepped on" cue when a controller's PTT is
// denied because someone else on the same TCW is already transmitting.
//
// Output: int16 mono at 16 kHz. Amplitude is fixed at ~50% of full scale
// to be audible without being painful.
func GenerateHeterodyne(durationMs int) []int16 {
	if durationMs <= 0 {
		return nil
	}
	const (
		freqHz    = 1000
		amplitude = 16000 // ~50% of int16 range
	)
	n := HeterodyneSampleRate * durationMs / 1000
	samplesPerHalfCycle := HeterodyneSampleRate / (freqHz * 2)
	pcm := make([]int16, n)
	for i := 0; i < n; i++ {
		if (i/samplesPerHalfCycle)%2 == 0 {
			pcm[i] = amplitude
		} else {
			pcm[i] = -amplitude
		}
	}
	return pcm
}
