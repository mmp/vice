// cmd/backshop/flights.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

// startTimeFormat is how the Traffic tab's start time is typed and shown. The
// sim's clock is UTC, as vice's times are everywhere.
const startTimeFormat = "2006-01-02 15:04"

// trafficTab browses the real-world traffic a scenario would fly: the flights
// the recorded data or a timetable holds for its airports, what each one would
// end up flying, and the ones the scenario has no way to fly at all.
type trafficTab struct {
	query sim.TrafficQuery
	// start is the query's start time as typed, which may not parse yet.
	start string

	report  sim.PublishedTraffic
	fetched bool
	err     string
	// run is the report being built; reading the flight data off disk and
	// placing every flight takes long enough to be worth not blocking a frame.
	run *trafficRun

	filter flightFilter
	// departures and arrivals are the report split by direction and cut down to
	// what the filter leaves showing. The busiest facilities publish thousands
	// of flights, so they are rebuilt when the filter changes, not every frame.
	departures, arrivals []sim.PublishedFlight
}

// flightFilter is which of a report's flights the tab lists.
type flightFilter struct {
	text        string
	onlyUnflown bool
}

// reset drops a report gathered for a scenario that has gone away; its
// runways, flows, and exits were that scenario's.
func (t *trafficTab) reset() {
	t.report, t.fetched, t.err, t.run = sim.PublishedTraffic{}, false, "", nil
	t.departures, t.arrivals = nil, nil
}

// trafficRun is a report being built on its own goroutine. It works from a copy
// of the scenario state: the client replaces the state it publishes rather than
// editing it, so a copy taken when the run starts stays a consistent view of
// the scenario for as long as the run takes.
type trafficRun struct {
	report sim.PublishedTraffic
	err    error
	done   chan struct{}
}

func startTrafficReport(state sim.CommonState, query sim.TrafficQuery) *trafficRun {
	run := &trafficRun{done: make(chan struct{})}
	go func() {
		defer close(run.done)
		run.report, run.err = state.PublishedTrafficReport(query)
	}()
	return run
}

// poll takes in a finished report. It runs every frame rather than with the
// tab, so that a fetch left behind still lands: the Routes tab lists the city
// pairs it turns up.
func (t *trafficTab) poll() {
	if t.run == nil {
		return
	}
	select {
	case <-t.run.done:
	default:
		return
	}

	if t.run.err != nil {
		t.err = t.run.err.Error()
	} else {
		t.report, t.fetched = t.run.report, true
		t.refilter()
	}
	t.run = nil
}

func (in *inspector) drawTrafficTab(a *app) {
	if !imgui.BeginTabItem("Traffic") {
		return
	}
	defer imgui.EndTabItem()

	if a.cc == nil {
		imgui.Text("No sim running.")
		return
	}
	spec := a.selectedSpec()
	if spec == nil {
		imgui.Text("No scenario selected.")
		return
	}

	t := &in.traffic
	imgui.TextWrapped("The real-world traffic the scenario's airports see and what this scenario " +
		"would do with each flight; nothing here changes the running sim. Class is P)rop, " +
		"T)urboprop, J)et, H)eavy jet.")

	sources := publishedSources(spec)
	if len(sources) == 0 {
		imgui.Text("This scenario has no published traffic: neither a timetable nor recorded flights.")
		return
	}
	t.drawQueryUI(a, spec, sources)

	if t.run != nil {
		imgui.Text("Reading flight data...")
		return
	}
	if t.err != "" {
		imgui.TextColored(warningColor, t.err)
		return
	}
	if !t.fetched {
		return
	}

	imgui.Separator()
	t.drawSummary(a)
	t.drawFlightTable(a, true)
	t.drawFlightTable(a, false)
}

// publishedSources are the traffic sources with flights to browse; a
// scenario's own rate-based traffic publishes no flight list.
func publishedSources(spec *scenario.Spec) []sim.TrafficSource {
	return util.FilterSlice(spec.TrafficSources, func(s sim.TrafficSource) bool {
		return s != sim.TrafficSourceScenario
	})
}

