// cmd/vice/simweather.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/gui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/scenario"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/AllenDang/cimgui-go/imgui"
)

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
	addRunway := func(airport av.ICAOAirportCode, id av.RunwayID) {
		dbap, ok := db.DB.Airports[airport]
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
func (c *NewSimConfiguration) fetchMETAR(seq uint64, facility string, airports []av.ICAOAirportCode, spec *scenario.Spec) {
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
	var metars map[av.ICAOAirportCode][]wx.METAR
	if metarErr == nil {
		metars = make(map[av.ICAOAirportCode][]wx.METAR)
		for ap, soa := range metarSOA {
			metars[ap] = soa.Decode(string(ap))
		}
	}
	// TRACON: single altitude at 5,000' (representative of terminal area
	// traffic). Center/ARTCC: FL240 and FL380 (lower and upper flight levels).
	isTRACON := db.DB.IsTRACON(facility)
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
		c.availableWXIntervals = metarIntervals
	} else {
		c.availableWXIntervals = util.IntersectIntervals(metarIntervals, facilityIntervals)
	}
}

const (
	NewSimCreateLocal = iota
	NewSimCreateRemote
	NewSimJoinRemote
)

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
		imgui.Text(gui.Icons.ExclamationTriangle + " " + c.weatherFilterError)
		imgui.PopStyleColor()
	}

	imgui.Separator()

	// Start time and METAR section
	metarAirports, haveOrder := c.metarAirportsByTraffic(c.ScenarioSpec)

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
		TimePicker(&c.NewSimRequest.StartTime, clock, c.validStartDays(c.ScenarioSpec), metar, ui.fixedFont)
		imgui.SameLine()
		if imgui.Button(gui.Icons.Redo + "##refreshTime") {
			c.updateStartTimeForRunways(c.ScenarioSpec)
		}
		imgui.SameLine()
		TimeSlider(&c.NewSimRequest.StartTime, clock, timeSliderWidth)

		// METAR, held back until the airport to lead with is known.
		if haveOrder {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Text("METAR:")
			imgui.TableNextColumn()
			currentMetar := wx.METARForTime(c.airportMETAR[metarAirports[0]], c.NewSimRequest.StartTime)
			gui.PushFont(ui.fixedFont)
			imgui.Text(c.metarText(currentMetar))
			gui.PopFont()

			if c.showAllMETAR && len(metarAirports) > 1 {
				for i := 1; i < len(metarAirports); i++ {
					ap := metarAirports[i]
					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.TableNextColumn()
					gui.PushFont(ui.fixedFont)
					m := wx.METARForTime(c.airportMETAR[ap], c.NewSimRequest.StartTime)
					imgui.Text(c.metarText(m))
					gui.PopFont()
				}
			}
		}

		imgui.EndTable()
	}

	if haveOrder && len(metarAirports) > 1 && !c.showAllMETAR {
		if imgui.Button("Show all airport METAR") {
			c.showAllMETAR = true
		}
	}

	if changed {
		c.updateStartTimeForRunways(c.ScenarioSpec)
	}
}

// metarAirportsByTraffic returns the airports METAR was fetched for, busiest
// first: the airport a controller spends the session watching is the one whose
// weather they care about. The selected traffic source decides what "busiest"
// means--the launch config's rates for scenario traffic, the timetable's
// airport for timetable traffic, the server's flight counts for historical
// traffic. Historical counts arrive by RPC, so until the first answer lands
// the order isn't known: the airports come back alphabetical, as before, and
// ok is false. Called with c.mu held.
func (c *NewSimConfiguration) metarAirportsByTraffic(spec *scenario.Spec) (airports []av.ICAOAirportCode, ok bool) {
	airports = util.SortedMapKeys(c.airportMETAR)

	var score func(ap av.ICAOAirportCode) float32
	switch spec.LaunchConfig.TrafficSource {
	case sim.TrafficSourceTimetable:
		score = func(ap av.ICAOAirportCode) float32 {
			return float32(util.Select(ap == spec.LaunchConfig.TimetableAirport, 1, 0))
		}
	case sim.TrafficSourceHistorical:
		if c.trafficPreviewOperations == nil {
			return airports, false
		}
		score = func(ap av.ICAOAirportCode) float32 { return float32(c.trafficPreviewOperations[ap]) }
	default:
		rates := spec.LaunchConfig.WorkedAirportRates()
		score = func(ap av.ICAOAirportCode) float32 { return rates[ap] }
	}

	// A stable sort over the already-alphabetical keys keeps ties alphabetical.
	slices.SortStableFunc(airports, func(a, b av.ICAOAirportCode) int {
		return cmp.Compare(score(b), score(a))
	})
	return airports, true
}

// Default sim start times are picked between these local hours at the primary
// airport, so that nobody inadvertently runs a sim in the middle of the night
// and wonders where the traffic is.
const (
	defaultStartLocalHourMin = 7  // 7am
	defaultStartLocalHourMax = 19 // 7pm
)

func (c *NewSimConfiguration) updateStartTimeForRunways(spec *scenario.Spec) {
	c.weatherFilterError = ""

	if spec == nil || c.airportMETAR == nil {
		return
	}

	airports, _ := c.metarAirportsByTraffic(spec)
	if len(airports) == 0 {
		return
	}
	apMETAR := c.airportMETAR[airports[0]]
	if len(apMETAR) == 0 {
		return
	}
	days := c.validStartDays(spec)
	if len(days) == 0 {
		return
	}
	clock := makeScenarioClock(spec)

	// Sample a METAR matching the combined weather filter (ground winds +
	// winds aloft), preferring the daytime hours of the valid days: a sim
	// inadvertently started in the middle of the night has hardly any traffic
	// to work. Each valid day is covered midnight to midnight, so its daytime
	// slice is covered too. If the weather filter only matches at night, the
	// weather wins and the whole days are sampled instead.
	sample := func(windows []util.TimeInterval) *wx.METAR {
		return wx.SampleWeatherWithFilter(apMETAR, c.atmosByTime, windows, &c.weatherFilter,
			c.windsAloftAltitudes, spec.MagneticVariation)
	}
	var m *wx.METAR
	var windows []util.TimeInterval
	if clock.local {
		windows = dayWindows(days, clock, defaultStartLocalHourMin, defaultStartLocalHourMax)
		m = sample(windows)
	}
	if m == nil {
		windows = dayWindows(days, clock, 0, 24)
		m = sample(windows)
	}
	if m == nil {
		c.weatherFilterError = "No weather matching filters found"
		return
	}

	// Start at a random time between the sampled METAR and the next one,
	// staying inside the window the METAR was drawn from.
	startTime := m.Time.UTC()
	end := m.Time
	if i := slices.IndexFunc(windows, func(iv util.TimeInterval) bool { return iv.Contains(m.Time) }); i != -1 {
		end = windows[i].End()
	}
	idx, _ := slices.BinarySearchFunc(apMETAR, m.Time, func(m wx.METAR, t time.Time) int {
		return m.Time.Compare(t)
	})
	if idx+1 < len(apMETAR) && apMETAR[idx+1].Time.Before(end) {
		end = apMETAR[idx+1].Time
	}
	startTime = startTime.Add(rand.Make().DurationRange(0, end.Sub(m.Time)))

	c.StartTime = startTime

	// Set VFR launch rate to zero if selected weather is IMC;
	// restore the original value if VMC.
	if !m.IsVMC() {
		spec.LaunchConfig.VFRDepartureRateScale = 0
	} else {
		spec.LaunchConfig.VFRDepartureRateScale = c.savedVFRDepartureRateScale
	}
}
