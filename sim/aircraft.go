// sim/aircraft.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// CallsignAddressingForm indicates how a controller addressed an aircraft's callsign.
type CallsignAddressingForm int

const (
	// AddressingFormFull is the full callsign form (e.g., "november 1 2 3 alpha bravo")
	AddressingFormFull CallsignAddressingForm = iota
	// AddressingFormTypeTrailing3 is the aircraft type + trailing 3 form (e.g., "skyhawk 3 alpha bravo")
	AddressingFormTypeTrailing3
)

const (
	trafficSightingMaxAge         = 60 * time.Second
	approachTrafficSightingMaxAge = 30 * time.Second
)

type SeenAircraft struct {
	Callsign                    av.ADSBCallsign
	SightedTime                 Time
	OfferedToMaintainSeparation bool
	MaintainingVisualSeparation bool
}

type UnseenTrafficCall struct {
	Callsign         av.ADSBCallsign
	CalledTime       Time
	WhereAskFireTime Time // If non-zero and passed, pilot proactively asks "where's that traffic"
}

type Aircraft struct {
	// This is ADS-B callsign of the aircraft. Just because different the
	// callsign in the flight plan can be different across multiple STARS
	// facilities, so two different facilities can show different
	// callsigns; however, the ADS-B callsign is transmitted from the
	// aircraft and would be the same to all facilities.
	ADSBCallsign av.ADSBCallsign

	Squawk av.Squawk
	Mode   av.TransponderMode

	IdentStartTime, IdentEndTime Time

	FlightPlan   av.FlightPlan
	TypeOfFlight av.TypeOfFlight
	// For departures, after we first see them in the departure acquisition
	// volume, we set a time a bit in the future for the flight plan to
	// actually acquire to simulate the delay in that.
	DepartureFPAcquisitionTime Time

	// State related to navigation.
	Nav nav.Nav

	// Departure-related state
	SID string

	// Arrival-related state
	STAR                string
	STARRunwayWaypoints map[string]av.WaypointArray
	GotContactTower     bool

	NASFlightPlan *NASFlightPlan

	// ControllerFrequency is the controller position whose radio frequency
	// this aircraft is tuned to. Only this controller can issue ATC commands
	// to the aircraft. Empty means the aircraft is not on any controller's
	// frequency.
	ControllerFrequency ControlPosition

	HoldForRelease    bool
	Released          bool // only used for hold for release
	ReleaseTime       Time
	WaitingForLaunch  bool // for departures
	MissingFlightPlan bool

	GoAroundDistance *float32

	// Set when tower sends aircraft around for spacing; affects the contact message.
	SentAroundForSpacing bool
	// Set when a spacing check rolled "no go-around"; prevents re-rolling every tick.
	SpacingGoAroundDeclined bool
	// Set when going around on runway heading (vs a specific assigned heading).
	GoAroundOnRunwayHeading bool
	// Set when the aircraft has gone around; prevents the arrival drop
	// filter from dropping its flight plan.
	WentAround bool

	// Departure related state
	DepartureContactAltitude float32 // 0 = waiting for /tc point, -1 = already contacted departure
	ReportDepartureHeading   bool    // true if runway has multiple exit headings
	ReportDepartureSID       bool    // true if runway has multiple SIDs

	FirstSeen Time

	RequestedFlightFollowing bool
	// WaitingForGoAhead is set when a VFR aircraft has made an abbreviated
	// flight following request ("approach, N123AB, VFR request") and is
	// waiting for the controller to say "go ahead".
	WaitingForGoAhead bool

	EmergencyState *EmergencyState

	LastRadioTransmission Time

	// LastAddressingForm tracks how the controller last addressed this aircraft.
	// Used for readbacks to match the controller's style.
	LastAddressingForm CallsignAddressingForm

	// ATIS letter the aircraft reported during initial contact (e.g., "B").
	// Empty if the pilot did not report having ATIS.
	ReportedATIS string

	// SeenTraffic tracks traffic the pilot has reported in sight, ordered
	// from oldest to newest.
	SeenTraffic []SeenAircraft
	// UnseenTrafficCall tracks the latest unresolved TRAFFIC advisory.
	UnseenTrafficCall *UnseenTrafficCall

	// FieldInSight is set when the pilot has confirmed the airport is in sight
	// (either via AP command response or spontaneous report).
	FieldInSight bool

	// RequestedVisualApproach is set when the pilot has spontaneously requested
	// the visual approach (field in sight). Prevents repeated requests.
	RequestedVisualApproach bool
	// WantsVisualApproach is decided at aircraft creation: whether this pilot spontaneously reports
	// field in sight when eligible.
	WantsVisualApproach bool
	// VisualApproachRequestDistance, if non-zero, is the distance (NM) from the arrival airport at
	// which the pilot will perform a single visibility check and request the visual approach if the
	// field is in sight. Set to zero after the check (requested or given up) to prevent retries.
	VisualApproachRequestDistance float32

	TouchAndGosRemaining int // >0 means pattern aircraft; decremented each lap
}

