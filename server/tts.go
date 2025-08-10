// server/tts.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"strings"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// REST API request/response structs
type synthesisInput struct {
	Text string `json:"text"`
}

type voiceSelectionParams struct {
	LanguageCode string `json:"languageCode"`
	Name         string `json:"name"`
}

type audioConfig struct {
	AudioEncoding   string  `json:"audioEncoding"`
	SpeakingRate    float64 `json:"speakingRate"`
	SampleRateHertz int     `json:"sampleRateHertz"`
}

type synthesizeRequest struct {
	Input       synthesisInput       `json:"input"`
	Voice       voiceSelectionParams `json:"voice"`
	AudioConfig audioConfig          `json:"audioConfig"`
}

type synthesizeResponse struct {
	AudioContent string `json:"audioContent"` // base64 encoded audio
}

type voiceInfo struct {
	LanguageCodes          []string `json:"languageCodes"`
	Name                   string   `json:"name"`
	SsmlGender             string   `json:"ssmlGender"`
	NaturalSampleRateHertz int      `json:"naturalSampleRateHertz"`
}

type listVoicesResponse struct {
	Voices []voiceInfo `json:"voices"`
}

var ErrTTSUnavailable = errors.New("TTS service unavailable")

var tokenSource oauth2.TokenSource
var httpClient *http.Client
var voices []string
var voicesCh chan []string

func init() {
	tryInitializeGoogleTTS()
}

func tryInitializeGoogleTTS() {
	creds := os.Getenv("VICE_GCS_CREDENTIALS")
	if creds == "" {
		fmt.Printf("VICE_GCS_CREDENTIALS not set: not initializing local TTS\n")
		return
	}

	voicesCh = make(chan []string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Create JWT config from service account JSON
		config, err := google.JWTConfigFromJSON(
			[]byte(creds),
			"https://www.googleapis.com/auth/cloud-platform",
		)
		if err != nil {
			fmt.Printf("TTS JWT config: %v\n", err)
			return
		}

		// Create token source and HTTP client
		tokenSource = config.TokenSource(ctx)
		httpClient = oauth2.NewClient(ctx, tokenSource)

		defer close(voicesCh)

		// List available voices
		resp, err := httpClient.Get("https://texttospeech.googleapis.com/v1/voices")
		if err != nil {
			fmt.Printf("TTS ListVoices request: %v\n", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("TTS ListVoices status: %d\n", resp.StatusCode)
			return
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("TTS ListVoices read: %v\n", err)
			return
		}

		var voicesResp listVoicesResponse
		if err := json.Unmarshal(body, &voicesResp); err != nil {
			fmt.Printf("TTS ListVoices unmarshal: %v\n", err)
			return
		}

		var voices []string
		for _, voice := range voicesResp.Voices {
			for _, p := range []string{"en-US-Neural2", "en-US-Standard", "en-US-Wavenet"} {
				if strings.HasPrefix(voice.Name, p) {
					voices = append(voices, voice.Name)
					break
				}
			}
		}
		voicesCh <- voices
	}()
}

// GoogleTTSProvider implements sim.TTSProvider using Google Cloud TTS
type GoogleTTSProvider struct {
	lg *log.Logger
}

func NewGoogleTTSProvider(lg *log.Logger) sim.TTSProvider {
	if httpClient == nil {
		return nil
	}
	return &GoogleTTSProvider{lg: lg}
}

func (g *GoogleTTSProvider) GetAllVoices() sim.TTSVoicesFuture {
	vch := make(chan []sim.Voice)
	errch := make(chan error)
	fut := sim.TTSVoicesFuture{VoicesCh: vch, ErrCh: errch}

	go func() {
		defer close(vch)
		defer close(errch)

		// The RPC to Google TTS for the voices list was made when
		// GoogleTTSProvider was created, with the hope that it would
		// return by the time code elsewhere asked for it.
		if voicesCh != nil && len(voices) == 0 {
			select {
			case voices = <-voicesCh:
				break
			case <-time.After(5 * time.Second):
				errch <- errors.New("Google TTS list voices call timed out")
				voicesCh = nil
				return
			}
		}

		if httpClient == nil || len(voices) == 0 {
			errch <- errors.New("Unable to get voices from Google TTS")
			return
		}

		// Convert string slice to Voice slice
		result := make([]sim.Voice, len(voices))
		for i, v := range voices {
			result[i] = sim.Voice(v)
		}
		vch <- result
	}()

	return fut
}

func (g *GoogleTTSProvider) TextToSpeech(voice sim.Voice, text string) sim.TTSSpeechFuture {
	mp3ch := make(chan []byte)
	errch := make(chan error)
	fut := sim.TTSSpeechFuture{Mp3Ch: mp3ch, ErrCh: errch}

	start := time.Now()

	go func() {
		defer close(mp3ch)
		defer close(errch)

		if httpClient == nil {
			errch <- ErrTTSUnavailable
			return
		}

		// Create the request payload
		req := synthesizeRequest{
			Input: synthesisInput{
				Text: text,
			},
			Voice: voiceSelectionParams{
				LanguageCode: "en-US",
				Name:         string(voice),
			},
			AudioConfig: audioConfig{
				AudioEncoding:   "MP3",
				SpeakingRate:    1.4,
				SampleRateHertz: 24000,
			},
		}

		reqBody, err := json.Marshal(req)
		if err != nil {
			errch <- err
			return
		}

		resp, err := httpClient.Post(
			"https://texttospeech.googleapis.com/v1/text:synthesize",
			"application/json",
			bytes.NewReader(reqBody),
		)
		if err != nil {
			errch <- err
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errch <- fmt.Errorf("TTS: speech %q voice %s status %d", text, voice, resp.StatusCode)
			return
		}

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			errch <- err
			return
		}

		var synthResp synthesizeResponse
		if err := json.Unmarshal(respBody, &synthResp); err != nil {
			errch <- err
			return
		}

		mp3, err := base64.StdEncoding.DecodeString(synthResp.AudioContent)
		if err != nil {
			errch <- err
			return
		}

		g.lg.Infof("Synthesized speech %q latency %s mp3 size %d", text, time.Since(start), len(mp3))
		mp3ch <- mp3
	}()

	return fut
}

