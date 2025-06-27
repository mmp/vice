// resources_download.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for builds that are expected to fetch resources as needed
// into a local cache from cloud storage.
//go:build downloadresources

package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"cloud.google.com/go/storage"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"google.golang.org/api/iterator"

	"github.com/AllenDang/cimgui-go/imgui"
)

//go:embed manifest.json
var resourcesManifest string

type ResourcesDownloadModalClient struct {
	currentFile     int
	totalFiles      int
	downloadedBytes int64
	totalBytes      int64
	errors          []string
}

func (r *ResourcesDownloadModalClient) Title() string {
	return "Downloading Resources"
}

func (r *ResourcesDownloadModalClient) Opening() {}

func (r *ResourcesDownloadModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{ModalDialogButton{text: "Cancel",
		action: func() bool {
			os.Exit(1)
			return true
		}}}
}

func (r *ResourcesDownloadModalClient) Draw() int {
	for _, e := range r.errors {
		imgui.Text("Error: " + e)
	}

	imgui.Text(fmt.Sprintf("Downloaded file %d of %d", r.currentFile, r.totalFiles))

	// Add some spacing
	imgui.Spacing()

	if r.totalBytes > 0 {
		progress := float32(r.downloadedBytes) / float32(r.totalBytes)

		// Use a fixed width progress bar to ensure minimum dialog width
		imgui.ProgressBarV(progress, imgui.Vec2{350, 0}, fmt.Sprintf("%.1f MB / %.1f MB",
			float64(r.downloadedBytes)/(1024*1024), float64(r.totalBytes)/(1024*1024)))
	}

	imgui.Spacing()

	return -1
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

func calculateSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func migrateResourcesIfNeeded(resourcesDir string) {
	if _, err := os.Stat("./resources"); err == nil {
		if _, err := os.Stat(resourcesDir); os.IsNotExist(err) {
			if err := os.MkdirAll(resourcesDir, 0755); err != nil {
				panic(fmt.Sprintf("failed to create resources directory: %v", err))
			}
			if err := copyDir("./resources", resourcesDir); err != nil {
				panic(fmt.Sprintf("failed to copy resources: %v", err))
			}
		}
	}
}

func checkManifestUpToDate(manifestPath string) bool {
	if existingManifest, err := os.ReadFile(manifestPath); err == nil {
		return string(existingManifest) == resourcesManifest
	}
	return false
}

func removeStaleResourcesFiles(resourcesDir string, manifest map[string]string) {
	manifestFiles := make(map[string]bool)
	for filename := range manifest {
		manifestFiles[filename] = true
	}

	filepath.Walk(resourcesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(resourcesDir, path)
		if err != nil {
			return nil
		}

		if relPath != "manifest.json" && !manifestFiles[relPath] {
			os.Remove(path)
		}

		return nil
	})
}

func writeManifestFile(manifestPath string) {
	manifestFile, err := os.Create(manifestPath)
	if err != nil {
		panic(fmt.Sprintf("failed to create manifest file: %v", err))
	}
	defer manifestFile.Close()

	if _, err := manifestFile.WriteString(resourcesManifest); err != nil {
		panic(fmt.Sprintf("failed to write manifest: %v", err))
	}

	if err := manifestFile.Sync(); err != nil {
		panic(fmt.Sprintf("failed to sync manifest file: %v", err))
	}
}

type workerStatus struct {
	doneChan     chan struct{}
	finishedChan chan int64 // bytes
	errorsChan   chan string
}