func (t *trafficTab) drawQueryUI(a *app, spec *scenario.Spec, sources []sim.TrafficSource) {
	// A scenario the user has just switched to may not fly the source or the
	// timetable the last one did.
	if !slices.Contains(sources, t.query.Source) || (t.query.Source == sim.TrafficSourceTimetable &&
		t.timetableLabel(spec) == "") {
		t.query.Source = sources[0]
		t.defaultQuery(spec)
	}

	// Everything that selects the traffic goes on one line: what is worth the
	// tab's height is the flights, not the controls.
	imgui.Text("Source:")
	for _, source := range sources {
		imgui.SameLine()
		if imgui.RadioButtonIntPtr(source.String(), (*int32)(&t.query.Source), int32(source)) {
			t.defaultQuery(spec)
		}
	}

	if t.query.Source == sim.TrafficSourceTimetable {
		imgui.SameLine()
		imgui.SetNextItemWidth(220)
		if imgui.BeginCombo("##timetable", t.timetableLabel(spec)) {
			for _, timetable := range spec.Timetables {
				label := string(timetable.Airport) + " " + timetable.Name
				if imgui.SelectableBool(label) {
					t.query.TimetableAirport, t.query.TimetableID = timetable.Airport, timetable.ID
				}
			}
			imgui.EndCombo()
		}
	}

	imgui.SameLine()
	imgui.SetNextItemWidth(150)
	imgui.InputTextWithHint("UTC", startTimeFormat, &t.start, 0, nil)
	start, err := time.Parse(startTimeFormat, strings.TrimSpace(t.start))
	if err == nil {
		t.query.Start = start
	}

	imgui.SameLine()
	imgui.BeginDisabledV(t.run != nil || err != nil)
	if imgui.Button("Fetch traffic") {
		t.fetch(a)
	}
	imgui.EndDisabled()

	if err != nil {
		imgui.TextColored(warningColor, "Start time must be written "+startTimeFormat+".")
	} else if t.query.Source == sim.TrafficSourceHistorical {
		imgui.TextDisabled("Recorded flights cover " + intervalSummary(spec.HistoricalFlightIntervals))
	}
}

// defaultQuery picks a start time and timetable the selected source actually
// has data for, so that the first fetch of a scenario returns something.
func (t *trafficTab) defaultQuery(spec *scenario.Spec) {
	switch t.query.Source {
	case sim.TrafficSourceTimetable:
		if len(spec.Timetables) > 0 {
			t.query.TimetableAirport = spec.Timetables[0].Airport
			t.query.TimetableID = spec.Timetables[0].ID
		}
		t.query.Start = simStartTime()

	case sim.TrafficSourceHistorical:
		// The last full window the data covers: reaching back from the end of
		// the most recent stretch leaves the whole report inside it.
		t.query.Start = simStartTime()
		if n := len(spec.HistoricalFlightIntervals); n > 0 {
			t.query.Start = spec.HistoricalFlightIntervals[n-1].End().
				Add(-sim.HistoricalFlightWindow).UTC().Truncate(time.Hour)
		}
	}
	t.start = t.query.Start.Format(startTimeFormat)
}

func (t *trafficTab) timetableLabel(spec *scenario.Spec) string {
	for _, timetable := range spec.Timetables {
		if timetable.Airport == t.query.TimetableAirport && timetable.ID == t.query.TimetableID {
			return string(timetable.Airport) + " " + timetable.Name
		}
	}
	return ""
}

// fetch starts the report. It reads the flight data and the timetables from
// resources/ in this process rather than asking the server, so what it reports
// is what is on this machine's disk.
func (t *trafficTab) fetch(a *app) {
	t.err = ""
	t.run = startTrafficReport(a.cc.State.CommonState, t.query)
}

// refilter rebuilds the lists of flights the two tables show.
func (t *trafficTab) refilter() {
	t.departures, t.arrivals = nil, nil
	for _, f := range t.report.Flights {
		if !t.filter.matches(f) {
			continue
		}
		if f.Departure {
			t.departures = append(t.departures, f)
		} else {
			t.arrivals = append(t.arrivals, f)
		}
	}
}

