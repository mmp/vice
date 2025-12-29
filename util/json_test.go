// util/json_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"testing"
)

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
