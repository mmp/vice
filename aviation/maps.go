// aviation/maps.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	gomath "math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/vmihailenco/msgpack/v5"
)

// STARSMap is the runtime representation of a single STARS-side video map.
// Lines/Symbols/Labels are populated by the full library loader and left
// nil by the metadata-only spec loader.
type STARSMap struct {
	Name     string
	Label    string // DCB button label (CRC's "shortName")
	Id       int    // user-visible STARS map number; 0 = no DCB id
	Group    int    // 0 = A, 1 = B
	Category int    // -1..9
	Color    int    // 0..8

	Lines   []MapLine
	Symbols []MapSymbol
	Labels  []MapLabel
}

// ERAMMap is the runtime representation of a single map within an ERAM
// filter-menu group. Per-map identity within a group is the pair
// (LabelLine1, LabelLine2); duplicates are allowed and share state by
// design.
type ERAMMap struct {
	LabelLine1 string
	LabelLine2 string
	BCGName    string // brightness control group key; intentionally shared across maps

	Lines   []MapLine
	Symbols []MapSymbol
	Labels  []MapLabel
}

type ERAMMapGroup struct {
	Name       string
	LabelLine1 string
	LabelLine2 string
	Maps       []ERAMMap
}

// Bounds returns the lat/lon bounding box covering every feature in the
// map (line vertices, symbol positions, and label positions). Returns
// an empty Extent2D if the map has no features.
func (m STARSMap) Bounds() math.Extent2D {
	return featureBounds(m.Lines, m.Symbols, m.Labels)
}

// Bounds returns the lat/lon bounding box covering every feature in the
// ERAM map (line vertices, symbol positions, and label positions).
// Returns an empty Extent2D if the map has no features.
func (m ERAMMap) Bounds() math.Extent2D {
	return featureBounds(m.Lines, m.Symbols, m.Labels)
}

func featureBounds(lines []MapLine, symbols []MapSymbol, labels []MapLabel) math.Extent2D {
	e := math.EmptyExtent2D()
	for _, l := range lines {
		for _, p := range l.Points {
			e = math.Union(e, p)
		}
	}
	for _, s := range symbols {
		e = math.Union(e, s.P)
	}
	for _, l := range labels {
		e = math.Union(e, l.P)
	}
	return e
}

// MapLibrary is the full in-memory representation of a video map
// file. STARS maps are keyed by Name (which is unique within a file);
// ERAM groups are keyed by group name.
type MapLibrary struct {
	Maps          map[string]STARSMap
	ERAMMapGroups map[string]ERAMMapGroup
}

// ---------- per-feature types -------------------------------------------

type MapLine struct {
	Points    []math.Point2LL
	Style     LineStyle
	Thickness uint8
	BCGIndex  uint8 // 0 = use the map's group BCG
}

type MapSymbol struct {
	P        math.Point2LL
	Style    SymbolStyle
	Size     uint8
	BCGIndex uint8 // 0 = use the map's group BCG
}

type MapLabel struct {
	P         math.Point2LL
	Text      string
	Size      uint8
	XOffset   int8
	YOffset   int8
	Underline bool
	Opaque    bool
	BCGIndex  uint8 // 0 = use the map's group BCG
}

type LineStyle uint8

const (
	LineStyleSolid LineStyle = iota
	LineStyleShortDashed
	LineStyleLongDashed
	LineStyleLongDashShortDash
)

func (s LineStyle) String() string {
	switch s {
	case LineStyleShortDashed:
		return "ShortDashed"
	case LineStyleLongDashed:
		return "LongDashed"
	case LineStyleLongDashShortDash:
		return "LongDashShortDash"
	default:
		return "Solid"
	}
}

type SymbolStyle uint8

// Symbol style values must be stable across releases — they're written
// to disk as raw bytes. Add new values at the end; do not renumber.
const (
	SymbolStyleVOR SymbolStyle = iota
	SymbolStyleNDB
	SymbolStyleTACAN
	SymbolStyleVOR_TACAN
	SymbolStyleDME
	SymbolStyleRNAV
	SymbolStyleRNAVOnlyWaypoint
	SymbolStyleAirport
	SymbolStyleSatelliteAirport
	SymbolStyleEmergencyAirport
	SymbolStyleHeliport
	SymbolStyleOtherWaypoints
	SymbolStyleAirwayIntersections
	SymbolStyleIAF
	SymbolStyleObstruction1
	SymbolStyleObstruction2
	SymbolStyleNuclear
	SymbolStyleRadar
)

