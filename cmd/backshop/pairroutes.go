// cmd/backshop/pairroutes.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// routesTab looks up how a city pair is really flown--the scenario's own
// "traffic_routes", the routes recently filed between the pair, and the FAA
// preferred and coded departure routes--and says what the scenario would do
// with each of them.
type routesTab struct {
	from, to string

	routes sim.PairRoutes
	err    string
	// pair names what routes describes, so that the answer isn't labeled with
	// whatever is in the boxes by the time it lands.
	pair string
}

// reset forgets a lookup made against a scenario that has gone away; which
// exits and arrivals would fly a route was that scenario's answer.
func (r *routesTab) reset() {
	r.routes, r.pair, r.err = sim.PairRoutes{}, "", ""
}

func (in *inspector) drawRoutesTab(a *app) {
	if !imgui.BeginTabItem("Routes") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}

	r := &in.pairRoutes
	imgui.TextWrapped("The ways a city pair is really flown, from the scenario's own " +
		"\"traffic_routes\", the recently filed routes, and the FAA route databases, with what " +
		"this scenario would do with each of them.")

	submitted := false
	airportInput := func(id, hint string, value *string) {
		imgui.SetNextItemWidth(80)
		submitted = imgui.InputTextWithHint(id, hint, value, imgui.InputTextFlagsCharsUppercase|
			imgui.InputTextFlagsEnterReturnsTrue, nil) || submitted
	}
	airportInput("##from", "KJFK", &r.from)
	imgui.SameLine()
	imgui.Text("to")
	imgui.SameLine()
	airportInput("##to", "KBOS", &r.to)
	imgui.SameLine()
	if imgui.Button("Look up") {
		submitted = true
	}
	if submitted {
		r.lookup(a, av.ICAOAirportCode(r.from), av.ICAOAirportCode(r.to))
	}

	in.drawTrafficPairs(a)

	if r.err != "" {
		imgui.TextColored(warningColor, r.err)
		return
	}
	if r.pair == "" {
		return
	}

	imgui.Separator()
	r.drawRoutes(a)
}

// lookup works out what the scenario would do with the pair's routes. It reads
// the route databases in this process, so an edit to resources/ shows up as
// soon as they are reloaded; nothing is asked of the server.
func (r *routesTab) lookup(a *app, from, to av.ICAOAirportCode) {
	from = av.ICAOAirportCode(strings.ToUpper(strings.TrimSpace(string(from))))
	to = av.ICAOAirportCode(strings.ToUpper(strings.TrimSpace(string(to))))
	if from == "" || to == "" {
		return
	}
	r.from, r.to = string(from), string(to)
	r.pair, r.err = string(from)+"-"+string(to), ""
	r.routes = a.cc.State.RoutesForPair(from, to)
}

