// server/stt.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
)

// STTTranscriptProvider decodes speech-to-text transcripts into aircraft commands.
type STTTranscriptProvider interface {
	// DecodeTranscript converts an STT transcript to aircraft commands.
	// Returns the full command string (e.g., "UAL123 H250 C120") or error.
	DecodeTranscript(qc STTQueryContext) (string, error)
}

func MakeSTTProvider(ctx context.Context, serverAddress string, lg *log.Logger) STTTranscriptProvider {
	// Try Anthropic first (direct API access)
	if p, err := NewAnthropicSTTProvider(lg); err == nil {
		fmt.Println("local")
		return p
	}

	// Fall back to remote proxy
	lg.Info("Anthropic API key not available, attempting remote STT provider")
	if p, err := NewRemoteSTTProvider(ctx, serverAddress, lg); err == nil {
		fmt.Println("server")
		return p
	}

	lg.Warn("STT provider unavailable: no API key and unable to connect to remote server")
	return nil
}

///////////////////////////////////////////////////////////////////////////
// AnthropicSTTProvider - Direct Anthropic API calls

type AnthropicSTTProvider struct {
	apiKey string
	lg     *log.Logger
}

func NewAnthropicSTTProvider(lg *log.Logger) (*AnthropicSTTProvider, error) {
	apiKey := os.Getenv("VICE_ANTHROPIC_KEY")
	if apiKey == "" {
		return nil, ErrMissingAnthropicKey
	}
	lg.Info("Using Anthropic STT provider (direct API access)")
	return &AnthropicSTTProvider{apiKey: apiKey, lg: lg}, nil
}

func (p *AnthropicSTTProvider) DecodeTranscript(qc STTQueryContext) (string, error) {
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

	queryBytes, err := json.Marshal(qc)
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

	start := time.Now()

	httpReq, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
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

	fmt.Printf("claude in %s\n", time.Since(start))

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

///////////////////////////////////////////////////////////////////////////
// RemoteSTTProvider - Proxies to public server via RPC

type RemoteSTTProvider struct {
	client *rpc.Client
	lg     *log.Logger
}

func NewRemoteSTTProvider(ctx context.Context, serverAddress string, lg *log.Logger) (*RemoteSTTProvider, error) {
	lg.Debugf("%s: connecting for STT", serverAddress)
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", serverAddress)
	if err != nil {
		lg.Warnf("%s: unable to connect for STT: %v", serverAddress, err)
		return nil, fmt.Errorf("unable to connect to STT server: %w", err)
	}
	lg.Debugf("%s: connected for STT in %s", serverAddress, time.Since(start))

	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		return nil, err
	}

	codec := util.MakeMessagepackClientCodec(cc)
	codec = util.MakeLoggingClientCodec(serverAddress, codec, lg)
	client := rpc.NewClientWithCodec(codec)

	lg.Info("Using remote STT provider (via public server)")
	return &RemoteSTTProvider{
		client: client,
		lg:     lg,
	}, nil
}

func (r *RemoteSTTProvider) callWithTimeout(serviceMethod string, args any, reply any) error {
	call := r.client.Go(serviceMethod, args, reply, nil)

	for {
		select {
		case <-call.Done:
			return call.Error
		case <-time.After(30 * time.Second):
			// Use longer timeout for STT since LLM calls can take time
			if !util.DebuggerIsRunning() {
				return fmt.Errorf("%s: RPC timeout", serviceMethod)
			}
		}
	}
}

func (r *RemoteSTTProvider) DecodeTranscript(qc STTQueryContext) (string, error) {
	var result string
	if err := r.callWithTimeout(DecodeSTTTranscriptRPC, qc, &result); err != nil {
		return "", err
	}
	return result, nil
}
