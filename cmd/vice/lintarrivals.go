// cmd/vice/lintarrivals.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// An arrival's airlines each name the airport their traffic comes from. The
// scenario's own generator uses that only to pick a livery and a flight plan
// origin--the flow itself decides where the aircraft appears and how it flies
// in--so an origin in the wrong direction costs nothing and is easy to write
// without noticing.
//
// Published traffic reads the same field as a statement of where a flow's
// traffic comes from: a real flight is flown by the flow that lists its origin,
// and one from an airport no flow lists is flown by the flow whose origins are
// nearest to it. Origins that don't match the direction their arrival flies in
// from therefore send real flights in through the wrong gate. Checking them is
// what this does.

// maxArrivalOriginHeadingDifference is how far an origin may lie from the
// direction its arrival flies in from before it is worth reporting. Gates are
// rarely less than this far apart, so a smaller difference doesn't say the
// traffic would come in anywhere else.
const maxArrivalOriginHeadingDifference = 60

// minArrivalSpawnDistance is how far out an arrival must appear for where it
// appears to say anything about where its traffic came from. Inside of this a
// flow is a feeder that starts aircraft on a downwind or a base inside the
// TRACON, pointed however the pattern needs rather than back down the airway
// they arrived on, and its origins are only picking liveries. Published traffic
// doesn't use these flows unless the scenario is running them, in which case the
// controller is working a pattern and not a gate.
const minArrivalSpawnDistance = 25 // nm

// arrivalGate is one way in to an airport: an arrival that appears far enough
// out that where it appears says where its traffic came from.
type arrivalGate struct {
	flow        string
	description string // tells a flow's several arrivals apart
	fromGate    math.TrueHeading
}

// label names a gate the way the report should.
func (g arrivalGate) label() string {
	if g.description == "" {
		return g.flow
	}
	return g.flow + " (" + g.description + ")"
}

// arrivalOriginProblem is one arrival airline whose origin lies in a different
// direction from the arrival airport than the arrival itself flies in from,
// together with the way in that the origin best lines up with.
type arrivalOriginProblem struct {
	gate       arrivalGate
	airport    string // the airport being landed
	origin     string // the airport the airline's traffic comes from
	toOrigin   math.TrueHeading
	difference float32

	// best is the arrival into the same airport that this origin lines up with
	// most closely and bestDifference is how far off it still is, set only when
	// some arrival does better than the one the origin is on now.
	best           arrivalGate
	bestDifference float32
	haveBest       bool
}

// eachArrivalGate calls f for every arrival that appears far enough out from an
// airport it lands to say where its traffic came from.
func eachArrivalGate(flows map[string]*av.InboundFlow, nmPerLongitude float32,
	f func(airport string, gate arrivalGate, arr *av.Arrival)) {
	for _, flow := range util.SortedMapKeys(flows) {
		for i := range flows[flow].Arrivals {
			arr := &flows[flow].Arrivals[i]
			if len(arr.Waypoints) == 0 {
				continue
			}
			// Where an arrival appears is the direction its traffic comes in
			// from, which is what its origins should agree with.
			spawn := arr.Waypoints[0].Location

			for _, airport := range util.SortedMapKeys(arr.Airlines) {
				ap, ok := av.DB.Airports[airport]
				if !ok || math.NMDistance2LL(ap.Location, spawn) < minArrivalSpawnDistance {
					continue // unknown, or a feeder rather than a gate
				}
				f(airport, arrivalGate{flow: flow, description: arr.Description,
					fromGate: math.Heading2LL(ap.Location, spawn, nmPerLongitude)}, arr)
			}
		}
	}
}

// checkArrivalOrigins reports the arrival airlines whose origin airport lies in
// a different direction than the arrival flies in from, each with the arrival
// into the same airport that the origin lines up with best.
func checkArrivalOrigins(flows map[string]*av.InboundFlow, nmPerLongitude float32) []arrivalOriginProblem {
	// Every way in to each airport, gathered first so that an origin on the
	// wrong one can be pointed at the one it belongs on.
	gates := make(map[string][]arrivalGate)
	eachArrivalGate(flows, nmPerLongitude, func(airport string, gate arrivalGate, _ *av.Arrival) {
		gates[airport] = append(gates[airport], gate)
	})

	var problems []arrivalOriginProblem
	seen := make(map[[3]string]bool)

	eachArrivalGate(flows, nmPerLongitude, func(airport string, gate arrivalGate, arr *av.Arrival) {
		ap := av.DB.Airports[airport]

		for _, airline := range arr.Airlines[airport] {
			// An airport has no direction from itself, so traffic a scenario
			// flies between its own airports says nothing.
			if airline.Airport == airport {
				continue
			}
			origin, ok := av.DB.Airports[airline.Airport]
			if !ok {
				continue
			}
			// Airlines are repeated to weight them; report each origin once.
			key := [3]string{gate.flow, airport, airline.Airport}
			if seen[key] {
				continue
			}
			seen[key] = true

			toOrigin := greatCircleHeading(ap.Location, origin.Location)
			difference := math.HeadingDifference(gate.fromGate, toOrigin)
			if difference <= maxArrivalOriginHeadingDifference {
				continue
			}

			p := arrivalOriginProblem{gate: gate, airport: airport, origin: airline.Airport,
				toOrigin: toOrigin, difference: difference}
			for _, candidate := range gates[airport] {
				d := math.HeadingDifference(candidate.fromGate, toOrigin)
				if d < difference && (!p.haveBest || d < p.bestDifference) {
					p.best, p.bestDifference, p.haveBest = candidate, d, true
				}
			}
			problems = append(problems, p)
		}
	})

	slices.SortFunc(problems, func(a, b arrivalOriginProblem) int {
		if a.difference != b.difference {
			return int(b.difference - a.difference) // worst first
		}
		return strings.Compare(a.gate.flow, b.gate.flow)
	})
	return problems
}

// greatCircleHeading returns the initial true heading from one point toward
// another along a great circle. The flat approximation the sim uses is fine over
// a TRACON but not over an ocean: Tokyo is west of Anchorage the way an airplane
// flies and southeast of it the way a flat map draws, and it is the airplane
// this is asking about.
func greatCircleHeading(from, to math.Point2LL) math.TrueHeading {
	lat1, lat2 := math.Radians(from[1]), math.Radians(to[1])
	dLon := math.Radians(to[0] - from[0])
	y := math.Sin(dLon) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dLon)
	return math.TrueHeading(math.NormalizeHeading(math.Degrees(math.Atan2(y, x))))
}

// printArrivalOriginProblems writes a facility's problems out, worst first.
func printArrivalOriginProblems(facility string, problems []arrivalOriginProblem) {
	for _, p := range problems {
		fmt.Printf("%s: %s: %s traffic from %s: arrives from %03d, %s lies %03d, %.0f degrees off",
			facility, p.airport, p.gate.label(), p.origin, int(p.gate.fromGate), p.origin,
			int(p.toOrigin), p.difference)
		if p.haveBest {
			// A flow's arrivals can share a name and a description and still
			// come in from different directions, in which case naming the best
			// one would just repeat what it is already on.
			closest := p.best.label()
			if closest == p.gate.label() {
				closest = "another of its own arrivals"
			}
			fmt.Printf("; closest is %s, which arrives from %03d, %.0f off",
				closest, int(p.best.fromGate), p.bestDifference)
		}
		fmt.Printf("\n")
	}
}
