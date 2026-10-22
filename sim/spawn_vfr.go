// sim/spawn_vfr.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

// vfrTerrainClearance is how far above the highest nearby terrain a VFR
// flight stays en route. It is applied here rather than built into the
// terrain data, which holds only the highest terrain within 0.5 nm, so that
// it can be tuned without rebuilding the data. Towers and antennas are not in
// that data, so the clearance has to cover them too; a real obstacle database
// would be the fix.
const vfrTerrainClearance = 1000

// Where airspace a VFR flight stays out of has its floor too low to keep
// vfrTerrainClearance under it, the flight keeps vfrShelfClearance above the
// terrain instead: about the least the regulations allow away from congested
// areas (14 CFR 91.119(c)). That applies within vfrShelfReach nm of the
// airspace, where the flight may be held down for it.
const (
	vfrShelfClearance = 500
	vfrShelfReach     = 1
)

// vfrDepartureClimbout is how far in nm from its departure runway a VFR
// flight may still be climbing away from it. The terrain data next to a
// runway can be a couple hundred feet above the field: it is rounded up, and
// the source data includes trees and buildings.
const vfrDepartureClimbout = 1

// Near its fields a VFR flight is climbing out or descending to land and
// keeps less than the full clearance above the terrain: none out to
// vfrDepartureExempt nm from its departure airport or vfrArrivalExempt nm
// from its arrival airport, then more in proportion to the distance, up to
// the full clearance at vfrDepartureRamp and vfrArrivalRamp nm.
const (
	vfrDepartureExempt = 3
	vfrDepartureRamp   = 3
	vfrArrivalExempt   = 1.5
	vfrArrivalRamp     = 5
)

// vfrClearanceScale returns the share of the full clearance above the terrain
// that a VFR flight keeps dDep nm from its departure airport and dArr nm from
// its arrival airport.
func vfrClearanceScale(dDep, dArr float32) float32 {
	ramp := func(d, exempt, full float32) float32 {
		if d <= exempt {
			return 0
		} else if d >= full {
			return 1
		}
		return (d - exempt) / (full - exempt)
	}
	return min(ramp(dDep, vfrDepartureExempt, vfrDepartureRamp), ramp(dArr, vfrArrivalExempt, vfrArrivalRamp))
}

// vfrFloor returns the lowest altitude a VFR flight from depap to arrap may
// be at p. It returns false on the departure runway and within
// vfrDepartureClimbout of it, where the flight is still taking off or
// climbing out and nothing is required. Elsewhere it keeps the share of the
// clearance vfrClearance gives that vfrClearanceScale allows. Near the
// fields, nothing keeps it clear of towers and antennas, which aren't in the
// terrain data.
func (s *Sim) vfrFloor(p math.Point2LL, depap, arrap db.Airport) (int, bool) {
	takeoff, nmPerLongitude := s.vfrTakeoff(depap), s.State.NmPerLongitude
	if math.PointSegmentDistance(math.LL2NM(p, nmPerLongitude), math.LL2NM(takeoff[0], nmPerLongitude),
		math.LL2NM(takeoff[1], nmPerLongitude)) < vfrDepartureClimbout {
		return 0, false
	}
	terrain := s.terrain.HighestTerrain(p)
	scale := vfrClearanceScale(math.NMDistance2LL(p, depap.Location), math.NMDistance2LL(p, arrap.Location))
	if scale == 0 {
		return terrain, true
	}
	return terrain + int(scale*float32(s.vfrClearance(p, terrain))), true
}

// vfrClearance returns how far above the terrain at p a VFR flight stays en
// route: vfrShelfClearance near a shelf of airspace whose floor is above the
// terrain but too low for vfrTerrainClearance, and vfrTerrainClearance
// elsewhere.
func (s *Sim) vfrClearance(p math.Point2LL, terrain int) int {
	if s.vfrAirspace.AnyNear(p, vfrShelfReach, func(v *av.AirspaceVolume) bool {
		return v.Floor > terrain && v.Floor-vfrShelfBuffer < terrain+vfrTerrainClearance
	}) {
		return vfrShelfClearance
	}
	return vfrTerrainClearance
}

// vfrTakeoff returns the runway VFR flights take off from at ap, from the
// start of the takeoff roll to its far end, or ap's location at both ends if
// there is none. Like the runway, it doesn't change during the sim, so it is
// worked out once per airport.
func (s *Sim) vfrTakeoff(ap db.Airport) [2]math.Point2LL {
	if t, ok := s.vfrTakeoffs[ap.Id]; ok {
		return t
	}
	t := [2]math.Point2LL{ap.Location, ap.Location}
	if rwy, opp, ok := s.vfrRunway(ap.Id); ok {
		t = [2]math.Point2LL{rwy.Start, opp.Start}
	}
	s.vfrTakeoffs[ap.Id] = t
	return t
}

// A VFR flight under airspace it avoids stays vfrShelfBuffer below its
// floor and then drops to the next vfrShelfIncrement, which under the usual
// 1200' and 3000' Class B shelves gives 1000' and 2500' -- what pilots fly
// there. Scud running under a shelf is ordinary VFR practice; brushing its
// floor is not, and neither is squeezing through less than minVFRShelfRoom of
// air between a field and the airspace over it. A flight passing over such
// airspace stays at least vfrShelfBuffer above its ceiling.
const (
	vfrShelfBuffer    = 200
	vfrShelfIncrement = 500
	minVFRShelfRoom   = 500
)

// Max altitude for VFR aircraft (below Class A airspace at 18,000')
const maxVFRAltitude = 17500

// maxHoldingShortVFR bounds how many VFR departures wait for the runway at a
// time; no more are spawned until the backlog clears.
const maxHoldingShortVFR = 5

// errNoVFRDestination is returned when arrivals are backed up at every
// airport that takes VFR traffic, leaving nowhere to send a VFR departure
// at the moment.
var errNoVFRDestination = errors.New("no VFR destination airport is accepting arrivals")

// spawnVFRDepartures spawns rate-based VFR departures. VFR traffic isn't part
// of the pregenerated schedule: its destinations depend on live arrival
// congestion.
func (s *Sim) spawnVFRDepartures() {
	if s.State.LaunchConfig.DepartureMode != LaunchAutomatic {
		return
	}
	now := s.State.SimTime

	for airport, runways := range util.SortedMap(s.DepartureState) {
		for runway, depState := range util.SortedMap(runways) {
			if now.After(depState.NextVFRSpawn) {
				ac, err := s.makeNewVFRDeparture(airport, runway)
				launched := ac != nil && err == nil
				if launched {
					s.addDepartureToPool(ac, runway, 0 /* no wait at the gate */)
				}
				// Also skip the slot if there was nowhere to send the
				// aircraft; otherwise we'd try again every second for as
				// long as arrivals are backed up.
				if launched || errors.Is(err, errNoVFRDestination) {
					depState.NextVFRSpawn = now.Add(randomWait(depState.VFRSpawnRate, false, s.Rand))
				}
			}
		}
	}
}

