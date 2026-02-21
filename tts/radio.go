// tts/radio.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Realistic AM aviation radio effect for pilot TTS transmissions.
//
// # Analysis Methodology
//
// The effect parameters were derived from spectral analysis of a 67-minute
// Seattle TRACON approach recording. 58 transmissions in the first 10
// minutes were automatically segmented by RMS envelope thresholding and
// classified as pilot-like or controller-like based on spectral features
// (centroid, bandwidth, high-frequency energy ratio). Key measurements:
//
// # Bandwidth Limiting
//
// Active transmissions showed a -3 dB bandwidth of 328-657 Hz, a -20 dB
// bandwidth of 135-2993 Hz, and peak energy at ~506 Hz. The HF rolloff
// above 3 kHz measured -24.5 dB/octave, consistent with a 4th-order
// (two cascaded 2nd-order) Butterworth lowpass. The LF rolloff below
// 300 Hz measured +11.2 dB/octave, consistent with a 2nd-order highpass.
// These match the expected AM aviation radio passband of ~300-3000 Hz.
//
// The implementation uses one 2nd-order Butterworth highpass (220-320 Hz)
// and two cascaded 2nd-order Butterworth lowpass filters (2800-3200 Hz)
// to reproduce the measured rolloff characteristics. Filter coefficients
// are computed using the Audio EQ Cookbook (Robert Bristow-Johnson)
// biquad formulas with Q = 1/sqrt(2) for maximally-flat passband.
//
// # Pilot vs Controller Spectral Differences
//
// Pilot transmissions showed a spectral centroid of 903 Hz vs 713 Hz for
// controllers, 30% wider spectral bandwidth, and 2.4x more energy above
// 2.5 kHz (5.28% vs 2.20%). The difference was most pronounced at
// 2.5-3.5 kHz (+6.6 dB). This extra HF content comes from cockpit
// engine noise picked up by the pilot's microphone and from the noisier
// air-to-ground radio path. Below 700 Hz, controller transmissions were
// actually slightly stronger (landline audio path, no engine noise).
//
// The implementation models this with two additive noise layers:
//   - Broadband static: white noise bandpass-filtered to the AM radio
//     bandwidth, simulating RF noise in the air-to-ground link.
//   - Engine rumble: white noise lowpass-filtered at 250-450 Hz,
//     simulating turbine/propeller noise from the cockpit environment.
//
// # Compression / AGC
//
// Many real transmissions clip at 0 dBFS, indicating aggressive automatic
// gain control (AGC) in the radio receiver. The implementation applies
// soft compression via tanh after normalizing the filtered speech to a
// target peak level (0.65-0.75). The tanh curve boosts quiet signals
// (~1.5-1.7x for small amplitudes) while limiting peaks, matching the
// narrowed dynamic range observed in the reference material.
//
// # Squelch Envelope
//
// Transmission onset in the reference material showed <5-10 ms rise time
// from noise floor to full level (squelch gate opening). Offset showed
// speech cutting quickly while background noise lingered briefly before
// the squelch gate closed. The implementation uses a 5 ms attack, 15 ms
// speech release, and 50 ms noise release to reproduce this effect.
// The caller also appends 0.1-0.3 seconds of silence that gets filled
// with noise-only signal, simulating the pilot holding the PTT key
// briefly after finishing speaking.
//
// # Per-Aircraft Variation
//
// Each aircraft's callsign is hashed to a seed that deterministically
// selects filter cutoff frequencies, noise levels, compression drive,
// and target peak level. This means the same aircraft always has
// consistent radio characteristics across transmissions, while different
// aircraft sound distinct. The actual noise waveform varies each
// transmission (using a fresh random source), since real radio noise
// is not repeatable.

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

