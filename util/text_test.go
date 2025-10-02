// util/text_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTransposeStrings(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{
			name:  "simple 2x2",
			input: []string{"ab", "cd"},
			want:  []string{"ac", "bd"},
		},
		{
			name:  "3x3 matrix",
			input: []string{"abc", "def", "ghi"},
			want:  []string{"adg", "beh", "cfi"},
		},
		{
			name:  "single string",
			input: []string{"hello"},
			want:  []string{"h", "e", "l", "l", "o"},
		},
		{
			name:  "empty input",
			input: []string{},
			want:  nil,
		},
		{
			name:    "unequal lengths",
			input:   []string{"abc", "de"},
			want:    nil,
			wantErr: true,
		},
		{
			name:  "single character strings",
			input: []string{"a", "b", "c"},
			want:  []string{"abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TransposeStrings(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("TransposeStrings() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TransposeStrings() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWrapText(t *testing.T) {
	input := "this is a test_with_a_long_line of stuff"
	expected := "this is \n  a \n  test_with_a_long_line \n  of \n  stuff"
	wrap, lines := WrapText(input, 8, 2, false, false)
	if wrap != expected {
		t.Errorf("wrapping gave %q; expected %q", wrap, expected)
	}
	if lines != 5 {
		t.Errorf("wrapping returned %d lines, expected 5", lines)
	}
}

func TestWrapNoSpace(t *testing.T) {
	// Test breaking mid-word when WrapNoSpace is true
	input := "supercalifragilisticexpialidocious"
	wrap, lines := WrapText(input, 10, 2, false, true)
	expected := "supercalif\n  ragilist\n  icexpial\n  idocious"
	if wrap != expected {
		t.Errorf("WrapNoSpace gave %q; expected %q", wrap, expected)
	}
	if lines != 4 {
		t.Errorf("WrapNoSpace returned %d lines, expected 4", lines)
	}

	// Test that without WrapNoSpace, long words overflow
	wrap2, lines2 := WrapText(input, 10, 2, false, false)
	if wrap2 != input {
		t.Errorf("Without WrapNoSpace gave %q; expected %q", wrap2, input)
	}
	if lines2 != 1 {
		t.Errorf("Without WrapNoSpace returned %d lines, expected 1", lines2)
	}
}

func TestStopShouting(t *testing.T) {
	input := "UNITED AIRLINES (North America)"
	expected := "United Airlines (North America)"
	ss := StopShouting(input)
	if ss != expected {
		t.Errorf("Got %q, expected %q", ss, expected)
	}
}

func TestHash(t *testing.T) {
	h, err := Hash(strings.NewReader("hello world"))
	if err != nil {
		t.Errorf("hash error: %v", err)
	}
	if !slices.Equal(h, []byte{0xb9, 0x4d, 0x27, 0xb9, 0x93, 0x4d, 0x3e, 0x08, 0xa5, 0x2e, 0x52, 0xd7, 0xda, 0x7d, 0xab,
		0xfa, 0xc4, 0x84, 0xef, 0xe3, 0x7a, 0x53, 0x80, 0xee, 0x90, 0x88, 0xf7, 0xac, 0xe2, 0xef, 0xcd, 0xe9}) {
		t.Errorf("hash mismatch")
	}
}

func TestEditDistance(t *testing.T) {
	type test struct {
		input     string
		options   []string
		expected1 []string
		expected2 []string
	}
	options := []string{"hello", "house", "mouse", "Hallow", "blunt", "blouse", "mousse", "halo"}
	tests := []test{
		{input: "hallo", options: options, expected1: []string{"hello", "halo"}, expected2: []string{"Hallow"}},
		{input: "houses", options: options, expected1: []string{"house"}, expected2: []string{"mouse", "blouse"}},
		{input: "monitor", options: options, expected1: nil, expected2: nil},
	}
	for tc := range slices.Values(tests) {
		d1, d2 := SelectInTwoEdits(tc.input, slices.Values(tc.options), nil, nil)
		if !slices.Equal(d1, tc.expected1) {
			t.Errorf("for %q 1 edit expected %v, got %v", tc.input, tc.expected1, d1)
		}
		if !slices.Equal(d2, tc.expected2) {
			t.Errorf("for %q 2 edit expected %v, got %v", tc.input, tc.expected2, d2)
		}
	}
}
