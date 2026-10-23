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
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

// Flight plan readouts: the formats of the fields they show, the QF command
// that displays one, and the CFR view that keeps readouts displayed.

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
	if e.qualifier == sim.PlainAltitude || (found && (len(alt) != 3 || !util.IsAllNumbers(alt))) {
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
// airport to the destination, in the form of figures 2-32 and 4-253: ".."
// joins points flown direct, "." joins a point to an airway or a procedure,
// and "./." follows the departure airport of a route that begins at the
// aircraft's present position, as in KATL./.ROA205050..ROA..HAR..KPIT.
// Some routes are already written partly in that form, so they are split
// at dots as well as spaces.
func formatRoute(fp *sim.FlightPlan, loc av.Locator) string {
	route := strings.FieldsFunc(fp.Route, func(ch rune) bool { return ch == ' ' || ch == '.' })
	route = slices.DeleteFunc(route, func(s string) bool { return s == "DCT" })
	route = slices.Concat([]string{string(fp.DepartureAirport)}, route, []string{string(fp.ArrivalAirport)})
	route = slices.Compact(slices.DeleteFunc(route, func(s string) bool { return s == "" }))

	// The present position, "/", is not a point: like an airway or a
	// procedure, it takes a single dot on either side.
	isPoint := func(s string) bool {
		if _, ok := loc.Airways(s); ok {
			return false
		}
		_, ok := loc.Locate(s)
		return ok
	}
	var b strings.Builder
	for i, s := range route {
		if i > 0 {
			b.WriteString(util.Select(isPoint(route[i-1]) && isPoint(s), "..", "."))
		}
		b.WriteString(s)
	}
	return b.String()
}

///////////////////////////////////////////////////////////////////////////
// QF - Flight Plan Readout Handlers

// handleFlightPlanReadout shows a flight plan readout in the CFR view if it
// is displayed and in the RA otherwise (2.10).
func handleFlightPlanReadout(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	if trk.FlightPlan == nil || !ep.currentPrefs().CFR.Visible {
		return handleFlightPlanReadoutRA(ep, ctx, trk)
	}
	ep.addCFREntry(trk.FlightPlan.ACID)
	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "FLIGHT PLAN READOUT", string(trk.ADSBCallsign) + "/" + trk.FlightPlan.CID},
	}, nil
}

// handleFlightPlanReadoutRA shows a flight plan readout in the RA, whether
// or not the CFR view is displayed (4.2.3).
func handleFlightPlanReadoutRA(ep *Scope, ctx *scope.Context, trk *sim.Track) (CommandStatus, error) {
	fp := trk.FlightPlan
	if fp == nil {
		// TODO: find the correct error message
		return CommandStatus{}, fmt.Errorf("REJECT - NO FLIGHT PLAN\nFLIGHT PLAN READOUT\n%s", trk.ADSBCallsign)
	}

	/*
		The flight plan readout contains the following elements:

		The Zulu time
		The aircraft's CID
		The aircraft's ID
		The track's owning sector ID (in parentheses)
		The aircraft's type and equipment suffix
		The aircraft's assigned beacon code
		The aircraft's filed cruise speed
		The aircraft's assigned altitude (in 100s of feet)
		The aircraft's route
		The aircraft's flight plan remarks (not in FlightPlan so nothing for now)
	*/
	zTime := ctx.Client.State.SimTime.Format("1504")
	readout := fmt.Sprintf("%v %v(%v) %v %v %v %v %v", fp.CID, fp.ACID, fp.TrackingController,
		formatAircraftData(fp), fp.AssignedSquawk, fp.CruiseSpeed, formatAssignedAltitude(fp),
		formatRoute(fp, &ctx.Client.State))
	// The readout of an active flight always shows the non-ADS-B indicator
	// (2.13).
	if nonADSB(ep.radarSample(trk)) {
		readout = nonADSBMark + " " + readout
	}
	return CommandStatus{responseArea: []string{zTime, readout}}, nil
}

///////////////////////////////////////////////////////////////////////////
// CFR view

// The CFR (Continuous Flight Plan Readout) view, which shows up to ten
// flight plan readouts and keeps them updated (4.2.3). While it is
// displayed, QF readouts go to it rather than to the RA.

const (
	// cfrNewEntryTime is how long a new entry's CID and ACID are green.
	cfrNewEntryTime = 10 * time.Second
	// cfrIndicatorChars is the column at the start of each entry for the
	// non-ADS-B indicator.
	cfrIndicatorChars = 2
)

// cfrFilter is one of the FILTERS menu's toggles, each of which displays
// a column or its data (Table 4-3).
type cfrFilter struct {
	label string
	shown func(*Preferences) *bool
}

