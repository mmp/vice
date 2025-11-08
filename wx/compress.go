// wx/compress.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/mmp/vice/util"
)

// CompressInt64Timestamps delta-encodes and flate-compresses a slice of int64 timestamps.
// Returns the compressed bytes suitable for storage.
func CompressInt64Timestamps(timestamps []int64) ([]byte, error) {
	// Delta-encode the timestamps
	deltaEncoded := util.DeltaEncode(timestamps)

	// Compress with flate
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return nil, err
	}

	// Write the delta-encoded timestamps using binary encoding
	if err := binary.Write(fw, binary.LittleEndian, deltaEncoded); err != nil {
		return nil, err
	}

	if err := fw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// DecompressInt64Timestamps decompresses flate-compressed, delta-encoded int64 timestamps.
// Returns the original int64 timestamps.
func DecompressInt64Timestamps(compressed []byte) ([]int64, error) {
	// Decompress with flate
	fr := flate.NewReader(bytes.NewReader(compressed))
	defer fr.Close()

	// Read all decompressed data first to determine length
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, fr); err != nil {
		return nil, err
	}

	data := buf.Bytes()
	if len(data)%8 != 0 {
		return nil, fmt.Errorf("invalid decompressed data length: %d (expected multiple of 8)", len(data))
	}

	// Read int64 slice using binary encoding
	numInts := len(data) / 8
	deltaEncoded := make([]int64, numInts)
	reader := bytes.NewReader(data)
	if err := binary.Read(reader, binary.LittleEndian, deltaEncoded); err != nil {
		return nil, err
	}

	// Delta-decode
	timestamps := util.DeltaDecode(deltaEncoded)

	return timestamps, nil
}
