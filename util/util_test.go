// util/util_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"testing"

	"io/fs"
)

func TestObjectArena(t *testing.T) {
	var a ObjectArena[int]

	for range 10 {
		seen := make(map[*int]any)
		for i := range 100 {
			p := a.AllocClear()
			if _, ok := seen[p]; ok {
				t.Errorf("%p: pointer returned twice!", p)
			}
			seen[p] = nil

			if *p != 0 {
				t.Errorf("%p = %d, expected 0", p, *p)
			}
			*p = i
		}

		if a.Cap() > 200 {
			t.Errorf("Capacity growing too fast: now %d", a.Cap())
		}

		a.Reset()
	}
}

func TestFindDuplicateJSONKeys(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected []DuplicateJSONKey
	}{
		{
			name:     "no duplicates",
			json:     `{"a": 1, "b": 2, "c": 3}`,
			expected: nil,
		},
		{
			name: "simple duplicate at root",
			json: `{"a": 1, "b": 2, "a": 3}`,
			expected: []DuplicateJSONKey{
				{Path: "", Key: "a"},
			},
		},
		{
			name: "duplicate in nested object",
			json: `{"outer": {"inner": 1, "inner": 2}}`,
			expected: []DuplicateJSONKey{
				{Path: "outer", Key: "inner"},
			},
		},
		{
			name: "multiple duplicates at different levels",
			json: `{"a": 1, "a": 2, "nested": {"b": 1, "b": 2}}`,
			expected: []DuplicateJSONKey{
				{Path: "", Key: "a"},
				{Path: "nested", Key: "b"},
			},
		},
		{
			name:     "array with objects no duplicates",
			json:     `{"items": [{"x": 1}, {"x": 2}]}`,
			expected: nil,
		},
		{
			name: "duplicate inside array element",
			json: `{"items": [{"x": 1, "x": 2}]}`,
			expected: []DuplicateJSONKey{
				{Path: "items", Key: "x"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindDuplicateJSONKeys([]byte(tt.json))

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d duplicates, got %d", len(tt.expected), len(result))
				return
			}

			for i, exp := range tt.expected {
				if result[i].Path != exp.Path || result[i].Key != exp.Key {
					t.Errorf("duplicate %d: expected {Path: %q, Key: %q}, got {Path: %q, Key: %q}",
						i, exp.Path, exp.Key, result[i].Path, result[i].Key)
				}
			}
		})
	}
}

// TestLoadResourceBytesDoesNotOverallocate guards the uncompressed path
// against going back through io.ReadAll, whose doubling buffer holds a second
// copy of the file and leaves slack in the result. That costs over a gigabyte
// of transient heap for the speech models.
func TestLoadResourceBytesDoesNotOverallocate(t *testing.T) {
	// Big enough that io.ReadAll's doubling overshoots by kilobytes;
	// a couple-KB file lands within a few bytes either way.
	const path = "airport_artccs.json"

	want, err := fs.Stat(GetResourcesFS(), path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	b := LoadResourceBytes(path)
	if int64(len(b)) != want.Size() {
		t.Errorf("%s: got %d bytes, want %d", path, len(b), want.Size())
	}
	// fs.ReadFile allocates one spare byte to see EOF; a growing buffer
	// overshoots by a fraction of the file size, which for a model is
	// tens of megabytes.
	if cap(b) > len(b)+64 {
		t.Errorf("%s: cap %d exceeds len %d; the file was copied through a growing buffer",
			path, cap(b), len(b))
	}
}
