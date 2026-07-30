// sim/filters.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type FilterRegion struct {
	av.AirspaceVolume
	InvertTest bool
}

type FilterRegions []FilterRegion

// FilterQualifiers holds qualifying attributes (DMS Table 4-109) shared
// by quicklook and FDAM filter regions.
type FilterQualifiers struct {
	// JSON input fields (comma-delimited strings)
	TCPsString                string `json:"tcps"`
	ScratchpadString          string `json:"scratchpad"`
	SecondaryScratchpadString string `json:"secondary_scratchpad"`
	OwningTCPString           string `json:"owning_tcp"`
	EntryFixString            string `json:"entry_fix"`
	ExitFixString             string `json:"exit_fix"`
	FlightType                string `json:"flight_type"`
	FlightRules               string `json:"flight_rules"`
	CWTCategory               string `json:"cwt_category"`
	SSRCodesString            string `json:"ssr_codes"`
	RequestedAltitudeString   string `json:"requested_altitude"`

	// Parsed runtime fields
	TCPs                 []ControlPosition `json:"-"`
	Scratchpads          []string          `json:"-"`
	SecondaryScratchpads []string          `json:"-"`
	OwningTCPs           []ControlPosition `json:"-"`
	EntryFixes           []string          `json:"-"`
	ExitFixes            []string          `json:"-"`
	SSRCodes             [][2]av.Squawk    `json:"-"`
	RequestedAltitudes   [][2]int          `json:"-"`
}

func (r *FilterQualifiers) PostDeserialize(controlPositions map[TCP]*av.Controller, e *util.ErrorLogger) {
	parseCSV := func(s string) []string {
		if s == "" {
			return nil
		}
		var result []string
		for v := range strings.SplitSeq(s, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				result = append(result, strings.ToUpper(v))
			}
		}
		return result
	}

	parseTCPs := func(s string, field string) []ControlPosition {
		vals := parseCSV(s)
		if len(vals) == 1 && vals[0] == "ALL" {
			var all []ControlPosition
			for tcp, ctrl := range controlPositions {
				if ctrl.FacilityIdentifier == "" {
					all = append(all, tcp)
				}
			}
			return all
		}
		var result []ControlPosition
		for _, v := range vals {
			tcp := ControlPosition(v)
			ctrl, ok := controlPositions[tcp]
			if !ok {
				e.ErrorString("unknown TCP %q in %q", v, field)
			} else if ctrl.FacilityIdentifier != "" {
				e.ErrorString("TCP %q in %q is not a local position", v, field)
			} else {
				result = append(result, tcp)
			}
		}
		return result
	}

	r.TCPs = parseTCPs(r.TCPsString, "tcps")
	r.Scratchpads = parseCSV(r.ScratchpadString)
	r.SecondaryScratchpads = parseCSV(r.SecondaryScratchpadString)
	r.OwningTCPs = parseTCPs(r.OwningTCPString, "owning_tcp")
	r.EntryFixes = parseCSV(r.EntryFixString)
	r.ExitFixes = parseCSV(r.ExitFixString)

	r.FlightType = strings.ToUpper(strings.TrimSpace(r.FlightType))
	if r.FlightType != "" && r.FlightType != "ARRIVAL" && r.FlightType != "DEPARTURE" && r.FlightType != "OVERFLIGHT" {
		e.ErrorString(`invalid "flight_type" %q: must be "arrival", "departure", or "overflight"`, r.FlightType)
	}

	r.FlightRules = strings.ToUpper(strings.TrimSpace(r.FlightRules))
	if r.FlightRules != "" && r.FlightRules != "V" && r.FlightRules != "I" && r.FlightRules != "B" {
		e.ErrorString(`invalid "flight_rules" %q: must be "V", "I", or "B"`, r.FlightRules)
	}

	r.CWTCategory = strings.ToUpper(strings.TrimSpace(r.CWTCategory))
	if r.CWTCategory != "" {
		if len(r.CWTCategory) != 1 || r.CWTCategory[0] < 'A' || r.CWTCategory[0] > 'I' {
			e.ErrorString(`invalid "cwt_category" %q: must be a single letter A-I`, r.CWTCategory)
		}
	}

	// Parse SSR codes: "1200,1300-1377"
	for s := range strings.SplitSeq(r.SSRCodesString, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(s, "-"); ok {
			loSq, err := av.ParseSquawk(lo)
			if err != nil {
				e.ErrorString(`invalid SSR code %q in "ssr_codes": %v`, lo, err)
				continue
			}
			hiSq, err := av.ParseSquawk(hi)
			if err != nil {
				e.ErrorString(`invalid SSR code %q in "ssr_codes": %v`, hi, err)
				continue
			}
			if loSq > hiSq {
				e.ErrorString(`SSR code range %q has low > high in "ssr_codes"`, s)
				continue
			}
			r.SSRCodes = append(r.SSRCodes, [2]av.Squawk{loSq, hiSq})
		} else {
			sq, err := av.ParseSquawk(s)
			if err != nil {
				e.ErrorString(`invalid SSR code %q in "ssr_codes": %v`, s, err)
				continue
			}
			r.SSRCodes = append(r.SSRCodes, [2]av.Squawk{sq, sq})
		}
	}

	// Parse requested altitudes: "40,100-180" (hundreds of feet)
	for s := range strings.SplitSeq(r.RequestedAltitudeString, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(s, "-"); ok {
			loAlt, err := strconv.Atoi(lo)
			if err != nil {
				e.ErrorString(`invalid altitude %q in "requested_altitude": %v`, lo, err)
				continue
			}
			hiAlt, err := strconv.Atoi(hi)
			if err != nil {
				e.ErrorString(`invalid altitude %q in "requested_altitude": %v`, hi, err)
				continue
			}
			if loAlt > hiAlt {
				e.ErrorString(`altitude range %q has low > high in "requested_altitude"`, s)
				continue
			}
			r.RequestedAltitudes = append(r.RequestedAltitudes, [2]int{loAlt, hiAlt})
		} else {
			alt, err := strconv.Atoi(s)
			if err != nil {
				e.ErrorString(`invalid altitude %q in "requested_altitude": %v`, s, err)
				continue
			}
			r.RequestedAltitudes = append(r.RequestedAltitudes, [2]int{alt, alt})
		}
	}
}

