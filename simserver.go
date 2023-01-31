// simserver.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

var (
	ErrArrivalAirportUnknown        = errors.New("Arrival airport unknown")
	ErrUnknownApproach              = errors.New("Unknown approach")
	ErrNotOnApproachCourse          = errors.New("Not on approach course")
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
)

type Simulator interface {
	AssignAltitude(callsign string, altitude int) error
	AssignHeading(callsign string, heading int) error
	AssignSpeed(callsign string, kts int) error
	DirectFix(callsign string, fix string) error
	ExpectApproach(callsign string, approach string) error
	ClearedApproach(callsign string, approach string) error

	PrintInfo(callsign string) error
	DeleteAircraft(callsign string) error
	TogglePause() error
}

func mustParseLatLong(l string) Point2LL {
	ll, err := ParseLatLong(l)
	if err != nil {
		panic(l + ": " + err.Error())
	}
	return ll
}

var configPositions map[string]Point2LL = map[string]Point2LL{
	"_JFK_31L": mustParseLatLong("N040.37.41.000, W073.46.20.227"),
	"_JFK_31R": mustParseLatLong("N040.38.35.986, W073.45.31.503"),
	"_JFK_22R": mustParseLatLong("N040.39.00.362, W073.45.49.053"),
	"_JFK_22L": mustParseLatLong("N040.38.41.232, W073.45.18.511"),
	"_JFK_4L":  mustParseLatLong("N040.37.19.370, W073.47.08.045"),
	"_JFK_4La": mustParseLatLong("N040.39.21.332, W073.45.32.849"),
	"_JFK_4R":  mustParseLatLong("N040.37.31.661, W073.46.12.894"),
	"_JFK_13R": mustParseLatLong("N040.38.53.537, W073.49.00.188"),
	"_JFK_13L": mustParseLatLong("N040.39.26.976, W073.47.24.277"),

	"_LGA_13":  mustParseLatLong("N040.46.56.029, W073.52.42.359"),
	"_LGA_13a": mustParseLatLong("N040.48.06.479, W073.55.40.914"),
	"_LGA_31":  mustParseLatLong("N040.46.19.788, W073.51.25.949"),
	"_LGA_31a": mustParseLatLong("N040.45.34.950, W073.49.52.922"),
	"_LGA_31b": mustParseLatLong("N040.48.50.809, W073.46.42.200"),
	"_LGA_22":  mustParseLatLong("N040.47.06.864, W073.52.14.811"),
	"_LGA_22a": mustParseLatLong("N040.51.18.890, W073.49.30.483"),
	"_LGA_4":   mustParseLatLong("N040.46.09.447, W073.53.02.574"),
	"_LGA_4a":  mustParseLatLong("N040.44.56.662, W073.51.53.497"),
	"_LGA_4b":  mustParseLatLong("N040.47.59.557, W073.47.11.533"),

	"_FRG_1":   mustParseLatLong("N040.43.20.230, W073.24.51.229"),
	"_FRG_1a":  mustParseLatLong("N040.46.52.637, W073.24.58.809"),
	"_FRG_19":  mustParseLatLong("N040.44.10.396, W073.24.50.982"),
	"_FRG_19a": mustParseLatLong("N040.41.03.313, W073.26.45.267"),
	"_FRG_14":  mustParseLatLong("N040.44.02.898, W073.25.17.486"),
	"_FRG_14a": mustParseLatLong("N040.38.37.868, W073.22.41.398"),
	"_FRG_32":  mustParseLatLong("N040.43.20.436, W073.24.13.848"),
	"_FRG_32a": mustParseLatLong("N040.45.28.921, W073.27.08.421"),

	"_ISP_6":    mustParseLatLong("N040.47.18.743, W073.06.44.022"),
	"_ISP_6a":   mustParseLatLong("N040.50.43.281, W073.02.11.698"),
	"_ISP_6b":   mustParseLatLong("N040.50.28.573, W073.09.10.827"),
	"_ISP_24":   mustParseLatLong("N040.48.06.643, W073.05.39.202"),
	"_ISP_24a":  mustParseLatLong("N040.45.56.414, W073.08.58.879"),
	"_ISP_24b":  mustParseLatLong("N040.47.41.032, W073.06.08.371"),
	"_ISP_24c":  mustParseLatLong("N040.48.48.350, W073.07.30.466"),
	"_ISP_15R":  mustParseLatLong("N040.48.05.462, W073.06.24.356"),
	"_ISP_15Ra": mustParseLatLong("N040.45.33.934, W073.02.36.555"),
	"_ISP_15Rb": mustParseLatLong("N040.49.18.755, W073.03.43.379"),
	"_ISP_15Rc": mustParseLatLong("N040.48.34.288, W073.09.11.211"),
	"_ISP_33L":  mustParseLatLong("N040.47.32.819, W073.05.41.702"),
	"_ISP_33La": mustParseLatLong("N040.49.52.085, W073.08.43.141"),
	"_ISP_33Lb": mustParseLatLong("N040.49.21.515, W073.06.31.250"),
	"_ISP_33Lc": mustParseLatLong("N040.48.20.019, W073.10.31.686"),
}

type AirportConfig struct {
	name             string
	departureConfigs []*DepartureConfig
	arrivalConfigs   []*ArrivalConfig
	approaches       []Approach
}

func (ac *AirportConfig) AddApproach(ap Approach) error {
	for i := range ap.Waypoints {
		n := len(ap.Waypoints[i])
		ap.Waypoints[i][n-1].Commands = append(ap.Waypoints[i][n-1].Commands, WaypointCommandDelete)

		for j, wp := range ap.Waypoints[i] {
			if pos, ok := database.Locate(wp.Fix); ok {
				ap.Waypoints[i][j].Location = pos
			} else if pos, ok := configPositions[wp.Fix]; ok {
				ap.Waypoints[i][j].Location = pos
			} else if pos, err := ParseLatLong(wp.Fix); err == nil {
				ap.Waypoints[i][j].Location = pos
			} else {
				return fmt.Errorf("%s: unable to locate waypoint", wp.Fix)
			}
		}
	}

	ac.approaches = append(ac.approaches, ap)
	return nil
}

type DepartureConfig struct {
	name            string
	rate            int32
	challenge       float32
	enabled         bool
	categoryEnabled map[string]*bool
	makeSpawner     func(*DepartureConfig) *AircraftSpawner
}

type ArrivalConfig struct {
	name        string
	rate        int32
	enabled     bool
	makeSpawner func(*ArrivalConfig) *AircraftSpawner
}

type ApproachType int

const (
	ILSApproach = iota
	RNAVApproach
)

type Approach struct {
	ShortName string
	FullName  string
	Type      ApproachType
	Waypoints [][]Waypoint // RNAV may e.g. have multiple (partially overlapping) options...
}

func (ap *Approach) Line() [2]Point2LL {
	// assume we have at least one set of waypoints and that it has >= 2 waypoints!
	wp := ap.Waypoints[0]

	// use the last two waypoints
	n := len(wp)
	return [2]Point2LL{wp[n-2].Location, wp[n-1].Location}
}

func (ap *Approach) Heading() int {
	p := ap.Line()
	return int(headingp2ll(p[0], p[1], database.MagneticVariation) + 0.5)
}

type WaypointCommand int

const (
	WaypointCommandHandoff = iota
	WaypointCommandDelete
)

type Waypoint struct {
	Fix      string
	Location Point2LL
	Altitude int
	Speed    int
	Heading  int // outbound heading after waypoint
	// Commands run when we reach the waypoint(ish)
	Commands []WaypointCommand
}

type SimServerConnectionConfiguration struct {
	callsign    string
	numAircraft int32

	wind struct {
		dir   int32
		speed int32
		gust  int32
	}

	airportConfigs []*AirportConfig
}

func (ssc *SimServerConnectionConfiguration) Initialize() {
	ssc.callsign = "JFK_DEP"
	ssc.numAircraft = 30
	ssc.wind.dir = 50
	ssc.wind.speed = 10
	ssc.wind.gust = 15

	ssc.airportConfigs = append(ssc.airportConfigs, GetJFKConfig())
	ssc.airportConfigs = append(ssc.airportConfigs, GetFRGConfig())
	ssc.airportConfigs = append(ssc.airportConfigs, GetISPConfig())
	ssc.airportConfigs = append(ssc.airportConfigs, GetLGAConfig())
}

func (ssc *SimServerConnectionConfiguration) DrawUI() bool {
	// imgui.InputText("Callsign", &ssc.callsign)
	imgui.SliderIntV("Total aircraft", &ssc.numAircraft, 1, 100, "%d", 0)

	imgui.SliderIntV("Wind heading", &ssc.wind.dir, 0, 360, "%d", 0)
	imgui.SliderIntV("Wind speed", &ssc.wind.speed, 0, 50, "%d", 0)
	imgui.SliderIntV("Wind gust", &ssc.wind.gust, 0, 50, "%d", 0)
	ssc.wind.gust = max(ssc.wind.gust, ssc.wind.speed)

	for i, apConfig := range ssc.airportConfigs {
		var headerFlags imgui.TreeNodeFlags
		if i == 0 {
			headerFlags = imgui.TreeNodeFlagsDefaultOpen
		}
		if !imgui.CollapsingHeaderV(apConfig.name, headerFlags) {
			continue
		}

		drawDepartureUI(apConfig.departureConfigs)
		drawArrivalUI(apConfig.arrivalConfigs)
	}

	return false
}

func drawDepartureUI(configs []*DepartureConfig) {
	if len(configs) == 0 {
		return
	}

	anyRunwaysActive := false
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	if imgui.BeginTableV("runways", 4, flags, imgui.Vec2{800, 0}, 0.) {
		imgui.TableSetupColumn("Enabled")
		imgui.TableSetupColumn("Runway")
		imgui.TableSetupColumn("ADR")
		imgui.TableSetupColumn("Challenge level")
		imgui.TableHeadersRow()

		for i := range configs {
			imgui.PushID(configs[i].name)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			if imgui.Checkbox("##enabled", &configs[i].enabled) {
				if configs[i].enabled {
					// enable all corresponding categories by default
					for _, enabled := range configs[i].categoryEnabled {
						*enabled = true
					}
				} else {
					// disable all corresponding configs
					for _, enabled := range configs[i].categoryEnabled {
						*enabled = false
					}
				}
			}
			anyRunwaysActive = anyRunwaysActive || configs[i].enabled
			imgui.TableNextColumn()
			imgui.Text(configs[i].name)
			imgui.TableNextColumn()
			imgui.InputIntV("##adr", &configs[i].rate, 1, 120, 0)
			imgui.TableNextColumn()
			imgui.SliderFloatV("##challenge", &configs[i].challenge, 0, 1, "%.01f", 0)
			imgui.PopID()
		}
		imgui.EndTable()
	}

	if anyRunwaysActive {
		imgui.Separator()
		flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
		if imgui.BeginTableV("configs", 2, flags, imgui.Vec2{800, 0}, 0.) {
			imgui.TableSetupColumn("Enabled")
			imgui.TableSetupColumn("Runway/Gate")
			imgui.TableHeadersRow()
			for i := range configs {
				if !configs[i].enabled {
					continue
				}

				imgui.PushID(configs[i].name)
				for _, category := range SortedMapKeys(configs[i].categoryEnabled) {
					imgui.PushID(category)
					imgui.TableNextRow()
					imgui.TableNextColumn()
					imgui.Checkbox("##check", configs[i].categoryEnabled[category])
					imgui.TableNextColumn()
					imgui.Text(configs[i].name + "/" + category)
					imgui.PopID()
				}
				imgui.PopID()
			}
			imgui.EndTable()
		}
	}
}