func (ac *Aircraft) GetRadarTrack(now Time) av.RadarTrack {
	return av.RadarTrack{
		ADSBCallsign:        ac.ADSBCallsign,
		Squawk:              util.Select(ac.Mode != av.TransponderModeStandby, ac.Squawk, av.Squawk(0)),
		Mode:                ac.Mode,
		Ident:               ac.Mode != av.TransponderModeStandby && now.After(ac.IdentStartTime) && now.Before(ac.IdentEndTime),
		TrueAltitude:        ac.Altitude(),
		TransponderAltitude: util.Select(ac.Mode == av.TransponderModeAltitude, ac.Altitude(), 0),
		Location:            ac.Position(),
		Heading:             ac.Heading(),
		Groundspeed:         ac.GS(),
		TypeOfFlight:        ac.TypeOfFlight,
	}
}

func (ac *Aircraft) clearUnseenTrafficCall() {
	ac.UnseenTrafficCall = nil
}

func (ac *Aircraft) clearOfferedToMaintainSeparation() {
	for i := range ac.SeenTraffic {
		ac.SeenTraffic[i].OfferedToMaintainSeparation = false
	}
}

// RecordSighting refreshes an existing sighting or appends a new one,
// keeping the slice ordered from oldest to newest.
func (ac *Aircraft) RecordSighting(traffic av.ADSBCallsign, now Time) *SeenAircraft {
	for i := range ac.SeenTraffic {
		if ac.SeenTraffic[i].Callsign != traffic {
			continue
		}

		seen := ac.SeenTraffic[i]
		seen.SightedTime = now
		ac.SeenTraffic = slices.Delete(ac.SeenTraffic, i, i+1)
		ac.SeenTraffic = append(ac.SeenTraffic, seen)
		return &ac.SeenTraffic[len(ac.SeenTraffic)-1]
	}

	ac.SeenTraffic = append(ac.SeenTraffic, SeenAircraft{
		Callsign:    traffic,
		SightedTime: now,
	})
	return &ac.SeenTraffic[len(ac.SeenTraffic)-1]
}

func (ac *Aircraft) RecentSighting(now Time, maxAge time.Duration) *SeenAircraft {
	for i := len(ac.SeenTraffic) - 1; i >= 0; i-- {
		if now.Sub(ac.SeenTraffic[i].SightedTime) <= maxAge {
			return &ac.SeenTraffic[i]
		}
	}
	return nil
}

func (ac *Aircraft) RecentSightingOf(traffic av.ADSBCallsign, now Time, maxAge time.Duration) *SeenAircraft {
	for i := len(ac.SeenTraffic) - 1; i >= 0; i-- {
		seen := &ac.SeenTraffic[i]
		if seen.Callsign == traffic && now.Sub(seen.SightedTime) <= maxAge {
			return seen
		}
	}
	return nil
}

// GetSTTFixes returns the raw fix names relevant for STT context.
// For STARS (terminal) sessions, assigned waypoints within 75nm are included with no count
// limit. For ERAM (enroute) sessions, up to 5 assigned waypoints within 300nm are included.
// Approach waypoints are included unconditionally when applicable.
func (ac *Aircraft) GetSTTFixes(isERAM bool) []string {
	var fixes []string
	p := ac.Nav.FlightState.Position

	maxDistNM, maxCount := float32(75), 0
	if isERAM {
		maxDistNM, maxCount = 300, 5
	}

	isValidFix := func(fix string) bool {
		return len(fix) >= 3 && len(fix) <= 5 && fix[0] != '_'
	}

	// Include arrival and departure airports so STT can match airport
	// names (e.g. "Kennedy 12 o'clock 12 miles" for AP command).
	if ac.FlightPlan.ArrivalAirport != "" {
		fixes = append(fixes, ac.FlightPlan.ArrivalAirport)
	}
	if ac.FlightPlan.DepartureAirport != "" {
		fixes = append(fixes, ac.FlightPlan.DepartureAirport)
	}

	routeFixes := 0
	for _, wp := range ac.Nav.AssignedWaypoints() {
		if math.NMDistance2LL(p, wp.Location) > maxDistNM && len(fixes) > 0 {
			break
		}
		if isValidFix(wp.Fix) {
			fixes = append(fixes, wp.Fix)
			routeFixes++
			if maxCount > 0 && routeFixes >= maxCount {
				break
			}
		}
	}

	if ac.Nav.Approach.Assigned != nil {
		// Check if approach waypoints are already in the route
		hasApproachWaypoints := slices.ContainsFunc(ac.Nav.AssignedWaypoints(),
			func(wp av.Waypoint) bool { return wp.OnApproach() })

		// If not, add all approach waypoints (aircraft is being vectored to intercept)
		if !hasApproachWaypoints {
			for _, wps := range ac.Nav.Approach.Assigned.Waypoints {
				for _, wp := range wps {
					if isValidFix(wp.Fix) {
						fixes = append(fixes, wp.Fix)
					}
				}
			}
		}
	}

	return fixes
}