func (s SymbolStyle) String() string {
	switch s {
	case SymbolStyleVOR:
		return "VOR"
	case SymbolStyleNDB:
		return "NDB"
	case SymbolStyleTACAN:
		return "TACAN"
	case SymbolStyleVOR_TACAN:
		return "VOR_TACAN"
	case SymbolStyleDME:
		return "DME"
	case SymbolStyleRNAV:
		return "RNAV"
	case SymbolStyleRNAVOnlyWaypoint:
		return "RNAVOnlyWaypoint"
	case SymbolStyleAirport:
		return "Airport"
	case SymbolStyleSatelliteAirport:
		return "SatelliteAirport"
	case SymbolStyleEmergencyAirport:
		return "EmergencyAirport"
	case SymbolStyleHeliport:
		return "Heliport"
	case SymbolStyleOtherWaypoints:
		return "OtherWaypoints"
	case SymbolStyleAirwayIntersections:
		return "AirwayIntersections"
	case SymbolStyleIAF:
		return "IAF"
	case SymbolStyleObstruction1:
		return "Obstruction1"
	case SymbolStyleObstruction2:
		return "Obstruction2"
	case SymbolStyleNuclear:
		return "Nuclear"
	case SymbolStyleRadar:
		return "Radar"
	default:
		return fmt.Sprintf("Symbol(%d)", uint8(s))
	}
}

// ---------- on-wire types -----------------------------------------------

// mapLibraryMagic is the 4-byte file header. The digit is the single
// version indicator for the on-disk format — bumping it (and the file
// header's structure / geometry payload shape together) makes old
// loaders fail with a clear error.
const mapLibraryMagic = "VMV2"

// wireFileHeader is the msgpack-encoded header block that immediately
// follows the magic + header length. It contains only metadata; the
// geometry payloads live in the flate-compressed region after it and
// are addressed by (GeomOffset, GeomLen).
type wireFileHeader struct {
	STARSMaps  []wireSTARSEntry `msgpack:"s"`
	ERAMGroups []wireERAMGroup  `msgpack:"e"`
}

type wireSTARSEntry struct {
	Name       string `msgpack:"n"`
	Label      string `msgpack:"l"`
	Id         int    `msgpack:"i"`
	Group      uint8  `msgpack:"g"`
	Category   int8   `msgpack:"c"`
	Color      uint8  `msgpack:"co"`
	GeomOffset uint32 `msgpack:"o"`
	GeomLen    uint32 `msgpack:"sz"`
}

type wireERAMGroup struct {
	Name       string          `msgpack:"n"`
	LabelLine1 string          `msgpack:"l1"`
	LabelLine2 string          `msgpack:"l2"`
	Maps       []wireERAMEntry `msgpack:"m"`
}

type wireERAMEntry struct {
	LabelLine1 string `msgpack:"l1"`
	LabelLine2 string `msgpack:"l2"`
	BCGName    string `msgpack:"b"`
	GeomOffset uint32 `msgpack:"o"`
	GeomLen    uint32 `msgpack:"sz"`
}

// ---------- MapLibrarySpec: metadata-only loader --------------------------

// MapLibrarySpec carries everything server startup needs to validate
// scenario references without touching the geometry region. It also
// remembers the underlying file so callers can compute Hash() lazily.
type MapLibrarySpec struct {
	header     *wireFileHeader
	filesystem fs.FS
	filename   string
}

// HasMap returns true if name matches a STARS map's Name, or matches
// the combined label of any ERAM map within any group.
func (s *MapLibrarySpec) HasMap(name string) bool {
	if s == nil || s.header == nil {
		return false
	}
	for _, m := range s.header.STARSMaps {
		if m.Name == name {
			return true
		}
	}
	for _, g := range s.header.ERAMGroups {
		for _, m := range g.Maps {
			if combineLabels(m.LabelLine1, m.LabelLine2) == name {
				return true
			}
		}
	}
	return false
}