func (t *trafficTab) drawSummary(a *app) {
	departures, arrivals := 0, 0
	unflown := make(map[string]int)
	for _, f := range t.report.Flights {
		if f.Departure {
			departures++
		} else {
			arrivals++
		}
		if f.Outcome != sim.FlightFlown {
			unflown[f.Problem]++
		}
	}

	summary := fmt.Sprintf("%s to %s: %d departures, %d arrivals",
		t.report.Start.Format(startTimeFormat), t.report.End.Format(startTimeFormat),
		departures, arrivals)
	if len(t.report.Excluded) > 0 {
		summary += "; left out " + strings.Join(t.report.Excluded, ", ")
	}
	imgui.TextWrapped(summary)
	drawUnflownReasons(unflown)

	changed := imgui.Checkbox("Only what won't fly", &t.filter.onlyUnflown)
	imgui.SameLine()
	imgui.SetNextItemWidth(140)
	changed = imgui.InputTextWithHint("##filter", "filter", &t.filter.text, 0, nil) || changed
	if changed {
		t.refilter()
	}

	imgui.SameLine()
	if imgui.SmallButton("copy##traffic") {
		a.plat.GetClipboard().SetClipboard(t.text())
		a.status = fmt.Sprintf("copied %d flights", len(t.departures)+len(t.arrivals))
	}
}

// drawUnflownReasons breaks the traffic the scenario won't fly down by reason,
// which is what says whether an exit is missing or a whole airport is idle.
func drawUnflownReasons(unflown map[string]int) {
	total := 0
	for _, n := range unflown {
		total += n
	}
	if total == 0 {
		return
	}

	imgui.PushStyleColorVec4(imgui.ColText, warningColor)
	open := imgui.CollapsingHeaderBoolPtr(fmt.Sprintf("%d flights the scenario won't fly", total), nil)
	imgui.PopStyleColor()
	if !open {
		return
	}
	for _, reason := range util.SortedMapKeys(unflown) {
		imgui.Text(fmt.Sprintf("%6d  %s", unflown[reason], reason))
	}
}

// matches reports whether a flight is one the filter leaves showing.
func (ff flightFilter) matches(f sim.PublishedFlight) bool {
	if ff.onlyUnflown && f.Outcome == sim.FlightFlown {
		return false
	}
	text := strings.ToUpper(strings.TrimSpace(ff.text))
	if text == "" {
		return true
	}
	return slices.ContainsFunc([]string{f.Callsign, f.AircraftType, string(f.From), string(f.To),
		string(f.Runway), string(f.Exit), f.Flow, f.STAR, f.Route},
		func(field string) bool { return strings.Contains(strings.ToUpper(field), text) })
}