func (s *Sim) makeNewVFRDeparture(depart av.ICAOAirportCode, runway av.RunwayID) (*Aircraft, error) {
	depState := s.DepartureState[depart][runway]
	if len(depState.ReleasedVFR) >= maxHoldingShortVFR || len(depState.ReleasedIFR) >= maxHoldingShort {
		// There's a backup; hold off on more.
		return nil, nil
	}

	if depState.VFRSpawnRate == 0 {
		return nil, nil
	}

	// Don't waste time trying to find a valid launch if it's been
	// near-impossible to find valid routes.
	if depState.VFRAttempts >= 400 &&
		(depState.VFRSuccesses == 0 || depState.VFRAttempts/depState.VFRSuccesses >= 200) {
		return nil, nil
	}

	randoms, route := s.sampleVFRFlight(s.State.Airports[depart])
	if randoms == nil && route == nil {
		// Nothing with a nonzero rate to sample from.
		return nil, nil
	}

	if route != nil && s.orbitingArrivals(route.Destination) > 0 {
		// Arrivals are backed up at the route's destination; hold off
		// on this one and try again later.
		return nil, errNoVFRDestination
	}

	// The candidates a destination is sampled from don't change over the
	// attempts below: a failed one leaves no trace in the sim and a
	// successful one returns before another sample is drawn.
	var destinations []av.ICAOAirportCode
	var destinationWeights map[av.ICAOAirportCode]float32
	if randoms != nil {
		destinations = util.SortedMapKeys(s.State.DepartureAirports)
		destinationWeights = s.vfrDestinationWeights()
	}
	callsigns := s.currentCallsigns()

	var err error
	for range 5 {
		var arrive av.ICAOAirportCode
		var fleet string
		var routeWps []av.Waypoint
		if randoms != nil {
			// Sample destination airport: may be where we started from.
			dest, ok := rand.SampleWeightedSeq(s.Rand, slices.Values(destinations),
				func(ap av.ICAOAirportCode) float32 { return destinationWeights[ap] })
			if !ok {
				// Arrivals are backed up at every airport that takes
				// VFR traffic; wait for one of them to clear.
				return nil, errNoVFRDestination
			}
			arrive, fleet = dest, randoms.Fleet
		} else {
			arrive, fleet, routeWps = route.Destination, route.Fleet, route.Waypoints
		}

		// Only count attempts where we actually went looking for a
		// route; the circuit breaker above is about routes that can't
		// be found, not about destinations being busy.
		depState.VFRAttempts++
		var ac *Aircraft
		if ac, err = s.createUncontrolledVFRDeparture(depart, arrive, fleet, routeWps, callsigns); err == nil {
			ac.ReleaseTime = s.State.SimTime
			depState.VFRSuccesses++
			return ac, nil
		}
	}
	return nil, err
}

// sampleVFRFlight chooses between the airport's random VFR flights and its
// VFR routes in proportion to their rates. It returns nil for both if none of
// them has a nonzero rate.
func (s *Sim) sampleVFRFlight(ap *av.Airport) (*av.VFRRandomsSpec, *av.VFRRouteSpec) {
	var rateSum float32
	var randoms *av.VFRRandomsSpec
	var route *av.VFRRouteSpec
	if ap.VFR.Randoms.Rate > 0 {
		rateSum = ap.VFR.Randoms.Rate
		randoms = &ap.VFR.Randoms
	}
	for i, r := range ap.VFR.Routes {
		if r.Rate > 0 {
			rateSum += r.Rate
			if s.Rand.Float32() < r.Rate/rateSum {
				randoms, route = nil, &ap.VFR.Routes[i]
			}
		}
	}
	return randoms, route
}

// sampleVFRDeparture samples a VFR departure from the given airport for a
// manual launch slot. Note that it may fail without an error if it's having
// trouble finding a route.
func (s *Sim) sampleVFRDeparture(departureAirport av.ICAOAirportCode) (*Aircraft, error) {
	ap, ok := s.State.Airports[departureAirport]
	if !ok {
		// This shouldn't happen...
		return nil, nil
	}
	randoms, route := s.sampleVFRFlight(ap)
	if route != nil {
		return s.createUncontrolledVFRDeparture(departureAirport, route.Destination, route.Fleet,
			route.Waypoints, s.currentCallsigns())
	}
	if randoms == nil {
		// This shouldn't happen either...
		return nil, nil
	}

	// Sample destination airport: may be where we started from.
	weights := s.vfrDestinationWeights()
	arrive, ok := rand.SampleWeightedSeq(s.Rand, slices.Values(util.SortedMapKeys(s.State.DepartureAirports)),
		func(ap av.ICAOAirportCode) float32 { return weights[ap] })
	if !ok {
		// Arrivals are backed up everywhere, but a controller asked for this
		// aircraft, so send it somewhere anyway.
		arrive, ok = rand.SampleWeightedSeq(s.Rand, slices.Values(util.SortedMapKeys(s.State.DepartureAirports)),
			func(ap av.ICAOAirportCode) float32 { return s.State.Airports[ap].VFRRateSum() })
		if !ok {
			return nil, nil
		}
	}

	return s.createUncontrolledVFRDeparture(departureAirport, arrive, randoms.Fleet, nil, s.currentCallsigns())
}

// vfrDestinationWeights gives the weight for sampling each airport as the
// destination of a random VFR departure. Airports where arrivals are already
// backed up waiting to land weigh nothing, so that we don't keep adding to
// the pile. Which those are takes a pass over every aircraft in the sim, so
// they are counted once for all the airports rather than once per airport.
func (s *Sim) vfrDestinationWeights() map[av.ICAOAirportCode]float32 {
	orbiting := make(map[av.ICAOAirportCode]int)
	for _, ac := range s.Aircraft {
		if isHoldingArrival(ac) {
			orbiting[ac.ArrivalAirport]++
		}
	}

	weights := make(map[av.ICAOAirportCode]float32, len(s.State.DepartureAirports))
	for ap := range s.State.DepartureAirports {
		if orbiting[ap] == 0 {
			weights[ap] = s.State.Airports[ap].VFRRateSum()
		}
	}
	return weights
}