func (ac *Aircraft) InitializeFlightPlan(r av.FlightRules, acType, dep, arr string) {
	ac.FlightPlan = av.FlightPlan{
		Rules:            r,
		AircraftType:     acType,
		DepartureAirport: dep,
		ArrivalAirport:   arr,
		CruiseSpeed:      int(ac.AircraftPerformance().Speed.CruiseTAS),
	}
}

func (ac *Aircraft) TAS(temp av.Temperature) float32 {
	return ac.Nav.TAS(temp)
}

///////////////////////////////////////////////////////////////////////////
// Navigation and simulation

func (ac *Aircraft) Update(model *wx.Model, simTime Time, bravo *av.AirspaceGrid, lg *log.Logger) nav.UpdateResult {
	if lg != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}

	navUpdate := ac.Nav.Update(string(ac.ADSBCallsign), model, &ac.FlightPlan, simTime.NavTime(), bravo)
	if navUpdate.PassedWaypoint != nil && lg != nil {
		lg.Debug("passed", slog.Any("waypoint", navUpdate.PassedWaypoint))
	}

	return navUpdate
}

func (ac *Aircraft) PilotMixUp() av.CommandIntent {
	return av.MixUpIntent{
		Callsign:    ac.ADSBCallsign,
		IsEmergency: ac.EmergencyState != nil,
	}
}

func (ac *Aircraft) Ident(now Time) av.CommandIntent {
	ac.IdentStartTime = now.Add(ac.Nav.Rand.DurationRange(2*time.Second, 5*time.Second)) // delay the start a bit
	ac.IdentEndTime = ac.IdentStartTime.Add(10 * time.Second)
	return av.TransponderIntent{Ident: true}
}

func (ac *Aircraft) AssignAltitude(altitude int, afterSpeed bool, simTime Time, delayReduction time.Duration) av.CommandIntent {
	return ac.Nav.AssignAltitude(float32(altitude), afterSpeed, simTime.NavTime(), delayReduction)
}

func (ac *Aircraft) AssignMach(mach float32, afterAltitude bool, temp av.Temperature) av.CommandIntent {
	return ac.Nav.AssignMach(mach, afterAltitude, temp)
}

func (ac *Aircraft) AssignSpeed(sr *av.SpeedRestriction, afterAltitude bool) av.CommandIntent {
	return ac.Nav.AssignSpeed(sr, afterAltitude)
}

func (ac *Aircraft) AssignSpeedUntil(sr *av.SpeedRestriction, until *av.SpeedUntil) av.CommandIntent {
	return ac.Nav.AssignSpeedUntil(sr, until)
}

func (ac *Aircraft) MaintainSlowestPractical() av.CommandIntent {
	return ac.Nav.MaintainSlowestPractical()
}

func (ac *Aircraft) MaintainMaximumForward() av.CommandIntent {
	return ac.Nav.MaintainMaximumForward()
}

func (ac *Aircraft) MaintainPresentSpeed() av.CommandIntent {
	return ac.Nav.MaintainPresentSpeed()
}

func (ac *Aircraft) SaySpeed(temp av.Temperature) av.CommandIntent {
	return ac.Nav.SaySpeed(temp)
}

func (ac *Aircraft) SayIndicatedSpeed() av.CommandIntent {
	return ac.Nav.SayIndicatedSpeed()
}

func (ac *Aircraft) SayMach(temp av.Temperature) av.CommandIntent {
	return ac.Nav.SayMach(temp)
}

func (ac *Aircraft) SayHeading() av.CommandIntent {
	return ac.Nav.SayHeading()
}

func (ac *Aircraft) SayAltitude() av.CommandIntent {
	return ac.Nav.SayAltitude()
}

func (ac *Aircraft) ExpediteDescent() av.CommandIntent {
	return ac.Nav.ExpediteDescent()
}

func (ac *Aircraft) ExpediteClimb() av.CommandIntent {
	return ac.Nav.ExpediteClimb()
}

func (ac *Aircraft) ExpediteDescentThrough(alt float32) av.CommandIntent {
	return ac.Nav.ExpediteDescentThrough(alt)
}

func (ac *Aircraft) ExpediteClimbThrough(alt float32) av.CommandIntent {
	return ac.Nav.ExpediteClimbThrough(alt)
}

func (ac *Aircraft) GoodRateDescent() av.CommandIntent {
	return ac.Nav.GoodRateDescent()
}

