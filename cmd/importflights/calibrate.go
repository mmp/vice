// cmd/importflights/calibrate.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Measuring how well the point a track was seen at picks an airport out of a
// list of candidates, so that the thresholds resolveEndpoint guards itself with
// are chosen from the data rather than guessed at.

package main

import (
	"fmt"
	"slices"

	av "github.com/mmp/vice/aviation"
)

// calibration scores picking the nearest candidate against the ends the
// itinerary settles on its own. Those are the only ends whose right answer is
// known, and they are airline flights at large airports--the traffic that needs
// the track position least--so the numbers flatter the method. The report says
// so.
type calibration struct {
	scored  map[band]tally // several candidates and an itinerary that names one
	single  map[band]int64 // one candidate, taken at its word today
	unaided map[band]int64 // several candidates and no itinerary: what nearest wins
}

// band is what the guard has to go on: how high the aircraft was above the
// field and how far it was from it, in the buckets the report prints.
type band struct {
	height, distance int
}

// tally counts the ends in one band and how many the nearest candidate got
// right.
type tally struct{ ends, agreed int64 }

// The rows and columns of the report's tables. Thresholds are chosen from these
// edges, so that a band either passes a threshold entirely or fails it
// entirely.
var (
	heightBands  = []float32{250, 500, 1000, 2000, 5000, 10000, 18000}
	heightLabels = []string{"ground", "<250", "250-500", "500-1000", "1000-2000",
		"2000-5000", "5000-10000", "10000-18000", "18000+", "unknown"}

	distanceBands  = []float32{0.5, 1, 2, 5, 10, 25}
	distanceLabels = []string{"<0.5", "0.5-1", "1-2", "2-5", "5-10", "10-25", "25+"}
)

func makeCalibration() *calibration {
	return &calibration{
		scored:  make(map[band]tally),
		single:  make(map[band]int64),
		unaided: make(map[band]int64),
	}
}

// observe scores both ends of one flight. The itinerary is consulted exactly as
// resolveEndpoints consults it, so that what is measured is what would be used.
func (c *calibration) observe(origin, destination trackEnd, route []string,
	airports map[string]av.FAAAirport) {
	from, to := routeEndpoints(route, origin.candidates, destination.candidates)
	c.observeEnd(origin, from, airports)
	c.observeEnd(destination, to, airports)
}

func (c *calibration) observeEnd(e trackEnd, itinerary endpoint, airports map[string]av.FAAAirport) {
	icao, ap, distance, ok := e.nearest(airports)
	if !ok {
		return
	}
	b := band{height: heightIndex(e, ap), distance: distanceIndex(distance)}

	switch {
	case len(e.candidates) == 1:
		c.single[b]++
	case itinerary.known():
		t := c.scored[b]
		t.ends++
		if icao == itinerary.airport {
			t.agreed++
		}
		c.scored[b] = t
	default:
		c.unaided[b]++
	}
}

// heightIndex buckets how far above the field the aircraft was.
func heightIndex(e trackEnd, ap av.FAAAirport) int {
	if e.onGround {
		return 0
	}
	if !e.hasHeight {
		return len(heightLabels) - 1
	}
	agl := e.height - float32(ap.Elevation)
	if i := slices.IndexFunc(heightBands, func(b float32) bool { return agl < b }); i >= 0 {
		return 1 + i
	}
	return 1 + len(heightBands)
}

// distanceIndex buckets how far from the airport the track point was.
func distanceIndex(d float32) int {
	if i := slices.IndexFunc(distanceBands, func(b float32) bool { return d < b }); i >= 0 {
		return i
	}
	return len(distanceBands)
}

