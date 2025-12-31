// server/stt.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

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
	"time"
	"unicode"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

//go:embed sttSystemPrompt.md
var systemPrompt string

// transcriptToDSL calls the Anthropic API to convert a transcript into a DSL command.
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

// processSTTTranscript processes a transcript from the client using server-side data.
// It converts the transcript to DSL, matches the callsign, and executes the command.
// Returns (callsign, command, error).
func processSTTTranscript(c *controllerContext, transcript string) (string, string, error) {
	// Get approaches from server-side airport data
	approaches := [][2]string{}
	for _, apt := range c.sim.State.Airports {
		for code, appr := range apt.Approaches {
			approaches = append(approaches, [2]string{appr.FullName, code})
		}
	}

	// Get tracks from server-side derived state
	stateUpdate := c.sim.GetStateUpdate()
	tracks := stateUpdate.DerivedState.Tracks

	// Filter tracks to only those controlled by this controller
	ourTracks := util.FilterSeq2(maps.All(tracks), func(cs av.ADSBCallsign, trk *sim.Track) bool {
		if trk.IsAssociated() {
			return c.sim.TCWControlsPosition(c.tcw, trk.FlightPlan.ControllingController)
		}
		return false
	})
	callsigns := slices.Collect(util.Seq2Keys(ourTracks))

	// Convert transcript to DSL command
	command, err := transcriptToDSL(approaches, callsigns, transcript)
	if err != nil {
		return "", "", err
	}

	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", "", nil
	}

	callsign := fields[0]
	// Check if callsign matches directly in tracks
	_, ok := tracks[av.ADSBCallsign(callsign)]
	if !ok {
		// Trim until first digit for suffix matching
		suffix := strings.TrimLeftFunc(callsign, func(r rune) bool { return !unicode.IsDigit(r) })
		if suffix != "" {
			matching := tracksFromSuffix(tracks, suffix)
			if len(matching) == 1 {
				callsign = string(matching[0].ADSBCallsign)
			}
		}
	}

	if len(fields) > 1 && callsign != "" {
		cmd := strings.Join(fields[1:], " ")
		// Execute the command on the server
		execResult := c.sim.RunAircraftControlCommands(c.tcw, av.ADSBCallsign(callsign), cmd)
		if execResult.Error != nil {
			return callsign, cmd, execResult.Error
		}
		return callsign, cmd, nil
	}

	return "", "", nil
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
