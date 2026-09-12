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
	"strings"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"golang.org/x/sync/errgroup"

	"github.com/AllenDang/cimgui-go/imgui"
	implogl3 "github.com/AllenDang/cimgui-go/impl/opengl3"
)

type userSyncChoice int

const (
	syncBackupAndContinue userSyncChoice = iota
	syncOverwriteAll
	syncQuit
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
	return []ModalDialogButton{{text: btext,
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
// Note: we only check existence, not sizes or content hashes: facility
// engineers overwrite files in the resources directory with copies they are
// working on, and anything stricter than an existence check sends their edits
// through the sync path, which restores the pristine files.
func validateAllResourcesExist(resourcesDir string, manifest map[string]manifestEntry) bool {
	for filename := range manifest {
		fullPath := filepath.Join(resourcesDir, filename)
		if _, err := os.Stat(fullPath); err != nil {
			return false
		}
	}
	return true
}

// findUserModifiedFiles returns filenames from the new manifest whose on-disk
// content was changed by the user (i.e. the hash differs from both the old and
// new manifests).
//
// Note: if a previous sync crashed, partially-downloaded files may have hashes
// matching neither manifest and will appear as "user modified." This is a
// harmless false positive — the user would choose "Overwrite All."
func findUserModifiedFiles(resourcesDir string, oldManifest, newManifest map[string]manifestEntry) []string {
	var modified []string
	for filename, newEntry := range newManifest {
		oldEntry, inOld := oldManifest[filename]
		if !inOld {
			continue // new file in this release
		}

		fullPath := filepath.Join(resourcesDir, filename)
		diskHash, err := calculateSHA256(fullPath)
		if err != nil {
			continue // file doesn't exist on disk; will be downloaded fresh
		}

		if diskHash == newEntry.Hash {
			continue // already correct
		}
		if diskHash == oldEntry.Hash {
			continue // user didn't change it; release updated it
		}

		modified = append(modified, filename)
	}
	return modified
}

// backupModifiedFiles copies each file in the list to
// {configDir}/Vice/resource-backups/{timestamp}/, preserving the relative
// directory structure. Returns the backup directory path.
func backupModifiedFiles(resourcesDir string, files []string) (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user config dir: %v", err)
	}

	timestamp := time.Now().Format("2006-01-02T150405")
	backupDir := filepath.Join(configDir, "Vice", "resource-backups", timestamp)

	for _, relPath := range files {
		src := filepath.Join(resourcesDir, relPath)
		dst := filepath.Join(backupDir, relPath)

		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return "", fmt.Errorf("failed to create backup directory for %s: %v", relPath, err)
		}

		srcFile, err := os.Open(src)
		if err != nil {
			return "", fmt.Errorf("failed to open %s for backup: %v", relPath, err)
		}

		dstFile, err := os.Create(dst)
		if err != nil {
			srcFile.Close()
			return "", fmt.Errorf("failed to create backup file %s: %v", relPath, err)
		}

		_, err = io.Copy(dstFile, srcFile)
		srcFile.Close()
		dstFile.Close()
		if err != nil {
			return "", fmt.Errorf("failed to copy %s to backup: %v", relPath, err)
		}
	}

	return backupDir, nil
}

// ModifiedFilesWarningModalClient implements ModalDialogClient to warn the
// user about resource files they have modified that will be overwritten.
type ModifiedFilesWarningModalClient struct {
	files  []string
	choice userSyncChoice
	done   bool
}

func (m *ModifiedFilesWarningModalClient) Title() string {
	return "Modified Resource Files Detected"
}

func (m *ModifiedFilesWarningModalClient) Opening() {}

func (m *ModifiedFilesWarningModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		{
			text: "Back Up and Update",
			action: func() bool {
				m.choice = syncBackupAndContinue
				m.done = true
				return true
			},
		},
		{
			text: "Overwrite All",
			action: func() bool {
				m.choice = syncOverwriteAll
				m.done = true
				return true
			},
		},
		{
			text: "Quit",
			action: func() bool {
				m.choice = syncQuit
				m.done = true
				return true
			},
		},
	}
}

