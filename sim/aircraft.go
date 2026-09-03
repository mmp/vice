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
	FollowingOnVisualApproach   bool
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
	STAR                  string
	STARRunwayWaypoints   map[string]av.WaypointArray
	GotContactTower       bool
	AskedAboutTowerSwitch bool

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

	// HoldingSince is when a VFR arrival started orbiting to wait for a slot
	// in the pattern; it orders the queue of aircraft waiting to get in.
	// Zero when the aircraft isn't holding.
	HoldingSince Time
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
	// Just check last one since it's the most recent.
	if n := len(ac.SeenTraffic); n > 0 && now.Sub(ac.SeenTraffic[n-1].SightedTime) <= maxAge {
		return &ac.SeenTraffic[n-1]
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

// maybeSetGoAround determines if an arrival should attempt a go-around and
// sets the GoAroundDistance if so. Go-arounds only occur for IFR aircraft
// that will be handed off to a human controller (checked via HumanHandoff
// waypoint), subject to the configured GoAroundRate probability.
func (ac *Aircraft) maybeSetGoAround(goAroundRate float32, r *rand.Rand) {
	if ac.FlightPlan.Rules != av.FlightRulesIFR {
		return // VFRs don't go around since they aren't talking to us
	}
	if r.Float32() >= goAroundRate {
		return // Random chance didn't trigger
	}
	// Only allow go-around if there's human controller involvement
	if !slices.ContainsFunc(ac.Nav.Waypoints, av.Waypoint.HasHumanHandoff) {
		return
	}
	d := r.Float32Range(0.1, 0.7)
	ac.GoAroundDistance = &d
}

// canRequestVisualApproach reports whether an aircraft is eligible to
// spontaneously request the visual approach. The aircraft must be an
// arrival on frequency, assigned a non-visual approach that hasn't been
// cleared yet, and must not have already made the request.
func (ac *Aircraft) canRequestVisualApproach() bool {
	if ac.IsDeparture() || ac.FieldInSight || ac.RequestedVisualApproach || ac.ControllerFrequency == "" {
		return false
	}
	if ac.Nav.Approach.AssignedId == "" || ac.Nav.Approach.EffectivelyCleared() {
		return false
	}
	appr := ac.Nav.Approach.Assigned
	return appr != nil && appr.Type != av.ChartedVisualApproach && appr.Type != av.VisualApproach
}

// canSeeTraffic reports whether traffic is within the pilot's forward
// visibility arc.
func (ac *Aircraft) canSeeTraffic(traffic *Aircraft) bool {
	bearingToTraffic := math.TrueToMagnetic(
		math.Heading2LL(ac.Position(), traffic.Position(), ac.NmPerLongitude()),
		ac.MagneticVariation())
	return math.HeadingDifference(ac.Heading(), bearingToTraffic) <= visualMaxBearingOff
}

// refreshSeenTraffic drops sightings the pilot can no longer act on: those the
// aircraft was told to follow or keep visual separation from are held only
// while the traffic remains visible, and the rest age out.
func (ac *Aircraft) refreshSeenTraffic(now Time, aircraft map[av.ADSBCallsign]*Aircraft) {
	ac.SeenTraffic = util.FilterSliceInPlace(ac.SeenTraffic,
		func(seen SeenAircraft) bool {
			if !seen.MaintainingVisualSeparation && !seen.FollowingOnVisualApproach {
				return now.Sub(seen.SightedTime) <= trafficSightingMaxAge
			}
			traffic, ok := aircraft[seen.Callsign]
			return ok && ac.canSeeTraffic(traffic)
		})
}

// GetSTTFixes returns the raw fix names relevant for STT context.
// For ERAM (enroute) sessions, up to 5 assigned waypoints within 300nm are included. For
// STARS (terminal) sessions, the fixes depend on the type of flight: for departures, the
// SID's waypoints and the exit fix plus the route fixes within 150nm; for arrivals, all
// remaining route waypoints; for overflights, remaining route waypoints within 120nm.
// For aircraft that have been told to expect an approach but haven't joined it yet, all
// of the approach's waypoints are included as well.
func (ac *Aircraft) GetSTTFixes(isERAM bool) []string {
	var fixes []string
	p := ac.Nav.FlightState.Position

	// Include the arrival and departure airports so STT can match airport
	// names (e.g. "Kennedy 12 o'clock 12 miles" for the AP command), but
	// only the ones the aircraft is near: an airport 100nm behind or ahead
	// is never named, and carrying it only costs a slot in the fix
	// vocabulary and in the whisper prompt.
	for _, id := range []string{ac.FlightPlan.ArrivalAirport, ac.FlightPlan.DepartureAirport} {
		if id == "" {
			continue
		}
		if ap, ok := av.DB.LookupAirport(id); !ok || math.NMDistance2LL(p, ap.Location) <= 100 {
			fixes = append(fixes, id)
		}
	}

	if isERAM {
		routeFixes := 0
		for _, wp := range ac.Nav.AssignedWaypoints() {
			if math.NMDistance2LL(p, wp.Location) > 300 && len(fixes) > 0 {
				break
			}
			if av.IsNamedFix(wp.Fix) {
				fixes = append(fixes, wp.Fix)
				routeFixes++
				if routeFixes >= 5 {
					break
				}
			}
		}
	} else if ac.IsDeparture() {
		// Include the SID's waypoints and the exit fix, regardless of how
		// far away they are. The exit fix isn't necessarily among the SID's
		// waypoints (e.g., when the SID ends with a vector to it), though it
		// may also appear both there and in the enroute part of the route,
		// so skip fixes we've already taken. Enroute fixes past the exit
		// come along when they're near enough that a controller might send
		// the aircraft direct to one; Nav.directFixWaypoints applies the
		// same cutoff to fixes that aren't in the route at all.
		const maxDepartureFixDistance = 150
		exit := ac.FlightPlan.Exit.Base()
		for _, wp := range ac.Nav.AssignedWaypoints() {
			if !av.IsNamedFix(wp.Fix) || slices.Contains(fixes, wp.Fix) {
				continue
			}
			if wp.OnSID() || wp.Fix == exit || math.NMDistance2LL(p, wp.Location) <= maxDepartureFixDistance {
				fixes = append(fixes, wp.Fix)
			}
		}
	} else if ac.IsArrival() {
		// Include all remaining route waypoints, regardless of distance.
		for _, wp := range ac.Nav.AssignedWaypoints() {
			if av.IsNamedFix(wp.Fix) && !slices.Contains(fixes, wp.Fix) {
				fixes = append(fixes, wp.Fix)
			}
		}
	} else {
		// Overflights and the rest: remaining route waypoints within 120nm,
		// though always take the first valid one so at least one fix is
		// included.
		haveRouteFix := false
		for _, wp := range ac.Nav.AssignedWaypoints() {
			if !av.IsNamedFix(wp.Fix) || slices.Contains(fixes, wp.Fix) {
				continue
			}
			if !haveRouteFix || math.NMDistance2LL(p, wp.Location) <= 120 {
				fixes = append(fixes, wp.Fix)
				haveRouteFix = true
			}
		}
	}

	if ac.Nav.Approach.Assigned != nil {
		// If the aircraft has been told to expect an approach but hasn't
		// joined it yet, add all of the approach's waypoints; once it has
		// joined, its remaining route carries the remaining approach
		// waypoints, so those suffice.
		joinedApproach := slices.ContainsFunc(ac.Nav.AssignedWaypoints(),
			func(wp av.Waypoint) bool { return wp.OnApproach() })

		if !joinedApproach {
			for _, wps := range ac.Nav.Approach.Assigned.Waypoints {
				for _, wp := range wps {
					if av.IsNamedFix(wp.Fix) && !slices.Contains(fixes, wp.Fix) {
						fixes = append(fixes, wp.Fix)
					}
				}
			}
		}
	}

	return fixes
}

// GetRouteFixes returns the ordered list of fix names from the aircraft's
// assigned route; only the ones that are published fix names (IsNamedFix)
// are included, so lat/long waypoints and internal nav markers are
// excluded. Unlike GetSTTFixes, the list is not distance- or
// count-truncated and the dep/arr airports are not auto-prepended.
func (ac *Aircraft) GetRouteFixes() []string {
	var fixes []string
	for _, wp := range ac.Nav.AssignedWaypoints() {
		if av.IsNamedFix(wp.Fix) {
			fixes = append(fixes, wp.Fix)
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

func (ac *Aircraft) Update(model *wx.Model, simTime Time, arrivalMETAR *wx.METAR, bravo *av.AirspaceGrid, lg *log.Logger) nav.UpdateResult {
	if lg != nil {
		lg = lg.With(slog.String("adsb_callsign", string(ac.ADSBCallsign)))
	}

	navUpdate := ac.Nav.Update(string(ac.ADSBCallsign), model, &ac.FlightPlan, arrivalMETAR, simTime.NavTime(), bravo)
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
	hdg := math.OffsetHeading(ac.Nav.FlightState.Heading, -deg)
	ac.Nav.AssignHeading(hdg, av.TurnLeft, simTime.NavTime(), delayReduction)
	return av.HeadingIntent{
		Type:    av.HeadingTurnLeft,
		Heading: hdg,
		Degrees: deg,
	}
}

func (ac *Aircraft) TurnRight(deg int, simTime Time, delayReduction time.Duration) av.CommandIntent {
	hdg := math.OffsetHeading(ac.Nav.FlightState.Heading, deg)
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

func (ac *Aircraft) InterceptRadial(fix string, radial int, outbound bool, simTime Time,
	delayReduction time.Duration) av.CommandIntent {
	return ac.Nav.InterceptRadial(strings.ToUpper(fix), math.MagneticHeading(radial), outbound,
		simTime.NavTime(), delayReduction)
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

func (ac *Aircraft) ExpectApproach(id string, ap *av.Airport) av.CommandIntent {
	return ac.Nav.ExpectApproach(ap, id, ac.STARRunwayWaypoints)
}

func (ac *Aircraft) AtFixCleared(fix, approach string, simTime Time, delayReduction time.Duration, straightIn bool) av.CommandIntent {
	return ac.Nav.AtFixCleared(fix, approach, simTime.NavTime(), delayReduction, straightIn)
}

func (ac *Aircraft) AtFixIntercept(fix string, simTime Time, delayReduction time.Duration) av.CommandIntent {
	return ac.Nav.AtFixIntercept(fix, simTime.NavTime(), delayReduction)
}

func (ac *Aircraft) ClearedApproach(id string, simTime Time, follow *nav.FollowTraffic) av.CommandIntent {
	return ac.Nav.ClearedApproach(id, follow, simTime.NavTime(), false, "")
}

func (ac *Aircraft) ClearedStraightInApproach(id string, simTime Time, follow *nav.FollowTraffic) av.CommandIntent {
	return ac.Nav.ClearedApproach(id, follow, simTime.NavTime(), true, "")
}

// ClearedApproachAtPassedFix issues the approach clearance a /clearapp route
// action calls for at fix. The aircraft has already crossed fix and dropped it
// from its route, so the approach is joined there rather than at a fix ahead.
func (ac *Aircraft) ClearedApproachAtPassedFix(fix string, simTime Time) av.CommandIntent {
	return ac.Nav.ClearedApproach(ac.Nav.Approach.AssignedId, nil, simTime.NavTime(), false, fix)
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

func (ac *Aircraft) InterceptApproach() av.CommandIntent {
	return ac.Nav.InterceptApproach("")
}

// InterceptApproachAtPassedFix carries out an /intercept route action at fix,
// which the aircraft has already crossed.
func (ac *Aircraft) InterceptApproachAtPassedFix(fix string) av.CommandIntent {
	return ac.Nav.InterceptApproach(fix)
}

func (ac *Aircraft) InitializeArrival(ap *av.Airport, arr *av.Arrival, cruise CruiseLimits,
	nmPerLongitude float32, magneticVariation float32,
	model *wx.Model, simTime Time, lg *log.Logger) error {
	ac.STAR = arr.STAR
	ac.STARRunwayWaypoints = arr.RunwayWaypoints[ac.FlightPlan.ArrivalAirport]

	perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]
	if !ok {
		lg.Errorf("%s: unable to get performance model", ac.FlightPlan.AircraftType)
		return ErrUnknownAircraftType
	}

	r := rand.Make()
	if idx := rand.SampleFiltered(r, arr.CruiseAltitudes, withinCeiling(perf)); idx != -1 {
		ac.FlightPlan.Altitude = arr.CruiseAltitudes[idx]
	} else {
		ac.FlightPlan.Altitude = FiledCruiseAltitude(ac.FlightPlan, perf, cruise, nmPerLongitude,
			magneticVariation, r)
	}
	if arr.FlightStripDisplayRoute != "" {
		ac.FlightPlan.Route = arr.FlightStripDisplayRoute
	} else if arr.STAR != "" {
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
		ac.ExpectApproach(*arr.ExpectApproach.A, ap)
	} else if arr.ExpectApproach.B != nil {
		if app, ok := (*arr.ExpectApproach.B)[ac.FlightPlan.ArrivalAirport]; ok {
			ac.ExpectApproach(app, ap)
		}
	}

	return nil
}

func (ac *Aircraft) InitializeDeparture(ap *av.Airport, departureAirport string, dep *av.Departure,
	runway string, exitRoute av.ExitRoute, cruise CruiseLimits, nmPerLongitude float32,
	magneticVariation float32, model *wx.Model, simTime Time, lg *log.Logger) error {
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
	if idx := rand.SampleFiltered(r, dep.Altitudes, withinCeiling(perf)); idx != -1 {
		ac.FlightPlan.Altitude = dep.Altitudes[idx]
	} else {
		ac.FlightPlan.Altitude = FiledCruiseAltitude(ac.FlightPlan, perf, cruise, nmPerLongitude,
			magneticVariation, r)
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

	r := rand.Make()
	if idx := rand.SampleFiltered(r, of.CruiseAltitudes, withinCeiling(perf)); idx != -1 {
		ac.FlightPlan.Altitude = of.CruiseAltitudes[idx]
	} else {
		cruise := CruiseLimits{Floor: of.Waypoints.AltitudeFloor()}
		ac.FlightPlan.Altitude = FiledCruiseAltitude(ac.FlightPlan, perf, cruise, nmPerLongitude,
			magneticVariation, r)
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

func (ac *Aircraft) ContactMessage() *av.RadioTransmission {
	// For departures, only report heading if the runway has varied exit headings.
	// For arrivals (and others), always report heading if assigned.
	reportHeading := !ac.IsDeparture() || ac.ReportDepartureHeading
	var runway string
	if ac.Nav.Approach.Assigned != nil {
		runway = ac.Nav.Approach.Assigned.Runway
	}
	return ac.Nav.ContactMessage(ac.STAR, runway, reportHeading, ac.IsDeparture())
}

func (ac *Aircraft) DepartOnCourse(simTime Time, lg *log.Logger) {
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

// withinCeiling reports whether a filed altitude is one the aircraft can reach.
func withinCeiling(perf av.AircraftPerformance) func(int) bool {
	return func(alt int) bool { return alt <= int(perf.Ceiling) }
}

// altitudeRange is the band of altitudes, in feet, a filed cruise altitude is
// drawn from. It is narrowed in turn by everything that has a say in it and
// never goes empty, so that there is always something to sample.
type altitudeRange struct{ low, high int }

// above raises the range's low end to a floor the flight has to clear. A floor
// over the top of the range takes it there and no further: an aircraft that
// can't reach a restriction flies as high as it can, not higher.
func (a altitudeRange) above(floor int) altitudeRange {
	return altitudeRange{max(a.low, min(floor, a.high)), a.high}
}

// narrowedTo intersects the range with b, or leaves it alone if the two don't
// meet. b is evidence of where the flight goes rather than a bound on it, so a
// band it cannot be reconciled with--the scraper caught a filing at 2,400 feet
// on a 570nm jet route--is discarded rather than obeyed.
func (a altitudeRange) narrowedTo(b altitudeRange) altitudeRange {
	if n := (altitudeRange{max(a.low, b.low), min(a.high, b.high)}); n.low <= n.high {
		return n
	}
	return a
}

// biasedTo slides b, keeping its width, until it lies within a, and clips what
// is left to a. A band the flight would otherwise never reach thereby moves to
// the nearest part of the range it can have instead of collapsing onto a's
// endpoint: a route that has to be flown at or above 24,000 by an aircraft
// usually flown at 16,000-24,000 files somewhere in 24,000-32,000.
func (a altitudeRange) biasedTo(b altitudeRange) altitudeRange {
	if d := a.low - b.low; d > 0 {
		b.low, b.high = b.low+d, b.high+d
	}
	if d := b.high - a.high; d > 0 {
		b.low, b.high = b.low-d, b.high-d
	}
	return altitudeRange{math.Clamp(b.low, a.low, a.high), math.Clamp(b.high, a.low, a.high)}
}

// sample picks a thousand from the range on the correct side of the
// hemispheric rule: odd thousands on a magnetic course under 180 degrees, even
// thousands on one at or over it. A range holding none of them--it has
// narrowed onto a single even thousand on an eastbound flight--gives the
// nearest thousand of the right parity above it, or the one below when that is
// over the aircraft's ceiling.
func (a altitudeRange) sample(r *rand.Rand, course math.MagneticHeading, ceiling int) int {
	odd := util.Select(course < 180, 1, 0)
	low, high := (a.low+999)/1000, a.high/1000
	if low%2 != odd {
		low++
	}
	if high%2 != odd {
		high--
	}
	if low > high {
		return 1000 * util.Select(low*1000 <= ceiling, low, high)
	}
	return 1000 * (low + 2*r.Intn((high-low)/2+1))
}

// maxCruiseDistance is longer than any flight, so that the last cruise band
// always matches.
const maxCruiseDistance = 1e6 // nm

// cruiseBand is the altitudes each class of aircraft is really flown at over
// trips no longer than maxDistance.
type cruiseBand struct {
	maxDistance            float32 // nm, inclusive
	jet, turboprop, piston altitudeRange
}

// cruiseBands are the median filed low and median filed high altitude in each
// distance bin of the scraped route database, taken over the routes only one
// class of aircraft was seen flying. The bins past 200nm for the classes that
// rarely fly that far are extrapolated; the aircraft's ceiling covers the ones
// that can't get there at all.
var cruiseBands = []cruiseBand{
	{50, altitudeRange{5000, 7000}, altitudeRange{4000, 5000}, altitudeRange{4000, 5000}},
	{100, altitudeRange{9000, 13000}, altitudeRange{6000, 8000}, altitudeRange{5000, 7000}},
	{200, altitudeRange{16000, 24000}, altitudeRange{12000, 14000}, altitudeRange{6000, 9000}},
	{300, altitudeRange{25000, 31000}, altitudeRange{19000, 22000}, altitudeRange{8000, 11000}},
	{500, altitudeRange{31000, 37000}, altitudeRange{20000, 25000}, altitudeRange{8000, 11000}},
	{maxCruiseDistance, altitudeRange{34000, 38000}, altitudeRange{20000, 25000}, altitudeRange{8000, 11000}},
}

// plausibleCruiseBand returns the altitudes trips like this one are really
// flown at: how far it is going and what the aircraft is.
func plausibleCruiseBand(fp av.FlightPlan, perf av.AircraftPerformance) altitudeRange {
	// Without both airports there is no distance to go on, so take the flight
	// to be a long one.
	d := float32(maxCruiseDistance)
	if dep, ok := av.DB.Airports[fp.DepartureAirport]; ok {
		if arr, ok := av.DB.Airports[fp.ArrivalAirport]; ok {
			d = math.NMDistance2LL(dep.Location, arr.Location)
		}
	}

	band := cruiseBands[slices.IndexFunc(cruiseBands,
		func(b cruiseBand) bool { return d <= b.maxDistance })]

	switch perf.Engine.AircraftType {
	case "J":
		return band.jet
	case "T":
		return band.turboprop
	default:
		return band.piston
	}
}

// terrainFloor is the lowest altitude a flight between the two airports can
// cruise at: 2,000 feet above the higher of the two fields. Only 20 of the
// 4,957 jet routes in the scraped database were ever filed below it.
func terrainFloor(fp av.FlightPlan) int {
	elevation := 0
	if ap, ok := av.DB.Airports[fp.DepartureAirport]; ok {
		elevation = max(elevation, ap.Elevation)
	}
	if ap, ok := av.DB.Airports[fp.ArrivalAirport]; ok {
		elevation = max(elevation, ap.Elevation)
	}
	return elevation + 2000
}

// cruiseCourse is the magnetic course the flight makes good, which decides
// which side of the hemispheric rule its altitude falls on.
func cruiseCourse(fp av.FlightPlan, nmPerLongitude float32, magneticVariation float32) math.MagneticHeading {
	dep, dok := av.DB.Airports[fp.DepartureAirport]
	arr, aok := av.DB.Airports[fp.ArrivalAirport]
	if !dok || !aok {
		return 0
	}
	return math.TrueToMagnetic(math.Heading2LL(dep.Location, arr.Location, nmPerLongitude),
		magneticVariation)
}

// CruiseLimits is what a flight's particular route says about the altitude it
// files, over and above what the trip and the aircraft allow. The zero value
// says nothing.
type CruiseLimits struct {
	// Floor is the highest crossing restriction the route's procedures publish.
	Floor int
	// Low and High are the altitudes the route has really been filed at.
	Low, High int
}

// FiledCruiseAltitude returns the altitude a flight files. It narrows the range
// the flight may cruise in by each thing that has a say: what it can physically
// do, then what its route requires and is really flown at, and last--as a bias
// rather than a bound, since a route observed at 30,000 shouldn't be dragged
// down because trips that long usually are--what trips like it are flown at.
func FiledCruiseAltitude(fp av.FlightPlan, perf av.AircraftPerformance, limits CruiseLimits,
	nmPerLongitude float32, magneticVariation float32, r *rand.Rand) int {
	ceiling := int(perf.Ceiling)
	if fp.Rules == av.FlightRulesVFR {
		ceiling = min(ceiling, 17000) // VFRs stay out of class A airspace
	}
	// What the aircraft can do bounds everything that follows: nothing below
	// narrows the range without also staying inside it.
	a := altitudeRange{min(terrainFloor(fp), ceiling), ceiling}
	if limits.Floor > 0 {
		a = a.above(limits.Floor) // the route's own crossing restrictions
	}
	if limits.Low > 0 {
		a = a.narrowedTo(altitudeRange{limits.Low, limits.High}) // where it is really flown
	}
	a = a.biasedTo(plausibleCruiseBand(fp, perf)) // what trips like it are flown at

	alt := a.sample(r, cruiseCourse(fp, nmPerLongitude, magneticVariation), ceiling)
	if fp.Rules == av.FlightRulesVFR {
		alt += 500
	}
	return alt
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
