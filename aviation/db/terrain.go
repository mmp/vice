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
	lat, lon := p[1], p[0]-360*math.Floor((p[0]+180)/360)
	k := TerrainTileKey{Latitude: int(math.Floor(lat)), Longitude: int(math.Floor(lon))}
	tile, ok := g.tiles[k]
	if !ok {
		tile = g.decode(k)
		g.tiles[k] = tile
	}
	if tile == nil {
		return 0
	}
	row := math.Clamp(int((float32(k.Latitude+1)-lat)*TerrainTileSize), 0, TerrainTileSize-1)
	col := math.Clamp(int((lon-float32(k.Longitude))*TerrainTileSize), 0, TerrainTileSize-1)
	return 100 * int(tile[row*TerrainTileSize+col])
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
