// pkg/platform/audio.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package platform

// typedef unsigned char uint8;
// void audioCallback(void *userdata, uint8 *stream, int len);
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"

	"github.com/tosone/minimp3"
	"github.com/veandco/go-sdl2/sdl"
)

const AudioSampleRate = 44100

type audioEngine struct {
	pinner   runtime.Pinner
	effects  []audioEffect
	speechq  []int16
	speechcb func()
	mu       sync.Mutex
	volume   int
}

type audioEffect struct {
	pcm            []int16
	playOnceCount  int
	playContinuous bool
	playOffset     int
}

func (a *audioEngine) Initialize(lg *log.Logger) {
	lg.Info("Starting to initialize audio")

	a.volume = 10

	user := (unsafe.Pointer)(a)
	a.pinner.Pin(user)

	spec := sdl.AudioSpec{
		Freq:     AudioSampleRate,
		Format:   sdl.AUDIO_S16SYS,
		Channels: 1,
		Samples:  2048,
		Callback: sdl.AudioCallback(C.audioCallback),
		UserData: user,
	}
	if err := sdl.OpenAudio(&spec, nil); err != nil {
		lg.Errorf("SDL OpenAudio: %v", err)
	}
	sdl.PauseAudio(false)

	lg.Info("Finished initializing audio")
}

func (a *audioEngine) AddPCM(pcm []byte, rate int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if rate != AudioSampleRate {
		return 0, fmt.Errorf("%d: sample rate doesn't match audio engine's %d",
			rate, AudioSampleRate)
	}

	a.effects = append(a.effects, audioEffect{pcm: pcm16FromBytes(pcm)})
	return len(a.effects), nil
}

func pcm16FromBytes(pcm []byte) []int16 {
	pcm16 := make([]int16, len(pcm)/2)
	for i := range pcm16 {
		pcm16[i] = int16(pcm[2*i]) | (int16(pcm[2*i+1]) << 8)
	}
	return pcm16
}

func (a *audioEngine) AddMP3(mp3 []byte) (int, error) {
	if dec, pcm, err := minimp3.DecodeFull(mp3); err != nil {
		return -1, err
	} else if dec.Channels != 1 {
		return -1, fmt.Errorf("expected 1 channel, got %d", dec.Channels)
	} else {
		return a.AddPCM(pcm, dec.SampleRate)
	}
}

func (a *audioEngine) TryEnqueueSpeechMP3(mp3 []byte, finished func()) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.speechq) > 0 {
		return ErrCurrentlyPlayingSpeech
	}

	if _, pcm, err := minimp3.DecodeFull(mp3); err != nil {
		return err
	} else {
		// Poor man's resampling: repeat each sample rep times to get close
		// to the standard rate.
		pcm16 := pcm16FromBytes(pcm)
		rep := 2
		pcmr := make([]int16, rep*len(pcm16))
		for i := range len(pcm16) {
			for j := range rep {
				pcmr[rep*i+j] = pcm16[i]
			}
		}

		addNoise(pcmr)

		a.speechq = pcmr
		a.speechcb = finished

		return nil
	}
}

func addNoise(pcm []int16) {
	r := rand.Make()
	amp := 256 + r.Intn(512)
	freqs := []int{10 + r.Intn(5), 18 + r.Intn(5)}
	noises := []int{0, 0}
	for i, v := range pcm {
		n := 0
		for j := range freqs {
			if i%freqs[j] == 0 {
				noises[j] = -amp + r.Intn(2*amp)
			}
			n += noises[j]
		}

		pcm[i] = int16(math.Clamp(n+int(v), -32768, 32767))
	}

	// Random squelch
	if false && r.Float32() < 0.1 {
		length := AudioSampleRate/2 + r.Intn(AudioSampleRate/4)
		freqs := []int{100 + r.Intn(50), 500 + r.Intn(250), 1500 + r.Intn(500), 4000 + r.Intn(1000)}
		start := r.Intn(4 * len(pcm) / 5)
		const amp = 20000

		n := min(len(pcm), start+length)
		for i := start; i < n; i++ {
			sq := -amp + r.Intn(2*amp)
			for _, fr := range freqs {
				sq += int(amp * math.Sin(float32(fr*i)*2*3.14159/AudioSampleRate))
			}
			sq /= 1 + len(freqs) // normalize
			pcm[i] = int16(math.Clamp(int(pcm[i]/4)+sq, -32768, 32767))
		}
	}
}

func (a *audioEngine) SetAudioVolume(vol int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.volume = math.Clamp(vol, 0, 10)
}

func (a *audioEngine) PlayAudioOnce(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if index == 0 {
		return
	}

	a.effects[index-1].playOnceCount++
}

func (a *audioEngine) StartPlayAudioContinuous(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if index == 0 {
		return
	}

	a.effects[index-1].playContinuous = true
}

func (a *audioEngine) StopPlayAudio(index int) {
	if index == 0 {
		return
	}

	// Don't check if audio or the effect is enabled since if those were
	// changed in the UI and the sound is playing, we still want to stop...
	a.mu.Lock()
	a.effects[index-1].playContinuous = false
	a.effects[index-1].playOffset = 0
	a.effects[index-1].playOnceCount = 0
	a.mu.Unlock()
}

//export audioCallback
func audioCallback(user unsafe.Pointer, ptr *C.uint8, size C.int) {
	n := int(size)
	out := unsafe.Slice(ptr, n)
	a := (*audioEngine)(user)

	accum := make([]int, n/2)

	a.mu.Lock()
	defer a.mu.Unlock()

	as := 0 // accum index for speech
	for len(a.speechq) > 0 && as < len(accum) {
		accum[as] = int(a.speechq[0])
		as++
		a.speechq = a.speechq[1:]
	}
	if len(a.speechq) == 0 && as > 0 && a.speechcb != nil {
		// We finished the speech; call the callback function
		a.speechcb()
		a.speechcb = nil
	}

	for i := range a.effects {
		e := &a.effects[i]
		buf := make([]int16, n/2)
		bread := buf
		for len(bread) > 0 && (e.playContinuous || e.playOnceCount > 0) {
			nc := copy(bread, e.pcm[e.playOffset:])
			e.playOffset += nc
			bread = bread[nc:]

			if e.playOffset == len(e.pcm) {
				e.playOffset = 0
				if e.playOnceCount > 0 {
					e.playOnceCount--
				}
			}
		}

		for i := 0; i < len(buf); i++ {
			accum[i] += int(buf[i])
		}
	}

	for i := 0; i < n/2; i++ {
		v := math.Clamp(accum[i]*a.volume/10, -32768, 32767)
		out[2*i] = C.uint8(v & 0xff)
		out[2*i+1] = C.uint8((v >> 8) & 0xff)
	}
}
