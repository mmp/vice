// cmd/crc2vice/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Reads a CRC ARTCC JSON and emits:
//   - one STARS video map library per STARS-equipped child facility
//     (<ARTCC>-<facility>.mappack), and
//   - a single ERAM video map library (<ARTCC>.mappack),
//     if the ARTCC has any ERAM geomaps.
//
// Run from the CRC working directory (where ARTCCs/ and VideoMaps/ live).

package main

import (
	"flag"
	"log"
	"os"

	"github.com/mmp/vice/maps"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	var artcc, outDir string
	flag.StringVar(&artcc, "artcc", "", "ARTCC to import (e.g. ZNY)")
	flag.StringVar(&outDir, "out", ".", "Directory to write the output .mappack file(s) into")
	flag.Parse()
	if artcc == "" {
		log.Fatal("-artcc is required")
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("%v", err)
	}

	if err := maps.ConvertCRC(cwd, artcc, outDir, func(line string) { log.Print(line) }); err != nil {
		log.Fatalf("%v", err)
	}
}
