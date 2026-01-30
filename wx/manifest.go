// wx/manifest.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/klauspost/compress/zstd"
	"github.com/mmp/vice/util"
	"github.com/vmihailenco/msgpack/v5"
)

// ManifestFilename is the standard filename for int64-based compressed manifests
const ManifestFilename = "manifest-int64time.msgpack.zst"

// METARFilename is the standard filename for the consolidated METAR file
const METARFilename = "METAR-flate.msgpack.zst"

// RawManifest is the underlying storage format for manifests.
// It maps TRACON/airport identifiers to compressed, delta-encoded int64 Unix timestamps.
type RawManifest map[string][]byte

// Manifest represents a weather data manifest that maps TRACONs/airports to
// compressed timestamps. The manifest provides efficient access to available
// data timestamps for each location.
type Manifest struct {
	// data maps TRACON/airport identifiers to compressed, delta-encoded int64 timestamps
	data RawManifest

	// cache stores decompressed timestamps for recently accessed TRACONs
	// to avoid repeated decompression
	cache *expirable.LRU[string, []time.Time]
}

// NewManifest creates an empty manifest with caching enabled
func NewManifest() *Manifest {
	return &Manifest{
		data:  make(RawManifest),
		cache: expirable.NewLRU[string, []time.Time](32, nil, 4*time.Hour),
	}
}

// MakeManifest creates a manifest from raw manifest data.
// This is useful when loading data from storage systems.
func MakeManifest(data RawManifest) *Manifest {
	return &Manifest{
		data:  data,
		cache: expirable.NewLRU[string, []time.Time](32, nil, 4*time.Hour),
	}
}

func MakeManifestFromMap(m map[string][]time.Time) (*Manifest, error) {
	manifest := NewManifest()
	for facility, times := range m {
		if err := manifest.SetFacilityTimestamps(facility, times); err != nil {
			return nil, err
		}
	}
	return manifest, nil
}

// SetFacilityTimestamps stores timestamps for a facility (TRACON or ARTCC).
func (m *Manifest) SetFacilityTimestamps(facilityID string, times []time.Time) error {
	timestamps := util.MapSlice(times, func(t time.Time) int64 { return t.Unix() })

	// Sort and compress
	slices.Sort(timestamps)
	compressed, err := compressTimestamps(timestamps)
	if err != nil {
		return err
	}

	m.data[facilityID] = compressed
	return nil
}

// LoadManifest reads a manifest from an io.Reader.
// The manifest format is msgpack-encoded RawManifest, compressed with zstd.
func LoadManifest(r io.Reader) (*Manifest, error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd reader: %w", err)
	}
	defer zr.Close()

	var data RawManifest
	if err := msgpack.NewDecoder(zr).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode manifest: %w", err)
	}

	return MakeManifest(data), nil
}

// Save writes the manifest to an io.Writer in the standard format
// (msgpack + zstd compression)
func (m *Manifest) Save(w io.Writer) error {
	zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return fmt.Errorf("failed to create zstd writer: %w", err)
	}
	defer zw.Close()

	if err := msgpack.NewEncoder(zw).Encode(m.data); err != nil {
		return fmt.Errorf("failed to encode manifest: %w", err)
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("failed to close zstd writer: %w", err)
	}

	return nil
}

// GetTimestamps retrieves and decompresses the timestamps for a specific TRACON/airport.
// Results are cached to avoid repeated decompression.
// Returns nil, false if the TRACON/airport is not in the manifest.
func (m *Manifest) GetTimestamps(identifier string) ([]time.Time, bool) {
	// Check cache first
	if times, ok := m.cache.Get(identifier); ok {
		return times, true
	}

	compressed, ok := m.data[identifier]

	if !ok {
		return nil, false
	}

	// Decompress timestamps
	timestamps, err := decompressTimestamps(compressed)
	if err != nil {
		return nil, false
	}

	// Convert to time.Time
	times := make([]time.Time, len(timestamps))
	for i, ts := range timestamps {
		times[i] = time.Unix(ts, 0).UTC()
	}

	// Cache the result
	m.cache.Add(identifier, times)

	return times, true
}

// RawManifest returns the underlying RawManifest for compatibility with
// storage systems that need the raw format.
func (m *Manifest) RawManifest() RawManifest {
	return maps.Clone(m.data)
}

// TRACONs returns a sorted list of all TRACON/airport identifiers in the manifest
func (m *Manifest) TRACONs() []string {
	tracons := slices.Collect(maps.Keys(m.data))
	slices.Sort(tracons)
	return tracons
}

// Count returns the number of TRACONs/airports in the manifest
func (m *Manifest) Count() int {
	return len(m.data)
}