// vfrDownwindOffset is how far to the side of the runway a departure's
// downwind runs for a light aircraft; faster types fly it proportionally
// wider. vfrClimboutSpeed is the speed the turn onto it is flown at, which
// below 10,000' is the 250 knot limit for anything that can reach it.
const vfrDownwindOffset = 1.5

func vfrClimboutSpeed(perf av.AircraftPerformance) float32 {
	return min(250, perf.Speed.CruiseTAS)
}

func (s *Sim) createUncontrolledVFRDeparture(depart, arrive av.ICAOAirportCode, fleet string, routeWps []av.Waypoint,
	callsigns []av.ADSBCallsign) (*Aircraft, error) {
	simTime := s.State.SimTime
	depap, arrap := db.DB.Airports[depart], db.DB.Airports[arrive]
	rwy, _, ok := s.vfrRunway(depart)
	if !ok {
		return nil, fmt.Errorf("%s: unable to find VFR runway", depart)
	}

	// Nothing else about the candidate matters if there is no room to get
	// into the pattern at the far end without entering the airspace over it.
	terminalCeiling, ok := s.vfrTerminalCeiling(arrap)
	if !ok {
		return nil, ErrViolatedAirspace
	}

	ac := s.sampleAircraft(av.AirlineSpecifier{ICAO: "N", Fleet: fleet}, depart, arrive, callsigns, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightRules = av.FlightRulesVFR
	ac.Squawk = 0o1200
	if r := s.Rand.Float32(); r < .02 {
		ac.Mode = av.TransponderModeOn // mode-A
	} else if r < .03 {
		ac.Mode = av.TransponderModeStandby // flat out off
	}

	perf, ok := db.DB.AircraftPerformance[ac.AircraftType]
	if !ok {
		return nil, fmt.Errorf("invalid aircraft type: no performance data %q", ac.AircraftType)
	}

	dist := math.NMDistance2LL(depap.Location, arrap.Location)

	mid := math.Mid2f(depap.Location, arrap.Location)
	if arrive == depart {
		dist = float32(s.Rand.IntRange(10, 30))
		// Bias heading to within ±90° of the departure runway heading so
		// the aircraft flies away from the airport before sightseeing,
		// rather than immediately looping back over the field.
		hdg := rwy.Heading + math.MagneticHeading(s.Rand.IntRange(-90, 90))
		v := [2]float32{dist * math.Sin(math.Radians(hdg)), dist * math.Cos(math.Radians(hdg))}
		dnm := math.LL2NM(depap.Location, s.State.NmPerLongitude)
		midnm := math.Add2f(dnm, v)
		mid = math.NM2LL(midnm, s.State.NmPerLongitude)
	}

	// This should be sufficient capacity to avoid reallocations / recopying in the following.
	wps := make([]av.Waypoint, 0, 20)

	wps = append(wps, av.Waypoint{Fix: "_dep_start", Location: rwy.Start})
	opp := math.Offset2LL(rwy.Start, math.MagneticToTrue(rwy.Heading, s.State.MagneticVariation), 1 /* nm */, s.State.NmPerLongitude)
	wps = append(wps, av.Waypoint{Fix: "_opp", Location: opp})

	rg := av.MakeRouteGenerator(rwy.Start, opp, s.State.NmPerLongitude)
	wp0 := rg.Waypoint("_dep_climb", 3, 0)
	wps = append(wps, wp0)

	// Fly a downwind if needed
	var hdg math.TrueHeading
	if len(routeWps) > 0 {
		hdg = math.Heading2LL(opp, routeWps[0].Location, s.State.NmPerLongitude)
	} else {
		hdg = math.Heading2LL(opp, mid, s.State.NmPerLongitude)
	}
	turn := math.HeadingSignedTurn(math.MagneticToTrue(rwy.Heading, s.State.MagneticVariation), hdg)
	if turn < -120 || turn > 120 {
		// Reversing onto a downwind takes twice the turn radius of lateral
		// room, which the light-aircraft spacing below doesn't give a fast
		// one: it would circle the downwind fix without ever reaching it.
		// Widen the whole leg for those rather than narrow it for everyone.
		k := max(1, 2*nav.TurnRadius(perf, vfrClimboutSpeed(perf))/vfrDownwindOffset)
		side := util.Select(turn < 0, k, -k)
		wps = append(wps, rg.Waypoint("_dep_downwind1", k, side*vfrDownwindOffset))
		wps = append(wps, rg.Waypoint("_dep_downwind2", 0, side*vfrDownwindOffset))
		wps = append(wps, rg.Waypoint("_dep_downwind3", -2*k, side*vfrDownwindOffset))
	}

	// Cruise above the terrain on the way, as far as the route is known yet.
	planned := []av.Waypoint{{Location: depap.Location}}
	if len(routeWps) > 0 {
		planned = append(planned, routeWps...)
	} else {
		planned = append(planned, av.Waypoint{Location: mid})
	}
	planned = append(planned, av.Waypoint{Location: arrap.Location})
	limits := CruiseLimits{Floor: slices.Max(s.vfrFloors(sampleRoute(planned), depap, arrap))}
	ac.CruiseAltitude = ac.FiledCruiseAltitude(perf, limits, s.State.NmPerLongitude,
		s.State.MagneticVariation, s.Rand)

	var randomizeAltitudeRange bool
	if len(routeWps) > 0 {
		wps = append(wps, routeWps...)
		randomizeAltitudeRange = true
	} else {
		randomizeAltitudeRange = false
		depEnd := wps[len(wps)-1].Location

		radius := .15 * dist

		airwork := func() bool {
			if depart == arrive {
				return s.Rand.Intn(3) == 0
			}
			return s.Rand.Intn(10) == 0
		}()

		const nsteps = 10
		for i := 1; i < nsteps-1; i++ { // skip first one and last one
			t := float32(i) / nsteps

			pt := func() math.Point2LL {
				if i <= nsteps/2 {
					return math.Lerp2f(2*t, depEnd, mid)
				} else {
					return math.Lerp2f(2*t-1, mid, arrap.Location)
				}
			}()

			var ar av.AltitudeRestriction
			alt := float32(ac.CruiseAltitude)
			if i < nsteps/2 {
				// At or above for the first half, even if unattainable so that they climb
				ar = av.MakeAtOrAboveAltitudeRestriction(alt)
			} else if i < nsteps-2 {
				// at or below to be able to start descending
				ar = av.MakeAtOrBelowAltitudeRestriction(alt)
			} else {
				// Last one: down to circuit height and under anything over
				// the field, so that the arrival maneuvering that follows --
				// the pattern entry, or an orbit if the pattern is full --
				// happens below the airspace rather than descending through
				// it. Those waypoints are built when the aircraft gets here,
				// long after this route was checked, so this is what keeps
				// them clear.
				high := float32(min(arrap.Elevation+2000, terminalCeiling))
				ar = av.MakeRangeAltitudeRestriction(min(float32(arrap.Elevation+1500), high), high)
			}

			wp := av.Waypoint{
				Fix:      "_route" + strconv.Itoa(i),
				Location: pt,
			}
			wp.SetAltitudeRestriction(ar)
			wp.InitExtra().Radius = util.Select(i <= 1, 0.2*radius, radius)
			wps = append(wps, wp)

			if airwork && i == nsteps/2 {
				w := &wps[len(wps)-1]
				extra := w.InitExtra()
				extra.AirworkRadius = int8(s.Rand.IntRange(4, 8))
				extra.AirworkMinutes = int8(s.Rand.IntRange(5, 20))
				// Airwork dives and descending turns go down to the floor,
				// so it must stay well clear of the ground; this waypoint's
				// "at or below" floor of 0 is no floor at all.
				fieldClearance := float32(max(depap.Elevation, arrap.Elevation) + 1000)
				w.AltRestriction.Range[0] = min(alt, max(alt-500, fieldClearance))
				w.AltRestriction.Range[1] = min(w.AltRestriction.Range[1]+2000, maxVFRAltitude)
			}
		}
	}

	wps[len(wps)-1].SetSequenceVFRLanding(true)

	if err := ac.InitializeVFRDeparture(s.State.Airports[depart], wps, randomizeAltitudeRange,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.wxModel, simTime, s.Rand, s.lg); err != nil {
		return nil, err
	}

	// Only now is the route the one the aircraft will fly: building the Nav
	// scatters the waypoints within their radii. Detours around airspace
	// start from that route, and the terrain and shelf legs have to be
	// sampled along it rather than the one they were planned on, both so the
	// airspace and terrain they clear are what is overflown and so they land
	// on the legs they divide rather than off to one side of them.
	ac.Nav.Waypoints, ok = s.detourAroundAirspace(ac.Nav.Waypoints, ac.CruiseAltitude, depap, arrap)
	if !ok {
		return nil, ErrViolatedAirspace
	}
	// Where the terrain and a shelf meet, the shelf wins, which
	// adjustRouteForShelves only allows if it leaves the flight above the
	// terrain's floor.
	ac.Nav.Waypoints = s.adjustRouteForTerrain(ac.Nav.Waypoints, depap, arrap)
	ac.Nav.Waypoints, ok = s.adjustRouteForShelves(ac.Nav.Waypoints, ac.CruiseAltitude, depap, arrap)
	if !ok {
		return nil, ErrViolatedAirspace
	}

	// Deep-copy only Nav (not the full Aircraft) to avoid copying
	// maps, pointers, and fields unused during route validation.
	simNav := deep.MustCopy(ac.Nav)
	simNav.Prespawn = true
	prespawnWxs := s.wxModel.Lookup(simNav.FlightState.Position,
		simNav.FlightState.Altitude, simTime.Time())
	for i := range 3 * 60 * 60 { // limit to 3 hours of sim time, just in case
		if wp := simNav.UpdateWithWeather("", prespawnWxs, nil, ac.FlightRules,
			simTime.NavTime(), nil).PassedWaypoint; wp != nil {
			if wp.HasDeleteAction() {
				return ac, nil
			}
			if wp.SequenceVFRLanding() {
				// Generate descent waypoints so prespawn validates the
				// descent from cruise altitude through any airspace down to
				// pattern altitude.
				arrAP, ok := db.DB.Airports[ac.ArrivalAirport]
				if !ok {
					return ac, nil
				}
				patternAlt := float32(arrAP.Elevation + vfrPatternAltitude)
				pos := simNav.FlightState.Position
				alt := simNav.FlightState.Altitude
				dest := arrAP.Location
				mid := math.Point2LL(math.Lerp2f(0.5, pos, dest))
				midAlt := (alt + patternAlt) / 2

				var descentWps []av.Waypoint
				descentWps = append(descentWps, av.Waypoint{
					Fix:      "_descent_mid",
					Location: mid,
				})
				descentWps[0].SetAltitudeRestriction(av.MakeAtAltitudeRestriction(midAlt))
				descentWps[0].SetSpeedRestriction(av.MakeAtSpeedRestriction(90))

				endWp := av.Waypoint{
					Fix:      "_descent_end",
					Location: dest,
				}
				endWp.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(patternAlt))
				endWp.SetSpeedRestriction(av.MakeAtSpeedRestriction(70))
				endWp.MergeActions(av.WaypointActions{Delete: true})
				descentWps = append(descentWps, endWp)

				// It stays as high as the terrain asks, but it never climbs:
				// the flight is already under the airspace it has to stay
				// out of.
				start := av.Waypoint{Fix: "_descent_start", Location: pos}
				descentWps = s.adjustRouteForTerrain(append([]av.Waypoint{start}, descentWps...), depap, arrap)[1:]
				for i := range descentWps {
					if ar := descentWps[i].AltitudeRestriction(); ar != nil {
						ar.Range[0] = min(ar.Range[0], alt)
					}
				}
				simNav.Waypoints = descentWps
				simNav.Heading = nav.Heading{}
				continue
			}
		}

		if (i % 4) != 0 {
			continue
		}
		pos := simNav.FlightState.Position
		alt := int(simNav.FlightState.Altitude)
		if s.vfrAirspace.Inside(pos, alt) || s.State.FacilityAdaptation.Filters.VFRInhibit.Inside(pos, alt) {
			return nil, ErrViolatedAirspace
		}
		if floor, ok := s.vfrFloor(pos, depap, arrap); ok && simNav.FlightState.Altitude < float32(floor) {
			var wpName string
			if len(simNav.Waypoints) > 0 {
				wpName = simNav.Waypoints[0].Fix
			}
			nav.NavLog(string(ac.ADSBCallsign), simTime.NavTime(), "state",
				"rejected at %.0f' (terrain %d', need %d') heading to %q, pos %v, %.1fnm from dep, %.1fnm from arr",
				simNav.FlightState.Altitude, s.terrain.HighestTerrain(pos), floor, wpName, pos,
				math.NMDistance2LL(pos, depap.Location), math.NMDistance2LL(pos, arrap.Location))
			return nil, ErrVFRBelowTerrain
		}
	}

	return nil, ErrVFRSimTookTooLong
}

