// cmd/updatesay/output.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"

	"github.com/mmp/vice/util"
)

// LoadSayFile loads a say*.json file and returns the pronunciation map.
func LoadSayFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// SaveSayFile saves a pronunciation map to a say*.json file, sorted alphabetically.
// Uses atomic write (temp file + rename) to be robust against interruption.
func SaveSayFile(path string, data map[string]string) error {
	// Build ordered JSON manually to preserve key order.
	// json.Marshal with a map doesn't preserve order.
	result := "{\n"
	i := 0
	for k, v := range util.SortedMap(data) {
		keyJSON, _ := json.Marshal(k)
		valJSON, _ := json.Marshal(v)
		result += "  " + string(keyJSON) + ": " + string(valJSON)
		if i < len(data)-1 {
			result += ","
		}
		result += "\n"
		i++
	}
	result += "}\n"

	// Atomic write: write to temp file in same directory, then rename
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".sayfile-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()

	// Clean up temp file on any error
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.WriteString(result); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}

	success = true
	return nil
}

// MergeSayData merges new pronunciations into existing ones.
// Returns the merged map and the count of new entries added.
func MergeSayData(existing, new map[string]string) (map[string]string, int) {
	result := make(map[string]string, len(existing)+len(new))

	// Copy existing
	maps.Copy(result, existing)

	// Add new (only if not already present)
	count := 0
	for k, v := range new {
		if _, exists := result[k]; !exists {
			result[k] = v
			count++
		}
	}

	return result, count
}

// FindMissing returns a sorted list of items in extracted that are not in existing.
func FindMissing(extracted map[string]struct{}, existing map[string]string) []string {
	var missing []string
	for name := range util.SortedMap(extracted) {
		if _, exists := existing[name]; !exists {
			missing = append(missing, name)
		}
	}
	return missing
}
