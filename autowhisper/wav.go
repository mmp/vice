package autowhisper

import (
	"errors"
	"math"
	"os"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

// ReadWavAsFloat32Mono16k reads a WAV from path and returns mono 16kHz PCM as float32 samples in [-1,1].
// It supports input WAVs with any sample rate and 1 or 2 channels, converting as needed.
func ReadWavAsFloat32Mono16k(path string) ([]float32, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	dec := wav.NewDecoder(fh)
	buf, err := dec.FullPCMBuffer()
	if err != nil {
		return nil, err
	}
	if buf == nil || buf.Data == nil {
		return nil, errors.New("invalid or empty wav data")
	}

	inRate := dec.SampleRate
	chans := dec.NumChans
	if inRate <= 0 {
		return nil, errors.New("invalid sample rate")
	}
	if chans != 1 && chans != 2 {
		return nil, errors.New("unsupported channel count")
	}

	// Convert to float64 for processing
	fbuf := audio.FloatBuffer{Data: make([]float64, len(buf.Data)), Format: &audio.Format{NumChannels: int(chans), SampleRate: int(inRate)}}
	for i, v := range buf.Data {
		fbuf.Data[i] = float64(v) / float64(1<<15)
		if fbuf.Data[i] > 1 {
			fbuf.Data[i] = 1
		} else if fbuf.Data[i] < -1 {
			fbuf.Data[i] = -1
		}
	}

	// Mixdown to mono if stereo
	mono := fbuf.Data
	if chans == 2 {
		mono = make([]float64, len(fbuf.Data)/2)
		for i := 0; i < len(mono); i++ {
			l := fbuf.Data[2*i]
			r := fbuf.Data[2*i+1]
			mono[i] = 0.5 * (l + r)
		}
	}

	// Resample to 16k using simple linear interpolation (adequate for speech)
	const outRate = 16000
	if inRate != outRate {
		ratio := float64(outRate) / float64(inRate)
		outLen := int(math.Ceil(float64(len(mono)) * ratio))
		res := make([]float64, outLen)
		for i := 0; i < outLen; i++ {
			srcPos := float64(i) / ratio
			j := int(math.Floor(srcPos))
			t := srcPos - float64(j)
			if j+1 < len(mono) {
				res[i] = (1-t)*mono[j] + t*mono[j+1]
			} else {
				res[i] = mono[j]
			}
		}
		mono = res
	}

	// Convert to float32
	out := make([]float32, len(mono))
	for i := range mono {
		out[i] = float32(mono[i])
	}
	return out, nil
}