// vfrPatternAltitude is how far above the field a VFR aircraft flies the
// pattern. vfrPatternMinRoom is the least room it can be squeezed into: below
// that the airspace is down around the base and final legs and there is no
// pattern left to fly.
const vfrPatternAltitude = 1000

const vfrPatternMinRoom = 500

// vfrTerminalCeiling returns the highest altitude a VFR arrival can use while
// maneuvering at ap and stay clear of the airspace VFR flights avoid. It
// reports false where there is no room above the pattern to do that:
// entering such a field means entering the airspace, which is not something
// we fly, so no VFR is sent there. The answer depends only on the airspace
// and the field, so it is worked out once per airport.
func (s *Sim) vfrTerminalCeiling(ap db.Airport) (int, bool) {
	if alt, ok := s.vfrTerminalAlts[ap.Id]; ok {
		return alt, alt > 0
	}

	// Arrivals maneuver at every altitude from the pattern up to the
	// ceiling, so they stay under the airspace over where they fly rather
	// than passing over it as a route may.
	ceiling := util.ReduceMin(slices.Values(s.vfrArrivalAirspace(ap)), maxVFRAltitude, func(vol *av.AirspaceVolume) int {
		return underFloor(vol.Floor)
	})

	if ceiling < ap.Elevation+vfrPatternMinRoom {
		ceiling = 0
	}
	s.vfrTerminalAlts[ap.Id] = ceiling
	return ceiling, ceiling > 0
}

