// bundleresources.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// bundleresources goes through the current resources directory and uploads
// files whose contents aren't already in the GCS bucket. It then generates a
// JSON manifest that records the filenames and their associated hashes; this
// is then used by vice at runtime to make sure it has all of the resources
// locally.

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
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const bucketName = "vice-resources"
const resourcesDir = "./resources"
const outFile = "manifest.json"

type uploadResult struct {
	path string
	hash string
}

func isTemporaryFile(name string) bool {
	base := filepath.Base(name)
	return strings.HasPrefix(base, ".") ||
		(strings.HasPrefix(base, "#") && strings.HasSuffix(base, "#")) ||
		strings.HasSuffix(base, "~")
}

func sha256file(path string) (string, error) {
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

// Upload the file at the given path (with associated SHA256 hash) to the
// GCS bucket.
func uploadToGCS(ctx context.Context, bucket *storage.BucketHandle, path, hash string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	w := bucket.Object(hash).NewWriter(ctx)

	if _, err = io.Copy(w, file); err != nil {
		return err
	}
	return w.Close()
}

func main() {
	if len(os.Args) != 1 {
		log.Fatal("Usage: bundleresources")
	}

	// Get credentials and create storage client.
	credsJSON := os.Getenv("VICE_GCS_CREDENTIALS")
	if credsJSON == "" {
		log.Fatal("VICE_GCS_CREDENTIALS environment variable not set")
	}

	ctx := context.Background()
	client, err := storage.NewClient(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
	if err != nil {
		log.Fatalf("Failed to create storage client: %v", err)
	}
	defer client.Close()

	bucket := client.Bucket(bucketName)

	// List existing objects in the bucket
	listStart := time.Now()
	existingObjects, err := listExistingObjects(ctx, bucket)
	if err != nil {
		log.Fatalf("Failed to list existing objects: %v", err)
	}
	fmt.Printf("Listed %d existing objects in %s\n", len(existingObjects), time.Since(listStart))

	// Walk the resources directory and send the paths of all non-temporary files to filesChan
	var eg errgroup.Group
	filesChan := make(chan string, 1)
	eg.Go(func() error {
		defer close(filesChan)

		return filepath.Walk(resourcesDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() || isTemporaryFile(path) {
				return nil
			}

			filesChan <- path

			return nil
		})
	})

	resultsChan := make(chan uploadResult)

	// Launch upload workers
	const numWorkers = 16
	for range numWorkers {
		eg.Go(func() error {
			for path := range filesChan {
				hash, err := sha256file(path)
				if err != nil {
					return fmt.Errorf("%s: %v", path, err)
				}

				if existingObjects[hash] {
					fmt.Printf("Skipped %s -> %s (already exists)\n", path, hash)
				} else {
					if err := uploadToGCS(ctx, bucket, path, hash); err != nil {
						return err
					}
					fmt.Printf("Uploaded %s -> %s\n", path, hash)
				}

				resultsChan <- uploadResult{
					path: path,
					hash: hash,
				}
			}
			return nil
		})
	}

	// Close the results channel once all workers have finished.
	go func() {
		if err := eg.Wait(); err != nil {
			log.Fatalf("%v", err)
		}
		close(resultsChan)
	}()

	// Harvest results as they come in and build the manifest.
	manifest := make(map[string]string)
	for result := range resultsChan {
		relPath, err := filepath.Rel(resourcesDir, result.path)
		if err != nil {
			log.Fatalf("%s: %v", result.path, err)
		}

		manifest[relPath] = result.hash
	}

	// Generate and write the manifest.
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal manifest: %v", err)
	}
	if err := os.WriteFile(outFile, manifestData, 0644); err != nil {
		log.Fatalf("%s: failed to write manifest: %v", outFile, err)
	}

	fmt.Printf("Generated %q with %d entries\n", outFile, len(manifest))
}
