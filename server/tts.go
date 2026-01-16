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
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/rand"
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

var ErrMissingTTSCredentials = errors.New("VICE_GCS_CREDENTIALS not set")
var ErrTTSUnavailable = errors.New("TTS service unavailable")

// ttsUsageStats tracks TTS usage per IP address
type ttsUsageStats struct {
	Calls    int
	Words    int
	LastUsed time.Time
}

///////////////////////////////////////////////////////////////////////////
// VoiceAssigner

// VoiceAssigner manages the pool of available TTS voices and assigns them
// to aircraft callsigns. Each aircraft gets a consistent voice throughout
// the session.
type VoiceAssigner struct {
	voicePool           []sim.Voice
	pendingVoicesFuture *sim.TTSVoicesFuture
	aircraftVoices      map[av.ADSBCallsign]sim.Voice
	rand                *rand.Rand
}

// NewVoiceAssigner creates a new VoiceAssigner.
func NewVoiceAssigner() *VoiceAssigner {
	return &VoiceAssigner{
		aircraftVoices: make(map[av.ADSBCallsign]sim.Voice),
		rand:           rand.Make(),
	}
}

// TryInit attempts non-blocking initialization of the voice pool from the
// TTS provider. Returns true if the pool is ready for use.
func (va *VoiceAssigner) TryInit(tts sim.TTSProvider, lg *log.Logger) bool {
	if tts == nil || len(va.voicePool) > 0 {
		return len(va.voicePool) > 0
	}

	// Start an async request for voices if we haven't already
	if va.pendingVoicesFuture == nil {
		fut := tts.GetAllVoices()
		va.pendingVoicesFuture = &fut
	}

	// Check if voices are ready (non-blocking)
	select {
	case voices, ok := <-va.pendingVoicesFuture.VoicesCh:
		if ok {
			va.voicePool = voices
		}
		va.pendingVoicesFuture = nil
		return len(va.voicePool) > 0
	case err, ok := <-va.pendingVoicesFuture.ErrCh:
		if ok {
			lg.Warnf("TTS GetAllVoices error: %v", err)
		}
		va.pendingVoicesFuture = nil
		return false
	default:
		// Voices not ready yet
		return false
	}
}

// GetVoice returns the voice assigned to an aircraft, assigning one if needed.
// Returns empty string and false if no voices are available yet.
func (va *VoiceAssigner) GetVoice(callsign av.ADSBCallsign) (sim.Voice, bool) {
	// Check if already assigned
	if voice, ok := va.aircraftVoices[callsign]; ok {
		return voice, true
	}

	// Need to assign a new voice
	if len(va.voicePool) == 0 {
		return "", false
	}

	// Take from pool and assign
	voice := va.voicePool[0]
	va.voicePool = va.voicePool[1:]
	va.aircraftVoices[callsign] = voice

	// Refill pool if empty (shuffle all voices)
	if len(va.voicePool) == 0 {
		va.voicePool = make([]sim.Voice, len(va.aircraftVoices))
		i := 0
		for _, v := range va.aircraftVoices {
			va.voicePool[i] = v
			i++
		}
		// Fisher-Yates shuffle
		for i := len(va.voicePool) - 1; i > 0; i-- {
			j := va.rand.Intn(i + 1)
			va.voicePool[i], va.voicePool[j] = va.voicePool[j], va.voicePool[i]
		}
	}

	return voice, true
}

///////////////////////////////////////////////////////////////////////////

// GoogleTTSProvider implements sim.TTSProvider using Google Cloud TTS
type GoogleTTSProvider struct {
	httpClient *http.Client
	voicesCh   chan []string
	errCh      chan error
	voices     []sim.Voice
	voicesMu   sync.RWMutex
	lg         *log.Logger
}