// vfrArrivalAirspace returns the avoided airspace over where VFR arrivals fly
// at ap: the traffic pattern for its VFR runway, the 45-degree entry and
// straight-in that join it, the orbits they hold in while it is full, and
// the connections from those orbits to the entries.
// Airspace beside the field leaves room for its traffic on the other side.
func (s *Sim) vfrArrivalAirspace(ap db.Airport) []*av.AirspaceVolume {
	rwy, opp, ok := s.vfrRunway(ap.Id)
	if !ok {
		return nil
	}
	nmPerLongitude, magneticVariation := s.State.NmPerLongitude, s.State.MagneticVariation

	rightHdg := math.MagneticToTrue(rwy.Heading, magneticVariation) + 90
	orbit := math.Offset2LL(rwy.Threshold, rightHdg, vfrOrbitOffset, nmPerLongitude)
	orbitRadius := float32(vfrOrbitRadius + vfrOrbitScatter*math.Sqrt2)
	vols := s.vfrAirspace.Near(orbit, orbitRadius)
	include := func(more []*av.AirspaceVolume) {
		for _, vol := range more {
			if !slices.Contains(vols, vol) {
				vols = append(vols, vol)
			}
		}
	}

	entries := [][]av.Waypoint{
		generatePatternEntryWaypoints(rwy, opp, ap.Elevation, maxVFRAltitude, nmPerLongitude, magneticVariation),
		generateStraightInWaypoints(rwy, ap.Elevation, maxVFRAltitude, nmPerLongitude, magneticVariation),
	}
	for _, wps := range entries {
		// An arrival can leave any point of its scattered orbit for the
		// first entry waypoint. These segments fill a disk that shrinks
		// from orbitRadius at the orbit center to zero at the entry.
		// Cover each interval with a disk at its midpoint, using the
		// interval's largest radius plus half its length so there are no
		// gaps. The 0.25nm spacing keeps Near's radius below its 2nm limit.
		entry := wps[0].Location
		distance := math.NMDistance2LL(orbit, entry)
		n := max(1, int(math.Ceil(distance/0.25)))
		for i := range n {
			p := math.Point2LL(math.Lerp2f((float32(i)+0.5)/float32(n), orbit, entry))
			radius := (1-float32(i)/float32(n))*orbitRadius + distance/(2*float32(n))
			include(s.vfrAirspace.Near(p, radius))
		}
	}
	for _, wps := range append(entries,
		generatePatternLap(rwy, opp, ap.Elevation, maxVFRAltitude, nmPerLongitude, magneticVariation)) {
		for i := 1; i < len(wps); i++ {
			include(s.vfrAirspace.Crossed(wps[i-1].Location, wps[i].Location))
		}
	}
	return vols
}

// underFloor returns the altitude a VFR flight flies under airspace with the
// given floor.
func underFloor(floor int) int {
	return (floor - vfrShelfBuffer) / vfrShelfIncrement * vfrShelfIncrement
}

// clearAltitude returns the highest altitude at or below alt at which a VFR
// flight is clear of the given volumes, treating them as if they were
// stacked over one point. The flight passes over the volumes whose ceilings
// are below it and that passOver allows, and under the rest. It returns zero
// if there is no such altitude above the ground.
func clearAltitude(vols []*av.AirspaceVolume, alt int, passOver func(*av.AirspaceVolume) bool) int {
	for alt > 0 {
		// Drop only as far as the highest floor in the way requires.
		floor, blocked := 0, false
		for _, v := range vols {
			inWay := alt > v.Floor-vfrShelfBuffer && (alt-vfrShelfBuffer < v.Ceiling || !passOver(v))
			if inWay && (!blocked || v.Floor > floor) {
				floor, blocked = v.Floor, true
			}
		}
		if !blocked {
			break
		}
		alt = underFloor(floor)
	}
	return alt
}

// vfrClimbGradient is how many feet a VFR flight is assumed to climb in each
// nm. It is a low estimate, below what light aircraft manage (about 550 for a
// C172), so that slow climbers can do what is asked of them. It is used to
// decide whether a flight near its departure or destination is high enough to
// pass over airspace, and how far ahead of rising terrain a flight has to
// start climbing.
const vfrClimbGradient = 300

// vfrPassOver returns a function that reports whether a VFR flight between
// depap and arrap is sure to be high enough to pass over a volume, given
// vfrClimbGradient: whether everywhere over it the flight is at least
// vfrShelfBuffer above its ceiling. The flight goes under or around a volume
// it may not have climbed above, rather than climbing through it.
func vfrPassOver(depap, arrap db.Airport, cruise int) func(*av.AirspaceVolume) bool {
	return func(v *av.AirspaceVolume) bool {
		lowest := func(ap db.Airport) float32 {
			d := math.NMDistance2LL(v.PolygonBounds.ClosestPointInBox(ap.Location), ap.Location)
			return float32(ap.Elevation) + vfrClimbGradient*d
		}
		low := int(min(float32(cruise), lowest(depap), lowest(arrap)))
		return low-vfrShelfBuffer >= v.Ceiling
	}
}

