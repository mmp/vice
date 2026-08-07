// cmd/scraperoutes/main_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"testing"

	av "github.com/mmp/vice/aviation"
)

// The rows are verbatim from the route analyzer: two summary rows counting
// the filings of each route with its altitude band, and two itemized rows
// giving one flight each, with its local time and aircraft type.
const analyzerBody = `
<table><thead><tr><th class="summaryHeader">Distance</th></tr></thead>	<tr>
		 <td class="row2">58</td>
		 <td class="row2" itemscope itemtype="http://schema.org/Airport"><span title="San Francisco Int'l (San Francisco, CA) - KSFO"><a href="/live/airport/KSFO" itemprop="url">KSFO</a></span></td>
		 <td class="row2" itemscope itemtype="http://schema.org/Airport"><span title="Portland Intl (Portland, OR) - KPDX"><a href="/live/airport/KPDX" itemprop="url">KPDX</a></span></td><td style="text-align: left" class="row2">FL300 - FL430</td><td style="text-align: left" class="row2"><a href="https://skyvector.com/?fpl=F300%20KSFO%20TRUKN2 GRTFL MACHU TMBRS4%20KPDX"target="_blank" rel="noopener noreferrer">TRUKN2 GRTFL MACHU TMBRS4</a></td><td style="text-align: left" class="row2">580 sm</td></tr>	<tr>
		 <td class="row1">1</td>
		 <td class="row1"><a href="/live/airport/KSFO">KSFO</a></td>
		 <td class="row1"><a href="/live/airport/KPDX">KPDX</a></td><td style="text-align: left" class="row1">FL380</td><td style="text-align: left" class="row1"><a href="https://skyvector.com/?fpl=F380%20KSFO%20SFO TRUKN2 GRTFL MACHU TMBRS4 PDX%20KPDX"target="_blank" rel="noopener noreferrer">SFO TRUKN2 GRTFL MACHU TMBRS4 PDX</a></td><td style="text-align: left" class="row1">553 sm</td></tr>	<tr>
		 <td class="row2" style="text-align: left">Wed 02:58PM&nbsp;<span class="tz">PDT</span></td>
		 <td class="row2" style="text-align: left"> <span title="United"><a href="/live/flight/UAL2851">UAL2851</a></span></td>
		 <td class="row2" style="text-align: left"><a href="/live/airport/KSFO">KSFO</a></td>
		 <td class="row2" style="text-align: left"><a href="/live/airport/KPDX">KPDX</a></td>
		 <td class="row2" style="text-align: left"><a href="/live/aircrafttype/B39M"><span title="Boeing 737 MAX 9 (twin-jet)">B39M</span></a></td>
		 <td class="row2" style="text-align: left">FL340</td>
		 <td class="row2" style="text-align: left"><a href="https://skyvector.com/?fpl=F340%20KSFO%20TRUKN2 GRTFL MACHU TMBRS4%20KPDX"target="_blank" rel="noopener noreferrer">TRUKN2 GRTFL MACHU TMBRS4</a></td><td style="text-align: left" class="row2">580 sm</td></tr>	<tr>
		 <td class="row1" style="text-align: left">Wed 12:32AM&nbsp;<span class="tz">PDT</span></td>
		 <td class="row1" style="text-align: left"> <span title="Alaska"><a href="/live/flight/ASA235">ASA235</a></span></td>
		 <td class="row1" style="text-align: left"><a href="/live/airport/KSFO">KSFO</a></td>
		 <td class="row1" style="text-align: left"><a href="/live/airport/KPDX">KPDX</a></td>
		 <td class="row1" style="text-align: left"><a href="/live/aircrafttype/B739"><span title="Boeing 737-900 (twin-jet)">B739</span></a></td>
		 <td class="row1" style="text-align: left">FL290</td>
		 <td class="row1" style="text-align: left"><a href="https://skyvector.com/?fpl=F290%20KSFO%20TRUKN2 GRTFL MACHU TMBRS4%20KPDX"target="_blank" rel="noopener noreferrer">TRUKN2 GRTFL MACHU TMBRS4</a></td><td style="text-align: left" class="row1">580 sm</td></tr>
</table>`

