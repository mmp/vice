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
	"os"
	"path/filepath"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"golang.org/x/sync/errgroup"

	"github.com/AllenDang/cimgui-go/imgui"
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
	btext := util.Select(r.currentFile == r.totalFiles && len(r.errors) > 0, "Ok", "Cancel")
	return []ModalDialogButton{ModalDialogButton{text: btext,
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
	errorsCh          chan error
}

// launchWorkers launches goroutines to check each entry in the manifest
// and see if we have a local copy of it with the correct contents.  If
// not, the file is downloaded from GCS. The returned workerStatus struct has
// three chans that provide information about the workers' progress.
func launchWorkers(resourcesDir string, manifest map[string]string) (workerStatus, int64) {
	status := workerStatus{
		doneCh:            make(chan struct{}),
		bytesDownloadedCh: make(chan int64),
		errorsCh:          make(chan error),
	}

	gcs, err := util.MakeGCSClient("vice-resources", util.GCSClientConfig{})
	if err != nil {
		status.errorsCh <- fmt.Errorf("failed to create GCS client: %w", err)
		close(status.doneCh)
		return status, 0
	}

	sizes, err := gcs.List("")
	if err != nil {
		// If for some reason we can't list the bucket contents, at least
		// populate sizes with bogus sizes for the items in the manifest so
		// that the dialog box drawing code behaves. (This may not be worth
		// bothering with since presumably the downloads will fail in this
		// case as well.)
		for _, hash := range manifest {
			sizes[hash] = 1
		}
	}

	var totalSize int64
	for _, hash := range manifest {
		totalSize += sizes[hash]
	}

	var eg errgroup.Group
	sem := make(chan struct{}, 8)
	for filename, hash := range manifest {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() {
				status.bytesDownloadedCh <- sizes[hash]
				<-sem
			}()

			fullPath := filepath.Join(resourcesDir, filename)

			return maybeDownload(gcs, filename, fullPath, hash)
		})
	}

	// Launch a separate goroutine to wait for the workers and report back
	// when they're all done. (We don't want to do this synchronously so
	// that SyncResources can update the UI/report progress.)
	go func() {
		if err := eg.Wait(); err != nil {
			status.errorsCh <- err
		}
		close(status.doneCh)
	}()

	return status, totalSize
}

func maybeDownload(gcs *util.GCSClient, filename, fullPath, hash string) error {
	// Check if file exists and has correct hash
	if existingHash, err := calculateSHA256(fullPath); err == nil && existingHash == hash {
		return nil
	}

	os.Remove(fullPath) // ignore errors; it may not exist

	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("%s: failed to create file's directory: %w", filename, err)
	}

	reader, err := gcs.GetReader(hash)
	if err != nil {
		return fmt.Errorf("%s: failed to download: %w", filename, err)
	}
	defer reader.Close()

	f, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("%s: failed to create: %w", filename, err)
	}
	defer f.Close()

	_, err = io.Copy(f, reader)
	if err != nil {
		return fmt.Errorf("%s: failed to write: %w", filename, err)
	}

	return nil
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
				if len(client.errors) == 0 {
					// Keep running the event loop if errors have been
					// reported; when the user acks, os.Exit will be
					// called.
					break loop
				}
			case nb := <-ws.bytesDownloadedCh:
				client.currentFile++
				client.downloadedBytes += nb
			case e := <-ws.errorsCh:
				client.errors = append(client.errors, e.Error())
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
