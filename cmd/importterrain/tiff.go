// cmd/importterrain/tiff.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/mmp/vice/math"
)

// A GMTED2010 7.5 arc-second tile has 480 cells per degree.
const (
	gmtedCols = 30 * 480
	gmtedRows = 20 * 480
)

// readGMTED returns the elevations in meters of the GMTED2010 tile at path,
// row by row from its north edge. golang.org/x/image/tiff doesn't read
// signed samples, so this reads the one layout the tiles have: a
// little-endian TIFF holding a single band of uncompressed int16 samples in
// strips of whole rows. It checks that the tile is the one given, with its
// corner within half a cell of a whole degree.
func readGMTED(path string, t gmtedTile) ([]int16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	if len(b) < 8 || string(b[:4]) != "II*\x00" {
		return nil, fmt.Errorf("%s: not a little-endian TIFF", path)
	}
	slice := func(off, n uint64) ([]byte, error) {
		if off+n > uint64(len(b)) {
			return nil, fmt.Errorf("%s: truncated", path)
		}
		return b[off : off+n], nil
	}

	ifd := uint64(le.Uint32(b[4:]))
	nb, err := slice(ifd, 2)
	if err != nil {
		return nil, err
	}
	entries, err := slice(ifd+2, 12*uint64(le.Uint16(nb)))
	if err != nil {
		return nil, err
	}
	ints := make(map[uint16][]uint64)
	floats := make(map[uint16][]float32)
	for e := entries; len(e) > 0; e = e[12:] {
		tag, typ, count := le.Uint16(e), le.Uint16(e[2:]), uint64(le.Uint32(e[4:]))
		size := map[uint16]uint64{3: 2, 4: 4, 12: 8}[typ] // SHORT, LONG, DOUBLE
		if size == 0 {
			continue // none of the fields read below
		}
		v := e[8:12]
		if size*count > 4 {
			if v, err = slice(uint64(le.Uint32(e[8:])), size*count); err != nil {
				return nil, err
			}
		}
		for i := range count {
			switch typ {
			case 3:
				ints[tag] = append(ints[tag], uint64(le.Uint16(v[2*i:])))
			case 4:
				ints[tag] = append(ints[tag], uint64(le.Uint32(v[4*i:])))
			case 12:
				var f float64
				binary.Decode(v[8*i:], le, &f)
				floats[tag] = append(floats[tag], float32(f))
			}
		}
	}

	// field returns a field that holds one value, def if it is absent, or
	// zero, which none of the fields checked may be, if it holds several.
	field := func(tag uint16, def uint64) uint64 {
		if v := ints[tag]; len(v) == 1 {
			return v[0]
		} else if len(v) == 0 {
			return def
		}
		return 0
	}
	if field(256, 0) != gmtedCols || field(257, 0) != gmtedRows || // ImageWidth, ImageLength
		field(258, 0) != 16 || field(277, 1) != 1 || // BitsPerSample, SamplesPerPixel
		field(259, 1) != 1 || field(339, 1) != 2 { // Compression, SampleFormat
		return nil, fmt.Errorf("%s: not a %d x %d tile of uncompressed int16 samples", path, gmtedCols, gmtedRows)
	}

	// ModelPixelScale and ModelTiepoint, which ties the corner of the first
	// row and column to its longitude and latitude.
	scale, tie := floats[33550], floats[33922]
	const cell = 1. / 480
	near := func(a, b, tolerance float32) bool { return math.Abs(a-b) < tolerance }
	if len(scale) < 2 || len(tie) < 6 || !near(scale[0], cell, 1e-6) || !near(scale[1], cell, 1e-6) ||
		tie[0] != 0 || tie[1] != 0 || !near(tie[3], float32(t.lon), cell/2) || !near(tie[4], float32(t.lat+20), cell/2) {
		return nil, fmt.Errorf("%s: not the %s tile; scale %v, tie point %v", path, t.name(), scale, tie)
	}

	offsets, counts := ints[273], ints[279] // StripOffsets, StripByteCounts
	if len(offsets) != len(counts) {
		return nil, fmt.Errorf("%s: %d strip offsets but %d byte counts", path, len(offsets), len(counts))
	}
	elev := make([]int16, 0, gmtedCols*gmtedRows)
	for i, off := range offsets {
		if counts[i]%(2*gmtedCols) != 0 {
			return nil, fmt.Errorf("%s: strip %d is not whole rows", path, i)
		}
		strip, err := slice(off, counts[i])
		if err != nil {
			return nil, err
		}
		for j := 0; j < len(strip); j += 2 {
			elev = append(elev, int16(le.Uint16(strip[j:])))
		}
	}
	if len(elev) != gmtedCols*gmtedRows {
		return nil, fmt.Errorf("%s: strips hold %d samples, expected %d", path, len(elev), gmtedCols*gmtedRows)
	}
	return elev, nil
}