func (r FilterQualifiers) Match(fp *NASFlightPlan, userPositions []ControlPosition,
	aircraftType string, significantPoints map[string]SignificantPoint) bool {
	if len(r.TCPs) > 0 {
		if !slices.ContainsFunc(userPositions, func(pos ControlPosition) bool {
			return slices.Contains(r.TCPs, pos)
		}) {
			return false
		}
	}

	// Flight plan-dependent checks: if fp is nil, skip them (pass).
	if fp != nil {
		if len(r.Scratchpads) > 0 {
			// When fp.Scratchpad is empty, the displayed scratchpad is
			// derived from the exit fix (possibly via its significant
			// point short name), so check that as well.
			sp := fp.Scratchpad
			if sp == "" {
				sp = exitFixDisplayName(fp.ExitFix, significantPoints)
			}
			if !slices.Contains(r.Scratchpads, sp) {
				return false
			}
		}
		if len(r.SecondaryScratchpads) > 0 && !slices.Contains(r.SecondaryScratchpads, fp.SecondaryScratchpad) {
			return false
		}
		if len(r.OwningTCPs) > 0 && !slices.Contains(r.OwningTCPs, fp.TrackingController) {
			return false
		}
		// Fix criteria match the derived fix (when reassignment substituted
		// one) as well as the actual one; a flight with neither set passes.
		fixMatch := func(fixes []string, actual, derived string) bool {
			if actual == "" && derived == "" {
				return true
			}
			return slices.Contains(fixes, actual) || (derived != "" && slices.Contains(fixes, derived))
		}
		if len(r.EntryFixes) > 0 && !fixMatch(r.EntryFixes, fp.EntryFix, fp.DerivedEntryFix) {
			return false
		}
		if len(r.ExitFixes) > 0 && !fixMatch(r.ExitFixes, fp.ExitFix, fp.DerivedExitFix) {
			return false
		}
		if r.FlightType != "" {
			switch r.FlightType {
			case "ARRIVAL":
				if fp.TypeOfFlight != av.FlightTypeArrival {
					return false
				}
			case "DEPARTURE":
				if fp.TypeOfFlight != av.FlightTypeDeparture {
					return false
				}
			case "OVERFLIGHT":
				if fp.TypeOfFlight != av.FlightTypeOverflight {
					return false
				}
			}
		}
		if r.FlightRules == "V" {
			if fp.Rules != av.FlightRulesVFR && fp.Rules != av.FlightRulesDVFR && fp.Rules != av.FlightRulesSVFR {
				return false
			}
		} else if r.FlightRules == "I" {
			if fp.Rules != av.FlightRulesIFR {
				return false
			}
		}
		if len(r.SSRCodes) > 0 {
			if !slices.ContainsFunc(r.SSRCodes, func(rng [2]av.Squawk) bool {
				return fp.AssignedSquawk >= rng[0] && fp.AssignedSquawk <= rng[1]
			}) {
				return false
			}
		}
		if len(r.RequestedAltitudes) > 0 {
			alt := fp.RequestedAltitude / 100
			if !slices.ContainsFunc(r.RequestedAltitudes, func(rng [2]int) bool {
				return alt >= rng[0] && alt <= rng[1]
			}) {
				return false
			}
		}
	}

	if r.CWTCategory != "" {
		if perf, ok := av.DB.AircraftPerformance[aircraftType]; !ok || perf.Category.CWT != r.CWTCategory {
			return false
		}
	}

	return true
}

