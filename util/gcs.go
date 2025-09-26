// util/gcs.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type GCSClient struct {
	httpClient          *http.Client
	bucket              string
	serviceAccountEmail string
	privateKey          *rsa.PrivateKey
	ctx                 context.Context
}

// GCSClientConfig holds configuration options for creating a GCS client
type GCSClientConfig struct {
	Context     context.Context // Optional: defaults to context.Background()
	Credentials []byte          // Optional: service account JSON; if nil, creates unauthenticated client
	Timeout     time.Duration   // Optional: HTTP client timeout; defaults to 30 seconds
}

// MakeGCSClient creates a GCS client with the given bucket and configuration.
// If Credentials is nil, creates an unauthenticated client.
// If Context is nil, uses context.Background().
// If Timeout is zero, uses 30 seconds default.
func MakeGCSClient(bucket string, config GCSClientConfig) (*GCSClient, error) {
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

	// Create unauthenticated client if no credentials provided
	if config.Credentials == nil {
		return &GCSClient{
			httpClient: &http.Client{
				Timeout: timeout,
			},
			bucket: bucket,
			ctx:    ctx,
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

	// Extract service account email from the JWT config
	serviceAccountEmail := jwtConfig.Email

	// Parse the private key from the JWT config
	var rsaKey *rsa.PrivateKey
	if len(jwtConfig.PrivateKey) > 0 {
		block, _ := pem.Decode(jwtConfig.PrivateKey)
		if block != nil {
			parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				// Try PKCS1 format as fallback
				if rsaKeyPKCS1, err2 := x509.ParsePKCS1PrivateKey(block.Bytes); err2 == nil {
					parsedKey = rsaKeyPKCS1
				} else {
					return nil, fmt.Errorf("failed to parse private key: %w (PKCS8: %v)", err2, err)
				}
			}

			if parsedRSAKey, ok := parsedKey.(*rsa.PrivateKey); ok {
				rsaKey = parsedRSAKey
			}
		}
	}

	httpClient := oauth2.NewClient(ctx, jwtConfig.TokenSource(ctx))
	httpClient.Timeout = timeout

	return &GCSClient{
		httpClient:          httpClient,
		bucket:              bucket,
		serviceAccountEmail: serviceAccountEmail,
		privateKey:          rsaKey,
		ctx:                 ctx,
	}, nil
}

// GCSObject represents the minimal object metadata we need from the GCS API response
type GCSObject struct {
	Name string `json:"name"`
	Size string `json:"size"`
}

// GCSListResponse represents the JSON response from the GCS objects list API
type GCSListResponse struct {
	Items         []GCSObject `json:"items"`
	NextPageToken string      `json:"nextPageToken"`
}

// List returns a map of object names to their sizes from the Google Cloud Storage bucket.
// It uses the GCS JSON API v1. If prefix is non-empty, only objects with that prefix are returned.
func (g *GCSClient) List(prefix string) (map[string]int64, error) {
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

		var listResp GCSListResponse
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
func (g *GCSClient) GetReader(objectName string) (io.ReadCloser, error) {
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

// GetURL returns a signed URL for downloading an object from the bucket.
// The lifetime parameter specifies how long the URL should remain valid.
// For unauthenticated clients, it returns a public URL (lifetime is ignored).
// For authenticated clients, it returns a V4 signed URL that expires after the specified duration.
func (g *GCSClient) GetURL(objectName string, lifetime time.Duration) (string, error) {
	if objectName == "" {
		return "", fmt.Errorf("object name cannot be empty")
	}
	if g.privateKey == nil {
		return "", fmt.Errorf("no private key is available")
	}

	// V4 signing has a maximum lifetime of 7 days
	maxLifetime := 7 * 24 * time.Hour
	if lifetime > maxLifetime {
		lifetime = maxLifetime
	}

	// Generate V4 signed URL
	now := time.Now().UTC()
	datestamp := now.Format("20060102")
	timestamp := now.Format("20060102T150405Z")
	expires := int64(lifetime.Seconds())

	credentialScope := fmt.Sprintf("%s/auto/storage/goog4_request", datestamp)

	canonicalURI := fmt.Sprintf("/%s/%s", g.bucket, url.PathEscape(objectName))

	// Query parameters for V4
	queryParams := url.Values{}
	queryParams.Set("X-Goog-Algorithm", "GOOG4-RSA-SHA256")
	queryParams.Set("X-Goog-Credential", fmt.Sprintf("%s/%s", g.serviceAccountEmail, credentialScope))
	queryParams.Set("X-Goog-Date", timestamp)
	queryParams.Set("X-Goog-Expires", strconv.FormatInt(expires, 10))
	queryParams.Set("X-Goog-SignedHeaders", "host")

	canonicalQueryString := queryParams.Encode()
	canonicalHeaders := "host:storage.googleapis.com\n"
	canonicalRequest := fmt.Sprintf("GET\n%s\n%s\n%s\nhost\nUNSIGNED-PAYLOAD",
		canonicalURI, canonicalQueryString, canonicalHeaders)

	// Hash the canonical request
	hasher := sha256.New()
	hasher.Write([]byte(canonicalRequest))
	canonicalRequestHash := fmt.Sprintf("%x", hasher.Sum(nil))

	stringToSign := fmt.Sprintf("GOOG4-RSA-SHA256\n%s\n%s\n%s",
		timestamp, credentialScope, canonicalRequestHash)

	hasher = sha256.New()
	hasher.Write([]byte(stringToSign))
	digest := hasher.Sum(nil)

	signature, err := rsa.SignPKCS1v15(rand.Reader, g.privateKey, crypto.SHA256, digest)
	if err != nil {
		return "", fmt.Errorf("failed to sign URL: %w", err)
	}

	queryParams.Set("X-Goog-Signature", fmt.Sprintf("%x", signature))

	// Build final URL
	return fmt.Sprintf("https://storage.googleapis.com%s?%s", canonicalURI, queryParams.Encode()), nil
}
