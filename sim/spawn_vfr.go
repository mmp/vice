// sim/spawn_vfr.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func (s *Sim) initializeIFRDepartureNoLock(ac *Aircraft, ap *av.Airport, departureAirport av.ICAOAirportCode,
	runway av.RunwayID, dep *av.Departure, cruise CruiseLimits,
	exitRoutes map[av.ExitID]*av.ExitRoute) (*Aircraft, error) {
	exitRoute := exitRoutes[dep.Exit]
	err := ac.InitializeDeparture(ap, departureAirport, dep, string(runway), *exitRoute, cruise,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.wxModel, s.State.SimTime, s.Rand, s.lg)
	if err != nil {
		return nil, err
	}

	// Departures aren't immediately associated, but the STARSComputer will
	ac.ReportDepartureHeading = exitRoutesHaveVariedHeadings(exitRoutes)
	ac.ReportDepartureSID = exitRoutesHaveVariedSIDs(exitRoutes)

	shortExit := dep.Exit.Base()
	isTRACON := db.DB.IsTRACON(s.State.Facility)
	nasFp := s.initNASFlightPlan(ac, av.FlightTypeDeparture)
	nasFp.Route = ac.FlightPlan.Route
	nasFp.EntryFix = db.AirportDisplayId(ac.FlightPlan.DepartureAirport)
	// The flight plan carries the exit's 3-character fix id when one is
	// adapted; fix-pair endpoints and adapted fix criteria match against it.
	nasFp.ExitFix = s.State.FacilityAdaptation.FixPairFixID(shortExit)
	nasFp.SecondaryScratchpad = dep.SecondaryScratchpad
	nasFp.RequestedAltitude = ac.FlightPlan.Altitude
	nasFp.AssignedAltitude = util.Select(!isTRACON, ac.FlightPlan.Altitude, 0)
	nasFp.RNAV = s.State.FacilityAdaptation.Datablocks.DisplayRNAVSymbol && exitRoute.IsRNAV

	ac.HoldForRelease = (ap.HoldForRelease || exitRoute.HoldForRelease) && ac.FlightPlan.Rules == av.FlightRulesIFR // VFRs aren't held
	s.assignDepartureController(ac, &nasFp, ap, exitRoute, departureAirport, string(runway))

	// Adapted scratchpads are per-area, so this must follow the controller assignment above.
	if dep.Scratchpad != "" {
		nasFp.Scratchpad = dep.Scratchpad
	} else if sp1 := s.State.FacilityAdaptation.Datablocks.Scratchpad1; sp1.DisplayExitFix ||
		sp1.DisplayExitFix1 || sp1.DisplayExitGate || sp1.DisplayAltExitGate {
		// Don't set the scratchpad; it will be set automatically.
	} else {
		nasFp.Scratchpad = s.State.FacilityAdaptation.ScratchpadForExit(dep.Exit,
			s.areaForTCP(nasFp.TrackingController))
	}

	if db.DB.IsARTCC(s.State.Facility) {
		// The departure levels off at the exit route's altitude until it is
		// climbed further, so the data block needs it as an interim altitude
		// for conflict alert to know where the climb stops.
		alt := util.Select(exitRoute.AssignedAltitude != 0, exitRoute.AssignedAltitude, exitRoute.ClearedAltitude)
		s.recordVirtualAltitudeEntry(&nasFp, min(alt, ac.FlightPlan.Altitude), true)
		nasFp.applyERAMEntries(exitRoute.ERAM)
	}

	// Pseudo-ERAM coordination then the STARS fix-pair pipeline; overrides the
	// departure assignment above when adapted.
	s.deriveERAMFixPair(&nasFp, ac)
	s.applyFixPairAssignment(&nasFp, ac)
	// A fully-contained (internal) flight whose exit fix is a local-arrival
	// airport is reclassified as an arrival for display/processing. The initial
	// owner stays the departure controller assigned above; ownership is
	// deliberately not re-derived as an arrival.
	if nasFp.LocalArrival {
		nasFp.TypeOfFlight = av.FlightTypeArrival
	}
	nasFp.applyAutoScratchpad(s.State.FacilityAdaptation.AutoScratchpadAssignment, s.State.ConfigurationId)

	if err := s.ERAMComputer.AssignSquawk(ac, &nasFp, s.Rand); err != nil {
		return nil, err
	}

	// Departures aren't immediately associated, but the STARSComputer will
	// hold on to their flight plans for now.
	// Create a flight strip for departures
	printStrips := ap.PrintDepartureStrips == nil || *ap.PrintDepartureStrips
	if printStrips && shouldCreateFlightStrip(&nasFp) {
		if s.isVirtualController(nasFp.TrackingController) {
			// Virtual controller: strip goes to the handoff target
			if !s.isVirtualController(nasFp.InboundHandoffController) {
				s.initFlightStrip(&nasFp, nasFp.InboundHandoffController)
			}
		} else {
			// Human controller: strip goes to the tracking controller
			s.initFlightStrip(&nasFp, nasFp.TrackingController)
		}
	}

	_, err = s.STARSComputer.CreateFlightPlan(nasFp)
	return ac, err
}

