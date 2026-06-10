// aviation/maps_test.go
// Copyright(c) 2025-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"reflect"
	"testing"

	"github.com/mmp/vice/math"
)

// buildSyntheticLibrary returns a small fixture covering all feature
// kinds: STARS maps with lines (solid + a dashed style), symbols, and
// labels; an ERAM group with two distinct maps; and a separate ERAM
// group exercising the duplicate-(L1,L2) case (e.g. ZLA's MVA collision).
func buildSyntheticLibrary() *MapLibrary {
	return &MapLibrary{
		Maps: map[string]STARSMap{
			"BASE": {
				Name:     "BASE",
				Label:    "BASE",
				Id:       1,
				Group:    0,
				Category: 1,
				Color:    3,
				Lines: []MapLine{
					{
						Points: []math.Point2LL{
							{-73.78, 40.64},
							{-73.79, 40.65},
							{-73.80, 40.66},
						},
						Style:     LineStyleSolid,
						Thickness: 1,
						BCGIndex:  2,
					},
					{
						Points: []math.Point2LL{
							{-73.50, 40.50},
							{-73.51, 40.51},
						},
						Style:     LineStyleShortDashed,
						Thickness: 2,
						BCGIndex:  0,
					},
				},
			},
			"VOR-MAP": {
				Name:  "VOR-MAP",
				Label: "VOR",
				Id:    101,
				Group: 1,
				Symbols: []MapSymbol{
					{P: math.Point2LL{-73.5, 40.5}, Style: SymbolStyleVOR, Size: 1, BCGIndex: 3},
					{P: math.Point2LL{-73.6, 40.6}, Style: SymbolStyleNDB, Size: 1, BCGIndex: 0},
				},
			},
			"LABELS": {
				Name:  "LABELS",
				Label: "LBL",
				Id:    202,
				Labels: []MapLabel{
					{
						P:         math.Point2LL{-74.0, 40.7},
						Text:      "KJFK",
						Size:      1,
						XOffset:   2,
						YOffset:   -1,
						Underline: true,
						BCGIndex:  4,
					},
				},
			},
		},
		ERAMMapGroups: map[string]ERAMMapGroup{
			"ZNYMAP": {
				Name:       "ZNYMAP",
				LabelLine1: "ZNY",
				LabelLine2: "ARTCC",
				Maps: []ERAMMap{
					{
						LabelLine1: "BASE",
						LabelLine2: "MAP",
						BCGName:    "BASE",
						Lines: []MapLine{{
							Points: []math.Point2LL{
								{-74.0, 40.7}, {-74.1, 40.8},
							},
							Style: LineStyleSolid,
						}},
					},
					{
						LabelLine1: "HIGH",
						LabelLine2: "SECTOR",
						BCGName:    "HI SEC",
						Symbols: []MapSymbol{{
							P: math.Point2LL{-73.5, 40.5}, Style: SymbolStyleVOR, Size: 1,
						}},
					},
				},
			},
			// ZLA-MVA case: two maps with identical (LabelLine1, LabelLine2).
			"ZLAWEST": {
				Name:       "ZLAWEST",
				LabelLine1: "ZLA",
				LabelLine2: "WEST",
				Maps: []ERAMMap{
					{
						LabelLine1: "MVA",
						BCGName:    "MVA",
						Lines: []MapLine{{
							Points: []math.Point2LL{{-118.0, 34.0}, {-118.1, 34.1}},
							Style:  LineStyleSolid,
						}},
					},
					{
						LabelLine1: "MVA", // duplicate on (L1,L2) — by design
						BCGName:    "MVA",
						Lines: []MapLine{{
							Points: []math.Point2LL{{-117.0, 33.0}, {-117.1, 33.1}},
							Style:  LineStyleSolid,
						}},
					},
				},
			},
		},
	}
}

func TestMapLibraryRoundtrip(t *testing.T) {
	orig := buildSyntheticLibrary()

	var buf bytes.Buffer
	if err := SaveMapLibrary(&buf, orig); err != nil {
		t.Fatalf("save: %v", err)
	}

	if string(buf.Bytes()[:4]) != mapLibraryMagic {
		t.Fatalf("missing magic: got %q", buf.Bytes()[:4])
	}

	got, err := decodeFromBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(got.Maps) != len(orig.Maps) {
		t.Fatalf("STARS map count: got %d, want %d", len(got.Maps), len(orig.Maps))
	}
	for name, want := range orig.Maps {
		gm, ok := got.Maps[name]
		if !ok {
			t.Errorf("STARS map %q missing after roundtrip", name)
			continue
		}
		if !reflect.DeepEqual(gm, want) {
			t.Errorf("STARS map %q mismatch:\n got %+v\nwant %+v", name, gm, want)
		}
	}

	if len(got.ERAMMapGroups) != len(orig.ERAMMapGroups) {
		t.Fatalf("ERAM group count: got %d, want %d", len(got.ERAMMapGroups), len(orig.ERAMMapGroups))
	}
	for name, want := range orig.ERAMMapGroups {
		gg, ok := got.ERAMMapGroups[name]
		if !ok {
			t.Errorf("ERAM group %q missing after roundtrip", name)
			continue
		}
		if !reflect.DeepEqual(gg, want) {
			t.Errorf("ERAM group %q mismatch:\n got %+v\nwant %+v", name, gg, want)
		}
	}
}