// TotalEntries returns the total number of timestamp entries across all TRACONs/airports
func (m *Manifest) TotalEntries() int {
	total := 0
	for tracon := range m.data {
		times, ok := m.GetTimestamps(tracon)
		if ok {
			total += len(times)
		}
	}
	return total
}

// GetAllTimestamps returns all timestamps from all TRACONs/airports in the manifest.
// The timestamps are collected in an unspecified order.
func (m *Manifest) GetAllTimestamps() []time.Time {
	var allTimes []time.Time
	for _, tracon := range m.TRACONs() {
		times, ok := m.GetTimestamps(tracon)
		if ok {
			allTimes = append(allTimes, times...)
		}
	}
	return allTimes
}

// ParseWeatherObjectPath extracts the TRACON/airport identifier and timestamp
// from a weather data object path.
// Expected format: "TRACON/2025-08-06T03:00:00Z.msgpack.zst"
func ParseWeatherObjectPath(relativePath string) (identifier string, timestamp int64, err error) {
	parts := strings.Split(relativePath, "/")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("unexpected path format: %s (expected TRACON/timestamp.msgpack.zst)", relativePath)
	}

	identifier = parts[0]
	timestampStr := strings.TrimSuffix(parts[1], ".msgpack.zst")

	t, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		return "", 0, fmt.Errorf("failed to parse timestamp from %s: %w", timestampStr, err)
	}

	return identifier, t.Unix(), nil
}

// GenerateManifest creates a manifest from a map of object paths to their sizes/timestamps.
// The pathParser function extracts the identifier and timestamp from each path.
// Timestamps are sorted, delta-encoded, and compressed for each identifier.
func GenerateManifest(paths map[string]int64, pathParser func(string) (string, int64, error)) (*Manifest, error) {
	// Collect timestamps per identifier
	timestampsByID := make(map[string][]int64)

	for path := range paths {
		// Skip manifest files themselves
		if strings.Contains(path, "manifest") {
			continue
		}

		identifier, timestamp, err := pathParser(path)
		if err != nil {
			// Skip unparseable paths
			continue
		}

		timestampsByID[identifier] = append(timestampsByID[identifier], timestamp)
	}

	// Sort, compress timestamps for each identifier
	manifest := NewManifest()
	for identifier, times := range timestampsByID {
		slices.Sort(times)
		compressed, err := compressTimestamps(times)
		if err != nil {
			return nil, fmt.Errorf("failed to compress timestamps for %s: %w", identifier, err)
		}
		manifest.data[identifier] = compressed
	}

	return manifest, nil
}

// GenerateManifestWithPrefix is a convenience function that generates a manifest
// from paths that include a prefix. The prefix is stripped before parsing.
// This is commonly used with storage backends that return full paths.
func GenerateManifestWithPrefix(paths map[string]int64, prefix string) (*Manifest, error) {
	return GenerateManifest(paths, func(path string) (string, int64, error) {
		// Remove prefix
		relativePath := strings.TrimPrefix(path, prefix+"/")
		return ParseWeatherObjectPath(relativePath)
	})
}

// BuildObjectPath constructs a weather data object path from a prefix, identifier, and timestamp.
// Returns a path in the format: "prefix/identifier/{RFC3339 time}.msgpack.zst"
func BuildObjectPath(prefix, identifier string, t time.Time) string {
	return filepath.Join(prefix, identifier, t.UTC().Format(time.RFC3339)+".msgpack.zst")
}

// ManifestPath returns the full path to a manifest file for a given prefix
func ManifestPath(prefix string) string {
	return filepath.Join(prefix, ManifestFilename)
}

// MonthlyManifestPath returns the full path to a monthly manifest file.
// The month parameter should be in "YYYY-MM" format (e.g., "2025-08").
// Returns a path like "prefix/manifest-int64time-YYYY-MM.msgpack.zst"
func MonthlyManifestPath(prefix, month string) string {
	return filepath.Join(prefix, fmt.Sprintf("manifest-int64time-%s.msgpack.zst", month))
}

// MonthlyManifestPrefix returns the prefix used for listing monthly manifest files.
// Returns a prefix like "prefix/manifest-int64time-"
func MonthlyManifestPrefix(prefix string) string {
	return filepath.Join(prefix, "manifest-int64time-")
}

// compressTimestamps delta-encodes and flate-compresses a slice of int64 timestamps.
// Returns the compressed bytes suitable for storage.
func compressTimestamps(timestamps []int64) ([]byte, error) {
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

// decompressTimestamps decompresses flate-compressed, delta-encoded int64 timestamps.
// Returns the original int64 timestamps.
func decompressTimestamps(compressed []byte) ([]int64, error) {
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
