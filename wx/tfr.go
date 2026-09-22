// wx/tfr.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"io"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

// TFRFilename is the standard filename for the consolidated TFR file.
const TFRFilename = "TFRs.msgpack.zst"

// LoadCompressedTFRs reads msgpack+zstd compressed TFRs from r.
func LoadCompressedTFRs(r io.Reader) ([]av.TFR, error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var tfrs []av.TFR
	if err := msgpack.NewDecoder(zr).Decode(&tfrs); err != nil {
		return nil, err
	}
	return tfrs, nil
}

// SaveCompressedTFRs writes the TFRs as msgpack+zstd to w.
func SaveCompressedTFRs(tfrs []av.TFR, w io.Writer) error {
	zw, err := zstd.NewWriter(w)
	if err != nil {
		return err
	}
	if err := msgpack.NewEncoder(zw).Encode(tfrs); err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}

// GetTFRsForARTCC returns all TFRs matching the given ARTCC that are active
// at time t (Effective <= t < Expire).
func GetTFRsForARTCC(tfrs []av.TFR, artcc string, t time.Time) []av.TFR {
	var result []av.TFR
	for _, tfr := range tfrs {
		if tfr.ARTCC == artcc && tfr.ActiveAt(t) {
			result = append(result, tfr)
		}
	}
	return result
}

// GetTFRsForTRACON returns TFRs matching the parent ARTCC that are active at
// time t and have at least one vertex within rangeNm of center.
func GetTFRsForTRACON(tfrs []av.TFR, artcc string, center math.Point2LL, rangeNm float32, t time.Time) []av.TFR {
	var result []av.TFR
	for _, tfr := range tfrs {
		if tfr.ARTCC != artcc || !tfr.ActiveAt(t) {
			continue
		}
		if tfr.NearPoint(center, rangeNm) {
			result = append(result, tfr)
		}
	}
	return result
}
