// cmd/wxgridviz/main.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// wxgridviz visualizes grid coverage from processed atmospheric files.
// Usage: wxgridviz <atmos-file.msgpack.zst>
//
// Loads a processed AtmosByPointSOA file, converts it to AtmosGrid
// as it would be at runtime, and outputs an ASCII representation
// showing how many samples hit each grid cell.

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/mmp/vice/wx"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: wxgridviz <atmos-file.msgpack.zst>")
		os.Exit(1)
	}

	for _, path := range os.Args[1:] {
		if err := visualizeGrid(path); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		}
	}
}

func visualizeGrid(path string) error {
	// Load the compressed msgpack file
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()

	data, err := io.ReadAll(zr)
	if err != nil {
		return err
	}

	var soa wx.AtmosByPointSOA
	if err := msgpack.Unmarshal(data, &soa); err != nil {
		return err
	}

	// Convert to AtmosByPoint and then to AtmosGrid (as happens at runtime)
	aos := soa.ToAOS()
	grid := aos.GetGrid()

	// Compute coverage statistics
	numSamples := len(aos.SampleStacks)
	totalCells := grid.Res[0] * grid.Res[1]

	// Count samples per cell for the lowest altitude level
	cellCounts := make([]int, grid.Res[0]*grid.Res[1])
	for loc := range aos.SampleStacks {
		// Map point to grid cell using the grid's method (handles lonShift for date line)
		pg := grid.PtToGrid(loc)
		x := int(pg[0] + 0.5)
		y := int(pg[1] + 0.5)
		if x >= 0 && x < grid.Res[0] && y >= 0 && y < grid.Res[1] {
			cellCounts[x+y*grid.Res[0]]++
		}
	}

	fmt.Printf("\n=== %s ===\n", path)
	fmt.Printf("Sample points: %d\n", numSamples)
	fmt.Printf("Grid dimensions: %dx%d (%d cells)\n", grid.Res[0], grid.Res[1], totalCells)
	fmt.Printf("Grid extent: lon [%.4f, %.4f], lat [%.4f, %.4f]\n",
		grid.Extent.P0[0], grid.Extent.P1[0], grid.Extent.P0[1], grid.Extent.P1[1])
	fmt.Printf("Sample density: %.2f samples/cell\n", float64(numSamples)/float64(totalCells))

	fmt.Printf("\nGrid visualization:\n")

	// Print from top (high lat) to bottom (low lat)
	for y := grid.Res[1] - 1; y >= 0; y-- {
		for x := 0; x < grid.Res[0]; x++ {
			c := cellCounts[x+y*grid.Res[0]]
			switch {
			case c == 0:
				fmt.Print(".")
			case c <= 9:
				fmt.Printf("%d", c)
			default:
				fmt.Print("+")
			}
		}
		fmt.Println()
	}

	return nil
}