func (ac *Aircraft) GoodRateClimb() av.CommandIntent {
	return ac.Nav.GoodRateClimb()
}

func (ac *Aircraft) GoodRateThrough(alt float32) av.CommandIntent {
	return ac.Nav.GoodRateThrough(alt)
}

func (ac *Aircraft) AssignHeading(heading int, turn av.TurnDirection, simTime Time, delayReduction time.Duration) av.CommandIntent {
	return ac.Nav.AssignHeading(math.MagneticHeading(heading), turn, simTime.NavTime(), delayReduction)
}

func (ac *Aircraft) TurnLeft(deg int, simTime Time, delayReduction time.Duration) av.CommandIntent {
	hdg := math.OffsetHeading(ac.Nav.FlightState.Heading, float32(-deg))
	ac.Nav.AssignHeading(hdg, av.TurnLeft, simTime.NavTime(), delayReduction)
	return av.HeadingIntent{
		Type:    av.HeadingTurnLeft,
		Heading: hdg,
		Degrees: deg,
	}
}

func (ac *Aircraft) TurnRight(deg int, simTime Time, delayReduction time.Duration) av.CommandIntent {
	hdg := math.OffsetHeading(ac.Nav.FlightState.Heading, float32(deg))
	ac.Nav.AssignHeading(hdg, av.TurnRight, simTime.NavTime(), delayReduction)
	return av.HeadingIntent{
		Type:    av.HeadingTurnRight,
		Heading: hdg,
		Degrees: deg,
	}
}

func (ac *Aircraft) FlyPresentHeading(simTime Time, delayReduction time.Duration) av.CommandIntent {
	return ac.Nav.FlyPresentHeading(simTime.NavTime(), delayReduction)
}

func (ac *Aircraft) ExpectDirect(fix string) av.CommandIntent {
	return ac.Nav.ExpectDirect(strings.ToUpper(fix))
}

func (ac *Aircraft) DirectFix(fix string, turn av.TurnDirection, simTime Time, delayReduction time.Duration) av.CommandIntent {
	return ac.Nav.DirectFix(strings.ToUpper(fix), turn, simTime.NavTime(), delayReduction)
}

func (ac *Aircraft) HoldAtFix(fix string, hold *av.Hold) av.CommandIntent {
	return ac.Nav.HoldAtFix(string(ac.ADSBCallsign), strings.ToUpper(fix), hold)
}

func (ac *Aircraft) DepartFixHeading(fix string, hdg int) av.CommandIntent {
	return ac.Nav.DepartFixHeading(strings.ToUpper(fix), math.MagneticHeading(hdg))
}

func (ac *Aircraft) DepartFixDirect(fixa, fixb string) av.CommandIntent {
	return ac.Nav.DepartFixDirect(strings.ToUpper(fixa), strings.ToUpper(fixb))
}

func (ac *Aircraft) CrossFixAt(fix string, ar *av.AltitudeRestriction, sr *av.SpeedRestriction) av.CommandIntent {
	return ac.Nav.CrossFixAt(strings.ToUpper(fix), ar, sr)
}

func (ac *Aircraft) CrossDistanceFromFixAt(fix string, dist float32, dir math.CardinalOrdinalDirection,
	ar *av.AltitudeRestriction, sr *av.SpeedRestriction) av.CommandIntent {
	return ac.Nav.CrossDistanceFromFixAt(strings.ToUpper(fix), dist, dir, ar, sr)
}

func (ac *Aircraft) CrossDMEAt(dist float32, ar *av.AltitudeRestriction, sr *av.SpeedRestriction) av.CommandIntent {
	return ac.Nav.CrossDMEAt(dist, ar, sr)
}

func (ac *Aircraft) AfterFixSpeed(fix string, sr *av.SpeedRestriction) av.CommandIntent {
	return ac.Nav.AfterFixSpeed(strings.ToUpper(fix), sr)
}

func (ac *Aircraft) AssignCompoundSpeed(segments []av.CompoundSpeedSegment) av.CommandIntent {
	for i := range segments {
		segments[i].UntilFix = strings.ToUpper(segments[i].UntilFix)
	}
	return ac.Nav.AssignCompoundSpeed(segments)
}

func (ac *Aircraft) AfterFixAltitude(fix string, alt float32) av.CommandIntent {
	return ac.Nav.AfterFixAltitude(strings.ToUpper(fix), alt)
}

func (ac *Aircraft) ExpectApproach(id string, ap *av.Airport, lahsoRunway string, lg *log.Logger) av.CommandIntent {
	return ac.Nav.ExpectApproach(ap, id, ac.STARRunwayWaypoints, lahsoRunway, lg)
}

func (ac *Aircraft) AtFixCleared(fix, approach string, straightIn bool) av.CommandIntent {
	return ac.Nav.AtFixCleared(fix, approach, straightIn)
}

