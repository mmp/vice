// cmd/crc2vice/eram.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// ERAM-mode import: walks facility.eramConfiguration.geoMaps and emits
// <ARTCC>.mappack.

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"golang.org/x/sync/errgroup"
)

func runERAM(cwd, outDir, inputARTCC string, artcc *ARTCC) error {
	geoMaps := artcc.Facility.ERAMConfiguration.GeoMaps
	if len(geoMaps) == 0 {
		return nil
	}
	log.Printf("found %d geomaps in eramConfiguration", len(geoMaps))

	lib := &av.MapLibrary{
		ERAMMapGroups: make(map[string]av.ERAMMapGroup, len(geoMaps)),
	}
	var totalMaps, totalLines, totalSymbols, totalLabels int

	for gi, geoMap := range geoMaps {
		log.Printf("ERAM [%d/%d] geomap %q  (%d filterMenu, %d bcgMenu, %d videoMaps)",
			gi+1, len(geoMaps), geoMap.Name, len(geoMap.FilterMenu), len(geoMap.BCGMenu),
			len(geoMap.VideoMapIds))

		// Build a per-filter-menu sink. filterMaps[fi] == nil means the
		// filter has empty labels and is skipped.
		filterMaps := make([]*av.ERAMMap, len(geoMap.FilterMenu))
		for fi, fm := range geoMap.FilterMenu {
			if fm.LabelLine1 == "" && fm.LabelLine2 == "" {
				continue
			}
			filterMaps[fi] = &av.ERAMMap{
				LabelLine1: fm.LabelLine1,
				LabelLine2: fm.LabelLine2,
			}
		}

		// Single pass per source: decode each feature once, then dispatch
		// to every matching filter bucket.
		sources, err := loadERAMSources(cwd, inputARTCC, geoMap.VideoMapIds)
		if err != nil {
			return err
		}
		drops := map[int]int{}
		for si := range sources {
			dispatchERAMFeatures(&sources[si], filterMaps, geoMap.BCGMenu, drops)
		}
		warnBCGDrops(geoMap.Name, geoMap.BCGMenu, drops)

		// Materialize per-filter ERAMMaps onto the group in filter-menu
		// order. The geoMap's bcgMenu rides through as the group's
		// BCGNames; per-feature BCGIndex (1-based) addresses into it at
		// draw time.
		group := av.ERAMMapGroup{
			Name:       geoMap.Name,
			LabelLine1: geoMap.LabelLine1,
			LabelLine2: geoMap.LabelLine2,
			BCGNames:   geoMap.BCGMenu,
		}
		for _, m := range filterMaps {
			if m == nil {
				continue
			}
			group.Maps = append(group.Maps, *m)
			totalMaps++
			totalLines += len(m.Lines)
			totalSymbols += len(m.Symbols)
			totalLabels += len(m.Labels)
		}
		lib.ERAMMapGroups[geoMap.Name] = group
	}

	outPath := filepath.Join(outDir, inputARTCC+".mappack")
	log.Printf("ERAM: %d maps, %d lines, %d symbols, %d labels across %d groups -> %s",
		totalMaps, totalLines, totalSymbols, totalLabels, len(lib.ERAMMapGroups), outPath)
	if f, err := os.Create(outPath); err != nil {
		return err
	} else if err := av.SaveMapLibrary(f, lib); err != nil {
		f.Close()
		return err
	} else {
		return f.Close()
	}
}

func loadERAMSources(cwd, artcc string, ids []string) ([]loadedSource, error) {
	srcs := make([]loadedSource, len(ids))

	var eg errgroup.Group
	eg.SetLimit(16)

	for i, id := range ids {
		eg.Go(func() error {
			path := filepath.Join(cwd, "VideoMaps", artcc, id+".geojson")
			var err error
			srcs[i], err = loadGeoJSON(path)
			return err
		})
	}

	return srcs, eg.Wait()
}

