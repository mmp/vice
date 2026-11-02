// nav/report.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package nav

import (
	"fmt"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/speech"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// What the pilot reports: the check-in transmissions a departure or an
// arrival makes on contact, and the summary of the aircraft's state shown
// when hovering over it on the scope.

type contactCrossingRestriction struct {
	Fix            string
	AltRestriction *av.AltitudeRestriction
	Speed          *av.SpeedRestriction
}

// Full human-readable summary of nav state for use when paused and mouse
// hover on the scope. Its phrasing is drawn from r rather than from the
// aircraft's own generator, so that asking for one doesn't change what the
// aircraft does next.
func (nav *Nav) Summary(rules av.FlightRules, model *wx.Model, simTime Time, r *rand.Rand, lg *log.Logger) string {
	var lines []string
	if nav.Altitude.Assigned != nil {
		if math.Abs(nav.FlightState.Altitude-*nav.Altitude.Assigned) < 100 {
			lines = append(lines, "At assigned altitude "+
				av.FormatAltitude(*nav.Altitude.Assigned))
		} else {
			line := "At " + av.FormatAltitude(nav.FlightState.Altitude) + " for " +
				av.FormatAltitude(*nav.Altitude.Assigned)
			line += nav.rateSummary()
			lines = append(lines, line)
		}
	} else if nav.Altitude.AfterSpeed != nil {
		dir := util.Select(*nav.Altitude.AfterSpeed > nav.FlightState.Altitude, "climb", "descend")
		rateSuffix := ""
		switch nav.Altitude.RateAfterSpeed {
		case RateGood:
			rateSuffix = ", good rate"
		case RateExpedite:
			rateSuffix = ", expediting"
		}
		lines = append(lines, fmt.Sprintf("At %s, %s to %s"+rateSuffix,
			*nav.Altitude.AfterSpeedSpeed, dir, av.FormatAltitude(*nav.Altitude.AfterSpeed)))
	} else if target, ok := nav.findAltitudeTarget(); ok {
		alt := nav.limitAltitude(target.altitude)
		switch {
		case alt > nav.FlightState.Altitude:
			lines = append(lines, "Climbing to "+av.FormatAltitude(alt)+" for alt. restriction at "+target.fix)
		case alt < nav.FlightState.Altitude:
			if tgt, _, _ := nav.TargetAltitude(); tgt < nav.FlightState.Altitude {
				lines = append(lines, "Descending to "+av.FormatAltitude(alt)+" for alt. restriction at "+target.fix)
			} else {
				lines = append(lines, "At "+av.FormatAltitude(nav.FlightState.Altitude)+"; will descend to "+
					av.FormatAltitude(alt)+" for alt. restriction at "+target.fix)
			}
		case nav.Altitude.Cleared != nil && alt == nav.Altitude.Cleared.Altitude:
			lines = append(lines, "Holding cleared altitude "+av.FormatAltitude(alt)+"; alt. restriction at "+target.fix)
		default:
			lines = append(lines, "At "+av.FormatAltitude(alt)+" for alt. restriction at "+target.fix)
		}
	} else if c := nav.Altitude.Cleared; c != nil {
		if math.Abs(nav.FlightState.Altitude-c.Altitude) < 100 {
			lines = append(lines, "At cleared altitude "+
				av.FormatAltitude(c.Altitude))
		} else {
			line := "At " + av.FormatAltitude(nav.FlightState.Altitude) + " for " +
				av.FormatAltitude(c.Altitude)
			line += nav.rateSummary()
			lines = append(lines, line)
		}
	} else if nav.Altitude.Restriction != nil {
		tgt := nav.Altitude.Restriction.TargetAltitude(nav.FlightState.Altitude)
		tgt = min(tgt, nav.FinalAltitude)

		switch {
		case tgt < nav.FlightState.Altitude:
			lines = append(lines, "Descending "+av.FormatAltitude(nav.FlightState.Altitude)+
				" to "+av.FormatAltitude(tgt)+" from previous crossing restriction")
		case tgt > nav.FlightState.Altitude:
			lines = append(lines, "Climbing "+av.FormatAltitude(nav.FlightState.Altitude)+
				" to "+av.FormatAltitude(tgt)+" from previous crossing restriction")
		default:
			lines = append(lines, "At "+av.FormatAltitude(tgt)+" from previous crossing restriction")
		}
	}
	if nav.FlightState.Altitude < nav.FlightState.PrevAltitude {
		lines = append(lines, fmt.Sprintf("Descent rate %.0f ft/minute", 60*(nav.FlightState.PrevAltitude-nav.FlightState.Altitude)))
	} else if nav.FlightState.Altitude > nav.FlightState.PrevAltitude {
		lines = append(lines, fmt.Sprintf("Climb rate %.0f ft/minute", 60*(nav.FlightState.Altitude-nav.FlightState.PrevAltitude)))
	}

	// Heading
	if nav.Heading.Assigned != nil {
		if *nav.Heading.Assigned == nav.FlightState.Heading {
			lines = append(lines, fmt.Sprintf("On assigned %03d heading",
				int(*nav.Heading.Assigned)))
		} else {
			lines = append(lines, fmt.Sprintf("Turning from %03d to assigned %03d heading bank angle %d",
				int(nav.FlightState.Heading), int(*nav.Heading.Assigned), int(nav.FlightState.BankAngle)))
		}
	}
	if hold := nav.Heading.Hold; hold != nil {
		lines = append(lines, fmt.Sprintf("Flying hold at %s, %s entry, step: %s",
			hold.Hold.DisplayName(), hold.Entry.String(), hold.currentStep()))
	}
	if aw := nav.Airwork; aw != nil {
		lines = append(lines, fmt.Sprintf("Airwork: practicing %s, %d minutes left", aw.Maneuver,
			max(0, aw.RemainingSteps/60)))
	}
	if maneuvers := nav.Heading.Maneuvers; len(maneuvers) > 0 {
		inPT := len(nav.Waypoints) > 0 && nav.Waypoints[0].ProcedureTurn() != nil
		lines = append(lines, util.Select(inPT, "Procedure turn", "Maneuver")+": "+maneuvers[0].String())
		for _, m := range maneuvers[1:] {
			lines = append(lines, "  then "+m.String())
		}
	}
	if dh := nav.DeferredNavHeading; dh != nil {
		if len(dh.Maneuvers) > 0 {
			lines = append(lines, "Will shortly "+dh.Maneuvers[0].String())
		} else if len(dh.Waypoints) > 0 {
			lines = append(lines, fmt.Sprintf("Will shortly go direct %s", dh.Waypoints[0].Fix))
		} else if dh.Heading != nil {
			lines = append(lines, fmt.Sprintf("Will shortly start flying heading %03d", int(*dh.Heading)))
		} else {
			lines = append(lines, "Will shortly proceed on course/fly the current SID/STAR")
		}
		if dh.Hold != nil {
			lines = append(lines, fmt.Sprintf("Will shortly enter hold at %s", dh.Hold.Hold.DisplayName()))
		}
	}

	// weather
	wxs := model.Lookup(nav.FlightState.Position, nav.FlightState.Altitude, simTime.Time())
	lines = append(lines, wxs.String())
	if nav.FlightState.Altitude > nav.FlightState.PrevAltitude {
		lines = append(lines, fmt.Sprintf("Weather-based climb rate factor %.2fx", atmosClimbFactor(nav.Perf, wxs)))
	}

	// Speed; don't be as exhaustive as we are for altitude
	targetAltitude, _, _ := nav.TargetAltitude()
	lines = append(lines, fmt.Sprintf("IAS %d GS %d TAS %d", int(nav.FlightState.IAS),
		int(nav.FlightState.GS), int(nav.TAS(wxs.Temperature()))))
	ias, _ := nav.TargetSpeed(targetAltitude, rules, wxs, nil, false)
	if nav.Speed.MaintainSlowestPractical {
		lines = append(lines, fmt.Sprintf("Maintain slowest practical speed: %.0f kts", ias))
	} else if nav.Speed.MaintainMaximumForward {
		lines = append(lines, fmt.Sprintf("Maintain maximum forward speed: %.0f kts", ias))
	} else if ias != nav.FlightState.IAS {
		lines = append(lines, fmt.Sprintf("Speed %.0f kts to %.0f", nav.FlightState.IAS, ias))
	} else if sr := nav.Speed.Assigned; sr != nil {
		lines = append(lines, fmt.Sprintf("Maintaining %s assignment", sr))
	} else if nav.Speed.AfterAltitude != nil && nav.Speed.AfterAltitudeAltitude != nil {
		lines = append(lines, fmt.Sprintf("At %s, maintain %s", av.FormatAltitude(*nav.Speed.AfterAltitudeAltitude),
			nav.Speed.AfterAltitude))
	}

	for fix, nfa := range util.SortedMap(nav.FixAssignments) {
		if nfa.Arrive.Altitude != nil || nfa.Arrive.Speed != nil {
			line := "Cross " + fix + " "
			if nfa.Arrive.Altitude != nil {
				ar := speech.MakeReadbackTransmission("{altrest}", nfa.Arrive.Altitude)
				if rd, err := ar.Render(r); err != nil {
					lg.Errorf("%v", err)
				} else {
					line += rd.Written + " "
				}
			}
			if nfa.Arrive.Speed != nil {
				line += "at " + nfa.Arrive.Speed.String()
			}
			lines = append(lines, line)
		}
		if nfa.Depart.Heading != nil && nav.Heading.Assigned == nil {
			lines = append(lines, fmt.Sprintf("Depart "+fix+" heading %03d",
				int(*nfa.Depart.Heading)))
		}
		if nfa.Depart.Speed != nil {
			lines = append(lines, fmt.Sprintf("After %s maintain %s", fix, nfa.Depart.Speed))
		}
		if nfa.Depart.Altitude != nil {
			alt := *nfa.Depart.Altitude
			verb := "climb"
			if alt < nav.FlightState.Altitude {
				verb = "descend"
			}
			lines = append(lines, fmt.Sprintf("After %s %s and maintain %s", fix, verb, av.FormatAltitude(alt)))
		}
	}

	// Approach
	if nav.Approach.Assigned != nil {
		verb := util.Select(nav.Approach.Cleared, "Cleared", "Assigned")
		if nav.Approach.Cleared && nav.Approach.NoPT {
			verb += " straight-in"
		}
		line := verb + " " + nav.Approach.Assigned.FullName
		switch nav.Approach.InterceptState {
		case NotIntercepting:
			// nada
		case InitialHeading:
			line += ", will join the approach"
		case TurningToJoin:
			line += ", turning to join the approach"
		case OnApproachCourse:
			line += ", established on the approach"
		}
		lines = append(lines, line)
	}

	lines = append(lines, "Route flying: "+av.WaypointArray(nav.Waypoints).Encode())
	if nav.hasDeferredRoute() {
		lines = append(lines, "Route assigned: "+av.WaypointArray(nav.DeferredNavHeading.Waypoints).Encode())
	}

	return strings.Join(lines, "\n")
}

// procedureHasAltRestrictions returns whether any remaining waypoints on the
// SID (if checkSID) or STAR (otherwise) have altitude restrictions.
func (nav *Nav) procedureHasAltRestrictions(checkSID bool) bool {
	for i := range nav.Waypoints {
		onProc := util.Select(checkSID, nav.Waypoints[i].OnSID(), nav.Waypoints[i].OnSTAR())
		if onProc && nav.Waypoints[i].AltitudeRestriction() != nil {
			return true
		}
	}
	return false
}

// altitudePhrasing returns the format and arguments that report the
// aircraft's altitude against the one it is climbing or descending to, or
// has reached.
func (nav *Nav) altitudePhrasing(targetAlt float32) (string, []any) {
	cur := nav.FlightState.Altitude
	diff := targetAlt - cur

	switch {
	case diff > 200:
		return "[leaving|out of] {alt} [climbing|for] {alt}", []any{cur, targetAlt}
	case diff < -200:
		return "[leaving|out of] {alt} [descending to|for] {alt}", []any{cur, targetAlt}
	case math.Abs(diff) > 50:
		return "[leveling|at] {alt}", []any{targetAlt}
	default:
		return "[at|] {alt}", []any{targetAlt}
	}
}

// addAltitudePhrasing appends realistic altitude reporting to the
// transmission based on the aircraft's current flight state.
func (nav *Nav) addAltitudePhrasing(rt *speech.RadioTransmission, targetAlt float32) {
	format, args := nav.altitudePhrasing(targetAlt)
	rt.Add(format, args...)
}

func (nav *Nav) DepartureMessage(sid string, reportHeading bool) *speech.RadioTransmission {
	rt := &speech.RadioTransmission{Type: speech.RadioTransmissionContact}

	target := nav.Altitude.Assigned
	if target == nil && nav.Altitude.Cleared != nil {
		target = &nav.Altitude.Cleared.Altitude
	}
	climbing := target != nil && *target-nav.FlightState.Altitude > 200

	if sid != "" && climbing {
		if nav.Altitude.Assigned == nil && nav.procedureHasAltRestrictions(true) {
			// SID with altitude restrictions, climbing via; an altitude
			// short of cruise is an "except maintain" and is reported.
			if *target < nav.FinalAltitude {
				rt.Add("[leaving|out of] {alt} for {alt} climbing via the {sid} [departure|]",
					nav.FlightState.Altitude, *target, sid)
			} else {
				rt.Add("[leaving|out of] {alt} climbing via the {sid} [departure|]",
					nav.FlightState.Altitude, sid)
			}
		} else {
			// An assigned altitude cancels the SID's altitude restrictions,
			// so the climb is reported as any other altitude assignment.
			rt.Add("[leaving|out of] {alt} [for|climbing] {alt} [on the {sid} departure|]",
				nav.FlightState.Altitude, *target, sid)
		}
	} else if climbing {
		// No SID, just climbing
		rt.Add("[leaving|out of] {alt} [for|climbing] {alt}",
			nav.FlightState.Altitude, *target)
	} else {
		// At altitude or leveling
		if target != nil {
			nav.addAltitudePhrasing(rt, *target)
		} else {
			rt.Add("[at|] {alt}", nav.FlightState.Altitude)
		}
	}

	// Heading information for departures with varied exit headings
	if reportHeading {
		hdg, state := nav.DepartureHeading()
		switch state {
		case OnHeading:
			rt.Add("[heading {hdg}|on a {hdg} heading]", hdg)
		case TurningToHeading:
			rt.Add("[turning to|about to turn to] [heading|] {hdg}", hdg)
		}
	}

	return rt
}

func (nav *Nav) ContactMessage(star string, runway string, reportHeading bool,
	isDeparture bool) *speech.RadioTransmission {
	var resp speech.RadioTransmission

	// Find the first applicable fix assignment for reporting
	crossing := nav.firstCrossingRestriction()

	if isDeparture && reportHeading {
		// Departure with varied headings
		hdg, state := nav.DepartureHeading()
		switch state {
		case OnHeading:
			resp.Add("[heading {hdg}|on a {hdg} heading]", hdg)
		case TurningToHeading:
			resp.Add("[turning to|about to turn to] [heading|] {hdg}", hdg)
		}
		// Add altitude after heading for departures
		nav.addContactAltitude(&resp, star, crossing)
	} else if hdg, ok := nav.AssignedHeading(); ok && reportHeading {
		// Being vectored - heading + altitude
		resp.Add("[heading {hdg}|on a {hdg} heading]", hdg)
		nav.addContactAltitude(&resp, "", crossing)
	} else if star != "" && len(nav.Waypoints) > 0 && nav.Waypoints[0].OnSTAR() {
		// On a STAR
		nav.addStarAltitude(&resp, star, crossing)
	} else {
		// Simple altitude
		nav.addContactAltitude(&resp, "", crossing)
	}

	// Runway assignment
	if runway != "" {
		resp.Add("[runway {rwy}|for runway {rwy}]", runway)
	}

	// Speed assignment
	if sr := nav.Speed.Assigned; sr != nil {
		spd := sr.Speed()
		if _, exact := sr.ExactValue(); exact {
			resp.Add("[assigned {airspeed}|speed {airspeed}]", spd)
		} else if sr.Range[1] == av.MaxRestrictionSpeed {
			resp.Add("[{airspeed} or greater|at or above {airspeed}]", spd)
		} else if sr.Range[0] == 0 {
			resp.Add("[not exceeding {airspeed}|at or below {airspeed}]", spd)
		}
	}

	return &resp
}

// firstCrossingRestriction finds the first controller-assigned crossing
// restriction in the remaining waypoints.
func (nav *Nav) firstCrossingRestriction() *contactCrossingRestriction {
	for _, wp := range nav.AssignedWaypoints() {
		if wp.SyntheticCrossing() {
			if ar, sr := wp.AltitudeRestriction(), wp.SpeedRestriction(); ar != nil || sr != nil {
				return &contactCrossingRestriction{
					Fix:            wp.Fix,
					AltRestriction: ar,
					Speed:          sr,
				}
			}
		}

		fa, ok := nav.FixAssignments[wp.Fix]
		if !ok || (fa.Arrive.Altitude == nil && fa.Arrive.Speed == nil) {
			continue
		}

		return &contactCrossingRestriction{
			Fix:            wp.Fix,
			AltRestriction: fa.Arrive.Altitude,
			Speed:          fa.Arrive.Speed,
		}
	}
	return nil
}

// addStarAltitude adds combined STAR + altitude phraseology.
func (nav *Nav) addStarAltitude(rt *speech.RadioTransmission, star string, crossing *contactCrossingRestriction) {
	hasAltRestrictions := nav.procedureHasAltRestrictions(false)
	cur := nav.FlightState.Altitude
	cleared := nav.Altitude.Cleared
	// An aircraft held at, or climbing back to, its "except maintain"
	// altitude isn't descending.
	descending := nav.Altitude.Assigned == nil && hasAltRestrictions && (cleared == nil || cleared.Altitude < cur-100)

	if descending && crossing != nil && crossing.AltRestriction != nil {
		// Descending via STAR with a controller fix assignment exception
		format, args := crossingInstructionFormat(crossing, crossingAltitude(crossing), true)
		rt.Add("[leaving|out of] {alt} descending via [the|] {star} [arrival|] except "+format,
			append([]any{cur, star}, args...)...)
	} else if descending && cleared != nil {
		// Descending via STAR, stopping at a cleared altitude
		rt.Add("[leaving|out of] {alt} for {alt} descending via the {star} [arrival|]",
			cur, cleared.Altitude, star)
	} else if descending {
		// Descending via STAR, no override
		rt.Add("[leaving|out of] {alt} descending via the {star} [arrival|]", cur, star)
	} else if nav.Altitude.Assigned != nil {
		// On STAR with a controller-assigned altitude, which "assigned"
		// tells apart from one the procedure publishes.
		format, args := nav.altitudePhrasing(*nav.Altitude.Assigned)
		rt.Add("on the {star} "+format+" [assigned|]", append([]any{star}, args...)...)
	} else {
		// On STAR at altitude
		rt.Add("on the {star} [at|] {alt}", star, cur)
	}

	if crossing != nil && crossing.AltRestriction == nil {
		format, args := crossingInstructionFormat(crossing, 0, false)
		rt.Add(format, args...)
	}
}

// addContactAltitude adds altitude phraseology for non-STAR contexts
// (vectored, departures, etc.). Absent an assigned altitude, it reports the
// altitude the aircraft is flying toward now, not a restriction it hasn't
// started down for yet.
func (nav *Nav) addContactAltitude(rt *speech.RadioTransmission, star string, crossing *contactCrossingRestriction) {
	cur := nav.FlightState.Altitude
	target, _, _ := nav.TargetAltitude()

	if crossing != nil && crossing.AltRestriction != nil && star == "" {
		// Fix crossing restriction without a STAR
		format, args := crossingInstructionFormat(crossing, crossingAltitude(crossing), true)
		lead := util.Select(math.Abs(target-cur) > 200, "[leaving|out of] {alt} ", "[at|] {alt} ")
		rt.Add(lead+format, append([]any{cur}, args...)...)
	} else if nav.Altitude.Assigned != nil {
		nav.addAltitudePhrasing(rt, *nav.Altitude.Assigned)
	} else {
		nav.addAltitudePhrasing(rt, target)
	}

	if crossing != nil && crossing.AltRestriction == nil {
		format, args := crossingInstructionFormat(crossing, 0, false)
		rt.Add(format, args...)
	}
}

func crossingAltitude(crossing *contactCrossingRestriction) float32 {
	alt := crossing.AltRestriction.Range[0]
	if crossing.AltRestriction.Range[1] != av.MaxAltitude {
		alt = crossing.AltRestriction.Range[1]
	}
	return alt
}

func crossingInstructionFormat(crossing *contactCrossingRestriction, altitude float32, includeAltitude bool) (string, []any) {
	targetFmt := "{fix}"
	speedFmt, speedArgs := crossingSpeedFormat(crossing)

	var format string
	args := []any{crossing.Fix}
	if includeAltitude {
		format = "[to cross|crossing] " + targetFmt + " at {alt}" + speedFmt
		args = append(args, altitude)
	} else {
		format = "[to cross|crossing] " + targetFmt + speedFmt
	}
	args = append(args, speedArgs...)
	return format, args
}

func crossingSpeedFormat(crossing *contactCrossingRestriction) (string, []any) {
	if crossing.Speed == nil {
		return "", nil
	}
	if spd, exact := crossing.Speed.ExactAirspeed(); exact {
		return " and {airspeed}", []any{spd}
	}
	phrase, args := speech.SpeedRestrictionPhrase(*crossing.Speed)
	return " " + phrase, args
}

func (nav *Nav) rateSummary() string {
	var s string
	switch nav.Altitude.Rate {
	case RateGood:
		s = ", good rate"
	case RateExpedite:
		s = ", expediting"
	}
	if s != "" && nav.Altitude.RateThrough != nil {
		s += " through " + av.FormatAltitude(*nav.Altitude.RateThrough)
	}
	return s
}
