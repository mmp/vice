package tts

import (
	"encoding/binary"
	"os"
	"testing"
)

// TestAddRadioEffect verifies the radio effect processing on a synthetic
// sine wave and writes the result to a WAV file for manual inspection.
func TestAddRadioEffect(t *testing.T) {
	const sampleRate = 44100
	const duration = 3 // seconds
	const numSamples = sampleRate * duration

	// Generate a 440 Hz sine wave at -6 dBFS as test speech signal.
	pcm := make([]int16, numSamples)
	for i := range pcm {
		phase := float32(i) * 440 * 2 * 3.14159265 / sampleRate
		pcm[i] = int16(16383 * sinApprox(phase)) // -6 dBFS
	}

	// Write original for comparison.
	writeWAV(t, "/tmp/radio_test_original.wav", pcm, sampleRate)

	// Process with different seeds to verify per-aircraft variation.
	for _, tc := range []struct {
		name string
		seed uint32
	}{
		{"seed42", 42},
		{"seed999", 999},
		{"seed0", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := make([]int16, len(pcm))
			copy(out, pcm)

			addRadioEffect(out, sampleRate, tc.seed, 1.0)

			// Verify output isn't silent.
			var maxAbs int16
			for _, v := range out {
				if v > maxAbs {
					maxAbs = v
				} else if -v > maxAbs {
					maxAbs = -v
				}
			}
			if maxAbs == 0 {
				t.Error("output is silent")
			}

			// Verify output differs from input (effect was applied).
			differs := false
			for i := range out {
				if out[i] != pcm[i] {
					differs = true
					break
				}
			}
			if !differs {
				t.Error("output is identical to input")
			}

			writeWAV(t, "/tmp/radio_test_"+tc.name+".wav", out, sampleRate)
		})
	}

	// Test with scale=0 (should still bandpass but no noise).
	t.Run("scale0", func(t *testing.T) {
		out := make([]int16, len(pcm))
		copy(out, pcm)
		addRadioEffect(out, sampleRate, 42, 0.0)
		writeWAV(t, "/tmp/radio_test_scale0.wav", out, sampleRate)
	})

	// Test determinism: same seed produces same characteristics
	// (noise pattern varies but filter/compression parameters are the same).
	t.Run("determinism", func(t *testing.T) {
		out1 := make([]int16, len(pcm))
		out2 := make([]int16, len(pcm))
		copy(out1, pcm)
		copy(out2, pcm)
		addRadioEffect(out1, sampleRate, 42, 1.0)
		addRadioEffect(out2, sampleRate, 42, 1.0)

		// The outputs will differ in noise (random each time) but the
		// overall characteristics (level, spectral shape) should be similar.
		// Check samples after squelch attack (first 5ms = ~220 samples).
		var max1, max2 int16
		for _, v := range out1[sampleRate/100:] { // skip first 10ms
			if v > max1 {
				max1 = v
			} else if -v > max1 {
				max1 = -v
			}
		}
		for _, v := range out2[sampleRate/100:] {
			if v > max2 {
				max2 = v
			} else if -v > max2 {
				max2 = -v
			}
		}
		if max1 == 0 {
			t.Error("first output is silent")
		}
		if max2 == 0 {
			t.Error("second output is silent")
		}
	})

	// Test empty input.
	t.Run("empty", func(t *testing.T) {
		addRadioEffect(nil, sampleRate, 42, 1.0)
		addRadioEffect([]int16{}, sampleRate, 42, 1.0)
	})

	t.Log("WAV files written to /tmp/radio_test_*.wav")
}

// TestAddRadioEffectSpeech processes a real speech WAV file if available.
func TestAddRadioEffectSpeech(t *testing.T) {
	const inputPath = "/tmp/seattle_atc.wav"
	if _, err := os.Stat(inputPath); os.IsNotExist(err) {
		t.Skip("reference audio not available")
	}

	// Read a 5-second segment from the reference audio.
	pcm, sampleRate := readWAVSegment(t, inputPath, 25, 5)

	// Process with radio effect.
	out := make([]int16, len(pcm))
	copy(out, pcm)
	addRadioEffect(out, sampleRate, 12345, 1.0)

	writeWAV(t, "/tmp/radio_test_speech_original.wav", pcm, sampleRate)
	writeWAV(t, "/tmp/radio_test_speech_processed.wav", out, sampleRate)
	t.Log("Speech test WAVs written to /tmp/radio_test_speech_*.wav")
}

func sinApprox(x float32) float32 {
	// Reduce to [0, 2*pi].
	const twoPi = 6.2831853
	for x > twoPi {
		x -= twoPi
	}
	for x < 0 {
		x += twoPi
	}
	// Bhaskara I approximation.
	const pi = 3.14159265
	if x > pi {
		return -sinApprox(x - pi)
	}
	return 16 * x * (pi - x) / (5*pi*pi - 4*x*(pi-x))
}

func writeWAV(t *testing.T, path string, pcm []int16, sampleRate int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	dataSize := len(pcm) * 2
	// RIFF header
	f.Write([]byte("RIFF"))
	binary.Write(f, binary.LittleEndian, uint32(36+dataSize))
	f.Write([]byte("WAVE"))
	// fmt chunk
	f.Write([]byte("fmt "))
	binary.Write(f, binary.LittleEndian, uint32(16)) // chunk size
	binary.Write(f, binary.LittleEndian, uint16(1))  // PCM
	binary.Write(f, binary.LittleEndian, uint16(1))  // mono
	binary.Write(f, binary.LittleEndian, uint32(sampleRate))
	binary.Write(f, binary.LittleEndian, uint32(sampleRate*2)) // byte rate
	binary.Write(f, binary.LittleEndian, uint16(2))            // block align
	binary.Write(f, binary.LittleEndian, uint16(16))           // bits per sample
	// data chunk
	f.Write([]byte("data"))
	binary.Write(f, binary.LittleEndian, uint32(dataSize))
	binary.Write(f, binary.LittleEndian, pcm)
}

func readWAVSegment(t *testing.T, path string, startSec, durationSec float64) ([]int16, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	// Skip RIFF header, read fmt chunk to get sample rate.
	header := make([]byte, 44)
	if _, err := f.Read(header); err != nil {
		t.Fatal(err)
	}
	sampleRate := int(binary.LittleEndian.Uint32(header[24:28]))

	startSample := int(startSec * float64(sampleRate))
	numSamples := int(durationSec * float64(sampleRate))

	// Seek to start position (44 byte header + 2 bytes per sample).
	f.Seek(int64(44+startSample*2), 0)

	pcm := make([]int16, numSamples)
	if err := binary.Read(f, binary.LittleEndian, pcm); err != nil {
		t.Fatal(err)
	}
	return pcm, sampleRate
}