// dispatchERAMFeatures walks src's features once and emits each into every filter bucket listed in
// the feature's effective Filters set.
//
// filterMaps is indexed by 0-based filter index. A nil entry means the filter is skipped (empty
// labels). The renderer resolves BCG (brightness) per feature against the group's BCGNames at draw
// time; this importer passes BCGIndex through. Features whose BCG is unset, out-of-range, or
// points at an empty-named slot would render as invisible black at runtime (bcgRGB[0] and empty
// slots are never populated), so we drop them entirely here and tally one count per dropped
// source feature (not per filter placement) in drops, keyed by the raw effective BCG value so
// out-of-range originals like 300 are reported as 300 rather than the post-clamp 255.
func dispatchERAMFeatures(src *loadedSource, filterMaps []*av.ERAMMap, bcgMenu []string, drops map[int]int) {
	clampPositive := func(v, dflt int) int {
		switch {
		case v <= 0:
			return dflt
		case v > 255:
			return 255
		default:
			return v
		}
	}
	// True if the raw BCG cannot resolve to a populated, brightness-controllable slot. Checked
	// against the raw eff.BCG so we don't confuse "source said 300" with "source said 255" after
	// clamping into uint8.
	invalidSlot := func(raw int) bool {
		return raw <= 0 || raw > len(bcgMenu) || bcgMenu[raw-1] == ""
	}

	for i := range src.features {
		f := &src.features[i]
		switch f.Geometry.Type {
		case "LineString", "MultiLineString":
			polylines := decodePolylines(f.Geometry.Type, f.Geometry.Coordinates)
			if len(polylines) == 0 {
				continue
			}
			eff := mergeDefaults(f.Properties, &src.lineDefaults)
			if invalidSlot(eff.BCG) {
				drops[eff.BCG]++
				continue
			}
			style := parseLineStyle(eff.Style)
			thickness := uint8(clampPositive(eff.Thickness, 1))
			bcg := uint8(eff.BCG) // safe: invalidSlot rejected anything > len(bcgMenu) (≤255 in practice).
			for _, filterIdx := range eff.Filters {
				fi := filterIdx - 1
				if fi < 0 || fi >= len(filterMaps) || filterMaps[fi] == nil {
					continue
				}
				for _, pts := range polylines {
					if len(pts) < 2 {
						continue
					}
					filterMaps[fi].Lines = append(filterMaps[fi].Lines, av.MapLine{
						Points:    pts,
						Style:     style,
						Thickness: thickness,
						BCGIndex:  bcg,
					})
				}
			}

		case "Point":
			p, ok := decodePoint(f.Geometry.Coordinates)
			if !ok {
				continue
			}
			if f.Properties != nil && len(f.Properties.Text) > 0 {
				eff := mergeDefaults(f.Properties, &src.textDefaults)
				if invalidSlot(eff.BCG) {
					drops[eff.BCG]++
					continue
				}
				size := uint8(clampPositive(eff.Size, 1))
				xoff := int8(eff.XOffset)
				yoff := int8(eff.YOffset)
				bcg := uint8(eff.BCG)
				// Join multi-line labels into a single MapLabel.
				text := strings.Join(f.Properties.Text, "\n")
				for _, filterIdx := range eff.Filters {
					fi := filterIdx - 1
					if fi < 0 || fi >= len(filterMaps) || filterMaps[fi] == nil {
						continue
					}
					filterMaps[fi].Labels = append(filterMaps[fi].Labels, av.MapLabel{
						P:         p,
						Text:      text,
						Size:      size,
						XOffset:   xoff,
						YOffset:   yoff,
						Underline: eff.Underline,
						Opaque:    eff.Opaque,
						BCGIndex:  bcg,
					})
				}
			} else {
				eff := mergeDefaults(f.Properties, &src.symbolDefaults)
				if invalidSlot(eff.BCG) {
					drops[eff.BCG]++
					continue
				}
				style, known := parseSymbolStyle(eff.Style)
				if !known {
					if eff.Style != "" {
						warnUnknownStyle(src.path, eff.Style)
					}
					style = av.SymbolStyleVOR
				}
				size := uint8(clampPositive(eff.Size, 1))
				bcg := uint8(eff.BCG)
				for _, filterIdx := range eff.Filters {
					fi := filterIdx - 1
					if fi < 0 || fi >= len(filterMaps) || filterMaps[fi] == nil {
						continue
					}
					filterMaps[fi].Symbols = append(filterMaps[fi].Symbols, av.MapSymbol{
						P:        p,
						Style:    style,
						Size:     size,
						BCGIndex: bcg,
					})
				}
			}
		}
	}
}

// warnBCGDrops prints one summary line per (geomap, raw BCG value) with dropped source features.
// Keys are the raw eff.BCG values (not clamped) so out-of-range or unset BCGs are reported as the
// upstream source said them.
func warnBCGDrops(geomapName string, bcgMenu []string, drops map[int]int) {
	if len(drops) == 0 {
		return
	}
	bcgs := make([]int, 0, len(drops))
	for b := range drops {
		bcgs = append(bcgs, b)
	}
	slices.Sort(bcgs)
	for _, b := range bcgs {
		var reason string
		switch {
		case b <= 0:
			reason = "no BCG set on feature or its source-file defaults"
		case b > len(bcgMenu):
			reason = fmt.Sprintf("out of range (bcgMenu has %d slots)", len(bcgMenu))
		default:
			reason = "empty slot name"
		}
		log.Printf("WARN: ERAM geomap %q: dropped %d features referencing BCG slot %d (%s)",
			geomapName, drops[b], b, reason)
	}
}