func launchWorkers(resourcesDir string, manifest map[string]string) (workerStatus, int64) {
	status := workerStatus{
		doneChan:     make(chan struct{}),
		finishedChan: make(chan int64),
		errorsChan:   make(chan string),
	}

	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	sizes := make(map[string]int64)
	if err != nil {
		// If for some reason we can't list the bucket contents, at least
		// populate sizes with bogus sizes for the items in the manifest so
		// that the dialog box drawing code behaves. (This may not be worth
		// bothering with since presumably the downloads will fail in this
		// case as well.)
		for _, hash := range manifest {
			sizes[hash] = 1
		}
	} else {
		defer client.Close()
		bucket := client.Bucket("vice-resources")

		// Get the sizes of all of the objects in the bucket; note that the key is the hash.
		query := storage.Query{Projection: storage.ProjectionNoACL}
		it := bucket.Objects(ctx, &query)
		for {
			if obj, err := it.Next(); err == iterator.Done {
				break
			} else if err == nil {
				sizes[obj.Name] = obj.Size
			}
		}
	}

	var totalSize int64
	for _, hash := range manifest {
		totalSize += sizes[hash]
	}

	type fileTask struct {
		filename string
		hash     string
	}
	fileChan := make(chan fileTask, len(manifest))

	var wg sync.WaitGroup
	const numWorkers = 8
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for task := range fileChan {
				func() {
					fullPath := filepath.Join(resourcesDir, task.filename)

					defer func() { status.finishedChan <- sizes[task.hash] }()

					// Check if file exists and has correct hash
					if existingHash, err := calculateSHA256(fullPath); err == nil && existingHash == task.hash {
						return
					}

					os.Remove(fullPath) // ignore errors; it may not exist

					// Download the file
					url := fmt.Sprintf("https://storage.googleapis.com/vice-resources/%s", task.hash)
					resp, err := http.Get(url)
					if err != nil {
						status.errorsChan <- err.Error()
						return
					}
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						status.errorsChan <- fmt.Sprintf("Failed to download %s: status %d", task.filename, resp.StatusCode)
						return
					}

					// Create directory if needed
					if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
						status.errorsChan <- fmt.Sprintf("Failed to create directory for %s: %v", task.filename, err)
						return
					}

					// Write file
					f, err := os.Create(fullPath)
					if err != nil {
						status.errorsChan <- fmt.Sprintf("Failed to create %s: %v", task.filename, err)
						return
					}
					defer f.Close()

					_, err = io.Copy(f, resp.Body)
					if err != nil {
						status.errorsChan <- fmt.Sprintf("Failed to write %s: %v", task.filename, err)
						return
					}
				}()
			}
		}()
	}

	go func() {
		for filename, hash := range manifest {
			fileChan <- fileTask{filename: filename, hash: hash}
		}
		close(fileChan)
		wg.Wait()
		close(status.doneChan)
	}()

	return status, totalSize
}

func SyncResources(plat platform.Platform, r renderer.Renderer, lg *log.Logger) {
	if resourcesManifest == "" {
		panic("manifest.json was not present during build")
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		panic(fmt.Sprintf("failed to get user config dir: %v", err))
	}

	resourcesDir := filepath.Join(configDir, "vice", "resources")
	manifestPath := filepath.Join(resourcesDir, "manifest.json")

	migrateResourcesIfNeeded(resourcesDir)

	var manifest map[string]string
	if err := json.Unmarshal([]byte(resourcesManifest), &manifest); err != nil {
		panic(fmt.Sprintf("failed to unmarshal manifest: %v", err))
	}

	// Check if manifest is up to date early
	if checkManifestUpToDate(manifestPath) {
		return
	}

	if _, err := os.Stat(manifestPath); err == nil {
		os.Remove(manifestPath)
	}

	// Launch worker goroutines
	ws, totalBytes := launchWorkers(resourcesDir, manifest)

	client := &ResourcesDownloadModalClient{totalFiles: len(manifest), totalBytes: totalBytes}
	dialog := NewModalDialogBox(client, plat)

loop:
	for {
		// Draw download progress dialog box
		plat.ProcessEvents()
		plat.NewFrame()
		imgui.NewFrame()
		imgui.PushFont(&ui.font.Ifont)
		dialog.Draw()
		imgui.PopFont()

		imgui.Render()
		var cb renderer.CommandBuffer
		renderer.GenerateImguiCommandBuffer(&cb, plat.DisplaySize(), plat.FramebufferSize(), lg)
		r.RenderCommandBuffer(&cb)
		plat.PostRender()

		select {
		case <-ws.doneChan:
			break loop
		case nb := <-ws.finishedChan:
			client.currentFile++
			client.downloadedBytes += nb
		case e := <-ws.errorsChan:
			client.errors = append(client.errors, e)
		default:
			break
		}
	}

	removeStaleResourcesFiles(resourcesDir, manifest)

	writeManifestFile(manifestPath)
}