func (m *ModifiedFilesWarningModalClient) Draw() int {
	imgui.Text("The following resource files have been modified locally and will be\noverwritten by the update:\n")
	for _, f := range m.files {
		imgui.BulletText(f)
	}
	imgui.Text("")
	return -1
}

// BackupErrorModalClient is shown when backing up modified files fails.
type BackupErrorModalClient struct {
	errMsg string
	choice userSyncChoice
	done   bool
}

func (b *BackupErrorModalClient) Title() string {
	return "Backup Failed"
}

func (b *BackupErrorModalClient) Opening() {}

func (b *BackupErrorModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		{
			text: "Overwrite All",
			action: func() bool {
				b.choice = syncOverwriteAll
				b.done = true
				return true
			},
		},
		{
			text: "Quit",
			action: func() bool {
				b.choice = syncQuit
				b.done = true
				return true
			},
		},
	}
}

func (b *BackupErrorModalClient) Draw() int {
	imgui.Text("Failed to back up modified files:\n")
	imgui.Text(b.errMsg)
	imgui.Text("")
	return -1
}

// showModifiedFilesWarning shows the modified-files warning dialog and
// returns the user's choice.
func showModifiedFilesWarning(plat platform.Platform, files []string) userSyncChoice {
	client := &ModifiedFilesWarningModalClient{files: files}
	d := NewModalDialogBox(client, plat)
	runModalEventLoop(plat, d, func() bool { return client.done })
	return client.choice
}

// showBackupErrorWarning shows the backup error dialog and returns the
// user's choice.
func showBackupErrorWarning(plat platform.Platform, errMsg string) userSyncChoice {
	client := &BackupErrorModalClient{errMsg: errMsg}
	d := NewModalDialogBox(client, plat)
	runModalEventLoop(plat, d, func() bool { return client.done })
	return client.choice
}

// promptModifiedFilesText is the CLI/text-mode equivalent of showModifiedFilesWarning.
// It prints the list of modified files and prompts the user for a choice.
func promptModifiedFilesText(files []string) userSyncChoice {
	fmt.Println("The following resource files have been modified locally and will be")
	fmt.Println("overwritten by the update:")
	for _, f := range files {
		fmt.Println("  - " + f)
	}
	fmt.Println()
	for {
		fmt.Print("[b]ack up and update / [o]verwrite all / [q]uit? ")
		var input string
		fmt.Scanln(&input)
		switch strings.ToLower(strings.TrimSpace(input)) {
		case "b":
			return syncBackupAndContinue
		case "o":
			return syncOverwriteAll
		case "q":
			return syncQuit
		}
	}
}

