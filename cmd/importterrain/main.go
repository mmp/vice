// cmd/importterrain/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Imports terrain elevations into resources/terrain-gmted2010.zip, which VFR
// flights use to stay clear of the ground. Input is the 7.5 arc-second
// maximum-elevation product of the USGS Global Multi-resolution Terrain
// Elevation Data 2010 (GMTED2010), where each cell holds the highest
// elevation of the finer source data within it. It comes in 20 by 30 degree
// tiles; the eleven that cover the US, Alaska, Hawaii, and Puerto Rico are
// downloaded, about 277 MB each, to the -tiles directory unless they are
// already there:
//
//	https://edcintl.cr.usgs.gov/downloads/sciweb1/shared/topo/downloads/GMTED/Global_tiles_GMTED/075darcsec/max/
//
// The output is the grid that db.Terrain describes: 15 arc-second cells
// holding the highest terrain within 0.5 nm, in hundreds of feet rounded up,
// in one-degree tiles. So that it compresses better, a cell may hold up to
// 100' more than that where doing so repeats the cell above or to its left.
// Tiles that are all sea level are left out.
//
// Must be run from the top of a Vice checkout so that the resources are found.
//
// Usage:
//
//	importterrain [-tiles dir] [-out file] [-dryrun]

package main

import (
	"archive/zip"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"

	"github.com/klauspost/compress/zstd"
)

const gmtedURL = "https://edcintl.cr.usgs.gov/downloads/sciweb1/shared/topo/downloads/GMTED/Global_tiles_GMTED/075darcsec/max/"

// gmtedTile is a source tile, identified by its southwest corner.
type gmtedTile struct{ lat, lon int }

var gmtedTiles = []gmtedTile{
	// The contiguous US; 10N090W also covers Puerto Rico and the Virgin
	// Islands.
	{30, -150}, {30, -120}, {30, -90}, {10, -120}, {10, -90},
	// Alaska, including the Aleutians west of the dateline.
	{50, -180}, {50, -150}, {70, -180}, {70, -150}, {50, 150},
	// Hawaii.
	{10, -180},
}

// name returns the tile's name as USGS gives it, e.g. 30N120W.
func (t gmtedTile) name() string {
	ns, ew := util.Select(t.lat < 0, 'S', 'N'), util.Select(t.lon < 0, 'W', 'E')
	return fmt.Sprintf("%02d%c%03d%c", math.Abs(t.lat), ns, math.Abs(t.lon), ew)
}

func (t gmtedTile) url() string {
	n := t.name()
	return gmtedURL + n[len(n)-1:] + n[3:len(n)-1] + "/" + n + "_20101117_gmted_max075.tif"
}

// coverage gives the regions written out, as ranges of whole degrees of
// latitude and longitude; terrain lookups elsewhere read as sea level.
var coverage = []struct{ lat0, lat1, lon0, lon1 int }{
	{24, 50, -125, -66},  // the contiguous US
	{51, 72, -180, -129}, // Alaska
	{51, 55, 172, 180},   // the Aleutians west of the dateline
	{18, 23, -161, -154}, // Hawaii
	{17, 19, -68, -64},   // Puerto Rico and the Virgin Islands
}

func main() {
	tilesDir := flag.String("tiles", defaultTilesDir(), "`directory` holding the GMTED2010 tiles; missing ones are downloaded to it")
	out := flag.String("out", "resources/terrain-gmted2010.zip", "`file` to write the terrain to")
	dryRun := flag.Bool("dryrun", false, "report what would be written without writing it")
	flag.Parse()

	if err := run(*tilesDir, *out, *dryRun); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func defaultTilesDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "vice", "gmted2010")
	}
	return ""
}