// routePoint is a point along a VFR route.
type routePoint struct {
	loc math.Point2LL
	wp  int // index in the route's waypoints, or -1 for a point sampled along a leg
}

// sampleRoute returns the waypoints of a route, with points about a mile
// apart along the legs between them.
func sampleRoute(wps []av.Waypoint) []routePoint {
	pts := []routePoint{{wps[0].Location, 0}}
	for i := 1; i < len(wps); i++ {
		for p := range legSamples(wps[i-1].Location, wps[i].Location) {
			pts = append(pts, routePoint{p, -1})
		}
		pts = append(pts, routePoint{wps[i].Location, i})
	}
	return pts
}

// legSamples yields points about a mile apart along the leg from a to b,
// excluding both ends.
func legSamples(a, b math.Point2LL) iter.Seq[math.Point2LL] {
	return func(yield func(math.Point2LL) bool) {
		n := max(1, int(math.NMDistance2LL(a, b)+0.5))
		for i := range n {
			if !yield(math.Lerp2f(float32(i+1)/float32(n+1), a, b)) {
				return
			}
		}
	}
}

// A VFR flight goes around airspace that it can neither pass over nor fly
// under by following a polygon with vfrDetourSides sides that stays
// vfrDetourMargin nm from it. If going around would make its route more than
// vfrMaxDetour times as long, it goes somewhere else instead.
const (
	vfrDetourSides  = 16
	vfrDetourMargin = 1
	vfrMaxDetour    = 1.5
)

// detourAroundAirspace routes a VFR flight around airspace that it avoids
// and can neither fly under nor pass over. Where a leg runs into such
// airspace, the waypoints inside a detourPolygon around it are replaced with
// the polygon's vertices along the shorter way around. If that path runs
// into more airspace, the polygon grows to take it in and the path is found
// again. It reports false if there is no way around: the polygon takes in the
// departure runway or the route's last waypoint, airspace blocks the way from
// that waypoint to the field, or the detours make the route too long.
func (s *Sim) detourAroundAirspace(wps []av.Waypoint, cruise int, depap, arrap db.Airport) ([]av.Waypoint, bool) {
	nmPerLongitude := s.State.NmPerLongitude
	passOver := vfrPassOver(depap, arrap, cruise)
	// inWay returns the volumes along the leg from a to b that the flight
	// must go around. It treats them as if they were stacked over one point;
	// if that leaves no room to fly under them, every volume below the
	// cruise altitude is in the way. The room is measured above the higher
	// of the two fields: a flight held lower than that seldom gets down in
	// time.
	minFloor := max(depap.Elevation, arrap.Elevation) + minVFRShelfRoom
	inWay := func(a, b math.Point2LL) []*av.AirspaceVolume {
		vols := s.vfrAirspace.Crossed(a, b)
		if clearAltitude(vols, cruise, passOver) >= minFloor {
			return nil
		}
		return util.FilterSlice(vols, func(v *av.AirspaceVolume) bool { return v.Floor-vfrShelfBuffer < cruise })
	}
	pathLength := func(path []math.Point2LL) float32 {
		var l float32
		for i := 1; i < len(path); i++ {
			l += math.NMDistance2LL(path[i-1], path[i])
		}
		return l
	}
	routeLength := func(wps []av.Waypoint) float32 {
		return pathLength(util.MapSlice(wps, func(wp av.Waypoint) math.Point2LL { return wp.Location }))
	}
	maxLength := vfrMaxDetour * routeLength(wps)

	detourWpNum := 0
	for i := 0; i+1 < len(wps); i++ {
		blocking := inWay(wps[i].Location, wps[i+1].Location)
		if len(blocking) == 0 {
			continue
		}

		poly := makeDetourPolygon(blocking, nmPerLongitude)
		for {
			start, end := i, i+1
			inside := func(j int) bool { return poly.contains(math.LL2NM(wps[j].Location, nmPerLongitude)) }
			for start > 0 && inside(start) {
				start--
			}
			for end < len(wps) && inside(end) {
				end++
			}
			if start == 0 || end == len(wps) {
				return nil, false
			}

			a, b := wps[start].Location, wps[end].Location
			path := []math.Point2LL{a}
			if anm, bnm := math.LL2NM(a, nmPerLongitude), math.LL2NM(b, nmPerLongitude); poly.intersects(anm, bnm) {
				for _, p := range poly.around(anm, bnm) {
					path = append(path, math.NM2LL(p, nmPerLongitude))
				}
			}
			path = append(path, b)
			if pathLength(path) > maxLength {
				return nil, false
			}

			var more []*av.AirspaceVolume
			for j := 1; j < len(path); j++ {
				for _, v := range inWay(path[j-1], path[j]) {
					if !slices.Contains(blocking, v) && !slices.Contains(more, v) {
						more = append(more, v)
					}
				}
			}
			if len(more) > 0 {
				blocking = append(blocking, more...)
				poly = makeDetourPolygon(blocking, nmPerLongitude)
				continue
			}

			detour := util.MapSlice(path[1:len(path)-1], func(p math.Point2LL) av.Waypoint {
				detourWpNum++
				return av.Waypoint{Fix: "_detour" + strconv.Itoa(detourWpNum), Location: p}
			})
			wps = slices.Concat(wps[:start+1], detour, wps[end:])
			i = start + len(detour)
			break
		}
	}
	if len(inWay(wps[len(wps)-1].Location, arrap.Location)) > 0 {
		return nil, false
	}
	return wps, routeLength(wps) <= maxLength
}

// vfrDetourNormals are the outward normals of the sides of a detourPolygon,
// counterclockwise.
var vfrDetourNormals = func() [vfrDetourSides][2]float32 {
	var n [vfrDetourSides][2]float32
	for k := range n {
		a := math.Radians(float32(k) * 360 / vfrDetourSides)
		n[k] = [2]float32{math.Cos(a), math.Sin(a)}
	}
	return n
}()

// detourPolygon is a convex polygon, in nm, around airspace a VFR flight
// detours around. Each side is perpendicular to one of vfrDetourNormals and
// lies vfrDetourMargin past the farthest the airspace reaches in that
// direction.
type detourPolygon [vfrDetourSides]float32

func makeDetourPolygon(vols []*av.AirspaceVolume, nmPerLongitude float32) detourPolygon {
	var d detourPolygon
	first := true
	for _, vol := range vols {
		for _, v := range vol.Vertices {
			p := math.LL2NM(v, nmPerLongitude)
			for k, n := range vfrDetourNormals {
				if reach := math.Dot(p, n) + vfrDetourMargin; first || reach > d[k] {
					d[k] = reach
				}
			}
			first = false
		}
	}
	return d
}

