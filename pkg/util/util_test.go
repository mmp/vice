// pkg/util/util_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"slices"
	"strings"
	"testing"
)

func TestWrapText(t *testing.T) {
	input := "this is a test_with_a_long_line of stuff"
	expected := "this is \n  a \n  test_with_a_long_line \n  of \n  stuff"
	wrap, lines := WrapText(input, 8, 2, false)
	if wrap != expected {
		t.Errorf("wrapping gave %q; expected %q", wrap, expected)
	}
	if lines != 5 {
		t.Errorf("wrapping returned %d lines, expected 5", lines)
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