func TestParseAnalyzerRoutes(t *testing.T) {
	av.InitDB()

	routes := parseAnalyzerRoutes(analyzerBody, "KSFO", "KPDX", true, true)
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want the two summary rows merged into one", len(routes))
	}

	r := routes[0]
	if r.Route != "TRUKN2 GRTFL MACHU TMBRS4" {
		t.Errorf("route = %q", r.Route)
	}
	// 58 filings plus the endpoint-token variant's 1.
	if r.Count != 59 {
		t.Errorf("count = %d, want 59", r.Count)
	}
	// FL300-FL430 from the first summary row; FL290 from an itemized flight
	// widens the bottom.
	if r.MinAltitude != 29000 || r.MaxAltitude != 43000 {
		t.Errorf("altitudes = %d-%d, want 29000-43000", r.MinAltitude, r.MaxAltitude)
	}
	// Both itemized flights are jets.
	if !r.Aircraft.Matches("B738") || r.Aircraft.Matches("C172") {
		t.Errorf("aircraft = %v, want jets only", r.Aircraft)
	}
	// Seen at 14:58 and 00:32 local.
	if r.Hours.String() != "0,14" {
		t.Errorf("hours = %q, want \"0,14\"", r.Hours.String())
	}
}

func TestCullRareRoutes(t *testing.T) {
	routes := []av.ScrapedRoute{
		{Route: "A", Count: 100},
		{Route: "B", Count: 11},
		{Route: "C", Count: 10}, // a tenth or less of the most common goes
		{Route: "D", Count: 1},
	}
	kept := cullRareRoutes(routes)
	if len(kept) != 2 || kept[0].Route != "A" || kept[1].Route != "B" {
		t.Errorf("kept %v, want A and B", kept)
	}
}

func TestParseAltitude(t *testing.T) {
	for s, want := range map[string]int{
		"FL340": 34000, "10,000": 10000, "350": 35000, "8000": 8000, "": 0, "junk": 0,
	} {
		if got := parseAltitude(s); got != want {
			t.Errorf("parseAltitude(%q) = %d, want %d", s, got, want)
		}
	}
}

// Oceanic routes keep only what belongs to the scenario's side: tracks and
// coordinate fixes change day to day and everything past them lies an ocean
// away.
func TestConsolidateRoute(t *testing.T) {
	for _, tc := range []struct {
		route          string
		from, to, want string
	}{
		// Eastbound: the domestic feed to the oceanic entry survives; the NAT
		// track and everything in Europe goes. N261B is a North American
		// Route, not a coordinate.
		{"BETTE ACK BRADD N261B NICSO NATU 5200N/02000W NATU XETBO EVRIN JETZI AMFUL ORZEB P2 SIRIC SIRIC1H",
			"KJFK", "EGLL", "BETTE ACK BRADD N261B NICSO"},
		// Westbound: the tail from the oceanic exit through the STAR survives.
		{"GAPLI OTMET NATV 5100N/02000W NATV DOVEY N139A ENE PARCH4",
			"EGLL", "KJFK", "DOVEY N139A ENE PARCH4"},
		// Transpacific between two scenario airports keeps both ends.
		{"TRUKN2 ALCOA 36N130W 34N140W BOARD BITTA CVPID",
			"KSFO", "PHNL", "TRUKN2 ALCOA BOARD BITTA CVPID"},
		// Domestic routes pass through untouched.
		{"WESLA5 NTELL Q174 FLCHR COKTL4", "KSFO", "KLAS", "WESLA5 NTELL Q174 FLCHR COKTL4"},
	} {
		scenario := map[string]bool{"KJFK": true, "KSFO": true, "PHNL": true, "KLAS": true}
		got := consolidateRoute(tc.route, scenario[tc.from], scenario[tc.to])
		if got != tc.want {
			t.Errorf("%s->%s: consolidated to %q, want %q", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestCullRareRoutesCap(t *testing.T) {
	var routes []av.ScrapedRoute
	for i := range 12 {
		routes = append(routes, av.ScrapedRoute{Route: fmt.Sprintf("R%d", i), Count: 100 - i})
	}
	if kept := cullRareRoutes(routes); len(kept) != maxRoutesPerPair {
		t.Errorf("kept %d routes, want the cap of %d", len(kept), maxRoutesPerPair)
	}
}

// FlightAware marks inferred segments with "+" and fills gaps with "TBD";
// both are its annotations, not parts of the route.
func TestCleanRouteAnnotations(t *testing.T) {
	for _, tc := range []struct{ route, want string }{
		{"+JFK SHIPP Y488 STERN", "SHIPP Y488 STERN"},
		{"+BIGGY Q75 ILBEE +RTE7 SAALR", "BIGGY Q75 ILBEE RTE7 SAALR"},
		{"MERDN +TBD M345 RUMMM", "MERDN M345 RUMMM"},
	} {
		if got := cleanRoute(tc.route, "KJFK", "KBOS"); got != tc.want {
			t.Errorf("cleanRoute(%q) = %q, want %q", tc.route, got, tc.want)
		}
	}
}