// drawFlightTable lists one direction's flights. Departures and arrivals go in
// tables of their own so that the column saying where each flight goes can name
// the runway and exit of a departure and the flow and STAR of an arrival rather
// than having to cover both.
func (t *trafficTab) drawFlightTable(a *app, departures bool) {
	flights := util.Select(departures, t.departures, t.arrivals)
	kind := util.Select(departures, "Departures", "Arrivals")
	if !imgui.CollapsingHeaderBoolPtrV(fmt.Sprintf("%s (%d)", kind, len(flights)), nil,
		imgui.TreeNodeFlagsDefaultOpen) {
		return
	}
	if len(flights) == 0 {
		imgui.Text("None.")
		return
	}

	// The two tables split what is left of the tab between them, so that both
	// are on screen at once and each scrolls within its share.
	height := imgui.ContentRegionAvail().Y
	if departures {
		height /= 2
	}
	flags, size := tableSize(len(flights), rowsInHeight(height))
	if !imgui.BeginTableV(kind, 10, flags, size, 0) {
		return
	}
	imgui.TableSetupColumnV("##warn", imgui.TableColumnFlagsWidthFixed, 20, 0)
	imgui.TableSetupColumnV("Time", imgui.TableColumnFlagsWidthFixed, 40, 0)
	imgui.TableSetupColumnV("Callsign", imgui.TableColumnFlagsWidthFixed, 68, 0)
	imgui.TableSetupColumnV("Type", imgui.TableColumnFlagsWidthFixed, 42, 0)
	imgui.TableSetupColumnV("Cl", imgui.TableColumnFlagsWidthFixed, 20, 0)
	imgui.TableSetupColumnV("From", imgui.TableColumnFlagsWidthFixed, 45, 0)
	imgui.TableSetupColumnV("To", imgui.TableColumnFlagsWidthFixed, 45, 0)
	if departures {
		imgui.TableSetupColumnV("Runway", imgui.TableColumnFlagsWidthFixed, 50, 0)
		imgui.TableSetupColumnV("Exit", imgui.TableColumnFlagsWidthFixed, 60, 0)
	} else {
		imgui.TableSetupColumnV("Flow", imgui.TableColumnFlagsWidthStretch, 1, 0)
		imgui.TableSetupColumnV("STAR", imgui.TableColumnFlagsWidthFixed, 60, 0)
	}
	imgui.TableSetupColumnV("Route", imgui.TableColumnFlagsWidthStretch, 4, 0)
	imgui.TableHeadersRow()

	// The busiest facilities publish thousands of flights; only the rows on
	// screen are worth building.
	clipper := imgui.NewListClipper()
	defer clipper.Destroy()
	clipper.Begin(int32(len(flights)))
	for clipper.Step() {
		for i := clipper.DisplayStart(); i < clipper.DisplayEnd(); i++ {
			imgui.PushIDInt(i)
			t.drawFlightRow(a, flights[i])
			imgui.PopID()
		}
	}
	imgui.EndTable()
}

// rowsInHeight is how many of these tables' rows fit in the given height,
// which is what tableSize wants to hear; three so that a table is never a
// sliver.
func rowsInHeight(height float32) int {
	pad := 2 * imgui.CurrentStyle().CellPadding().Y
	rows := (height - imgui.TextLineHeight() - pad) / (imgui.FrameHeight() + pad)
	return max(int(rows), 3)
}

func (t *trafficTab) drawFlightRow(a *app, f sim.PublishedFlight) {
	imgui.TableNextRow()
	imgui.TableNextColumn()
	if f.Outcome != sim.FlightFlown {
		imgui.TextColored(outcomeColor(f.Outcome), gui.Icons.ExclamationTriangle)
		tooltip(f.Problem)
	}
	imgui.TableNextColumn()
	imgui.Text(f.Published.Format("15:04"))
	imgui.TableNextColumn()
	imgui.Text(f.Callsign)
	imgui.TableNextColumn()
	imgui.Text(f.AircraftType)
	imgui.TableNextColumn()
	imgui.Text(aircraftClassLetter(f.AircraftType))
	tooltip(aircraftClassName(f.AircraftType))
	imgui.TableNextColumn()
	imgui.Text(string(f.From))
	imgui.TableNextColumn()
	imgui.Text(string(f.To))

	if f.Departure {
		imgui.TableNextColumn()
		imgui.Text(string(f.Runway))
		imgui.TableNextColumn()
		imgui.Text(string(f.Exit))
	} else {
		imgui.TableNextColumn()
		if f.Flow != "" {
			imgui.Text(fmt.Sprintf("%s #%d", f.Flow, f.ArrivalIndex))
			tooltip(fmt.Sprintf("Arrival %d of the %s flow", f.ArrivalIndex, f.Flow))
		}
		imgui.TableNextColumn()
		imgui.Text(f.STAR)
	}

	imgui.TableNextColumn()
	if f.Outcome != sim.FlightFlown {
		drawUnflownRoute(f)
		return
	}
	if imgui.SelectableBool(f.Route) {
		a.plat.GetClipboard().SetClipboard(f.Route)
		a.status = "copied route"
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip(f.Route + "\n" + f.How + "\n(click to copy)")
	}
}

