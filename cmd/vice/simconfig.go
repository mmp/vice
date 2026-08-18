// simconfig.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/brunoga/deep"
)

type NewSimConfiguration struct {
	server.NewSimRequest

	selectedFacilityCatalogs map[string]*server.ScenarioCatalog

	displayError error

	mgr             *client.ConnectionManager
	selectedServer  *client.Server
	defaultFacility *string
	lg              *log.Logger

	// UI state
	newSimType          newSimType
	joinRequest         server.JoinSimRequest
	showAllMETAR        bool
	showReliefPositions bool
	selectedTCW         sim.TCW
	selectedTCPs        map[sim.TCP]bool

	// New UI state for improved flow
	filterText string // search/filter for scenario selection

	// Weather filter UI state
	weatherFilter      wx.WeatherFilter
	weatherFilterError string

	// mu protects all fields written by the fetchMETAR and fetchTrafficPreview
	// goroutines and the inputs they read back when committing: the cached
	// METAR + atmospheric state below, the traffic preview counts at the end,
	// plus c.weatherFilter / c.weatherFilterError / c.StartTime /
	// c.savedVFRDepartureRateScale, and the c.GroupName / c.ScenarioSpec /
	// c.ScenarioName fields written by SetScenario. fetchSeq is incremented
	// each time SetScenario launches a new fetch; a goroutine bails if its
	// captured seq no longer matches, and the preview does the same with its
	// pending key.
	mu              util.LoggingMutex
	fetchSeq        uint64
	airportMETAR    map[string][]wx.METAR
	metarAirports   []string
	metarFacility   string
	metarWidth      int // characters; see metarText
	fetchMETARError error

	// Winds aloft data for the current facility
	atmosByTime         *wx.AtmosByTime
	windsAloftAltitudes [2]float32 // altitudes for WindsAloft[0] and [1]; 0 means unused
	isTRACON            bool

	availableWXIntervals []util.TimeInterval

	savedVFRDepartureRateScale float32

	// Traffic preview counts from the server, drawn under the traffic source
	// radio buttons. trafficPreviewKey identifies the settings the counts in
	// hand answer for and trafficPreviewPending the settings the request in
	// flight asks about; at most one request is out at a time, so dragging the
	// start time around coalesces to one round trip after another rather than a
	// pile of them.
	trafficPreviewKey        string
	trafficPreviewPending    string
	trafficPreviewDepartures []uint16
	trafficPreviewArrivals   []uint16
	trafficPreviewError      error
	trafficPreviewRetryAt    time.Time
}

func MakeNewSimConfiguration(mgr *client.ConnectionManager, defaultFacility *string, lg *log.Logger) *NewSimConfiguration {
	c := &NewSimConfiguration{
		lg:              lg,
		mgr:             mgr,
		selectedServer:  mgr.LocalServer,
		defaultFacility: defaultFacility,
		NewSimRequest:   server.MakeNewSimRequest(),
	}

	c.SetFacility(*defaultFacility)

	return c
}

func (c *NewSimConfiguration) SetFacility(name string) {
	var ok bool
	catalogs := c.selectedServer.GetScenarioCatalogs()
	if c.selectedFacilityCatalogs, ok = catalogs[name]; !ok {
		if name != "" {
			c.lg.Errorf("%s: TRACON not found!", name)
		}
		// Pick one at random
		name = util.SortedMapKeys(catalogs)[rand.Make().Intn(len(catalogs))]
		c.selectedFacilityCatalogs = catalogs[name]
	}
	c.Facility = name
	var scenarioCatalog *server.ScenarioCatalog
	c.GroupName, scenarioCatalog = util.FirstSortedMapEntry(c.selectedFacilityCatalogs)

	c.SetScenario(c.GroupName, scenarioCatalog.DefaultScenario)
}

func (c *NewSimConfiguration) SetScenario(groupName, scenarioName string) {
	var ok bool
	var scenarioCatalog *server.ScenarioCatalog
	if scenarioCatalog, ok = c.selectedFacilityCatalogs[groupName]; !ok {
		c.lg.Errorf("%s: group not found in TRACON %s", groupName, c.Facility)
		groupName, scenarioCatalog = util.FirstSortedMapEntry(c.selectedFacilityCatalogs)
	}

	spec, ok := scenarioCatalog.Scenarios[scenarioName]
	if !ok {
		if scenarioName != "" {
			c.lg.Errorf("%s: scenario not found in group %s", scenarioName, groupName)
		}
		scenarioName = scenarioCatalog.DefaultScenario
		spec = scenarioCatalog.Scenarios[scenarioName]
	}

	airports := spec.AllAirports()
	facility := c.Facility

	c.mu.Lock(c.lg)
	c.GroupName = groupName
	c.ScenarioSpec = spec
	c.ScenarioName = scenarioName
	normalizeTrafficSourceConfig(c.ScenarioSpec)
	c.savedVFRDepartureRateScale = spec.LaunchConfig.VFRDepartureRateScale
	c.initDefaultWindDirection()
	c.clearTrafficPreviewLocked()
	c.fetchSeq++
	seq := c.fetchSeq
	if c.metarFacility != facility || !slices.Equal(c.metarAirports, airports) {
		c.clearWeatherLocked()
	}
	c.mu.Unlock(c.lg)

	go c.fetchMETAR(seq, facility, airports, spec)
}

func normalizeTrafficSourceConfig(spec *server.ScenarioSpec) {
	lc := &spec.LaunchConfig
	lc.TimetableStartMinute = min(max(lc.TimetableStartMinute, 0), 24*60-1)
	lc.PublishedArrivalRateScale = math.Clamp(lc.PublishedArrivalRateScale, 0, sim.MaxPublishedRateScale)
	lc.PublishedDepartureRateScale = math.Clamp(lc.PublishedDepartureRateScale, 0, sim.MaxPublishedRateScale)

	// The server decides which sources a scenario can be flown with; fall back
	// to the first it offers rather than one it would refuse.
	if !slices.Contains(spec.TrafficSources, lc.TrafficSource) && len(spec.TrafficSources) > 0 {
		lc.TrafficSource = spec.TrafficSources[0]
	}

	if len(spec.Timetables) == 0 {
		lc.TimetableID, lc.TimetableAirport = "", ""
		return
	}

	for _, timetable := range spec.Timetables {
		if timetable.ID == lc.TimetableID && timetable.Airport == lc.TimetableAirport {
			return
		}
	}
	lc.TimetableID, lc.TimetableAirport = spec.Timetables[0].ID, spec.Timetables[0].Airport
}

// A scenario may offer timetables for more than one of its airports, so it
// takes both the id and the airport to name one.
func selectedTimetableSummary(spec *server.ScenarioSpec) (sim.TimetableSummary, bool) {
	for _, timetable := range spec.Timetables {
		if timetable.ID == spec.LaunchConfig.TimetableID &&
			timetable.Airport == spec.LaunchConfig.TimetableAirport {
			return timetable, true
		}
	}
	return sim.TimetableSummary{}, false
}

// timetableLabel names a timetable in the picker. Timetables at different
// airports may share a name, so the airport goes in the label when the
// scenario offers more than one airport's.
func timetableLabel(spec *server.ScenarioSpec, timetable sim.TimetableSummary) string {
	for _, other := range spec.Timetables {
		if other.Airport != timetable.Airport {
			return av.TrimICAOPrefix(timetable.Airport) + " " + timetable.Name
		}
	}
	return timetable.Name
}

// timetableStartMinute is the sim start time as a local clock time at the
// timetable's airport, which is how a timetable's own times are expressed. The
// start time is chosen once, above; a timetable just needs it in local terms.
func timetableStartMinute(start time.Time, airport string) (int, error) {
	location, ok := av.DB.AirportTimeZone(airport)
	if !ok {
		return 0, fmt.Errorf("no time zone is known for %s", airport)
	}
	local := start.In(location)
	return local.Hour()*60 + local.Minute(), nil
}

func (c *NewSimConfiguration) trafficSourceTooltip(source sim.TrafficSource, spec *server.ScenarioSpec) string {
	switch source {
	case sim.TrafficSourceScenario:
		return "Traffic generated from the scenario's own definitions, at the arrival\n" +
			"and departure rates you set."
	case sim.TrafficSourceTimetable:
		return "Fly a curated daily timetable, starting at the selected time.\n" +
			"Overflights remain randomly generated."
	case sim.TrafficSourceHistorical:
		return "Fly the traffic that really operated at " + c.NewSimRequest.Facility +
			" on the selected date,\nfrom recorded flight data."
	default:
		return ""
	}
}

func (c *NewSimConfiguration) drawTrafficSourceUI(spec *server.ScenarioSpec, p platform.Platform) {
	lc := &spec.LaunchConfig

	// Only the sources the server says it will run this scenario with are
	// offered: a scenario that gives no airlines has no traffic of its own to
	// generate, and most facilities have no timetable.
	imgui.Text("IFR traffic source:")
	for _, source := range spec.TrafficSources {
		imgui.SameLine()
		if len(spec.TrafficSources) == 1 {
			imgui.Text(source.String())
		} else {
			imgui.RadioButtonIntPtr(source.String(), (*int32)(&lc.TrafficSource), int32(source))
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip(c.trafficSourceTooltip(source, spec))
		}
	}

	normalizeTrafficSourceConfig(spec)

	if lc.TrafficSource == sim.TrafficSourceTimetable {
		selected, _ := selectedTimetableSummary(spec)

		imgui.Text("Timetable:")
		imgui.SameLine()
		imgui.SetNextItemWidth(260)
		if imgui.BeginCombo("##timetable", timetableLabel(spec, selected)) {
			for _, timetable := range spec.Timetables {
				isSelected := timetable.ID == lc.TimetableID && timetable.Airport == lc.TimetableAirport
				if imgui.SelectableBoolV(timetableLabel(spec, timetable), isSelected, 0, imgui.Vec2{}) {
					lc.TimetableID, lc.TimetableAirport = timetable.ID, timetable.Airport
				}
				if isSelected {
					imgui.SetItemDefaultFocus()
				}
			}
			imgui.EndCombo()
		}
	}

	// Scenario traffic comes at the rates set below, which the Departures and
	// Arrivals headers already total; only published traffic has a shape worth
	// plotting.
	if lc.TrafficSource != sim.TrafficSourceScenario {
		c.drawTrafficPlot(spec, p)
	}
}

///////////////////////////////////////////////////////////////////////////
// Traffic preview plot

const (
	trafficPlotWidth  = 560
	trafficPlotHeight = 80

	// trafficPlotBandwidth is the standard deviation, in minutes, of the
	// Gaussian the per-minute operation counts are smoothed with. Operations
	// come in clumps, so the raw counts are nothing but spikes; a few minutes of
	// smoothing turns them into the rate a controller would feel.
	trafficPlotBandwidth = 6

	// trafficPlotRateStep is what the vertical axis rounds up to. Rounding
	// coarsely keeps the scale still as the start time moves, so that two times
	// can be compared by eye rather than by reading the axis each time.
	trafficPlotRateStep = 20

	// trafficPlotLookahead is how far ahead of the minute under the cursor the
	// tooltip counts the traffic, answering what the rate under it can't: how
	// many aircraft actually turn up if the sim starts there.
	trafficPlotLookahead = 15

	// trafficPreviewRetryDelay is how long a failed preview request stands as
	// the answer for its settings before they are asked about again. A remote
	// server's failure may be a passing one--a network blip, a restart--and
	// asking again is how the plot comes back.
	trafficPreviewRetryDelay = 5 * time.Second
)

var (
	trafficPlotDepartureColor  = imgui.Vec4{.36, .66, .38, 1}
	trafficPlotArrivalColor    = imgui.Vec4{.44, .61, .85, 1}
	trafficPlotOverflightColor = imgui.Vec4{.85, .68, .36, 1}
)

