// pkg/util/compress_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"slices"
	"testing"
)

func TestDeltaEncodeDecode(t *testing.T) {
	tests := []struct {
		name  string
		input []int
	}{
		{
			name:  "empty",
			input: []int{},
		},
		{
			name:  "single int",
			input: []int{42},
		},
		{
			name:  "ascending sequence",
			input: []int{0, 1, 2, 3, 4, 5},
		},
		{
			name:  "constant values",
			input: []int{10, 10, 10, 10},
		},
		{
			name:  "random values",
			input: []int{100, 50, 75, 200, 150},
		},
		{
			name:  "wrapping values",
			input: []int{250, 255, 5, 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := DeltaEncode(tt.input)
			decoded := DeltaDecode(encoded)

			if !slices.Equal(decoded, tt.input) {
				t.Errorf("DeltaDecode(DeltaEncode(%v)) = %v, want %v", tt.input, decoded, tt.input)
			}
		})
	}
}

func TestDeltaEncodeNil(t *testing.T) {
	if got := DeltaEncode[int](nil); got != nil {
		t.Errorf("DeltaEncode(nil) = %v, want nil", got)
	}
}

func TestDeltaDecodeNil(t *testing.T) {
	if got := DeltaDecode[int](nil); got != nil {
		t.Errorf("DeltaDecode(nil) = %v, want nil", got)
	}
}

func TestDeltaEncodeDecodeBytes(t *testing.T) {
	tests := []struct {
		name      string
		reference []byte
		next      []byte
	}{
		{
			name:      "empty next",
			reference: []byte("hello"),
			next:      []byte{},
		},
		{
			name:      "identical strings",
			reference: []byte("hello"),
			next:      []byte("hello"),
		},
		{
			name:      "one char difference",
			reference: []byte("hello"),
			next:      []byte("hallo"),
		},
		{
			name:      "next longer",
			reference: []byte("hello"),
			next:      []byte("hello world"),
		},
		{
			name:      "next shorter",
			reference: []byte("hello world"),
			next:      []byte("hello"),
		},
		{
			name:      "completely different",
			reference: []byte("abc"),
			next:      []byte("xyz"),
		},
		{
			name:      "empty reference",
			reference: []byte{},
			next:      []byte("hello"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delta := DeltaEncodeBytes(tt.reference, tt.next)
			decoded := DeltaDecodeBytes(tt.reference, delta)

			if !slices.Equal(decoded, tt.next) {
				t.Errorf("DeltaDecodeBytes(%v, DeltaEncodeBytes(%v, %v)) = %v, want %v",
					tt.reference, tt.reference, tt.next, decoded, tt.next)
			}

			if len(tt.next) > 0 && len(delta) != len(tt.next) {
				t.Errorf("delta length = %d, want %d", len(delta), len(tt.next))
			}

			for i := 0; i < len(delta) && i < len(tt.reference) && i < len(tt.next); i++ {
				if tt.reference[i] == tt.next[i] && delta[i] != 0 {
					t.Errorf("delta[%d] = %d, want 0 for matching chars", i, delta[i])
				}
			}
		})
	}
}

func TestDeltaEncodeDecodeBytesSlice(t *testing.T) {
	tests := []struct {
		name string
		data [][]byte
	}{
		{
			name: "empty",
			data: [][]byte{},
		},
		{
			name: "single element",
			data: [][]byte{[]byte("hello")},
		},
		{
			name: "identical elements",
			data: [][]byte{
				[]byte("test"),
				[]byte("test"),
				[]byte("test"),
			},
		},
		{
			name: "incremental changes",
			data: [][]byte{
				[]byte("hello"),
				[]byte("hallo"),
				[]byte("hullo"),
			},
		},
		{
			name: "varying lengths",
			data: [][]byte{
				[]byte("hi"),
				[]byte("hello"),
				[]byte("h"),
				[]byte("hello world"),
			},
		},
		{
			name: "with empty slices",
			data: [][]byte{
				[]byte("start"),
				{},
				[]byte("end"),
			},
		},
		{
			name: "completely different",
			data: [][]byte{
				[]byte("abc"),
				[]byte("xyz"),
				[]byte("123"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := DeltaEncodeBytesSlice(tt.data)
			decoded := DeltaDecodeBytesSlice(encoded)

			if len(decoded) != len(tt.data) {
				t.Errorf("decoded length = %d, want %d", len(decoded), len(tt.data))
			}

			for i := range tt.data {
				if !slices.Equal(decoded[i], tt.data[i]) {
					t.Errorf("decoded[%d] = %v, want %v", i, decoded[i], tt.data[i])
				}
			}

			if len(encoded) > 0 {
				if !slices.Equal(encoded[0], tt.data[0]) {
					t.Errorf("first element should be unchanged: got %v, want %v", encoded[0], tt.data[0])
				}

				for i := 1; i < len(encoded); i++ {
					if len(encoded[i]) > 0 && len(tt.data[i-1]) > 0 && len(tt.data[i]) > 0 {
						for j := 0; j < len(encoded[i]) && j < len(tt.data[i-1]) && j < len(tt.data[i]); j++ {
							if tt.data[i-1][j] == tt.data[i][j] && encoded[i][j] != 0 {
								t.Errorf("encoded[%d][%d] = %d, want 0 for matching bytes", i, j, encoded[i][j])
							}
						}
					}
				}
			}
		})
	}
}
