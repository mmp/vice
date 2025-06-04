// pkg/speech/tts.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package speech

import (
	"context"
	"errors"
	"fmt"
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

var ErrNoTTSKey = errors.New("VICE_TTS_KEY not set; unable TTS")

var client *texttospeech.Client
var voices []string
var voicepool []string

func InitTTS() error {
	ctx := context.Background()

	var err error
	client, err = texttospeech.NewClient(ctx)
	if err != nil {
		return err
	}

	resp, err := client.ListVoices(ctx, &texttospeechpb.ListVoicesRequest{})
	if err != nil {
		return err
	}

	for _, voice := range resp.Voices {
		for _, p := range []string{"en-US-Neural2", "en-US-Standard", "en-US-Wavenet"} {
			if strings.HasPrefix(voice.Name, p) {
				//fmt.Printf("%s: %v hz %d\n", voice.Name, voice.LanguageCodes, voice.NaturalSampleRateHertz)
				voices = append(voices, voice.Name)
				break
			}
		}
	}

	return nil
}

func GetRandomVoice() string {
	if client == nil {
		return "(NO TTS AVAILABLE)"
	}

	if len(voicepool) == 0 {
		voicepool = slices.Clone(voices)
	}
	r := rand.Make()
	i := r.Intn(len(voicepool))
	v := voicepool[i]
	voicepool = append(voicepool[:i], voicepool[i+1:]...)
	return v
}

func RequestTTS(voice, text string, lg *log.Logger) (<-chan []byte, error) {
	if client == nil {
		return nil, errors.New("Unable to initialize TTS")
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
				Name:         voice,
			},
			AudioConfig: &texttospeechpb.AudioConfig{
				SpeakingRate:    1.4,
				SampleRateHertz: 24000,
				AudioEncoding:   texttospeechpb.AudioEncoding_MP3,
			},
		}

		ctx := context.Background()
		resp, err := client.SynthesizeSpeech(ctx, &req)
		if err != nil {
			panic(err)
		}

		fmt.Printf("speech request latency %s size %d; voice %s for %s\n", time.Since(start), len(resp.AudioContent), voice, text)

		ch <- resp.AudioContent
	}()

	return ch, nil
}

/*
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
