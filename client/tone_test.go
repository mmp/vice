// client/tone_test.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import "testing"

func TestGenerateHeterodyne_Length(t *testing.T) {
	pcm := GenerateHeterodyne(250)
	const sampleRate = 16000
	want := sampleRate * 250 / 1000
	if len(pcm) != want {
		t.Errorf("len = %d, want %d", len(pcm), want)
	}
}

func TestGenerateHeterodyne_NonZero(t *testing.T) {
	pcm := GenerateHeterodyne(250)

	allZero := true
	var maxAbs int32
	for _, s := range pcm {
		if s != 0 {
			allZero = false
		}
		v := int32(s)
		if v < 0 {
			v = -v
		}
		if v > maxAbs {
			maxAbs = v
		}
	}
	if allZero {
		t.Fatal("PCM is all zeros")
	}
	if maxAbs < 1000 {
		t.Errorf("peak amplitude %d is too quiet (expected at least ~1000 of int16)", maxAbs)
	}
}

func TestGenerateHeterodyne_ZeroDurationReturnsEmpty(t *testing.T) {
	if pcm := GenerateHeterodyne(0); len(pcm) != 0 {
		t.Errorf("zero-duration PCM should be empty, got %d samples", len(pcm))
	}
}
