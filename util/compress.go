// pkg/util/compress.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"golang.org/x/exp/constraints"
)

func DeltaEncode[T constraints.Integer](d []T) []T {
	if len(d) == 0 {
		return nil
	}
	r := make([]T, len(d))

	var prev T
	for i, v := range d {
		delta := v - prev
		r[i] = delta
		prev = v
	}
	return r
}

func DeltaDecode[T constraints.Integer](d []T) []T {
	if len(d) == 0 {
		return nil
	}
	r := make([]T, len(d))

	var prev T
	for i, delta := range d {
		r[i] = prev + delta
		prev = r[i]
	}

	return r
}

func DeltaEncodeBytes(ref, next []byte) []byte {
	if len(next) == 0 {
		return nil
	}

	delta := make([]byte, len(next))
	for i := range next {
		if i < len(ref) {
			delta[i] = next[i] - ref[i]
		} else {
			delta[i] = next[i]
		}
	}
	return delta
}

func DeltaDecodeBytes(ref, delta []byte) []byte {
	if len(delta) == 0 {
		return nil
	}

	r := make([]byte, len(delta))
	for i := range delta {
		if i < len(ref) {
			r[i] = ref[i] + delta[i]
		} else {
			r[i] = delta[i]
		}
	}
	return r
}

func DeltaEncodeBytesSlice(data [][]byte) [][]byte {
	if len(data) == 0 {
		return nil
	}

	r := make([][]byte, len(data))
	r[0] = append([]byte(nil), data[0]...)

	for i := 1; i < len(data); i++ {
		r[i] = DeltaEncodeBytes(data[i-1], data[i])
	}

	return r
}

func DeltaDecodeBytesSlice(encoded [][]byte) [][]byte {
	if len(encoded) == 0 {
		return nil
	}

	r := make([][]byte, len(encoded))
	r[0] = append([]byte(nil), encoded[0]...)

	for i := 1; i < len(encoded); i++ {
		r[i] = DeltaDecodeBytes(r[i-1], encoded[i])
	}

	return r
}