// STARSMapId returns the STARS DCB Id for the named map. Returns 0 if
// the name is unknown or the map has no DCB Id assigned (0 is the
// sentinel for "no Id" since STARS commands reject Id 0 anyway).
func (s *MapLibrarySpec) STARSMapId(name string) int {
	if s == nil || s.header == nil {
		return 0
	}
	for _, m := range s.header.STARSMaps {
		if m.Name == name {
			return m.Id
		}
	}
	return 0
}

// HasMapGroup returns true if name matches an ERAM group's name.
func (s *MapLibrarySpec) HasMapGroup(name string) bool {
	if s == nil || s.header == nil {
		return false
	}
	for _, g := range s.header.ERAMGroups {
		if g.Name == name {
			return true
		}
	}
	return false
}

// Hash returns a hash of the underlying video map file.
func (s *MapLibrarySpec) Hash() ([]byte, error) {
	if s == nil {
		return nil, errors.New("nil MapLibrarySpec")
	}
	f, err := s.filesystem.Open(s.filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return util.Hash(f)
}

// LoadMapLibrarySpec opens a video map file and decodes only the metadata
// header, leaving the flate-compressed geometry region untouched.
func LoadMapLibrarySpec(path string) (*MapLibrarySpec, error) {
	filesystem := mapLibraryFS(path)
	f, err := filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hdr, err := readMapLibraryHeaderOnly(f)
	if err != nil {
		return nil, err
	}

	return &MapLibrarySpec{
		header:     hdr,
		filesystem: filesystem,
		filename:   path,
	}, nil
}

// readMapLibraryHeaderOnly reads just the magic + headerLen + header msgpack
// from f and stops without slurping the geometry region. Used by
// LoadMapLibrarySpec so server startup / lint cost is proportional to the
// header size rather than the (multi-MB) compressed geometry that follows.
func readMapLibraryHeaderOnly(f fs.File) (*wireFileHeader, error) {
	var prefix [8]byte
	if _, err := io.ReadFull(f, prefix[:]); err != nil {
		return nil, fmt.Errorf("video map: short prefix: %w", err)
	}
	if string(prefix[:4]) != mapLibraryMagic {
		return nil, fmt.Errorf("video map: wrong magic %q (expected %q); "+
			"re-import with cmd/crc2vice or convert with cmd/upgradevideomap",
			prefix[:4], mapLibraryMagic)
	}
	headerLen := binary.LittleEndian.Uint32(prefix[4:8])
	headerBytes := make([]byte, headerLen)
	if _, err := io.ReadFull(f, headerBytes); err != nil {
		return nil, fmt.Errorf("video map: short header: %w", err)
	}
	hdr := &wireFileHeader{}
	if err := msgpack.Unmarshal(headerBytes, hdr); err != nil {
		return nil, fmt.Errorf("video map: header decode: %w", err)
	}
	return hdr, nil
}

// ---------- LoadMapLibrary: full load ------------------------------

func LoadMapLibrary(path string) (*MapLibrary, error) {
	filesystem := mapLibraryFS(path)
	f, err := filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hdr, body, err := readMapLibraryHeader(f)
	if err != nil {
		return nil, err
	}

	// Decompress the geometry region.
	fr := flate.NewReader(bytes.NewReader(body))
	defer fr.Close()
	geom, err := io.ReadAll(fr)
	if err != nil {
		return nil, fmt.Errorf("video map %s: geometry decompress: %w", path, err)
	}

	lib := &MapLibrary{
		Maps:          make(map[string]STARSMap, len(hdr.STARSMaps)),
		ERAMMapGroups: make(map[string]ERAMMapGroup, len(hdr.ERAMGroups)),
	}

	for _, e := range hdr.STARSMaps {
		lines, symbols, labels, err := decodeGeometry(geom, e.GeomOffset, e.GeomLen)
		if err != nil {
			return nil, fmt.Errorf("video map %s: STARS %q: %w", path, e.Name, err)
		}
		lib.Maps[e.Name] = STARSMap{
			Name:     e.Name,
			Label:    e.Label,
			Id:       e.Id,
			Group:    int(e.Group),
			Category: int(e.Category),
			Color:    int(e.Color),
			Lines:    lines,
			Symbols:  symbols,
			Labels:   labels,
		}
	}

	for _, g := range hdr.ERAMGroups {
		group := ERAMMapGroup{
			Name:       g.Name,
			LabelLine1: g.LabelLine1,
			LabelLine2: g.LabelLine2,
			Maps:       make([]ERAMMap, len(g.Maps)),
		}
		for i, m := range g.Maps {
			lines, symbols, labels, err := decodeGeometry(geom, m.GeomOffset, m.GeomLen)
			if err != nil {
				return nil, fmt.Errorf("video map %s: ERAM %s/%s: %w",
					path, g.Name, combineLabels(m.LabelLine1, m.LabelLine2), err)
			}
			group.Maps[i] = ERAMMap{
				LabelLine1: m.LabelLine1,
				LabelLine2: m.LabelLine2,
				BCGName:    m.BCGName,
				Lines:      lines,
				Symbols:    symbols,
				Labels:     labels,
			}
		}
		lib.ERAMMapGroups[g.Name] = group
	}

	return lib, nil
}

// HashCheckLoadMapLibrary loads the file only if its hash matches.
func HashCheckLoadMapLibrary(path string, wantHash []byte) (*MapLibrary, error) {
	filesystem := mapLibraryFS(path)
	f, err := filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	gotHash, herr := util.Hash(f)
	f.Close()
	if herr != nil {
		return nil, herr
	}
	if !bytesEqual(gotHash, wantHash) {
		return nil, errors.New("hash mismatch")
	}
	return LoadMapLibrary(path)
}

// readMapLibraryHeader reads and validates the magic + header length +
// header msgpack. It returns the decoded header and the remaining bytes
// (the flate-compressed geometry region).
func readMapLibraryHeader(f fs.File) (*wireFileHeader, []byte, error) {
	contents, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return parseMapLibraryHeader(contents)
}

// parseMapLibraryHeader runs the actual magic/length/msgpack validation
// against an in-memory slice. Split out so tests can exercise the
// header parser without bringing up an fs.FS.
func parseMapLibraryHeader(contents []byte) (*wireFileHeader, []byte, error) {
	if len(contents) < 8 {
		return nil, nil, errors.New("video map file too short")
	}
	if string(contents[:4]) != mapLibraryMagic {
		return nil, nil, fmt.Errorf("video map: wrong magic %q (expected %q); "+
			"re-import with cmd/crc2vice or convert with cmd/upgradevideomap",
			contents[:4], mapLibraryMagic)
	}
	headerLen := binary.LittleEndian.Uint32(contents[4:8])
	if uint64(headerLen)+8 > uint64(len(contents)) {
		return nil, nil, errors.New("video map: header length out of bounds")
	}
	hdr := &wireFileHeader{}
	if err := msgpack.Unmarshal(contents[8:8+headerLen], hdr); err != nil {
		return nil, nil, fmt.Errorf("video map: header decode: %w", err)
	}
	body := contents[8+headerLen:]
	return hdr, body, nil
}

// ---------- SaveMapLibrary: write path -----------------------------

// SaveMapLibrary encodes a library to the wire format and writes it
// to w. Iteration order over the input maps is deterministic (sorted by
// name) so files re-import bit-stable across runs.
func SaveMapLibrary(w io.Writer, lib *MapLibrary) error {
	// First pass: assemble the geometry blob region, recording offsets.
	var geom bytes.Buffer

	hdr := wireFileHeader{}

	starsNames := make([]string, 0, len(lib.Maps))
	for n := range lib.Maps {
		starsNames = append(starsNames, n)
	}
	sort.Strings(starsNames)
	for _, n := range starsNames {
		m := lib.Maps[n]
		offset := uint32(geom.Len())
		payload := encodeGeometry(m.Lines, m.Symbols, m.Labels)
		geom.Write(payload)
		hdr.STARSMaps = append(hdr.STARSMaps, wireSTARSEntry{
			Name:       m.Name,
			Label:      m.Label,
			Id:         m.Id,
			Group:      uint8(m.Group),
			Category:   int8(m.Category),
			Color:      uint8(m.Color),
			GeomOffset: offset,
			GeomLen:    uint32(len(payload)),
		})
	}

	eramNames := make([]string, 0, len(lib.ERAMMapGroups))
	for n := range lib.ERAMMapGroups {
		eramNames = append(eramNames, n)
	}
	sort.Strings(eramNames)
	for _, n := range eramNames {
		g := lib.ERAMMapGroups[n]
		wg := wireERAMGroup{
			Name:       g.Name,
			LabelLine1: g.LabelLine1,
			LabelLine2: g.LabelLine2,
			Maps:       make([]wireERAMEntry, len(g.Maps)),
		}
		for i, m := range g.Maps {
			offset := uint32(geom.Len())
			payload := encodeGeometry(m.Lines, m.Symbols, m.Labels)
			geom.Write(payload)
			wg.Maps[i] = wireERAMEntry{
				LabelLine1: m.LabelLine1,
				LabelLine2: m.LabelLine2,
				BCGName:    m.BCGName,
				GeomOffset: offset,
				GeomLen:    uint32(len(payload)),
			}
		}
		hdr.ERAMGroups = append(hdr.ERAMGroups, wg)
	}

	// Marshal the header and flate-compress the geometry region.
	headerBytes, err := msgpack.Marshal(&hdr)
	if err != nil {
		return fmt.Errorf("video map header encode: %w", err)
	}

	var compressed bytes.Buffer
	zw, err := flate.NewWriter(&compressed, flate.BestCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(geom.Bytes()); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	// Write [magic][headerLen][header][geometryFlate].
	if _, err := w.Write([]byte(mapLibraryMagic)); err != nil {
		return err
	}
	var hdrLen [4]byte
	binary.LittleEndian.PutUint32(hdrLen[:], uint32(len(headerBytes)))
	if _, err := w.Write(hdrLen[:]); err != nil {
		return err
	}
	if _, err := w.Write(headerBytes); err != nil {
		return err
	}
	if _, err := w.Write(compressed.Bytes()); err != nil {
		return err
	}
	return nil
}

// ---------- helpers ----------------------------------------------------

func mapLibraryFS(path string) fs.FS {
	if filepath.IsAbs(path) {
		return util.RootFS{}
	}
	return util.GetResourcesFS()
}

func combineLabels(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + " " + b
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PrintMapLibrary prints a table of the maps in the given file.
func PrintMapLibrary(path string, e *util.ErrorLogger) {
	vmf, err := LoadMapLibrary(path)
	if err != nil {
		e.Error(err)
		return
	}

	if len(vmf.Maps) > 0 {
		maps := make([]STARSMap, 0, len(vmf.Maps))
		for _, m := range vmf.Maps {
			maps = append(maps, m)
		}
		sort.Slice(maps, func(i, j int) bool {
			if maps[i].Id != maps[j].Id {
				return maps[i].Id < maps[j].Id
			}
			return maps[i].Name < maps[j].Name
		})
		fmt.Printf("STARS MAPS\n")
		fmt.Printf("%5s\t%20s\t%s\n", "Id", "Label", "Name")
		for _, m := range maps {
			fmt.Printf("%5d\t%20s\t%s\n", m.Id, m.Label, m.Name)
		}
	}

	if len(vmf.ERAMMapGroups) > 0 {
		groups := make([]string, 0, len(vmf.ERAMMapGroups))
		for n := range vmf.ERAMMapGroups {
			groups = append(groups, n)
		}
		sort.Strings(groups)
		fmt.Printf("\nERAM GROUPS\n")
		for _, gn := range groups {
			g := vmf.ERAMMapGroups[gn]
			fmt.Printf("  %s (%s / %s)\n", g.Name, g.LabelLine1, g.LabelLine2)
			for _, m := range g.Maps {
				fmt.Printf("    %-30s  BCG=%s\n", combineLabels(m.LabelLine1, m.LabelLine2), m.BCGName)
			}
		}
	}
}

// Geometry payload layout (one per STARS or ERAM map). All in a single
// flate-compressed stream shared by every map in the file; each map's
// payload is addressed by (GeomOffset, GeomLen) in the wire header.
//
//   [NumLines uvarint]
//     per line preamble: [NumPoints uvarint][Style u8][Thickness u8][BCGIndex u8]
//   [Combined path bytes for ALL lines: math.EncodePathBytes over the
//    concatenated point streams — the delta state is *chained* across
//    line boundaries, so the first point of line N+1 deltas against the
//    last point of line N. Adjacent lines in the same map are usually
//    spatially close, so the inter-line delta is small.]
//   [NumSymbols uvarint]
//     per symbol: [x f32][y f32][Style u8][Size u8][BCGIndex u8]
//   [NumLabels uvarint]
//     per label: [x f32][y f32][Size u8][Flags u8][XOffset i8][YOffset i8][BCGIndex u8]
//                [text: uvarint length + UTF-8 bytes]
//
// The whole geometry region is wrapped in a single shared
// flate.BestCompression writer at the file level so the redundancy in
// the high-order bytes is exploited across maps.
//
// There is no per-payload version byte; the entire file's layout is
// identified by the magic prefix (mapLibraryMagic). If the geometry shape
// ever needs to change, bump the magic suffix ("VMV1" → "VMV2").

// encodeGeometry serializes a single map's features to a self-contained
// payload (no flate; the caller wraps the whole geometry region).
func encodeGeometry(lines []MapLine, symbols []MapSymbol, labels []MapLabel) []byte {
	var b []byte

	// All line preambles first, then a single chained path stream.
	b = appendUvarint(b, uint64(len(lines)))
	var totalPts int
	for _, l := range lines {
		b = appendUvarint(b, uint64(len(l.Points)))
		b = append(b, byte(l.Style), l.Thickness, l.BCGIndex)
		totalPts += len(l.Points)
	}
	if totalPts > 0 {
		all := make([]math.Point2LL, 0, totalPts)
		for _, l := range lines {
			all = append(all, l.Points...)
		}
		b = math.EncodePathBytes(b, all)
	}

	b = appendUvarint(b, uint64(len(symbols)))
	for _, s := range symbols {
		b = appendFloat32(b, s.P[0])
		b = appendFloat32(b, s.P[1])
		b = append(b, byte(s.Style), s.Size, s.BCGIndex)
	}

	b = appendUvarint(b, uint64(len(labels)))
	for _, l := range labels {
		b = appendFloat32(b, l.P[0])
		b = appendFloat32(b, l.P[1])
		var flags byte
		if l.Underline {
			flags |= 0x01
		}
		if l.Opaque {
			flags |= 0x02
		}
		b = append(b, l.Size, flags, byte(l.XOffset), byte(l.YOffset), l.BCGIndex)
		b = appendUvarint(b, uint64(len(l.Text)))
		b = append(b, l.Text...)
	}

	return b
}

// decodeGeometry parses one map's payload out of the shared decompressed
// geometry region. (offset, length) come from the wire header.
func decodeGeometry(region []byte, offset, length uint32) ([]MapLine, []MapSymbol, []MapLabel, error) {
	if uint64(offset)+uint64(length) > uint64(len(region)) {
		return nil, nil, nil, fmt.Errorf("geometry slice out of bounds: offset=%d len=%d region=%d",
			offset, length, len(region))
	}
	if length == 0 {
		return nil, nil, nil, nil
	}
	d := &payloadReader{buf: region[offset : offset+length]}

	lines, err := decodeLines(d)
	if err != nil {
		return nil, nil, nil, err
	}

	numSymbols, err := d.uvarint()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("num symbols: %w", err)
	}
	var symbols []MapSymbol
	if numSymbols > 0 {
		symbols = make([]MapSymbol, numSymbols)
	}
	for i := range symbols {
		x, err := d.float32()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("symbol %d: x: %w", i, err)
		}
		y, err := d.float32()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("symbol %d: y: %w", i, err)
		}
		attrs, err := d.byteN(3)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("symbol %d: attrs: %w", i, err)
		}
		symbols[i] = MapSymbol{
			P:        math.Point2LL{x, y},
			Style:    SymbolStyle(attrs[0]),
			Size:     attrs[1],
			BCGIndex: attrs[2],
		}
	}

	numLabels, err := d.uvarint()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("num labels: %w", err)
	}
	var labels []MapLabel
	if numLabels > 0 {
		labels = make([]MapLabel, numLabels)
	}
	for i := range labels {
		x, err := d.float32()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("label %d: x: %w", i, err)
		}
		y, err := d.float32()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("label %d: y: %w", i, err)
		}
		attrs, err := d.byteN(5)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("label %d: attrs: %w", i, err)
		}
		tlen, err := d.uvarint()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("label %d: text length: %w", i, err)
		}
		text, err := d.byteN(int(tlen))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("label %d: text: %w", i, err)
		}
		labels[i] = MapLabel{
			P:         math.Point2LL{x, y},
			Size:      attrs[0],
			Underline: attrs[1]&0x01 != 0,
			Opaque:    attrs[1]&0x02 != 0,
			XOffset:   int8(attrs[2]),
			YOffset:   int8(attrs[3]),
			BCGIndex:  attrs[4],
			Text:      string(text),
		}
	}

	return lines, symbols, labels, nil
}