func drawArrivalUI(configs []*ArrivalConfig) {
	if len(configs) == 0 {
		return
	}

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	if imgui.BeginTableV("arrivals", 3, flags, imgui.Vec2{600, 0}, 0.) {
		imgui.TableSetupColumn("Enabled")
		imgui.TableSetupColumn("Arrival")
		imgui.TableSetupColumn("AAR")
		imgui.TableHeadersRow()

		for i := range configs {
			imgui.PushID(configs[i].name)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Checkbox("##enabled", &configs[i].enabled)
			imgui.TableNextColumn()
			imgui.Text(configs[i].name)
			imgui.TableNextColumn()
			imgui.InputIntV("##aar", &configs[i].rate, 1, 120, 0)
			imgui.PopID()
		}
		imgui.EndTable()
	}
}

func (ssc *SimServerConnectionConfiguration) Valid() bool {
	// Make sure that at least one scenario is selected
	for _, apConfig := range ssc.airportConfigs {
		for _, d := range apConfig.departureConfigs {
			for _, enabled := range d.categoryEnabled {
				if *enabled {
					return true
				}
			}
		}
		for _, a := range apConfig.arrivalConfigs {
			if a.enabled {
				return true
			}
		}
	}

	return false
}

func (ssc *SimServerConnectionConfiguration) Connect() error {
	// Send out events to remove any existing aircraft (necessary for when
	// we restart...)
	for _, ac := range server.GetAllAircraft() {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	server = NewSimServer(*ssc)
	return nil
}

///////////////////////////////////////////////////////////////////////////
// SSAircraft

type SSAircraft struct {
	AC          *Aircraft
	Performance AircraftPerformance
	Strip       FlightStrip
	Waypoints   []Waypoint

	Position Point2LL
	Heading  float32
	Altitude float32
	IAS, GS  float32 // speeds...

	// The following are for controller-assigned altitudes, speeds, and
	// headings.  Values of 0 indicate no assignment.
	AssignedAltitude int
	AssignedSpeed    int
	AssignedHeading  int
	TurnDirection    int

	// These are for altitudes/speeds to meet at the next fix; unlike
	// controller-assigned ones, where we try to get there as quickly as
	// the aircraft is capable of, these we try to get to exactly at the
	// fix.
	CrossingAltitude int
	CrossingSpeed    int

	Approach        *Approach // if assigned
	ClearedApproach bool
	OnFinal         bool
}

func (ac *SSAircraft) TAS() float32 {
	// Simple model for the increase in TAS as a function of altitude: 2%
	// additional TAS on top of IAS for each 1000 feet.
	return ac.IAS * (1 + .02*ac.Altitude/1000)
}

func (ac *SSAircraft) UpdateAirspeed() {
	// Figure out what speed we're supposed to be going. The following is
	// prioritized, so once targetSpeed has been set, nothing should
	// override it.  cruising speed.
	perf := ac.Performance
	var targetSpeed int

	// Slow down on final approach
	if ac.OnFinal {
		if airportPos, ok := database.Locate(ac.AC.FlightPlan.ArrivalAirport); ok {
			airportDist := nmdistance2ll(ac.Position, airportPos)
			if airportDist < 1 {
				targetSpeed = perf.Speed.Landing
			} else if airportDist < 5 || (airportDist < 10 && ac.AssignedSpeed == 0) {
				// Ignore speed restrictions if the aircraft is within 5
				// miles; otherwise start slowing down if it hasn't't been
				// given a speed restriction.

				// Expected speed at 10 DME, without further direction.
				approachSpeed := min(210, perf.Speed.Cruise)
				landingSpeed := perf.Speed.Landing
				targetSpeed = int(lerp((airportDist-1)/9, float32(landingSpeed), float32(approachSpeed)))
			}

			// However, don't accelerate if the aircraft is already under
			// the target speed.
			targetSpeed = min(targetSpeed, int(ac.IAS))
			//lg.Errorf("airport dist %f -> target speed %d", airportDist, targetSpeed)
		}
	}

	if targetSpeed == 0 && ac.AssignedSpeed != 0 {
		// Use the controller-assigned speed, but only as far as the
		// aircraft's capabilities.
		targetSpeed = clamp(ac.AssignedSpeed, perf.Speed.Min, perf.Speed.Max)
	}

	if targetSpeed == 0 && ac.CrossingSpeed != 0 {
		dist, ok := ac.nextFixDistance()
		if !ok {
			lg.Errorf("unable to get crossing fix distance... %+v", ac)
			return
		}

		eta := dist / float32(ac.AC.Groundspeed()) * 3600 // in seconds
		cs := float32(ac.CrossingSpeed)
		if ac.IAS < cs {
			accel := (cs - ac.IAS) / eta * 1.25
			accel = min(accel, ac.Performance.Rate.Accelerate/2)
			ac.IAS = min(float32(targetSpeed), ac.IAS+accel)
		} else if ac.IAS > cs {
			decel := (ac.IAS - cs) / eta * 0.75
			decel = min(decel, ac.Performance.Rate.Decelerate/2)
			ac.IAS = max(float32(targetSpeed), ac.IAS-decel)
			//lg.Errorf("dist %f eta %f ias %f crossing %f decel %f", dist, eta, ac.IAS, cs, decel)
		}

		return
	}

	if targetSpeed == 0 {
		targetSpeed = perf.Speed.Cruise

		// But obey 250kts under 10,000'
		if ac.Altitude < 10000 {
			targetSpeed = min(targetSpeed, 250)
		}
	}

	// Finally, adjust IAS subject to the capabilities of the aircraft.
	if ac.IAS+1 < float32(targetSpeed) {
		accel := ac.Performance.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
		ac.IAS = min(float32(targetSpeed), ac.IAS+accel)
	} else if ac.IAS-1 > float32(targetSpeed) {
		decel := ac.Performance.Rate.Decelerate / 2 // Decel is given in "per 2 seconds..."
		ac.IAS = max(float32(targetSpeed), ac.IAS-decel)
	}
}

func (ac *SSAircraft) UpdateAltitude() {
	// Climb or descend, but only if it's going fast enough to be
	// airborne.  (Assume no stalls in flight.)
	airborne := ac.IAS >= 1.1*float32(ac.Performance.Speed.Min)
	if !airborne {
		return
	}

	if ac.AssignedAltitude == 0 && ac.CrossingAltitude == 0 {
		// No altitude assignment, so... just stay where we are
		return
	}

	// Baseline climb and descent capabilities in ft/minute
	climb, descent := float32(ac.Performance.Rate.Climb), float32(ac.Performance.Rate.Descent)

	// For high performing aircraft, reduce climb rate after 5,000'
	if climb >= 2500 && ac.Altitude > 5000 {
		climb -= 500
	}

	if ac.AssignedAltitude != 0 {
		// Controller-assigned altitude takes precedence over a crossing
		// altitude.

		if ac.Altitude < float32(ac.AssignedAltitude) {
			// Simple model: we just update altitude based on the rated climb
			// rate; does not account for simultaneous acceleration, etc...
			ac.Altitude = min(float32(ac.AssignedAltitude), ac.Altitude+climb/60)
		} else if ac.Altitude > float32(ac.AssignedAltitude) {
			// Similarly, descent modeling doesn't account for airspeed or
			// acceleration/deceleration...
			ac.Altitude = max(float32(ac.AssignedAltitude), ac.Altitude-descent/60)
		}
	} else {
		// We have a crossing altitude.  Estimated time to get there in minutes.
		dist, ok := ac.nextFixDistance()
		if !ok {
			lg.Errorf("unable to get crossing fix distance... %+v", ac)
			return
		}

		eta := dist / float32(ac.AC.Groundspeed()) * 60 // in minutes
		if ac.CrossingAltitude > int(ac.Altitude) {
			// Need to climb.  Figure out rate of climb that would get us
			// there when we reach the fix (ft/min).
			rate := (float32(ac.CrossingAltitude) - ac.Altitude) / eta

			// But we can't climb faster than the aircraft is capable of.
			ac.Altitude += min(rate, climb) / 60
		} else {
			// Need to descend; same logic as the climb case.
			rate := (ac.Altitude - float32(ac.CrossingAltitude)) / eta
			ac.Altitude -= min(rate, descent) / 60
			//lg.Errorf("dist %f eta %f alt %f crossing %d eta %f -> rate %f ft/min -> delta %f",
			//dist, eta, ac.Altitude, ac.CrossingAltitude, eta, rate, min(rate, descent)/60)
		}
	}
}

// Returns the distance from the aircraft to the next fix in its waypoints,
// in nautical miles.
func (ac *SSAircraft) nextFixDistance() (float32, bool) {
	if len(ac.Waypoints) == 0 {
		return 0, false
	}

	return nmdistance2ll(ac.Position, ac.Waypoints[0].Location), true
}

func (ac *SSAircraft) UpdateHeading() {
	// Figure out the heading; if the route is empty, just leave it
	// on its current heading...
	targetHeading := ac.Heading
	turn := float32(0)

	// Are we intercepting a localizer? Possibly turn to join it.
	if ap := ac.Approach; ap != nil &&
		ac.ClearedApproach &&
		ap.Type == ILSApproach &&
		ac.AssignedHeading != 0 &&
		ac.AssignedHeading != ap.Heading() &&
		headingDifference(float32(ap.Heading()), ac.Heading) < 40 /* allow quite some slop... */ {
		// Estimate time to intercept.  Do this using nm coordinates
		loc := ap.Line()
		loc[0], loc[1] = ll2nm(loc[0]), ll2nm(loc[1])

		pos := ll2nm(ac.Position)
		hdg := ac.Heading - database.MagneticVariation
		headingVector := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
		pos1 := add2f(pos, headingVector)

		// Intersection of aircraft's path with the localizer
		isect, ok := LineLineIntersect(loc[0], loc[1], pos, pos1)
		if !ok {
			lg.Errorf("no intersect!")
			return // better luck next time...
		}

		// Is the intersection behind the aircraft? (This can happen if it
		// has flown through the localizer.) Ignore it if so.
		v := sub2f(isect, pos)
		if v[0]*headingVector[0]+v[1]*headingVector[1] < 0 {
			lg.Errorf("behind us...")
		} else {
			// Find eta to the intercept and the turn required to align with
			// the localizer.
			dist := distance2f(pos, isect)
			eta := dist / float32(ac.AC.Groundspeed()) * 3600 // in seconds
			turn := abs(headingDifference(hdg, float32(ap.Heading())-database.MagneticVariation))
			lg.Errorf("dist %f, eta %f, turn %f", dist, eta, turn)

			// Assuming 3 degree/second turns, then we might start to turn to
			// intercept when the eta until intercept is 1/3 the number of
			// degrees to cover.  However... the aircraft approaches the
			// localizer more slowly as it turns, so we'll add another 1/2
			// fudge factor, which seems to account for that reasonably well.
			if eta < turn/3/2 {
				lg.Errorf("assigned approach heading! %d", ap.Heading())
				ac.AssignedHeading = ap.Heading()
				ac.TurnDirection = 0
				// Just in case.. Thus we will be ready to pick up the
				// approach waypoints once we capture.
				ac.Waypoints = nil
			}
		}
	}

	// Otherwise, if the controller has assigned a heading, then no matter
	// what, that's what we will turn to.
	if ac.AssignedHeading != 0 {
		targetHeading = float32(ac.AssignedHeading)
		if ac.TurnDirection != 0 {
			// If the controller specified a left or right turn, then
			// compute the full turn angle. We'll do no more than 3 degrees
			// of that.
			if ac.TurnDirection < 0 { // left
				angle := ac.Heading - targetHeading
				if angle < 0 {
					angle += 360
				}
				angle = min(angle, 3)
				turn = -angle
			} else if ac.TurnDirection > 0 { // right
				angle := targetHeading - ac.Heading
				if angle < 0 {
					angle += 360
				}
				angle = min(angle, 3)
				turn = angle
			}
		}
	} else if len(ac.Waypoints) > 0 {
		// Our desired heading is the heading to get to the next waypoint.
		targetHeading = headingp2ll(ac.Position, ac.Waypoints[0].Location,
			database.MagneticVariation)
	} else {
		// And otherwise we're flying off into the void...
		return
	}

	// A turn direction wasn't specified, so figure out which way is
	// closest.
	if turn == 0 {
		// First find the angle to rotate the target heading by so that
		// it's aligned with 180 degrees. This lets us not worry about the
		// complexities of the wrap around at 0/360..
		rot := 180 - targetHeading
		if rot < 0 {
			rot += 360
		}
		cur := mod(ac.Heading+rot, 360) // w.r.t. 180 target
		turn = clamp(180-cur, -3, 3)    // max 3 degrees / second
	}

	// Finally, do the turn.
	if ac.Heading != targetHeading {
		ac.Heading += turn
		if ac.Heading < 0 {
			ac.Heading += 360
		} else if ac.Heading > 360 {
			ac.Heading -= 360
		}
	}
}

func (ss *SimServer) RunWaypointCommands(ac *SSAircraft, cmds []WaypointCommand) {
	for _, cmd := range cmds {
		switch cmd {
		case WaypointCommandHandoff:
			// Handoff to the user's position?
			ac.AC.InboundHandoffController = ss.callsign
			eventStream.Post(&OfferedHandoffEvent{controller: ac.AC.TrackingController, ac: ac.AC})

		case WaypointCommandDelete:
			eventStream.Post(&RemovedAircraftEvent{ac: ac.AC})
			delete(ss.aircraft, ac.AC.Callsign)
			return
		}
	}
}

func (ac *SSAircraft) UpdatePositionAndGS(ss *SimServer) {
	// Update position given current heading
	prev := ac.Position
	hdg := ac.Heading - database.MagneticVariation
	v := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
	// First use TAS to get a first whack at the new position.
	newPos := add2f(ll2nm(ac.Position), scale2f(v, ac.TAS()/3600))

	// Now add wind...
	airborne := ac.IAS >= 1.1*float32(ac.Performance.Speed.Min)
	if airborne {
		// TODO: have a better gust model?
		windKts := float32(ss.wind.speed + rand.Intn(ss.wind.gust))

		// wind.dir is where it's coming from, so +180 to get the vector
		// that affects the aircraft's course.
		d := float32(ss.wind.dir + 180)
		vWind := [2]float32{sin(radians(d)), cos(radians(d))}
		newPos = add2f(newPos, scale2f(vWind, windKts/3600))
	}

	// Finally update position and groundspeed.
	ac.Position = nm2ll(newPos)
	ac.GS = distance2f(ll2nm(prev), newPos) * 3600
}

func (ac *SSAircraft) UpdateWaypoints(ss *SimServer) {
	if ap := ac.Approach; ap != nil &&
		ac.ClearedApproach &&
		len(ac.Waypoints) == 0 &&
		headingDifference(float32(ap.Heading()), ac.Heading) < 2 &&
		ac.Approach.Type == ILSApproach {
		loc := ap.Line()
		dist := PointLineDistance(ll2nm(ac.Position), ll2nm(loc[0]), ll2nm(loc[1]))
		lg.Errorf("dist %f", dist)

		if dist < .2 {
			// we'll call that good enough. Now we need to figure out which
			// fixes in the approach are still ahead and then add them to
			// the aircraft's waypoints..
			if len(ap.Waypoints) != 1 {
				lg.Errorf("ILS approaches should only have a single set of waypoints?!")
			}

			pos := ll2nm(ac.Position)
			hdg := ac.Heading - database.MagneticVariation
			headingVector := [2]float32{sin(radians(hdg)), cos(radians(hdg))}

			ac.Waypoints = nil
			found := false
			for _, wp := range ap.Waypoints[0] {
				if wp.Altitude != 0 && int(ac.Altitude) < wp.Altitude {
					continue
				}

				v := sub2f(ll2nm(wp.Location), pos)
				if !found && v[0]*headingVector[0]+v[1]*headingVector[1] < 0 {
					lg.Errorf("%s: behind us...", wp.Fix)
				} else {
					lg.Errorf("%s: ahead of us...", wp.Fix)
					ac.Waypoints = append(ac.Waypoints, wp)
				}
			}

			ac.AssignedHeading = 0
			ac.AssignedAltitude = 0
		}
	}

	if len(ac.Waypoints) == 0 {
		return
	}

	wp := ac.Waypoints[0]

	// Are we nearly at the fix?  Move on to the next one a little bit
	// early so that we don't overshoot.
	// TODO: do the turn in a more principled manner so that we smoothly
	// intercept the desired outbound direction.
	if nmdistance2ll(ac.Position, wp.Location) < .75 {
		// Execute any commands associated with the waypoint
		ss.RunWaypointCommands(ac, wp.Commands)

		if ac.Waypoints[0].Heading != 0 {
			// We have an outbound heading
			ac.AssignedHeading = wp.Heading
			ac.TurnDirection = 0
			// The aircraft won't head to the next waypoint until the
			// assigned heading is cleared, though...
			ac.Waypoints = ac.Waypoints[1:]
		} else {
			ac.Waypoints = ac.Waypoints[1:]

			if len(ac.Waypoints) > 0 {
				ac.WaypointUpdate(ac.Waypoints[0])
			}
		}
	}
}

func (ac *SSAircraft) WaypointUpdate(wp Waypoint) {
	lg.Errorf("waypoint update %+v, ac %+v", wp, ac)

	// Handle altitude/speed restriction at waypoint.
	if wp.Altitude != 0 {
		ac.CrossingAltitude = wp.Altitude
		ac.AssignedAltitude = 0
	}
	if wp.Speed != 0 {
		ac.CrossingSpeed = wp.Speed
		ac.AssignedSpeed = 0
	}

	ac.AssignedHeading = 0
	ac.TurnDirection = 0

	if ac.ClearedApproach {
		// The aircraft has made it to the approach fix they
		// were cleared to.
		lg.Errorf("%s: on final...", ac.AC.Callsign)
		ac.OnFinal = true
	}
}

///////////////////////////////////////////////////////////////////////////
// SimServer

type SimServer struct {
	callsign    string
	aircraft    map[string]*SSAircraft
	handoffs    map[string]time.Time
	controllers map[string]*Controller

	currentTime       time.Time // this is our fake time--accounting for pauses & simRate..
	lastUpdateTime    time.Time // this is w.r.t. true wallclock time
	simRate           float32
	paused            bool
	remainingLaunches int
	airportConfigs    []*AirportConfig

	wind struct {
		dir   int
		speed int
		gust  int
	}

	lastTrackUpdate time.Time
	lastSimUpdate   time.Time

	spawners []*AircraftSpawner

	showSettings bool
}

func NewSimServer(ssc SimServerConnectionConfiguration) *SimServer {
	rand.Seed(time.Now().UnixNano())

	ss := &SimServer{
		callsign:          ssc.callsign,
		aircraft:          make(map[string]*SSAircraft),
		handoffs:          make(map[string]time.Time),
		controllers:       make(map[string]*Controller),
		currentTime:       time.Now(),
		lastUpdateTime:    time.Now(),
		remainingLaunches: int(ssc.numAircraft),
		airportConfigs:    ssc.airportConfigs,
		simRate:           1,
	}
	ss.wind.dir = int(ssc.wind.dir)
	ss.wind.speed = int(ssc.wind.speed)
	ss.wind.gust = int(ssc.wind.gust)

	addController := func(cs string, loc string, freq float32) {
		pos, _ := database.Locate(loc)
		ss.controllers[cs] = &Controller{
			Callsign:  cs,
			Location:  pos,
			Frequency: NewFrequency(freq),
		}
	}

	// Us.
	addController("JFK_DEP", "KJFK", 135.9) //  2A

	addController("LGA_DEP", "KLGA", 120.4)     //  1L
	addController("ISP_APP", "KISP", 120.05)    //  3H
	addController("JFK_APP", "KJFK", 132.4)     //  2A
	addController("NY_LE_DEP", "MERIT", 126.8)  //  5E
	addController("NY_LS_DEP", "DIXIE", 124.75) //  5S
	addController("NY_F_CTR", "KEWR", 128.3)    // N66
	addController("BOS_E_CTR", "KBOS", 133.45)  // B17

	for _, ap := range ssc.airportConfigs {
		for _, d := range ap.departureConfigs {
			if d.enabled {
				ss.spawners = append(ss.spawners, d.makeSpawner(d))
			}
		}
		for _, a := range ap.arrivalConfigs {
			if a.enabled {
				ss.spawners = append(ss.spawners, a.makeSpawner(a))
			}
		}
	}
	if len(ss.spawners) == 0 {
		panic("NO SPAWNERS?!??!?")
	}

	// Prime the pump before the user gets involved
	for _, spawner := range ss.spawners {
		if ss.remainingLaunches > 0 {
			err := spawner.MaybeSpawn(ss)
			if err != nil {
				lg.Errorf("Spawn error: %v", err)
			}
		}
	}
	for i := 0; i < 60; i++ {
		ss.updateSim()
	}

	return ss
}

func (ss *SimServer) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetScratchpad(callsign string, scratchpad string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.Scratchpad = scratchpad
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) SetTemporaryAltitude(callsign string, alt int) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) PushFlightStrip(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) InitiateTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != "" {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.TrackingController = ss.callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		eventStream.Post(&InitiatedTrackEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) DropTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.TrackingController = ""
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		eventStream.Post(&DroppedTrackEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) Handoff(callsign string, controller string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else if ctrl := ss.GetController(controller); ctrl == nil {
		return ErrNoController
	} else {
		ac.AC.OutboundHandoffController = ctrl.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		eventStream.Post(&OfferedHandoffEvent{controller: ss.callsign, ac: ac.AC})
		acceptDelay := 2 + rand.Intn(10)
		ss.handoffs[callsign] = ss.CurrentTime().Add(time.Duration(acceptDelay) * time.Second)
		return nil
	}
}

func (ss *SimServer) AcceptHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.InboundHandoffController != ss.callsign {
		return ErrNotBeingHandedOffToMe
	} else {
		ac.AC.InboundHandoffController = ""
		ac.AC.TrackingController = ss.callsign
		eventStream.Post(&AcceptedHandoffEvent{controller: ss.callsign, ac: ac.AC})
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC}) // FIXME...
		return nil
	}
}