// drawTrafficPlot draws how much traffic the selected start time brings over
// the following two hours: departures, arrivals, and overflights stacked, with
// the local clock along the bottom. Only published traffic has a shape to plot,
// and the counts behind it come from the server, which is the one with the
// flight data and the one that will fly it.
func (c *NewSimConfiguration) drawTrafficPlot(spec *server.ScenarioSpec, p platform.Platform) {
	c.updateTrafficPreview(spec)

	if c.trafficPreviewError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " " + c.trafficPreviewError.Error())
		imgui.PopStyleColor()
		return
	}
	// The preview reaches past both ends of the window so that the smoothing has
	// something to work with at the edges; only the window itself is drawn.
	pad := int(sim.TrafficCountsPad / time.Minute)
	window := int(sim.TrafficCountsWindow / time.Minute)
	if len(c.trafficPreviewDepartures) < pad+window || len(c.trafficPreviewArrivals) < pad+window {
		imgui.Text("Estimating traffic...")
		return
	}
	departures := smoothTrafficCounts(c.trafficPreviewDepartures)[pad : pad+window]
	arrivals := smoothTrafficCounts(c.trafficPreviewArrivals)[pad : pad+window]

	// No traffic source publishes overflights--they are generated at the
	// scenario's rate whatever the source--so their band is flat.
	overflightRate := spec.LaunchConfig.WorkedOverflightRate()
	overflights := make([]float32, window)
	for i := range overflights {
		overflights[i] = overflightRate
	}

	series := [3][]float32{departures, arrivals, overflights}
	colors := [3]imgui.Vec4{trafficPlotDepartureColor, trafficPlotArrivalColor, trafficPlotOverflightColor}

	// Round the peak up so the axis holds still as the start time moves.
	var peak float32
	for i := range window {
		peak = max(peak, departures[i]+arrivals[i]+overflightRate)
	}
	maxRate := trafficPlotRateStep * math.Ceil(peak/trafficPlotRateStep)
	maxRate = max(maxRate, trafficPlotRateStep)

	imgui.BeginGroup()
	imgui.Text("Next 2 hours:")
	drawTrafficPlotLegend(trafficPlotDepartureColor, "departures", sumTrafficCounts(c.trafficPreviewDepartures, pad, window))
	drawTrafficPlotLegend(trafficPlotArrivalColor, "arrivals", sumTrafficCounts(c.trafficPreviewArrivals, pad, window))
	drawTrafficPlotLegend(trafficPlotOverflightColor, "overflights", int(overflightRate*2+.5))
	imgui.EndGroup()
	imgui.SameLine()

	scale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	size := imgui.Vec2{X: scale * trafficPlotWidth, Y: scale * trafficPlotHeight}
	p0 := imgui.CursorScreenPos()
	p1 := imgui.Vec2{X: p0.X + size.X, Y: p0.Y + size.Y}

	x := func(minute int) float32 { return p0.X + size.X*float32(minute)/float32(window-1) }
	y := func(rate float32) float32 { return p1.Y - size.Y*min(rate/maxRate, 1) }

	drawList := imgui.WindowDrawList()
	drawList.AddRectFilled(p0, p1, imgui.ColorU32Vec4(imgui.Vec4{.1, .1, .12, 1}))

	// levels[0] is the bottom of the stack and each band raises it by its own
	// rates, so a band fills between levels[band] and levels[band+1] and the
	// last level is the total.
	levels := make([][]float32, len(series)+1)
	levels[0] = make([]float32, window)
	for band, rates := range series {
		levels[band+1] = make([]float32, window)
		for i, rate := range rates {
			levels[band+1][i] = levels[band][i] + rate
		}
	}

	// The fills are a quad per minute, which anti-aliasing would leave a seam
	// between; the outlines drawn after them do want it.
	const aaFlags = imgui.DrawListFlagsAntiAliasedLines | imgui.DrawListFlagsAntiAliasedFill
	prevFlags := drawList.Flags()
	drawList.SetFlags(prevFlags &^ aaFlags)
	for band, color := range colors {
		color.W = .8
		fillColor := imgui.ColorU32Vec4(color)
		bottom, top := levels[band], levels[band+1]
		for i := range window - 1 {
			drawList.AddQuadFilled(imgui.Vec2{X: x(i), Y: y(bottom[i])},
				imgui.Vec2{X: x(i + 1), Y: y(bottom[i+1])},
				imgui.Vec2{X: x(i + 1), Y: y(top[i+1])},
				imgui.Vec2{X: x(i), Y: y(top[i])}, fillColor)
		}
	}
	drawList.SetFlags(prevFlags)

	outline := make([]imgui.Vec2, window)
	for band, color := range colors {
		for i, top := range levels[band+1] {
			outline[i] = imgui.Vec2{X: x(i), Y: y(top)}
		}
		drawList.AddPolyline(&outline[0], int32(window), imgui.ColorU32Vec4(color), 0, scale)
	}

	c.drawTrafficPlotAxes(drawList, spec, p0, p1, size, window, maxRate)
	drawList.AddRect(p0, p1, imgui.ColorU32Vec4(imgui.Vec4{.4, .4, .4, 1}))

	imgui.Dummy(size)

	if imgui.IsItemHovered() {
		minute := int(math.Clamp((imgui.MousePos().X-p0.X)/size.X*float32(window-1), 0, float32(window-1)))
		cursor := x(minute)
		drawList.AddLine(imgui.Vec2{X: cursor, Y: p0.Y}, imgui.Vec2{X: cursor, Y: p1.Y},
			imgui.ColorU32Vec4(imgui.Vec4{1, 1, 1, .6}))
		// The curve is a rate: what the traffic around this minute comes to per
		// hour, not a count of what follows it. Give the count over the next
		// quarter hour too, since that is the other thing worth knowing about a
		// time and reading it off a rate is guesswork.
		imgui.SetTooltip(fmt.Sprintf("%s\nrate: %d dep, %d arr, %d ovf per hour\n"+
			"next %d min: %d dep, %d arr, %d ovf",
			c.trafficPlotTime(spec, minute), int(departures[minute]+.5), int(arrivals[minute]+.5),
			int(overflightRate+.5), trafficPlotLookahead,
			sumTrafficCounts(c.trafficPreviewDepartures, pad+minute, trafficPlotLookahead),
			sumTrafficCounts(c.trafficPreviewArrivals, pad+minute, trafficPlotLookahead),
			int(overflightRate*trafficPlotLookahead/60+.5)))
	}
}

// drawTrafficPlotAxes draws the half-hour gridlines with the local clock time at
// each, and marks the top of the vertical axis with the rate it stands for.
func (c *NewSimConfiguration) drawTrafficPlotAxes(drawList *imgui.DrawList, spec *server.ScenarioSpec,
	p0, p1, size imgui.Vec2, window int, maxRate float32) {
	gridColor := imgui.ColorU32Vec4(imgui.Vec4{1, 1, 1, .25})
	labelColor := imgui.ColorU32Vec4(imgui.Vec4{1, 1, 1, .5})

	start := c.NewSimRequest.StartTime.Truncate(time.Minute)
	clock := makeScenarioClock(spec)
	for minute := range window {
		t := start.Add(time.Duration(minute) * time.Minute).In(clock.loc)
		if t.Minute()%30 != 0 {
			continue
		}
		x := p0.X + size.X*float32(minute)/float32(window-1)
		drawList.AddLine(imgui.Vec2{X: x, Y: p0.Y}, imgui.Vec2{X: x, Y: p1.Y}, gridColor)

		label := t.Format("15:04")
		if width := imgui.CalcTextSize(label).X; x+2+width < p1.X {
			drawList.AddTextVec2(imgui.Vec2{X: x + 2, Y: p1.Y - imgui.TextLineHeight()}, labelColor, label)
		}
	}

	// The horizontal rule is the top of the axis, so the label goes just below it.
	drawList.AddLine(imgui.Vec2{X: p0.X, Y: p0.Y}, imgui.Vec2{X: p1.X, Y: p0.Y}, gridColor)
	label := fmt.Sprintf("%d/hr", int(maxRate))
	drawList.AddTextVec2(imgui.Vec2{X: p1.X - imgui.CalcTextSize(label).X - 3, Y: p0.Y + 1}, labelColor, label)
}

func (c *NewSimConfiguration) trafficPlotTime(spec *server.ScenarioSpec, minute int) string {
	t := c.NewSimRequest.StartTime.Truncate(time.Minute).Add(time.Duration(minute) * time.Minute)
	return makeScenarioClock(spec).format(t, "15:04")
}

func drawTrafficPlotLegend(color imgui.Vec4, label string, count int) {
	imgui.PushStyleColorVec4(imgui.ColText, color)
	imgui.Text(fmt.Sprintf("%3d %s", count, label))
	imgui.PopStyleColor()
}

// sumTrafficCounts counts the operations over n minutes from offset, stopping
// at the end of what the server sent rather than running off it.
func sumTrafficCounts(counts []uint16, offset, n int) int {
	total := 0
	for _, count := range counts[offset:min(offset+n, len(counts))] {
		total += int(count)
	}
	return total
}

// smoothTrafficCounts turns per-minute operation counts into the rate at each
// minute, in operations per hour. The Gaussian is centered, so a minute's value
// describes the traffic around it--the half hour it sits in the middle of,
// weighted towards the minute itself--rather than the traffic that follows it.
// It is a rate and not a count: how much traffic an hour it would come to if it
// carried on at the density it has there, which is the sense in which a
// controller's ADR and AAR are per hour too.
func smoothTrafficCounts(counts []uint16) []float32 {
	// Truncate the Gaussian at three standard deviations; past that it
	// contributes under half a percent and only costs time.
	radius := 3 * trafficPlotBandwidth
	kernel := make([]float32, 2*radius+1)
	var kernelSum float32
	for i := range kernel {
		x := float32(i-radius) / trafficPlotBandwidth
		kernel[i] = math.FastExp(-.5 * x * x)
		kernelSum += kernel[i]
	}

	rates := make([]float32, len(counts))
	for i := range counts {
		var rate float32
		for j, weight := range kernel {
			if k := i + j - radius; k >= 0 && k < len(counts) {
				rate += weight * float32(counts[k])
			}
		}
		// One aircraft a minute is sixty an hour, which is how much traffic is
		// always talked about.
		rates[i] = 60 * rate / kernelSum
	}
	return rates
}

// updateTrafficPreview asks the server how much traffic the current settings
// would fly, if what came back last time doesn't already answer for them. Only
// one request is in flight at a time: dragging the start time around would
// otherwise put out a request a frame, and the answers are only worth having in
// the order they were asked for anyway. A failed answer stands only until
// trafficPreviewRetryAt, so a passing failure doesn't leave the error up for
// good.
func (c *NewSimConfiguration) updateTrafficPreview(spec *server.ScenarioSpec) {
	if c.trafficPreviewPending != "" {
		return
	}
	key := trafficPreviewKey(&c.NewSimRequest, spec)
	if key == c.trafficPreviewKey &&
		(c.trafficPreviewError == nil || time.Now().Before(c.trafficPreviewRetryAt)) {
		return
	}

	// The launch config's maps are the dialog's, which the user goes on editing
	// while the request is in flight; the request needs its own copy.
	args := &server.TrafficCountsArgs{
		Facility:     c.Facility,
		GroupName:    c.GroupName,
		ScenarioName: c.ScenarioName,
		StartTime:    c.NewSimRequest.StartTime,
		LaunchConfig: deep.MustCopy(spec.LaunchConfig),
	}
	if spec.LaunchConfig.TrafficSource == sim.TrafficSourceTimetable {
		// Where a timetable's day starts, worked out the same way Start() works
		// it out, so that the preview and the sim it previews agree.
		minutes, err := timetableStartMinute(c.NewSimRequest.StartTime, spec.LaunchConfig.TimetableAirport)
		if err != nil {
			c.trafficPreviewKey, c.trafficPreviewError = key, err
			c.trafficPreviewRetryAt = time.Now().Add(trafficPreviewRetryDelay)
			return
		}
		args.LaunchConfig.TimetableStartMinute = minutes
	}

	c.trafficPreviewPending = key
	go c.fetchTrafficPreview(c.selectedServer, key, args)
}

// fetchTrafficPreview runs in its own goroutine. Like fetchMETAR, its inputs
// come in by parameter--the server among them, which the UI thread reassigns
// when the user switches between a local and a remote sim--so that it reads
// nothing off the UI thread while the slow call is in flight.
func (c *NewSimConfiguration) fetchTrafficPreview(srv *client.Server, key string,
	args *server.TrafficCountsArgs) {
	var result server.TrafficCountsResult
	err := srv.CallWithTimeout(server.GetTrafficCountsRPC, args, &result)

	c.mu.Lock(c.lg)
	defer c.mu.Unlock(c.lg)

	// The scenario may have changed while the request was out, in which case
	// this answer is for a scenario no longer on screen; drawing it would be
	// worse than drawing nothing. The draw path asks again.
	if c.trafficPreviewPending != key {
		return
	}

	c.trafficPreviewKey = key
	c.trafficPreviewPending = ""
	c.trafficPreviewDepartures, c.trafficPreviewArrivals = result.Departures, result.Arrivals
	c.trafficPreviewError = err
	if err != nil {
		c.trafficPreviewRetryAt = time.Now().Add(trafficPreviewRetryDelay)
	}
}

// trafficPreviewKey gathers everything the server's answer depends on, so that
// the preview is asked for again exactly when one of them changes. Start times
// are keyed to the minute since that is as fine as the counts go.
func trafficPreviewKey(req *server.NewSimRequest, spec *server.ScenarioSpec) string {
	lc := &spec.LaunchConfig

	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s/%s %s %s %s %.1f %.1f", req.Facility, req.GroupName, req.ScenarioName,
		req.StartTime.UTC().Format("2006-01-02T15:04"), lc.TrafficSource, lc.TimetableID,
		lc.PublishedArrivalRateScale, lc.PublishedDepartureRateScale)

	// Which flows are on decides what flies, so it decides the counts as well.
	for airport, runways := range util.SortedMap(lc.DepartureEnabled) {
		for runway, categories := range util.SortedMap(runways) {
			for category, enabled := range util.SortedMap(categories) {
				if enabled {
					fmt.Fprintf(&b, " D%s/%s/%s", airport, runway, category)
				}
			}
		}
	}
	for group, airports := range util.SortedMap(lc.InboundFlowEnabled) {
		for airport, enabled := range util.SortedMap(airports) {
			if enabled {
				fmt.Fprintf(&b, " A%s/%s", group, airport)
			}
		}
	}
	return b.String()
}

// clearTrafficPreviewLocked drops the counts in hand, for when the scenario they
// were for is no longer the one on screen. Clearing the pending key abandons a
// request that is still out: its answer is for the old scenario too.
func (c *NewSimConfiguration) clearTrafficPreviewLocked() {
	c.trafficPreviewKey = ""
	c.trafficPreviewPending = ""
	c.trafficPreviewDepartures = nil
	c.trafficPreviewArrivals = nil
	c.trafficPreviewError = nil
	c.trafficPreviewRetryAt = time.Time{}
}

