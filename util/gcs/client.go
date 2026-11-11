// util/gcs/client.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package gcs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Client is a read-only client for a Google Cloud Storage bucket.
type Client struct {
	httpClient *http.Client
	bucket     string
	ctx        context.Context
}

// Config holds configuration options for creating a GCS client
type Config struct {
	Context     context.Context // Optional: defaults to context.Background()
	Credentials []byte          // Optional: service account JSON; if nil, creates unauthenticated client
	Timeout     time.Duration   // Optional: HTTP client timeout; defaults to 30 seconds
}

// MakeClient creates a GCS client with the given bucket and configuration.
// If Credentials is nil, creates an unauthenticated client.
// If Context is nil, uses context.Background().
// If Timeout is zero, uses 30 seconds default.
func MakeClient(bucket string, config Config) (*Client, error) {
	if bucket == "" {
		return nil, fmt.Errorf("bucket name cannot be empty")
	}

	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}

	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	baseTransport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}

	// Create unauthenticated client if no credentials provided
	if config.Credentials == nil {
		return &Client{
			httpClient: &http.Client{Transport: baseTransport},
			bucket:     bucket,
			ctx:        ctx,
		}, nil
	}

	// Create authenticated client
	jwtConfig, err := google.JWTConfigFromJSON(
		config.Credentials,
		"https://www.googleapis.com/auth/devstorage.read_only",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT config: %w", err)
	}

	// Use a bounded client for token acquisition. The GCS data client
	// deliberately does not set http.Client.Timeout, so large file downloads
	// can continue after the response headers arrive.
	tokenClient := &http.Client{Transport: baseTransport, Timeout: timeout}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, tokenClient)
	httpClient := &http.Client{
		Transport: &oauth2.Transport{
			Source: jwtConfig.TokenSource(ctx),
			Base:   baseTransport,
		},
	}

	return &Client{
		httpClient: httpClient,
		bucket:     bucket,
		ctx:        ctx,
	}, nil
}

// Object represents the minimal object metadata we need from the GCS API response
type Object struct {
	Name string `json:"name"`
	Size string `json:"size"`
}

// ListResponse represents the JSON response from the GCS objects list API
type ListResponse struct {
	Items         []Object `json:"items"`
	NextPageToken string   `json:"nextPageToken"`
}

// List returns a map of object names to their sizes from the Google Cloud Storage bucket.
// It uses the GCS JSON API v1. If prefix is non-empty, only objects with that prefix are returned.
func (g *Client) List(prefix string) (map[string]int64, error) {
	allObjects := make(map[string]int64)
	nextPageToken := ""

	for {
		apiURL := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o?projection=noAcl", g.bucket)
		if prefix != "" {
			apiURL += "&prefix=" + url.QueryEscape(prefix)
		}
		if nextPageToken != "" {
			apiURL += "&pageToken=" + url.QueryEscape(nextPageToken)
		}

		req, err := http.NewRequestWithContext(g.ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		resp, err := g.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to make request: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("GCS API returned status %d for bucket %s", resp.StatusCode, g.bucket)
		}

		var listResp ListResponse
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		resp.Body.Close()

		for _, obj := range listResp.Items {
			size, err := strconv.ParseInt(obj.Size, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse size for object %s: %w", obj.Name, err)
			}
			allObjects[obj.Name] = size
		}

		// Check if there are more pages
		if listResp.NextPageToken == "" {
			break
		}
		nextPageToken = listResp.NextPageToken
	}

	return allObjects, nil
}

// GetReader returns a ReadCloser for downloading an object from the Google Cloud Storage bucket.
// The caller is responsible for closing the returned ReadCloser.
func (g *Client) GetReader(objectName string) (io.ReadCloser, error) {
	if objectName == "" {
		return nil, fmt.Errorf("object name cannot be empty")
	}

	apiURL := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s?alt=media",
		g.bucket, url.QueryEscape(objectName))

	req, err := http.NewRequestWithContext(g.ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to download %s from bucket %s: status %d", objectName, g.bucket, resp.StatusCode)
	}

	return resp.Body, nil
}