func (ss *SimServer) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) CancelHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.OutboundHandoffController = ""
		// TODO: we are inconsistent in other control backends about events
		// when user does things like this; sometimes no event, sometimes
		// modified a/c event...
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) PointOut(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SendTextMessage(m TextMessage) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) RequestControllerATIS(controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) Disconnect() {
	for _, ac := range ss.aircraft {
		eventStream.Post(&RemovedAircraftEvent{ac: ac.AC})
	}
}

func (ss *SimServer) GetAircraft(callsign string) *Aircraft {
	if ac, ok := ss.aircraft[callsign]; ok {
		return ac.AC
	}
	return nil
}

func (ss *SimServer) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range ss.aircraft {
		if filter(ac.AC) {
			filtered = append(filtered, ac.AC)
		}
	}
	return filtered
}

func (ss *SimServer) GetAllAircraft() []*Aircraft {
	return ss.GetFilteredAircraft(func(*Aircraft) bool { return true })
}

func (ss *SimServer) GetFlightStrip(callsign string) *FlightStrip {
	if ac, ok := ss.aircraft[callsign]; ok {
		return &ac.Strip
	}
	return nil
}

func (ss *SimServer) AddAirportForWeather(airport string) {
	// UNIMPLEMENTED
}

func (ss *SimServer) GetMETAR(location string) *METAR {
	// UNIMPLEMENTED
	return nil
}