// removeStaleResourcesFiles removes resource files that are no longer needed.
// When oldManifest is non-nil, only files that were in the old manifest but not
// the new one are deleted — user-added files (not in either manifest) are left
// alone. When oldManifest is nil (first install), all files not in newManifest
// are deleted (original behavior).
func removeStaleResourcesFiles(resourcesDir string, newManifest, oldManifest map[string]manifestEntry) {
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
		slashPath := filepath.ToSlash(relPath)

		if _, inNew := newManifest[slashPath]; inNew {
			return nil // still needed
		}

		if oldManifest != nil {
			// Only delete files that were in the old manifest (i.e. managed by vice).
			// User-added files (not in either manifest) are preserved.
			if _, inOld := oldManifest[slashPath]; !inOld {
				return nil
			}
		}

		os.Remove(path)

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

	// Wrap reader to report progress during download
	pr := &progressReader{
		reader:     resp.Body,
		filename:   filename,
		progressCh: progressCh,
	}

	hasher := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, hasher), pr)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	// The file is closed before either os.Remove below since Windows won't
	// delete a file that is still open; leaving a partial one behind means
	// the next run finds it and takes it for a good download.
	if err != nil {
		os.Remove(fullPath)
		return fmt.Errorf("%s: failed to write: %w", filename, err)
	}

	if got := hex.EncodeToString(hasher.Sum(nil)); got != hash {
		os.Remove(fullPath)
		return fmt.Errorf("%s: download hash mismatch (got %s, want %s)", filename, got, hash)
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

	// Migrate from lowercase "vice" to "Vice" for case-sensitive filesystems.
	oldDir := filepath.Join(configDir, "vice")
	newDir := filepath.Join(configDir, "Vice")
	oldInfo, oldErr := os.Stat(oldDir)
	newInfo, newErr := os.Stat(newDir)
	// On case-insensitive filesystems (macOS), both paths resolve to the same
	// directory. os.SameFile detects this so we skip the migration entirely.
	sameFile := oldErr == nil && newErr == nil && os.SameFile(oldInfo, newInfo)
	if oldErr == nil && oldInfo.IsDir() && !sameFile {
		if os.IsNotExist(newErr) {
			// Only the old lowercase directory exists; rename it.
			os.Rename(oldDir, newDir)
		} else if newErr == nil {
			// Both exist (unusual). Move resources if needed, then remove the old dir.
			oldRes := filepath.Join(oldDir, "resources")
			newRes := filepath.Join(newDir, "resources")
			if _, err := os.Stat(oldRes); err == nil {
				if _, err := os.Stat(newRes); os.IsNotExist(err) {
					os.Rename(oldRes, newRes)
				}
			}
			os.RemoveAll(oldDir)
		}
	}

	resourcesDir := filepath.Join(configDir, "Vice", "resources")
	manifestPath := filepath.Join(resourcesDir, "manifest.json")

	var manifest map[string]manifestEntry
	if err := json.Unmarshal([]byte(resourcesManifest), &manifest); err != nil {
		return fmt.Errorf("failed to unmarshal resources manifest: %v", err)
	}

	// Check if manifest is up to date and all files exist
	if checkManifestUpToDate(manifestPath) && validateAllResourcesExist(resourcesDir, manifest) {
		return nil
	}

	// Read the old local manifest (from the previous sync) for three-way
	// comparison. We intentionally leave the old manifest on disk so that
	// if the user quits, the warning reappears on next launch. It is
	// overwritten by writeManifestFile at the end of a successful sync.
	var oldManifest map[string]manifestEntry
	if oldData, err := os.ReadFile(manifestPath); err == nil {
		json.Unmarshal(oldData, &oldManifest) // ignore errors; treat as nil
	}

	// Check for user-modified files and warn before overwriting.
	if oldManifest != nil {
		modifiedFiles := findUserModifiedFiles(resourcesDir, oldManifest, manifest)
		if len(modifiedFiles) > 0 {
			var choice userSyncChoice
			if plat != nil {
				choice = showModifiedFilesWarning(plat, modifiedFiles)
			} else {
				choice = promptModifiedFilesText(modifiedFiles)
			}

			switch choice {
			case syncQuit:
				os.Exit(0)
			case syncBackupAndContinue:
				backupDir, err := backupModifiedFiles(resourcesDir, modifiedFiles)
				if err != nil {
					if plat != nil {
						errChoice := showBackupErrorWarning(plat, err.Error())
						if errChoice == syncQuit {
							os.Exit(0)
						}
					} else {
						fmt.Printf("Backup failed: %v\n", err)
						fmt.Print("[o]verwrite all / [q]uit? ")
						var input string
						fmt.Scanln(&input)
						if strings.ToLower(strings.TrimSpace(input)) == "q" {
							os.Exit(0)
						}
					}
				} else if plat == nil {
					fmt.Printf("Modified files backed up to: %s\n", backupDir)
				}
			case syncOverwriteAll:
				// proceed
			}
		}
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

	removeStaleResourcesFiles(resourcesDir, manifest, oldManifest)

	// Only now do we write the current manifest.json to reflect that we are good to go.
	return writeManifestFile(manifestPath)
}
