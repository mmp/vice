// hacky utility to convert BDF font files for STARS cursors
//
// go run ~/vice/util/bdfdump.go < cursors.bdf  >| stars-cursors.go && gofmt -w stars-cursors.go

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	fmt.Printf(`// pkg/renderer/stars-cursors.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Automatically generated from STARS PCF font files using util/bdfdump.go

package renderer

var starsCursors STARSFont = STARSFont{
    PointSize:9,
    Glyphs: []STARSGlyph{
`)

	sc := bufio.NewScanner(os.Stdin)
	bitmap, firstBitmap := false, false
	firstGlyph := true
	var bx, by int
	var maxHeight int
	var err error
	charname := ""
	var encoding int
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 5 && f[0] == "BBX" {
			if bx, err = strconv.Atoi(f[1]); err != nil {
				panic(err)
			}
			if by, err = strconv.Atoi(f[2]); err != nil {
				panic(err)
			}
			if by > maxHeight {
				maxHeight = by
			}
		} else if len(f) == 2 && f[0] == "STARTCHAR" {
			charname = f[1]
		} else if len(f) == 2 && f[0] == "ENCODING" {
			if encoding, err = strconv.Atoi(f[1]); err != nil {
				panic(err)
			}
		} else if len(f) == 1 && f[0] == "BITMAP" {
			bitmap = true
			firstBitmap = true
			if firstGlyph {
				firstGlyph = false
			} else {
				fmt.Printf(",\n")
			}
			fmt.Printf(`%d: STARSGlyph{Name: "%s", StepX: %d, Bounds: [2]int{%d, %d}, Bitmap: []uint32{`,
				encoding, charname, bx, bx, by)
		} else if len(f) == 1 && f[0] == "ENDCHAR" {
			fmt.Printf("} }")
			bitmap = false
		} else if bitmap {
			if firstBitmap {
				firstBitmap = false
			} else {
				fmt.Printf(", ")
			}
			fmt.Printf("0b")
			for _, ch := range f[0] {
				var v int
				if ch >= '0' && ch <= '9' {
					v = int(ch - '0')
				} else if ch >= 'A' && ch <= 'F' {
					v = 10 + int(ch-'A')
				} else {
					panic(ch)
				}
				for i := 3; i >= 0; i-- {
					if v&(1<<i) != 0 {
						fmt.Printf("1")
					} else {
						fmt.Printf("0")
					}
				}
			}
			// we output len(f[0])*4 bits. add trailing 0s to pad so the
			// font pixels are in the high bits.
			for i := len(f[0]) * 4; i < 32; i++ {
				fmt.Printf("0")
			}
		}
	}
	fmt.Printf("},Width: %d,\nHeight: %d,\n }\n", maxHeight, maxHeight)
}
