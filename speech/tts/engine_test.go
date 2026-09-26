// speech/tts/engine_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package tts

import (
	"testing"

	"github.com/mmp/vice/math"
)

// TestTimeStretch checks that timeStretch changes a tone's duration by the
// rate but leaves its pitch alone.
func TestTimeStretch(t *testing.T) {
	const sampleRate = 24000
	const freq = 220

	tone := make([]float32, sampleRate)
	for i := range tone {
		tone[i] = 0.5 * math.Sin(2*math.Pi*freq*float32(i)/sampleRate)
	}

	for _, rate := range []float32{0.75, 1.5, 2.5} {
		out := timeStretch(tone, sampleRate, rate)
		if want := int(float32(len(tone)) / rate); len(out) != want {
			t.Errorf("rate %.2f: got %d samples, want %d", rate, len(out), want)
		}

		// Count rising zero crossings away from the ends, where the
		// windows taper.
		lo, hi := sampleRate/10, len(out)-sampleRate/10
		crossings := 0
		for i := lo + 1; i < hi; i++ {
			if out[i-1] < 0 && out[i] >= 0 {
				crossings++
			}
		}
		if got := float32(crossings) * sampleRate / float32(hi-lo); math.Abs(got-freq) > 0.02*freq {
			t.Errorf("rate %.2f: tone is %.1f Hz, want %d Hz", rate, got, freq)
		}
	}

	if out := timeStretch(tone, sampleRate, 1); &out[0] != &tone[0] {
		t.Errorf("rate 1 should return the input unchanged")
	}
}

// TestKokoroTempo checks that kokoroTempo passes through the measured
// tempos and never slows down as the requested speed goes up.
func TestKokoroTempo(t *testing.T) {
	for _, m := range kokoroTempos {
		if got := kokoroTempo(m.speed); math.Abs(got-m.tempo) > 1e-5 {
			t.Errorf("speed %.2f: got tempo %.4f, want %.4f", m.speed, got, m.tempo)
		}
	}

	prev := kokoroTempo(0.7)
	for speed := float32(0.71); speed <= 3; speed += 0.01 {
		tempo := kokoroTempo(speed)
		if tempo < prev {
			t.Errorf("tempo drops from %.4f to %.4f at speed %.2f", prev, tempo, speed)
		}
		prev = tempo
	}
}