func (ac *Aircraft) AtFixIntercept(fix string, lg *log.Logger) av.CommandIntent {
	return ac.Nav.AtFixIntercept(fix, ac.FlightPlan.ArrivalAirport, lg)
}

func (ac *Aircraft) ClearedApproach(id string, simTime Time, lg *log.Logger) av.CommandIntent {
	return ac.Nav.ClearedApproach(ac.FlightPlan.ArrivalAirport, id, false, simTime.NavTime())
}

func (ac *Aircraft) ClearedVisualApproach(runway string, follow *nav.FollowTraffic, refs []*av.Approach, lahsoRunway string, simTime Time) av.CommandIntent {
	return ac.Nav.ClearedVisualApproach(runway, follow, refs, lahsoRunway, simTime.NavTime())
}

func (ac *Aircraft) ClearedStraightInApproach(id string, simTime Time, lg *log.Logger) av.CommandIntent {
	return ac.Nav.ClearedApproach(ac.FlightPlan.ArrivalAirport, id, true, simTime.NavTime())
}

func (ac *Aircraft) CancelApproachClearance() av.CommandIntent {
	return ac.Nav.CancelApproachClearance()
}

func (ac *Aircraft) ClimbViaSID(simTime Time) av.CommandIntent {
	return ac.Nav.ClimbViaSID(simTime.NavTime())
}

func (ac *Aircraft) DescendViaSTAR(simTime Time) av.CommandIntent {
	return ac.Nav.DescendViaSTAR(simTime.NavTime())
}

func (ac *Aircraft) ResumeOwnNavigation() av.CommandIntent {
	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		return av.MakeUnableIntent("unable. We're IFR")
	} else {
		return ac.Nav.ResumeOwnNavigation()
	}
}

func (ac *Aircraft) AltitudeOurDiscretion() av.CommandIntent {
	if ac.FlightPlan.Rules == av.FlightRulesIFR {
		return av.MakeUnableIntent("unable. We're IFR")
	} else {
		return ac.Nav.AltitudeOurDiscretion()
	}
}

func (ac *Aircraft) ContactTower(lg *log.Logger, freq av.Frequency) (av.CommandIntent, bool) {
	if ac.GotContactTower {
		// No response; they're not on our frequency any more.
		return nil, false
	} else if ac.FlightPlan.Rules == av.FlightRulesVFR {
		// VFR aircraft on flight following can be told to contact tower
		// without needing an approach assignment.
		ac.GotContactTower = true
		return av.ContactTowerIntent{Frequency: freq}, true
	} else if ac.Nav.Approach.Assigned == nil {
		return av.MakeUnableIntent("unable. We haven't been given an approach."), false
	} else if !ac.Nav.Approach.Cleared {
		return av.MakeUnableIntent("unable. We haven't been cleared for the approach."), false
	} else {
		ac.GotContactTower = true
		return av.ContactTowerIntent{Frequency: freq}, true
	}
}

func (ac *Aircraft) InterceptApproach(lg *log.Logger) av.CommandIntent {
	return ac.Nav.InterceptApproach(ac.FlightPlan.ArrivalAirport, lg)
}

func (ac *Aircraft) InitializeArrival(ap *av.Airport, arr *av.Arrival, nmPerLongitude float32, magneticVariation float32,
	model *wx.Model, simTime Time, lg *log.Logger) error {
	ac.STAR = arr.STAR
	ac.STARRunwayWaypoints = arr.RunwayWaypoints[ac.FlightPlan.ArrivalAirport]

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(arr.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation, rand.Make())
	}
	if arr.Route != "" {
		ac.FlightPlan.Route = arr.Route
	} else {
		ac.FlightPlan.Route = "/. " + arr.STAR
	}
	ac.TypeOfFlight = av.FlightTypeArrival

	nav := nav.MakeArrivalNav(ac.ADSBCallsign, arr, ac.FlightPlan, perf, nmPerLongitude, magneticVariation, model,
		simTime.NavTime(), lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	if arr.ExpectApproach.A != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
		ac.ExpectApproach(*arr.ExpectApproach.A, ap, "", lg)
	} else if arr.ExpectApproach.B != nil {
		if app, ok := (*arr.ExpectApproach.B)[ac.FlightPlan.ArrivalAirport]; ok {
			lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)), slog.Any("aircraft", ac))
			ac.ExpectApproach(app, ap, "", lg)
		}
	}

	return nil
}

