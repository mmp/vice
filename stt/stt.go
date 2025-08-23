package stt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	whisper "github.com/checkandmate1/whisper/pkg"
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

func CallModel(model string, approaches map[string]string, transcript string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set")
	}

	// Build the system + user messages
	systemMsg := OpenAIMessage{
		Role:    "system",
		Content: "Convert ATC transcripts to DSL; output only the DSL line.",
	}

	// userContent := fmt.Sprintf("AllowedApproaches: %v\nTranscript: \"%s\"", approaches, transcript)
	userContent := fmt.Sprintf("Transcript: \"%s\"", transcript)
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

func VoiceToCommand(audio *AudioData, approaches map[string]string) (string, error) {
	text, err := Transcribe2(audio)
	if err != nil {
		return "", err
	}
	fmt.Println("Transcription: ", text)
	model := os.Getenv("OPENAI_MODEL")
	command, err := CallModel(model, approaches, text)
	fmt.Println("Command: ", command)
	if err != nil {
		return "", err
	}
	return command, nil
}

func Transcribe2(audio *AudioData) (string, error) {
	// Make a model 

	model, err := whisper.NewTemporaryTranscriber("whisper-cli", "../../resources/models/ggml-tiny.bin", true)
	if err != nil {
		return "", err
	}

	text, err := model.TranscribeSamples(audio.SampleRate, audio.Channels, audio.Data)
	if err != nil {
		return "", err
	}
	return processTranscription(text), nil
}


// The OpenAI model was fine-tuned to handle numbers as their ICAO words (eg. 1 as wun, 2 as too, etc.)
// This is mainly to bring some normalization to the transcription and between controller phraseology. 
// For example, sometimes the transcription will be 3-5-0 rather than 350, or controllers will say "climb and maintain 1-0, ten, thousand"
// processTranscription() removes all punctuation and numbers, and replaces them with their ICAO counterparts.
func processTranscription(text string) string {
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
	return text
}