// sees reports whether side k faces p: p lies outside the line along it.
func (d detourPolygon) sees(p [2]float32, k int) bool {
	k = (k + vfrDetourSides) % vfrDetourSides
	return math.Dot(p, vfrDetourNormals[k]) >= d[k]
}

func (d detourPolygon) contains(p [2]float32) bool {
	for k := range d {
		if d.sees(p, k) {
			return false
		}
	}
	return true
}

// vertex returns the vertex where side k meets side k+1.
func (d detourPolygon) vertex(k int) [2]float32 {
	k1 := (k + 1) % vfrDetourSides
	n0, n1 := vfrDetourNormals[k], vfrDetourNormals[k1]
	det := n0[0]*n1[1] - n0[1]*n1[0]
	return [2]float32{(d[k]*n1[1] - n0[1]*d[k1]) / det, (n0[0]*d[k1] - d[k]*n1[0]) / det}
}

// intersects reports whether the segment from a to b passes through the
// polygon.
func (d detourPolygon) intersects(a, b [2]float32) bool {
	// Clip the segment's parametric range to each side's half-plane.
	t0, t1 := float32(0), float32(1)
	ab := math.Sub2f(b, a)
	for k, n := range vfrDetourNormals {
		room, rate := d[k]-math.Dot(a, n), math.Dot(ab, n)
		switch {
		case rate > 0:
			t1 = min(t1, room/rate)
		case rate < 0:
			t0 = max(t0, room/rate)
		case room <= 0:
			return false
		}
	}
	return t0 < t1
}

// around returns the vertices of the shorter way around the polygon from a
// to b, two points outside it on either side.
func (d detourPolygon) around(a, b [2]float32) [][2]float32 {
	const n = vfrDetourSides
	// A point outside the polygon sees a consecutive run of its sides. A path
	// around the polygon from the point leaves it at the vertex at one end of
	// that run.
	seen := func(p [2]float32) (first, last int) {
		for k := range n {
			if d.sees(p, k) && !d.sees(p, k-1) {
				first = k
			}
			if d.sees(p, k) && !d.sees(p, k+1) {
				last = k
			}
		}
		return
	}
	walk := func(from, to, step int) [][2]float32 {
		var path [][2]float32
		for k := from; ; k = (k + step + n) % n {
			path = append(path, d.vertex(k))
			if k == to {
				return path
			}
		}
	}
	length := func(path [][2]float32) float32 {
		l := math.Distance2f(a, path[0]) + math.Distance2f(path[len(path)-1], b)
		for i := 1; i < len(path); i++ {
			l += math.Distance2f(path[i-1], path[i])
		}
		return l
	}

	// Going counterclockwise, the path leaves a at the vertex after the last
	// side a sees and reaches b from the vertex before the first side b sees.
	// Going clockwise is the mirror image.
	af, al := seen(a)
	bf, bl := seen(b)
	ccw := walk(al, (bf+n-1)%n, 1)
	cw := walk((af+n-1)%n, bl, -1)
	return util.Select(length(ccw) < length(cw), ccw, cw)
}

// adjustRouteForShelves holds a VFR route under the airspace it crosses and
// doesn't pass over: the shelves of Class B and C airspace and the like. A
// waypoint beneath a shelf is restricted to the altitude flown under it and, where the
// shelf over a leg changes, waypoints are added so that the aircraft is down
// before it passes under a lower one and stays down until it is out from
// under it. Everywhere else the route keeps its cruise altitude, so a shelf
// near one end of the route doesn't hold the whole flight under it. It
// reports false where there is no room to fly under a shelf at all: the
// field is too close below it, or it is below the floor that vfrFloor keeps
// the flight above, so the flight has to go somewhere else.
func (s *Sim) adjustRouteForShelves(wps []av.Waypoint, cruise int, depap, arrap db.Airport) ([]av.Waypoint, bool) {
	pts := sampleRoute(wps)

	// The altitude to fly at each point: the cruise altitude, or over or
	// under the airspace there.
	passOver := vfrPassOver(depap, arrap, cruise)
	ceiling := util.MapSlice(pts, func(p routePoint) int {
		return clearAltitude(slices.Collect(s.vfrAirspace.Over(p.loc)), cruise, passOver)
	})

	// A shelf's edge lies somewhere between two adjacent points, so at each
	// of them the aircraft must be under the lower of the two: down before
	// the first point beneath a lower shelf and still down at the first
	// point past it.
	held := make([]int, len(pts))
	for i := range pts {
		held[i] = ceiling[i]
		if i > 0 {
			held[i] = min(held[i], ceiling[i-1])
		}
		if i+1 < len(pts) {
			held[i] = min(held[i], ceiling[i+1])
		}
	}

	for i, p := range pts {
		if held[i] == cruise {
			continue
		}
		dDep, dArr := math.NMDistance2LL(p.loc, depap.Location), math.NMDistance2LL(p.loc, arrap.Location)
		nearer := util.Select(dDep < dArr, depap, arrap)
		floor, ok := s.vfrFloor(p.loc, depap, arrap)
		if held[i] < nearer.Elevation+minVFRShelfRoom || (ok && held[i] < floor) {
			return nil, false
		}
	}

	hold := func(wp *av.Waypoint, alt int) {
		if wp.AltitudeRestriction() == nil {
			wp.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(float32(alt)))
			return
		}
		wp.AltRestriction.Range[1] = min(wp.AltRestriction.Range[1], float32(alt))
		wp.AltRestriction.Range[0] = min(wp.AltRestriction.Range[0], wp.AltRestriction.Range[1])
	}

	result := make([]av.Waypoint, 0, len(wps)+4)
	shelfWpNum := 0
	for i, p := range pts {
		if p.wp != -1 {
			wp := wps[p.wp]
			if held[i] < cruise {
				hold(&wp, held[i])
			}
			result = append(result, wp)
			continue
		}
		// Between waypoints, only the ends of a stretch held at one altitude
		// need a waypoint: the aircraft must be down by the first and stay
		// down through the last.
		if held[i] == cruise || (held[i] == held[i-1] && held[i] == held[i+1]) {
			continue
		}
		shelfWpNum++
		wp := av.Waypoint{
			Fix:      fmt.Sprintf("_shelf%d@%d", shelfWpNum, held[i]),
			Location: p.loc,
		}
		wp.SetAltitudeRestriction(av.MakeAtAltitudeRestriction(float32(held[i])))
		result = append(result, wp)
	}
	return result, true
}