var cfrFilters = []cfrFilter{
	{"ALT", func(ps *Preferences) *bool { return &ps.CFR.ShowAltitude }},
	{"BCN", func(ps *Preferences) *bool { return &ps.CFR.ShowBeaconCode }},
	{"CID", func(ps *Preferences) *bool { return &ps.CFR.ShowCID }},
	{"FIX", func(ps *Preferences) *bool { return &ps.CFR.ShowFix }},
	{"LBL", func(ps *Preferences) *bool { return &ps.CFR.ShowLabels }},
	{"RTE", func(ps *Preferences) *bool { return &ps.CFR.ShowRoute }},
	{"SPD", func(ps *Preferences) *bool { return &ps.CFR.ShowSpeed }},
	{"TIM", func(ps *Preferences) *bool { return &ps.CFR.ShowTime }},
	{"TYP", func(ps *Preferences) *bool { return &ps.CFR.ShowType }},
}

// cfrColumn is one of the view's columns after FLID, which is always
// shown: its label and the FILTERS toggle that displays it.
type cfrColumn struct {
	label string
	shown func(*Preferences) *bool
	text  func(ctx *scope.Context, fp *sim.FlightPlan) string
}

var cfrColumns = []cfrColumn{
	{"TYP", func(ps *Preferences) *bool { return &ps.CFR.ShowType },
		func(_ *scope.Context, fp *sim.FlightPlan) string { return formatAircraftData(fp) }},
	{"BCN", func(ps *Preferences) *bool { return &ps.CFR.ShowBeaconCode },
		func(_ *scope.Context, fp *sim.FlightPlan) string { return fp.AssignedSquawk.String() }},
	{"SPD", func(ps *Preferences) *bool { return &ps.CFR.ShowSpeed },
		func(_ *scope.Context, fp *sim.FlightPlan) string { return fmt.Sprint(fp.CruiseSpeed) }},
	{"FIX", func(ps *Preferences) *bool { return &ps.CFR.ShowFix },
		func(_ *scope.Context, fp *sim.FlightPlan) string { return fp.CoordinationFix }},
	{"TIM", func(ps *Preferences) *bool { return &ps.CFR.ShowTime },
		func(_ *scope.Context, fp *sim.FlightPlan) string {
			return util.Select(fp.CoordinationTime.IsZero(), "", "E"+fp.CoordinationTime.Format("1504"))
		}},
	{"ALT", func(ps *Preferences) *bool { return &ps.CFR.ShowAltitude },
		func(_ *scope.Context, fp *sim.FlightPlan) string { return formatAssignedAltitude(fp) }},
}

// addCFREntry adds a flight's readout to the CFR view, or moves it to the
// end if it is already there; the oldest entries make way for it.
func (ep *Scope) addCFREntry(acid sim.ACID) {
	ep.CFREntries = append(slices.DeleteFunc(ep.CFREntries, func(a sim.ACID) bool { return a == acid }), acid)
	ep.trimCFREntries()
	if ep.cfrAdded == nil {
		ep.cfrAdded = make(map[sim.ACID]time.Time)
	}
	ep.cfrAdded[acid] = time.Now()
}

// trimCFREntries deletes the oldest entries past the ENTRIES setting.
func (ep *Scope) trimCFREntries() {
	if n := len(ep.CFREntries) - ep.currentPrefs().CFR.Entries; n > 0 {
		ep.CFREntries = slices.Delete(ep.CFREntries, 0, n)
	}
}

// cfrEntry is an entry's fields as the view shows them.
type cfrEntry struct {
	acid       sim.ACID
	nonADSB    bool
	flid       string   // CID, if shown, and ACID with its sector
	columns    []string // the shown columns after FLID
	route      string
	truncated  bool // route cut at the RTE setting
	newlyAdded bool
}

// cfrLayout is the view's contents: its shown columns, their widths, and
// its entries.
type cfrLayout struct {
	columns []cfrColumn
	widths  []int // FLID, then each of columns
	entries []cfrEntry
}

