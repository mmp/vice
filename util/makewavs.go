package main

/*
Make WAV files for various sound effects, following real-world STARS:

https://www.sciencedirect.com/science/article/pii/S2590198221002074#tblfn2

- transponder emergency codes 1400 Hz 600ms on 250 ms off
- CA: 1600 Hz 60ms on 60 ms off
- mode c intruder 1600 Hz 130ms on 130ms off
- MSAW 260 ms at 1600 Hz, then 180 ms at 2000 Hz

*/

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// thx chat gpt for the WAV code!
type WAVHeader struct {
	RIFF            [4]byte
	OverallSize     uint32
	WAVE            [4]byte
	FmtChunkMarker  [4]byte
	LengthOfFmt     uint32
	FormatType      uint16
	Channels        uint16
	SampleRate      uint32
	ByteRate        uint32
	BlockAlign      uint16
	BitsPerSample   uint16
	DataChunkHeader [4]byte
	DataSize        uint32
}

func main() {
	sampleRate := uint32(44100) // Sample rate
	bitsPerSample := uint16(16) // Bits per sample

	// spec is alternating time in ms, frequency in Hz. 0 frequency -> silence
	write := func(fn string, spec []float64) {
		// Find total ms of the sample
		ms := float64(0)
		for i, s := range spec {
			if i%2 == 0 {
				ms += s
			}
		}

		t := float64(0) // time in ms
		t0 := spec[0]
		dt := 1000 / float64(sampleRate) // between samples
		var b []byte
		for len(spec) > 0 {
			if t > t0 {
				t0 += spec[0]
				spec = spec[2:]
				continue
			}
			v := float64(0)
			if spec[1] != 0 {
				v = 28000 * (1 + math.Sin(2*3.14159*t*spec[1]/1000)) / 2
			}
			b = append(b, byte(uint16(v)&0xff), byte(uint16(v)>>8))
			t += dt
		}

		numSamples := uint32(len(b) / 2) // 16-bit

		// Create WAV header
		var header WAVHeader
		copy(header.RIFF[:], "RIFF")
		header.OverallSize = 36 + numSamples*uint32(bitsPerSample/8)
		copy(header.WAVE[:], "WAVE")
		copy(header.FmtChunkMarker[:], "fmt ")
		header.LengthOfFmt = 16
		header.FormatType = 1 // PCM
		header.Channels = 1
		header.SampleRate = sampleRate
		header.ByteRate = sampleRate * uint32(header.Channels) * uint32(bitsPerSample/8)
		header.BlockAlign = header.Channels * (bitsPerSample / 8)
		header.BitsPerSample = bitsPerSample
		copy(header.DataChunkHeader[:], "data")
		header.DataSize = numSamples * uint32(bitsPerSample/8)

		file, err := os.Create(fn)
		if err != nil {
			panic(err)
		}
		binary.Write(file, binary.LittleEndian, &header)
		file.Write(b)
		file.Close()
	}

	// CA: 1600Hz 60ms on, 60 ms off
	write("../resources/audio/ca.wav", []float64{60, 1600, 60, 0})

	// Emergency squawk code: 1400Hz, 600ms on, 250ms off
	write("../resources/audio/emergency.wav", []float64{600, 1400, 250, 0})

	// Mode C intruder: 1600Hz, 130ms on, 130ms off
	write("../resources/audio/intruder.wav", []float64{130, 1600, 130, 0})

	// MSAW: 1600Hz for 260ms, 2000Hz for 180ms
	write("../resources/audio/msaw.wav", []float64{260, 1600, 180, 2000})
}
