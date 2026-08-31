// radar/route_cifp_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package radar

import (
	gomath "math"
	"strings"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

// TestWalkCIFPRoutes walks every SID and approach the CIFP gives for a few
// airports, checking that the walker survives real data, and logs where
// the triggers of a few notable routes land.
func TestWalkCIFPRoutes(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the aviation database")
	}
	av.InitDB()

	notable := map[string]bool{
		"KPHX BALDY3 RWY25L": true, "KPHX BROAK1 RWY25L": true, "KSAN PEBLE6 RWY27": true,
		"KSFO GAPP7 RWY1L": true, "KLAX GMN7 RWY24L": true, "KBUR ELMOO9 RWY33": true,
		"KBUR ELMOO9 RWY26": true, "KSEA SUMMA2 RWY16L": true, "KSEA SUMMA2 RWY34L": true,
	}
	// Approximate magnetic variation, west positive as vice takes it.
	magneticVariation := map[string]float32{"KPHX": -10, "KSAN": -11, "KSFO": -13, "KLAX": -12, "KBUR": -12,
		"KSEA": -15, "KEWR": 13, "KJFK": 13, "KPBF": 1, "KJAX": 6}

	var routes, triggers, indeterminate int
	walk := func(name string, wps av.WaypointArray, rc RouteDrawContext) {
		icao := name[:4]
		ap := av.DB.Airports[icao]
		nmPerLongitude := math.NMPerLongitudeAt(ap.Location)
		wps = wps.Clone().InitializeLocations(dmeLocator{}, nmPerLongitude, magneticVariation[icao], true, &util.ErrorLogger{})
		w := newRouteWalker(nmPerLongitude, magneticVariation[icao], rc, renderer.GetColoredLinesDrawBuilder(), renderer.RGB{}, NewDrawnRoutes())
		w.walk(wps)
		routes++

		start := w.nm(wps[0].Location)
		for _, l := range w.labels {
			if gomath.IsNaN(float64(l.p[0])) || gomath.IsNaN(float64(l.p[1])) {
				t.Errorf("%s: label %q at NaN", name, l.text)
			}
			if strings.HasPrefix(l.text, "@") {
				triggers++
				if strings.HasSuffix(l.text, "?") {
					indeterminate++
					t.Logf("indeterminate: %s: %s", name, l.text)
					if notable[name] {
						t.Errorf("%s: indeterminate trigger %s", name, l.text)
					}
				}
				if notable[name] {
					t.Logf("%s: %s at %.1fnm from the start", name, l.text, math.Distance2f(start, l.p))
				}
			}
		}
	}

	for _, icao := range []string{"KPHX", "KSAN", "KSFO", "KLAX", "KBUR", "KSEA", "KEWR", "KJFK", "KPBF", "KJAX"} {
		ap := av.DB.Airports[icao]
		for sidName, sid := range util.SortedMap(ap.SIDs) {
			for rwy, wps := range util.SortedMap(sid.RunwayTransitions) {
				r, ok := av.LookupRunway(icao, rwy)
				opp, ok2 := av.LookupOppositeRunway(icao, rwy)
				if !ok || !ok2 {
					continue
				}
				// As ExitRoute.initialize does, put the runway in front.
				wps = append(av.WaypointArray{
					{Fix: rwy, Location: r.Threshold},
					{Fix: rwy + "-mid", Location: math.Lerp2f(0.75, r.Threshold, opp.Threshold)},
				}, wps...)
				walk(icao+" "+sidName+" RWY"+rwy, wps,
					RouteDrawContext{Departure: true, FieldElevation: ap.Elevation, ClearedAltitude: 5000})
			}
			for tr, wps := range util.SortedMap(sid.EnrouteTransitions) {
				walk(icao+" "+sidName+" "+tr, wps, RouteDrawContext{})
			}
		}
		for apprName, appr := range util.SortedMap(ap.Approaches) {
			for i, wps := range appr.Waypoints {
				walk(icao+" "+apprName+" "+string(rune('A'+i)), wps, RouteDrawContext{ApproachType: appr.Type})
			}
		}
	}
	t.Logf("%d routes, %d triggers, %d indeterminate", routes, triggers, indeterminate)
}

// dmeLocator locates fixes from the database, DME stations included.
type dmeLocator struct{ enroute.DBLocator }

func (dmeLocator) LocateDME(fix string) (math.Point2LL, int, bool) { return av.DB.LookupDME(fix) }
