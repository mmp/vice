package eram

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const eramDirName = "eram"

// SetTemporaryCursor displays a cursor type for the given number of seconds.
// cursorType may be a numeric size (e.g., "5") or a cursor base name (e.g., "Eram5").
// Passing seconds == -1 keeps the cursor indefinitely; seconds <= 0 clears the override.
// If rollbackCursor is not empty, it will be set as the cursor after the temporary cursor expires.
func (ep *ERAMPane) SetTemporaryCursor(cursorType string, seconds float64, rollbackCursor string) {
	cursorType = strings.TrimSpace(cursorType)
	if cursorType == "" || seconds == 0 || seconds < -1 {
		ep.ClearTemporaryCursor()
		return
	}

	if size, err := strconv.Atoi(cursorType); err == nil {
		cursorType = fmt.Sprintf("Eram%d", size)
	}

	ep.cursorOverrideSelection = cursorType
	rollback := strings.TrimSpace(rollbackCursor)
	if rollback != "" {
		if size, err := strconv.Atoi(rollback); err == nil {
			rollback = fmt.Sprintf("Eram%d", size)
		}
		ep.cursorRollbackSelection = rollback
	} else {
		ep.cursorRollbackSelection = ""
	}
	if seconds == -1 {
		ep.cursorOverrideUntil = time.Time{}
	} else {
		ep.cursorOverrideUntil = time.Now().Add(time.Duration(seconds * float64(time.Second)))
	}
}

// ClearTemporaryCursor removes any temporary cursor override and rollback cursor.
func (ep *ERAMPane) ClearTemporaryCursor() {
	ep.cursorOverrideSelection = ""
	ep.cursorOverrideUntil = time.Time{}
	ep.cursorRollbackSelection = ""
}

func (ep *ERAMPane) cursorDirCandidates() []string {
	dirs := []string{filepath.Join(eramDirName, "cursors")}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		exeCursorDir := filepath.Join(exeDir, eramDirName, "cursors")
		if exeCursorDir != dirs[0] {
			dirs = append(dirs, exeCursorDir)
		}
	}
	return dirs
}

func (ep *ERAMPane) resolveCursorPath(selection string) (string, error) {
	name := strings.TrimSpace(selection)
	if name == "" {
		return "", nil
	}

	nameWithExt := name
	if filepath.Ext(nameWithExt) == "" {
		nameWithExt += ".cur"
	}

	if isPathLike(name) {
		if fileExists(name) {
			return name, nil
		}
		if nameWithExt != name && fileExists(nameWithExt) {
			return nameWithExt, nil
		}
	}

	for _, dir := range ep.cursorDirCandidates() {
		candidate := filepath.Join(dir, nameWithExt)
		if fileExists(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("cursor %q not found in %s", nameWithExt, strings.Join(ep.cursorDirCandidates(), ", "))
}

func (ep *ERAMPane) availableCursorNames() []string {
	names := map[string]struct{}{}
	for _, dir := range ep.cursorDirCandidates() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.EqualFold(filepath.Ext(name), ".cur") {
				base := strings.TrimSuffix(name, filepath.Ext(name))
				if base != "" {
					names[base] = struct{}{}
				}
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func isPathLike(name string) bool {
	if filepath.IsAbs(name) {
		return true
	}
	return strings.ContainsAny(name, string(os.PathSeparator)+"/")
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