func (ac *Aircraft) InitializeDeparture(ap *av.Airport, departureAirport string, dep *av.Departure,
	runway string, exitRoute av.ExitRoute, nmPerLongitude float32, magneticVariation float32,
	model *wx.Model, simTime Time, lg *log.Logger) error {
	wp := util.DuplicateSlice(exitRoute.Waypoints)
	wp = append(wp, dep.RouteWaypoints...)
	wp = util.FilterSliceInPlace(wp, func(wp av.Waypoint) bool { return !wp.Location.IsZero() })

	if exitRoute.SID != "" {
		ac.SID = exitRoute.SID
		ac.FlightPlan.Route = exitRoute.SID + " " + dep.Route
	} else {
		ac.FlightPlan.Route = dep.Route
	}

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Exit = dep.Exit
	ac.FlightPlan.DepartureRunway = runway

	r := rand.Make()
	idx := rand.SampleFiltered(r, dep.Altitudes, func(alt int) bool { return alt <= int(perf.Ceiling) })
	if idx == -1 {
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation, r)
	} else {
		ac.FlightPlan.Altitude = dep.Altitudes[idx]
	}

	ac.TypeOfFlight = av.FlightTypeDeparture

	randomizeAltitudeRange := ac.FlightPlan.Rules == av.FlightRulesVFR
	nav := nav.MakeDepartureNav(ac.ADSBCallsign, ac.FlightPlan, perf, exitRoute.AssignedAltitude,
		exitRoute.ClearedAltitude, wp, randomizeAltitudeRange,
		nmPerLongitude, magneticVariation, model, simTime.NavTime(), lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) InitializeVFRDeparture(ap *av.Airport, wps av.WaypointArray,
	randomizeAltitudeRange bool, nmPerLongitude float32, magneticVariation float32, model *wx.Model,
	simTime Time, lg *log.Logger) error {
	wp := util.DuplicateSlice(wps)

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.TypeOfFlight = av.FlightTypeDeparture

	nav := nav.MakeDepartureNav(ac.ADSBCallsign, ac.FlightPlan, perf, 0, /* assigned alt */
		ac.FlightPlan.Altitude /* cleared alt */, wp,
		randomizeAltitudeRange, nmPerLongitude, magneticVariation, model, simTime.NavTime(), lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav
	ac.Nav.Check(lg)

	return nil
}

func (ac *Aircraft) InitializeOverflight(of *av.Overflight, nmPerLongitude float32,
	magneticVariation float32, model *wx.Model, simTime Time, lg *log.Logger) error {
	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	ac.FlightPlan.Altitude = int(of.CruiseAltitude)
	if ac.FlightPlan.Altitude == 0 { // unspecified
		ac.FlightPlan.Altitude =
			PlausibleFinalAltitude(ac.FlightPlan, perf, nmPerLongitude, magneticVariation, rand.Make())
	}
	ac.FlightPlan.Route = of.Waypoints.RouteString()
	ac.TypeOfFlight = av.FlightTypeOverflight

	nav := nav.MakeOverflightNav(ac.ADSBCallsign, of, ac.FlightPlan, perf, nmPerLongitude,
		magneticVariation, model, simTime.NavTime(), lg)
	if nav == nil {
		return fmt.Errorf("error initializing Nav")
	}
	ac.Nav = *nav

	return nil
}

func (ac *Aircraft) NavSummary(model *wx.Model, simTime Time, lg *log.Logger) string {
	return ac.Nav.Summary(ac.FlightPlan, model, simTime.NavTime(), lg)
}

func (ac *Aircraft) ContactMessage(reportingPoints []av.ReportingPoint) *av.RadioTransmission {
	// For departures, only report heading if the runway has varied exit headings.
	// For arrivals (and others), always report heading if assigned.
	reportHeading := !ac.IsDeparture() || ac.ReportDepartureHeading
	var runway string
	if ac.Nav.Approach.Assigned != nil {
		runway = ac.Nav.Approach.Assigned.Runway
	}
	return ac.Nav.ContactMessage(reportingPoints, ac.STAR, runway, reportHeading, ac.IsDeparture())
}

func (ac *Aircraft) DepartOnCourse(simTime Time, lg *log.Logger) {
	if ac.FlightPlan.Exit == "" {
		lg.Warn(`unset "exit" for departure`, slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}
	ac.Nav.DepartOnCourse(float32(ac.FlightPlan.Altitude), string(ac.FlightPlan.Exit), simTime.NavTime())
}

func (ac *Aircraft) Check(lg *log.Logger) {
	ac.Nav.Check(lg)
}

func (ac *Aircraft) Position() math.Point2LL {
	return ac.Nav.FlightState.Position
}

func (ac *Aircraft) Altitude() float32 {
	return ac.Nav.FlightState.Altitude
}

func (ac *Aircraft) Heading() math.MagneticHeading {
	return ac.Nav.FlightState.Heading
}

func (ac *Aircraft) NmPerLongitude() float32 {
	return ac.Nav.FlightState.NmPerLongitude
}

func (ac *Aircraft) MagneticVariation() float32 {
	return ac.Nav.FlightState.MagneticVariation
}

func (ac *Aircraft) IsAirborne() bool {
	return ac.Nav.IsAirborne()
}

func (ac *Aircraft) IAS() float32 {
	return ac.Nav.FlightState.IAS
}

func (ac *Aircraft) GS() float32 {
	return ac.Nav.FlightState.GS
}

func (ac *Aircraft) OnApproach(checkAltitude bool) bool {
	return ac.Nav.OnApproach(checkAltitude)
}

func (ac *Aircraft) OnExtendedCenterline(maxNmDeviation float32) bool {
	return ac.Nav.OnExtendedCenterline(maxNmDeviation)
}

func (ac *Aircraft) DepartureAirportElevation() float32 {
	return ac.Nav.FlightState.DepartureAirportElevation
}

func (ac *Aircraft) ArrivalAirportElevation() float32 {
	return ac.Nav.FlightState.ArrivalAirportElevation
}

func (ac *Aircraft) DepartureAirportLocation() math.Point2LL {
	return ac.Nav.FlightState.DepartureAirportLocation
}

func (ac *Aircraft) ArrivalAirportLocation() math.Point2LL {
	return ac.Nav.FlightState.ArrivalAirportLocation
}

func (ac *Aircraft) ATPAVolume() *av.ATPAVolume {
	return ac.Nav.Approach.ATPAVolume
}

func (ac *Aircraft) MVAsApply() bool {
	return !ac.OnApproach(true)
}

func (ac *Aircraft) AircraftPerformance() av.AircraftPerformance {
	return ac.Nav.Perf
}

func (ac *Aircraft) RouteIncludesFix(fix string) bool {
	return slices.ContainsFunc(ac.Nav.Waypoints, func(w av.Waypoint) bool { return w.Fix == fix })
}

func (ac *Aircraft) DistanceToEndOfApproach() (float32, error) {
	return ac.Nav.DistanceToEndOfApproach()
}

func (ac *Aircraft) Waypoints() []av.Waypoint {
	return ac.Nav.Waypoints
}

func (ac *Aircraft) DistanceAlongRoute(fix string) (float32, error) {
	return ac.Nav.DistanceAlongRoute(fix)
}

func (ac *Aircraft) CWT() string {
	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		return "NOWGT"
	}
	cwt := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "NOWGT"}
	if !slices.Contains(cwt, perf.Category.CWT) {
		return "NOWGT"
	}
	return perf.Category.CWT
}

