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
				recs := matchingSSARecs(line)
				id := recs[0].id
				if star := parseSTAR(recs); star != nil {
					if airports[icao].STARs == nil {
						ap := airports[icao]
						ap.STARs = make(map[string]STAR)
						airports[icao] = ap
					}
					if _, ok := airports[icao].STARs[id]; ok {
						panic("already seen STAR id " + id)
					}

					airports[icao].STARs[id] = *star
				}

			case 'F': // Approach 4.1.9
				recs := matchingSSARecs(line)
				id := recs[0].id

				if wps := parseApproach(recs); wps != nil {
					// Note: database.Airports isn't initialized yet but
					// the CIFP file is sorted so we get the airports
					// before the approaches..
					if airports[icao].Approaches == nil {
						ap := airports[icao]
						ap.Approaches = make(map[string][]WaypointArray)
						airports[icao] = ap
					}

					id = tidyFAAApproachId(id)
					if _, ok := airports[icao].Approaches[id]; ok {
						panic("already seen approach id " + id)
					}

					airports[icao].Approaches[id] = wps
				}

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

	fmt.Printf("parsed ARINC242 in %s\n", time.Since(start))

	return airports, navaids, fixes
}

func tidyFAAApproachId(id string) string {
	// Remove any hyphens
	id = strings.ReplaceAll(id, "-", "")

	// Pull out alphabetical identifier of the approach
	alpha := ""
	if end := id[len(id)-1]; end >= 'A' && end <= 'Z' && end != 'R' && end != 'L' {
		alpha = string(id[len(id)-1])
		id = id[:len(id)-1]
	}

	atype := id[0]
	if atype == 'H' {
		// Make all RNAVs start with "R"
		atype = 'R'
	}
	rwy := id[1:]

	// Trim leading 0 from runway numbers
	if rwy[0] == '0' {
		rwy = rwy[1:]
	}

	return string(atype) + alpha + rwy
}

type ssaRecord struct {
	icao                   string
	id                     string
	transition             string
	fix                    string
	turnDirectionValid     byte
	pathAndTermination     string
	waypointDescription    []byte
	continuation           byte
	turnDirection          byte
	recommendedNavaid      []byte
	arcRadius              []byte
	rho                    []byte
	outboundMagneticCourse []byte
	routeDistance          []byte
	altDescrip             byte
	alt0, alt1             []byte
	speed                  []byte
	centerFix              []byte
	speedLimitType         byte
}

func (r ssaRecord) Print() {
	fmt.Printf("icao %s id %s fix %5s.%5s %s desc [%s] alt %s/%s[%c] speed %s[%c] turn valid [%c] arc %s dist %s "+
		"center fix %s rho %s outbound mag %s recommended navaid %s\n",
		r.icao, r.id, r.fix, r.transition, string(r.pathAndTermination), string(r.waypointDescription),
		string(r.alt0), string(r.alt1), r.altDescrip, string(r.speed), r.speedLimitType,
		r.turnDirectionValid, string(r.arcRadius), string(r.routeDistance), string(r.centerFix),
		string(r.rho), string(r.outboundMagneticCourse), string(r.recommendedNavaid))
}

func parseSSA(line []byte) ssaRecord {
	return ssaRecord{
		icao:                   string(line[6:10]),
		id:                     strings.TrimSpace(string(line[13:19])),
		continuation:           line[38],
		transition:             strings.TrimSpace(string(line[20:25])),
		fix:                    strings.TrimSpace(string(line[29:34])),
		waypointDescription:    line[39:43],
		turnDirectionValid:     line[49],
		pathAndTermination:     string(line[47:49]), // 5.21, p188
		turnDirection:          line[43],
		recommendedNavaid:      line[50:54],
		arcRadius:              line[56:62],
		rho:                    line[66:70],
		outboundMagneticCourse: line[70:74],
		routeDistance:          line[74:78],
		altDescrip:             line[82], // sec 5.29
		alt0:                   line[84:89],
		alt1:                   line[89:94],
		speed:                  line[99:102],
		centerFix:              line[106:111],
		speedLimitType:         line[117], // 5.261
	}
}

