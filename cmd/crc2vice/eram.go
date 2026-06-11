// cmd/crc2vice/eram.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// ERAM-mode import: walks facility.eramConfiguration.geoMaps and emits
// <ARTCC>.mappack.

package main

import (
	"log"
	"os"
	"path/filepath"
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
		// filter has empty labels and is skipped. groupBCGs[fi] is the
		// BCG name we'll attach to filterMaps[fi]; it may get promoted
		// from a per-feature override during the pass.
		filterMaps := make([]*av.ERAMMap, len(geoMap.FilterMenu))
		groupBCGs := make([]string, len(geoMap.FilterMenu))
		for fi, fm := range geoMap.FilterMenu {
			if fm.LabelLine1 == "" && fm.LabelLine2 == "" {
				continue
			}
			filterMaps[fi] = &av.ERAMMap{
				LabelLine1: fm.LabelLine1,
				LabelLine2: fm.LabelLine2,
			}
			if fi < len(geoMap.BCGMenu) {
				groupBCGs[fi] = geoMap.BCGMenu[fi]
			}
		}

		// Single pass per source: decode each feature once, then dispatch
		// to every matching filter bucket.
		sources, err := loadERAMSources(cwd, inputARTCC, geoMap.VideoMapIds)
		if err != nil {
			return err
		}
		for si := range sources {
			dispatchERAMFeatures(&sources[si], filterMaps, groupBCGs, geoMap.BCGMenu)
		}

		// Materialize per-filter ERAMMaps onto the group in filter-menu
		// order.
		group := av.ERAMMapGroup{
			Name:       geoMap.Name,
			LabelLine1: geoMap.LabelLine1,
			LabelLine2: geoMap.LabelLine2,
		}
		for fi, m := range filterMaps {
			if m == nil {
				continue
			}
			m.BCGName = groupBCGs[fi]
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
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := av.SaveMapLibrary(f, lib); err != nil {
		return err
	}
	return f.Close()
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
// labels). groupBCGs[fi] is the per-filter BCG name; it may be promoted from a per-feature BCG by
// the maybePromoteBCG helper inlined into this function.
func dispatchERAMFeatures(src *loadedSource, filterMaps []*av.ERAMMap, groupBCGs []string, bcgMenu []string) {
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
	promoteBCG := func(fi, featureBCG int) {
		if groupBCGs[fi] != "" {
			return
		}
		if featureBCG-1 >= 0 && featureBCG-1 < len(bcgMenu) && bcgMenu[featureBCG-1] != "" {
			groupBCGs[fi] = bcgMenu[featureBCG-1]
		}
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
			style := parseLineStyle(eff.Style)
			thickness := uint8(clampPositive(eff.Thickness, 1))
			bcg := uint8(clampPositive(eff.BCG, 0))
			for _, filterIdx := range eff.Filters {
				fi := filterIdx - 1
				if fi < 0 || fi >= len(filterMaps) || filterMaps[fi] == nil {
					continue
				}
				promoteBCG(fi, eff.BCG)
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
				size := uint8(clampPositive(eff.Size, 1))
				xoff := int8(eff.XOffset)
				yoff := int8(eff.YOffset)
				bcg := uint8(clampPositive(eff.BCG, 0))
				// Join multi-line labels into a single MapLabel.
				text := strings.Join(f.Properties.Text, "\n")
				for _, filterIdx := range eff.Filters {
					fi := filterIdx - 1
					if fi < 0 || fi >= len(filterMaps) || filterMaps[fi] == nil {
						continue
					}
					promoteBCG(fi, eff.BCG)
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
				style, known := parseSymbolStyle(eff.Style)
				if !known {
					if eff.Style != "" {
						warnUnknownStyle(src.path, eff.Style)
					}
					style = av.SymbolStyleVOR
				}
				size := uint8(clampPositive(eff.Size, 1))
				bcg := uint8(clampPositive(eff.BCG, 0))
				for _, filterIdx := range eff.Filters {
					fi := filterIdx - 1
					if fi < 0 || fi >= len(filterMaps) || filterMaps[fi] == nil {
						continue
					}
					promoteBCG(fi, eff.BCG)
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
