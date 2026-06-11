// cmd/crc2vice/stars.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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
				if err := writeFacility(cwd, outDir, inputARTCC, child.ID, child.StarsConfiguration.VideoMapIds,
					catalog); err != nil {
					return fmt.Errorf("%s/%s: %v", inputARTCC, child.ID, err)
				}
				return nil
			})
		}
	}

	return nil
}

func writeFacility(cwd, outDir, artccID, childID string, ids []string, catalog map[string]*ARTCCVideoMap) error {
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
	if f, err := os.Create(outPath); err != nil {
		return err
	} else if err := av.SaveMapLibrary(f, lib); err != nil {
		f.Close()
		return err
	} else {
		return f.Close()
	}
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