// drawUnflownRoute fills the route cell of a flight that won't be flown: why it
// won't, and then, dimmed, the route it would have filed. The route is what
// explains the why--which STAR it ends with, which gate it leaves through--so
// it is worth the width even though the flight never flies it.
func drawUnflownRoute(f sim.PublishedFlight) {
	both := f.Problem
	if f.Route != "" {
		both += "\n" + f.Route
	}
	imgui.TextColored(outcomeColor(f.Outcome), f.Problem)
	tooltip(both)
	if f.Route != "" {
		imgui.SameLine()
		imgui.TextDisabled(f.Route)
		tooltip(both)
	}
}

// flownVia is where a flight goes in or out: the runway and exit of a
// departure, the flow and arrival of an arrival.
func flownVia(f sim.PublishedFlight) string {
	if f.Departure {
		if f.Exit == "" {
			return ""
		}
		return string(f.Runway) + " " + string(f.Exit)
	}
	if f.Flow == "" {
		return ""
	}
	via := fmt.Sprintf("%s #%d", f.Flow, f.ArrivalIndex)
	if f.STAR != "" {
		via += " " + f.STAR
	}
	return via
}

// aircraftClassName is how a flight's aircraft is classified when real routes
// and arrivals are chosen for it, and aircraftClassLetter is the same thing in
// the one character a table column can spare.
func aircraftClassName(aircraftType string) string {
	if class := av.AircraftClassOf(aircraftType); class != 0 {
		return class.String()
	}
	return "unknown"
}

func aircraftClassLetter(aircraftType string) string {
	switch av.AircraftClassOf(aircraftType) {
	case av.AircraftClassProp:
		return "P"
	case av.AircraftClassTurboprop:
		return "T"
	case av.AircraftClassNonheavyJet:
		return "J"
	case av.AircraftClassHeavyJet:
		return "H"
	default:
		return "?"
	}
}

// text renders the report the way the clipboard wants it, one flight a line,
// with the filters applied: what is on screen is what gets copied.
func (t *trafficTab) text() string {
	var b strings.Builder
	for _, f := range slices.Concat(t.departures, t.arrivals) {
		b.WriteString(flightText(f))
		b.WriteByte('\n')
	}
	return b.String()
}

func flightText(f sim.PublishedFlight) string {
	direction := util.Select(f.Departure, "departure", "arrival")
	line := fmt.Sprintf("%s %-8s %-5s %-10s %s %s->%s %s", f.Published.Format(startTimeFormat),
		f.Callsign, f.AircraftType, aircraftClassName(f.AircraftType), direction,
		f.From, f.To, flownVia(f))
	if f.Substitute != "" {
		line += " (as from " + string(f.Substitute) + ")"
	}
	switch f.Outcome {
	case sim.FlightWaiting:
		line += " WAITING: " + f.Problem
	case sim.FlightDropped:
		line += " DROPPED: " + f.Problem
	}
	return line + " " + f.Route
}

// outcomeColor separates the traffic the scenario has no way to fly from the
// traffic that is only waiting for a flow to be switched on.
func outcomeColor(outcome sim.FlightOutcome) imgui.Vec4 {
	if outcome == sim.FlightWaiting {
		return imgui.Vec4{X: 1, Y: 0.8, Z: 0.3, W: 1}
	}
	return warningColor
}

// intervalSummary is the stretches of time the recorded flight data covers,
// which is where a start time has to fall for there to be any traffic.
func intervalSummary(intervals []util.TimeInterval) string {
	if len(intervals) == 0 {
		return "nothing for this scenario's airports"
	}
	const day = "2006-01-02"
	summary := intervals[0].Start().Format(day) + " to " + intervals[len(intervals)-1].End().Format(day)
	if n := len(intervals) - 1; n > 0 {
		summary += fmt.Sprintf(", with %d %s", n, util.Select(n == 1, "gap", "gaps"))
	}
	return summary
}