// admits reports whether a guard with these thresholds would accept a band. A
// band passes only if its whole range does, which is why the thresholds are
// drawn from the band edges.
func admits(b band, ground, airborne, height float32) bool {
	if b.distance == len(distanceBands) {
		return false // the open-ended band reaches past any distance
	}
	far := distanceBands[b.distance]

	switch {
	case b.height == 0:
		return far <= ground
	case b.height == len(heightLabels)-1:
		return false // no height to judge by, so not at the airport
	case b.height == len(heightBands)+1:
		return false // the open-ended band reaches past any height
	default:
		return far <= airborne && heightBands[b.height-1] <= height
	}
}

// The thresholds the report scores, as (ground nm, airborne nm, feet above the
// field). Every value is a band edge.
var sweepThresholds = [][3]float32{
	{1, 1, 250}, {2, 1, 250}, {2, 2, 500}, {2, 2, 1000}, {5, 2, 1000},
	{5, 5, 1000}, {5, 5, 2000}, {10, 5, 2000}, {10, 10, 5000}, {25, 25, 5000},
}

func (c *calibration) report() {
	var ends, agreed int64
	for _, t := range c.scored {
		ends += t.ends
		agreed += t.agreed
	}

	fmt.Printf("\nCalibration over %s track ends where several airports were possible and\n",
		commas(ends))
	fmt.Printf("the callsign's itinerary names one of them. Those are airline flights at large\n")
	fmt.Printf("airports, which is the traffic that needs the track position least, so read\n")
	fmt.Printf("these knowing the general aviation traffic this is meant to recover isn't in\n")
	fmt.Printf("them. The nearest candidate was the itinerary's airport %s times, %s.\n\n",
		commas(agreed), percent(agreed, ends))

	printGrid("Nearest candidate agrees with the itinerary", func(b band) (string, int64) {
		t := c.scored[b]
		return percent(t.agreed, t.ends), t.ends
	})

	fmt.Printf("Guard thresholds, scored over those same ends:\n")
	fmt.Printf("  %6s %8s %8s   %9s  %9s\n", "ground", "airborne", "above", "admitted", "of those")
	fmt.Printf("  %6s %8s %8s   %9s  %9s\n", "nm", "nm", "ft", "", "right")
	for _, t := range sweepThresholds {
		var admitted, right int64
		for b, v := range c.scored {
			if admits(b, t[0], t[1], t[2]) {
				admitted += v.ends
				right += v.agreed
			}
		}
		fmt.Printf("  %6g %8g %8g   %9s  %9s\n", t[0], t[1], t[2],
			percent(admitted, ends), percent(right, admitted))
	}
	fmt.Printf("\n")

	printGrid("Track ends with one candidate, which the guard would now judge",
		func(b band) (string, int64) { return "", c.single[b] })
	printGrid("Track ends with several candidates and no itinerary, which nearest recovers",
		func(b band) (string, int64) { return "", c.unaided[b] })
}

// printGrid prints one table of the report, height down the side and distance
// across the top. Cells show the caller's value over the number of track ends
// behind it; a table with no value to show gives just the counts.
func printGrid(title string, cell func(band) (string, int64)) {
	fmt.Printf("%s:\n", title)
	printGridRow("", distanceLabels)

	for h, label := range heightLabels {
		values := make([]string, len(distanceLabels))
		counts := make([]string, len(distanceLabels))
		var total int64
		valued := false
		for d := range distanceLabels {
			value, n := cell(band{height: h, distance: d})
			total += n
			valued = valued || value != ""
			values[d] = value
			if n > 0 {
				counts[d] = "(" + commas(n) + ")"
			}
		}
		if total == 0 {
			continue
		}

		if !valued {
			printGridRow(label, counts)
			continue
		}
		printGridRow(label, values)
		printGridRow("", counts)
	}
	fmt.Printf("\n")
}

func printGridRow(label string, cells []string) {
	fmt.Printf("  %-10s", label)
	for _, c := range cells {
		fmt.Printf("%12s", c)
	}
	fmt.Printf("\n")
}

// percent formats a share of a total, or "-" when there is nothing to divide.
func percent(n, total int64) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(total))
}