func run(tilesDir, out string, dryRun bool) error {
	if tilesDir == "" {
		return errors.New("no -tiles directory given")
	}
	if err := os.MkdirAll(tilesDir, 0o755); err != nil {
		return err
	}

	var g sourceGrid
	for _, t := range gmtedTiles {
		path := filepath.Join(tilesDir, filepath.Base(t.url()))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := download(t.url(), path); err != nil {
				return err
			}
		}
		fmt.Printf("Reading %s\n", path)
		elev, err := readGMTED(path, t)
		if err != nil {
			return err
		}
		g.add(t, reduce(elev))
	}

	var keys []db.TerrainTileKey
	for _, c := range coverage {
		for lat := c.lat0; lat < c.lat1; lat++ {
			for lon := c.lon0; lon < c.lon1; lon++ {
				keys = append(keys, db.TerrainTileKey{Latitude: lat, Longitude: lon})
			}
		}
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	compressed := make([][]byte, len(keys))
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Go(func() {
			v := g.filterTile(k)
			if slices.ContainsFunc(v, func(c uint8) bool { return c != 0 }) {
				compressed[i] = enc.EncodeAll(snap(v), nil)
			}
		})
	}
	wg.Wait()

	terrain := &db.Terrain{Tiles: make(map[db.TerrainTileKey][]byte)}
	var size int
	for i, k := range keys {
		if compressed[i] != nil {
			terrain.Tiles[k] = compressed[i]
			size += len(compressed[i])
		}
	}
	fmt.Printf("%d tiles, %d bytes\n", len(terrain.Tiles), size)

	if err := spotCheck(terrain); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	if err := write(terrain, out); err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	fmt.Printf("Wrote %s\n", out)
	return nil
}

// download fetches url to path, resuming after a dropped connection.
// It is written to a partial file first so that a file at path is always
// complete.
func download(url, path string) error {
	fmt.Printf("Downloading %s\n", url)
	part := path + ".part"
	const attempts = 10
	for i := 1; ; i++ {
		err := fetch(url, part)
		if err == nil {
			return os.Rename(part, path)
		}
		if i == attempts {
			return fmt.Errorf("%s: %w", url, err)
		}
		fmt.Printf("%s: %v; retrying\n", url, err)
	}
}

// fetch adds the rest of url to the partial download at part and checks that
// it then holds all of it.
func fetch(url, part string) error {
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	have := fi.Size()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var total int64
	switch resp.StatusCode {
	case http.StatusOK:
		if err := f.Truncate(0); err != nil {
			return err
		}
		have, total = 0, resp.ContentLength
	case http.StatusPartialContent:
		// Content-Range: bytes first-last/total
		cr := resp.Header.Get("Content-Range")
		if total, err = strconv.ParseInt(cr[strings.LastIndex(cr, "/")+1:], 10, 64); err != nil {
			return fmt.Errorf("Content-Range %q: %w", cr, err)
		}
	case http.StatusRequestedRangeNotSatisfiable:
		// The partial file is as long as the whole one, or longer; start over.
		if err := f.Truncate(0); err != nil {
			return err
		}
		return errors.New(resp.Status)
	default:
		return errors.New(resp.Status)
	}
	if total <= 0 {
		return errors.New("server did not give the file's length")
	}

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	if have+n != total {
		return fmt.Errorf("got %d of %d bytes", have+n, total)
	}
	return nil
}

// spotCheck looks up places whose terrain is known, through the same code
// the sim uses, so that a bad run is obvious.
func spotCheck(t *db.Terrain) error {
	g := t.MakeGrid()
	checks := []struct {
		name    string
		p       math.Point2LL
		low, hi int
	}{
		{"Denali", math.Point2LL{-151.0070, 63.0692}, 20300, 20300},
		{"KASE", math.Point2LL{-106.8688, 39.2232}, 8500, 8600},
		{"Badwater Basin", math.Point2LL{-116.7700, 36.2300}, 2400, 2500}, // below sea level, at the foot of a ridge
		{"KDDC", math.Point2LL{-99.9656, 37.7634}, 2600, 2800},
		{"Pacific Ocean", math.Point2LL{-140, 30}, 0, 0},
	}
	var failed []string
	for _, c := range checks {
		alt := g.HighestTerrain(c.p)
		ok := alt >= c.low && alt <= c.hi
		fmt.Printf("%-15s %6d' (expected %d' to %d')%s\n", c.name, alt, c.low, c.hi, util.Select(ok, "", " MISMATCH"))
		if !ok {
			failed = append(failed, c.name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("unexpected terrain at %s", strings.Join(failed, ", "))
	}
	return nil
}

// write writes the tiles to a zip file. They are already compressed, so the
// zip stores them as they are.
func write(t *db.Terrain, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	keys := slices.SortedFunc(maps.Keys(t.Tiles), func(a, b db.TerrainTileKey) int {
		return strings.Compare(a.FileName(), b.FileName())
	})
	for _, k := range keys {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: k.FileName(), Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err := w.Write(t.Tiles[k]); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}
