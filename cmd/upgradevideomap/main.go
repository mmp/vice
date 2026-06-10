// cmd/upgradevideomap/main.go
//
// One-shot converter from the legacy video map format
// (<file>-videomaps.gob.zst paired with <file>-manifest.gob) to the
// current VMV2 .mappack format. Useful for facilities whose CRC/DAT
// source files aren't on hand at upgrade time. For facilities where
// the source files are available, prefer re-running cmd/crc2vice or
// cmd/dat2vice instead.
//
// Usage:
//
//	upgradevideomap <basename>-videomaps.gob.zst [<more>...]
//
// Each input is read, decoded, and re-emitted as <basename>.mappack
// next to the original.

package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
)

// Legacy types — these mirror the pre-VMV1 sim.VideoMap shape so the gob
// decoder can land into them. The new sim.VideoMap has different
// fields, so we can't reuse it directly.

type legacyVideoMap struct {
	Label       string
	Group       int
	Name        string
	Id          int
	Category    int
	Restriction struct {
		Id        int
		Text      [2]string
		TextBlink bool
		HideText  bool
	}
	Color int
	Lines [][]math.Point2LL
}

type legacyERAMMap struct {
	BcgName    string
	LabelLine1 string
	LabelLine2 string
	Name       string
	Lines      [][]math.Point2LL
}

type legacyERAMMapGroup struct {
	Maps       []legacyERAMMap
	LabelLine1 string
	LabelLine2 string
}

type legacyERAMMapGroups map[string]legacyERAMMapGroup

type legacyVideoMapLibrary struct {
	Maps          []legacyVideoMap
	ERAMMapGroups legacyERAMMapGroups
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr,
			"usage: upgradevideomap <basename>-videomaps.gob.zst [<more>...]\n")
		os.Exit(1)
	}
	for _, in := range os.Args[1:] {
		if err := upgrade(in); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", in, err)
			os.Exit(1)
		}
	}
}

func upgrade(inPath string) error {
	data, err := os.ReadFile(inPath)
	if err != nil {
		return err
	}

	lib, err := decodeLegacy(data, strings.Contains(inPath, "eram"))
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	newLib := convert(lib)

	outPath := strings.TrimSuffix(inPath, ".zst")
	outPath = strings.TrimSuffix(outPath, "-videomaps.gob")
	outPath += ".mappack"

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := sim.SaveVideoMapLibrary(f, newLib); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Printf("%s -> %s (%d STARS maps, %d ERAM groups)\n",
		inPath, outPath, len(newLib.Maps), len(newLib.ERAMMapGroups))
	return nil
}

// decodeLegacy peels the zstd wrapper (if present) and runs the gob
// decoder, falling back to the "bare slice" gob shape that some old
// files used.
func decodeLegacy(contents []byte, isEram bool) (*legacyVideoMapLibrary, error) {
	br := bytes.NewReader(contents)
	var r io.Reader = br
	if len(contents) > 4 && contents[0] == 0x28 && contents[1] == 0xb5 &&
		contents[2] == 0x2f && contents[3] == 0xfd {
		zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(0))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	}

	var lib legacyVideoMapLibrary
	if err := gob.NewDecoder(r).Decode(&lib); err == nil {
		return &lib, nil
	}

	// Old-old format: just a slice (STARS) or just a map (ERAM).
	br = bytes.NewReader(contents)
	r = br
	if len(contents) > 4 && contents[0] == 0x28 && contents[1] == 0xb5 &&
		contents[2] == 0x2f && contents[3] == 0xfd {
		zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(0))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	}
	if isEram {
		groups := make(legacyERAMMapGroups)
		if err := gob.NewDecoder(r).Decode(&groups); err != nil {
			return nil, err
		}
		return &legacyVideoMapLibrary{ERAMMapGroups: groups}, nil
	}
	var maps []legacyVideoMap
	if err := gob.NewDecoder(r).Decode(&maps); err != nil {
		return nil, err
	}
	return &legacyVideoMapLibrary{Maps: maps}, nil
}

// convert turns a legacy library into a new one. Legacy files have no
// symbols or labels and line style was already pre-expanded into many
// short segments at import time, so the conversion lifts every line as
// LineStyleSolid.
func convert(lib *legacyVideoMapLibrary) *sim.VideoMapLibrary {
	out := &sim.VideoMapLibrary{
		Maps:          make(map[string]sim.VideoMap, len(lib.Maps)),
		ERAMMapGroups: make(map[string]sim.ERAMMapGroup, len(lib.ERAMMapGroups)),
	}

	for _, m := range lib.Maps {
		if _, dup := out.Maps[m.Name]; dup {
			// Append " #N" until unique. The legacy format allowed name
			// collisions; the new format doesn't. This is a fail-soft.
			base := m.Name
			for n := 2; ; n++ {
				candidate := fmt.Sprintf("%s #%d", base, n)
				if _, dup := out.Maps[candidate]; !dup {
					m.Name = candidate
					break
				}
			}
		}
		out.Maps[m.Name] = sim.VideoMap{
			Name:     m.Name,
			Label:    m.Label,
			Id:       m.Id,
			Group:    m.Group,
			Category: m.Category,
			Color:    m.Color,
			Lines:    liftLines(m.Lines),
		}
	}

	for gn, g := range lib.ERAMMapGroups {
		newGroup := sim.ERAMMapGroup{
			Name:       gn,
			LabelLine1: g.LabelLine1,
			LabelLine2: g.LabelLine2,
			Maps:       make([]sim.ERAMMap, len(g.Maps)),
		}
		for i, m := range g.Maps {
			newGroup.Maps[i] = sim.ERAMMap{
				LabelLine1: m.LabelLine1,
				LabelLine2: m.LabelLine2,
				BCGName:    m.BcgName,
				Lines:      liftLines(m.Lines),
			}
		}
		out.ERAMMapGroups[gn] = newGroup
	}

	return out
}

func liftLines(in [][]math.Point2LL) []sim.VideoMapLine {
	if len(in) == 0 {
		return nil
	}
	out := make([]sim.VideoMapLine, 0, len(in))
	for _, pts := range in {
		if len(pts) < 2 {
			continue
		}
		out = append(out, sim.VideoMapLine{
			Points:    pts,
			Style:     sim.LineStyleSolid,
			Thickness: 1,
		})
	}
	return out
}