// initDefaultWindDirection computes the default wind direction range from the scenario's runways.
// It calculates the average runway heading and sets a ±30 degree range around it.
func (c *NewSimConfiguration) initDefaultWindDirection() {
	if c.ScenarioSpec == nil {
		return
	}

	// Average the headings of every runway the scenario works, at all of its
	// airports: they are aligned with the prevailing wind, so their average
	// points into it.
	var sumRunwayVecs [2]float32
	addRunway := func(airport string, id av.RunwayID) {
		dbap, ok := av.DB.Airports[airport]
		if !ok {
			return
		}
		for _, rwy := range dbap.Runways {
			if rwy.Id == id.Base() {
				// HeadingVector expects TrueHeading; we pass magnetic headings here,
				// but the constant magnetic variation cancels in the vector average.
				sumRunwayVecs = math.Add2f(sumRunwayVecs, math.HeadingVector(math.TrueHeading(rwy.Heading)))
			}
		}
	}
	for _, rwy := range c.ScenarioSpec.DepartureRunways {
		addRunway(rwy.Airport, rwy.Runway)
	}
	for _, rwy := range c.ScenarioSpec.ArrivalRunways {
		addRunway(rwy.Airport, rwy.Runway)
	}

	// Runway headings from the database are already magnetic, so the
	// average is magnetic as well; no further conversion needed.
	avgRwyMagneticHeading := float32(math.VectorHeading(sumRunwayVecs))

	// Set default wind direction range to ±30 degrees from average runway heading
	windDirMin := int(math.NormalizeHeading(avgRwyMagneticHeading - 30))
	windDirMax := int(math.NormalizeHeading(avgRwyMagneticHeading + 30))

	// Reset weather filter for the new scenario with default wind direction
	c.weatherFilter = wx.WeatherFilter{
		WindDirMin: &windDirMin,
		WindDirMax: &windDirMax,
	}
	c.weatherFilterError = ""
}

// fetchMETAR runs in its own goroutine. Its inputs come in by parameter so it
// never reads c.Facility / c.ScenarioSpec from the UI thread; staleness is
// detected via the fetchSeq snapshot. The slow wx.GetMETAR / wx.GetAtmosByTime
// calls happen without c.mu held so the UI thread (which also takes c.mu in the
// dialog draw) doesn't stall for several seconds while we read from resources.
func (c *NewSimConfiguration) fetchMETAR(seq uint64, facility string, airports []string, spec *server.ScenarioSpec) {
	c.mu.Lock(c.lg)
	if c.fetchSeq != seq {
		c.mu.Unlock(c.lg)
		return
	}
	if c.metarFacility == facility && slices.Equal(c.metarAirports, airports) {
		// No need to refetch, but the scenario may have changed
		// (different runways / weather filter), so resample the start time.
		c.updateStartTimeForRunways(spec)
		c.mu.Unlock(c.lg)
		return
	}
	c.mu.Unlock(c.lg)

	metarSOA, metarErr := wx.GetMETAR(airports)
	var metars map[string][]wx.METAR
	if metarErr == nil {
		metars = make(map[string][]wx.METAR)
		for ap, soa := range metarSOA {
			metars[ap] = soa.Decode(ap)
		}
	}
	// TRACON: single altitude at 5,000' (representative of terminal area
	// traffic). Center/ARTCC: FL240 and FL380 (lower and upper flight levels).
	isTRACON := av.DB.IsTRACON(facility)
	windsAloftAltitudes := [2]float32{24000, 38000}
	if isTRACON {
		windsAloftAltitudes = [2]float32{5000, 0}
	}
	atmosByTime, _ := wx.GetAtmosByTime(facility)

	c.mu.Lock(c.lg)
	defer c.mu.Unlock(c.lg)

	if c.fetchSeq != seq {
		return
	}

	c.airportMETAR = metars
	c.fetchMETARError = metarErr
	c.metarAirports = airports
	c.metarFacility = facility
	c.metarWidth = 0
	for _, ms := range metars {
		for _, m := range ms {
			c.metarWidth = max(c.metarWidth, len(metarObservation(m)))
		}
	}
	c.atmosByTime = atmosByTime
	c.isTRACON = isTRACON
	c.windsAloftAltitudes = windsAloftAltitudes
	c.availableWXIntervals = nil

	if metarErr != nil {
		return
	}

	c.computeAvailableWXIntervals(facility)
	c.updateStartTimeForRunways(spec)
}

// metarObservation is the observation as it is shown in the configuration
// window: without its report type and without the remarks.
func metarObservation(m wx.METAR) string {
	return strings.TrimPrefix(strings.TrimPrefix(m.Observation(), "METAR "), "SPECI ")
}

// metarText pads the observation out to the width of the longest one on hand.
// The METAR is drawn in a fixed-width font and is the widest thing in the
// window, so without this the window resizes under the cursor as the start time
// is scrubbed from one observation to the next.
func (c *NewSimConfiguration) metarText(m wx.METAR) string {
	return fmt.Sprintf("%-*s", c.metarWidth, metarObservation(m))
}

func (c *NewSimConfiguration) clearWeatherLocked() {
	c.airportMETAR = nil
	c.metarAirports = nil
	c.metarFacility = ""
	c.metarWidth = 0
	c.fetchMETARError = nil
	c.atmosByTime = nil
	c.isTRACON = false
	c.windsAloftAltitudes = [2]float32{}
	c.availableWXIntervals = nil
	c.weatherFilterError = ""
}

func (c *NewSimConfiguration) computeAvailableWXIntervals(facility string) {
	// Extract METAR times from all airports
	var metarTimes []time.Time
	for _, metars := range c.airportMETAR {
		for _, m := range metars {
			metarTimes = append(metarTimes, m.Time.UTC())
		}
	}
	slices.SortFunc(metarTimes, func(a, b time.Time) int { return a.Compare(b) })

	// Compute METAR intervals
	var metarIntervals []util.TimeInterval
	if len(metarTimes) > 0 {
		metarIntervals = wx.METARIntervals(metarTimes)
	}

	// Get facility-specific intervals from local resources.
	// TRACONs and ARTCCs have different data histories.
	var facilityIntervals []util.TimeInterval
	if c.isTRACON {
		if intervals, ok := wx.GetTRACONTimeIntervals()[facility]; ok {
			facilityIntervals = intervals
		}
	} else {
		if intervals, ok := wx.GetARTCCTimeIntervals()[facility]; ok {
			facilityIntervals = intervals
		}
	}

	if len(facilityIntervals) == 0 {
		// Just use the METAR.
		c.availableWXIntervals = wx.MergeAndAlignToMidnight(metarIntervals)
	} else {
		// Intersect METAR intervals with facility intervals and align to midnight
		c.availableWXIntervals = wx.MergeAndAlignToMidnight(metarIntervals, facilityIntervals)
	}
}

const (
	NewSimCreateLocal = iota
	NewSimCreateRemote
	NewSimJoinRemote
)

type newSimType int32

func (n newSimType) String() string {
	return []string{
		"Create a local sim",
		"Create a sim on the public vice server",
		"Join a sim on the public vice server"}[n]
}

func (c *NewSimConfiguration) UIButtonText() string {
	return util.Select(c.newSimType == NewSimJoinRemote, "Join", "Next")
}

// ShowConfigurationWindow returns true if we should show the configuration screen
// (for create flows), false for join flow which goes directly to join.
func (c *NewSimConfiguration) ShowConfigurationWindow() bool {
	return c.newSimType != NewSimJoinRemote
}

// ScenarioSelectionDisabled returns true if the Next/Join button should be disabled
// on the scenario selection screen.
func (c *NewSimConfiguration) ScenarioSelectionDisabled(config *Config) bool {
	if c.newSimType == NewSimJoinRemote {
		// For join, need TCW selected and initials
		if c.selectedTCW == "" || len(config.ControllerInitials) != 2 {
			return true
		}
	}
	// For create flows, just need a valid scenario selected (no validation needed here)
	return false
}

// ConfigurationDisabled returns true if the Create button should be disabled
// on the configuration screen.
func (c *NewSimConfiguration) ConfigurationDisabled(config *Config) bool {
	if len(config.ControllerInitials) != 2 {
		return true
	}
	return c.newSimType == NewSimCreateRemote && (c.NewSimName == "" || (c.RequirePassword && c.Password == ""))
}

// getARTCCForFacility returns the ARTCC code for a given facility.
func getARTCCForFacility(facility string, catalog *server.ScenarioCatalog) string {
	if catalog != nil && catalog.ARTCC != "" {
		return catalog.ARTCC
	}
	if artcc := av.DB.ARTCCForFacility(facility); artcc != "" {
		return artcc
	}
	return facility
}

// trimFacilityName removes common suffixes from facility names for cleaner display.
func trimFacilityName(name, facilityType string) string {
	name = strings.TrimSpace(name)
	switch facilityType {
	case "TRACON":
		name = strings.TrimSuffix(name, " TRACON")
		name = strings.TrimSuffix(name, " ATCT/TRACON")
		name = strings.TrimSuffix(name, " Tower")
	case "ARTCC", "Area":
		name = strings.TrimSuffix(name, " ARTCC")
		name = strings.TrimSuffix(name, " Center")
	}
	return strings.TrimSpace(name)
}

// formatFacilityLabel returns a display label for a facility, including its full name if available.
func formatFacilityLabel(facility string) string {
	if traconInfo, ok := av.DB.TRACONs[facility]; ok {
		name := trimFacilityName(traconInfo.Name, "TRACON")
		return util.Select(name == "", facility, fmt.Sprintf("%s (%s)", facility, name))
	}
	if atctInfo, ok := av.DB.ATCTs[facility]; ok {
		name := trimFacilityName(atctInfo.Name, "ATCT")
		return util.Select(name == "", facility, fmt.Sprintf("%s (%s)", facility, name))
	}
	if artccInfo, ok := av.DB.ARTCCs[facility]; ok {
		name := trimFacilityName(artccInfo.Name, "ARTCC")
		return util.Select(name == "", facility, fmt.Sprintf("%s (%s)", facility, name))
	}
	return facility
}

// getAreaKey returns the area identifier for grouping scenarios.
// For TRACONs, returns the groupName; for ARTCCs, returns the trimmed Area field.
func getAreaKey(facility, groupName string, catalog *server.ScenarioCatalog) string {
	if av.DB.IsTRACON(facility) {
		return groupName
	}
	return trimFacilityName(catalog.Area, "Area")
}

