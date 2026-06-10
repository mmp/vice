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
	usedIds := make(map[int]string) // Id -> Name for duplicate detection

	var eg errgroup.Group
	eg.SetLimit(16)
	var mu sync.Mutex

	for _, ulid := range ids {
		eg.Go(func() error {
			meta, ok := catalog[ulid]
			if !ok {
				return fmt.Errorf("video map %q referenced by %q but not in catalog; skipping", ulid, childID)
			}

			id := meta.StarsID
			vm := av.STARSMap{
				Name:  meta.Name,
				Label: meta.ShortName,
				Id:    id,
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

			// Enforce within-facility starsId uniqueness when non-zero.
			if id != 0 {
				other, dup := usedIds[id]
				if dup {
					return fmt.Errorf("duplicate starsId %d: %q and %q", id, other, meta.Name)
				}
				usedIds[id] = meta.Name
			}

			lib.Maps[meta.Name] = vm
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	outPath := filepath.Join(outDir, fmt.Sprintf("%s-%s.mappack", artccID, childID))
	log.Printf("STARS [%s/%s] %d video maps -> %s", artccID, childID, len(ids), outPath)
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
	return nil
}
