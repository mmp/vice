// aviation/maps_test.go
// Copyright(c) 2025-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"bytes"
	"encoding/binary"
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
				BCGNames:   []string{"BASE", "HI SEC", "", "SPARE"},
				// Always-displayed geometry: no label, no filter-menu slot.
				BaseMap: ERAMMap{
					Lines: []MapLine{{
						Points:    []math.Point2LL{{-75.0, 39.0}, {-75.1, 39.1}, {-75.2, 39.0}},
						Style:     LineStyleLongDashed,
						Thickness: 2,
						BCGIndex:  1,
					}},
					Symbols: []MapSymbol{{
						P: math.Point2LL{-75.05, 39.05}, Style: SymbolStyleNDB, Size: 2, BCGIndex: 1,
					}},
				},
				Maps: []ERAMMap{
					{
						LabelLine1: "BASE",
						LabelLine2: "MAP",
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
						Symbols: []MapSymbol{{
							P: math.Point2LL{-73.5, 40.5}, Style: SymbolStyleVOR, Size: 1,
						}},
					},
					// Placeholder holding the position of an empty filter-menu
					// slot: no label, no geometry, not nameable.
					{},
					{
						// Same label as a map in ZLAWEST; labels are unique
						// only within a group.
						LabelLine1: "MVA",
						Lines: []MapLine{{
							Points: []math.Point2LL{{-74.5, 41.0}, {-74.6, 41.1}},
							Style:  LineStyleSolid,
						}},
					},
				},
			},
			// ZLA-MVA case: two maps with identical (LabelLine1, LabelLine2).
			"ZLAWEST": {
				Name:       "ZLAWEST",
				LabelLine1: "ZLA",
				LabelLine2: "WEST",
				BCGNames:   []string{"MVA"},
				Maps: []ERAMMap{
					{
						LabelLine1: "MVA",
						Lines: []MapLine{{
							Points: []math.Point2LL{{-118.0, 34.0}, {-118.1, 34.1}},
							Style:  LineStyleSolid,
						}},
					},
					{
						LabelLine1: "MVA", // duplicate on (L1,L2) — by design
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

// TestERAMBaseMap verifies that a group's always-displayed base map survives
// roundtrip with its geometry intact, that a group without one decodes to an
// empty BaseMap (the omitted-wire-key path that pre-base-map files also take),
// and that the base map is not exposed as a named, referenceable map.
func TestERAMBaseMap(t *testing.T) {
	orig := buildSyntheticLibrary()
	var buf bytes.Buffer
	if err := SaveMapLibrary(&buf, orig); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := decodeFromBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	want := orig.ERAMMapGroups["ZNYMAP"].BaseMap
	gotBase := got.ERAMMapGroups["ZNYMAP"].BaseMap
	if gotBase.IsEmpty() {
		t.Fatal("ZNYMAP: base map lost in roundtrip")
	}
	if !reflect.DeepEqual(gotBase, want) {
		t.Errorf("ZNYMAP base map mismatch:\n got %+v\nwant %+v", gotBase, want)
	}

	// ZLAWEST has no base map, so both wire keys are omitted.
	if b := got.ERAMMapGroups["ZLAWEST"].BaseMap; !b.IsEmpty() {
		t.Errorf("ZLAWEST: got base map %+v, want none", b)
	}

	// The base map has no label, so it must not be reachable as a normal map:
	// it consumes no filter-menu slot and can't be named from scenario JSON.
	if n := len(got.ERAMMapGroups["ZNYMAP"].Maps); n != len(orig.ERAMMapGroups["ZNYMAP"].Maps) {
		t.Errorf("ZNYMAP: got %d maps, want %d — base map leaked into Maps",
			n, len(orig.ERAMMapGroups["ZNYMAP"].Maps))
	}
	hdr, _, err := parseMapLibraryHeader(buf.Bytes())
	if err != nil {
		t.Fatalf("parseMapLibraryHeader: %v", err)
	}
	spec := &MapLibrarySpec{header: hdr}
	if spec.HasERAMMap(ERAMMapRef{Group: "ZNYMAP", Map: ""}) {
		t.Error("ZNYMAP: base map is referenceable by empty label")
	}

	// ZNYMAP also carries an unlabeled placeholder in Maps, holding an empty
	// filter-menu slot's position. Neither it nor the base map may be
	// nameable, or an empty "default_maps"/"always_maps" entry would validate.
	if spec.HasMap("") {
		t.Error(`HasMap("") is true; unlabeled placeholders must not be nameable`)
	}
	if _, _, ok := got.LookupERAMMap(ERAMMapRef{Group: "ZNYMAP", Map: ""}); ok {
		t.Error("LookupERAMMap resolved an empty label")
	}
}

// TestERAMMapRefLookup verifies that a (group, label) reference resolves to
// the map in that group even when another group uses the same label, and that
// the group it comes back with is the one whose BCG names its features index.
func TestERAMMapRefLookup(t *testing.T) {
	orig := buildSyntheticLibrary()
	var buf bytes.Buffer
	if err := SaveMapLibrary(&buf, orig); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := decodeFromBytes(buf.Bytes())
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	zny, znyGroup, ok := got.LookupERAMMap(ERAMMapRef{Group: "ZNYMAP", Map: "MVA"})
	if !ok {
		t.Fatal("ZNYMAP MVA not found")
	}
	if znyGroup.Name != "ZNYMAP" {
		t.Errorf("ZNYMAP MVA: came back with group %q", znyGroup.Name)
	}
	if !reflect.DeepEqual(znyGroup.BCGNames, orig.ERAMMapGroups["ZNYMAP"].BCGNames) {
		t.Errorf("ZNYMAP MVA: BCG names %v, want %v", znyGroup.BCGNames,
			orig.ERAMMapGroups["ZNYMAP"].BCGNames)
	}

	zla, zlaGroup, ok := got.LookupERAMMap(ERAMMapRef{Group: "ZLAWEST", Map: "MVA"})
	if !ok {
		t.Fatal("ZLAWEST MVA not found")
	}
	if zlaGroup.Name != "ZLAWEST" {
		t.Errorf("ZLAWEST MVA: came back with group %q", zlaGroup.Name)
	}
	if reflect.DeepEqual(zny.Lines[0].Points, zla.Lines[0].Points) {
		t.Error("the two MVA maps resolved to the same geometry")
	}

	if _, _, ok := got.LookupERAMMap(ERAMMapRef{Group: "ZNYMAP", Map: "NO SUCH"}); ok {
		t.Error("unknown label resolved")
	}
	if _, _, ok := got.LookupERAMMap(ERAMMapRef{Group: "NO SUCH", Map: "MVA"}); ok {
		t.Error("unknown group resolved")
	}

	hdr, _, err := parseMapLibraryHeader(buf.Bytes())
	if err != nil {
		t.Fatalf("parse header: %v", err)
	}
	spec := &MapLibrarySpec{header: hdr}
	for _, ref := range []ERAMMapRef{{Group: "ZNYMAP", Map: "MVA"}, {Group: "ZLAWEST", Map: "MVA"},
		{Group: "ZNYMAP", Map: "HIGH SECTOR"}} {
		if !spec.HasERAMMap(ref) {
			t.Errorf("spec missing %s", ref)
		}
	}
	for _, ref := range []ERAMMapRef{{Group: "ZNYMAP", Map: "NO SUCH"}, {Group: "NO SUCH", Map: "MVA"},
		{Group: "ZLAWEST", Map: "HIGH SECTOR"}} {
		if spec.HasERAMMap(ref) {
			t.Errorf("spec has %s, which doesn't exist", ref)
		}
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

// decodeFromBytes parses a serialized library from an in-memory buffer. It
// runs the loader's own decode path rather than a copy of it, so a field
// added to the wire format can't be silently untested here.
func decodeFromBytes(data []byte) (*MapLibrary, error) {
	return decodeMapLibrary(data, "<memory>")
}