// layoutCFR lays out the view's entries. SHOW FIELDS shows all of the
// columns and whole routes.
func (ep *Scope) layoutCFR(ctx *scope.Context) cfrLayout {
	ps := ep.currentPrefs()
	shown := func(f func(*Preferences) *bool) bool { return ep.CFRShowFields || *f(ps) }

	l := cfrLayout{columns: util.FilterSlice(cfrColumns, func(c cfrColumn) bool { return shown(c.shown) })}
	l.widths = make([]int, 1+len(l.columns))
	if shown(func(ps *Preferences) *bool { return &ps.CFR.ShowLabels }) {
		l.widths[0] = len("FLID")
		for i, c := range l.columns {
			l.widths[i+1] = len(c.label)
		}
	}

	for _, acid := range ep.CFREntries {
		trk, ok := ctx.Client.State.GetTrackByACID(acid)
		if !ok || trk.IsUnassociated() {
			continue
		}
		fp := trk.FlightPlan
		e := cfrEntry{
			acid:       acid,
			nonADSB:    ps.NonADSBIndicator && nonADSB(ep.radarSample(trk)),
			flid:       fmt.Sprintf("%s(%s)", fp.ACID, fp.TrackingController),
			newlyAdded: time.Since(ep.cfrAdded[acid]) < cfrNewEntryTime,
		}
		if shown(func(ps *Preferences) *bool { return &ps.CFR.ShowCID }) {
			e.flid = fp.CID + " " + e.flid
		}
		for _, c := range l.columns {
			e.columns = append(e.columns, c.text(ctx, fp))
		}
		if shown(func(ps *Preferences) *bool { return &ps.CFR.ShowRoute }) {
			e.route = formatRoute(fp, &ctx.Client.State)
			if !ep.CFRShowFields && len(e.route) > ps.CFR.RouteChars {
				e.route, e.truncated = e.route[:ps.CFR.RouteChars], true
			}
		}

		l.widths[0] = max(l.widths[0], len(e.flid))
		for i, s := range e.columns {
			l.widths[i+1] = max(l.widths[i+1], len(s))
		}
		l.entries = append(l.entries, e)
	}
	return l
}

// line joins a line's fields, each padded to its column's width. The first
// spans the non-ADS-B indicator's column and FLID's.
func (l *cfrLayout) line(first string, columns []string, route string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-*s", cfrIndicatorChars+l.widths[0], first)
	for i, s := range columns {
		fmt.Fprintf(&sb, " %-*s", l.widths[i+1], s)
	}
	if route != "" {
		sb.WriteString(" " + route)
	}
	return sb.String()
}

// labels returns the column labels' line, with FLID over the non-ADS-B
// indicator's column as in figure 4-18.
func (l *cfrLayout) labels(route bool) string {
	return l.line("FLID", util.MapSlice(l.columns, func(c cfrColumn) string { return c.label }),
		util.Select(route, "RTE/RMK", ""))
}