// DrawScenarioSelectionUI draws Screen 1: scenario selection, sim type choice, and join flow UI
func (c *NewSimConfiguration) DrawScenarioSelectionUI(p platform.Platform, config *Config) bool {
	if err := c.mgr.UpdateRunningSims(); err != nil {
		c.lg.Warnf("UpdateRunningSims: %v", err)
	}

	if c.displayError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		if errors.Is(c.displayError, server.ErrRPCTimeout) || util.IsRPCServerError(c.displayError) {
			imgui.Text("Unable to reach vice server")
		} else if errors.Is(c.displayError, server.ErrInvalidPassword) {
			imgui.Text("Invalid password entered")
		} else {
			imgui.Text(c.displayError.Error())
		}
		imgui.PopStyleColor()
		imgui.Separator()
	}

	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	var runningSims map[string]*server.RunningSim
	if c.mgr.RemoteServer != nil {
		runningSims = c.mgr.RemoteServer.GetRunningSims()

		if imgui.BeginTableV("server", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Sim options:")

			origType := c.newSimType

			doButton := func(ty newSimType, srv *client.Server) {
				if imgui.RadioButtonIntPtr(ty.String(), (*int32)(&c.newSimType), int32(ty)) && origType != ty {
					c.selectedServer = srv
					c.SetFacility(c.Facility)
					c.displayError = nil
				}
			}

			imgui.TableNextColumn()
			doButton(NewSimCreateLocal, c.mgr.LocalServer)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			doButton(NewSimCreateRemote, c.mgr.RemoteServer)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()

			if len(runningSims) == 0 {
				imgui.BeginDisabled()
				if c.newSimType == NewSimJoinRemote {
					c.newSimType = NewSimCreateRemote
				}
			}
			doButton(NewSimJoinRemote, c.mgr.RemoteServer)
			if len(runningSims) == 0 {
				imgui.EndDisabled()
			}

			imgui.EndTable()
		}
	} else {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text("Unable to connect to the vice server; only local scenarios are available.")
		imgui.PopStyleColor()
		c.newSimType = NewSimCreateLocal
	}
	imgui.Separator()

	// Helper types and functions for facility data access and formatting
	const indentSpaces = "  "

	type areaInfo struct {
		area       string
		groupNames []string
	}

	type scenarioInfo struct {
		groupName    string
		scenarioName string
		spec         *server.ScenarioSpec
	}

	if c.newSimType == NewSimCreateLocal || c.newSimType == NewSimCreateRemote {
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))

		// Search/filter input
		filterW := tableScale*700 - 60

		imgui.SetNextItemWidth(filterW)
		imgui.InputTextWithHint("##filter", "Search scenarios, TRACONs, ARTCCs...", &c.filterText, 0, nil)
		imgui.SameLine()
		if imgui.Button("Clear") {
			c.filterText = ""
		}
		imgui.Spacing()

		// Precompute lowercased filter text once for all filter checks
		filterLower := strings.ToLower(c.filterText)

		// Helper to check if text matches filter
		matchesFilter := func(text string) bool {
			if filterLower == "" {
				return true
			}
			return strings.Contains(strings.ToLower(text), filterLower)
		}

		// Helper to check if a catalog has matching airports
		catalogHasMatchingAirport := func(catalog *server.ScenarioCatalog) bool {
			return filterLower == "" || util.SeqContainsFunc(slices.Values(catalog.Airports),
				func(ap string) bool { return strings.Contains(strings.ToLower(ap), filterLower) })
		}

		// Helper to check if a catalog has matching scenario names
		catalogHasMatchingScenario := func(catalog *server.ScenarioCatalog) bool {
			return filterLower == "" || util.SeqContainsFunc(maps.Keys(catalog.Scenarios),
				func(scenarioName string) bool { return strings.Contains(strings.ToLower(scenarioName), filterLower) })
		}

		// Helper to check if a catalog matches the filter (name, facility, airports, or scenarios)
		catalogMatchesFilter := func(catalog *server.ScenarioCatalog) bool {
			if filterLower == "" {
				return true
			}
			// Check airports in the catalog
			if catalogHasMatchingAirport(catalog) {
				return true
			}
			// Check facility name
			if matchesFilter(catalog.Facility) {
				return true
			}
			// Check scenario names
			if catalogHasMatchingScenario(catalog) {
				return true
			}
			return false
		}

		// Helper to check if any catalog in a facility matches
		facilityMatchesFilter := func(facility string, catalogs map[string]*server.ScenarioCatalog) bool {
			if filterLower == "" {
				return true
			}
			// Check facility name
			if matchesFilter(facility) {
				return true
			}
			// Check catalogs (airports and scenario names)
			for _, catalog := range catalogs {
				if catalogHasMatchingAirport(catalog) || catalogHasMatchingScenario(catalog) {
					return true
				}
			}
			return false
		}

		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("SelectScenario", 3, flags, imgui.Vec2{tableScale * 700, tableScale * 500}, 0.) {
			imgui.TableSetupColumn("ARTCC")
			imgui.TableSetupColumn("TRACON/AREA")
			imgui.TableSetupColumn("Scenario")
			imgui.TableHeadersRow()
			imgui.TableNextRow()

			// Build facility data structures
			catalogsByFacility := c.selectedServer.GetScenarioCatalogs()
			allFacilities := util.SortedMapKeys(catalogsByFacility)
			facilityCatalogs := make(map[string]*server.ScenarioCatalog, len(catalogsByFacility))
			for facility, catalogs := range catalogsByFacility {
				for _, cfg := range catalogs {
					facilityCatalogs[facility] = cfg
					break
				}
			}

			// Collect unique ARTCCs and track which ones match the filter
			artccs := make(map[string]struct{})
			matchingARTCCs := make(map[string]struct{})
			matchingFacilities := make(map[string]struct{})
			// Track groups that have matching scenarios specifically
			type facilityGroup struct {
				facility  string
				groupName string
			}
			var matchingGroups []facilityGroup
			// Helper to check if an ARTCC matches the filter
			artccMatchesFilter := func(artcc string) bool {
				if filterLower == "" {
					return true
				}
				if matchesFilter(artcc) {
					return true
				}
				// Also check the ARTCC's full name
				if artccInfo, ok := av.DB.ARTCCs[artcc]; ok {
					if matchesFilter(artccInfo.Name) {
						return true
					}
				}
				return false
			}

			for facility, catalogs := range catalogsByFacility {
				info := facilityCatalogs[facility]
				if info == nil {
					continue
				}
				artcc := getARTCCForFacility(facility, info)
				artccs[artcc] = struct{}{}

				// Check if this facility matches the filter (including ARTCC name)
				if facilityMatchesFilter(facility, catalogs) || artccMatchesFilter(artcc) {
					matchingARTCCs[artcc] = struct{}{}
					matchingFacilities[facility] = struct{}{}
				}

				// Track groups with matching scenarios
				if filterLower != "" {
					for groupName, catalog := range catalogs {
						if catalogHasMatchingScenario(catalog) {
							matchingGroups = append(matchingGroups, facilityGroup{facility, groupName})
						}
					}
				}
			}

			// Auto-select ARTCC if only one matches the filter
			selectedARTCC := ""
			if c.Facility != "" {
				selectedARTCC = getARTCCForFacility(c.Facility, facilityCatalogs[c.Facility])
			}
			if filterLower != "" && len(matchingARTCCs) == 1 {
				for artcc := range matchingARTCCs {
					if artcc != selectedARTCC {
						// Find first matching facility in this ARTCC and select it
						for facility := range matchingFacilities {
							if getARTCCForFacility(facility, facilityCatalogs[facility]) == artcc {
								c.SetFacility(facility)
								selectedARTCC = artcc
								break
							}
						}
					}
					break
				}
			}

			// Auto-select facility if only one matches within the selected ARTCC
			if filterLower != "" && selectedARTCC != "" {
				var matchingInARTCC []string
				for facility := range matchingFacilities {
					if getARTCCForFacility(facility, facilityCatalogs[facility]) == selectedARTCC {
						matchingInARTCC = append(matchingInARTCC, facility)
					}
				}
				if len(matchingInARTCC) == 1 && matchingInARTCC[0] != c.Facility {
					c.SetFacility(matchingInARTCC[0])
				}
			}

			// Ensure we have a group with matching scenarios selected, but only if the
			// current ARTCC doesn't have any matching facilities (respect user's ARTCC choice)
			_, currentARTCCHasMatches := matchingARTCCs[selectedARTCC]
			if filterLower != "" && len(matchingGroups) > 0 && !currentARTCCHasMatches {
				// Sort for deterministic selection
				sort.Slice(matchingGroups, func(i, j int) bool {
					if matchingGroups[i].facility != matchingGroups[j].facility {
						return matchingGroups[i].facility < matchingGroups[j].facility
					}
					return matchingGroups[i].groupName < matchingGroups[j].groupName
				})
				fg := matchingGroups[0]
				c.SetFacility(fg.facility)
				c.SetScenario(fg.groupName, c.selectedFacilityCatalogs[fg.groupName].DefaultScenario)
				selectedARTCC = getARTCCForFacility(fg.facility, facilityCatalogs[fg.facility])
			}

			// Calculate proportional column widths: 25%, 25%, 50%
			totalWidth := tableScale * 700
			artccWidth := max(totalWidth*0.25, tableScale*170)
			traconWidth := max(totalWidth*0.25, tableScale*160)
			scenarioWidth := max(totalWidth*0.50, tableScale*280)
			columnHeight := tableScale * 480

			// Column 1: ARTCC list
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("artccs", imgui.Vec2{artccWidth, columnHeight}, 0, 0) {
				for artcc := range util.SortedMap(artccs) {
					name := trimFacilityName(av.DB.ARTCCs[artcc].Name, "ARTCC")
					if name == "" {
						name = artcc
					}
					label := fmt.Sprintf("%s (%s)", artcc, name)
					// Filter: show if name matches or if any facility in this ARTCC has matching airports
					_, artccMatches := matchingARTCCs[artcc]
					if filterLower != "" && !artccMatches && !matchesFilter(artcc) && !matchesFilter(name) {
						continue
					}
					if imgui.SelectableBoolV(label, artcc == selectedARTCC, 0, imgui.Vec2{}) && artcc != selectedARTCC {
						// Find first matching facility in this ARTCC
						var facilityToSelect string
						for facility := range matchingFacilities {
							if getARTCCForFacility(facility, facilityCatalogs[facility]) == artcc {
								facilityToSelect = facility
								break
							}
						}
						if facilityToSelect == "" {
							// No matching facility, just pick the first one
							for _, facility := range allFacilities {
								if getARTCCForFacility(facility, facilityCatalogs[facility]) == artcc {
									facilityToSelect = facility
									break
								}
							}
						}
						if facilityToSelect != "" {
							c.SetFacility(facilityToSelect)
							selectedARTCC = artcc // Update for this frame
						}
					}
				}
			}
			imgui.EndChild()

			// Column 2: TRACONs or ARTCC areas for selected ARTCC
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("tracons/areas", imgui.Vec2{traconWidth, columnHeight}, 0, 0) {
				for _, facility := range allFacilities {
					info := facilityCatalogs[facility]
					if info == nil {
						continue
					}
					artcc := getARTCCForFacility(facility, info)
					if selectedARTCC != "" && artcc != selectedARTCC {
						continue
					}

					// Build area/group structure for this facility, only including matching catalogs
					catalogs := catalogsByFacility[facility]
					isTRACON := av.DB.IsTRACON(facility)
					areaToGroups := make(map[string]*areaInfo)

					for groupName, gcfg := range catalogs {
						// Skip catalogs that don't match the filter (unless ARTCC matches)
						if filterLower != "" && !catalogMatchesFilter(gcfg) && !artccMatchesFilter(artcc) {
							continue
						}
						area := getAreaKey(facility, groupName, gcfg)
						if areaToGroups[area] == nil {
							areaToGroups[area] = &areaInfo{area: area}
						}
						areaToGroups[area].groupNames = append(areaToGroups[area].groupNames, groupName)
					}

					if len(areaToGroups) == 0 {
						continue
					}

					// Display facility label
					label := formatFacilityLabel(facility)
					if imgui.SelectableBoolV(label, facility == c.Facility, 0, imgui.Vec2{}) && facility != c.Facility {
						c.SetFacility(facility)
					}

					// Display sub-items (groups for TRACONs, areas for ARTCCs)
					if facility == c.Facility {
						for aInfo := range util.SortedMapValues(areaToGroups) {
							// For TRACONs, just show group names; for ARTCCs, show area names
							itemLabel := indentSpaces + aInfo.area
							if !isTRACON && aInfo.area == "" {
								itemLabel = indentSpaces + aInfo.groupNames[0]
							}
							// Check if any group in this area/item is selected
							selected := slices.Contains(aInfo.groupNames, c.GroupName)
							if imgui.SelectableBoolV(itemLabel, selected, 0, imgui.Vec2{}) {
								firstGroup := aInfo.groupNames[0]
								if firstGroup != c.GroupName {
									c.SetScenario(firstGroup, catalogs[firstGroup].DefaultScenario)
								}
							}
						}
					}
				}
			}
			imgui.EndChild()

			// Column 3: Scenarios for the selected TRACON or area
			imgui.TableNextColumn()
			if imgui.BeginChildStrV("scenarios", imgui.Vec2{scenarioWidth, columnHeight}, 0, 0) {
				selectedCatalog := c.selectedFacilityCatalogs[c.GroupName]
				if selectedCatalog != nil {
					selectedArea := getAreaKey(c.Facility, c.GroupName, selectedCatalog)

					// Collect all scenarios from groups with the same area
					type scenarioWithCatalog struct {
						scenarioInfo
						catalog *server.ScenarioCatalog
					}
					var allScenarios []scenarioWithCatalog
					for groupName, group := range c.selectedFacilityCatalogs {
						if getAreaKey(c.Facility, groupName, group) == selectedArea {
							for name, spec := range group.Scenarios {
								allScenarios = append(allScenarios, scenarioWithCatalog{
									scenarioInfo: scenarioInfo{
										groupName:    groupName,
										scenarioName: name,
										spec:         spec,
									},
									catalog: group,
								})
							}
						}
					}

					// Sort and display scenarios
					sort.Slice(allScenarios, func(i, j int) bool {
						return allScenarios[i].scenarioName < allScenarios[j].scenarioName
					})
					for _, s := range allScenarios {
						// Filter scenarios: show if this specific scenario name matches, OR
						// if the catalog has a matching airport/facility name (but NOT because
						// another scenario in the catalog matches), OR if the ARTCC matches
						if filterLower != "" &&
							!matchesFilter(s.scenarioName) &&
							!catalogHasMatchingAirport(s.catalog) &&
							!matchesFilter(s.catalog.Facility) &&
							!artccMatchesFilter(selectedARTCC) {
							continue
						}
						selected := s.groupName == c.GroupName && s.scenarioName == c.ScenarioName
						if imgui.SelectableBoolV(s.scenarioName, selected, 0, imgui.Vec2{}) {
							c.SetScenario(s.groupName, s.scenarioName)
						}
					}
				}
			}
			imgui.EndChild()

			imgui.EndTable()
		}

		if len(c.ScenarioSpec.ArrivalRunways) > 0 {
			var a []string
			for _, rwy := range c.ScenarioSpec.ArrivalRunways {
				a = append(a, rwy.Airport+"/"+string(rwy.Runway))
			}
			sort.Strings(a)
			base := "Landing: "
			for len(a) > 0 {
				const max = 7 // per line
				if len(a) > max {
					imgui.Text(base + strings.Join(a[:max], ", "))
					base = "    "
					a = a[max:]
				} else {
					imgui.Text(base + strings.Join(a, ", "))
					break
				}
			}
		}
		if desc := c.ScenarioSpec.Description; desc != "" {
			imgui.Spacing()
			imgui.Separator()
			imgui.Spacing()
			if imgui.BeginChildStrV("scenario_desc", imgui.Vec2{0, 0}, imgui.ChildFlagsBorders|imgui.ChildFlagsAutoResizeY, 0) {
				imgui.TextWrapped(desc)
			}
			imgui.EndChild()
		}
		// Configuration options (initials, checkboxes, METAR) are now on Screen 2
	} else {
		// Join remote
		rs, ok := runningSims[c.joinRequest.SimName]
		if !ok || c.joinRequest.SimName == "" {
			c.joinRequest.SimName, rs = util.FirstSortedMapEntry(runningSims)
		}
		controllersForGroup := controlPositionsForGroup(c.selectedServer, rs.GroupName)

		imgui.Text("Available simulations:")
		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingFixedFit
		if imgui.BeginTableV("simulation", 4, flags, imgui.Vec2{tableScale * 700, 0}, 0.) {
			imgui.TableSetupColumn("") // lock
			imgui.TableSetupColumn("Name")
			imgui.TableSetupColumn("Configuration")
			imgui.TableSetupColumn("Controllers")
			imgui.TableHeadersRow()

			for simName, rs := range util.SortedMap(runningSims) {
				imgui.PushIDStr(simName)
				imgui.TableNextRow()
				imgui.TableNextColumn()

				// Indicate if a password is required
				if rs.RequirePassword {
					imgui.Text(renderer.FontAwesomeIconLock)
				}
				imgui.TableNextColumn()

				selected := simName == c.joinRequest.SimName
				selFlags := imgui.SelectableFlagsSpanAllColumns | imgui.SelectableFlagsNoAutoClosePopups
				if imgui.SelectableBoolV(simName, selected, selFlags, imgui.Vec2{}) {
					c.joinRequest.SimName = simName
					// Reset TCW selection when switching sims
					c.selectedTCW = ""
					c.selectedTCPs = nil
				}

				imgui.TableNextColumn()
				imgui.Text(runningSims[simName].ScenarioName)

				imgui.TableNextColumn()
				// Count occupied vs total TCWs
				var occupied, total int
				var occupiedTCWs []string
				for tcw, state := range rs.CurrentConsolidation {
					total++
					if state.IsOccupied() {
						occupied++
						occupiedTCWs = append(occupiedTCWs,
							controllerDisplayLabel(controllersForGroup, av.ControlPosition(tcw)),
						)
					}
				}
				controllers := fmt.Sprintf("%d / %d", occupied, total)
				imgui.Text(controllers)
				if imgui.IsItemHovered() && occupied > 0 {
					slices.Sort(occupiedTCWs)
					imgui.SetTooltip(strings.Join(occupiedTCWs, ", "))
				}

				imgui.PopID()
			}
			imgui.EndTable()
		}

		// Handle the case where selected TCW is no longer valid
		if c.selectedTCW != "" {
			if state, ok := rs.CurrentConsolidation[c.selectedTCW]; ok {
				// Check if TCW is still valid for current mode
				if c.showReliefPositions && !state.IsOccupied() {
					c.selectedTCW = ""
				} else if !c.showReliefPositions && state.IsOccupied() {
					c.selectedTCW = ""
				}
			} else {
				c.selectedTCW = ""
			}
		}

		// Format TCPs for display (SSA style: "primary *sec1 sec2")
		fmtTCPs := func(cons server.TCPConsolidation) string {
			var result strings.Builder
			result.WriteString(controllerDisplayLabel(controllersForGroup, av.ControlPosition(cons.PrimaryTCP)))
			for _, sec := range cons.SecondaryTCPs {
				prefix := ""
				if sec.Type == sim.ConsolidationBasic {
					prefix = "*"
				}
				result.WriteString(" " + prefix +
					controllerDisplayLabel(controllersForGroup, av.ControlPosition(sec.TCP)))
			}
			return result.String()
		}

		// Compute covered TCPs (primary at an occupied TCW)
		coveredPrimaryTCPs := make(map[sim.TCP]bool)
		for _, cons := range rs.CurrentConsolidation {
			if cons.PrimaryTCP != "" && cons.IsOccupied() {
				coveredPrimaryTCPs[cons.PrimaryTCP] = true
			}
		}

		// getAvailableTCPs returns all TCPs that can be selected:
		// - All positions (primary + secondary) from unoccupied TCWs
		// - Only secondary positions from occupied TCWs
		getAvailableTCPs := func() map[sim.TCP]bool {
			result := make(map[sim.TCP]bool)
			for _, cons := range rs.CurrentConsolidation {
				if cons.PrimaryTCP != "" && !cons.IsOccupied() {
					result[cons.PrimaryTCP] = true
				}
				for _, sec := range cons.SecondaryTCPs {
					result[sec.TCP] = true
				}
			}
			return result
		}

		// getDefaultSelectedTCPs returns the TCPs that should be selected by default for a TCW:
		// - Currently owned positions by the TCW (if any)
		// - Otherwise, just the position with the same name as the TCW
		getDefaultSelectedTCPs := func(tcw sim.TCW) map[sim.TCP]bool {
			result := make(map[sim.TCP]bool)
			cons := rs.CurrentConsolidation[tcw]
			if cons.PrimaryTCP != "" {
				result[cons.PrimaryTCP] = true
			}
			for _, sec := range cons.SecondaryTCPs {
				result[sec.TCP] = true
			}

			// If no positions found, default to just the TCW name
			if len(result) == 0 {
				result[sim.TCP(tcw)] = true
			}
			return result
		}

		// Checkbox for showing relief positions (only if some TCWs are occupied)
		if len(coveredPrimaryTCPs) > 0 {
			if imgui.Checkbox("Join as relief (show occupied positions)", &c.showReliefPositions) {
				// Clear selection when mode changes
				c.selectedTCW = ""
				c.selectedTCPs = nil
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Relief sign-in shares control with existing controller")
			}
		}

		// Sign-on options table
		imgui.Spacing()
		tableFlags := imgui.TableFlagsSizingFixedFit
		if imgui.BeginTableV("signon_options", 2, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Label")
			imgui.TableSetupColumn("Value")

			// Row 1: Select TCW
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Select TCW:")
			imgui.TableNextColumn()
			const tcwsPerRow = 8
			tcwCol := 0
			startX := imgui.CursorPosX()
			style := imgui.CurrentStyle()
			// Measure column width from the radio button circle plus the widest
			// 2-character label, then add spacing.
			colWidth := imgui.FrameHeight() + style.ItemInnerSpacing().X +
				imgui.CalcTextSizeV("WW", false, 0).X + style.ItemSpacing().X
			for tcw, cons := range util.SortedMap(rs.CurrentConsolidation) {
				// Filter: relief shows only occupied, normal shows only unoccupied
				if c.showReliefPositions != cons.IsOccupied() {
					continue
				}
				// Skip internal positions
				if len(tcw) > 0 && tcw[0] == '_' {
					continue
				}

				if tcwCol%tcwsPerRow != 0 {
					imgui.SameLine()
					imgui.SetCursorPosX(startX + float32(tcwCol%tcwsPerRow)*colWidth)
				}
				tcwCol++

				label := controllerDisplayLabel(controllersForGroup, av.ControlPosition(tcw))
				selected := tcw == c.selectedTCW
				if imgui.RadioButtonBool(fmt.Sprintf("%s##tcw-%s", label, tcw), selected) {
					c.selectedTCW = tcw
					c.joinRequest.JoiningAsRelief = c.showReliefPositions
					// Initialize selected TCPs from TCW's current positions
					if !c.showReliefPositions {
						c.selectedTCPs = getDefaultSelectedTCPs(tcw)
					} else {
						c.selectedTCPs = nil
					}
				}
				// Tooltip shows positions (and controller for relief mode)
				if c.showReliefPositions && imgui.IsItemHovered() {
					tooltip := fmtTCPs(cons)
					if len(cons.Initials) > 0 {
						tooltip += " (" + strings.Join(cons.Initials, ", ") + ")"
					}
					imgui.SetTooltip(tooltip)
				}
			}

			// Row 2: Select positions (only for unoccupied TCW selection, not relief)
			if c.selectedTCW != "" && !c.showReliefPositions {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Select positions:")
				imgui.TableNextColumn()

				// Show all available TCPs (excludes primaries at occupied TCWs)
				availableTCPs := getAvailableTCPs()
				tcpCol := 0
				tcpStartX := imgui.CursorPosX()
				tcpStyle := imgui.CurrentStyle()
				tcpColWidth := imgui.FrameHeight() + tcpStyle.ItemInnerSpacing().X +
					imgui.CalcTextSizeV("WW", false, 0).X + tcpStyle.ItemSpacing().X
				for tcp := range util.SortedMap(availableTCPs) {
					if len(tcp) > 0 && tcp[0] == '_' {
						continue
					}

					if tcpCol%tcwsPerRow != 0 {
						imgui.SameLine()
						imgui.SetCursorPosX(tcpStartX + float32(tcpCol%tcwsPerRow)*tcpColWidth)
					}
					tcpCol++

					isSelected := c.selectedTCPs[tcp]
					label := controllerDisplayLabel(controllersForGroup, av.ControlPosition(tcp))
					if imgui.Checkbox(fmt.Sprintf("%s##tcp-%s", label, tcp), &isSelected) {
						if c.selectedTCPs == nil {
							c.selectedTCPs = make(map[sim.TCP]bool)
						}
						c.selectedTCPs[tcp] = isSelected
					}
				}

				if tcpCol%tcwsPerRow != 0 {
					imgui.SameLine()
					imgui.SetCursorPosX(tcpStartX + float32(tcpCol%tcwsPerRow)*tcpColWidth)
				}
				imgui.Checkbox("Instructor", &c.Privileged)
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Allows control of any aircraft regardless of position ownership")
				}
			}

			// Row 3: Controller initials
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Controller initials:")
			imgui.TableNextColumn()
			imgui.SetNextItemWidth(50)
			initialsFlags := imgui.InputTextFlagsCharsUppercase | imgui.InputTextFlagsCallbackCharFilter | imgui.InputTextFlagsCallbackEdit
			imgui.InputTextWithHint("##initials", "XX", &config.ControllerInitials, initialsFlags,
				func(input imgui.InputTextCallbackData) int {
					if input.EventFlag()&imgui.InputTextFlagsCallbackCharFilter != 0 {
						if ch := input.EventChar(); ch < 'A' || ch > 'Z' {
							return 1
						}
					}
					if input.EventFlag()&imgui.InputTextFlagsCallbackEdit != 0 {
						if input.BufTextLen() > 2 {
							input.DeleteChars(2, input.BufTextLen()-2)
						}
					}
					return 0
				})
			if len(config.ControllerInitials) < 2 {
				imgui.SameLine()
				imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{.7, .1, .1, 1})
				imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " Must enter initials")
				imgui.PopStyleColor()
			}

			// Row 4: Password (if required)
			if rs.RequirePassword {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Password:")
				imgui.TableNextColumn()
				imgui.InputTextWithHint("##pw", "", &c.joinRequest.Password, 0, nil)
			}

			imgui.EndTable()
		}

	}

	return false
}