func (r *ssaRecord) GetWaypoint() (wp Waypoint, arc *DMEArc) {
	switch string(r.pathAndTermination) {
	case "FM", "VM":
		// these are headings off of the previous waypoint
		panic("shouldn't call GetWaypoint on FM Or VM record")

	case "AF", "RF": // arcs
		break

	case "HF", "PI": // procedure turns
		break

	case "IF", "TF": // initial fix, direct to fix
		break

	case "CF": // heading to fix; treat as direct to fix?
		break

	case "DF": // direct to fix from unspecified point
		break

	default:
		/*
			r.Print()
			panic(string(r.pathAndTermination) + ": unexpected pathAndTermination")
		*/
	}

	var alt0, alt1, speed int
	if !empty(r.alt0) {
		alt0 = parseAltitude(r.alt0)
	}
	if !empty(r.alt1) {
		alt1 = parseAltitude(r.alt1)
	}
	if !empty(r.speed) {
		speed = parseInt(r.speed)
	}

	wp = Waypoint{
		Fix:     r.fix,
		Speed:   speed,
		FlyOver: r.waypointDescription[1] == 'Y',
		IAF:     r.waypointDescription[3] == 'A' || r.waypointDescription[3] == 'C' || r.waypointDescription[3] == 'D',
		IF:      r.waypointDescription[3] == 'B',
		FAF:     r.waypointDescription[3] == 'I',
	}
	if alt0 != 0 || alt1 != 0 {
		switch r.altDescrip { // 5.29
		case ' ':
			wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{float32(alt0), float32(alt0)}}
		case '+':
			wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{float32(alt0)}}
		case '-':
			wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{0, float32(alt0)}}
		case 'B':
			wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{float32(alt0), float32(alt1)}}
		case 'G', 'I':
			// glideslope alt in second, 'at' in first
			wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{float32(alt0), float32(alt0)}}
		case 'H', 'J':
			// glideslope alt in second, 'at or above' in first
			wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{float32(alt0)}}
		case 'V':
			// coded vertical angle alt in second, 'at or above' in first
			wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{float32(alt0)}}
		case 'X':
			// coded vertical angle alt in second, 'at' in first
			wp.AltitudeRestriction = &AltitudeRestriction{Range: [2]float32{float32(alt0), float32(alt0)}}
		default:
			panic("TODO alt descrip: " + string(r.altDescrip))
		}
	}

	switch r.pathAndTermination {
	case "AF": // arc to fix. w.r.t. a NAVAID
		arc = &DMEArc{
			Fix:    strings.TrimSpace(string(r.recommendedNavaid)),
			Radius: float32(parseInt(r.rho)) / 10,
		}

	case "RF": // constant radius arc
		arc = &DMEArc{
			Fix:    strings.TrimSpace(string(r.centerFix)),
			Radius: float32(parseInt(r.routeDistance)) / 10,
		}

	case "HF", "PI": // procedure turns
		if alt0 == 0 {
			fmt.Printf("%s/%s/%s: HF no alt0?\n", r.icao, r.id, r.fix)
		}
		pt := &ProcedureTurn{
			Type:       PTType(Select(r.pathAndTermination == "HI", PTRacetrack, PTStandard45)),
			RightTurns: r.turnDirection != 'L',
			// TODO: when do we set Entry180NoPt /nopt180?
			ExitAltitude: alt0,
		}

		if r.routeDistance[0] == 'T' { // it's a time
			pt.MinuteLimit = float32(parseInt(r.routeDistance[1:])) / 10
		} else {
			pt.NmLimit = float32(parseInt(r.routeDistance)) / 10
		}

		wp.ProcedureTurn = pt
	}
	return
}

