// resources_download.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// This file is included for builds that are expected to fetch resources as needed
// into a local cache from cloud storage.
//go:build downloadresources

package main

import (
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
	"sync/atomic"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"

	"github.com/AllenDang/cimgui-go/imgui"
)

//go:embed manifest.json
var resourcesManifest string

type ResourcesDownloadModalClient struct {
	totalBytes      int64
	downloadedBytes atomic.Int64
	totalFiles      int
	currentFile     atomic.Int32
	done            atomic.Bool
	errorMsg        string
	mu              sync.Mutex
}

func (r *ResourcesDownloadModalClient) Title() string {
	return "Downloading Resources"
}

func (r *ResourcesDownloadModalClient) Opening() {}

func (r *ResourcesDownloadModalClient) Buttons() []ModalDialogButton {
	if r.done.Load() {
		return []ModalDialogButton{ModalDialogButton{text: "Ok", action: func() bool { return true }}}
	} else {
		return []ModalDialogButton{ModalDialogButton{text: "Cancel",
			action: func() bool {
				os.Exit(1)
				return true
			}}}
	}
}

func (r *ResourcesDownloadModalClient) Draw() int {
	if r.errorMsg != "" {
		imgui.Text("Error: " + r.errorMsg)
		return -1
	}

	current := int(r.currentFile.Load())
	downloaded := r.downloadedBytes.Load()

	imgui.Text(fmt.Sprintf("Downloading resource file %d of %d", current, r.totalFiles))

	// Add some spacing
	imgui.Spacing()

	if r.totalBytes > 0 {
		progress := float32(downloaded) / float32(r.totalBytes)

		// Use a fixed width progress bar to ensure minimum dialog width
		imgui.ProgressBarV(progress, imgui.Vec2{350, 0}, fmt.Sprintf("%.1f MB / %.1f MB",
			float64(downloaded)/(1024*1024), float64(r.totalBytes)/(1024*1024)))
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

type downloadTask struct {
	hash     string
	filename string
	size     int64
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

func collectDownloadTasks(manifest map[string]string, resourcesDir string, taskChan chan<- downloadTask,
	doneChan <-chan struct{}) {
	defer close(taskChan)

	type checkJob struct {
		filename string
		hash     string
	}

	checkChan := make(chan checkJob, len(manifest))
	var wg sync.WaitGroup

	// Start workers to check files concurrently
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range checkChan {
				fullPath := filepath.Join(resourcesDir, job.filename)

				if existingHash, err := calculateSHA256(fullPath); err != nil || existingHash != job.hash {
					if _, err := os.Stat(fullPath); err == nil {
						os.Remove(fullPath)
					}

					resp, err := http.Head("https://storage.googleapis.com/vice-resources/" + job.hash)
					if err == nil && resp.StatusCode == http.StatusOK {
						size := resp.ContentLength
						resp.Body.Close()

						task := downloadTask{
							hash:     job.hash,
							filename: job.filename,
							size:     size,
						}

						select {
						case taskChan <- task:
						case <-doneChan:
							return
						}
					}
				}
			}
		}()
	}

	// Send all check jobs
	for filename, hash := range manifest {
		checkChan <- checkJob{filename: filename, hash: hash}
	}
	close(checkChan)

	wg.Wait()
}

func downloadResources(taskChan <-chan downloadTask, doneChan <-chan struct{}, resourcesDir string,
	client *ResourcesDownloadModalClient) {
	var wg sync.WaitGroup

	var fileCounter atomic.Int32

	// Start download workers
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Helper function to report errors to client
			reportError := func(task downloadTask, format string, args ...interface{}) {
				if client != nil {
					client.mu.Lock()
					client.errorMsg = fmt.Sprintf("Failed to "+format, append([]interface{}{task.filename}, args...)...)
					client.mu.Unlock()
				}
			}

			for {
				select {
				case task, ok := <-taskChan:
					if !ok {
						return
					}

					if client != nil {
						client.currentFile.Store(fileCounter.Add(1))
					}

					url := fmt.Sprintf("https://storage.googleapis.com/vice-resources/%s", task.hash)
					resp, err := http.Get(url)
					if err != nil {
						reportError(task, "download %s: %v", err)
						return
					}
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusOK {
						reportError(task, "download %s: status %d", resp.StatusCode)
						return
					}

					fullPath := filepath.Join(resourcesDir, task.filename)
					if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
						reportError(task, "create directory for %s: %v", err)
						return
					}

					file, err := os.Create(fullPath)
					if err != nil {
						reportError(task, "create %s: %v", err)
						return
					}
					defer file.Close()

					written, err := io.Copy(file, resp.Body)
					if err != nil {
						reportError(task, "write %s: %v", err)
						return
					}

					if client != nil {
						client.downloadedBytes.Add(written)
					}

				case <-doneChan:
					return
				}
			}
		}()
	}

	wg.Wait()

	if client != nil {
		client.done.Store(true)
	}
}

func cleanupOldFiles(resourcesDir string, manifest map[string]string) {
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

	// Start concurrent collection and downloading pipeline
	taskChan := make(chan downloadTask, 10)
	doneChan := make(chan struct{})

	var client *ResourcesDownloadModalClient
	var dialog *ModalDialogBox
	var totalBytes atomic.Int64
	var taskCount atomic.Int32
	var downloadStarted atomic.Bool

	// Start collecting download tasks concurrently
	go collectDownloadTasks(manifest, resourcesDir, taskChan, doneChan)

	// Prepare download channels
	downloadTaskChan := make(chan downloadTask, 10)
	downloadDoneChan := make(chan struct{})
	var downloadWg sync.WaitGroup
	var downloadStartedOnce atomic.Bool

	for {
		if dialog != nil && !client.done.Load() {
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
			lastUIUpdate = time.Now()
		}

		// Process tasks with a short timeout
		select {
		case task, ok := <-taskChan:
			if !ok {
				// No more tasks, close download channel and break
				close(downloadTaskChan)
				goto waitForDownloads
			}

			// First task - initialize UI and download workers if needed
			if !downloadStarted.Load() {
				totalFiles := int(taskCount.Add(1))
				totalBytes.Add(task.size)

				if plat != nil && r != nil && lg != nil {
					client = &ResourcesDownloadModalClient{
						totalBytes: totalBytes.Load(),
						totalFiles: totalFiles,
					}
					dialog = NewModalDialogBox(client, plat)
				}

				// Start download workers now that we have the client
				if !downloadStartedOnce.Load() {
					downloadWg.Add(1)
					go func() {
						defer downloadWg.Done()
						downloadResources(downloadTaskChan, downloadDoneChan, resourcesDir, client)
					}()
					downloadStartedOnce.Store(true)
				}

				downloadStarted.Store(true)
			} else {
				taskCount.Add(1)
				totalBytes.Add(task.size)

				// Update client totals if we have one
				if client != nil {
					client.totalFiles = int(taskCount.Load())
					client.totalBytes = totalBytes.Load()
				}
			}

			// Send task to download workers
			downloadTaskChan <- task

		case <-time.After(time.Millisecond):
			// Short timeout to ensure UI stays responsive
		}
	}

waitForDownloads:
	// Continue UI updates while downloads finish
	done := make(chan struct{})
	go func() {
		downloadWg.Wait()
		close(done)
	}()

	for {
		// Update UI at regular intervals
		if dialog != nil && !client.done.Load() {
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
			lastUIUpdate = time.Now()
		}

		select {
		case <-done:
			goto downloadsComplete
		default:
			break
		}
	}

downloadsComplete:

	cleanupOldFiles(resourcesDir, manifest)
	writeManifestFile(manifestPath)
}