// sampleVFRDeparture samples a VFR departure from the given airport for a
// manual launch slot. Note that it may fail without an error if it's having
// trouble finding a route.
func (s *Sim) sampleVFRDeparture(departureAirport av.ICAOAirportCode) (*Aircraft, error) {
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

	ap, ok := s.State.Airports[departureAirport]
	if !ok || ap.VFRRateSum() == 0 {
		// This shouldn't happen...
		return nil, nil
	}

	ac, _, err := s.createUncontrolledVFRDeparture(departureAirport, arrive, ap.VFR.Randoms.Fleet, nil,
		s.currentCallsigns(), s.State.SimTime)
	return ac, err
}

func makeDepartureAircraft(ac *Aircraft, simTime Time, gateDelay time.Duration) DepartureAircraft {
	d := DepartureAircraft{
		ADSBCallsign:        ac.ADSBCallsign,
		SpawnTime:           simTime,
		ReadyDepartGateTime: simTime.Add(gateDelay),
	}

	// Simulate out the takeoff roll and initial climb to figure out when
	// we'll have sufficient separation to launch the next aircraft and to
	// record the aircraft's initial flight path. The simulation uses calm
	// wind so that the courses measured from the paths reflect the charted
	// departure procedures: controllers judge divergence from what's
	// charted, and wind drift varying with each aircraft's spawn time and
	// speed would otherwise blur it.
	model := wx.MakeCalmModel()
	simAc := *ac
	start := ac.Position()
	const nsteps = 120
	d.MinSeparation = nsteps * time.Second // just in case
	d.AirborneDistance = -1                // not airborne within the simulation horizon
	d.LaunchPath = make([]math.Point2LL, 0, nsteps+1)
	d.LaunchPath = append(d.LaunchPath, start)
	minSepSet := false
	for i := range nsteps {
		simAc.Update(model, simTime, nil, nil, nil /* lg */)
		d.LaunchPath = append(d.LaunchPath, simAc.Position())
		if d.AirborneDistance < 0 && simAc.IsAirborne() {
			d.AirborneDistance = math.NMDistance2LL(start, simAc.Position())
			d.AirborneTime = time.Duration(i+1) * time.Second
		}
		// We need 6,000' and airborne, but we'll add a bit of slop
		if !minSepSet && simAc.IsAirborne() && math.NMDistance2LL(start, simAc.Position()) > 7500*math.FeetToNauticalMiles {
			d.MinSeparation = time.Duration(i) * time.Second
			minSepSet = true
		}
	}

	return d
}

