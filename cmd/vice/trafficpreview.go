// cmd/vice/trafficpreview.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

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
func (c *NewSimConfiguration) drawTrafficPlot(spec *scenario.Spec, p platform.Platform) {
	c.updateTrafficPreview(spec)

	if c.trafficPreviewError != nil {
		imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, .5, .5, 1})
		imgui.Text(gui.Icons.ExclamationTriangle + " " + c.trafficPreviewError.Error())
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
func (c *NewSimConfiguration) drawTrafficPlotAxes(drawList *imgui.DrawList, spec *scenario.Spec,
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

func (c *NewSimConfiguration) trafficPlotTime(spec *scenario.Spec, minute int) string {
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
func (c *NewSimConfiguration) updateTrafficPreview(spec *scenario.Spec) {
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
		minutes, err := sim.TimetableStartMinute(c.NewSimRequest.StartTime, spec.LaunchConfig.TimetableAirport)
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
	} else if result.AirportOperations != nil {
		c.trafficPreviewOperations = result.AirportOperations
	} else {
		// gob drops empty maps, and nil is reserved for "no answer yet".
		c.trafficPreviewOperations = make(map[av.ICAOAirportCode]int)
	}
}

// trafficPreviewKey gathers everything the server's answer depends on, so that
// the preview is asked for again exactly when one of them changes. Start times
// are keyed to the minute since that is as fine as the counts go.
func trafficPreviewKey(req *server.NewSimRequest, spec *scenario.Spec) string {
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
	c.trafficPreviewOperations = nil
	c.trafficPreviewError = nil
	c.trafficPreviewRetryAt = time.Time{}
}