func (r *routesTab) drawRoutes(a *app) {
	pr := &r.routes
	imgui.Text(r.pair)
	imgui.SameLine()
	switch {
	case pr.IsDeparture && pr.IsArrival:
		imgui.TextDisabled("(the scenario both departs and lands this pair)")
	case pr.IsDeparture:
		imgui.TextDisabled("(departures from " + string(pr.From) + ")")
	case pr.IsArrival:
		imgui.TextDisabled("(arrivals into " + string(pr.To) + ")")
	default:
		imgui.TextDisabled("(the scenario flies neither end of this pair)")
	}
	for _, note := range pr.Notes {
		imgui.TextColored(warningColor, note)
	}

	if len(pr.Routes) == 0 {
		imgui.Text("No routes between them in any of the databases.")
		return
	}

	// The route is what the table is for, so the columns that only the scraped
	// filings fill in are left out entirely when no scraped route turned up.
	filed := slices.ContainsFunc(pr.Routes, func(r sim.PairRoute) bool { return r.Filings > 0 })
	columns := int32(4) // the warning, the source, the restriction, and the route
	if filed {
		columns += 3
	}
	if pr.IsDeparture {
		columns++
	}
	if pr.IsArrival {
		columns++
	}
	if !imgui.BeginTableV("pairroutes", columns, scrollingTableFlags, imgui.Vec2{}, 0) {
		return
	}
	imgui.TableSetupColumnV("##warn", imgui.TableColumnFlagsWidthFixed, 20, 0)
	imgui.TableSetupColumnV("Source", imgui.TableColumnFlagsWidthFixed, 55, 0)
	imgui.TableSetupColumnV("Aircraft", imgui.TableColumnFlagsWidthFixed, 70, 0)
	if filed {
		imgui.TableSetupColumnV("Filed", imgui.TableColumnFlagsWidthFixed, 40, 0)
		imgui.TableSetupColumnV("Altitudes", imgui.TableColumnFlagsWidthFixed, 65, 0)
		imgui.TableSetupColumnV("Hours", imgui.TableColumnFlagsWidthFixed, 80, 0)
	}
	if pr.IsDeparture {
		imgui.TableSetupColumnV("Departs via", imgui.TableColumnFlagsWidthStretch, 1, 0)
	}
	if pr.IsArrival {
		imgui.TableSetupColumnV("Arrives on", imgui.TableColumnFlagsWidthStretch, 1, 0)
	}
	imgui.TableSetupColumnV("Route", imgui.TableColumnFlagsWidthStretch, 5, 0)
	imgui.TableHeadersRow()

	for i, route := range pr.Routes {
		problem := routeProblem(pr, route)

		imgui.PushIDInt(int32(i))
		imgui.TableNextRow()
		imgui.TableNextColumn()
		if problem != "" {
			imgui.TextColored(warningColor, renderer.FontAwesomeIconExclamationTriangle)
			if imgui.IsItemHovered() {
				imgui.SetTooltip(problem)
			}
		}
		imgui.TableNextColumn()
		imgui.Text(route.Source)
		imgui.TableNextColumn()
		imgui.Text(aircraftRestriction(route))
		if filed {
			imgui.TableNextColumn()
			if route.Filings > 0 {
				imgui.Text(fmt.Sprint(route.Filings))
			}
			imgui.TableNextColumn()
			imgui.Text(altitudeRange(route.MinAltitude, route.MaxAltitude))
			imgui.TableNextColumn()
			imgui.Text(route.Hours)
		}
		if pr.IsDeparture {
			imgui.TableNextColumn()
			drawRouteUse(route.DepartureUse)
		}
		if pr.IsArrival {
			imgui.TableNextColumn()
			drawRouteUse(route.ArrivalUse)
		}
		imgui.TableNextColumn()
		if imgui.SelectableBool(route.Route) {
			a.plat.GetClipboard().SetClipboard(route.Route)
			a.status = "copied route"
		}
		// Routes run well past the column; the tooltip is where a long one can
		// be read without widening the window.
		if imgui.IsItemHovered() {
			imgui.SetTooltip(route.Route + "\n(click to copy)")
		}
		imgui.PopID()
	}
	imgui.EndTable()
}

// routeProblem is why the scenario can't fly a route, taken from whichever
// ends of the pair it flies.
func routeProblem(pr *sim.PairRoutes, route sim.PairRoute) string {
	var problems []string
	if pr.IsDeparture && route.DepartureUse.Problem != "" {
		problems = append(problems, route.DepartureUse.Problem)
	}
	if pr.IsArrival && route.ArrivalUse.Problem != "" {
		problems = append(problems, route.ArrivalUse.Problem)
	}
	return strings.Join(problems, "; ")
}

// drawRouteUse says which exit or inbound flows the scenario would fly a route
// on, or why it can't fly it at all. The cell stays narrow so that the route
// itself gets the width; the whole of it is a hover away.
func drawRouteUse(use sim.RouteUse) {
	if use.Problem != "" {
		imgui.TextColored(warningColor, use.Problem)
		tooltip(use.Problem)
		return
	}
	if use.Exit != "" {
		imgui.Text(string(use.Exit))
		tooltip("Leaves over " + string(use.Exit))
		return
	}
	flows := strings.Join(use.Flows, ", ")
	imgui.Text(flows)
	if use.STAR == "" {
		tooltip("Comes in on the " + flows + " flow")
	} else {
		tooltip("Flies the " + use.STAR + " on the " + flows + " flow")
	}
}