// TestSpecFastPath verifies that the metadata-only loader needs only
// magic + headerLen + header bytes — never any of the flate-compressed
// geometry region. We confirm by truncating the data right after the
// header and checking the spec loader still succeeds.
func TestSpecFastPath(t *testing.T) {
	orig := buildSyntheticLibrary()
	var buf bytes.Buffer
	if err := SaveMapLibrary(&buf, orig); err != nil {
		t.Fatalf("save: %v", err)
	}
	data := buf.Bytes()

	headerLen := binary.LittleEndian.Uint32(data[4:8])
	headerEnd := 8 + int(headerLen)

	// Truncate the geometry region entirely — the spec loader must not
	// touch it.
	truncated := append([]byte(nil), data[:headerEnd]...)

	hdr, _, err := parseMapLibraryHeader(truncated)
	if err != nil {
		t.Fatalf("parseMapLibraryHeader on truncated file: %v", err)
	}
	if len(hdr.STARSMaps) != len(orig.Maps) {
		t.Errorf("spec STARS count: got %d, want %d", len(hdr.STARSMaps), len(orig.Maps))
	}
	if len(hdr.ERAMGroups) != len(orig.ERAMMapGroups) {
		t.Errorf("spec ERAM group count: got %d, want %d",
			len(hdr.ERAMGroups), len(orig.ERAMMapGroups))
	}

	spec := &MapLibrarySpec{header: hdr}
	for name := range orig.Maps {
		if !spec.HasMap(name) {
			t.Errorf("spec missing STARS map %q", name)
		}
	}
	for name := range orig.ERAMMapGroups {
		if !spec.HasMapGroup(name) {
			t.Errorf("spec missing ERAM group %q", name)
		}
	}
	if !spec.HasMap(combineLabels("HIGH", "SECTOR")) {
		t.Errorf("spec missing ERAM combined label HIGH SECTOR")
	}
}

// TestDuplicateERAMLabels verifies the ZLA-MVA case: two maps in the
// same group with identical (LabelLine1, LabelLine2) survive roundtrip
// as distinct entries with distinct geometry payloads.
func TestDuplicateERAMLabels(t *testing.T) {
	orig := buildSyntheticLibrary()
	var buf bytes.Buffer
	if err := SaveMapLibrary(&buf, orig); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := decodeFromBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	g := got.ERAMMapGroups["ZLAWEST"]
	if len(g.Maps) != 2 {
		t.Fatalf("ZLAWEST: got %d maps, want 2", len(g.Maps))
	}
	if g.Maps[0].LabelLine1 != "MVA" || g.Maps[1].LabelLine1 != "MVA" {
		t.Fatalf("ZLAWEST: both labels should be MVA; got %q and %q",
			g.Maps[0].LabelLine1, g.Maps[1].LabelLine1)
	}
	if reflect.DeepEqual(g.Maps[0].Lines[0].Points, g.Maps[1].Lines[0].Points) {
		t.Errorf("ZLAWEST: the two MVA maps share geometry but should not")
	}
}

// TestLineStylePreserved verifies dashed lines are stored as data, not
// pre-expanded into segments at import time.
func TestLineStylePreserved(t *testing.T) {
	orig := buildSyntheticLibrary()
	var buf bytes.Buffer
	if err := SaveMapLibrary(&buf, orig); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := decodeFromBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	base := got.Maps["BASE"]
	if len(base.Lines) != 2 {
		t.Fatalf("BASE: expected 2 lines, got %d", len(base.Lines))
	}
	if base.Lines[1].Style != LineStyleShortDashed {
		t.Errorf("BASE line 1: style = %v, want ShortDashed", base.Lines[1].Style)
	}
	if len(base.Lines[1].Points) != 2 {
		t.Errorf("BASE line 1: %d points after roundtrip, want 2 (not pre-expanded)",
			len(base.Lines[1].Points))
	}
}

// TestWrongMagic ensures the loader rejects a legacy-format file with a
// clear pointer to upgradevideomap.
func TestWrongMagic(t *testing.T) {
	data := []byte("XXXXjunk")
	_, _, err := parseMapLibraryHeader(data)
	if err == nil {
		t.Fatal("expected error for wrong magic, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("upgradevideomap")) {
		t.Errorf("error %q should mention upgradevideomap", err)
	}
}

// ---------- test helpers -----------------------------------------------

// decodeFromBytes parses a serialized library from an in-memory buffer.
// Mirrors the body of LoadMapLibrary without the fs.FS plumbing.
func decodeFromBytes(data []byte) (*MapLibrary, error) {
	hdr, body, err := parseMapLibraryHeader(data)
	if err != nil {
		return nil, err
	}
	fr := flate.NewReader(bytes.NewReader(body))
	defer fr.Close()
	geom, err := io.ReadAll(fr)
	if err != nil {
		return nil, err
	}

	lib := &MapLibrary{
		Maps:          make(map[string]STARSMap, len(hdr.STARSMaps)),
		ERAMMapGroups: make(map[string]ERAMMapGroup, len(hdr.ERAMGroups)),
	}
	for _, e := range hdr.STARSMaps {
		lines, symbols, labels, err := decodeGeometry(geom, e.GeomOffset, e.GeomLen)
		if err != nil {
			return nil, err
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
				return nil, err
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
