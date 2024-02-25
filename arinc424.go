// arinc424.go
// Copyright(c) 2024 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/exp/slices"
)

const ARINC424LineLength = 134 // 132 chars + \r + \n

func empty(s []byte) bool {
	for _, b := range s {
		if b != ' ' {
			return false
		}
	}
	return true
}

func parseInt(s []byte) int {
	if v, err := strconv.Atoi(string(s)); err != nil {
		panic(err)
	} else {
		return v
	}
}

func parseAltitude(s []byte) int {
	if len(s) > 2 && string(s[:2]) == "FL" {
		return 100 * parseInt(s[2:])
	}
	return parseInt(s)
}

func printColumnHeader() {
	for i := 0; i < ARINC424LineLength/10; i++ {
		fmt.Printf("         |")
	}
	fmt.Printf("\n")
}

func ParseARINC424(file []byte) (map[string]FAAAirport, map[string]Navaid, map[string]Fix) {
	start := time.Now()

	airports := make(map[string]FAAAirport)
	navaids := make(map[string]Navaid)
	fixes := make(map[string]Fix)

	if len(file)%ARINC424LineLength != 0 {
		panic("Invalid ARINC-424 file: not all lines are 132 characters")
	}

	parseLLDigits := func(d, m, s []byte) float32 {
		deg, err := strconv.Atoi(string(d))
		if err != nil {
			panic(err)
		}
		min, err := strconv.Atoi(string(m))
		if err != nil {
			panic(err)
		}
		sec, err := strconv.Atoi(string(s))
		if err != nil {
			panic(err)
		}
		return float32(deg) + float32(min)/60 + float32(sec)/100/3600
	}
	parseLatLong := func(lat, long []byte) Point2LL {
		var p Point2LL

		p[1] = parseLLDigits(lat[1:3], lat[3:5], lat[5:])
		p[0] = parseLLDigits(long[1:4], long[4:6], long[6:])

		if lat[0] == 'S' {
			p[1] = -p[1]
		}
		if long[0] == 'W' {
			p[0] = -p[0]
		}
		return p
	}

	offset := 0
	getline := func() []byte {
		if offset == len(file) {
			return nil
		}
		start := offset
		offset += ARINC424LineLength
		return file[start:offset]
	}
	ungetline := func() {
		if offset == 0 {
			panic("can't unget")
		}
		offset -= ARINC424LineLength
	}

	// returns array of ssaRecords for all lines starting at the given one
	// that are airport records with the same subsection.
	matchingSSARecs := func(line []byte) []ssaRecord {
		icao := string(line[6:10])
		id := strings.TrimSpace(string(line[13:19]))
		subsec := line[12]

		log := icao == "KJAX" && id == "TEBOW1"

		if log {
			printColumnHeader()
		}

		var recs []ssaRecord
		for {
			if log {
				fmt.Printf("%s", string(line))
			}
			recs = append(recs, parseSSA(line))
			line = getline()
			if line == nil {
				break
			}
			lineid := strings.TrimSpace(string(line[13:19]))
			if lineid != id || line[0] != 'S' || line[4] != 'P' /* section: airport */ || line[12] != subsec {
				ungetline()
				break
			}
		}
		return recs
	}

	for {
		line := getline()
		if line == nil {
			break
		}

		recordType := line[0]
		if recordType != 'S' { // not a standard field
			continue
		}

		sectionCode := line[4]
		switch sectionCode {
		case 'D':
			subsectionCode := line[6]
			if subsectionCode == ' ' /* VOR */ || subsectionCode == 'B' /* NDB */ {
				id := strings.TrimSpace(string(line[13:17]))
				if len(id) < 3 {
					break
				}

				name := strings.TrimSpace(string(line[93:123]))
				if !empty(line[32:51]) {
					navaids[id] = Navaid{
						Id:       id,
						Type:     Select(subsectionCode == ' ', "VOR", "NDB"),
						Name:     name,
						Location: parseLatLong(line[32:41], line[41:51]),
					}
				} else {
					navaids[id] = Navaid{
						Id:       id,
						Type:     "DME",
						Name:     name,
						Location: parseLatLong(line[55:64], line[64:74]),
					}
				}
			}

		case 'E':
			subsection := line[5]
			switch subsection {
			case 'A': // enroute waypoint
				id := strings.TrimSpace(string(line[13:18]))
				fixes[id] = Fix{
					Id:       id,
					Location: parseLatLong(line[32:41], line[41:51]),
				}
			}
			// TODO: holding patterns, airways, etc...

		case 'H': // Heliports
			subsection := line[12]
			switch subsection {
			case 'C': // waypoint record
				id := string(line[13:18])
				location := parseLatLong(line[32:41], line[41:51])
				if _, ok := fixes[id]; ok {
					fmt.Printf("%s: repeats\n", id)
				}
				fixes[id] = Fix{Id: id, Location: location}
			}

		case 'P': // Airports
			icao := string(line[6:10])
			subsection := line[12]
			switch subsection {
			case 'A': // primary airport records 4.1.7
				location := parseLatLong(line[32:41], line[41:51])
				elevation := parseInt(line[56:61])

				airports[icao] = FAAAirport{
					Id:        icao,
					Elevation: elevation,
					Location:  location,
				}

			case 'C': // waypoint record 4.1.4
				id := string(line[13:18])
				location := parseLatLong(line[32:41], line[41:51])
				if _, ok := fixes[id]; ok {
					fmt.Printf("%s: repeats\n", id)
				}
				fixes[id] = Fix{Id: id, Location: location}

			case 'D': // SID 4.1.9

			case 'E': // STAR 4.1.9

			case 'F': // Approach 4.1.9

			case 'G': // runway records 4.1.10
				continuation := line[21]
				if continuation != '0' && continuation != '1' {
					continue
				}
				if string(line[27:31]) == "    " {
					// No heading available. This happens for e.g. seaports.
					continue
				}

				rwy := string(line[13:18])
				rwy = strings.TrimPrefix(rwy, "RW")
				rwy = strings.TrimPrefix(rwy, "0")
				rwy = strings.TrimSpace(rwy)

				ap := airports[icao]
				ap.Runways = append(ap.Runways, Runway{
					Id:        rwy,
					Heading:   float32(parseInt(line[27:31])) / 10,
					Threshold: parseLatLong(line[32:41], line[41:51]),
					Elevation: parseInt(line[66:71]),
				})
				airports[icao] = ap
			}
		}

	}

	return airports, navaids, fixes
}
