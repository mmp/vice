// util/resources_sync.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// SyncChoice is how the user chose to proceed when a resource sync would
// overwrite resource files they have modified locally.
type SyncChoice int

const (
	SyncBackupAndContinue SyncChoice = iota
	SyncOverwriteAll
	SyncQuit
)

// ErrSyncCanceled is returned by Sync when the user chose to quit rather
// than let the sync continue.
var ErrSyncCanceled = errors.New("resource sync canceled")

// SyncStatus is a snapshot of the progress of an in-flight resource sync.
type SyncStatus struct {
	CompletedFiles  int
	TotalFiles      int
	DownloadedBytes int64
	TotalBytes      int64
	CurrentFile     string
}

// SyncUI presents the progress of a resource sync and asks the user how to
// proceed when the sync would overwrite locally-modified files.
type SyncUI interface {
	// PromptModifiedFiles reports the resource files the user has modified
	// locally and returns how they want to proceed.
	PromptModifiedFiles(files []string) SyncChoice

	// BackupFailed reports that backing up those files failed and returns
	// whether to continue anyway.
	BackupFailed(err error) SyncChoice

	// BackedUp reports the directory the modified files were copied to.
	BackedUp(dir string)

	// Update is called repeatedly while files are downloading, both when
	// the status changes and on a timer so that a GUI implementation has a
	// place to render a frame.
	Update(st SyncStatus)
}

// TextSyncUI implements SyncUI for programs without a GUI: it prints
// progress to stdout and prompts on stdin.
type TextSyncUI struct {
	lastReported int
	lastWidth    int
	lastTenth    int
}

func (t *TextSyncUI) PromptModifiedFiles(files []string) SyncChoice {
	fmt.Println("The following resource files have been modified locally and will be")
	fmt.Println("overwritten by the update:")
	for _, f := range files {
		fmt.Println("  - " + f)
	}
	fmt.Println()
	for {
		input, ok := prompt("[b]ack up and update / [o]verwrite all / [q]uit? ")
		if !ok {
			return SyncQuit
		}
		switch input {
		case "b":
			return SyncBackupAndContinue
		case "o":
			return SyncOverwriteAll
		case "q":
			return SyncQuit
		}
	}
}

func (t *TextSyncUI) BackupFailed(err error) SyncChoice {
	fmt.Printf("Backup failed: %v\n", err)
	for {
		input, ok := prompt("[o]verwrite all / [q]uit? ")
		if !ok {
			return SyncQuit
		}
		switch input {
		case "o":
			return SyncOverwriteAll
		case "q":
			return SyncQuit
		}
	}
}

func (t *TextSyncUI) BackedUp(dir string) {
	fmt.Printf("Modified files backed up to: %s\n", dir)
}

func (t *TextSyncUI) Update(st SyncStatus) {
	if st.CompletedFiles == t.lastReported {
		return
	}
	t.lastReported = st.CompletedFiles

	// The counts include files that were already present and up to date, not
	// just the ones that had to be downloaded.
	const mb = 1024 * 1024
	msg := fmt.Sprintf("Synced %d of %d resource files (%.1f of %.1f MB)", st.CompletedFiles,
		st.TotalFiles, float64(st.DownloadedBytes)/mb, float64(st.TotalBytes)/mb)

	if !stdoutIsTerminal() {
		// Rewriting the line would run the whole sync into one enormous line
		// in a file or a service's log; report each further tenth instead.
		if tenth := 10 * st.CompletedFiles / max(st.TotalFiles, 1); tenth != t.lastTenth {
			t.lastTenth = tenth
			fmt.Println(msg)
		}
		return
	}

	// Pad to the previous message's width so that a shorter one doesn't leave
	// the tail of its predecessor behind. The carriage return is an ordinary
	// console control character everywhere, Windows included, unlike the
	// escape sequence that would erase the line for us.
	fmt.Printf("\r%-*s", t.lastWidth, msg)
	t.lastWidth = len(msg)

	if st.CompletedFiles == st.TotalFiles {
		fmt.Println()
	}
}

// prompt asks the user a question and returns their answer, reporting
// ok==false if there is no one to answer: stdin at EOF means the program was
// launched without a console or with its input redirected, and repeating the
// question would spin forever. (An empty line gives "unexpected newline"
// rather than io.EOF, so a bare Enter still re-prompts.)
// stdoutIsTerminal reports whether stdout is a console rather than a file or
// a pipe, which decides whether progress can be rewritten in place.
func stdoutIsTerminal() bool {
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func prompt(msg string) (string, bool) {
	fmt.Print(msg)
	var input string
	if _, err := fmt.Scanln(&input); errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, "\nstdin is not available to answer; giving up on the resource sync.")
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(input)), true
}
