// pkg/util/gcs.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

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

// ListGCSBucketObjects returns a map of object names to their sizes from the specified Google Cloud Storage bucket.
// It uses the GCS JSON API v1.
func ListGCSBucketObjects(bucketName string) (map[string]int64, error) {
	if bucketName == "" {
		return nil, fmt.Errorf("bucket name cannot be empty")
	}

	allObjects := make(map[string]int64)
	nextPageToken := ""

	for {
		url := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o?projection=noAcl", bucketName)
		if nextPageToken != "" {
			url += "&pageToken=" + nextPageToken
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to make request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GCS API returned status %d", resp.StatusCode)
		}

		var listResp GCSListResponse
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}

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