func parseTransitions(recs []ssaRecord, log func(r ssaRecord) bool, skip func(r ssaRecord) bool,
	terminate func(r ssaRecord, transitions map[string]WaypointArray) bool) map[string]WaypointArray {
	transitions := make(map[string]WaypointArray)

	for _, rec := range recs {
		if log(rec) {
			rec.Print()
		}
		if skip(rec) {
			continue
		}
		if terminate(rec, transitions) {
			break
		}

		if string(rec.pathAndTermination) == "FM" || string(rec.pathAndTermination) == "VM" {
			hdg := parseInt(rec.outboundMagneticCourse)
			if n := len(transitions[rec.transition]); n == 0 {
				panic("FM as first waypoint in transition?")
			} else {
				transitions[rec.transition][n-1].Heading = (hdg + 5) / 10
			}
		} else {
			wp, arc := rec.GetWaypoint()
			if arc != nil {
				// it goes on the previous one...
				if n := len(transitions[rec.transition]); n == 0 {
					fmt.Printf("%s/%s/%s: no previous fix to add arc to?\n", rec.icao, rec.id, rec.fix)
				} else {
					transitions[rec.transition][n-1].Arc = arc
				}
			}
			transitions[rec.transition] = append(transitions[rec.transition], wp)
		}
	}

	return transitions
}

func parseSTAR(recs []ssaRecord) *STAR {
	transitions := parseTransitions(recs,
		func(r ssaRecord) bool { return false },                                          // log
		func(r ssaRecord) bool { return r.continuation != '0' && r.continuation != '1' }, // skip continuation records
		func(r ssaRecord, transitions map[string]WaypointArray) bool { return false })    // terminate

	star := MakeSTAR()
	for t, wps := range transitions {
		if len(t) > 3 && t[:2] == "RW" && t[2] >= '0' && t[2] <= '9' {
			// it's a runway
			rwy := t[2:]
			if rwy[0] == '0' {
				rwy = rwy[1:]
			}
			if _, ok := star.RunwayWaypoints[rwy]; ok {
				panic(rwy + " runway already seen?")
			}
			star.RunwayWaypoints[rwy] = wps
		} else if t == "" {
			// common waypoints; skip...
		} else {
			base, ok := transitions[""]
			if !ok {
				base, ok = transitions["ALL"]
			}
			if base == nil {
				// There's no common segment, which is fine
				star.Transitions[t] = wps
			} else {
				sp := spliceTransition(wps, base)
				if sp == nil {
					fmt.Printf("%s/%s [%s] [%s]: mismatching fixes for %s transition\n",
						recs[0].icao, recs[0].id, WaypointArray(wps).Encode(), WaypointArray(base).Encode(), t)

				} else {
					star.Transitions[t] = sp
				}
			}
		}
	}

	return star
}

func spliceTransition(tr WaypointArray, base WaypointArray) WaypointArray {
	idx := slices.IndexFunc(base, func(wp Waypoint) bool { return wp.Fix == tr[len(tr)-1].Fix })
	if idx == -1 {
		return nil
	}

	// Important: we cull the dupe from the common segment, since the transitions may
	// include procedure turns, etc., in their Waypoint variants for the fix.
	return append(WaypointArray(tr), base[idx+1:]...)
}

func parseApproach(recs []ssaRecord) []WaypointArray {
	transitions := parseTransitions(recs,
		func(r ssaRecord) bool { return false },                                          // log
		func(r ssaRecord) bool { return r.continuation != '0' && r.continuation != '1' }, // skip continuation records
		func(r ssaRecord, transitions map[string]WaypointArray) bool {
			return (r.fix == "" && len(transitions[""]) > 0) ||
				r.waypointDescription[0] == 'G' /* field 40: runway as waypoint */
		})

	if len(transitions) == 1 {
		return []WaypointArray{transitions[""]}
	} else {
		base := transitions[""]

		var wps []WaypointArray
		for t, w := range transitions {
			if t != "" {
				sp := spliceTransition(w, base)
				if sp == nil {
					fmt.Printf("%s [%s] [%s]: mismatching fixes for %s transition\n",
						recs[0].icao, WaypointArray(w).Encode(), WaypointArray(base).Encode(), t)
				} else {
					wps = append(wps, sp)
				}
			}
		}
		return wps
	}
}