// decodeLines reads the line-block: all preambles first, then a single
// chained path stream covering every line's points.
func decodeLines(d *payloadReader) ([]MapLine, error) {
	numLines, err := d.uvarint()
	if err != nil {
		return nil, fmt.Errorf("num lines: %w", err)
	}
	if numLines == 0 {
		return nil, nil
	}
	lines := make([]MapLine, numLines)
	counts := make([]int, numLines)
	totalPts := 0
	for i := range lines {
		n, err := d.uvarint()
		if err != nil {
			return nil, fmt.Errorf("line %d: num points: %w", i, err)
		}
		style, err := d.byteN(3)
		if err != nil {
			return nil, fmt.Errorf("line %d: header: %w", i, err)
		}
		lines[i] = MapLine{
			Style:     LineStyle(style[0]),
			Thickness: style[1],
			BCGIndex:  style[2],
		}
		counts[i] = int(n)
		totalPts += int(n)
	}
	if totalPts == 0 {
		return lines, nil
	}
	all := make([]math.Point2LL, totalPts)
	if err := d.readPath(all); err != nil {
		return nil, fmt.Errorf("chained path: %w", err)
	}
	// Carve `all` into per-line subslices (no copy; lines share backing array).
	pos := 0
	for i, n := range counts {
		lines[i].Points = all[pos : pos+n : pos+n]
		pos += n
	}
	return lines, nil
}

