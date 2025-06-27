// bundleresources.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const bucketName = "vice-resources"
const resourcesDir = "./resources"
const outFile = "manifest.json"

type resourceFile struct {
	path    string
	relPath string
}

type uploadResult struct {
	path     string
	hash     string
	uploaded bool
	err      error
}

func isTemporaryFile(name string) bool {
	base := filepath.Base(name)
	return strings.HasPrefix(base, ".") ||
		(strings.HasPrefix(base, "#") && strings.HasSuffix(base, "#")) ||
		strings.HasSuffix(base, "~")
}

func computeSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func listExistingObjects(ctx context.Context, bucket *storage.BucketHandle) (map[string]bool, error) {
	existingObjects := make(map[string]bool)

	query := storage.Query{Projection: storage.ProjectionNoACL}
	it := bucket.Objects(ctx, &query)
	for {
		if obj, err := it.Next(); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("failed to list objects: %v", err)
		} else {
			existingObjects[obj.Name] = true
		}
	}

	return existingObjects, nil
}

func uploadToGCS(ctx context.Context, bucket *storage.BucketHandle, path, hash string, existing map[string]bool) (bool, error) {
	if existing[hash] {
		// The object exists already
		return false, nil
	}

	// Object doesn't exist, upload it
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	obj := bucket.Object(hash)
	w := obj.NewWriter(ctx)
	defer w.Close()

	if _, err := io.Copy(w, file); err != nil {
		return false, err
	}

	return true, nil
}

func main() {
	if len(os.Args) != 1 {
		log.Fatal("Usage: bundleresources")
	}

	// Get credentials and create storage client.
	credsJSON := os.Getenv("GCS_UPLOAD_CREDENTIALS")
	if credsJSON == "" {
		log.Fatal("GCS_UPLOAD_CREDENTIALS environment variable not set")
	}
	ctx := context.Background()
	client, err := storage.NewClient(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
	if err != nil {
		log.Fatalf("Failed to create storage client: %v", err)
	}
	defer client.Close()

	bucket := client.Bucket(bucketName)

	// Walk the resources directory and record all non-temporary files
	var files []string
	err = filepath.Walk(resourcesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || isTemporaryFile(path) {
			return nil
		}

		files = append(files, path)

		return nil
	})
	if err != nil {
		log.Fatalf("Error walking resources directory: %v", err)
	}

	// Enqueue jobs for resources files
	filesChan := make(chan string, len(files))
	resultsChan := make(chan uploadResult, len(files))

	for _, f := range files {
		filesChan <- f
	}
	close(filesChan)

	// List existing objects in the bucket
	listStart := time.Now()
	existingObjects, err := listExistingObjects(ctx, bucket)
	if err != nil {
		log.Fatalf("Failed to list existing objects: %v", err)
	}
	fmt.Printf("Listed %d existing objects in %s\n", len(existingObjects), time.Since(listStart))

	// Launch upload workers
	const numWorkers = 8
	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range filesChan {
				hash, err := computeSHA256(path)
				if err != nil {
					log.Fatalf("failed to compute hash for %s: %v", path, err)
				}

				uploaded, err := uploadToGCS(ctx, bucket, path, hash, existingObjects)

				resultsChan <- uploadResult{
					path:     path,
					hash:     hash,
					uploaded: uploaded,
					err:      err,
				}
			}
		}()
	}

	// Close the results channel once all workers have finished.
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Harvest results as they come in and build the manifest.
	manifest := make(map[string]string)
	hasError := false
	for result := range resultsChan {
		if result.err != nil {
			log.Printf("Failed to upload %s: %v", result.path, result.err)
			hasError = true
			continue
		}

		relPath, err := filepath.Rel(resourcesDir, result.path)
		if err != nil {
			log.Printf("%s: %v", result.path, err)
			hasError = true
			continue
		}

		manifest[relPath] = result.hash

		if result.uploaded {
			fmt.Printf("Uploaded %s -> %s\n", result.path, result.hash)
		} else {
			fmt.Printf("Skipped %s -> %s (already exists)\n", result.path, result.hash)
		}
	}
	if hasError {
		log.Fatal("Some uploads failed")
	}

	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal manifest: %v", err)
	}

	if err := os.WriteFile(outFile, manifestData, 0644); err != nil {
		log.Fatalf("%s: failed to write manifest: %v", outFile, err)
	}

	fmt.Printf("Generated %q with %d entries\n", outFile, len(manifest))
}
