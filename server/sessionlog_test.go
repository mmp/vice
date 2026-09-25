// server/sessionlog_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/simlog"
)

// A local server keeps the most recent logs and the databases they, and the
// session about to start, run on.
func TestPruneSessionLogs(t *testing.T) {
	dir := t.TempDir()
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := os.MkdirAll(filepath.Join(dir, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"old", "kept", "current", "unused"} {
		if err := os.WriteFile(simlog.DatabasePath(dir, hash), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 6 {
		hash := "kept"
		if i < 2 {
			hash = "old"
		}
		path := filepath.Join(dir, fmt.Sprintf("2026-09-0%dT12-00-00Z_PHL_test.simlog", i+1))
		w, err := simlog.Create(path, simlog.Header{Database: hash}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}

	pruneSessionLogs(dir, 4, "current", lg)

	logs, _ := filepath.Glob(filepath.Join(dir, "*.simlog"))
	var names []string
	for _, l := range logs {
		names = append(names, filepath.Base(l)[:10])
	}
	if want := []string{"2026-09-03", "2026-09-04", "2026-09-05", "2026-09-06"}; !slices.Equal(names, want) {
		t.Errorf("kept logs %v, want %v", names, want)
	}

	databases, _ := filepath.Glob(simlog.DatabasePath(dir, "*"))
	var hashes []string
	for _, d := range databases {
		hashes = append(hashes, filepath.Base(d))
	}
	slices.Sort(hashes)
	if want := []string{"current.db", "kept.db"}; !slices.Equal(hashes, want) {
		t.Errorf("kept databases %v, want %v", hashes, want)
	}
}
