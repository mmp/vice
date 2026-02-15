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
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"golang.org/x/sync/errgroup"

	"github.com/AllenDang/cimgui-go/imgui"
	implogl3 "github.com/AllenDang/cimgui-go/impl/opengl3"
)

const resourcesBaseURL = "https://vice-resources.pharr.org"

// resourcesManifest holds the filenames, SHA256 hashes, and sizes of all the resource files
// this build of vice expects to have available.
//
//go:embed manifest.json
var resourcesManifest string

type manifestEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

type ResourcesDownloadModalClient struct {
	currentFile     int
	totalFiles      int
	completedBytes  int64            // Sum of fully downloaded files
	inProgressBytes map[string]int64 // filename -> bytes downloaded so far
	completedFiles  map[string]bool  // files that have completed (to ignore late progress updates)
	totalBytes      int64
	currentFileName string // Most recent filename for display
	errors          []string
}

func (r *ResourcesDownloadModalClient) Title() string {
	return "Downloading Resources"
}

func (r *ResourcesDownloadModalClient) Opening() {}

func (r *ResourcesDownloadModalClient) FixedSize() [2]float32 {
	return [2]float32{450, 150}
}

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

	if r.currentFileName != "" {
		imgui.Text(fmt.Sprintf("Downloading: %s", r.currentFileName))
	} else {
		imgui.Text("\n")
	}

	imgui.Spacing()

	if r.totalBytes > 0 {
		// Sum bytes from all files currently being downloaded
		var inProgress int64
		for _, bytes := range r.inProgressBytes {
			inProgress += bytes
		}
		totalDownloaded := r.completedBytes + inProgress
		progress := float32(totalDownloaded) / float32(r.totalBytes)

		// Progress bar fills available width (-1 for width)
		imgui.ProgressBarV(progress, imgui.Vec2{-1, 0}, fmt.Sprintf("%.1f MB / %.1f MB",
			float64(totalDownloaded)/(1024*1024), float64(r.totalBytes)/(1024*1024)))
	}

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

// validateAllResourcesExist checks that all files in the manifest exist on disk.
// This catches cases where a crash during download left some files missing,
// or where files were deleted after the manifest was written.
// Note: we only check existence, not content hashes, to preserve user edits.
func validateAllResourcesExist(resourcesDir string, manifest map[string]manifestEntry) bool {
	for filename := range manifest {
		fullPath := filepath.Join(resourcesDir, filename)
		if _, err := os.Stat(fullPath); err != nil {
			return false
		}
	}
	return true
}