// drawSectionHeader draws a styled section header
func drawSectionHeader(title string) {
	imgui.Spacing()
	imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{0.6, 0.8, 1.0, 1.0})
	imgui.Text(strings.ToUpper(title))
	imgui.PopStyleColor()
	imgui.Separator()
}

// DrawConfigurationUI draws Screen 2: configuration options and traffic rates (combined)
func (c *NewSimConfiguration) DrawConfigurationUI(p platform.Platform, config *Config) bool {
	if c.displayError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(c.displayError.Error())
		imgui.PopStyleColor()
		imgui.Separator()
	}

	// CONTROLLER SETTINGS section
	drawSectionHeader("Controller Settings")

	// Controller initials
	imgui.Text("Initials:")
	imgui.SameLine()
	imgui.SetNextItemWidth(50)
	initialsFlags := imgui.InputTextFlagsCharsUppercase | imgui.InputTextFlagsCallbackCharFilter | imgui.InputTextFlagsCallbackEdit
	imgui.InputTextWithHint("##initials", "XX", &config.ControllerInitials, initialsFlags,
		func(input imgui.InputTextCallbackData) int {
			if input.EventFlag()&imgui.InputTextFlagsCallbackCharFilter != 0 {
				if ch := input.EventChar(); ch < 'A' || ch > 'Z' {
					return 1
				}
			}
			if input.EventFlag()&imgui.InputTextFlagsCallbackEdit != 0 {
				if input.BufTextLen() > 2 {
					input.DeleteChars(2, input.BufTextLen()-2)
				}
			}
			return 0
		})
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Enter two letters for controller initials")
	}
	if len(config.ControllerInitials) < 2 {
		imgui.SameLine()
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{.7, .1, .1, 1})
		imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " Must enter initials")
		imgui.PopStyleColor()
	}

	if c.newSimType == NewSimCreateRemote {
		imgui.Checkbox("Sign in with instructor/RPO privileges", &c.Privileged)
	}
	imgui.Spacing()

	// SESSION OPTIONS section (remote only)
	if c.newSimType == NewSimCreateRemote {
		drawSectionHeader("Session Options")

		imgui.Text("Name: " + c.NewSimName)

		imgui.Checkbox("Require Password", &c.RequirePassword)
		if c.RequirePassword {
			imgui.SameLine()
			imgui.SetNextItemWidth(150)
			imgui.InputTextWithHint("##password", "Enter password", &c.Password, 0, nil)
			if c.Password == "" {
				imgui.SameLine()
				imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{.7, .1, .1, 1})
				imgui.Text(renderer.FontAwesomeIconExclamationTriangle)
				imgui.PopStyleColor()
			}
		}
		imgui.Spacing()
	}

	// SIMULATION SETTINGS section
	drawSectionHeader("Simulation Settings")

	// A published flight's callsign is the one it really used, so a clash can't
	// be settled by drawing another: the flight would be thrown away instead.
	// Only the scenario's own generator can resample, so the setting is offered
	// only there.
	publishedTraffic := c.ScenarioSpec != nil &&
		c.ScenarioSpec.LaunchConfig.TrafficSource != sim.TrafficSourceScenario
	if publishedTraffic {
		imgui.BeginDisabled()
		c.NewSimRequest.EnforceUniqueCallsignSuffix = false
	}
	imgui.Checkbox("Ensure unique callsign suffixes", &c.NewSimRequest.EnforceUniqueCallsignSuffix)
	if publishedTraffic {
		imgui.EndDisabled()
		imgui.SameLine()
		imgui.Text("(" + c.ScenarioSpec.LaunchConfig.TrafficSource.String() +
			" traffic flies the callsigns it really used)")
	}

	imgui.Text("Readback error interval:")
	imgui.SameLine()
	imgui.SetNextItemWidth(200)
	imgui.SliderFloatV("##errorInterval", &c.PilotErrorInterval, 0, 30,
		util.Select(c.PilotErrorInterval == 0, "never", "%.1f min"), imgui.SliderFlagsNone)
	imgui.Spacing()

	// WEATHER & TIME section
	drawSectionHeader("Weather & Time")

	c.mu.Lock(c.lg)
	defer c.mu.Unlock(c.lg)

	if c.fetchMETARError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text("Error: " + c.fetchMETARError.Error())
		imgui.PopStyleColor()
	} else if len(c.airportMETAR) > 0 {
		c.drawWeatherFilterUI()
	}
	imgui.Spacing()

	// TRAFFIC SOURCE section
	drawSectionHeader("Traffic Source")
	c.drawTrafficSourceUI(c.ScenarioSpec, p)
	imgui.Spacing()

	// TRAFFIC RATES section
	drawSectionHeader("Traffic Rates")

	// Rate limit warning
	const rateLimit = 100.0
	if !c.ScenarioSpec.LaunchConfig.CheckRateLimits(rateLimit) {
		c.ScenarioSpec.LaunchConfig.ClampRates(rateLimit)
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " Rates reduced to stay within limits")
		imgui.PopStyleColor()
	}

	// Published IFR traffic or the scenario's own rate controls; drawDepartureUI
	// and drawArrivalUI show the appropriate controls for the traffic source.
	// The rate-derived totals mean nothing when the data decides the traffic, so
	// leave them out of the headers then. The totals are of the traffic the user
	// will work: a scenario that flies a neighboring airport's operations for
	// realism isn't offering them to anybody.
	lc := &c.ScenarioSpec.LaunchConfig
	if lc.HaveDepartures() {
		headerText := "Departures###departures"
		if lc.TrafficSource == sim.TrafficSourceScenario {
			headerText = fmt.Sprintf("Departures (Total: %d/hr)###departures",
				int(lc.WorkedDepartureRate()+0.5))
		}
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawDepartureUI(lc, p)
			imgui.Spacing()
		}
	}

	if lc.HaveArrivals() {
		headerText := "Arrivals###arrivals"
		if lc.TrafficSource == sim.TrafficSourceScenario {
			headerText = fmt.Sprintf("Arrivals (Total: %d/hr)###arrivals",
				int(lc.WorkedArrivalRate()+0.5))
		}
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawArrivalUI(lc, p)
			imgui.Spacing()
		}
	}

	// VFR Departures remain independent of the IFR traffic source.
	if len(lc.VFRAirportRates) > 0 {
		var vfrRate float32
		for _, rate := range lc.VFRAirportRates {
			r := rate * lc.VFRDepartureRateScale
			if r > 0 {
				vfrRate += r
			}
		}
		headerText := fmt.Sprintf(
			"VFR Departures (%d/hr)###vfrdepartures",
			int(vfrRate+0.5),
		)
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawVFRDepartureUI(lc, p)
			imgui.Spacing()
		}
	}

	// Overflights (collapsible)
	if lc.HaveOverflights() {
		ofRate := lc.WorkedOverflightRate()
		headerText := fmt.Sprintf("Overflights (%d/hr)###overflights", int(ofRate+0.5))
		if imgui.CollapsingHeaderBoolPtr(headerText, nil) {
			drawOverflightUI(lc, p)
			imgui.Spacing()
		}
	}

	// Emergency rate (always visible)
	imgui.Spacing()
	imgui.Text("Emergency aircraft rate:")
	imgui.SameLine()
	imgui.SetNextItemWidth(150)
	imgui.SliderFloatV("##emergencyRate", &lc.EmergencyAircraftRate, 0, 20,
		util.Select(lc.EmergencyAircraftRate == 0, "never", "%.1f /hr"), imgui.SliderFlagsNone)

	return false
}