func PlausibleFinalAltitude(fp av.FlightPlan, perf av.AircraftPerformance, nmPerLongitude float32, magneticVariation float32,
	r *rand.Rand) int {
	// try to figure out direction of flight
	dep, dok := av.DB.Airports[fp.DepartureAirport]
	arr, aok := av.DB.Airports[fp.ArrivalAirport]
	if !dok || !aok {
		if fp.Rules == av.FlightRulesIFR {
			return 34000
		} else {
			return 12500
		}
	}

	// Pick a base altitude in thousands and a sampling delta. Odd altitude
	// for now; we deal with direction of flight later.
	pDep, pArr := dep.Location, arr.Location
	alt, delta := 0, 2
	if math.NMDistance2LL(pDep, pArr) < 50 {
		alt = 5
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			alt += 2
		}
	} else if math.NMDistance2LL(pDep, pArr) < 100 {
		alt = 9
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			alt += 2
		}
	} else if math.NMDistance2LL(pDep, pArr) < 200 {
		alt = 15
		delta = 2
		if dep.Elevation > 3000 || arr.Elevation > 3000 {
			alt += 2
		}
	} else if math.NMDistance2LL(pDep, pArr) < 300 {
		alt = 21
		delta = 2
	} else {
		alt = 35
		delta = 3

	}

	// Randomize the altitude a bit
	alt = r.IntRange(alt-delta, alt+delta)

	// Round ceiling down to odd 1000s.
	ceiling := int(perf.Ceiling) / 1000
	if ceiling%2 == 0 {
		ceiling--
	}

	// Enforce ceiling
	alt = min(alt, ceiling)

	if fp.Rules == av.FlightRulesVFR {
		alt = min(alt, 17) // VFRs stay out of class A airspace
	}

	if math.TrueToMagnetic(math.Heading2LL(pDep, pArr, nmPerLongitude), magneticVariation) > 180 {
		// Decrease rather than increasing so that we don't potentially go
		// above the aircraft's ceiling.
		alt--
	}

	altitude := alt * 1000

	if fp.Rules == av.FlightRulesVFR {
		altitude += 500
	}

	return altitude
}

func (ac *Aircraft) IsDeparture() bool {
	return ac.TypeOfFlight == av.FlightTypeDeparture
}

func (ac *Aircraft) IsArrival() bool {
	return ac.TypeOfFlight == av.FlightTypeArrival
}