func (s *Sim) createUncontrolledVFRDeparture(depart, arrive av.ICAOAirportCode, fleet string, routeWps []av.Waypoint,
	callsigns []av.ADSBCallsign, simTime Time) (*Aircraft, string, error) {
	depap, arrap := db.DB.Airports[depart], db.DB.Airports[arrive]
	rwy, _, ok := s.currentVFRRunway(depart)
	if !ok {
		return nil, "", fmt.Errorf("%s: unable to find current VFR runway", depart)
	}

	// Nothing else about the candidate matters if there is no room to get
	// into the pattern at the far end without entering the airspace over it.
	terminalCeiling, ok := s.vfrTerminalCeiling(arrap)
	if !ok {
		return nil, "", ErrViolatedAirspace
	}

	ac, acType := s.sampleAircraft(av.AirlineSpecifier{ICAO: "N", Fleet: fleet}, depart, arrive, callsigns, s.lg)
	if ac == nil {
		return nil, "", fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.FlightRulesVFR
	ac.Squawk = 0o1200
	if r := s.Rand.Float32(); r < .02 {
		ac.Mode = av.TransponderModeOn // mode-A
	} else if r < .03 {
		ac.Mode = av.TransponderModeStandby // flat out off
	}
	ac.InitializeFlightPlan(rules, acType, depart, arrive)

	perf, ok := db.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		return nil, "", fmt.Errorf("invalid aircraft type: no performance data %q", ac.FlightPlan.AircraftType)
	}

	dist := math.NMDistance2LL(depap.Location, arrap.Location)

	mid := math.Mid2f(depap.Location, arrap.Location)
	if arrive == depart {
		dist := float32(s.Rand.IntRange(10, 30))
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

	wps = append(wps, av.Waypoint{Fix: "_dep_threshold", Location: rwy.Threshold})
	opp := math.Offset2LL(rwy.Threshold, math.MagneticToTrue(rwy.Heading, s.State.MagneticVariation), 1 /* nm */, s.State.NmPerLongitude)
	wps = append(wps, av.Waypoint{Fix: "_opp", Location: opp})

	rg := av.MakeRouteGenerator(rwy.Threshold, opp, s.State.NmPerLongitude)
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

	ac.FlightPlan.Altitude = FiledCruiseAltitude(ac.FlightPlan, perf, CruiseLimits{},
		s.State.NmPerLongitude, s.State.MagneticVariation, s.Rand)

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
			alt := float32(ac.FlightPlan.Altitude)
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
		return nil, "", err
	}

	// Only now is the route the one the aircraft will fly: building the Nav
	// scatters the waypoints within their radii. The shelf and MVA legs have
	// to be sampled along that route rather than the one they were planned
	// on, both so the airspace and terrain they clear are what is overflown
	// and so they land on the legs they divide rather than off to one side
	// of them.
	ac.Nav.Waypoints, ok = s.adjustRouteForShelves(ac.Nav.Waypoints, ac.FlightPlan.Altitude, depap, arrap)
	if !ok {
		return nil, "", ErrViolatedAirspace
	}
	ac.Nav.Waypoints = s.adjustRouteForMVA(string(ac.ADSBCallsign), ac.Nav.Waypoints)

	// Deep-copy only Nav (not the full Aircraft) to avoid copying
	// maps, pointers, and fields unused during route validation.
	simNav := deep.MustCopy(ac.Nav)
	simNav.Prespawn = true
	simFP := ac.FlightPlan
	prespawnWxs := s.wxModel.Lookup(simNav.FlightState.Position,
		simNav.FlightState.Altitude, simTime.Time())
	for i := range 3 * 60 * 60 { // limit to 3 hours of sim time, just in case
		if wp := simNav.UpdateWithWeather("", prespawnWxs, nil, &simFP,
			simTime.NavTime(), nil).PassedWaypoint; wp != nil {
			if wp.HasDeleteAction() {
				return ac, rwy.Id, nil
			}
			if wp.SequenceVFRLanding() {
				// Generate descent waypoints so prespawn validates the
				// descent from cruise altitude through any bravo/charlie
				// airspace down to pattern altitude.
				arrAP, ok := db.DB.Airports[ac.FlightPlan.ArrivalAirport]
				if !ok {
					return ac, rwy.Id, nil
				}
				patternAlt := float32(arrAP.Elevation + 1000)
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
		if s.bravoAirspace.Inside(pos, alt) ||
			s.charlieAirspace.Inside(pos, alt) ||
			s.State.FacilityAdaptation.Filters.VFRInhibit.Inside(pos, alt) {
			return nil, "", ErrViolatedAirspace
		}
		// Check MVA violation: aircraft must stay at or above MVA - 1000'.
		// Skip when within 3nm of departure airport or 5nm of arrival airport.
		distFromDeparture := math.NMDistance2LL(pos, simNav.FlightState.DepartureAirportLocation)
		distToArrival := math.NMDistance2LL(pos, simNav.FlightState.ArrivalAirportLocation)
		if distFromDeparture > 3 && distToArrival > 5 {
			if mva := s.mvaGrid.GetMVA(pos); mva > 0 && simNav.FlightState.Altitude < float32(mva-vfrMVABuffer) {
				// Find which waypoint we're heading toward
				wpIdx := -1
				var wpName string
				for j, wp := range simNav.Waypoints {
					wpIdx = j
					wpName = wp.Fix
					break
				}
				nav.NavLog(string(ac.ADSBCallsign), simTime.NavTime(), "state",
					"rejected at %.0f' (MVA %d, need %d) heading to wp %d %q, pos %v, %.1fnm from dep, %.1fnm from arr",
					simNav.FlightState.Altitude, mva, mva-vfrMVABuffer, wpIdx, wpName, pos, distFromDeparture, distToArrival)
				return nil, "", ErrVFRBelowMVA
			}
		}
	}

	//s.lg.Infof("%s: %s/%s aircraft not finished after 3 hours of sim time",		ac.ADSBCallsign, depart, arrive)

	return nil, "", ErrVFRSimTookTooLong
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

// adjustRouteForShelves holds a VFR route under the Class B and C shelves it
// crosses. A waypoint beneath a shelf is restricted to the altitude flown
// under it and, where the shelf over a leg changes, waypoints are added so
// that the aircraft is down before it passes under a lower one and stays down
// until it is out from under it. Everywhere else the route keeps its cruise
// altitude, so a shelf near one end of the route doesn't hold the whole
// flight under it. It reports false where there is no room to fly under a
// shelf at all: the ground is too close or the MVA is above it, so the flight
// has to go somewhere else. The MVA is left out near either field, as it is
// during the route's validation flight, since an aircraft is below it on
// departure and arrival.
func (s *Sim) adjustRouteForShelves(wps []av.Waypoint, cruise int, depap, arrap db.Airport) ([]av.Waypoint, bool) {
	// Sample the route about a mile apart, keeping track of which points
	// are its waypoints.
	type routePoint struct {
		loc math.Point2LL
		wp  int // index in wps, or -1 for a point sampled along a leg
	}
	pts := []routePoint{{wps[0].Location, 0}}
	for i := 1; i < len(wps); i++ {
		for p := range legSamples(wps[i-1].Location, wps[i].Location) {
			pts = append(pts, routePoint{p, -1})
		}
		pts = append(pts, routePoint{wps[i].Location, i})
	}

	// The altitude to fly at each point: the cruise altitude, or under the
	// lowest shelf over it.
	ceiling := util.MapSlice(pts, func(p routePoint) int {
		c := cruise
		for _, grid := range []*db.AirspaceGrid{s.bravoAirspace, s.charlieAirspace} {
			if floor, covered := grid.ShelfFloor(p.loc); covered {
				c = min(c, (floor-vfrShelfBuffer)/vfrShelfIncrement*vfrShelfIncrement)
			}
		}
		return c
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
		if held[i] < nearer.Elevation+minVFRShelfRoom {
			return nil, false
		}
		if dDep > 3 && dArr > 5 {
			if mva := s.mvaGrid.GetMVA(p.loc); mva > 0 && held[i] < mva-vfrMVABuffer {
				return nil, false
			}
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

// vfrTerminalRadius bounds where a VFR arrival maneuvers at its destination.
// The traffic pattern, the 45-degree entry to it, and the orbit it holds in
// when the pattern is full all fall within it.
const vfrTerminalRadius = 5

// vfrPatternAltitude is how far above the field a VFR aircraft flies the
// pattern. vfrPatternMinRoom is the least room it can be squeezed into: below
// that the airspace is down around the base and final legs and there is no
// pattern left to fly.
const vfrPatternAltitude = 1000
const vfrPatternMinRoom = 500

// vfrTerminalCeiling returns the highest altitude a VFR arrival can use while
// maneuvering at ap and stay clear of Class B and C airspace. It reports
// false where there is no room above the pattern to do that: entering such a
// field means entering the airspace, which is not something we fly, so no VFR
// is sent there. The answer depends only on the airspace and the field, so it
// is worked out once per airport.
func (s *Sim) vfrTerminalCeiling(ap db.Airport) (int, bool) {
	s.ensureAirspaceGrids()
	if alt, ok := s.vfrTerminalAlts[ap.Id]; ok {
		return alt, alt > 0
	}

	ceiling := maxVFRAltitude
	sample := func(p math.Point2LL) {
		for _, grid := range []*db.AirspaceGrid{s.bravoAirspace, s.charlieAirspace} {
			if floor, covered := grid.ShelfFloor(p); covered {
				ceiling = min(ceiling, (floor-vfrShelfBuffer)/vfrShelfIncrement*vfrShelfIncrement)
			}
		}
	}

	sample(ap.Location)
	for r := 1; r <= vfrTerminalRadius; r++ {
		for hdg := 0; hdg < 360; hdg += 30 {
			sample(math.Offset2LL(ap.Location, math.TrueHeading(float32(hdg)), float32(r),
				s.State.NmPerLongitude))
		}
	}

	if ceiling < ap.Elevation+vfrPatternMinRoom {
		ceiling = 0
	}
	s.vfrTerminalAlts[ap.Id] = ceiling
	return ceiling, ceiling > 0
}

func (s *Sim) ensureAirspaceGrids() {
	if s.bravoAirspace == nil || s.charlieAirspace == nil || s.mvaGrid == nil {
		s.initializeAirspaceGrids()
	}
}

func (s *Sim) initializeAirspaceGrids() {
	s.vfrTerminalAlts = make(map[av.ICAOAirportCode]int)
	initAirspace := func(a map[string][]av.AirspaceVolume) *db.AirspaceGrid {
		var vols []*av.AirspaceVolume
		for volslice := range maps.Values(a) {
			for _, v := range volslice {
				vols = append(vols, &v)
			}
		}
		return db.MakeAirspaceGrid(vols)
	}
	s.bravoAirspace = initAirspace(db.DB.BravoAirspace)
	s.charlieAirspace = initAirspace(db.DB.CharlieAirspace)
	s.mvaGrid = db.MakeMVAGrid(db.DB.MVAs[s.State.Facility])
}

// adjustRouteForMVA modifies the waypoint altitude restrictions to ensure
// the aircraft stays above MVA - vfrMVABuffer along the route.
func (s *Sim) adjustRouteForMVA(callsign string, wps []av.Waypoint) []av.Waypoint {
	if s.mvaGrid == nil || len(wps) < 2 {
		return wps
	}

	result := make([]av.Waypoint, 0, len(wps)*2)
	mvaWpNum := 0

	for i, wp := range wps {
		if i > 0 {
			// Sample between previous waypoint and this one to look for MVA transitions.
			prevWp := wps[i-1]
			prevMVA := s.mvaGrid.GetMVA(prevWp.Location)
			prevPos := prevWp.Location

			for pos := range legSamples(prevWp.Location, wp.Location) {
				mva := s.mvaGrid.GetMVA(pos)

				if mva != prevMVA && mva > 0 && prevMVA > 0 {
					// MVA changed - insert a waypoint
					// Higher MVA: insert a new waypoint with an altitude restriction at the
					// previous sample position so that we can record "at or above" there with the
					// hopes that the aircraft will be able to reach it.
					// Lower MVA: insert at the current position to indicate that a descent may be
					// possible.
					pNew := util.Select(mva > prevMVA, prevPos, pos)

					minAlt := min(float32(mva-vfrMVABuffer), maxVFRAltitude)
					mvaWpNum++
					mvaWp := av.Waypoint{
						Fix:      fmt.Sprintf("_mva%d@%.0f", mvaWpNum, minAlt),
						Location: pNew,
					}
					mvaWp.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(minAlt))
					result = append(result, mvaWp)
				}

				prevMVA = mva
				prevPos = pos
			}
		}

		// Apply MVA constraints to this waypoint and add it
		if mva := s.mvaGrid.GetMVA(wp.Location); mva > 0 {
			minAlt := min(float32(mva-vfrMVABuffer), maxVFRAltitude)
			if wp.AltitudeRestriction() == nil {
				wp.SetAltitudeRestriction(av.MakeAtOrAboveAltitudeRestriction(minAlt))
			} else {
				if wp.AltRestriction.Range[0] < minAlt {
					wp.AltRestriction.Range[0] = minAlt
				}
				if wp.AltRestriction.Range[1] != av.MaxAltitude && wp.AltRestriction.Range[1] < wp.AltRestriction.Range[0] {
					wp.AltRestriction.Range[1] = wp.AltRestriction.Range[0] + 1000
				}
			}
		}
		result = append(result, wp)
	}

	return result
}