func (ss *SimServer) GetAirportATIS(airport string) []ATIS {
	// UNIMPLEMENTED
	return nil
}

func (ss *SimServer) GetController(callsign string) *Controller {
	if ctrl, ok := ss.controllers[callsign]; ok {
		return ctrl
	}
	for _, ctrl := range ss.controllers {
		if pos := ctrl.GetPosition(); pos != nil && pos.SectorId == callsign {
			return ctrl
		}
	}
	return nil
}

func (ss *SimServer) GetAllControllers() []*Controller {
	_, ctrl := FlattenMap(ss.controllers)
	return ctrl
}

func (ss *SimServer) SetPrimaryFrequency(f Frequency) {
	// UNIMPLEMENTED
}

func (ss *SimServer) GetUpdates() {
	if ss.paused {
		return
	}

	// Update the current time
	elapsed := time.Since(ss.lastUpdateTime)
	elapsed = time.Duration(ss.simRate * float32(elapsed))
	ss.currentTime = ss.currentTime.Add(elapsed)
	ss.lastUpdateTime = time.Now()

	// Accept any handoffs whose time has time...
	now := ss.CurrentTime()
	for callsign, t := range ss.handoffs {
		if now.After(t) {
			ac := ss.aircraft[callsign].AC
			ac.TrackingController = ac.OutboundHandoffController
			ac.OutboundHandoffController = ""
			eventStream.Post(&AcceptedHandoffEvent{controller: ac.TrackingController, ac: ac})
			globalConfig.AudioSettings.HandleEvent(AudioEventHandoffAccepted)
			delete(ss.handoffs, callsign)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(ss.lastSimUpdate) >= time.Second {
		ss.lastSimUpdate = now
		ss.updateSim()
	}

	// Add a new radar track every 5 seconds.
	if now.Sub(ss.lastTrackUpdate) >= 5*time.Second {
		ss.lastTrackUpdate = now

		for _, ac := range ss.aircraft {
			ac.AC.AddTrack(RadarTrack{
				Position:    ac.Position,
				Altitude:    int(ac.Altitude),
				Groundspeed: int(ac.GS),
				Heading:     ac.Heading - database.MagneticVariation,
				Time:        now,
			})

			eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		}
	}

	// And give all of the spawners a chance to launch aircraft, if they
	// feel like it.
	if ss.remainingLaunches > 0 {
		for _, spawner := range ss.spawners {
			spawner.MaybeSpawn(ss)
		}
	}
}

func (ss *SimServer) updateSim() {
	for _, ac := range ss.aircraft {
		ac.UpdateAirspeed()
		ac.UpdateAltitude()
		ac.UpdateHeading()
		ac.UpdatePositionAndGS(ss)
		ac.UpdateWaypoints(ss)
	}
}

func (ss *SimServer) Connected() bool {
	return true
}

func (ss *SimServer) Callsign() string {
	return ss.callsign
}

func (ss *SimServer) CurrentTime() time.Time {
	return ss.currentTime
}

func (ss *SimServer) GetWindowTitle() string {
	return "SimServer: " + ss.callsign
}

func (ss *SimServer) SpawnAircraft(ssa *SSAircraft) {
	if _, ok := ss.aircraft[ssa.AC.Callsign]; ok {
		lg.Errorf("%s: already have an aircraft with that callsign!", ssa.AC.Callsign)
		return
	}

	if len(ssa.Waypoints) == 0 {
		lg.Errorf("%s: no waypoints specified", ssa.AC.Callsign)
		return
	}

	ss.RunWaypointCommands(ssa, ssa.Waypoints[0].Commands)

	ssa.Position = ssa.Waypoints[0].Location
	ssa.Heading = float32(ssa.Waypoints[0].Heading)

	if ssa.Heading == 0 { // unassigned, so get the heading from the next fix
		ssa.Heading = headingp2ll(ssa.Position, ssa.Waypoints[1].Location, database.MagneticVariation)
	}

	ssa.Waypoints = ssa.Waypoints[1:]

	ss.aircraft[ssa.AC.Callsign] = ssa

	ss.remainingLaunches--

	eventStream.Post(&AddedAircraftEvent{ac: ssa.AC})
}

func pilotResponse(callsign string, fm string, args ...interface{}) {
	tm := TextMessage{
		sender:      callsign,
		messageType: TextBroadcast,
		contents:    fmt.Sprintf(fm, args...),
	}
	eventStream.Post(&TextMessageEvent{message: &tm})
}

func (ss *SimServer) AssignAltitude(callsign string, altitude int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if float32(altitude) > ac.Altitude {
			pilotResponse(callsign, "climb and maintain %d", altitude)
		} else if float32(altitude) == ac.Altitude {
			pilotResponse(callsign, "maintain %d", altitude)
		} else {
			pilotResponse(callsign, "descend and maintain %d", altitude)
		}

		ac.AssignedAltitude = altitude
		return nil
	}
}

func (ss *SimServer) AssignHeading(callsign string, heading int, turn int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if turn > 0 {
			pilotResponse(callsign, "turn right heading %d", heading)
		} else if turn == 0 {
			pilotResponse(callsign, "fly heading %d", heading)
		} else {
			pilotResponse(callsign, "turn left heading %d", heading)
		}

		// A 0 heading shouldn't be specified, but at least cause the
		// aircraft to do what is intended, since 0 represents an
		// unassigned heading.
		if heading == 0 {
			heading = 360
		}

		ac.AssignedHeading = heading
		ac.TurnDirection = turn
		return nil
	}
}

func (ss *SimServer) AssignSpeed(callsign string, speed int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if speed == 0 {
			pilotResponse(callsign, "cancel speed restrictions")
		} else if speed == ac.AssignedSpeed {
			pilotResponse(callsign, "we'll maintain %d knots", speed)
		} else {
			pilotResponse(callsign, "maintain %d knots", speed)
		}

		ac.AssignedSpeed = speed
		return nil
	}
}

func (ss *SimServer) DirectFix(callsign string, fix string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		fix = strings.ToUpper(fix)

		// Look for the fix in the waypoints in the flight plan.
		for i, wp := range ac.Waypoints {
			if fix == wp.Fix {
				ac.Waypoints = ac.Waypoints[i:]
				if len(ac.Waypoints) > 0 {
					ac.WaypointUpdate(wp)
				}
				pilotResponse(callsign, "direct %s", fix)
				return nil
			}
		}

		if ac.Approach != nil {
			for _, route := range ac.Approach.Waypoints {
				for _, wp := range route {
					if wp.Fix == fix {
						ac.Waypoints = []Waypoint{wp}
						if len(ac.Waypoints) > 0 {
							ac.WaypointUpdate(wp)
						}
						pilotResponse(callsign, "direct %s", fix)
						return nil
					}
				}
			}
		}

		return fmt.Errorf("%s: fix not found in route", fix)
	}
}

