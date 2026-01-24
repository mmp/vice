// cmd/updatesay/claude.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
)

const (
	claudeAPIURL = "https://api.anthropic.com/v1/messages"
	claudeModel  = "claude-sonnet-4-20250514"
	chunkSize    = 100
)

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []claudeContentBlock `json:"content"`
	Error   *claudeError         `json:"error,omitempty"`
}

type claudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// QueryPronunciations queries Claude API for pronunciations of missing items.
// This version doesn't save incrementally - use QueryAndSave for that.
func QueryPronunciations(apiKey string, missing []string, itemType string) (map[string]string, error) {
	result := make(map[string]string)

	// Process in chunks to avoid token limits
	for i := 0; i < len(missing); i += chunkSize {
		end := min(i+chunkSize, len(missing))
		chunk := missing[i:end]

		fmt.Printf("  Processing chunk %d-%d of %d...\n", i+1, end, len(missing))

		pronunciations, err := queryChunk(apiKey, chunk, itemType)
		if err != nil {
			fmt.Printf("  Warning: chunk failed: %v\n", err)
			continue
		}

		maps.Copy(result, pronunciations)
	}

	return result, nil
}

// QueryAndSave queries Claude for pronunciations and saves after each chunk.
// This ensures progress is preserved if interrupted. Returns the updated data and count of new entries.
func QueryAndSave(apiKey string, missing []string, itemType string, existing map[string]string, filePath string) (map[string]string, int) {
	totalAdded := 0

	// Process in chunks to avoid token limits
	for i := 0; i < len(missing); i += chunkSize {
		end := min(i+chunkSize, len(missing))
		chunk := missing[i:end]

		fmt.Printf("  Processing chunk %d-%d of %d...\n", i+1, end, len(missing))

		pronunciations, err := queryChunk(apiKey, chunk, itemType)
		if err != nil {
			fmt.Printf("  Warning: chunk failed: %v\n", err)
			continue
		}

		// Merge and save after each successful chunk
		merged, count := MergeSayData(existing, pronunciations)
		existing = merged
		totalAdded += count

		if err := SaveSayFile(filePath, existing); err != nil {
			fmt.Printf("  Warning: failed to save %s: %v\n", filePath, err)
		} else {
			fmt.Printf("  Saved %d new pronunciations (total so far: %d)\n", count, totalAdded)
		}
	}

	return existing, totalAdded
}

func queryChunk(apiKey string, codes []string, itemType string) (map[string]string, error) {
	prompt := buildPrompt(codes, itemType)

	reqBody := claudeRequest{
		Model:     claudeModel,
		MaxTokens: 4096,
		Messages: []claudeMessage{
			{Role: "user", Content: prompt},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", claudeAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var claudeResp claudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if claudeResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", claudeResp.Error.Message)
	}

	if len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from API")
	}

	// Extract text from response
	text := claudeResp.Content[0].Text

	// Parse JSON from response (strip markdown code fences if present)
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var result map[string]string
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON from response: %w\nResponse text: %s", err, text)
	}

	return result, nil
}

func buildPrompt(codes []string, itemType string) string {
	codesStr := strings.Join(codes, ", ")
	return fmt.Sprintf(`Here is a list of %s. For each one, give one or two English words that give a reasonable pronunciation. For example, for "MMILE" you might respond "Emily". Follow these rules:
1. Each should be spoken as one or at most two words/syllables
2. If there are English words similar to the name, use them
3. Otherwise make up a word that fits, possibly adding vowels and removing repeated letters
4. Names are acceptable (e.g., CHELE -> Shelley, CARYN -> Karen)
5. Familiar non-English words that are pronounceable are ok (e.g., COPKO -> Copko)

Return ONLY valid JSON with no markdown, mapping each code to its pronunciation:
{"CODE1": "pronunciation1", "CODE2": "pronunciation2", ...}

Codes to process:
%s`, itemType, codesStr)
}

// QuerySampleChunk queries Claude for a single chunk of items (for testing/evaluation).
func QuerySampleChunk(apiKey string, codes []string, itemType string) (map[string]string, error) {
	return queryChunk(apiKey, codes, itemType)
}

// EstimateCost estimates the API cost for querying pronunciations.
func EstimateCost(missingFixes, missingSIDs, missingSTARs []string) {
	// Prompt template is ~300 tokens
	// Each code is ~2-3 tokens
	// Response is ~2-3 tokens per code (JSON key + value)

	total := len(missingFixes) + len(missingSIDs) + len(missingSTARs)
	if total == 0 {
		fmt.Println("No missing items to process.")
		return
	}

	chunks := (total + chunkSize - 1) / chunkSize

	inputTokens := chunks*300 + total*3 // prompt + codes
	outputTokens := total * 5           // JSON output

	// Claude Sonnet pricing: $3/MTok input, $15/MTok output
	inputCost := float64(inputTokens) / 1_000_000 * 3.0
	outputCost := float64(outputTokens) / 1_000_000 * 15.0
	totalCost := inputCost + outputCost

	fmt.Printf("\nEstimated cost: $%.4f (%d input tokens, %d output tokens)\n",
		totalCost, inputTokens, outputTokens)
}