///////////////////////////////////////////////////////////////////////////
// RemoteTTSProvider

// TTSRequest represents a text-to-speech request (shared between client and server)
type TTSRequest struct {
	Voice    sim.Voice
	Text     string
	ClientIP string // Automatically populated by LoggingServerCodec
}

// RemoteTTSProvider implements sim.TTSProvider by making RPC calls to a remote server
type RemoteTTSProvider struct {
	client       *rpc.Client
	lg           *log.Logger
	cachedVoices []sim.Voice
}

// NewRemoteTTSProvider creates a new RemoteTTSProvider that connects to the specified server
func NewRemoteTTSProvider(serverAddress string, lg *log.Logger) (*RemoteTTSProvider, error) {
	lg.Debugf("%s: connecting for TTS", serverAddress)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", serverAddress, 5*time.Second)
	if err != nil {
		lg.Warnf("%s: unable to connect: %v", serverAddress, err)
		return nil, fmt.Errorf("unable to connect to TTS server: %w", err)
	}
	lg.Debugf("%s: connected in %s", serverAddress, time.Since(start))

	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		return nil, err
	}

	codec := util.MakeMessagepackClientCodec(cc)
	codec = util.MakeLoggingClientCodec(serverAddress, codec, lg)
	client := rpc.NewClientWithCodec(codec)

	return &RemoteTTSProvider{
		client: client,
		lg:     lg,
	}, nil
}

func (r *RemoteTTSProvider) callWithTimeout(serviceMethod string, args any, reply any) error {
	call := r.client.Go(serviceMethod, args, reply, nil)

	for {
		select {
		case <-call.Done:
			return call.Error
		case <-time.After(5 * time.Second):
			if !util.DebuggerIsRunning() {
				return ErrRPCTimeout
			}
		}
	}
}

// GetAllVoices returns all available voices from the remote server, cached after first call
func (r *RemoteTTSProvider) GetAllVoices() sim.TTSVoicesFuture {
	vch := make(chan []sim.Voice)
	errch := make(chan error)
	fut := sim.TTSVoicesFuture{VoicesCh: vch, ErrCh: errch}

	go func() {
		defer close(vch)
		defer close(errch)

		if r.cachedVoices == nil {
			// Fetch voices from remote server
			var voices []sim.Voice
			if err := r.callWithTimeout("SimManager.GetAllVoices", struct{}{}, &voices); err != nil {
				errch <- err
				return
			}

			// Cache the voices for future calls
			r.cachedVoices = voices
		}
		vch <- r.cachedVoices
	}()

	return fut
}

// TextToSpeech converts text to speech using the remote server
func (r *RemoteTTSProvider) TextToSpeech(voice sim.Voice, text string) sim.TTSSpeechFuture {
	mp3ch := make(chan []byte)
	errch := make(chan error)
	fut := sim.TTSSpeechFuture{Mp3Ch: mp3ch, ErrCh: errch}

	go func() {
		defer close(mp3ch)
		defer close(errch)

		var mp3 []byte
		if err := r.callWithTimeout("SimManager.TextToSpeech", &TTSRequest{
			Voice: voice,
			Text:  text,
		}, &mp3); err != nil {
			errch <- err
		} else {
			mp3ch <- mp3
		}
	}()

	return fut
}