func (ss *SimServer) getApproach(callsign string, approach string) (*Approach, *SSAircraft, error) {
	ac, ok := ss.aircraft[callsign]
	if !ok {
		return nil, nil, ErrNoAircraftForCallsign
	}
	fp := ac.AC.FlightPlan
	if fp == nil {
		return nil, nil, ErrNoFlightPlan
	}

	for _, ap := range ss.airportConfigs {
		if ap.name == fp.ArrivalAirport {
			for i, appr := range ap.approaches {
				if appr.ShortName == approach {
					return &ap.approaches[i], ac, nil
				}
			}
			lg.Errorf("wanted approach %s; airport %s options -> %+v", approach, ap.name, ap.approaches)
			return nil, nil, ErrUnknownApproach
		}
	}
	return nil, nil, ErrArrivalAirportUnknown
}

func (ss *SimServer) ExpectApproach(callsign string, approach string) error {
	ap, ac, err := ss.getApproach(callsign, approach)
	if err != nil {
		return err
	}

	ac.Approach = ap
	pilotResponse(callsign, "we'll expect the "+ap.FullName+" approach")

	return nil
}

func (ss *SimServer) ClearedApproach(callsign string, approach string) error {
	ap, ac, err := ss.getApproach(callsign, approach)
	if err != nil {
		return err
	}

	response := ""
	if ac.Approach == nil {
		// allow it anyway...
		response = "you never told us to expect an approach, but "
		ac.Approach = ap
	}
	if ac.Approach.ShortName != approach {
		pilotResponse(callsign, "but you cleared us for the "+ap.FullName+" approach...")
		return ErrClearedForUnexpectedApproach
	}
	if ac.ClearedApproach {
		pilotResponse(callsign, "you already cleared us for the "+ap.FullName+" approach...")
		return nil
	}

	if ac.Approach.Type == ILSApproach && len(ap.Waypoints) == 0 {
		if ac.AssignedHeading == 0 {
			pilotResponse(callsign, "we need either direct or a heading to intercept")
			return nil
		}
		// Otherwise nothing more to do; keep flying the heading for now.
		// After we intercept we'll get the rest of the waypoints added.
	} else {
		// For RNAV or ILS direct to a fix, the first (and only) entry in
		// Waypoints should be one of the approach fixes. Add the remaining ones..
		if len(ac.Waypoints) != 1 {
			// TODO: ERROR CHECKING BETTER, HANDLE RNAV CLEARED W/O DIRECT TO A FIX...
			lg.Errorf("WTF waypoints: %+v", ac.Waypoints)
			return errors.New("WTF waypoints")
		}
		found := false
		for _, route := range ap.Waypoints {
			for _, wp := range route {
				if wp.Fix == ac.Waypoints[0].Fix {
					found = true
				} else if found {
					ac.Waypoints = append(ac.Waypoints, wp)
				}
			}
			if found {
				break
			}
		}
		if !found {
			lg.Errorf("waypoint %s not in approach's waypoints %+v", ac.Waypoints[0].Fix, ap.Waypoints)
			return errors.New("WTF2 waypoints")
		}
	}

	ac.AssignedSpeed = 0 // cleared approach cancels speed restrictions
	ac.ClearedApproach = true

	pilotResponse(callsign, response+"cleared "+ap.FullName+" approach")
	return nil
}

func (ss *SimServer) PrintInfo(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		s := fmt.Sprintf("%s: current alt %d, assigned alt %d crossing alt %d",
			ac.AC.Callsign, ac.AC.Altitude(), ac.AssignedAltitude, ac.CrossingAltitude)
		if ac.AssignedHeading != 0 {
			s += fmt.Sprintf(" heading %d", ac.AssignedHeading)
			if ac.TurnDirection != 0 {
				s += fmt.Sprintf(" turn direction %d", ac.TurnDirection)
			}
		}
		s += fmt.Sprintf(", IAS %f GS %d speed %d crossing speed %d",
			ac.IAS, ac.AC.Groundspeed(), ac.AssignedSpeed, ac.CrossingSpeed)

		if ac.ClearedApproach {
			s += ", cleared approach"
		}
		if ac.OnFinal {
			s += ", on final"
		}
		if ac.Approach != nil {
			s += fmt.Sprintf(", approach: %+v", ac.Approach)
		}
		s += fmt.Sprintf(", route %+v", ac.Waypoints)
		lg.Errorf("%s", s)
	}
	return nil
}

func (ss *SimServer) DeleteAircraft(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		eventStream.Post(&RemovedAircraftEvent{ac: ac.AC})
		delete(ss.aircraft, callsign)
		return nil
	}
}

func (ss *SimServer) Paused() bool {
	return ss.paused
}

func (ss *SimServer) TogglePause() error {
	ss.paused = !ss.paused
	ss.lastUpdateTime = time.Now() // ignore time passage...
	return nil
}

func (ss *SimServer) ActivateSettingsWindow() {
	ss.showSettings = true

}

func (ss *SimServer) DrawSettingsWindow() {
	if !ss.showSettings {
		return
	}

	imgui.BeginV("Simulation Settings", &ss.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	imgui.SliderFloatV("Simulation speed", &ss.simRate, 1, 10, "%.1f", 0)
	imgui.Separator()

	var fsp *FlightStripPane
	var stars *STARSPane
	globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
		switch pane := p.(type) {
		case *FlightStripPane:
			fsp = pane
		case *STARSPane:
			stars = pane
		}
	})
	if imgui.CollapsingHeader("Audio") {
		globalConfig.AudioSettings.DrawUI()
	}
	if fsp != nil && imgui.CollapsingHeader("Flight Strips") {
		fsp.DrawUI()
	}
	if stars != nil && imgui.CollapsingHeader("STARS Radar Scope") {
		stars.DrawUI()
	}

	imgui.End()
}

///////////////////////////////////////////////////////////////////////////

type RouteTemplate struct {
	Waypoints        string
	Scratchpad       string
	Route            string
	InitialAltitude  int
	ClearedAltitude  int
	InitialSpeed     int
	SpeedRestriction int

	DestinationAirports []string
	DepartureAirports   []string

	Category string

	InitialController string
	Airlines          []string
	Fleet             string
}

func (r *RouteTemplate) RandomAircraft() (*Aircraft, error) {
	callsign, aircraftICAO, err := chooseAircraft(r.Airlines, r.Fleet)
	if err != nil {
		return nil, err
	}

	departure := r.DepartureAirports[rand.Intn(len(r.DepartureAirports))]
	destination := r.DestinationAirports[rand.Intn(len(r.DestinationAirports))]
	squawk := Squawk(rand.Intn(0o7000))
	alt := 20000 + 1000*rand.Intn(22)
	if rand.Float32() < .3 {
		alt = 7000 + 1000*rand.Intn(11)
	}

	ac := &Aircraft{
		Callsign:           callsign,
		Scratchpad:         r.Scratchpad,
		AssignedSquawk:     squawk,
		Squawk:             squawk,
		Mode:               Charlie,
		TrackingController: r.InitialController,
		FlightPlan: &FlightPlan{
			Rules:            IFR,
			AircraftType:     aircraftICAO,
			DepartureAirport: departure,
			ArrivalAirport:   destination,
			Altitude:         alt,
			Route:            r.Route + " DCT " + destination,
		},
	}

	acInfo, ok := database.AircraftPerformance[aircraftICAO]
	if !ok {
		return nil, fmt.Errorf("%s: ICAO not in db", aircraftICAO)
	}
	if acInfo.WeightClass == "H" {
		ac.FlightPlan.AircraftType = "H/" + ac.FlightPlan.AircraftType
	}
	if acInfo.WeightClass == "J" {
		ac.FlightPlan.AircraftType = "J/" + ac.FlightPlan.AircraftType
	}

	return ac, nil
}

func chooseAircraft(airlines []string, fleetId string) (callsign string, aircraftICAO string, err error) {
	al := airlines[rand.Intn(len(airlines))]
	airline, ok := database.Airlines[al]
	if !ok {
		err = fmt.Errorf("%s: unknown airline!", al)
		return
	}

	// random callsign
	callsign = strings.ToUpper(airline.ICAO)
	for _, ch := range airline.Callsign.CallsignFormats[rand.Intn(len(airline.Callsign.CallsignFormats))] {
		switch ch {
		case '#':
			callsign += fmt.Sprintf("%d", rand.Intn(10))

		case '@':
			callsign += string(rune('A' + rand.Intn(26)))
		}
	}

	// Pick an aircraft.
	var aircraft FleetAircraft
	count := 0

	fleet, ok := airline.Fleets[fleetId]
	if !ok {
		lg.Errorf("%s: didn't find fleet %s -- %+v", airline.ICAO, fleetId, airline)
		for _, fl := range airline.Fleets {
			fleet = fl
			break
		}
	}

	for _, ac := range fleet {
		// Reservoir sampling...
		count += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(count) {
			aircraft = ac
		}
	}

	if _, ok := database.AircraftPerformance[aircraft.ICAO]; !ok {
		err = fmt.Errorf("%s: chose aircraft but not in DB!", aircraft.ICAO)
		return
	}

	aircraftICAO = aircraft.ICAO
	return
}

///////////////////////////////////////////////////////////////////////////
// AircraftSpawner

type AircraftSpawner struct {
	nextSpawn time.Time

	rate      int
	challenge float32

	routeTemplates []RouteTemplate

	lastRouteTemplateCategory string
	lastRouteTemplate         *RouteTemplate
}

func (as *AircraftSpawner) MaybeSpawn(ss *SimServer) error {
	if ss.CurrentTime().Before(as.nextSpawn) {
		return nil
	}

	// Pick a route
	var rt *RouteTemplate
	u := rand.Float32()
	if u < as.challenge/2 {
		rt = as.lastRouteTemplate // note: may be nil the first time...
	} else if u < as.challenge {
		// Try to find one with the same category; reservoir sampling
		n := float32(0)
		for _, r := range as.routeTemplates {
			if r.Category == as.lastRouteTemplateCategory {
				n++
				if rand.Float32() < 1/n {
					rt = &r
				}
			}
		}
	}

	// Either the challenge cases didn't hit or they did and it's the first
	// time through...
	if rt == nil {
		rt = &as.routeTemplates[rand.Intn(len(as.routeTemplates))]
	}
	as.lastRouteTemplateCategory = rt.Category
	as.lastRouteTemplate = rt

	ac, err := rt.RandomAircraft()
	if err != nil {
		return err
	}

	acInfo, ok := database.AircraftPerformance[ac.FlightPlan.BaseType()]
	if !ok {
		return fmt.Errorf("%s: ICAO not in db", ac.FlightPlan.BaseType())
	}

	wp, err := parseWaypoints(rt.Waypoints)
	if err != nil {
		return err
	}

	ss.SpawnAircraft(&SSAircraft{
		AC:               ac,
		Performance:      acInfo,
		Waypoints:        wp,
		Altitude:         float32(rt.InitialAltitude),
		AssignedAltitude: rt.ClearedAltitude,
		IAS:              float32(rt.InitialSpeed),
		AssignedSpeed:    rt.SpeedRestriction,
	})

	seconds := 3600/as.rate - 10 + rand.Intn(21)
	as.nextSpawn = ss.CurrentTime().Add(time.Duration(seconds) * time.Second)

	return nil
}

