// tts/radio.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Realistic VHF AM aviation radio effect for pilot TTS transmissions to ATC.
// This version degrades the speech itself (narrower band, more distortion) so it
// no longer sounds like clean voice + overlaid static. Bass is heavily cut.

package tts

import (
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
)

// biquad implements a second-order IIR (biquad) digital filter.
type biquad struct {
	b0, b1, b2, a1, a2 float32
	x1, x2, y1, y2     float32
}

func (f *biquad) process(x float32) float32 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2 = f.x1
	f.x1 = x
	f.y2 = f.y1
	f.y1 = y
	return y
}

// lowpassBiquad returns coefficients for a 2nd-order Butterworth lowpass
// filter using the Audio EQ Cookbook formulas.
func lowpassBiquad(sampleRate int, cutoffHz float32) biquad {
	w0 := 2 * math.Pi * cutoffHz / float32(sampleRate)
	sc := math.SinCos(w0)
	sinW0, cosW0 := sc[0], sc[1]
	alpha := sinW0 / math.Sqrt2 // Q = 1/sqrt(2) for Butterworth

	b1 := 1 - cosW0
	b0 := b1 / 2
	a0 := 1 + alpha

	return biquad{
		b0: b0 / a0,
		b1: b1 / a0,
		b2: b0 / a0,
		a1: (-2 * cosW0) / a0,
		a2: (1 - alpha) / a0,
	}
}

// highpassBiquad returns coefficients for a 2nd-order Butterworth highpass
// filter using the Audio EQ Cookbook formulas.
func highpassBiquad(sampleRate int, cutoffHz float32) biquad {
	w0 := 2 * math.Pi * cutoffHz / float32(sampleRate)
	sc := math.SinCos(w0)
	sinW0, cosW0 := sc[0], sc[1]
	alpha := sinW0 / math.Sqrt2 // Q = 1/sqrt(2) for Butterworth

	b1 := -(1 + cosW0)
	b0 := -b1 / 2
	a0 := 1 + alpha

	return biquad{
		b0: b0 / a0,
		b1: b1 / a0,
		b2: b0 / a0,
		a1: (-2 * cosW0) / a0,
		a2: (1 - alpha) / a0,
	}
}

// addRadioEffect - updated for more integrated, less clear "through the radio" sound
func addRadioEffect(pcm []int16, sampleRate int, seed uint32, scale float32) {
	if len(pcm) == 0 {
		return
	}

	params := rand.Make()
	params.Seed(uint64(seed))

	// Much higher highpass to kill bass and make it tinny/thin
	hpCutoff := float32(params.IntRange(340, 400))
	// Tighter lowpass for classic muffled airband quality
	lpCutoff := float32(params.IntRange(2300, 2550))

	staticGain := params.Float32Range(0.10, 0.20)
	engineGain := params.Float32Range(0.025, 0.075)
	clipDrive := params.Float32Range(1.45, 2.05) // stronger distortion on speech

	staticGain *= scale
	engineGain *= scale

	// === Speech path - filter FIRST to degrade the voice itself ===
	hp := highpassBiquad(sampleRate, hpCutoff)
	lp1 := lowpassBiquad(sampleRate, lpCutoff)
	lp2 := lowpassBiquad(sampleRate, lpCutoff) // cascaded = steeper roll-off

	buf := make([]float32, len(pcm))
	var peak float32

	for i, v := range pcm {
		x := float32(v) / 32767.0
		x = hp.process(x) // cut bass early
		x = lp1.process(x)
		x = lp2.process(x) // voice is now thin and muffled

		buf[i] = x
		if a := math.Abs(x); a > peak {
			peak = a
		}
	}

	// Normalize to a hot level (helps push into distortion)
	normGain := float32(0.78)
	if peak > 0.001 {
		normGain = 0.78 / peak
	}

	// Noise setup
	noiseRng := rand.Make()
	noiseHp := highpassBiquad(sampleRate, hpCutoff)
	noiseLp1 := lowpassBiquad(sampleRate, lpCutoff)
	noiseLp2 := lowpassBiquad(sampleRate, lpCutoff)
	engineLp := lowpassBiquad(sampleRate, 230)

	// Squelch
	attack := sampleRate * 4 / 1000
	speechRelease := sampleRate * 10 / 1000
	noiseRelease := sampleRate * 35 / 1000

	invTanh := 1 / tanhApprox(clipDrive)

	for i, speech := range buf {
		speech *= normGain

		// Heavy distortion on the already-filtered speech
		speech = tanhApprox(speech*clipDrive) * invTanh

		// Extra hard clipping for that overdriven mic/receiver feel
		if speech > 0.78 {
			speech = 0.78
		} else if speech < -0.78 {
			speech = -0.78
		}

		// Noise (filtered to same band)
		static := noiseHp.process(noiseLp2.process(noiseLp1.process(noiseRng.Float32Range(-1, 1))))
		engine := engineLp.process(noiseRng.Float32Range(-1, 1))
		noise := static*staticGain + engine*engineGain

		// Squelch envelope
		speechEnv := float32(1)
		noiseEnv := float32(1)

		if i < attack {
			t := float32(i) / float32(attack)
			speechEnv = t * t
			noiseEnv = t
		}
		if i >= len(pcm)-noiseRelease {
			noiseEnv = float32(len(pcm)-i) / float32(noiseRelease)
		}
		if i >= len(pcm)-speechRelease {
			speechEnv = float32(len(pcm)-i) / float32(speechRelease)
		}

		x := speech*speechEnv + noise*noiseEnv
		pcm[i] = int16(math.Clamp(int(x*32767), -32768, 32767))
	}
}

// tanhApprox computes tanh(x) using FastExp.
func tanhApprox(x float32) float32 {
	if x > 5 {
		return 1
	}
	if x < -5 {
		return -1
	}
	e2x := math.FastExp(2 * x)
	return (e2x - 1) / (e2x + 1)
}