// ---------- low-level helpers -------------------------------------------

func appendUvarint(b []byte, x uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], x)
	return append(b, tmp[:n]...)
}

func appendFloat32(b []byte, f float32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], gomath.Float32bits(f))
	return append(b, tmp[:]...)
}

// payloadReader is a tiny cursor over a byte slice with the operations
// used by decodeGeometry.
type payloadReader struct {
	buf []byte
	pos int
}

func (r *payloadReader) byteN(n int) ([]byte, error) {
	if r.pos+n > len(r.buf) {
		return nil, fmt.Errorf("short read: want %d, have %d", n, len(r.buf)-r.pos)
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *payloadReader) uvarint() (uint64, error) {
	x, n := binary.Uvarint(r.buf[r.pos:])
	if n <= 0 {
		return 0, fmt.Errorf("malformed uvarint at offset %d", r.pos)
	}
	r.pos += n
	return x, nil
}

func (r *payloadReader) float32() (float32, error) {
	if r.pos+4 > len(r.buf) {
		return 0, fmt.Errorf("short read for float32 at offset %d", r.pos)
	}
	u := binary.LittleEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return gomath.Float32frombits(u), nil
}

// readPath decodes len(out) points from the cursor using
// math.DecodePathBytes (consumes 8*len(out) bytes).
func (r *payloadReader) readPath(out []math.Point2LL) error {
	n, err := math.DecodePathBytes(r.buf[r.pos:], out)
	if err != nil {
		return err
	}
	r.pos += n
	return nil
}