func parseWaypoints(str string) ([]Waypoint, error) {
	var waypoints []Waypoint
	for _, wp := range strings.Split(str, "/") {
		if len(wp) == 0 {
			return nil, fmt.Errorf("Empty waypoint in string: \"%s\"", str)
		}

		if wp == "@" {
			if len(waypoints) == 0 {
				return nil, fmt.Errorf("No previous waypoint before handoff specifier")
			}
			waypoints[len(waypoints)-1].Commands =
				append(waypoints[len(waypoints)-1].Commands, WaypointCommandHandoff)
		} else if wp[0] == '#' {
			if len(waypoints) == 0 {
				return nil, fmt.Errorf("No previous waypoint before heading specifier")
			}
			if hdg, err := strconv.Atoi(wp[1:]); err != nil {
				return nil, fmt.Errorf("%s: invalid waypoint outbound heading: %v", wp[1:], err)
			} else {
				waypoints[len(waypoints)-1].Heading = hdg
			}
		} else {
			var pos Point2LL
			var ok bool
			if pos, ok = database.Locate(wp); !ok {
				if pos, ok = configPositions[wp]; !ok {
					var err error
					if pos, err = ParseLatLong(wp); err != nil {
						return nil, fmt.Errorf("%s: unable to locate waypoint", wp)
					}
				}
			}
			waypoints = append(waypoints, Waypoint{Fix: wp, Location: pos})
		}
	}

	return waypoints, nil
}

///////////////////////////////////////////////////////////////////////////
// KJFK

type Exit struct {
	name         string
	fixes        [][2]string // fix and scratchpad
	destinations []string
}

var jfkWater = Exit{
	name:         "Water",
	fixes:        [][2]string{[2]string{"WAVEY", "WAV"}, [2]string{"SHIPP", "SHI"}, [2]string{"HAPIE", "HAP"}, [2]string{"BETTE", "BET"}},
	destinations: []string{"TAPA", "TXKF", "KMCO", "KFLL", "KSAV", "KATL", "EGLL", "EDDF", "LFPG", "EINN"},
}

var jfkEast = Exit{
	name:         "East",
	fixes:        [][2]string{[2]string{"MERIT", "MER"}, [2]string{"GREKI", "GRE"}, [2]string{"BAYYS", "BAY"}, [2]string{"BDR", "BDR"}},
	destinations: []string{"KBOS", "KPVD", "KACK", "KBDL", "KPWM", "KSYR"},
}

var jfkSouthwest = Exit{
	name:         "Southwest",
	fixes:        [][2]string{[2]string{"DIXIE", "DIX"}, [2]string{"WHITE", "WHI"}, [2]string{"RBV", "RBV"}, [2]string{"ARD", "ARD"}},
	destinations: []string{"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL"},
}

var jfkDIXIE = Exit{
	name:         "DIXIE",
	fixes:        [][2]string{[2]string{"DIXIE", "DIX"}},
	destinations: []string{"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL"},
}

var jfkWHITE = Exit{
	name:         "WHITE",
	fixes:        [][2]string{[2]string{"WHITE", "WHI"}},
	destinations: []string{"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL"},
}

var jfkNorth = Exit{
	name:         "North",
	fixes:        [][2]string{[2]string{"COATE", "COA"}, [2]string{"NEION", "NEI"}, [2]string{"HAAYS", "HAY"}, [2]string{"GAYEL", "GAY"}},
	destinations: []string{"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW"},
}

var jfkDEEZZ = Exit{
	name:         "North",
	fixes:        [][2]string{[2]string{"DEEZZ", "DEZ"}},
	destinations: []string{"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW"},
}

func jfkRunwayConfig() *DepartureConfig {
	c := &DepartureConfig{
		rate:            45,
		challenge:       0.5,
		categoryEnabled: make(map[string]*bool),
	}
	c.categoryEnabled["Water"] = new(bool)
	c.categoryEnabled["East"] = new(bool)
	c.categoryEnabled["Southwest"] = new(bool)
	c.categoryEnabled["North"] = new(bool)
	return c
}

func jfkJetProto() RouteTemplate {
	return RouteTemplate{
		InitialAltitude:   13,
		DepartureAirports: []string{"KJFK"},
		ClearedAltitude:   5000,
		Fleet:             "default",
		Airlines: []string{
			"AAL", "AFR", "AIC", "AMX", "ANA", "ASA", "BAW", "BWA", "CCA", "CLX", "CPA", "DAL", "DLH", "EDV", "EIN",
			"ELY", "FDX", "FFT", "GEC", "IBE", "JBU", "KAL", "KLM", "LXJ", "NKS", "QXE", "SAS", "UAE", "UAL", "UPS"},
	}
}

func jfkPropProto() RouteTemplate {
	return RouteTemplate{
		InitialAltitude:   13,
		DepartureAirports: []string{"KJFK"},
		ClearedAltitude:   2000,
		Fleet:             "short",
		Airlines:          []string{"QXE", "BWA", "FDX"},
	}
}

func (e Exit) GetRouteTemplates(r RouteTemplate, waypoints, route string) []RouteTemplate {
	var routeTemplates []RouteTemplate
	for _, fix := range e.fixes {
		r.Waypoints = waypoints + "/" + fix[0]
		r.Route = route + " " + fix[0]
		r.Category = e.name
		r.Scratchpad = fix[1]
		r.DestinationAirports = e.destinations
		routeTemplates = append(routeTemplates, r)
	}
	return routeTemplates
}

func jfk31LRunwayConfig() *DepartureConfig {
	c := jfkRunwayConfig()
	c.name = "31L"
	c.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
		var routeTemplates []RouteTemplate

		rp := jfkJetProto()

		if *config.categoryEnabled["Water"] {
			routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, "_JFK_31L/_JFK_13R/CRI/#176", "SKORR5.YNKEE")...)
		}
		if *config.categoryEnabled["East"] {
			routeTemplates = append(routeTemplates, jfkEast.GetRouteTemplates(rp, "_JFK_31L/_JFK_13R/CRI/#176", "SKORR5.YNKEE")...)
		}
		if *config.categoryEnabled["Southwest"] {
			routeTemplates = append(routeTemplates, jfkSouthwest.GetRouteTemplates(rp, "_JFK_31L/_JFK_13R/CRI/#223", "SKORR5.RNGRR")...)
		}
		if *config.categoryEnabled["North"] {
			routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, "_JFK_31L/_JFK_13R/CRI/#176", "SKORR5.YNKEE")...)
			routeTemplates = append(routeTemplates, jfkDEEZZ.GetRouteTemplates(rp, "_JFK_31L/_JFK_13R/SKORR/CESID/YNKEE/#172", "DEEZZ5.CANDR J60")...)
		}

		return &AircraftSpawner{
			rate:           int(config.rate),
			challenge:      config.challenge,
			routeTemplates: routeTemplates,
		}
	}
	return c
}

func jfk22RRunwayConfig() *DepartureConfig {
	c := jfkRunwayConfig()
	c.name = "22R"
	c.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
		var routeTemplates []RouteTemplate

		rp := jfkJetProto()

		if *config.categoryEnabled["Water"] {
			routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, "_JFK_22R/_JFK_4L/#222", "JFK5")...)
		}
		if *config.categoryEnabled["East"] {
			routeTemplates = append(routeTemplates, jfkEast.GetRouteTemplates(rp, "_JFK_22R/_JFK_4L/#222", "JFK5")...)
		}
		if *config.categoryEnabled["Southwest"] {
			routeTemplates = append(routeTemplates, jfkSouthwest.GetRouteTemplates(rp, "_JFK_22R/_JFK_4L/#222", "JFK5")...)
		}
		if *config.categoryEnabled["North"] {
			routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, "_JFK_22R/_JFK_4L/#222", "JFK5")...)
			routeTemplates = append(routeTemplates, jfkDEEZZ.GetRouteTemplates(rp, "_JFK_22R/_JFK_4L/#224", "DEEZZ5.CANDR J60")...)
		}

		return &AircraftSpawner{
			rate:           int(config.rate),
			challenge:      config.challenge,
			routeTemplates: routeTemplates,
		}
	}
	return c
}

func jfk13RRunwayConfig() *DepartureConfig {
	c := jfkRunwayConfig()
	c.name = "13R"
	c.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
		var routeTemplates []RouteTemplate

		rp := jfkJetProto()

		if *config.categoryEnabled["Water"] {
			routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, "_JFK_13R/_JFK_31L/#109", "JFK5")...)
		}
		if *config.categoryEnabled["East"] {
			routeTemplates = append(routeTemplates, jfkEast.GetRouteTemplates(rp, "_JFK_13R/_JFK_31L/#109", "JFK5")...)
		}
		if *config.categoryEnabled["Southwest"] {
			routeTemplates = append(routeTemplates, jfkSouthwest.GetRouteTemplates(rp, "_JFK_13R/_JFK_31L/#109", "JFK5")...)
		}
		if *config.categoryEnabled["North"] {
			routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, "_JFK_13R/_JFK_31L/#109", "JFK5")...)
			routeTemplates = append(routeTemplates, jfkDEEZZ.GetRouteTemplates(rp, "_JFK_13R/_JFK_31L/#109", "DEEZZ5.CANDR J60")...)
		}

		return &AircraftSpawner{
			rate:           int(config.rate),
			challenge:      config.challenge,
			routeTemplates: routeTemplates,
		}
	}
	return c
}

func jfk4LRunwayConfig() *DepartureConfig {
	c := jfkRunwayConfig()
	c.name = "4L"
	c.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
		var routeTemplates []RouteTemplate

		rp := jfkJetProto()

		if *config.categoryEnabled["Water"] {
			routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, "_JFK_4L/_JFK_4La/#099", "JFK5")...)
		}
		if *config.categoryEnabled["East"] {
			routeTemplates = append(routeTemplates, jfkEast.GetRouteTemplates(rp, "_JFK_4L/_JFK_4La/#099", "JFK5")...)
		}
		if *config.categoryEnabled["Southwest"] {
			routeTemplates = append(routeTemplates, jfkSouthwest.GetRouteTemplates(rp, "_JFK_4L/_JFK_4La/#099", "JFK5")...)
		}
		if *config.categoryEnabled["North"] {
			routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, "_JFK_4L/_JFK_4La/#099", "JFK5")...)
			routeTemplates = append(routeTemplates, jfkDEEZZ.GetRouteTemplates(rp, "_JFK_4L/_JFK_4La/#099", "DEEZZ5.CANDR J60")...)
		}

		return &AircraftSpawner{
			rate:           int(config.rate),
			challenge:      config.challenge,
			routeTemplates: routeTemplates,
		}
	}
	return c
}