// vfrFloors returns vfrFloor at each of the points of a route between depap
// and arrap, or zero where there is none.
func (s *Sim) vfrFloors(pts []routePoint, depap, arrap db.Airport) []int {
	return util.MapSlice(pts, func(p routePoint) int {
		f, _ := s.vfrFloor(p.loc, depap, arrap)
		return f
	})
}

// vfrTerrainIncrement is the step between the altitudes of the restrictions
// adjustRouteForTerrain gives a route.
const vfrTerrainIncrement = 500

// adjustRouteForTerrain gives a VFR route the altitude restrictions that keep
// it above vfrFloor. The terrain under a point sampled along the route
// reaches toward its neighbors, so at each point the flight has to be above
// the floor there and at the points on either side: at the next
// vfrTerrainIncrement above it, which leaves room for a flight that settles
// a little below a restriction it is holding. It may have to be higher still
// to climb at vfrClimbGradient to what is needed further on. The route's
// waypoints are restricted to at or above that altitude, rounded up to
// vfrTerrainIncrement. Between them, a waypoint is added where the rounded
// altitude goes up, which the flight has to be up to by then, and where it
// goes down, which the flight has to stay up for until then. The restrictions
// are at or above rather than at, so that a flight above them stays where it
// is; whether a flight can climb to them in time is for the route's
// validation flight to find out.
func (s *Sim) adjustRouteForTerrain(wps []av.Waypoint, depap, arrap db.Airport) []av.Waypoint {
	pts := sampleRoute(wps)
	floor := s.vfrFloors(pts, depap, arrap)

	// Work back from the end of the route to find the altitude needed at
	// each point.
	need := make([]int, len(pts))
	for i := len(pts) - 1; i >= 0; i-- {
		if f := slices.Max(floor[max(i-1, 0):min(i+2, len(pts))]); f > 0 {
			need[i] = (f/vfrTerrainIncrement + 1) * vfrTerrainIncrement
		}
		if i+1 < len(pts) {
			climb := int(vfrClimbGradient * math.NMDistance2LL(pts[i].loc, pts[i+1].loc))
			need[i] = max(need[i], need[i+1]-climb)
		}
	}
	alt := util.MapSlice(need, func(n int) int {
		return min((n+vfrTerrainIncrement-1)/vfrTerrainIncrement*vfrTerrainIncrement, maxVFRAltitude)
	})

	result := make([]av.Waypoint, 0, len(wps)+8)
	terrainWpNum := 0
	for i, p := range pts {
		if p.wp != -1 {
			wp := wps[p.wp]
			if alt[i] > 0 {
				raiseAltitudeRestriction(&wp, float32(alt[i]))
			}
			result = append(result, wp)
			continue
		}
		if alt[i] <= alt[i-1] && alt[i] <= alt[i+1] {
			continue
		}
		terrainWpNum++
		wp := av.Waypoint{
			Fix:      fmt.Sprintf("_terrain%d@%d", terrainWpNum, alt[i]),
			Location: p.loc,
		}
		wp.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(float32(alt[i])))
		result = append(result, wp)
	}
	return result
}

// raiseAltitudeRestriction makes wp's altitude restriction at least alt,
// keeping a range at least 1000' wide if it has to raise its top.
func raiseAltitudeRestriction(wp *av.Waypoint, alt float32) {
	ar := wp.AltitudeRestriction()
	if ar == nil {
		wp.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(alt))
		return
	}
	ar.Range[0] = max(ar.Range[0], alt)
	if ar.Range[1] != av.MaxAltitude && ar.Range[1] < ar.Range[0] {
		ar.Range[1] = ar.Range[0] + 1000
	}
}

// vfrAvoids reports whether VFR traffic stays out of the volume vol of the
// special use airspace sua (AIM 3-4). Prohibited and restricted areas are
// regulatory, and warning areas hold hazards such as live fire. MOAs and
// alert areas only ask VFR pilots to take care. The CIFP codes national
// security areas, special flight rules areas, and special air traffic rules
// areas (AIM 3-5-7) as unspecified. Pilots are asked to avoid national
// security areas. VFR traffic flies through the other two by following their
// procedures, but stays out of the flight restricted zones (FRZ) and flight
// free zones (FFZ) inside them.
func vfrAvoids(sua db.SpecialUseAirspace, vol db.SpecialUseAirspaceVolume) bool {
	switch sua.Type {
	case db.ProhibitedArea, db.RestrictedArea, db.WarningArea:
		return true
	case db.UnspecifiedArea:
		procedural := strings.Contains(sua.Designation, "SFRA") || strings.Contains(sua.Designation, "SATR")
		closed := strings.Contains(vol.Description, " FRZ") || strings.Contains(vol.Description, " FFZ")
		return !procedural || closed
	default:
		return false
	}
}

// vfrAvoidedSpecialUseAirspace returns the volumes of special use airspace
// that VFR traffic stays out of. Without the terrain under them, limits above
// ground level are taken conservatively: a floor is at least as many feet
// above sea level, which keeps flights below it, and a volume with such a
// ceiling is taken to reach all the way up. A volume from the ground to 0'
// AGL holds no airspace and is left out.
func vfrAvoidedSpecialUseAirspace() []*av.AirspaceVolume {
	var vols []*av.AirspaceVolume
	for _, sua := range db.DB.SpecialUseAirspace {
		for _, v := range sua.Volumes {
			if !vfrAvoids(sua, v) || v.Ceiling == 0 {
				continue
			}
			vol := v.AirspaceVolume
			if v.CeilingAGL {
				vol.Ceiling = db.UnlimitedCeiling
			}
			vols = append(vols, &vol)
		}
	}
	return vols
}

func (s *Sim) initializeAirspaceGrids() {
	s.vfrTerminalAlts = make(map[av.ICAOAirportCode]int)
	s.vfrTakeoffs = make(map[av.ICAOAirportCode][2]math.Point2LL)
	volumes := func(a map[string][]av.AirspaceVolume) []*av.AirspaceVolume {
		var vols []*av.AirspaceVolume
		for _, volslice := range util.SortedMap(a) {
			for _, v := range volslice {
				vols = append(vols, &v)
			}
		}
		return vols
	}
	bravo := volumes(db.DB.BravoAirspace)
	s.bravoAirspace = db.MakeAirspaceGrid(bravo)
	s.vfrAirspace = db.MakeAirspaceGrid(slices.Concat(bravo, volumes(db.DB.CharlieAirspace), vfrAvoidedSpecialUseAirspace()))
	s.terrain = db.DB.Terrain.MakeGrid()
}
