// aviation/db/terrain.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/klauspost/compress/zstd"
)

// TerrainTileSize is the number of 15 arc-second cells along each side of a
// one-degree terrain tile.
const TerrainTileSize = 240

// Terrain holds the compressed tiles of the terrain grid: for each 15
// arc-second cell, the highest terrain within 0.5 nm, in hundreds of feet
// rounded up, from the GMTED2010 maximum-elevation data. Obstacles such as
// towers and antennas are not included. Tiles cover the US, Alaska, Hawaii
// and Puerto Rico; elsewhere there is no tile and the grid reads as sea
// level.
//
// A tile decompresses with zstd to TerrainTileSize rows of TerrainTileSize
// cells, a byte each, row 0 along its north edge and column 0 along its west
// edge.
type Terrain struct {
	Tiles map[TerrainTileKey][]byte
}

// TerrainTileKey is the southwest corner of a one-degree tile.
type TerrainTileKey struct{ Latitude, Longitude int }

// FileName returns the name of the tile in the terrain resource, e.g.
// N39W107.zst for 39 to 40N and 107 to 106W.
func (k TerrainTileKey) FileName() string {
	ns, lat := util.Select(k.Latitude < 0, 'S', 'N'), math.Abs(k.Latitude)
	ew, lon := util.Select(k.Longitude < 0, 'W', 'E'), math.Abs(k.Longitude)
	return fmt.Sprintf("%c%02d%c%03d.zst", ns, lat, ew, lon)
}

func parseTerrainTileKey(name string) (TerrainTileKey, error) {
	var ns, ew rune
	var k TerrainTileKey
	if _, err := fmt.Sscanf(name, "%c%2d%c%3d.zst", &ns, &k.Latitude, &ew, &k.Longitude); err != nil {
		return k, fmt.Errorf("%s: %w", name, err)
	}
	if ns == 'S' {
		k.Latitude = -k.Latitude
	}
	if ew == 'W' {
		k.Longitude = -k.Longitude
	}
	if k.FileName() != name {
		return k, fmt.Errorf("%s: malformed terrain tile name", name)
	}
	return k, nil
}

func parseTerrain() *Terrain {
	z := util.LoadResourceBytes("terrain-gmted2010.zip")
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		panic(err)
	}

	t := &Terrain{Tiles: make(map[TerrainTileKey][]byte, len(zr.File))}
	for _, f := range zr.File {
		k, err := parseTerrainTileKey(f.Name)
		if err != nil {
			panic(err)
		}
		r, err := f.Open()
		if err != nil {
			panic(err)
		}
		if t.Tiles[k], err = io.ReadAll(r); err != nil {
			panic(err)
		}
		r.Close()
	}
	return t
}

// terrainDecoder decompresses terrain tiles. Its DecodeAll is safe for
// concurrent use.
var terrainDecoder = sync.OnceValue(func() *zstd.Decoder {
	d, err := zstd.NewReader(nil)
	if err != nil {
		panic(err)
	}
	return d
})

// MakeGrid returns a TerrainGrid that decompresses tiles from t as they are
// needed. Each Sim makes its own so that what it decompressed goes away with
// it.
func (t *Terrain) MakeGrid() *TerrainGrid {
	return &TerrainGrid{
		terrain: t,
		tiles:   make(map[TerrainTileKey]*[TerrainTileSize * TerrainTileSize]uint8),
	}
}

// TerrainGrid answers terrain lookups for one user of the data; like the Sim
// that owns it, it is not safe for concurrent use.
type TerrainGrid struct {
	terrain *Terrain
	tiles   map[TerrainTileKey]*[TerrainTileSize * TerrainTileSize]uint8 // nil where there is no tile
}

// HighestTerrain returns the highest terrain within 0.5 nm of p in feet MSL,
// rounded up to the next 100', or 0 where there is no data.
func (g *TerrainGrid) HighestTerrain(p math.Point2LL) int {
	return g.HighestTerrainIn(math.Extent2D{P0: p, P1: p})
}

// HighestTerrainIn returns the highest terrain within 0.5 nm of the given
// latitude-longitude bounds in feet MSL, rounded up to the next 100', or 0
// where there is no data.
func (g *TerrainGrid) HighestTerrainIn(e math.Extent2D) int {
	// Tiles are keyed by longitudes in [-180, 180); bounds that cross the
	// dateline continue into the tiles on the far side of it.
	wrap := 360 * math.Floor((e.P0[0]+180)/360)
	lon0, lon1 := e.P0[0]-wrap, e.P1[0]-wrap
	cell := func(v, origin float32) int {
		return math.Clamp(int((v-origin)*TerrainTileSize), 0, TerrainTileSize-1)
	}

	var highest uint8
	for lat := int(math.Floor(e.P0[1])); lat <= int(math.Floor(e.P1[1])); lat++ {
		for lon := int(math.Floor(lon0)); lon <= int(math.Floor(lon1)); lon++ {
			k := TerrainTileKey{Latitude: lat, Longitude: lon - 360*int(math.Floor(float32(lon+180)/360))}
			tile := g.tile(k)
			if tile == nil {
				continue
			}
			// Rows run south from the tile's north edge.
			top := float32(lat + 1)
			for row := cell(top-e.P1[1], 0); row <= cell(top-e.P0[1], 0); row++ {
				for col := cell(lon0, float32(lon)); col <= cell(lon1, float32(lon)); col++ {
					highest = max(highest, tile[row*TerrainTileSize+col])
				}
			}
		}
	}
	return 100 * int(highest)
}

// tile returns the tile for k, decompressing it the first time it is
// asked for, or nil if there is none.
func (g *TerrainGrid) tile(k TerrainTileKey) *[TerrainTileSize * TerrainTileSize]uint8 {
	tile, ok := g.tiles[k]
	if !ok {
		tile = g.decode(k)
		g.tiles[k] = tile
	}
	return tile
}

func (g *TerrainGrid) decode(k TerrainTileKey) *[TerrainTileSize * TerrainTileSize]uint8 {
	z, ok := g.terrain.Tiles[k]
	if !ok {
		return nil
	}
	b, err := terrainDecoder().DecodeAll(z, make([]byte, 0, TerrainTileSize*TerrainTileSize))
	if err != nil {
		panic(fmt.Sprintf("%s: %v", k.FileName(), err))
	}
	if len(b) != TerrainTileSize*TerrainTileSize {
		panic(fmt.Sprintf("%s: %d bytes, expected %d", k.FileName(), len(b), TerrainTileSize*TerrainTileSize))
	}
	return (*[TerrainTileSize * TerrainTileSize]uint8)(b)
}
