// pkg/speech/tts.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package speech

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/rand"

	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"cloud.google.com/go/texttospeech/apiv1/texttospeechpb"
)

type TTSRequest struct {
	Model string  `json:"model"`
	Input string  `json:"input"`
	Voice string  `json:"voice"`
	Speed float64 `json:"speed"` // tts-1 and tts-1-hd only
	//Instructions string  `json:"instructions"`  // gpt-4o-mini-tts only
	Format string `json:"response_format"`
}

var ErrTTSUnavailable = errors.New("TTS service unavailable")

type Voice string

var client *texttospeech.Client
var voices []string
var voicesCh chan []string
var lg *log.Logger

func InitTTS(l *log.Logger) error {
	loadPronunciations()

	lg = l
	ctx, _ := context.WithTimeout(context.TODO(), 3*time.Second)

	var err error
	client, err = texttospeech.NewClient(ctx)
	if err != nil {
		return err
	}

	voicesCh = make(chan []string)
	go func() {
		defer close(voicesCh)

		resp, err := client.ListVoices(ctx, &texttospeechpb.ListVoicesRequest{})
		if err != nil {
			lg.Warnf("TTS ListVoices: %v\n", err)
			return
		}

		var voices []string
		for _, voice := range resp.Voices {
			for _, p := range []string{"en-US-Neural2", "en-US-Standard", "en-US-Wavenet"} {
				if strings.HasPrefix(voice.Name, p) {
					voices = append(voices, voice.Name)
					break
				}
			}
		}
		voicesCh <- voices
	}()

	return nil
}

// Voices are assigned by repeatedly copying the slice of available voices and then randomly
// selecting a voice from the copy and removing it. In this way we generally minimize reuse of
// the same voice for multiple aircraft.
var voicepool []string

func GetRandomVoice() (Voice, error) {
	// If the request for the voices is still outstanding, try to harvest
	// the result, but time out after a few seconds in case of network
	// issues.
	if voicesCh != nil && len(voices) == 0 {
		select {
		case voices = <-voicesCh:
			break
		case <-time.After(5 * time.Second):
			lg.Errorf("Timed out waiting for list of available voices. TTS disabled.")
			voicesCh = nil
			break
		}
	}

	if client == nil || len(voices) == 0 {
		return "__unavailable__", ErrTTSUnavailable
	}

	// We have voices; update the pool we're handing out voices from if
	// it's empty.
	if len(voicepool) == 0 {
		voicepool = slices.Clone(voices)
	}
	r := rand.Make()
	i := r.Intn(len(voicepool))
	v := Voice(voicepool[i])
	voicepool = append(voicepool[:i], voicepool[i+1:]...)
	return v, nil
}

// RequestTTS requests synthesis of the provided text using the given
// voice. If successful, it returns a chan that provides the MP3 of the
// synthesized voice when it is available.
func RequestTTS(voice Voice, text string, lg *log.Logger) (<-chan []byte, error) {
	if client == nil {
		return nil, ErrTTSUnavailable
	}
	ch := make(chan []byte)
	start := time.Now()

	go func() {
		defer close(ch)

		req := texttospeechpb.SynthesizeSpeechRequest{
			Input: &texttospeechpb.SynthesisInput{
				InputSource: &texttospeechpb.SynthesisInput_Text{Text: text},
			},
			Voice: &texttospeechpb.VoiceSelectionParams{
				LanguageCode: "en-US",
				Name:         string(voice),
			},
			AudioConfig: &texttospeechpb.AudioConfig{
				SpeakingRate:    1.4,
				SampleRateHertz: 24000,
				AudioEncoding:   texttospeechpb.AudioEncoding_MP3,
			},
		}

		resp, err := client.SynthesizeSpeech(context.Background(), &req)
		if err != nil {
			lg.Errorf("TTS: speech %q voice %s error %v", text, voice, err)
		} else {
			lg.Infof("Synthesized speech %q latency %s result size %d", text, time.Since(start), len(resp.AudioContent))

			ch <- resp.AudioContent
		}
	}()

	return ch, nil
}

/*
var ErrNoTTSKey = errors.New("VICE_TTS_KEY not set; unable TTS")

func RequestTTS(key, text string, lg *log.Logger) (<-chan []byte, error) {
	apiKey := os.Getenv("VICE_TTS_KEY")
	if apiKey == "" {
		return nil, ErrNoTTSKey
	}

	voices := []string{"alloy", "ash", "coral", "echo", "fable", "onyx", "nova", "sage", "shimmer"}
	voice := voices[int(util.HashString64(key)&0xffff)%len(voices)]

	fmt.Printf("voice %q for %q\n", voice, key)

	client := &http.Client{}

	jd, err := json.Marshal(TTSRequest{
		Model: "tts-1",
		Input: text,
		Voice: voice,
		Speed: 1.5,
		Format: "pcm",
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/speech", bytes.NewReader(jd))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	ch := make(chan []byte)
	start := time.Now()

	go func() {
		defer close(ch)

		if resp, err := client.Do(req); err != nil {
			lg.Errorf("HTTP Error: %v", err)
		} else {
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				lg.Errorf("HTTP Error: %s\n", body)
			} else {
				fmt.Printf("speech request latency %s\n", time.Since(start))
				ch <- body
			}
		}
	}()

	return ch, nil
}
*/