type QuicklookRegion struct {
	av.AirspaceVolume
	FilterQualifiers
}

type QuicklookRegions []QuicklookRegion

func (r *QuicklookRegion) ValidateTCPs(controlPositions map[TCP]*av.Controller, e *util.ErrorLogger) {
	r.FilterQualifiers.PostDeserialize(controlPositions, e)
}

func (r *QuicklookRegion) PostDeserialize(loc av.Locator, e *util.ErrorLogger) {
	r.AirspaceVolume.PostDeserialize(loc, e)
}

// exitFixDisplayName returns the name that would be displayed as the
// scratchpad for the given exit fix, accounting for significant point
// short names. This mirrors the datablock rendering logic.
func exitFixDisplayName(exitFix string, significantPoints map[string]SignificantPoint) string {
	if exitFix == "" {
		return ""
	}
	fix, _, _ := strings.Cut(exitFix, ".")
	if sp, ok := significantPoints[fix]; ok {
		if sp.ShortName != "" {
			return sp.ShortName
		} else if len(fix) > 3 {
			return fix[:3]
		}
		return fix
	}
	return ""
}

func (r QuicklookRegion) Match(p math.Point2LL, alt int, fp *NASFlightPlan,
	userPositions []ControlPosition, aircraftType string,
	significantPoints map[string]SignificantPoint) bool {
	return r.AirspaceVolume.Inside(p, alt) &&
		r.FilterQualifiers.Match(fp, userPositions, aircraftType, significantPoints)
}

func (r QuicklookRegions) Match(p math.Point2LL, alt int, fp *NASFlightPlan,
	userPositions []ControlPosition, aircraftType string,
	significantPoints map[string]SignificantPoint) bool {
	return slices.ContainsFunc(r, func(r QuicklookRegion) bool {
		return r.Match(p, alt, fp, userPositions, aircraftType, significantPoints)
	})
}

func (r QuicklookRegions) HaveId(s string) bool {
	return slices.ContainsFunc(r, func(r QuicklookRegion) bool { return s == r.Id })
}

// FDAMRegion defines a Flight Data Auto-Modify filter region. When a
// qualifying track enters the region, entry actions are applied; when it
// exits, exit actions may revert changes.
type FDAMRegion struct {
	av.AirspaceVolume
	FilterQualifiers

	// Entry actions
	NewScratchpad1           string `json:"new_scratchpad_1"`
	AllowScratchpad1Override bool   `json:"allow_scratchpad_1_override"`
	NewScratchpad2           string `json:"new_scratchpad_2"`
	AllowScratchpad2Override bool   `json:"allow_scratchpad_2_override"`

	NewOwnerLeaderDirectionString string `json:"new_owner_leader_direction"`
	NewOwnerLeaderDirection       *math.CardinalOrdinalDirection

	HandoffInitiateTransfer string `json:"handoff_initiate_transfer"` // "I", "T", or "N"
	NewOwnerTCPString       string `json:"new_owner_tcp"`
	NewOwnerTCP             ControlPosition

	NewTCPSpecificLeaderDirectionString string `json:"new_tcp_specific_leader_direction"`
	NewTCPSpecificLeaderDirection       *math.CardinalOrdinalDirection

	ImmediatePointout  bool   `json:"immediate_pointout"`
	PointoutTCPsString string `json:"pointout_tcps"`
	PointoutTCPs       []ControlPosition

	// Exit actions
	RetainOwnerLeaderDirection       bool `json:"retain_owner_leader_direction"`
	RetainTCPSpecificLeaderDirection bool `json:"retain_tcp_specific_leader_direction"`
}

type FDAMRegions []FDAMRegion

// FDAMTrackState tracks per-aircraft state for a single FDAM region so
// that entry/exit transitions and revert-on-exit can be handled.
type FDAMTrackState struct {
	Inside                       bool
	PreEntryOwnerLeaderDirection *math.CardinalOrdinalDirection
}

