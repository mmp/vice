// cmd/crc2vice/stars.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
	"golang.org/x/sync/errgroup"
)

func runSTARS(cwd, outDir, inputARTCC string, artcc *ARTCC, eg *errgroup.Group) error {
	// Index the top-level videoMaps catalog by ULID for fast per-facility
	// lookup.
	catalog := make(map[string]*ARTCCVideoMap, len(artcc.VideoMaps))
	for i := range artcc.VideoMaps {
		catalog[artcc.VideoMaps[i].ID] = &artcc.VideoMaps[i]
	}

	// Walk every STARS-equipped child facility under the ARTCC.
	for _, child := range artcc.Facility.ChildFacilities {
		if len(child.StarsConfiguration.VideoMapIds) > 0 {
			eg.Go(func() error {
				if err := writeFacility(cwd, outDir, inputARTCC, child.ID,
					child.StarsConfiguration.VideoMapIds, child.StarsConfiguration.MapGroups,
					catalog); err != nil {
					return fmt.Errorf("%s/%s: %v", inputARTCC, child.ID, err)
				}
				return nil
			})
		}
	}

	return nil
}

func writeFacility(cwd, outDir, artccID, childID string, ids []string, mapGroups []MapGroup,
	catalog map[string]*ARTCCVideoMap) error {
	lib := &av.MapLibrary{Maps: make(map[string]av.STARSMap, len(ids))}

	var eg errgroup.Group
	eg.SetLimit(16)
	var mu sync.Mutex

	for _, ulid := range ids {
		eg.Go(func() error {
			meta, ok := catalog[ulid]
			if !ok {
				return fmt.Errorf("video map %q referenced by %q but not in catalog; skipping", ulid, childID)
			}

			vm := av.STARSMap{
				Name:  meta.Name,
				Label: meta.ShortName,
				Id:    meta.StarsID,
				Group: util.Select(meta.StarsBrightnessCategory == "A", 0, 1),
			}
			if err := loadMapGeometry(cwd, artccID, ulid, &vm); err != nil {
				return err
			}

			mu.Lock()
			defer mu.Unlock()

			// Enforce within-facility Name uniqueness.
			if _, dup := lib.Maps[meta.Name]; dup {
				return fmt.Errorf("duplicate name %q in facility %s", meta.Name, childID)
			}

			lib.Maps[meta.Name] = vm
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	// Now that every map has been loaded, resolve starsId collisions.  Iterating in sorted-name
	// order makes the winner of each clash deterministic, and doing it post-load keeps a duplicate
	// from poaching an id that another map would have used.
	claimed := make(map[int]bool)
	for name, m := range util.SortedMap(lib.Maps) {
		if m.Id == 0 {
			continue
		}
		orig := m.Id
		for claimed[m.Id] {
			if m.Id = m.Id%999 + 1; m.Id == orig {
				return fmt.Errorf("%s/%s: no free starsId for %q", artccID, childID, name)
			}
		}
		if m.Id != orig {
			log.Printf("STARS [%s/%s] remapping starsId of %q due to duplication: %d -> %d",
				artccID, childID, name, orig, m.Id)
		}
		claimed[m.Id] = true
		lib.Maps[name] = m
	}

	outPath := filepath.Join(outDir, fmt.Sprintf("%s-%s.mappack", artccID, childID))
	log.Printf("STARS [%s/%s] %d video maps -> %s", artccID, childID, len(ids), outPath)
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	if err := av.SaveMapLibrary(f, lib); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return writeMapConfig(outDir, artccID, childID, mapGroups, lib)
}

// DCB layout: main bar has 3 columns × 2 rows of map buttons; the MAPS submenu has 15 × 2.
// vice stores both sections row-major (top row first, then bottom row) but CRC stores its
// mapGroup.mapIds column-major (consecutive entries are top/bottom of the same column), so
// we transpose each section before emitting.
const (
	mainDCBMapCols     = 3
	mapsSubmenuMapCols = 15
)

// writeMapConfig emits a per-TRACON sidecar JSON describing each mapGroup (the DCB-button layouts a
// controller sees). The output is wrapped in a "controllers" object so it can be pasted directly
// into a vice facility_adaptations block. Keys are the comma-joined TCP list (e.g. "1A,1D,1E").
func writeMapConfig(outDir, artccID, childID string, mapGroups []MapGroup, lib *av.MapLibrary) error {
	if len(mapGroups) == 0 {
		return nil
	}

	// The catalog can have multiple maps claiming the same raw starsId. The collision pass above
	// picks a single winner per starsId and renumbers the losers; the .mappack holds that resolution.
	// We index off the post-resolution library so each mapGroup slot resolves to the name vice will
	// actually render at that starsId.
	byStarsID := make(map[int]string, len(lib.Maps))
	for name, m := range lib.Maps {
		if m.Id != 0 {
			byStarsID[m.Id] = name
		}
	}

	type groupEntry struct {
		VideoMaps []string `json:"video_maps"`
	}
	controllers := make(map[string]groupEntry, len(mapGroups))

	for _, mg := range mapGroups {
		key := strings.Join(mg.Tcps, ",")
		// Resolve each CRC slot in source (column-major) order. Unresolved starsIds and nil
		// slots both render as "" so the column alignment isn't disturbed.
		raw := make([]string, len(mg.MapIds))
		for i, idp := range mg.MapIds {
			if idp == nil {
				continue
			}
			if name, ok := byStarsID[*idp]; ok {
				raw[i] = name
			} else {
				log.Printf("STARS [%s/%s] mapGroup [%s]: unresolved starsId %d",
					artccID, childID, key, *idp)
			}
		}
		// Transpose each section into vice's row-major layout. Main DCB is slots 0..5; MAPS
		// submenu is slots 6..35. CRC's data can have 38 slots (a 19-column layout) but vice
		// only renders 15 submenu columns, so the trailing two CRC slots are dropped.
		var names []string
		names = append(names, transposeColumnMajor(raw, 0, mainDCBMapCols)...)
		names = append(names, transposeColumnMajor(raw, 2*mainDCBMapCols, mapsSubmenuMapCols)...)
		controllers[key] = groupEntry{VideoMaps: names}
	}

	outPath := filepath.Join(outDir, fmt.Sprintf("%s-%s-maps.json", artccID, childID))
	data, err := json.MarshalIndent(map[string]any{"controllers": controllers}, "", "  ")
	if err != nil {
		return err
	}
	log.Printf("STARS [%s/%s] %d mapGroups -> %s", artccID, childID, len(mapGroups), outPath)
	return os.WriteFile(outPath, data, 0644)
}

// transposeColumnMajor reads 2*cols entries starting at base from a CRC column-major slice
// (where index 2k is the top of column k and 2k+1 is the bottom) and returns them in vice's
// row-major order: top row left-to-right, then bottom row left-to-right. Missing input
// entries become "".
func transposeColumnMajor(in []string, base, cols int) []string {
	out := make([]string, 2*cols)
	for col := range cols {
		for row := range 2 {
			src := base + col*2 + row
			if src < len(in) {
				out[row*cols+col] = in[src]
			}
		}
	}
	return out
}

// loadMapGeometry parses a single .geojson into the given VideoMap's
// Lines/Symbols/Labels.
func loadMapGeometry(cwd, artccID, ulid string, vm *av.STARSMap) error {
	path := filepath.Join(cwd, "VideoMaps", artccID, ulid+".geojson")
	src, err := loadGeoJSON(path)
	if err != nil {
		return err
	}
	appendFeatures(&src, featureSink{
		Lines:   &vm.Lines,
		Symbols: &vm.Symbols,
		Labels:  &vm.Labels,
	}, nil, nil)
	warnSTARSUnrenderable(artccID, vm)
	return nil
}

// warnSTARSUnrenderable logs warnings for STARS map features that have no
// rendering path yet (symbols, labels, non-solid lines). The features are
// kept on the map so the .mappack carries the full data — STARS just won't
// draw them today. When STARS rendering catches up to ERAM these maps will
// "wake up" automatically.
func warnSTARSUnrenderable(artccID string, vm *av.STARSMap) {
	if n := len(vm.Symbols); n > 0 {
		log.Printf("STARS [%s] %q: %d symbols present; STARS symbol rendering not implemented",
			artccID, vm.Name, n)
	}
	if n := len(vm.Labels); n > 0 {
		log.Printf("STARS [%s] %q: %d labels present; STARS label rendering not implemented",
			artccID, vm.Name, n)
	}
	for i, l := range vm.Lines {
		if l.Style != av.LineStyleSolid {
			log.Printf("STARS [%s] %q line %d: non-solid style %s; STARS dashed-line rendering not implemented",
				artccID, vm.Name, i, l.Style)
			break
		}
	}
}
