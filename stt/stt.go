package stt

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	whisper "github.com/mmp/vice/autowhisper"
)

// AudioData represents audio data in memory
type AudioData struct {
	SampleRate int
	Channels   int
	Data       []int16 // PCM audio data
}

//go:embed systemPrompt.md
var systemPrompt string

// transcriptToDSL calls the OpenAI API to convert a transcript into a DSL command.
func transcriptToDSL(approaches [][2]string, callsigns []av.ADSBCallsign, transcript string) (string, error) {
	type claudeMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type claudeRequest struct {
		Model     string          `json:"model"`
		MaxTokens int             `json:"max_tokens"`
		System    string          `json:"system"`
		Messages  []claudeMessage `json:"messages"`
	}
	type claudeResponse struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}

	type userQuery struct {
		Callsigns  []av.ADSBCallsign `json:"callsigns,omitempty"`
		Airlines   map[string]string `json:"airlines,omitempty"`
		Approaches [][2]string       `json:"approaches,omitempty"`
		Transcript string            `json:"transcript"`
	}
	uq := userQuery{
		Callsigns:  callsigns,
		Airlines:   make(map[string]string),
		Approaches: approaches,
		Transcript: transcript,
	}
	for _, cs := range callsigns {
		for i := range cs {
			if cs[i] >= '0' && cs[i] <= '9' {
				// Hit the first number
				if tele, ok := av.DB.Callsigns[string(cs[:i])]; ok {
					uq.Airlines[tele] = string(cs[:i])
				}
				break
			}
		}
	}
	queryBytes, err := json.Marshal(uq)
	if err != nil {
		return "", err
	}

	req := claudeRequest{
		Model:     "claude-haiku-4-5",
		MaxTokens: 16,
		System:    systemPrompt,
		Messages: []claudeMessage{
			{Role: "user", Content: string(queryBytes)},
		},
	}

	jsonBytes, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}
	fmt.Println(string(jsonBytes))

	apiKey := os.Getenv("VICE_ANTHROPIC_KEY")
	if apiKey == "" {
		panic("must set VICE_ANTHROPIC_KEY")
	}

	httpReq, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var parsed claudeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse API response: %w", err)
	}

	if len(parsed.Content) > 0 {
		fmt.Println("  -----> " + parsed.Content[0].Text)
		return parsed.Content[0].Text, nil
	}

	return "", fmt.Errorf("API returned empty response")
}

// VoiceToCommand transcribes audio and converts it to a command.
// Returns (command, transcription, error).
func VoiceToCommand(audio *AudioData, approaches [][2]string, callsigns []av.ADSBCallsign, lg *log.Logger) (string, string, error) {
	if text, err := Transcribe(audio); err != nil {
		return "", "", err
	} else if command, err := transcriptToDSL(approaches, callsigns, text); err != nil {
		return "", text, err
	} else {
		return command, text, nil
	}
}

var whisperModel *whisper.Model
var whisperModelErr error
var whisperModelOnce sync.Once

func Transcribe(audio *AudioData) (string, error) {
	whisperModelOnce.Do(func() {
		data := util.LoadResourceBytes("models/ggml-medium-q5_0.bin")
		whisperModel, whisperModelErr = whisper.LoadModelFromBytes(data)
	})
	if whisperModelErr != nil {
		return "", whisperModelErr
	}
	text, err := whisper.TranscribeWithModel(whisperModel, audio.Data, audio.SampleRate, audio.Channels, whisper.Options{
		Language: "en",
	})
	if err != nil {
		return "", err
	}
	return text, nil
}

func ProcessSTTKeyboardInput(p platform.Platform, client *client.ControlClient, lg *log.Logger, pttKey imgui.Key, selectedMicrophone string) {
	if client == nil || pttKey == imgui.KeyNone {
		return
	}

	// Start on initial press (ignore repeats by checking our own flag)
	if imgui.IsKeyDown(pttKey) && !client.RadioIsActive() {
		if !client.PTTRecording.Load() && !p.IsAudioRecording() {
			if err := p.StartAudioRecordingWithDevice(selectedMicrophone); err != nil {
				lg.Errorf("Failed to start audio recording: %v", err)
			} else {
				client.PTTRecording.Store(true)
				lg.Infof("Push-to-talk: Started recording")
			}
		}
	}

	// Independently detect release (do not tie to pressed state; key repeat may keep it true)
	if client.PTTRecording.Load() && !imgui.IsKeyDown(pttKey) {
		if p.IsAudioRecording() {
			data, err := p.StopAudioRecording()
			client.PTTRecording.Store(false)
			if err != nil {
				lg.Errorf("Failed to stop audio recording: %v", err)
			} else {
				lg.Infof("Push-to-talk: Stopped recording, transcribing...")
				go func(samples []int16) {
					defer lg.CatchAndReportCrash()

					// Make approach map
					approaches := [][2]string{} // Type (eg. ILS) and runway (eg. 28R)
					for _, apt := range client.State.Airports {
						for code, appr := range apt.Approaches {
							approaches = append(approaches, [2]string{appr.FullName, code})
						}
					}
					audio := &AudioData{SampleRate: platform.AudioInputSampleRate, Channels: 1, Data: samples}
					ourTracks := util.FilterSeq2(maps.All(client.State.Tracks), func(cs av.ADSBCallsign, trk *sim.Track) bool {
						if trk.IsAssociated() {
							return client.State.TCWControlsPosition(client.State.UserTCW, trk.FlightPlan.ControllingController)
						} else {
							// TODO: also include VFRs who have called in
							return false
						}
					})
					callsigns := slices.Collect(util.Seq2Keys(ourTracks))
					command, transcription, err := VoiceToCommand(audio, approaches, callsigns, lg)
					client.SetLastTranscription(transcription)
					if err != nil {
						lg.Errorf("Push-to-talk: Transcription error: %v", err)
						return
					}
					fields := strings.Fields(command)

					if len(fields) > 0 {
						callsign := fields[0]
						// Check if callsign matches, if not check if the numbers match
						_, ok := client.State.GetTrackByCallsign(av.ADSBCallsign(callsign))
						if !ok {
							// Trim until first digit for suffix matching
							callsign = strings.TrimLeftFunc(callsign, func(r rune) bool { return !unicode.IsDigit(r) })
							// Only try suffix matching if we have something to match
							if callsign != "" {
								matching := tracksFromSuffix(client.State.Tracks, callsign)
								if len(matching) == 1 {
									callsign = string(matching[0].ADSBCallsign)
								}
							}
						}
						if len(fields) > 1 && callsign != "" {
							cmd := strings.Join(fields[1:], " ")
							client.RunAircraftCommands(av.ADSBCallsign(callsign), cmd, false, false, nil)
							lg.Infof("STT command: %s %s", callsign, cmd)
							client.SetLastCommand(callsign + " " + cmd)
						}
					}
				}(data)
			}
		} else {
			// Platform already not recording; reset our flag
			client.PTTRecording.Store(false)
		}
	}
}

// tracksFromSuffix returns tracks whose callsign ends with the given suffix.
func tracksFromSuffix(tracks map[av.ADSBCallsign]*sim.Track, suffix string) []*sim.Track {
	match := func(trk *sim.Track) bool {
		if trk.IsUnassociated() {
			return strings.HasSuffix(string(trk.ADSBCallsign), suffix)
		}
		return strings.HasSuffix(string(trk.FlightPlan.ACID), suffix)
	}
	return slices.Collect(util.FilterSeq(maps.Values(tracks), match))
}
