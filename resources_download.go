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

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"

	"cloud.google.com/go/storage"
	"github.com/AllenDang/cimgui-go/imgui"
	"google.golang.org/api/iterator"
)

// resourcesManifest holds the filenames and SHA256 hashes of all the resource files this build
// of vice expects to have available.
//
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

// We write the manifest.json file to our local resources directory *after*
// downloading all of the resources it lists (and delete manifest.json if
// it is out of date). Thus, we can assume that if manifest.json is there,
// the underlying resources are all as expected.
func checkManifestUpToDate(manifestPath string) bool {
	if existingManifest, err := os.ReadFile(manifestPath); err == nil {
		return string(existingManifest) == resourcesManifest
	}
	return false
}

// Removes any resource files that are present on disk but are not listed in the manifest.
func removeStaleResourcesFiles(resourcesDir string, manifest map[string]string) {
	filepath.Walk(resourcesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(resourcesDir, path)
		if err != nil {
			return nil
		}

		if relPath == "manifest.json" {
			return nil
		}

		if _, ok := manifest[relPath]; !ok {
			os.Remove(path)
		}

		return nil
	})
}

func writeManifestFile(manifestPath string) error {
	f, err := os.Create(manifestPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(resourcesManifest)
	return err
}

type workerStatus struct {
	doneCh            chan struct{}
	bytesDownloadedCh chan int64
	errorsCh          chan string
}

// launchWorkers launches goroutines to check each entry in the manifest
// and see if we have a local copy of it with the correct contents.  If
// not, the file is downloaded from GCS. The returned workerStatus struct has
// three chans that provide information about the workers' progress.
func launchWorkers(resourcesDir string, manifest map[string]string) (workerStatus, int64) {
	status := workerStatus{
		doneCh:            make(chan struct{}),
		bytesDownloadedCh: make(chan int64),
		errorsCh:          make(chan string),
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
	for filename, hash := range manifest {
		fileChan <- fileTask{filename: filename, hash: hash}
	}
	close(fileChan)

	var wg sync.WaitGroup
	const numWorkers = 8
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for task := range fileChan {
				func() {
					fullPath := filepath.Join(resourcesDir, task.filename)

					defer func() { status.bytesDownloadedCh <- sizes[task.hash] }()

					// Check if file exists and has correct hash
					if existingHash, err := calculateSHA256(fullPath); err == nil && existingHash == task.hash {
						return
					}

					os.Remove(fullPath) // ignore errors; it may not exist

					// Download the file
					url := fmt.Sprintf("https://storage.googleapis.com/vice-resources/%s", task.hash)
					resp, err := http.Get(url)
					if err != nil {
						status.errorsCh <- err.Error()
						return
					}
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						status.errorsCh <- fmt.Sprintf("Failed to download %s: status %d", task.filename, resp.StatusCode)
						return
					}

					// Create directory if needed
					if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
						status.errorsCh <- fmt.Sprintf("%s: failed to create file's directory: %v", task.filename, err)
						return
					}

					// Write file
					f, err := os.Create(fullPath)
					if err != nil {
						status.errorsCh <- fmt.Sprintf("%s: failed to create: %v", task.filename, err)
						return
					}
					defer f.Close()

					_, err = io.Copy(f, resp.Body)
					if err != nil {
						status.errorsCh <- fmt.Sprintf("%s: failed to write: %v", task.filename, err)
						return
					}

					if err := f.Sync(); err != nil {
						status.errorsCh <- fmt.Sprintf("%s: failed to sync: %v", task.filename, err)
						return
					}
				}()
			}
		}()
	}

	// Launch a separate goroutine to wait for the workers and report back
	// when they're all done. (We don't want to do this synchronously so
	// that SyncResources can update the UI/report progress.)
	go func() {
		wg.Wait()
		close(status.doneCh)
	}()

	return status, totalSize
}

func SyncResources(plat platform.Platform, r renderer.Renderer, lg *log.Logger) error {
	if resourcesManifest == "" {
		return fmt.Errorf("manifest.json was not present during build")
	}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get user config dir: %v", err)
	}

	resourcesDir := filepath.Join(configDir, "vice", "resources")
	manifestPath := filepath.Join(resourcesDir, "manifest.json")

	var manifest map[string]string
	if err := json.Unmarshal([]byte(resourcesManifest), &manifest); err != nil {
		return fmt.Errorf("failed to unmarshal resources manifest: %v", err)
	}

	// Check if manifest is up to date early
	if checkManifestUpToDate(manifestPath) {
		return nil
	}

	// Otherwise remove it immediately to reflect that the local resources
	// directory will be in flux and doesn't match any manifest.
	if _, err := os.Stat(manifestPath); err == nil {
		os.Remove(manifestPath)
	}

	ws, totalBytes := launchWorkers(resourcesDir, manifest)

	if plat != nil {
		// Draw download progress dialog box
		client := &ResourcesDownloadModalClient{totalFiles: len(manifest), totalBytes: totalBytes}
		dialog := NewModalDialogBox(client, plat)

	loop:
		for {
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
			case <-ws.doneCh:
				break loop
			case nb := <-ws.bytesDownloadedCh:
				client.currentFile++
				client.downloadedBytes += nb
			case e := <-ws.errorsCh:
				client.errors = append(client.errors, e)
			default:
				break
			}
		}
	} else {
		// Text-mode (e.g. for the server and tests; print updates to stdout.)
		nfiles, nbytes := 0, int64(0)
	loopb:
		for {
			select {
			case <-ws.doneCh:
				break loopb
			case nb := <-ws.bytesDownloadedCh:
				nfiles++
				nbytes += nb
				fmt.Printf("%d files (%d bytes) downloaded\n", nfiles, nbytes)
			case e := <-ws.errorsCh:
				fmt.Printf("Error: %v\n", e)
			default:
				break
			}
		}
	}

	removeStaleResourcesFiles(resourcesDir, manifest)

	// Only now do we write the current manifest.json to reflect that we are good to go.
	return writeManifestFile(manifestPath)
}