// drawCFRView draws the CFR view. A TBE on an entry's CID or ACID offers
// to delete it, and a TBP or TBE on the additional route indicator, a
// boxed asterisk after a route cut short, shows the whole readout in the RA.
func (ep *Scope) drawCFRView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.CFR.Visible {
		return
	}
	ep.trimCFREntries()

	l := ep.layoutCFR(ctx)
	font := ep.ERAMFont(ps.CFR.Font)
	cw := charWidth(font)
	textColor := ps.CFR.Bright.ScaleRGB(colors.view.text)
	indicatorStyle := renderer.TextStyle{Font: font, Color: ps.Brightness.NonADSB.ScaleRGB(colors.coral)}
	newStyle := renderer.TextStyle{Font: font, Color: ps.CFR.Bright.ScaleRGB(colors.successGreen)}

	var rows []Row
	if ep.CFRShowFields || ps.CFR.ShowLabels {
		rows = append(rows, Row{Label: l.labels(ep.CFRShowFields || ps.CFR.ShowRoute)})
	}
	labelRows := len(rows)
	for _, e := range l.entries {
		// The indicator and FLID are drawn over the spaces left for them.
		label := l.line(strings.Repeat(" ", cfrIndicatorChars+len(e.flid)), e.columns, e.route)
		if e.truncated {
			label += "  "
		}
		rows = append(rows, Row{Label: label, AfterDraw: func(row math.Extent2D, origin [2]float32, b *ViewBuilders) {
			x := row.P0[0] + titleBarButtonPad
			if e.nonADSB {
				b.Td.AddText("A", [2]float32{x, origin[1]}, indicatorStyle)
			}
			style := util.Select(e.newlyAdded, newStyle, renderer.TextStyle{Font: font, Color: textColor})
			b.Td.AddText(e.flid, [2]float32{x + cfrIndicatorChars*cw, origin[1]}, style)
			if e.truncated {
				box := cfrRouteIndicator(row, label, cw)
				b.Td.AddTextCentered("*", box.Center(), renderer.TextStyle{Font: font, Color: textColor})
				b.Ld.AddLineLoop(textColor, box.Corners())
			}
		}})
	}

	var extents []math.Extent2D
	v := View{
		Position:   &ps.CFR.Position,
		ID:         "cfr",
		Title:      "CFR",
		Opaque:     ps.CFR.Opaque,
		ShowBorder: ps.CFR.ShowBorder,
		Brightness: ps.CFR.Bright,
		OnMenu: ep.makeViewMenu(ctx, "cfr", 7+len(cfrFilters),
			func(pb popupBase) popup { return &cfrPopup{popupBase: pb} }),
		OnMinimize: func() { ps.CFR.Visible = false },
		TitleButtons: []ViewTitleButton{{
			Label: "SHOW FIELDS",
			// It is grayed out with nothing more to show.
			Disabled: len(l.entries) == 0 || !ep.CFRShowFields &&
				!slices.ContainsFunc(cfrFilters, func(f cfrFilter) bool { return !*f.shown(ps) }),
			OnClick: func() { ep.CFRShowFields = !ep.CFRShowFields },
		}, {
			Label:   "DEL ALL",
			OnClick: func() { ep.CFREntries = nil },
		}},
		RowSource: &ViewRowSource{
			Rows:                    rows,
			FontIndex:               ps.CFR.Font,
			ContentChars:            util.ReduceSlice(rows, 0, func(r Row, n int) int { return max(n, len(r.Label)) }),
			MaxCols:                 1,
			VisibleRows:             max(1, len(rows)),
			RowSpacing:              RowSpacingCompact,
			CenterColumnsInTitleBar: true,
			OnRowExtents:            func(_ int, e []math.Extent2D) { extents = e },
		},
	}
	ep.DrawView(ctx, transforms, cb, v)

	mouse := ctx.Mouse
	primary, tertiary := ep.mousePrimaryClicked(mouse), ep.mouseTertiaryClicked(mouse)
	if !primary && !tertiary {
		return
	}
	for i, e := range l.entries {
		if labelRows+i >= len(extents) {
			break
		}
		row := extents[labelRows+i]
		x0 := row.P0[0] + titleBarButtonPad + cfrIndicatorChars*cw
		flid := math.Extent2D{P0: [2]float32{x0, row.P0[1]}, P1: [2]float32{x0 + float32(len(e.flid))*cw, row.P1[1]}}
		switch {
		case tertiary && flid.Inside(mouse.Pos):
			ep.popup = ep.openDeleteEntryPopup(ctx, ViewSelectableItem{Extent: flid}, &ViewSelectable{
				Label:    func(int) string { return string(e.acid) },
				OnDelete: func(int) { ep.deleteCFREntry(e.acid) },
			}, ep.ERAMFont(2))
		case e.truncated && cfrRouteIndicator(row, rows[labelRows+i].Label, cw).Inside(mouse.Pos):
			if trk, ok := ctx.Client.State.GetTrackByACID(e.acid); ok {
				status, err := handleFlightPlanReadoutRA(ep, ctx, trk)
				ep.applyCommandStatus(ctx, status, err)
			}
		default:
			continue
		}
		ep.consumeMouseClick(mouse)
		return
	}
}

// cfrRouteIndicator returns the extent of the additional route indicator
// at the end of an entry's line.
func cfrRouteIndicator(row math.Extent2D, label string, cw float32) math.Extent2D {
	x := row.P0[0] + titleBarButtonPad + float32(len(label)-1)*cw
	return math.Extent2D{P0: [2]float32{x - 1, row.P0[1] + 1}, P1: [2]float32{x + cw + 1, row.P1[1] - 1}}
}

func (ep *Scope) deleteCFREntry(acid sim.ACID) {
	ep.CFREntries = slices.DeleteFunc(ep.CFREntries, func(a sim.ACID) bool { return a == acid })
}

// cfrPopup is the CFR view's menu (figure 4-20). FILTERS shows the filter
// toggles below it.
type cfrPopup struct {
	popupBase
	filters bool
}

func (c *cfrPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	rows := []MenuItem{
		ep.makeBooleanMenuItem(&ps.CFR.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.CFR.ShowBorder, "BORDER"),
		makeIntMenuItem(ep, &ps.CFR.Entries, "ENTRIES", 1, 10, 1),
		makeIntMenuItem(ep, &ps.CFR.Font, "FONT", 1, 3, 1),
		makeIntMenuItem(ep, &ps.CFR.Bright, "BRIGHT", 0, 100, 2),
		makeIntMenuItem(ep, &ps.CFR.RouteChars, "RTE", 30, 100, 1),
		ep.makeToggleMenuItem(&c.filters, "FILTERS"),
	}
	if c.filters {
		for _, f := range cfrFilters {
			rows = append(rows, ep.makeToggleMenuItem(f.shown(ps), f.label))
		}
	}
	ep.DrawERAMMenu(ctx, transforms, cb, c.origin, MenuConfig{
		Title: "CFR",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	})
}
