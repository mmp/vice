// eram/flightplan.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// formatAircraftData formats flight plan field 03: the number of aircraft
// when there is more than one and the heavy indicator, then the type and
// the equipment suffix.
func formatAircraftData(fp *sim.FlightPlan) string {
	var prefix string
	if fp.AircraftCount > 1 {
		prefix = strconv.Itoa(fp.AircraftCount)
	}
	if isHeavy(fp.AircraftType) {
		prefix += "H"
	}
	s := formatTypeAndSuffix(fp)
	if prefix != "" {
		s = prefix + "/" + s
	}
	return s
}

// formatTypeAndSuffix formats the aircraft type and the equipment suffix,
// as in B738/L.
func formatTypeAndSuffix(fp *sim.FlightPlan) string {
	if fp.EquipmentSuffix == "" {
		return fp.AircraftType
	}
	return fp.AircraftType + "/" + fp.EquipmentSuffix
}

// isHeavy reports whether the aircraft type is a heavy or a super, which
// field 03 marks with the heavy indicator.
func isHeavy(actype string) bool {
	weightClass := db.DB.AircraftPerformance[actype].WeightClass
	return weightClass == "H" || weightClass == "J"
}

// formatAssignedAltitude formats the flight plan's assigned altitude in
// hundreds of feet: a single altitude such as 350, a block such as 220B230,
// or a VFR or VFR-on-top altitude such as VFR or OTP/105. A flight
// descending via a STAR has the altitude its previous controller assigned.
func formatAssignedAltitude(fp *sim.FlightPlan) string {
	if fp.HasAltitudeBlock() {
		return formatAltitudeBlock([2]int{fp.AltitudeBlock[0] / 100, fp.AltitudeBlock[1] / 100})
	}
	if q := fp.AssignedAltitudeQualifier; q != sim.PlainAltitude {
		if fp.AssignedAltitude == 0 {
			return q.String()
		}
		return fmt.Sprintf("%s/%03d", q, fp.AssignedAltitude/100)
	}
	return fmt.Sprintf("%03d", util.Select(fp.AssignedAltitude != 0, fp.AssignedAltitude, fp.PerceivedAssigned)/100)
}

// altitudeEntry is an altitude field of a flight plan; see
// parseAltitudeEntry.
type altitudeEntry struct {
	altitude  int // feet
	qualifier sim.AltitudeQualifier
}

// parseAltitudeEntry parses a VFR or VFR-on-top altitude field: VFR,
// VFR/ddd, OTP, or OTP/ddd (8.1.3), with the altitude given in hundreds of
// feet. formatErr is the error for a value that is none of those.
func parseAltitudeEntry(s string, formatErr string) (altitudeEntry, error) {
	q, alt, found := strings.Cut(s, "/")
	e := altitudeEntry{qualifier: map[string]sim.AltitudeQualifier{"VFR": sim.VFRAltitude, "OTP": sim.OTPAltitude}[q]}
	if e.qualifier == sim.PlainAltitude || (found && (len(alt) != 3 || !allDigits(alt))) {
		return e, errors.New(formatErr)
	}
	n, _ := strconv.Atoi(alt)
	e.altitude = n * 100
	return e, nil
}

// setAssigned adds assigning the altitude to spec.
func (e altitudeEntry) setAssigned(spec *sim.FlightPlanSpecifier) {
	spec.AssignedAltitude.Set(e.altitude)
	spec.AssignedAltitudeQualifier.Set(e.qualifier)
}

// formatRoute formats field 10 as a readout shows it, from the departure
// airport to the destination, as in KBOS.SSOXS.SEY.PARCH4.KJFK.
func formatRoute(fp *sim.FlightPlan) string {
	route := slices.Concat([]string{string(fp.DepartureAirport)}, strings.Fields(strings.TrimPrefix(fp.Route, "/. ")),
		[]string{string(fp.ArrivalAirport)})
	return strings.Join(slices.DeleteFunc(route, func(s string) bool { return s == "" }), ".")
}