// addRadioEffect applies realistic AM radio transmission effects to PCM
// audio. It simulates pilot radio transmissions by applying bandwidth
// limiting, adding broadband static and engine noise, and shaping the
// signal with compression and squelch envelopes.
//
// The seed determines per-aircraft radio characteristics (noise levels,
// filter cutoffs, compression) so the same aircraft has a consistent
// sound. The scale controls overall noise intensity: 0 gives a clean
// signal with only bandwidth limiting, 1 is typical radio quality.
func addRadioEffect(pcm []int16, sampleRate int, seed uint32, scale float32) {
	if len(pcm) == 0 {
		return
	}

	// Derive per-aircraft characteristics deterministically from seed.
	params := &rand.Rand{}
	params.Seed(uint64(seed))

	hpCutoff := float32(220 + params.Intn(100))        // 220-320 Hz
	lpCutoff := float32(2800 + params.Intn(400))       // 2800-3200 Hz
	engineLpCutoff := float32(250 + params.Intn(200))  // 250-450 Hz engine rumble
	noiseGain := float32(0.04) + params.Float32()*0.06 // 0.04-0.10 static level
	engineGain := float32(0.15) + params.Float32()*0.5 // 0.08-0.20 engine level
	compDrive := float32(1.3) + params.Float32()*0.7   // 1.3-2.0 compression
	targetPeak := float32(0.65) + params.Float32()*0.1 // 0.65-0.75 normalized peak

	// Scale noise levels; filtering and compression always apply.
	noiseGain *= scale
	engineGain *= scale

	// Speech filters: highpass → lowpass → lowpass (cascaded for ~24 dB/oct
	// HF rolloff, matching measured AM aviation radio characteristics).
	hp := highpassBiquad(sampleRate, hpCutoff)
	lp1 := lowpassBiquad(sampleRate, lpCutoff)
	lp2 := lowpassBiquad(sampleRate, lpCutoff)

	// Pass 1: bandpass filter speech and find peak amplitude.
	buf := make([]float32, len(pcm))
	var peak float32
	for i, v := range pcm {
		x := float32(v) / 32767
		x = hp.process(x)
		x = lp1.process(x)
		x = lp2.process(x)
		buf[i] = x
		if a := math.Abs(x); a > peak {
			peak = a
		}
	}

	// Normalize filtered speech to target peak level.
	normGain := float32(1)
	if peak > 0.001 {
		normGain = targetPeak / peak
	}

	// Noise generation uses a fresh random source (noise should differ
	// each transmission, only the characteristics stay consistent).
	noiseRng := rand.Make()

	// Noise filters: bandpass static to match radio bandwidth.
	noiseLp := lowpassBiquad(sampleRate, lpCutoff)
	noiseHp := highpassBiquad(sampleRate, hpCutoff)

	// Engine rumble: lowpass filtered noise.
	engineLp := lowpassBiquad(sampleRate, engineLpCutoff)

	// Squelch envelope timing.
	squelchAttack := sampleRate * 5 / 1000  // 5ms fade-in
	speechRelease := sampleRate * 15 / 1000 // 15ms speech fade-out
	noiseRelease := sampleRate * 50 / 1000  // 50ms noise fade-out (squelch tail)

	// Precompute inverse of tanh(compDrive) for compression normalization.
	invTanhDrive := 1 / tanhApprox(compDrive)

	// Pass 2: normalize, compress, add noise, apply squelch.
	for i, speech := range buf {
		speech *= normGain

		// Soft compression via tanh, normalized for unity gain on small signals.
		speech = tanhApprox(speech*compDrive) * invTanhDrive

		// Generate filtered noise.
		white := noiseRng.Float32()*2 - 1
		static := noiseHp.process(noiseLp.process(white))

		white2 := noiseRng.Float32()*2 - 1
		engine := engineLp.process(white2)

		noise := static*noiseGain + engine*engineGain

		// Squelch envelope: speech fades fast, noise lingers briefly
		// creating the characteristic squelch tail heard on real radios.
		speechEnv := float32(1)
		noiseEnv := float32(1)

		if i < squelchAttack {
			t := float32(i) / float32(squelchAttack)
			speechEnv = t
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
