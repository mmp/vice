package stt

import (
	"bytes"
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

// OpenAI API types for transcript-to-DSL conversion
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"input"`
}

type openAIResponse struct {
	Output []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

const openAITimeout = 30 * time.Second

// formatApproachesForModel converts approach data into a string format suitable for the model prompt.
// Each approach is formatted as "pronunciation": "code" pairs.
func formatApproachesForModel(approaches [][2]string) string {
	var b strings.Builder
	for i, approach := range approaches {
		if i > 0 {
			b.WriteString(", ")
		}
		pronounce := formatToModel(approach[0])
		// Expand standalone 'l' or 'r' suffix to full words for clarity
		if strings.HasSuffix(pronounce, " l") {
			pronounce = strings.TrimSuffix(pronounce, " l") + " left"
		} else if strings.HasSuffix(pronounce, " r") {
			pronounce = strings.TrimSuffix(pronounce, " r") + " right"
		}
		pronounce = strings.TrimSpace(pronounce)
		fmt.Fprintf(&b, "\"%s\": \"%s\"", pronounce, approach[1])
	}
	return b.String()
}

// transcriptToDSL calls the OpenAI API to convert a transcript into a DSL command.
func transcriptToDSL(model, apiKey string, approaches [][2]string, transcript string) (string, error) {
	req := openAIRequest{
		Model: model,
		Messages: []openAIMessage{
			{Role: "system", Content: "Convert ATC transcripts to DSL; output only the DSL line."},
			{Role: "user", Content: fmt.Sprintf("AllowedApproaches: %s\nTranscript: \"%s\"",
				formatApproachesForModel(approaches), transcript)},
		},
	}

	jsonBytes, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", "https://api.openai.com/v1/responses", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	httpClient := &http.Client{Timeout: openAITimeout}
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

	var parsed openAIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse API response: %w", err)
	}

	if len(parsed.Output) > 0 && len(parsed.Output[0].Content) > 0 {
		return parsed.Output[0].Content[0].Text, nil
	}

	return "", fmt.Errorf("API returned empty response")
}

// callModel converts a transcript to a DSL command using the OpenAI API.
func callModel(model string, approaches [][2]string, transcript string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY environment variable is not set")
	}
	if model == "" {
		return "", fmt.Errorf("OPENAI_MODEL environment variable is not set")
	}
	return transcriptToDSL(model, apiKey, approaches, transcript)
}

// VoiceToCommand transcribes audio and converts it to a command.
// Returns (command, transcription, error).
func VoiceToCommand(audio *AudioData, approaches [][2]string, lg *log.Logger) (string, string, error) {
	text, err := Transcribe(audio)
	if err != nil {
		return "", "", err
	}
	model := os.Getenv("OPENAI_MODEL")
	command, err := callModel(model, approaches, text)
	if err != nil {
		return "", text, err
	}
	return command, text, nil
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
	return formatToModel(text), nil
}

// formatReplacer removes punctuation and replaces numbers with ICAO words
var formatReplacer = strings.NewReplacer(
	// Common punctuation in speech transcription -> empty
	",", "", ".", "", "-", "", ":", "", ";", "", "!", "", "?", "", "'", "", "\"", "",
	// Numbers -> ICAO words (with trailing space for word separation)
	"0", "zero ", "1", "wun ", "2", "too ", "3", "tree ", "4", "fower ",
	"5", "fife ", "6", "six ", "7", "seven ", "8", "ait ", "9", "niner ",
)

// The OpenAI model was fine-tuned to handle numbers as their ICAO words (eg. 1 as wun, 2 as too, etc.)
// This is mainly to bring some normalization to the transcription and between controller phraseology.
// For example, sometimes the transcription will be 3-5-0 rather than 350, or controllers will say "climb and maintain 1-0, ten, thousand"
// formatToModel() removes all punctuation and numbers, and replaces them with their ICAO counterparts.
func formatToModel(text string) string {
	text = formatReplacer.Replace(text)
	text = strings.ReplaceAll(text, "  ", " ") // Remove double spaces
	text = strings.ToLower(text)
	return strings.TrimSpace(text)
}

func ProcessSTTKeyboardInput(p platform.Platform, client *client.ControlClient, lg *log.Logger,
	PTTKey imgui.Key, SelectedMicrophone *string) {
	if client == nil || PTTKey == imgui.KeyNone {
		return
	}

	// Start on initial press (ignore repeats by checking our own flag)
	if imgui.IsKeyDown(PTTKey) && !client.RadioIsActive() {
		if !client.PTTRecording.Load() && !p.IsAudioRecording() {
			if err := p.StartAudioRecordingWithDevice(*SelectedMicrophone); err != nil {
				lg.Errorf("Failed to start audio recording: %v", err)
			} else {
				client.PTTRecording.Store(true)
				lg.Infof("Push-to-talk: Started recording")
			}
		}
	}

	// Independently detect release (do not tie to pressed state; key repeat may keep it true)
	if client.PTTRecording.Load() && !imgui.IsKeyDown(PTTKey) {
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
					audio := &AudioData{SampleRate: platform.AudioSampleRate, Channels: 1, Data: samples}
					command, transcription, err := VoiceToCommand(audio, approaches, lg)
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
