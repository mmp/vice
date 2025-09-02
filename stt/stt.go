package stt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/stars"
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

// Input for the function
type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"input"`
}

// Response format (simplified)
type OpenAIResponse struct {
	Output []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func callModel(model string, approaches [][2]string, transcript string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set")
	}

	// Build the system + user messages
	systemMsg := OpenAIMessage{
		Role:    "system",
		Content: "Convert ATC transcripts to DSL; output only the DSL line.",
	}
	var apprStr string
	for _, approach := range approaches {
		pronounce := formatToModel(approach[0])
		// Replace only standalone 'l' or 'r' at the end (with space before them)
		if strings.HasSuffix(pronounce, " l") {
			pronounce = strings.TrimSuffix(pronounce, " l") + " left"
		} else if strings.HasSuffix(pronounce, " r") {
			pronounce = strings.TrimSuffix(pronounce, " r") + " right"
		} else if strings.HasSuffix(pronounce, " c") {
			pronounce = strings.TrimSuffix(pronounce, " c") + " center"
		}
		pronounce = strings.TrimSpace(pronounce)

		apprStr += fmt.Sprintf("\"%s\": \"%s\", ", pronounce, approach[1])
	}
	apprStr = strings.TrimSuffix(apprStr, ", ")

	userContent := fmt.Sprintf("AllowedApproaches: %s\nTranscript: \"%s\"", apprStr, transcript)
	userMsg := OpenAIMessage{
		Role:    "user",
		Content: userContent,
	}

	reqBody := OpenAIRequest{
		Model:    model,
		Messages: []OpenAIMessage{systemMsg, userMsg},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	// Make request
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/responses", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Parse JSON response
	var parsed OpenAIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse response: %v\nraw: %s", err, string(body))
	}

	// Extract first text output
	if len(parsed.Output) > 0 && len(parsed.Output[0].Content) > 0 {
		return parsed.Output[0].Content[0].Text, nil
	}

	return "", fmt.Errorf("no output found: %s", string(body))
}

func VoiceToCommand(audio *AudioData, approaches [][2]string, lastTranscription *string, lg *log.Logger) (string, error) {
	text, err := Transcribe(audio)
	if err != nil {
		return "", err
	}
	*lastTranscription = text
	model := os.Getenv("OPENAI_MODEL")
	command, err := callModel(model, approaches, text)
	lg.Infof("Command: %s", command)
	if err != nil {
		return "", err
	}
	return command, nil
}

func Transcribe(audio *AudioData) (string, error) {
	// Make a model
	resourcesPath := util.GetResourcesFolderPath()
	modelPath := filepath.Join(resourcesPath, "models", "ggml-medium-q5_0.bin")
	text, err := whisper.Transcribe(modelPath, audio.Data, audio.SampleRate, audio.Channels, whisper.Options{
		Language: "en",
	})
	if err != nil {
		return "", err
	}
	return formatToModel(text), nil
}

// The OpenAI model was fine-tuned to handle numbers as their ICAO words (eg. 1 as wun, 2 as too, etc.)
// This is mainly to bring some normalization to the transcription and between controller phraseology.
// For example, sometimes the transcription will be 3-5-0 rather than 350, or controllers will say "climb and maintain 1-0, ten, thousand"
// formatToModel() removes all punctuation and numbers, and replaces them with their ICAO counterparts.
func formatToModel(text string) string {
	punctuation := []string{",", ".", "-", "–", "—", ":", ";", "/", "\\", "(", ")", "[", "]", "{", "}", "!", "?", "+", "_", "=", "*", "\"", "'", "<", ">", "|"}
	for _, p := range punctuation {
		text = strings.ReplaceAll(text, p, "")
	}
	numberToWord := map[string]string{
		"0": "zero",
		"1": "wun",
		"2": "too",
		"3": "tree",
		"4": "fower",
		"5": "fife",
		"6": "six",
		"7": "seven",
		"8": "ait",
		"9": "niner",
	}
	for number, word := range numberToWord {
		text = strings.ReplaceAll(text, number, word+" ")
	}
	text = strings.ReplaceAll(text, "  ", " ") // Remove double spaces
	text = strings.ToLower(text)
	return text
}

func ProcessSTTKeyboardInput(p platform.Platform, client *client.ControlClient, lg *log.Logger,
	PTTKey imgui.Key, SelectedMicrophone *string) {
	if PTTKey != imgui.KeyNone {
		// Start on initial press (ignore repeats by checking our own flag)
		if imgui.IsKeyDown(PTTKey) && !client.RadioIsActive() {
			if !client.PTTRecording && !p.IsAudioRecording() {
				if err := p.StartAudioRecordingWithDevice(*SelectedMicrophone); err != nil {
					lg.Errorf("Failed to start audio recording: %v", err)
				} else {
					client.PTTRecording = true
					lg.Infof("Push-to-talk: Started recording")
				}
			}
		} else if client.RadioIsActive() {
			// TODO: think of something to do (ie. a sound effect, the pilot readback gets cut off, etc.)
		}

		// Independently detect release (do not tie to pressed state; key repeat may keep it true)
		if client.PTTRecording && !imgui.IsKeyDown(PTTKey) {
			if p.IsAudioRecording() {
				data, err := p.StopAudioRecording()
				client.PTTRecording = false
				if err != nil {
					lg.Errorf("Failed to stop audio recording: %v", err)
				} else {
					lg.Infof("Push-to-talk: Stopped recording, transcribing...")
					go func(samples []int16) {
						// Make approach map
						approaches := [][2]string{} // Type (eg. ILS) and runway (eg. 28R)
						for _, apt := range client.State.Airports {
							for code, appr := range apt.Approaches {
								approaches = append(approaches, [2]string{appr.FullName, code})
							}
						}
						audio := &AudioData{SampleRate: platform.AudioSampleRate, Channels: 1, Data: samples}
						text, err := VoiceToCommand(audio, approaches, &client.LastTranscription, lg)
						if err != nil {
							lg.Errorf("Push-to-talk: Transcription error: %v\n", err)
							return
						}
						fields := strings.Fields(text)

						trimFunc := func(s string) string {
							for i, char := range s {
								if unicode.IsDigit(char) {
									return s[i:]
								}
							}
							return ""
						}
						if len(fields) > 0 {
							callsign := fields[0]
							// Check if callsign matches, if not check if the numbers match
							_, ok := client.State.GetTrackByCallsign(av.ADSBCallsign(callsign))
							if !ok {
								// trim until first number
								callsign = trimFunc(callsign)
								matching := stars.TracksFromACIDSuffix(client, callsign)
								if len(matching) == 1 {
									callsign = string(matching[0].ADSBCallsign)
								}
							}
							if len(fields) > 1 && callsign != "" {
								cmd := strings.Join(fields[1:], " ")
								client.RunAircraftCommands(av.ADSBCallsign(callsign), cmd,
									nil)
								lg.Infof("Command: %v Callsign: %v", cmd, callsign)
								client.LastCommand = callsign + " " + cmd
							}
							client.PTTRecording = false
							lg.Infof("Push-to-talk: Transcription: %s\n", text)

						}

					}(data)
				}
			} else {
				// Platform already not recording; reset our flag
				client.PTTRecording = false
				return
			}
		}
	}
}