// tooltip shows text when the item just drawn is hovered.
func tooltip(text string) {
	if imgui.IsItemHovered() {
		imgui.SetTooltip(text)
	}
}

// aircraftRestriction says which aircraft a route is limited to; the FAA
// databases also record whether one takes RNAV equipment only.
func aircraftRestriction(route sim.PairRoute) string {
	switch {
	case !route.RNAVRequired:
		return route.Aircraft
	case route.Aircraft == "":
		return "RNAV"
	default:
		return route.Aircraft + ", RNAV"
	}
}

// altitudeRange formats the filed cruise altitudes of a scraped route, in
// hundreds of feet the way a flight level is written.
func altitudeRange(low, high int) string {
	switch {
	case low == 0 && high == 0:
		return ""
	case low == high:
		return fmt.Sprint(low / 100)
	default:
		return fmt.Sprintf("%d-%d", low/100, high/100)
	}
}

// drawTrafficPairs lists the city pairs the real-world traffic the Traffic tab
// has fetched actually flies, busiest first, so that the pairs worth checking
// can be picked off rather than guessed at.
func (in *inspector) drawTrafficPairs(a *app) {
	flights := in.traffic.report.Flights
	if len(flights) == 0 {
		imgui.TextDisabled("Fetch traffic in the Traffic tab to list the pairs it flies.")
		return
	}

	type pairCount struct {
		from, to av.ICAOAirportCode
		flights  int
		dropped  int
	}
	counts := make(map[string]*pairCount)
	for _, f := range flights {
		pair := string(f.From) + "-" + string(f.To)
		c, ok := counts[pair]
		if !ok {
			c = &pairCount{from: f.From, to: f.To}
			counts[pair] = c
		}
		c.flights++
		if f.Outcome == sim.FlightDropped {
			c.dropped++
		}
	}

	label := fmt.Sprintf("City pairs in the fetched traffic (%d)", len(counts))
	if !imgui.CollapsingHeaderBoolPtr(label, nil) {
		return
	}

	// Busiest first, and among equals the ones losing the most traffic; the
	// keys come out sorted, so ties stay alphabetical.
	pairs := util.SortedMapKeys(counts)
	slices.SortStableFunc(pairs, func(a, b string) int {
		if n := counts[b].flights - counts[a].flights; n != 0 {
			return n
		}
		return counts[b].dropped - counts[a].dropped
	})

	flags, size := tableSize(len(pairs), 10)
	if !imgui.BeginTableV("trafficpairs", 4, flags, size, 0) {
		return
	}
	imgui.TableSetupColumnV("##warn", imgui.TableColumnFlagsWidthFixed, 20, 0)
	imgui.TableSetupColumnV("Pair", imgui.TableColumnFlagsWidthFixed, 110, 0)
	imgui.TableSetupColumnV("Flights", imgui.TableColumnFlagsWidthFixed, 60, 0)
	imgui.TableSetupColumn("Dropped")
	imgui.TableHeadersRow()

	for _, pair := range pairs {
		c := counts[pair]
		imgui.TableNextRow()
		imgui.TableNextColumn()
		if c.dropped > 0 {
			imgui.TextColored(warningColor, renderer.FontAwesomeIconExclamationTriangle)
		}
		imgui.TableNextColumn()
		if imgui.SelectableBool(pair) {
			in.pairRoutes.lookup(a, c.from, c.to)
		}
		imgui.TableNextColumn()
		imgui.Text(fmt.Sprint(c.flights))
		imgui.TableNextColumn()
		if c.dropped > 0 {
			imgui.TextColored(warningColor, fmt.Sprint(c.dropped))
		}
	}
	imgui.EndTable()
}
