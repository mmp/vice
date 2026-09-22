// util/resources_sync_download.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Downloading of the resource files into the local cache, for builds that
// fetch them from cloud storage rather than reading them from the source tree.
//go:build release

package util

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

	"golang.org/x/sync/errgroup"
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

// SyncResources makes the local resources directory match the manifest this
// build was made with, downloading whatever is missing or out of date. It
// reports progress and asks about locally-modified files via ui.
func SyncResources(ui SyncUI) error {
	if resourcesManifest == "" {
		return fmt.Errorf("manifest.json was not present during build")
	}

	if err := migrateConfigDirCase(); err != nil {
		return err
	}

	resourcesDir, err := configResourcesDir()
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(resourcesDir, "manifest.json")

	var manifest map[string]manifestEntry
	if err := json.Unmarshal([]byte(resourcesManifest), &manifest); err != nil {
		return fmt.Errorf("failed to unmarshal resources manifest: %w", err)
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

	if oldManifest != nil {
		if err := handleModifiedFiles(ui, resourcesDir, oldManifest, manifest); err != nil {
			return err
		}
	}

	if err := download(ui, resourcesDir, manifest); err != nil {
		return err
	}

	removeStaleResourcesFiles(resourcesDir, manifest, oldManifest)

	// Only now do we write the current manifest.json to reflect that we are good to go.
	return writeManifestFile(manifestPath)
}

// migrateConfigDirCase renames a lowercase "vice" configuration directory
// left by older versions to "Vice", for case-sensitive filesystems.
func migrateConfigDirCase() error {
	newDir, err := log.ConfigDir()
	if err != nil {
		return err
	}
	oldDir := filepath.Join(filepath.Dir(newDir), "vice")

	oldInfo, oldErr := os.Stat(oldDir)
	newInfo, newErr := os.Stat(newDir)
	// On case-insensitive filesystems (macOS), both paths resolve to the same
	// directory. os.SameFile detects this so we skip the migration entirely.
	sameFile := oldErr == nil && newErr == nil && os.SameFile(oldInfo, newInfo)
	if oldErr != nil || !oldInfo.IsDir() || sameFile {
		return nil
	}

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
	return nil
}

// handleModifiedFiles warns about resource files the user has edited that
// the sync is about to overwrite and acts on their choice.
func handleModifiedFiles(ui SyncUI, resourcesDir string, oldManifest, newManifest map[string]manifestEntry) error {
	modified := findUserModifiedFiles(resourcesDir, oldManifest, newManifest)
	if len(modified) == 0 {
		return nil
	}

	switch ui.PromptModifiedFiles(modified) {
	case SyncQuit:
		return ErrSyncCanceled

	case SyncBackupAndContinue:
		backupDir, err := backupModifiedFiles(resourcesDir, modified)
		if err != nil {
			if ui.BackupFailed(err) == SyncQuit {
				return ErrSyncCanceled
			}
		} else {
			ui.BackedUp(backupDir)
		}
	}
	return nil
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
	dir, err := log.ConfigDir()
	if err != nil {
		return "", err
	}

	timestamp := time.Now().Format("2006-01-02T150405")
	backupDir := filepath.Join(dir, "resource-backups", timestamp)

	for _, relPath := range files {
		src := filepath.Join(resourcesDir, relPath)
		dst := filepath.Join(backupDir, relPath)

		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return "", fmt.Errorf("failed to create backup directory for %s: %w", relPath, err)
		}

		srcFile, err := os.Open(src)
		if err != nil {
			return "", fmt.Errorf("failed to open %s for backup: %w", relPath, err)
		}

		dstFile, err := os.Create(dst)
		if err != nil {
			srcFile.Close()
			return "", fmt.Errorf("failed to create backup file %s: %w", relPath, err)
		}

		_, err = io.Copy(dstFile, srcFile)
		srcFile.Close()
		dstFile.Close()
		if err != nil {
			return "", fmt.Errorf("failed to copy %s to backup: %w", relPath, err)
		}
	}

	return backupDir, nil
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

// download runs the workers that bring the resources directory up to date,
// feeding ui progress updates until they have all finished.
func download(ui SyncUI, resourcesDir string, manifest map[string]manifestEntry) error {
	ws, totalBytes := launchWorkers(resourcesDir, manifest)

	st := SyncStatus{TotalFiles: len(manifest), TotalBytes: totalBytes}
	var completedBytes int64
	inProgress := make(map[string]int64)
	completed := make(map[string]bool)
	var downloadErr error

	// ui.Update draws a frame for a GUI implementation, which costs a vsync
	// interval; the workers block on the unbuffered completedCh, so only the
	// ticker case falls through to it. Everything else loops back around to
	// take the next message immediately.
	tick := time.NewTicker(16 * time.Millisecond)
	defer tick.Stop()

	for done := false; !done; {
		select {
		case <-ws.doneCh:
			done = true

		case fc := <-ws.completedCh:
			st.CompletedFiles++
			completedBytes += fc.size
			completed[fc.filename] = true
			delete(inProgress, fc.filename)
			if st.CurrentFile == fc.filename {
				st.CurrentFile = ""
			}
			continue

		case p := <-ws.progressCh:
			// Ignore late progress updates for files that have already completed
			if !completed[p.filename] {
				st.CurrentFile = p.filename
				inProgress[p.filename] = p.bytesWritten
			}
			continue

		case err := <-ws.errorsCh:
			downloadErr = err
			continue

		case <-tick.C:
		}

		st.DownloadedBytes = completedBytes
		for _, b := range inProgress {
			st.DownloadedBytes += b
		}
		ui.Update(st)
	}

	return downloadErr
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
	// that the caller can update the UI/report progress.)
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