func jfk31RRunwayConfig() *DepartureConfig {
	c := jfkRunwayConfig()
	delete(c.categoryEnabled, "Southwest")

	c.name = "31R"
	c.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
		var routeTemplates []RouteTemplate

		rp := jfkPropProto()

		if *config.categoryEnabled["Water"] {
			routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, "_JFK_31R/_JFK_13L/#090", "JFK5")...)
		}
		if *config.categoryEnabled["East"] {
			routeTemplates = append(routeTemplates, jfkEast.GetRouteTemplates(rp, "_JFK_31R/_JFK_13L/#090", "JFK5")...)
		}
		if *config.categoryEnabled["North"] {
			routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, "_JFK_31R/_JFK_13L/#090", "JFK5")...)
		}

		return &AircraftSpawner{
			rate:           int(config.rate),
			challenge:      config.challenge,
			routeTemplates: routeTemplates,
		}
	}
	return c
}

func GetJFKConfig() *AirportConfig {
	ac := &AirportConfig{name: "KJFK"}

	ac.departureConfigs = append(ac.departureConfigs, jfk31LRunwayConfig())
	ac.departureConfigs = append(ac.departureConfigs, jfk31RRunwayConfig())
	ac.departureConfigs = append(ac.departureConfigs, jfk22RRunwayConfig())
	ac.departureConfigs = append(ac.departureConfigs, jfk13RRunwayConfig())
	ac.departureConfigs = append(ac.departureConfigs, jfk4LRunwayConfig())

	i4l := Approach{
		ShortName: "I4L",
		FullName:  "ILS 4 Left",
		Type:      ILSApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "AROKE", Altitude: 2000},
			Waypoint{Fix: "KRSTL", Altitude: 1500},
			Waypoint{Fix: "_JFK_4L", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(i4l); err != nil {
		lg.Errorf("%v", err)
	}

	i4r := Approach{
		ShortName: "I4R",
		FullName:  "ILS 4 Right",
		Type:      ILSApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "ZETAL", Altitude: 2000},
			Waypoint{Fix: "EBBEE", Altitude: 1500},
			Waypoint{Fix: "_JFK_4R", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(i4r); err != nil {
		lg.Errorf("%v", err)
	}

	rz4l := Approach{
		ShortName: "R4L",
		FullName:  "RNAV Zulu 4 Left",
		Type:      RNAVApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "REPRE", Altitude: 2000},
			Waypoint{Fix: "KRSTL", Altitude: 1500},
			Waypoint{Fix: "_JFK_4L", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(rz4l); err != nil {
		lg.Errorf("%v", err)
	}

	rz4r := Approach{
		ShortName: "R4R",
		FullName:  "RNAV Zulu 4 Right",
		Type:      RNAVApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "VERRU", Altitude: 2000},
			Waypoint{Fix: "EBBEE", Altitude: 1500},
			Waypoint{Fix: "_JFK_4R", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(rz4r); err != nil {
		lg.Errorf("%v", err)
	}

	i13l := Approach{
		ShortName: "I3L",
		FullName:  "ILS 13 Left",
		Type:      ILSApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "COVIR", Altitude: 3000},
			Waypoint{Fix: "KMCHI", Altitude: 2900},
			Waypoint{Fix: "BUZON", Altitude: 2900},
			Waypoint{Fix: "TELEX", Altitude: 2100},
			Waypoint{Fix: "CAXUN", Altitude: 1500},
			Waypoint{Fix: "UXHUB", Altitude: 680},
			Waypoint{Fix: "_JFK_13L", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(i13l); err != nil {
		lg.Errorf("%v", err)
	}

	rz13l := Approach{
		ShortName: "R3L",
		FullName:  "RNAV Zulu 13 Left",
		Type:      RNAVApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "ASALT", Altitude: 3000, Speed: 210},
			Waypoint{Fix: "CNRSE", Altitude: 2000},
			Waypoint{Fix: "LEISA", Altitude: 1246},
			Waypoint{Fix: "SILJY", Altitude: 835},
			Waypoint{Fix: "ROBJE", Altitude: 456},
			Waypoint{Fix: "_JFK_13L", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(rz13l); err != nil {
		lg.Errorf("%v", err)
	}

	rz13r := Approach{
		ShortName: "R3R",
		FullName:  "RNAV Zulu 13 Right",
		Type:      RNAVApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "ASALT", Altitude: 3000, Speed: 210},
			Waypoint{Fix: "NUCRI", Altitude: 2000},
			Waypoint{Fix: "PEEBO", Altitude: 921},
			Waypoint{Fix: "MAYMA", Altitude: 520},
			Waypoint{Fix: "_JFK_13R", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(rz13r); err != nil {
		lg.Errorf("%v", err)
	}

	i22l := Approach{
		ShortName: "I2L",
		FullName:  "ILS 22 Left",
		Type:      ILSApproach,
		Waypoints: [][]Waypoint{
			[]Waypoint{
				Waypoint{Fix: "CIMBL", Altitude: 14000},
				Waypoint{Fix: "HAIRR", Altitude: 14000},
				Waypoint{Fix: "CEEGL", Altitude: 10000},
				Waypoint{Fix: "TAPPR", Altitude: 8000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "ROSLY", Altitude: 3000},
				Waypoint{Fix: "ZALPO", Altitude: 1800},
				Waypoint{Fix: "_JFK_22L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "NRTON", Altitude: 10000},
				Waypoint{Fix: "SAJUL", Altitude: 10000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "ROSLY", Altitude: 3000},
				Waypoint{Fix: "ZALPO", Altitude: 1800},
				Waypoint{Fix: "_JFK_22L", Altitude: 50},
			}},
	}
	if err := ac.AddApproach(i22l); err != nil {
		lg.Errorf("%v", err)
	}

	i22r := Approach{
		ShortName: "I2R",
		FullName:  "ILS 22 Right",
		Type:      ILSApproach,
		Waypoints: [][]Waypoint{
			[]Waypoint{
				Waypoint{Fix: "CIMBL", Altitude: 14000},
				Waypoint{Fix: "HAIRR", Altitude: 14000},
				Waypoint{Fix: "CEEGL", Altitude: 10000},
				Waypoint{Fix: "TAPPR", Altitude: 8000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "CORVT", Altitude: 3000},
				Waypoint{Fix: "MATTR", Altitude: 1800},
				Waypoint{Fix: "_JFK_22R", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "NRTON", Altitude: 10000},
				Waypoint{Fix: "SAJUL", Altitude: 10000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "CORVT", Altitude: 3000},
				Waypoint{Fix: "MATTR", Altitude: 1900},
				Waypoint{Fix: "_JFK_22R", Altitude: 50},
			}},
	}
	if err := ac.AddApproach(i22r); err != nil {
		lg.Errorf("%v", err)
	}

	rz22l := Approach{
		ShortName: "R2L",
		FullName:  "RNAV Zulu 22 Left",
		Type:      RNAVApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "TUSTE", Altitude: 3000},
			Waypoint{Fix: "ZAKUS", Altitude: 1900},
			Waypoint{Fix: "JIRVA", Altitude: 1900},
			Waypoint{Fix: "SOGOE", Altitude: 1429},
			Waypoint{Fix: "HESOR", Altitude: 1019},
			Waypoint{Fix: "_JFK_22L", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(rz22l); err != nil {
		lg.Errorf("%v", err)
	}

	rz22r := Approach{
		ShortName: "R2R",
		FullName:  "RNAV Zulu 22 Right",
		Type:      RNAVApproach,
		Waypoints: [][]Waypoint{[]Waypoint{
			Waypoint{Fix: "RIVRA", Altitude: 3000},
			Waypoint{Fix: "HENEB", Altitude: 1900},
			Waypoint{Fix: "_JFK_22R", Altitude: 50},
		}},
	}
	if err := ac.AddApproach(rz22r); err != nil {
		lg.Errorf("%v", err)
	}

	i31l := Approach{
		ShortName: "I3L",
		FullName:  "ILS 31 Left",
		Type:      ILSApproach,
		Waypoints: [][]Waypoint{
			[]Waypoint{
				Waypoint{Fix: "CHANT", Altitude: 2000},
				Waypoint{Fix: "ZACHS", Altitude: 2000},
				Waypoint{Fix: "MEALS", Altitude: 1500},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "DPK", Altitude: 2000},
				Waypoint{Fix: "ZACHS", Altitude: 2000},
				Waypoint{Fix: "MEALS", Altitude: 1500},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
		},
	}
	if err := ac.AddApproach(i31l); err != nil {
		lg.Errorf("%v", err)
	}

	i31r := Approach{
		ShortName: "I3R",
		FullName:  "ILS 31 Right",
		Type:      ILSApproach,
		Waypoints: [][]Waypoint{
			[]Waypoint{
				Waypoint{Fix: "CATOD", Altitude: 3000},
				Waypoint{Fix: "MALDE", Altitude: 3000},
				Waypoint{Fix: "ZULAB", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
		},
	}
	if err := ac.AddApproach(i31r); err != nil {
		lg.Errorf("%v", err)
	}

	rz31l := Approach{
		ShortName: "R1L",
		FullName:  "RNAV Zulu 31 Left",
		Type:      RNAVApproach,
		Waypoints: [][]Waypoint{
			[]Waypoint{
				Waypoint{Fix: "SESKE"},
				Waypoint{Fix: "ZACHS", Altitude: 2000, Speed: 210},
				Waypoint{Fix: "CUVKU", Altitude: 1800},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "RISSY"},
				Waypoint{Fix: "ZACHS", Altitude: 2000, Speed: 210},
				Waypoint{Fix: "CUVKU", Altitude: 1800},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
		},
	}
	if err := ac.AddApproach(rz31l); err != nil {
		lg.Errorf("%v", err)
	}

	rz31r := Approach{
		ShortName: "R1R",
		FullName:  "RNAV Zulu 31 Right",
		Type:      RNAVApproach,
		Waypoints: [][]Waypoint{
			[]Waypoint{
				Waypoint{Fix: "PZULU"},
				Waypoint{Fix: "CATOD", Altitude: 3000, Speed: 210},
				Waypoint{Fix: "IGIDE", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "VIDIO"},
				Waypoint{Fix: "CATOD", Altitude: 3000, Speed: 210},
				Waypoint{Fix: "IGIDE", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
		},
	}
	if err := ac.AddApproach(rz31r); err != nil {
		lg.Errorf("%v", err)
	}

	camrn4 := &ArrivalConfig{
		name: "CAMRN4",
		rate: 30,
		makeSpawner: func(ac *ArrivalConfig) *AircraftSpawner {
			return &AircraftSpawner{
				rate: int(ac.rate),
				routeTemplates: []RouteTemplate{
					RouteTemplate{
						Waypoints:           "N039.46.43.120, W074.03.15.529/KARRS/@/CAMRN/#041",
						Route:               "/. CAMRN4",
						InitialAltitude:     15000,
						ClearedAltitude:     11000,
						InitialSpeed:        300,
						SpeedRestriction:    250,
						DepartureAirports:   []string{"KATL", "KFLL", "KIAD"}, // TODO
						DestinationAirports: []string{"KJFK"},
						InitialController:   "NY_F_CTR",
						Airlines:            []string{"UAL", "AAL", "DAL", "BAW"}, // TODO
						Fleet:               "default",
					},
				},
			}
		},
	}
	ac.arrivalConfigs = append(ac.arrivalConfigs, camrn4)

	lendy8 := &ArrivalConfig{
		name: "LENDY8",
		rate: 30,
		makeSpawner: func(ac *ArrivalConfig) *AircraftSpawner {
			return &AircraftSpawner{
				rate: int(ac.rate),
				routeTemplates: []RouteTemplate{
					RouteTemplate{
						Waypoints:           "N040.56.09.863, W074.30.33.013/N040.55.09.974, W074.25.19.628/@/LENDY/#135",
						Route:               "/. LENDY8",
						InitialAltitude:     20000,
						ClearedAltitude:     19000,
						InitialSpeed:        300,
						SpeedRestriction:    250,
						DepartureAirports:   []string{"KMSP", "KORD", "KDTW"}, // TODO
						DestinationAirports: []string{"KJFK"},
						InitialController:   "NY_F_CTR",
						Airlines:            []string{"UAL", "AAL", "DAL", "BAW"}, // TODO
						Fleet:               "default",
					},
				},
			}
		},
	}
	ac.arrivalConfigs = append(ac.arrivalConfigs, lendy8)

	debug := &ArrivalConfig{
		name: "DEBUG",
		rate: 30,
		makeSpawner: func(ac *ArrivalConfig) *AircraftSpawner {
			return &AircraftSpawner{
				rate: int(ac.rate),
				routeTemplates: []RouteTemplate{
					RouteTemplate{
						Waypoints:           "N040.20.22.874, W073.48.09.981/N040.21.34.834, W073.51.11.997/@/#360",
						Route:               "/. DEBUG",
						InitialAltitude:     3000,
						ClearedAltitude:     2000,
						InitialSpeed:        250,
						DepartureAirports:   []string{"KMSP", "KORD", "KDTW"}, // TODO
						DestinationAirports: []string{"KJFK"},
						InitialController:   "NY_F_CTR",
						Airlines:            []string{"UAL", "AAL", "DAL", "BAW"}, // TODO
						Fleet:               "default",
					},
				},
			}
		},
	}
	ac.arrivalConfigs = append(ac.arrivalConfigs, debug)

	parch3 := &ArrivalConfig{
		name: "PARCH3",
		rate: 30,
		makeSpawner: func(ac *ArrivalConfig) *AircraftSpawner {
			return &AircraftSpawner{
				rate: int(ac.rate),
				routeTemplates: []RouteTemplate{
					RouteTemplate{
						Waypoints:           "N041.02.38.230, W072.23.00.102/N040.57.31.959, W072.42.21.494/@/CCC/ROBER/#278",
						Route:               "/. PARCH3",
						InitialAltitude:     13000,
						ClearedAltitude:     12000,
						InitialSpeed:        275,
						SpeedRestriction:    250,
						DepartureAirports:   []string{"KBOS"}, // TODO
						DestinationAirports: []string{"KJFK"},
						InitialController:   "NY_F_CTR",
						Airlines:            []string{"UAL", "AAL", "DAL", "BAW"}, // TODO
						Fleet:               "default",
					},
				},
			}
		},
	}
	ac.arrivalConfigs = append(ac.arrivalConfigs, parch3)

	// TODO? PAWLING2 (turboprop <= 250KT)

	return ac
}

func GetFRGConfig() *AirportConfig {
	ac := &AirportConfig{name: "KFRG"}

	runways := map[string]string{
		"1":  "_FRG_1/_FRG_19/_FRG_1a/@/#013",
		"19": "_FRG_19/_FRG_1/_FRG_19a/@/#220",
		"14": "_FRG_14/_FRG_32/_FRG_14a/@/#220",
		"32": "_FRG_32/_FRG_14/_FRG_32a/@/#010",
	}

	for rwy, way := range runways {
		config := &DepartureConfig{
			name:            rwy,
			rate:            30,
			challenge:       0.5,
			categoryEnabled: make(map[string]*bool),
		}
		config.categoryEnabled["Water"] = new(bool)
		config.categoryEnabled["East"] = new(bool)
		config.categoryEnabled["Southwest"] = new(bool)
		config.categoryEnabled["North"] = new(bool)

		config.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
			rp := RouteTemplate{
				InitialAltitude:   70,
				DepartureAirports: []string{"KFRG"},
				ClearedAltitude:   5000,
				InitialController: "JFK_APP",
				Fleet:             "default",
				Airlines:          []string{"AAL", "ASA", "DAL", "EDV", "FDX", "FFT", "JBU", "NKS", "QXE", "UAL", "UPS"},
			}

			var routeTemplates []RouteTemplate

			if *config.categoryEnabled["Water"] {
				routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, way, "REP1")...)
			}
			if *config.categoryEnabled["East"] {
				routeTemplates = append(routeTemplates, jfkEast.GetRouteTemplates(rp, way, "REP1")...)
			}
			if *config.categoryEnabled["Southwest"] {
				routeTemplates = append(routeTemplates, jfkSouthwest.GetRouteTemplates(rp, way, "REP1")...)
			}
			if *config.categoryEnabled["North"] {
				routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, way, "REP1")...)
				routeTemplates = append(routeTemplates, jfkDEEZZ.GetRouteTemplates(rp, way, "REP1")...)
			}

			return &AircraftSpawner{
				rate:           int(config.rate),
				challenge:      config.challenge,
				routeTemplates: routeTemplates,
			}
		}

		ac.departureConfigs = append(ac.departureConfigs, config)
	}

	return ac
}

func GetISPConfig() *AirportConfig {
	ac := &AirportConfig{name: "KISP"}

	runways := map[string]string{
		"6":   "_ISP_6/_ISP_6a/_ISP_6b/@/#270",
		"24":  "_ISP_24/_ISP_24a/_ISP_24b/_ISP_24c/@/#275",
		"15R": "_ISP_15R/_ISP_15Ra/_ISP_15Rb/_ISP_15Rc/@/#275",
		"33L": "_ISP_33L/_ISP_33La/_ISP_33Lb/_ISP_33Lc/@/#275",
	}

	for rwy, way := range runways {
		config := &DepartureConfig{
			name:            rwy,
			rate:            20,
			challenge:       0.5,
			categoryEnabled: make(map[string]*bool),
		}
		config.categoryEnabled["North"] = new(bool)

		config.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
			rp := RouteTemplate{
				InitialAltitude:   70,
				DepartureAirports: []string{"KISP"},
				ClearedAltitude:   8000,
				InitialController: "ISP",
				Fleet:             "default",
				Airlines:          []string{"AAL", "ASA", "DAL", "EDV", "FDX", "FFT", "JBU", "NKS", "QXE", "UAL", "UPS"},
			}

			var routeTemplates []RouteTemplate

			if *config.categoryEnabled["North"] {
				routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, way, "LONGI7")...)
			}

			return &AircraftSpawner{
				rate:           int(config.rate),
				challenge:      config.challenge,
				routeTemplates: routeTemplates,
			}
		}

		ac.departureConfigs = append(ac.departureConfigs, config)
	}

	return ac
}

func GetLGAConfig() *AirportConfig {
	ac := &AirportConfig{name: "KLGA"}

	runways := map[string]string{
		"4":  "_LGA_4/_LGA_22/_LGA_22a/@/JFK",
		"22": "_LGA_22/_LGA_4/_LGA_4a/_LGA_4b/@/JFK",
		"13": "_LGA_13/_LGA_31/_LGA_31a/_LGA_31b/@/JFK",
		"31": "_LGA_31/_LGA_13/_LGA_13a/@/JFK",
	}

	for rwy, way := range runways {
		config := &DepartureConfig{
			name:            rwy,
			rate:            30,
			challenge:       0.5,
			categoryEnabled: make(map[string]*bool),
		}
		config.categoryEnabled["Water"] = new(bool)
		config.categoryEnabled["Southwest"] = new(bool)
		config.categoryEnabled["Southwest Props"] = new(bool)

		config.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
			proto := RouteTemplate{
				InitialAltitude:   70,
				DepartureAirports: []string{"KLGA"},
				InitialController: "LGA_DEP",
				Fleet:             "default",
				Airlines:          []string{"AAL", "ASA", "DAL", "EDV", "FDX", "FFT", "JBU", "NKS", "QXE", "UAL", "UPS"},
			}

			var routeTemplates []RouteTemplate

			if *config.categoryEnabled["Water"] {
				rp := proto
				rp.ClearedAltitude = 8000
				routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, way, "LGA7")...)
			}
			if *config.categoryEnabled["Southwest"] {
				rp := proto
				rp.ClearedAltitude = 6000
				routeTemplates = append(routeTemplates, jfkDIXIE.GetRouteTemplates(rp, way, "LGA7")...)
			}
			if *config.categoryEnabled["Southwest Props"] {
				// WHITE Props
				rp := proto
				rp.ClearedAltitude = 7000
				rp.Fleet = "short"
				rp.Airlines = []string{"QXE", "BWA", "FDX"}
				routeTemplates = append(routeTemplates, jfkWHITE.GetRouteTemplates(rp, way, "LGA7")...)
			}

			return &AircraftSpawner{
				rate:           int(config.rate),
				challenge:      config.challenge,
				routeTemplates: routeTemplates,
			}
		}

		ac.departureConfigs = append(ac.departureConfigs, config)
	}

	return ac
}