// Removes any resource files that are present on disk but are not listed in the manifest.
func removeStaleResourcesFiles(resourcesDir string, manifest map[string]manifestEntry) {
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

		// Use forward slashes for lookup since manifest keys use forward slashes,
		// but filepath.Rel returns OS-native separators (backslashes on Windows).
		if _, ok := manifest[filepath.ToSlash(relPath)]; !ok {
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

type downloadProgress struct {
	filename     string
	bytesWritten int64
}

type fileCompleted struct {
	filename string
	size     int64
}

type workerStatus struct {
	doneCh      chan struct{}
	completedCh chan fileCompleted
	progressCh  chan downloadProgress
	errorsCh    chan error
}

// progressReader wraps an io.Reader and reports progress as data is read.
type progressReader struct {
	reader      io.Reader
	filename    string
	bytesRead   int64
	progressCh  chan<- downloadProgress
	lastReportN int64
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.bytesRead += int64(n)

	// Report progress every 64KB to avoid flooding the channel
	if pr.bytesRead-pr.lastReportN >= 64*1024 || err == io.EOF {
		select {
		case pr.progressCh <- downloadProgress{filename: pr.filename, bytesWritten: pr.bytesRead}:
		default:
			// Non-blocking send; skip if channel is full
		}
		pr.lastReportN = pr.bytesRead
	}

	return n, err
}

// launchWorkers launches goroutines to check each entry in the manifest
// and see if we have a local copy of it with the correct contents.  If
// not, the file is downloaded from R2. The returned workerStatus struct has
// three chans that provide information about the workers' progress.
func launchWorkers(resourcesDir string, manifest map[string]manifestEntry) (workerStatus, int64) {
	status := workerStatus{
		doneCh:      make(chan struct{}),
		completedCh: make(chan fileCompleted),
		progressCh:  make(chan downloadProgress, 16), // Buffered to avoid blocking workers
		errorsCh:    make(chan error),
	}

	var totalSize int64
	for _, entry := range manifest {
		totalSize += entry.Size
	}

	var eg errgroup.Group
	sem := make(chan struct{}, 8)
	for filename, entry := range manifest {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() {
				status.completedCh <- fileCompleted{filename: filename, size: entry.Size}
				<-sem
			}()

			fullPath := filepath.Join(resourcesDir, filename)

			return maybeDownload(filename, fullPath, entry.Hash, status.progressCh)
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

func maybeDownload(filename, fullPath, hash string, progressCh chan<- downloadProgress) error {
	// Check if file exists and has correct hash
	if existingHash, err := calculateSHA256(fullPath); err == nil && existingHash == hash {
		return nil
	}

	os.Remove(fullPath) // ignore errors; it may not exist

	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("%s: failed to create file's directory: %w", filename, err)
	}

	resp, err := http.Get(resourcesBaseURL + "/" + hash)
	if err != nil {
		return fmt.Errorf("%s: failed to download: %w", filename, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: download returned status %d", filename, resp.StatusCode)
	}

	f, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("%s: failed to create: %w", filename, err)
	}
	defer f.Close()

	// Wrap reader to report progress during download
	pr := &progressReader{
		reader:     resp.Body,
		filename:   filename,
		progressCh: progressCh,
	}

	_, err = io.Copy(f, pr)
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

	var manifest map[string]manifestEntry
	if err := json.Unmarshal([]byte(resourcesManifest), &manifest); err != nil {
		return fmt.Errorf("failed to unmarshal resources manifest: %v", err)
	}

	// Check if manifest is up to date and all files exist
	if checkManifestUpToDate(manifestPath) && validateAllResourcesExist(resourcesDir, manifest) {
		return nil
	}

	// Otherwise remove the manifest immediately to reflect that the local resources
	// directory will be in flux and doesn't match any manifest.
	if _, err := os.Stat(manifestPath); err == nil {
		os.Remove(manifestPath)
	}

	ws, totalBytes := launchWorkers(resourcesDir, manifest)

	if plat != nil {
		// Draw download progress dialog box
		client := &ResourcesDownloadModalClient{
			totalFiles:      len(manifest),
			totalBytes:      totalBytes,
			inProgressBytes: make(map[string]int64),
			completedFiles:  make(map[string]bool),
		}
		dialog := NewModalDialogBox(client, plat)

	loop:
		for {
			plat.ProcessEvents()
			plat.NewFrame()
			imgui.NewFrame()
			ui.font.ImguiPush()
			dialog.Draw()
			imgui.PopFont()

			imgui.Render()
			implogl3.RenderDrawData(imgui.CurrentDrawData())

			if imgui.CurrentIO().ConfigFlags()&imgui.ConfigFlagsViewportsEnable != 0 {
				imgui.UpdatePlatformWindows()
				imgui.RenderPlatformWindowsDefault()
				plat.MakeContextCurrent()
			}

			plat.PostRender()

			select {
			case <-ws.doneCh:
				if len(client.errors) == 0 {
					// Keep running the event loop if errors have been
					// reported; when the user acks, os.Exit will be
					// called.
					break loop
				}
			case fc := <-ws.completedCh:
				client.currentFile++
				client.completedBytes += fc.size
				client.completedFiles[fc.filename] = true
				delete(client.inProgressBytes, fc.filename)
				if client.currentFileName == fc.filename {
					client.currentFileName = ""
				}
			case p := <-ws.progressCh:
				// Ignore late progress updates for files that have already completed
				if !client.completedFiles[p.filename] {
					client.currentFileName = p.filename
					client.inProgressBytes[p.filename] = p.bytesWritten
				}
			case e := <-ws.errorsCh:
				client.errors = append(client.errors, e.Error())
			default:
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
			case fc := <-ws.completedCh:
				nfiles++
				nbytes += fc.size
				fmt.Printf("%d files (%d bytes) downloaded\n", nfiles, nbytes)
			case <-ws.progressCh:
				// In text mode, we don't need real-time progress updates
			case e := <-ws.errorsCh:
				fmt.Printf("Error: %v\n", e)
			default:
				time.Sleep(50 * time.Millisecond)
			}
		}
	}

	removeStaleResourcesFiles(resourcesDir, manifest)

	// Only now do we write the current manifest.json to reflect that we are good to go.
	return writeManifestFile(manifestPath)
}