func (c *NewSimConfiguration) Start(config *Config) error {
	c.ScenarioSpec.LaunchConfig.EnableTowerGoArounds = config.EnableTowerGoArounds

	if c.ScenarioSpec.LaunchConfig.TrafficSource == sim.TrafficSourceTimetable {
		minutes, err := timetableStartMinute(c.NewSimRequest.StartTime, c.ScenarioSpec.LaunchConfig.TimetableAirport)
		if err != nil {
			return err
		}
		c.ScenarioSpec.LaunchConfig.TimetableStartMinute = minutes
	}

	if c.newSimType == NewSimJoinRemote {
		// Set the privileged flag from the main config
		c.joinRequest.Privileged = c.Privileged
		// Set TCW from selection
		c.joinRequest.TCW = c.selectedTCW
		// Convert selected TCPs map to slice (only for non-relief)
		if !c.joinRequest.JoiningAsRelief {
			var tcps []sim.TCP
			for tcp, selected := range c.selectedTCPs {
				if selected {
					tcps = append(tcps, tcp)
				}
			}
			c.joinRequest.SelectedTCPs = tcps
		}
		c.joinRequest.Initials = config.ControllerInitials
		if err := c.mgr.ConnectToSim(c.joinRequest, config.ControllerInitials, c.selectedServer, c.lg); err != nil {
			c.lg.Errorf("ConnectToSim failed: %v", err)
			return err
		}
	} else {
		// Create sim configuration for new sim
		c.NewSimRequest.Initials = config.ControllerInitials
		if err := c.mgr.CreateNewSim(c.NewSimRequest, config.ControllerInitials, c.selectedServer, c.lg); err != nil {
			c.lg.Errorf("CreateNewSim failed: %v", err)
			return err
		}
	}

	*c.defaultFacility = c.Facility
	return nil
}