func (r *FDAMRegion) ValidateTCPs(controlPositions map[TCP]*av.Controller, e *util.ErrorLogger) {
	r.FilterQualifiers.PostDeserialize(controlPositions, e)

	if r.TCPsString != "" {
		e.ErrorString(`"tcps" is not supported for FDAM regions`)
	}
	r.TCPs = nil // FDAM regions don't filter by user position

	r.NewOwnerTCPString = strings.ToUpper(strings.TrimSpace(r.NewOwnerTCPString))
	if r.NewOwnerTCPString != "" {
		tcp := ControlPosition(r.NewOwnerTCPString)
		if _, ok := controlPositions[tcp]; !ok {
			e.ErrorString(`unknown TCP %q in "new_owner_tcp"`, r.NewOwnerTCPString)
		} else {
			r.NewOwnerTCP = tcp
		}
	}

	// Parse pointout TCPs
	if r.PointoutTCPsString != "" {
		for v := range strings.SplitSeq(r.PointoutTCPsString, ",") {
			v = strings.ToUpper(strings.TrimSpace(v))
			if v == "" {
				continue
			}
			tcp := ControlPosition(v)
			if _, ok := controlPositions[tcp]; !ok {
				e.ErrorString(`unknown TCP %q in "pointout_tcps"`, v)
			} else {
				r.PointoutTCPs = append(r.PointoutTCPs, tcp)
			}
		}
	}
	if r.ImmediatePointout && len(r.PointoutTCPs) == 0 {
		e.ErrorString(`"pointout_tcps" must be specified when "immediate_pointout" is true`)
	}
}

func (r *FDAMRegion) PostDeserialize(loc av.Locator, e *util.ErrorLogger) {
	r.AirspaceVolume.PostDeserialize(loc, e)

	parseDirection := func(s, field string) *math.CardinalOrdinalDirection {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" {
			return nil
		}
		dir, err := math.ParseCardinalOrdinalDirection(s)
		if err != nil {
			e.ErrorString("invalid %q: %v", field, err)
			return nil
		}
		return &dir
	}

	r.NewOwnerLeaderDirection = parseDirection(r.NewOwnerLeaderDirectionString, "new_owner_leader_direction")
	r.NewTCPSpecificLeaderDirection = parseDirection(r.NewTCPSpecificLeaderDirectionString, "new_tcp_specific_leader_direction")

	r.HandoffInitiateTransfer = strings.ToUpper(strings.TrimSpace(r.HandoffInitiateTransfer))
	if r.HandoffInitiateTransfer == "" {
		r.HandoffInitiateTransfer = "N"
	}
	if r.HandoffInitiateTransfer != "I" && r.HandoffInitiateTransfer != "T" && r.HandoffInitiateTransfer != "N" {
		e.ErrorString(`invalid "handoff_initiate_transfer" %q: must be "I", "T", or "N"`, r.HandoffInitiateTransfer)
	}
	if r.HandoffInitiateTransfer != "N" && r.NewOwnerTCP == "" {
		e.ErrorString(`"new_owner_tcp" must be specified when "handoff_initiate_transfer" is %q`, r.HandoffInitiateTransfer)
	}

	if r.RetainOwnerLeaderDirection && r.NewOwnerLeaderDirection == nil {
		e.ErrorString(`"retain_owner_leader_direction" requires "new_owner_leader_direction"`)
	}
	if r.RetainTCPSpecificLeaderDirection && r.NewTCPSpecificLeaderDirection == nil {
		e.ErrorString(`"retain_tcp_specific_leader_direction" requires "new_tcp_specific_leader_direction"`)
	}
}

func (r FDAMRegion) Match(p math.Point2LL, alt int, fp *NASFlightPlan, aircraftType string,
	significantPoints map[string]SignificantPoint) bool {
	// FDAM uses TCPs="ALL" per the DMS manual, so no userPositions filtering
	return r.AirspaceVolume.Inside(p, alt) &&
		r.FilterQualifiers.Match(fp, nil, aircraftType, significantPoints)
}

func (r FDAMRegions) HaveId(s string) bool {
	return slices.ContainsFunc(r, func(r FDAMRegion) bool { return s == r.Id })
}

func (r FilterRegion) Inside(p math.Point2LL, alt int) bool {
	return r.AirspaceVolume.Inside(p, alt) != r.InvertTest
}

func (r FilterRegions) Inside(p math.Point2LL, alt int) bool {
	return slices.ContainsFunc(r, func(r FilterRegion) bool { return r.Inside(p, alt) })
}

func (r FilterRegions) HaveId(s string) bool {
	return slices.ContainsFunc(r, func(r FilterRegion) bool { return s == r.Id })
}