// Opus-specific constants for 3-letter procedure lookups
const (
	opusModel     = "claude-opus-4-20250514"
	opusChunkSize = 5 // Small chunks since Opus needs to do web searches
)

// QueryOpusAndSave queries Opus for 3-letter procedure names with web search.
// Saves after each chunk. Returns the updated data and count of new entries.
func QueryOpusAndSave(apiKey string, procedures map[string]*ProcedureInfo, procedureType string, existing map[string]string, filePath string) (map[string]string, int) {
	totalAdded := 0

	// Get sorted list of base names
	baseNames := make([]string, 0, len(procedures))
	for name := range procedures {
		baseNames = append(baseNames, name)
	}

	// Process in small chunks (5 at a time for web search)
	for i := 0; i < len(baseNames); i += opusChunkSize {
		end := min(i+opusChunkSize, len(baseNames))
		chunkNames := baseNames[i:end]

		// Build chunk map with procedure info
		chunk := make(map[string]*ProcedureInfo)
		for _, name := range chunkNames {
			chunk[name] = procedures[name]
		}

		fmt.Printf("  Processing Opus chunk %d-%d of %d...\n", i+1, end, len(baseNames))

		results, err := queryOpusChunk(apiKey, chunk, procedureType)
		if err != nil {
			fmt.Printf("  Warning: Opus chunk failed: %v\n", err)
			continue
		}

		// Merge and save after each successful chunk
		merged, count := MergeSayData(existing, results)
		existing = merged
		totalAdded += count

		if err := SaveSayFile(filePath, existing); err != nil {
			fmt.Printf("  Warning: failed to save %s: %v\n", filePath, err)
		} else {
			fmt.Printf("  Saved %d new names (total so far: %d)\n", count, totalAdded)
		}
	}

	return existing, totalAdded
}

// QueryOpusSample queries Opus for a sample of 3-letter procedures (for testing).
func QueryOpusSample(apiKey string, procedures map[string]*ProcedureInfo, procedureType string) (map[string]string, error) {
	// Take up to 5 items for sample
	sample := make(map[string]*ProcedureInfo)
	count := 0
	for name, info := range procedures {
		if count >= opusChunkSize {
			break
		}
		sample[name] = info
		count++
	}

	return queryOpusChunk(apiKey, sample, procedureType)
}

func queryOpusChunk(apiKey string, procedures map[string]*ProcedureInfo, procedureType string) (map[string]string, error) {
	prompt := buildOpusPrompt(procedures, procedureType)

	reqBody := claudeRequest{
		Model:     opusModel,
		MaxTokens: 4096,
		Messages: []claudeMessage{
			{Role: "user", Content: prompt},
			{Role: "assistant", Content: "{"}, // Prefill to force JSON output
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", claudeAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var claudeResp claudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if claudeResp.Error != nil {
		return nil, fmt.Errorf("API error: %s", claudeResp.Error.Message)
	}

	if len(claudeResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from API")
	}

	// Extract text from response - prepend { since we used assistant prefill
	text := "{" + claudeResp.Content[0].Text

	// Parse JSON from response (strip markdown code fences if present)
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var result map[string]string
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON from response: %w\nResponse text: %s", err, text)
	}

	return result, nil
}

func buildOpusPrompt(procedures map[string]*ProcedureInfo, procedureType string) string {
	var sb strings.Builder

	if procedureType == "STAR" {
		sb.WriteString("Here are the names of several STARs (Standard Terminal Arrival Routes) and their associated airports. ")
		sb.WriteString("Use web searches as needed to determine their full names. ")
		sb.WriteString("For example, GEP1 at KMSP corresponds to the \"Gopher One\" arrival, so the answer would be \"Gopher\".\n\n")
	} else {
		sb.WriteString("Here are the names of several SIDs (Standard Instrument Departures) and their associated airports. ")
		sb.WriteString("Use web searches as needed to determine their full names. ")
		sb.WriteString("For example, if TEX5 at KDFW is the \"Texarkana Five\" departure, the answer would be \"Texarkana\".\n\n")
	}

	sb.WriteString("Procedures to look up:\n")
	for baseName, info := range procedures {
		// Get example full names with numbers
		fullNames := make([]string, 0, len(info.FullNames))
		for fn := range info.FullNames {
			fullNames = append(fullNames, fn)
		}
		// Get airports
		airports := make([]string, 0, len(info.Airports))
		for ap := range info.Airports {
			airports = append(airports, ap)
		}
		sb.WriteString(fmt.Sprintf("- %s (variants: %s) at %s\n",
			baseName, strings.Join(fullNames, ", "), strings.Join(airports, ", ")))
	}

	sb.WriteString("\nReturn ONLY valid JSON mapping each base identifier (without the number) to its name (also without the number):\n")
	sb.WriteString("{\"ABC\": \"Name\", \"DEF\": \"Other Name\", ...}\n")

	return sb.String()
}