func NewGoogleTTSProvider(lg *log.Logger) (sim.TTSProvider, error) {
	creds := os.Getenv("VICE_GCS_CREDENTIALS")
	if creds == "" {
		return nil, ErrMissingTTSCredentials
	}

	// Create JWT config from service account JSON
	config, err := google.JWTConfigFromJSON(
		[]byte(creds),
		"https://www.googleapis.com/auth/cloud-platform",
	)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	g := &GoogleTTSProvider{
		httpClient: oauth2.NewClient(ctx, config.TokenSource(ctx)),
		voicesCh:   make(chan []string),
		errCh:      make(chan error),
		lg:         lg,
	}
	g.httpClient.Timeout = 10 * time.Second

	go func() {
		defer close(g.voicesCh)
		defer close(g.errCh)

		// List available voices
		resp, err := g.httpClient.Get("https://texttospeech.googleapis.com/v1/voices")
		if err != nil {
			g.errCh <- err
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			g.errCh <- fmt.Errorf("TTS ListVoices status: %d", resp.StatusCode)
			return
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			g.errCh <- err
			return
		}

		var voicesResp listVoicesResponse
		if err := json.Unmarshal(body, &voicesResp); err != nil {
			g.errCh <- err
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
		g.voicesCh <- voices
	}()

	return g, nil
}

func (g *GoogleTTSProvider) GetAllVoices() sim.TTSVoicesFuture {
	vch := make(chan []sim.Voice)
	errch := make(chan error)
	fut := sim.TTSVoicesFuture{VoicesCh: vch, ErrCh: errch}

	go func() {
		defer close(vch)
		defer close(errch)

		g.voicesMu.RLock()
		if len(g.voices) > 0 {
			voices := g.voices
			g.voicesMu.RUnlock()
			vch <- voices
			return
		}
		g.voicesMu.RUnlock()

		if g.httpClient == nil || g.voicesCh == nil {
			errch <- ErrTTSUnavailable
			return
		}

		// The RPC to Google TTS for the voices list was made when
		// GoogleTTSProvider was created, with the hope that it would
		// return by the time code elsewhere asked for it.
		var voices []string
		var ok bool
		select {
		case voices, ok = <-g.voicesCh:
			if ok {
				break
			}
			g.voicesCh = nil
		case err := <-g.errCh:
			errch <- fmt.Errorf("list voices: %v", err)
			return
		}

		if len(voices) == 0 {
			errch <- errors.New("Unable to get voices from Google TTS")
			return
		}

		// Convert string slice to Voice slice
		g.voicesMu.Lock()
		g.voices = make([]sim.Voice, len(voices))
		for i, v := range voices {
			g.voices[i] = sim.Voice(v)
		}
		voicesCopy := g.voices
		g.voicesMu.Unlock()

		vch <- voicesCopy
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

		if g.httpClient == nil {
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
				SpeakingRate:    1.75,
				SampleRateHertz: 24000,
			},
		}

		reqBody, err := json.Marshal(req)
		if err != nil {
			errch <- err
			return
		}

		resp, err := g.httpClient.Post(
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
	ClientIP string // Automatically populated by util.LoggingServerCodec
}

// RemoteTTSProvider implements sim.TTSProvider by making RPC calls to a remote server
type RemoteTTSProvider struct {
	client       *rpc.Client
	lg           *log.Logger
	cachedVoices []sim.Voice
}

// NewRemoteTTSProvider creates a new RemoteTTSProvider that connects to the specified server
func NewRemoteTTSProvider(ctx context.Context, serverAddress string, lg *log.Logger) (*RemoteTTSProvider, error) {
	lg.Debugf("%s: connecting for TTS", serverAddress)
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", serverAddress)
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
			if err := r.callWithTimeout(GetAllVoicesRPC, struct{}{}, &voices); err != nil {
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
		if err := r.callWithTimeout(TextToSpeechRPC, &TTSRequest{
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

// SynthesizeSpeechWithTimeout synchronously synthesizes speech with the given timeout.
// Returns a PilotSpeech struct on success, nil on timeout or error.
// This is used for synchronous readback synthesis in RunAircraftCommands.
func SynthesizeSpeechWithTimeout(va *VoiceAssigner, tts sim.TTSProvider, callsign av.ADSBCallsign,
	transmissionType av.RadioTransmissionType, text string, simTime time.Time, timeout time.Duration, lg *log.Logger) *sim.PilotSpeech {
	if tts == nil || text == "" {
		return nil
	}

	// Get voice for this aircraft
	voice, ok := va.GetVoice(callsign)
	if !ok {
		lg.Warnf("No voice available for %s", callsign)
		return nil
	}

	// Start TTS synthesis
	fut := tts.TextToSpeech(voice, text)

	// Wait for result with timeout
	select {
	case mp3, ok := <-fut.Mp3Ch:
		if ok && len(mp3) > 0 {
			return &sim.PilotSpeech{
				Callsign: callsign,
				Type:     transmissionType,
				Text:     text,
				MP3:      mp3,
				SimTime:  simTime,
			}
		}
		return nil
	case err, ok := <-fut.ErrCh:
		if ok {
			lg.Warnf("TTS error for %s %q: %v", callsign, text, err)
		}
		return nil
	case <-time.After(timeout):
		lg.Warnf("TTS timeout for %s %q after %v", callsign, text, timeout)
		return nil
	}
}

// makeTTSProvider creates a TTS provider, preferring Google TTS if credentials
// are available, otherwise falling back to the remote TTS server.
func makeTTSProvider(ctx context.Context, serverAddress string, lg *log.Logger) sim.TTSProvider {
	// Try to create a Google TTS provider first
	p, err := NewGoogleTTSProvider(lg)
	if err == nil {
		lg.Info("Using Google TTS provider")
		return p
	}

	// If Google TTS is not available (no credentials), try to connect to the remote server
	lg.Infof("Google TTS unavailable: %v, attempting to use remote TTS provider at %s", err, serverAddress)
	rp, err := NewRemoteTTSProvider(ctx, serverAddress, lg)
	if err != nil {
		lg.Errorf("Failed to connect to remote TTS provider: %v", err)
		return nil
	}

	lg.Info("Successfully connected to remote TTS provider")
	return rp
}
