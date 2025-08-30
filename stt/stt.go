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

	whisper "github.com/mmp/vice/autowhisper"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
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
		}
		pronounce = strings.TrimSpace(pronounce)

		apprStr += fmt.Sprintf("\"%s\": \"%s\", ", pronounce, approach[1])
	}
	apprStr = strings.TrimSuffix(apprStr, ", ")
	fmt.Println(apprStr)

	userContent := fmt.Sprintf("AllowedApproaches: %s\nTranscript: \"%s\"", apprStr, transcript)
	fmt.Println("User content: ", userContent)
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

func VoiceToCommand(audio *AudioData, approaches [][2]string, lg *log.Logger) (string, error) {
	text, err := Transcribe(audio)
	if err != nil {
		return "", err
	}
	fmt.Println("Transcription: ", text)
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
	fmt.Println("Transcription (no formatting): ", text)
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
