// bundleresources.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// bundleresources goes through the current resources directory and uploads
// files whose contents aren't already in the R2 bucket. It then generates a
// JSON manifest that records the filenames and their associated hashes and
// sizes; this is then used by vice at runtime to make sure it has all of the
// resources locally.

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/sync/errgroup"
)

const bucketName = "vice-resources"
const resourcesDir = "./resources"
const modelsManifestPath = "./resources/models/manifest.json"
const outFile = "manifest.json"

type manifestEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

type uploadResult struct {
	path string
	hash string
	size int64
}

// Walk the resources directory and send the paths of all non-temporary files to filesChan
func walkResourcesDirectory(eg *errgroup.Group) chan string {
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
	return filesChan
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

func listExistingObjects(ctx context.Context, client *s3.Client) (map[string]bool, error) {
	existingObjects := make(map[string]bool)

	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %v", err)
		}
		for _, obj := range page.Contents {
			existingObjects[*obj.Key] = true
		}
	}

	return existingObjects, nil
}

// Upload the file at the given path (with associated SHA256 hash) to the
// R2 bucket.
func uploadToR2(ctx context.Context, client *s3.Client, path, hash string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(hash),
		Body:   file,
	})
	return err
}

func main() {
	noUpload := flag.Bool("no-upload", false, "generate manifest.json from local files without uploading to R2")
	flag.Parse()

	if flag.NArg() != 0 {
		log.Fatal("Usage: bundleresources [-no-upload]")
	}

	manifest := make(map[string]manifestEntry)

	var eg errgroup.Group
	if *noUpload {
		filesChan := walkResourcesDirectory(&eg)

		for path := range filesChan {
			hash, err := sha256file(path)
			if err != nil {
				log.Fatalf("%s: %v", path, err)
			}

			info, err := os.Stat(path)
			if err != nil {
				log.Fatalf("%s: %v", path, err)
			}

			relPath, err := filepath.Rel(resourcesDir, path)
			if err != nil {
				log.Fatalf("%s: %v", path, err)
			}

			manifest[relPath] = manifestEntry{Hash: hash, Size: info.Size()}
		}
	} else {
		// Get R2 credentials from environment.
		accountID := os.Getenv("R2_ACCOUNT_ID")
		accessKeyID := os.Getenv("R2_ACCESS_KEY_ID")
		secretAccessKey := os.Getenv("R2_SECRET_ACCESS_KEY")
		if accountID == "" || accessKeyID == "" || secretAccessKey == "" {
			log.Fatal("R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, and R2_SECRET_ACCESS_KEY environment variables must be set")
		}

		ctx := context.Background()

		// Create S3-compatible client for Cloudflare R2.
		// Force TLS 1.2 to work around TLS 1.3 handshake failures with R2 endpoints.
		r2Endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MaxVersion: tls.VersionTLS12,
				},
			},
		}
		cfg, err := config.LoadDefaultConfig(ctx,
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
			config.WithRegion("auto"),
			config.WithHTTPClient(httpClient),
		)
		if err != nil {
			log.Fatalf("Failed to load AWS config: %v", err)
		}

		client := s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(r2Endpoint)
			o.UsePathStyle = true
		})

		// List existing objects in the bucket
		listStart := time.Now()
		existingObjects, err := listExistingObjects(ctx, client)
		if err != nil {
			log.Fatalf("Failed to list existing objects: %v", err)
		}
		fmt.Printf("Listed %d existing objects in %s\n", len(existingObjects), time.Since(listStart))

		filesChan := walkResourcesDirectory(&eg)

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

					info, err := os.Stat(path)
					if err != nil {
						return fmt.Errorf("%s: %v", path, err)
					}

					if existingObjects[hash] {
						fmt.Printf("Skipped %s -> %s (already exists)\n", path, hash)
					} else {
						if err := uploadToR2(ctx, client, path, hash); err != nil {
							return err
						}
						fmt.Printf("Uploaded %s -> %s\n", path, hash)
					}

					resultsChan <- uploadResult{
						path: path,
						hash: hash,
						size: info.Size(),
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
		for result := range resultsChan {
			relPath, err := filepath.Rel(resourcesDir, result.path)
			if err != nil {
				log.Fatalf("%s: %v", result.path, err)
			}

			manifest[relPath] = manifestEntry{Hash: result.hash, Size: result.size}
		}
	}

	// Merge in the models manifest. The model files are stored directly in
	// R2 (not in git) so they're not found when walking the resources
	// directory. We have their hashes in resources/models/manifest.json,
	// so we merge those entries with the appropriate path prefix.
	if modelsJSON, err := os.ReadFile(modelsManifestPath); err == nil {
		var modelsManifest map[string]manifestEntry
		if err := json.Unmarshal(modelsJSON, &modelsManifest); err != nil {
			log.Fatalf("Failed to parse models manifest: %v", err)
		}
		for filename, entry := range modelsManifest {
			manifest["models/"+filename] = entry
		}
		fmt.Printf("Merged %d entries from models manifest\n", len(modelsManifest))
	} else if !os.IsNotExist(err) {
		log.Fatalf("Failed to read models manifest: %v", err)
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