func (ac *Aircraft) IsOverflight() bool {
	return ac.TypeOfFlight == av.FlightTypeOverflight
}

func (ac *Aircraft) WillDoAirwork() bool {
	return ac.Nav.Airwork != nil ||
		slices.ContainsFunc(ac.Nav.Waypoints, func(wp av.Waypoint) bool { return wp.AirworkRadius() > 0 })
}

func (ac *Aircraft) IsUnassociated() bool {
	return ac.NASFlightPlan == nil
}

func (ac *Aircraft) IsAssociated() bool {
	return ac.NASFlightPlan != nil
}

func (ac *Aircraft) AssociateFlightPlan(fp *NASFlightPlan) {
	fp.Location = math.Point2LL{} // clear location in case it was an unsupported DB
	ac.NASFlightPlan = fp
}

func (ac *Aircraft) DisassociateFlightPlan() *NASFlightPlan {
	fp := ac.NASFlightPlan
	ac.NASFlightPlan = nil
	return fp
}

func (ac *Aircraft) DivertToAirport(ap string) {
	ac.FlightPlan.ArrivalAirport = ap
	ac.TypeOfFlight = av.FlightTypeArrival

	ac.Nav.DivertToAirport(ap)
}

///////////////////////////////////////////////////////////////////////////
// VoiceAssigner

// AirlineVoices maps airline ICAO codes (or comma-separated codes) to voice names.
// "default" is used for callsigns that don't match any specific airline.
// Edit this map to customize which voices are used for each airline.
var AirlineVoices = map[string][]string{
	"default": {
		"af_alloy", "af_aoede", "af_bella", "af_heart", "af_nova", "af_kore",
		"af_river", "af_sarah", "af_sky", "am_adam", "am_echo", "am_eric", "am_fenrir", "am_liam",
		"am_michael", "am_onyx", "am_puck",
	},
	"BAW,VIR,DLH,KQA": {
		"bf_alice", "bf_emma", "bf_isabella", "bf_lily",
		"bm_daniel", "bm_fable", "bm_george", "bm_lewis",
	},
	"AMX,IBE": {
		"ef_dora", "em_alex",
	},
	"AFR": {
		"ff_siwis",
	},
	"ITY,LOT": {
		"if_sara", "im_nicola",
	},
	"TAP,TAM,ELY": {
		"pf_dora", "pm_alex", "pm_santa",
	},
	"AIC": {
		"hf_alpha", "hf_beta", "hm_omega", "hm_psi",
	},
	"JAL,ANA,KAL": {
		"jf_alpha", "jf_gongitsune", "jf_nezumi", "jf_tebukuro", "jm_kumo",
	},
	"CAL,CCA,CES,CSN,CXA,SIA": {
		"zf_xiaobei", "zf_xiaoni", "zf_xiaoxiao", "zm_yunjian", "zm_yunxi", "zm_yunxia", "zm_yunyang",
	},
}

// VoiceAssigner manages the pool of available TTS voice names and assigns them
// to aircraft callsigns. Each aircraft gets a consistent voice throughout
// the session.
type VoiceAssigner struct {
	// Same keys as AirlineVoices, shuffled and consumed FIFO.
	VoicePools map[string][]string
	// Callsign -> voice name mapping
	AircraftVoices map[av.ADSBCallsign]string
}

// NewVoiceAssigner creates a new VoiceAssigner with airline-based voice pools.
func NewVoiceAssigner(r *rand.Rand) *VoiceAssigner {
	va := &VoiceAssigner{
		VoicePools:     maps.Clone(AirlineVoices),
		AircraftVoices: make(map[av.ADSBCallsign]string),
	}

	for voices := range maps.Values(va.VoicePools) {
		rand.ShuffleSlice(voices, r)
	}

	return va
}

// GetVoice returns the voice name assigned to an aircraft, assigning one if needed.
func (va *VoiceAssigner) GetVoice(callsign av.ADSBCallsign, r *rand.Rand) string {
	// Check if already assigned
	if voiceName, ok := va.AircraftVoices[callsign]; ok {
		return voiceName
	}

	getVoice := func(callsigns string) string {
		voices := va.VoicePools[callsigns]
		if len(voices) == 0 {
			voices = slices.Clone(AirlineVoices[callsigns])
			rand.ShuffleSlice(voices, r)
		}

		voice := voices[0]
		va.VoicePools[callsigns] = voices[1:]
		va.AircraftVoices[callsign] = voice
		return voice
	}

	if len(callsign) > 3 {
		icao := string(callsign[:3])
		for callsigns := range va.VoicePools {
			if util.SeqContains(strings.SplitSeq(callsigns, ","), icao) {
				return getVoice(callsigns)
			}
		}
	}

	return getVoice("default")
}

type AircraftDisplayState struct {
	Spew        string // for debugging
	FlightState string // for display when paused
}