// drawPublishedDepartureUI and drawPublishedArrivalUI stand in for the rate
// controls when the sim is flying historical or timetable traffic: how much
// there is and where it goes comes from the data, so what is left to choose is
// how fast to fly it and which flows are active.
func drawPublishedDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	label := util.Select(lc.TrafficSource == sim.TrafficSourceHistorical,
		"Historical departure rate scale", "Timetable departure rate scale")
	imgui.SetNextItemWidth(260)
	changed = imgui.SliderFloatV(label, &lc.PublishedDepartureRateScale, 0, sim.MaxPublishedRateScale,
		"%.1f", imgui.SliderFlagsNoInput)

	airportDepartures := make(map[string]int) // key is e.g. KJFK, then count of runways cross categories.
	for ap, runwayEnabled := range lc.DepartureEnabled {
		for _, categories := range runwayEnabled {
			airportDepartures[ap] = airportDepartures[ap] + len(categories)
		}
	}
	maxDepartureCategories := 0
	for _, n := range airportDepartures {
		maxDepartureCategories = max(n, maxDepartureCategories)
	}
	if maxDepartureCategories == 0 {
		return
	}

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	adrColumns := min(3, maxDepartureCategories)
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("departureRunways", int32(1+3*adrColumns), flags, imgui.Vec2{tableScale * float32(200+250*adrColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range adrColumns {
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Category")
			imgui.TableSetupColumn("Active")
		}
		imgui.TableHeadersRow()

		for airport := range util.SortedMap(lc.DepartureEnabled) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(airport)

			imgui.PushIDStr(airport)
			adrColumn := 0
			for runway := range util.SortedMap(lc.DepartureEnabled[airport]) {
				imgui.PushIDStr(string(runway))

				for category := range util.SortedMap(lc.DepartureEnabled[airport][runway]) {
					imgui.TableNextColumn()
					imgui.Text(runway.Base()) // don't include extras in the UI
					imgui.TableNextColumn()

					imgui.PushIDStr(category)

					if category == "" {
						imgui.Text("(All)")
					} else {
						imgui.Text(category)
					}
					imgui.TableNextColumn()

					enabled := lc.DepartureEnabled[airport][runway][category]
					if imgui.Checkbox("##enabled", &enabled) {
						lc.DepartureEnabled[airport][runway][category] = enabled
						changed = true
					}

					adrColumn++

					if adrColumn < airportDepartures[airport] && adrColumn%adrColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.PopID()
				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}

	imgui.Separator()

	return
}

func drawPublishedArrivalUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	label := util.Select(lc.TrafficSource == sim.TrafficSourceHistorical,
		"Historical arrival rate scale", "Timetable arrival rate scale")
	imgui.SetNextItemWidth(260)
	changed = imgui.SliderFloatV(label, &lc.PublishedArrivalRateScale, 0, sim.MaxPublishedRateScale,
		"%.1f", imgui.SliderFlagsNoInput)

	// Go-arounds still apply; the arrival pushes don't, since when aircraft
	// show up is what the data says.
	changed = imgui.SliderFloatV("Go around probability", &lc.GoAroundRate, 0, 1, "%.02f", 0) || changed

	numAirportFlows := make(map[string]int)
	for _, groupEnabled := range lc.InboundFlowEnabled {
		for ap := range groupEnabled {
			numAirportFlows[ap] = numAirportFlows[ap] + 1
		}
	}
	if len(numAirportFlows) == 0 { // no arrivals
		return
	}
	maxAirportFlows := 0
	for _, n := range numAirportFlows {
		maxAirportFlows = max(n, maxAirportFlows)
	}

	aarColumns := min(3, maxAirportFlows)
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("arrivalgroups", int32(1+2*aarColumns), flags, imgui.Vec2{tableScale * float32(150+250*aarColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range aarColumns {
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("Active")
		}
		imgui.TableHeadersRow()

		for ap := range util.SortedMap(numAirportFlows) {
			imgui.PushIDStr(ap)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(ap)

			aarCol := 0
			for group, apEnabled := range util.SortedMap(lc.InboundFlowEnabled) {
				imgui.PushIDStr(group)
				if enabled, ok := apEnabled[ap]; ok {
					if aarCol > 0 && aarCol%aarColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.TableNextColumn()
					imgui.Text(group)
					imgui.TableNextColumn()
					if imgui.Checkbox("##enabled", &enabled) {
						lc.InboundFlowEnabled[group][ap] = enabled
						changed = true
					}
					aarCol++
				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}

	imgui.Separator()

	return
}

func drawDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	if lc.TrafficSource != sim.TrafficSourceScenario {
		return drawPublishedDepartureUI(lc, p)
	}
	if len(lc.DepartureRates) == 0 {
		return
	}

	airportDepartures := make(map[string]int) // key is e.g. KJFK, then count of active runways cross categories.
	for ap, runwayRates := range lc.DepartureRates {
		for _, categories := range runwayRates {
			airportDepartures[ap] = airportDepartures[ap] + len(categories)
		}
	}
	maxDepartureCategories := 0
	for _, n := range airportDepartures {
		maxDepartureCategories = max(n, maxDepartureCategories)
	}

	// SliderFlagsNoInput is more or less a hack to prevent keyboard focus
	// from being here initially.
	changed = imgui.SliderFloatV("Departure rate scale", &lc.DepartureRateScale, 0, 5, "%.1f", imgui.SliderFlagsNoInput) || changed

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp

	if lc.DepartureRateScale == 0 {
		imgui.BeginDisabled()
	}
	adrColumns := min(3, maxDepartureCategories)
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if imgui.BeginTableV("departureRunways", int32(1+3*adrColumns), flags, imgui.Vec2{tableScale * float32(200+250*adrColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range adrColumns {
			imgui.TableSetupColumn("Runway")
			imgui.TableSetupColumn("Category")
			imgui.TableSetupColumn("ADR")
		}
		imgui.TableHeadersRow()

		for airport := range util.SortedMap(lc.DepartureRates) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(airport)

			imgui.PushIDStr(airport)
			adrColumn := 0
			for runway := range util.SortedMap(lc.DepartureRates[airport]) {
				imgui.PushIDStr(string(runway))

				for category := range util.SortedMap(lc.DepartureRates[airport][runway]) {
					imgui.TableNextColumn()
					rshort := runway.Base() // don't include extras in the UI
					imgui.Text(rshort)
					imgui.TableNextColumn()

					imgui.PushIDStr(category)

					if category == "" {
						imgui.Text("(All)")
					} else {
						imgui.Text(category)
					}
					imgui.TableNextColumn()

					r := lc.DepartureRateScale * lc.DepartureRates[airport][runway][category]
					if imgui.InputFloatV("##adr", &r, 0, 0, "%g", 0) {
						lc.DepartureRates[airport][runway][category] = r / max(.01, lc.DepartureRateScale)
						changed = true
					}

					adrColumn++

					if adrColumn < airportDepartures[airport] && adrColumn%adrColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.PopID()
				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.DepartureRateScale == 0 {
		imgui.EndDisabled()
	}

	imgui.Separator()

	return
}

func drawVFRDepartureUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	if len(lc.VFRAirportRates) == 0 {
		return
	}

	// SliderFlagsNoInput is more or less a hack to prevent keyboard focus
	// from being here initially.
	changed = imgui.SliderFloatV("VFR departure rate scale", &lc.VFRDepartureRateScale, 0, 2, "%.1f", imgui.SliderFlagsNoInput) || changed

	if !lc.HaveVFRReportingRegions {
		imgui.BeginDisabled()
	}
	changed = imgui.InputIntV("Flight following request rate", &lc.VFFRequestRate, 0, 60, 0) || changed
	if !lc.HaveVFRReportingRegions {
		imgui.EndDisabled()
	}

	imgui.Separator()

	return
}

func drawArrivalUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	// Figure out the maximum number of inbound flows per airport to figure
	// out the number of table columns and also sum up the overall arrival
	// rate.
	numAirportFlows := make(map[string]int)
	for _, agr := range lc.InboundFlowRates {
		for ap := range agr {
			if ap != "overflights" {
				numAirportFlows[ap] = numAirportFlows[ap] + 1
			}
		}
	}
	if len(numAirportFlows) == 0 { // no arrivals
		return
	}
	if lc.TrafficSource != sim.TrafficSourceScenario {
		return drawPublishedArrivalUI(lc, p)
	}
	maxAirportFlows := 0
	for _, n := range numAirportFlows {
		maxAirportFlows = max(n, maxAirportFlows)
	}

	changed = imgui.SliderFloatV("Arrival/overflight rate scale", &lc.InboundFlowRateScale, 0, 5, "%.1f", imgui.SliderFlagsNoInput) || changed

	changed = imgui.SliderFloatV("Go around probability", &lc.GoAroundRate, 0, 1, "%.02f", 0) || changed

	changed = imgui.Checkbox("Include random arrival pushes", &lc.ArrivalPushes) || changed
	if !lc.ArrivalPushes {
		imgui.BeginDisabled()
	}
	freq := int32(lc.ArrivalPushFrequencyMinutes)
	changed = imgui.SliderInt("Push frequency (minutes)", &freq, 3, 60) || changed
	lc.ArrivalPushFrequencyMinutes = int(freq)
	mins := int32(lc.ArrivalPushLengthMinutes)
	changed = imgui.SliderInt("Length of push (minutes)", &mins, 5, 30) || changed
	lc.ArrivalPushLengthMinutes = int(mins)
	if !lc.ArrivalPushes {
		imgui.EndDisabled()
	}

	aarColumns := min(3, maxAirportFlows)
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if lc.InboundFlowRateScale == 0 {
		imgui.BeginDisabled()
	}
	if imgui.BeginTableV("arrivalgroups", int32(1+2*aarColumns), flags, imgui.Vec2{tableScale * float32(150+250*aarColumns), 0}, 0.) {
		imgui.TableSetupColumn("Airport")
		for range aarColumns {
			imgui.TableSetupColumn("Arrival")
			imgui.TableSetupColumn("AAR")
		}
		imgui.TableHeadersRow()

		for ap := range util.SortedMap(numAirportFlows) {
			imgui.PushIDStr(ap)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text(ap)

			aarCol := 0
			for group, aprates := range util.SortedMap(lc.InboundFlowRates) {
				imgui.PushIDStr(group)
				if rate, ok := aprates[ap]; ok {
					if aarCol > 0 && aarCol%aarColumns == 0 {
						// Overflow
						imgui.TableNextRow()
						imgui.TableNextColumn()
					}

					imgui.TableNextColumn()
					imgui.Text(group)
					imgui.TableNextColumn()
					r := rate * lc.InboundFlowRateScale
					if imgui.InputFloatV("##aar-"+ap, &r, 0, 0, "%g", 0) {
						changed = true
						lc.InboundFlowRates[group][ap] = r / max(.01, lc.InboundFlowRateScale)
					}
					aarCol++

				}
				imgui.PopID()
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.InboundFlowRateScale == 0 {
		imgui.EndDisabled()
	}

	imgui.Separator()

	return
}

func drawOverflightUI(lc *sim.LaunchConfig, p platform.Platform) (changed bool) {
	// Sum up the overall overflight rate
	overflightGroups := make(map[string]any)
	for group, rates := range lc.InboundFlowRates {
		if _, ok := rates["overflights"]; ok {
			overflightGroups[group] = nil
		}
	}
	if len(overflightGroups) == 0 {
		return
	}

	if lc.TrafficSource != sim.TrafficSourceScenario {
		imgui.TextDisabled("Overflights are randomly generated; these rates apply with any traffic source.")
	}

	ofColumns := min(3, len(overflightGroups))
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
	if lc.InboundFlowRateScale == 0 {
		imgui.BeginDisabled()
	}
	if imgui.BeginTableV("overflights", int32(2*ofColumns), flags, imgui.Vec2{tableScale * float32(250*ofColumns), 0}, 0.) {
		for range ofColumns {
			imgui.TableSetupColumn("Group")
			imgui.TableSetupColumn("Rate")
		}
		imgui.TableHeadersRow()

		ofCol := 0
		for group := range util.SortedMap(overflightGroups) {
			imgui.PushIDStr(group)
			if ofCol%ofColumns == 0 {
				imgui.TableNextRow()
			}

			imgui.TableNextColumn()
			imgui.Text(group)
			imgui.TableNextColumn()
			r := lc.InboundFlowRates[group]["overflights"] * lc.InboundFlowRateScale
			if imgui.InputFloatV("##of-"+group, &r, 0, 0, "%g", 0) {
				changed = true
				lc.InboundFlowRates[group]["overflights"] = r / max(.01, lc.InboundFlowRateScale)
			}
			ofCol++

			imgui.PopID()
		}
		imgui.EndTable()
	}
	if lc.InboundFlowRateScale == 0 {
		imgui.EndDisabled()
	}

	return
}

func controllerDisplayLabel(controllers map[av.ControlPosition]*av.Controller, pos av.ControlPosition) string {
	if ctrl, ok := controllers[pos]; ok && ctrl != nil {
		if label := ctrl.ERAMID(); label != "" {
			return label
		}
	}
	return string(pos)
}

func controlPositionsForGroup(server *client.Server, groupName string) map[sim.TCP]*av.Controller {
	if server == nil || groupName == "" {
		return nil
	}
	for _, groups := range server.GetScenarioCatalogs() {
		if catalog, ok := groups[groupName]; ok {
			return catalog.ControlPositions
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////

var acknowledgedATIS = make(map[string]string)

func drawScenarioInfoWindow(mgr *client.ConnectionManager, config *Config, c *client.ControlClient, activeRadarPane panes.Pane, p platform.Platform, lg *log.Logger) bool {
	// Ensure that the window is wide enough to show the description
	sz := imgui.CalcTextSize(c.State.SimDescription)
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{sz.X + 50, 0}, imgui.Vec2{100000, 100000})

	show := true
	applyPinWindowClass("ScenarioInfo", config, p)
	imgui.BeginV(c.State.SimDescription+"###ScenarioInfo", &show, imgui.WindowFlagsAlwaysAutoResize)
	drawPinButton("ScenarioInfo", config, p)

	if imgui.CollapsingHeaderBoolPtr("Controllers", nil) {
		// Make big(ish) tables somewhat more legible
		tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
			imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("controllers", 4, tableFlags, imgui.Vec2{}, 0) {
			imgui.TableSetupColumn("Workstation")
			imgui.TableSetupColumn("Name")
			imgui.TableSetupColumn("Human")
			imgui.TableSetupColumn("Positions")
			imgui.TableHeadersRow()

			// First the potentially-human-controlled ones
			tcws := util.SortedMapKeys(c.State.CurrentConsolidation)
			coveredPositions := make(map[av.ControlPosition]struct{})
			for _, tcw := range tcws {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(controllerDisplayLabel(c.State.Controllers, av.ControlPosition(tcw)))

				imgui.TableNextColumn()
				imgui.Text(c.State.Controllers[av.ControlPosition(tcw)].Callsign)

				imgui.TableNextColumn()
				sq := renderer.FontAwesomeIconCheckSquare
				// Center the square in the column: https://stackoverflow.com/a/66109051
				pos := imgui.CursorPosX() + float32(imgui.ColumnWidth()) - imgui.CalcTextSize(sq).X - imgui.ScrollX() -
					2*imgui.CurrentStyle().ItemSpacing().X
				if pos > imgui.CursorPosX() {
					imgui.SetCursorPos(imgui.Vec2{X: pos, Y: imgui.CursorPos().Y})
				}
				imgui.Text(sq)

				imgui.TableNextColumn()
				if cons, ok := c.State.CurrentConsolidation[tcw]; ok {
					var p []string
					for _, pos := range cons.OwnedPositions() {
						coveredPositions[pos] = struct{}{}
						ctrl := c.State.Controllers[pos]
						p = append(p, fmt.Sprintf("%s (%s, %s)",
							controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()),
							ctrl.Position,
							ctrl.Frequency.String(),
						))
					}

					var s strings.Builder
					for len(p) > 3 {
						s.WriteString(strings.Join(p[:3], ", ") + "\n")
						p = p[3:]
					}
					s.WriteString(strings.Join(p, ", "))
					imgui.Text(s.String())
				}
			}

			// Sort 2-char before 3-char and then alphabetically
			sorted := slices.Collect(maps.Keys(c.State.Controllers))
			slices.SortFunc(sorted, func(a, b sim.TCP) int {
				if len(a) < len(b) {
					return -1
				} else if len(a) > len(b) {
					return 1
				} else {
					return strings.Compare(string(a), string(b))
				}
			})

			for _, pos := range sorted {
				if _, ok := coveredPositions[pos]; ok {
					continue
				}

				ctrl := c.State.Controllers[pos]
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text(controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()))
				imgui.TableNextColumn()
				imgui.Text(ctrl.Callsign)
				imgui.TableNextColumn()
				imgui.TableNextColumn()
				imgui.Text(fmt.Sprintf("%s (%s, %s)",
					controllerDisplayLabel(c.State.Controllers, ctrl.PositionId()),
					ctrl.Position,
					ctrl.Frequency.String(),
				))
			}

			imgui.EndTable()
		}
	}

	drawScenarioBriefSection(mgr, config, c, p, lg)

	if len(c.State.METAR) > 0 {
		// Collect IFR airports: those with IFR departures or arrivals
		ifrAirports := make(map[string]bool)
		for ap := range c.State.LaunchConfig.DepartureRates {
			ifrAirports[ap] = true
		}
		for ap := range c.State.ArrivalAirports {
			ifrAirports[ap] = true
		}

		atisExpanded := imgui.CollapsingHeaderBoolPtr("ATIS / METAR", nil)
		if atisExpanded {
			tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
				imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
			if imgui.BeginTableV("atis_metar", 2, tableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumnV("ATIS", imgui.TableColumnFlagsWidthFixed, 0, 0)
				imgui.TableSetupColumn("METAR")
				imgui.TableHeadersRow()

				airports := util.SortedMapKeys(c.State.METAR)
				for _, ap := range airports {
					if !ifrAirports[ap] {
						continue
					}
					letter := c.State.ATISLetter[ap]
					metar := c.State.METAR[ap]

					imgui.TableNextRow()
					imgui.TableNextColumn()

					// Flash if ATIS letter changed since last acknowledgement
					if _, ok := acknowledgedATIS[ap]; !ok {
						acknowledgedATIS[ap] = letter
					}
					ui.fixedFont.ImguiPush()
					flashing := acknowledgedATIS[ap] != letter
					if flashing && int64(imgui.Time()*2)%2 == 0 {
						imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .2, .2, 1})
					}
					// Center the letter in the column
					colW := imgui.ColumnWidth()
					textW := imgui.CalcTextSize(letter).X
					pad := (colW - textW) / 2
					if pad > 0 {
						imgui.SetCursorPosX(imgui.CursorPosX() + pad)
					}
					if imgui.SelectableBoolV(letter+"##atis_"+ap, false, 0, imgui.Vec2{}) {
						acknowledgedATIS[ap] = letter
					}
					if flashing && int64(imgui.Time()*2)%2 == 0 {
						imgui.PopStyleColor()
					}

					imgui.TableNextColumn()
					raw := strings.TrimPrefix(metar.Observation(), "METAR ")
					raw = strings.TrimPrefix(raw, "SPECI ")
					imgui.Text(raw)
					imgui.PopFont()
				}

				imgui.EndTable()
			}

		}
	}

	if len(c.State.TFRs) > 0 {
		if imgui.CollapsingHeaderBoolPtr("TFRs", nil) {
			tableFlags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH |
				imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
			if imgui.BeginTableV("tfrs", 5, tableFlags, imgui.Vec2{}, 0) {
				imgui.TableSetupColumn("Type")
				imgui.TableSetupColumn("Name")
				imgui.TableSetupColumn("ARTCC")
				imgui.TableSetupColumn("Effective")
				imgui.TableSetupColumn("Expires")
				imgui.TableHeadersRow()

				ui.fixedFont.ImguiPush()
				for _, tfr := range c.State.TFRs {
					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.Text(tfr.Type)
					rowHovered := imgui.IsItemHovered()
					imgui.TableNextColumn()
					imgui.Text(tfr.LocalName)
					rowHovered = rowHovered || imgui.IsItemHovered()
					imgui.TableNextColumn()
					imgui.Text(tfr.ARTCC)
					rowHovered = rowHovered || imgui.IsItemHovered()
					imgui.TableNextColumn()
					imgui.Text(tfr.Effective.Format("2006-01-02 15:04Z"))
					rowHovered = rowHovered || imgui.IsItemHovered()
					imgui.TableNextColumn()
					imgui.Text(tfr.Expire.Format("2006-01-02 15:04Z"))
					rowHovered = rowHovered || imgui.IsItemHovered()

					if rowHovered {
						var lines []string
						if tfr.Regulation != "" {
							r := tfr.Regulation
							if tfr.Type != "" {
								r += " - " + tfr.Type
							}
							lines = append(lines, r)
						}
						if tfr.City != "" || tfr.State != "" {
							loc := tfr.City
							if loc != "" && tfr.State != "" {
								loc += ", "
							}
							loc += tfr.State
							lines = append(lines, loc)
						}
						if tfr.AltDescr != "" {
							lines = append(lines, tfr.AltDescr)
						}
						if tfr.Purpose != "" {
							lines = append(lines, tfr.Purpose)
						}
						if len(lines) > 0 {
							imgui.SetTooltip(strings.Join(lines, "\n"))
						}
					}
				}
				imgui.PopFont()

				imgui.EndTable()
			}
		}
	}

	if draw, ok := activeRadarPane.(panes.InfoWindowDrawer); ok {
		draw.DrawInfo(c, p, lg)
	}
	imgui.End()

	return show
}

// drawWeatherFilterUI draws the weather filter controls organized into logical groups
func (c *NewSimConfiguration) drawWeatherFilterUI() {
	const inputWidth float32 = 50
	changed := false

	// Helper to convert *int to string for display
	intPtrToStr := func(v *int) string {
		if v == nil {
			return ""
		}
		return strconv.Itoa(*v)
	}

	// Helper to parse string to *int
	parseOptionalInt := func(s string) *int {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		if v, err := strconv.Atoi(s); err == nil {
			return &v
		}
		return nil
	}

	// Helper for optional int input fields, returns true if changed
	optionalIntInput := func(label, hint string, value **int) bool {
		s := intPtrToStr(*value)
		imgui.SetNextItemWidth(inputWidth)
		if imgui.InputTextWithHint(label, hint, &s, 0, nil) {
			*value = parseOptionalInt(s)
			return true
		}
		return false
	}

	// Flight Rules (always visible, most important filter)
	imgui.Text("Flight Rules:")
	imgui.SameLine()
	flightRulesInt := int32(c.weatherFilter.FlightRules)
	if imgui.RadioButtonIntPtr("Any##fr", &flightRulesInt, int32(wx.FlightRulesAny)) {
		c.weatherFilter.FlightRules = wx.FlightRulesAny
		changed = true
	}
	imgui.SameLine()
	if imgui.RadioButtonIntPtr("VMC##fr", &flightRulesInt, int32(wx.FlightRulesVMC)) {
		c.weatherFilter.FlightRules = wx.FlightRulesVMC
		changed = true
	}
	imgui.SameLine()
	if imgui.RadioButtonIntPtr("IMC##fr", &flightRulesInt, int32(wx.FlightRulesIMC)) {
		c.weatherFilter.FlightRules = wx.FlightRulesIMC
		changed = true
	}

	// Temperature
	imgui.Text("Temperature (C):")
	imgui.SameLine()
	if optionalIntInput("##tempMin", "Min", &c.weatherFilter.TemperatureMin) {
		changed = true
	}
	imgui.SameLine()
	imgui.Text("-")
	imgui.SameLine()
	if optionalIntInput("##tempMax", "Max", &c.weatherFilter.TemperatureMax) {
		changed = true
	}

	if c.isTRACON {
		// Surface Wind group (TRACONs only)
		imgui.SeparatorText("Surface Wind")
		if imgui.BeginTableV("surfaceWind", 2, imgui.TableFlagsSizingFixedFit, imgui.Vec2{}, 0) {
			imgui.TableSetupColumnV("Label", imgui.TableColumnFlagsWidthFixed, 100, 0)
			imgui.TableSetupColumnV("Value", imgui.TableColumnFlagsWidthStretch, 0, 0)

			// Direction (most important for runway selection)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Direction (mag):")
			imgui.TableNextColumn()
			if optionalIntInput("##windDirMin", "Min", &c.weatherFilter.WindDirMin) {
				changed = true
			}
			imgui.SameLine()
			imgui.Text("-")
			imgui.SameLine()
			if optionalIntInput("##windDirMax", "Max", &c.weatherFilter.WindDirMax) {
				changed = true
			}

			// Speed
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Speed (kt):")
			imgui.TableNextColumn()
			if optionalIntInput("##windSpeedMin", "Min", &c.weatherFilter.WindSpeedMin) {
				changed = true
			}
			imgui.SameLine()
			imgui.Text("-")
			imgui.SameLine()
			if optionalIntInput("##windSpeedMax", "Max", &c.weatherFilter.WindSpeedMax) {
				changed = true
			}

			// Gusting
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("Gusting:")
			imgui.TableNextColumn()
			gustInt := int32(c.weatherFilter.Gusting)
			if imgui.RadioButtonIntPtr("Any##gust", &gustInt, int32(wx.GustAny)) {
				c.weatherFilter.Gusting = wx.GustAny
				changed = true
			}
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("Yes##gust", &gustInt, int32(wx.GustYes)) {
				c.weatherFilter.Gusting = wx.GustYes
				changed = true
			}
			imgui.SameLine()
			if imgui.RadioButtonIntPtr("No##gust", &gustInt, int32(wx.GustNo)) {
				c.weatherFilter.Gusting = wx.GustNo
				changed = true
			}

			imgui.EndTable()
		}
	}

	// Winds Aloft groups (only show if atmosByTime is available)
	if c.atmosByTime != nil {
		for i, alt := range c.windsAloftAltitudes {
			if alt == 0 {
				continue
			}
			altLabel := fmt.Sprintf("Winds Aloft (%s)", av.FormatAltitude(alt))
			imgui.SeparatorText(altLabel)

			idSuffix := fmt.Sprintf("%d", i)
			if imgui.BeginTableV("windsAloft"+idSuffix, 2, imgui.TableFlagsSizingFixedFit, imgui.Vec2{}, 0) {
				imgui.TableSetupColumnV("Label", imgui.TableColumnFlagsWidthFixed, 100, 0)
				imgui.TableSetupColumnV("Value", imgui.TableColumnFlagsWidthStretch, 0, 0)

				// Direction
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Direction (mag):")
				imgui.TableNextColumn()
				aloftDirChanged := optionalIntInput("##aloftDirMin"+idSuffix, "Min", &c.weatherFilter.WindsAloft[i].DirMin)
				imgui.SameLine()
				imgui.Text("-")
				imgui.SameLine()
				aloftDirChanged = optionalIntInput("##aloftDirMax"+idSuffix, "Max", &c.weatherFilter.WindsAloft[i].DirMax) || aloftDirChanged
				// Only trigger update when both values are set or both are empty
				if aloftDirChanged {
					bothSet := c.weatherFilter.WindsAloft[i].DirMin != nil && c.weatherFilter.WindsAloft[i].DirMax != nil
					bothEmpty := c.weatherFilter.WindsAloft[i].DirMin == nil && c.weatherFilter.WindsAloft[i].DirMax == nil
					if bothSet || bothEmpty {
						changed = true
					}
				}

				// Speed
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.Text("Speed (kt):")
				imgui.TableNextColumn()
				if optionalIntInput("##aloftSpeedMin"+idSuffix, "Min", &c.weatherFilter.WindsAloft[i].SpeedMin) {
					changed = true
				}
				imgui.SameLine()
				imgui.Text("-")
				imgui.SameLine()
				if optionalIntInput("##aloftSpeedMax"+idSuffix, "Max", &c.weatherFilter.WindsAloft[i].SpeedMax) {
					changed = true
				}

				imgui.EndTable()
			}
		}
	}

	// Filter error (if any)
	if c.weatherFilterError != "" {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(renderer.FontAwesomeIconExclamationTriangle + " " + c.weatherFilterError)
		imgui.PopStyleColor()
	}

	imgui.Separator()

	// Start time and METAR section
	// The airports the scenario is actually worked at come first; the rest are
	// context.
	scenarioAirports := c.ScenarioSpec.AllAirports()
	metarAirports := util.SortedMapKeys(c.airportMETAR)
	slices.SortStableFunc(metarAirports, func(a, b string) int {
		return util.Select(slices.Contains(scenarioAirports, b), 1, 0) -
			util.Select(slices.Contains(scenarioAirports, a), 1, 0)
	})

	if imgui.BeginTableV("timeAndMetar", 2, imgui.TableFlagsSizingFixedFit, imgui.Vec2{}, 0) {
		imgui.TableSetupColumnV("Label", imgui.TableColumnFlagsWidthFixed, 70, 0)
		imgui.TableSetupColumnV("Value", imgui.TableColumnFlagsWidthStretch, 0, 0)

		// Start time
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("Start time:")
		imgui.TableNextColumn()
		metar := c.airportMETAR[metarAirports[0]]
		clock := makeScenarioClock(c.ScenarioSpec)
		TimePicker(&c.NewSimRequest.StartTime, clock, c.startTimeIntervals(c.ScenarioSpec), metar, ui.fixedFont)
		imgui.SameLine()
		if imgui.Button(renderer.FontAwesomeIconRedo + "##refreshTime") {
			c.updateStartTimeForRunways(c.ScenarioSpec)
		}
		imgui.SameLine()
		TimeSlider(&c.NewSimRequest.StartTime, clock, timeSliderWidth)

		// METAR
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("METAR:")
		imgui.TableNextColumn()
		currentMetar := wx.METARForTime(c.airportMETAR[metarAirports[0]], c.NewSimRequest.StartTime)
		ui.fixedFont.ImguiPush()
		imgui.Text(c.metarText(currentMetar))
		imgui.PopFont()

		if c.showAllMETAR && len(metarAirports) > 1 {
			for i := 1; i < len(metarAirports); i++ {
				ap := metarAirports[i]
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.TableNextColumn()
				ui.fixedFont.ImguiPush()
				m := wx.METARForTime(c.airportMETAR[ap], c.NewSimRequest.StartTime)
				imgui.Text(c.metarText(m))
				imgui.PopFont()
			}
		}

		imgui.EndTable()
	}

	if len(metarAirports) > 1 && !c.showAllMETAR {
		if imgui.Button("Show all airport METAR") {
			c.showAllMETAR = true
		}
	}

	if changed {
		c.updateStartTimeForRunways(c.ScenarioSpec)
	}
}

// startTimeIntervals returns the times a sim may start at. When the scenario is
// flying historical traffic the weather intervals are narrowed to the stretches
// the flight data covers, each less a final day so that a sim started near the
// end of one still has traffic to fly. A stretch with less than that in it is
// no use to anyone and drops out.
func (c *NewSimConfiguration) startTimeIntervals(spec *server.ScenarioSpec) []util.TimeInterval {
	if spec == nil || spec.LaunchConfig.TrafficSource != sim.TrafficSourceHistorical {
		return c.availableWXIntervals
	}

	// The times come back from the server in the local zone, so they are put
	// back in UTC before being compared with anything.
	flights := util.MapSlice(spec.HistoricalFlightIntervals, func(iv util.TimeInterval) util.TimeInterval {
		return util.TimeInterval{iv[0].UTC(), iv[1].UTC().Add(-24 * time.Hour)}
	})
	flights = util.FilterSliceInPlace(flights, func(iv util.TimeInterval) bool {
		return iv[0].Before(iv[1])
	})

	wx := util.MapSlice(c.availableWXIntervals, func(iv util.TimeInterval) util.TimeInterval {
		return util.TimeInterval{iv[0].UTC(), iv[1].UTC()}
	})
	return util.IntersectIntervals(wx, flights)
}

// Default sim start times are picked between these local hours at the primary
// airport, so that nobody inadvertently runs a sim in the middle of the night
// and wonders where the traffic is.
const (
	defaultStartLocalHourMin = 7  // 7am
	defaultStartLocalHourMax = 19 // 7pm
)

func (c *NewSimConfiguration) updateStartTimeForRunways(spec *server.ScenarioSpec) {
	c.weatherFilterError = ""

	if spec == nil || c.airportMETAR == nil {
		return
	}

	airports := spec.AllAirports()
	if len(airports) == 0 {
		return
	}
	if apMETAR, ok := c.airportMETAR[airports[0]]; ok && len(apMETAR) > 0 {
		intervals := c.startTimeIntervals(spec)
		clock := makeScenarioClock(spec)

		// Sample using the combined weather filter (ground winds + winds
		// aloft), retrying for a daytime start where the scenario is flown: a
		// sim inadvertently started in the middle of the night has hardly any
		// traffic to work. If the weather filter only matches nighttime, the
		// weather wins and the last sample is kept.
		var sampledMETAR *wx.METAR
		var startTime time.Time
		for range 25 {
			sampledMETAR = wx.SampleWeatherWithFilter(
				apMETAR,
				c.atmosByTime,
				intervals,
				&c.weatherFilter,
				c.windsAloftAltitudes,
				spec.MagneticVariation)
			if sampledMETAR == nil {
				break
			}

			// Start at a random time between the sampled METAR and the next one
			startTime = sampledMETAR.Time.UTC()
			idx, _ := slices.BinarySearchFunc(apMETAR, sampledMETAR.Time, func(m wx.METAR, t time.Time) int {
				return m.Time.Compare(t)
			})
			if idx+1 < len(apMETAR) {
				validDuration := apMETAR[idx+1].Time.Sub(sampledMETAR.Time)
				startTime = startTime.Add(rand.Make().DurationRange(0, validDuration))
			}

			if !clock.local {
				break
			}
			if hour := startTime.In(clock.loc).Hour(); hour >= defaultStartLocalHourMin &&
				hour < defaultStartLocalHourMax {
				break
			}
		}

		if sampledMETAR != nil {
			c.StartTime = startTime

			// Set VFR launch rate to zero if selected weather is IMC;
			// restore the original value if VMC.
			if !sampledMETAR.IsVMC() {
				spec.LaunchConfig.VFRDepartureRateScale = 0
			} else {
				spec.LaunchConfig.VFRDepartureRateScale = c.savedVFRDepartureRateScale
			}
		} else {
			c.weatherFilterError = "No weather matching filters found"
		}
	}
}
