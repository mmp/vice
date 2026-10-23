// eram/fel.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"cmp"
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

// The FLIGHT EVENT (Flight Event List) view and the FEL indicator in data
// blocks (4.2.13, 5.1.5.19). Of the view's conditions, vice has only call
// sign mismatches, where a flight plan's ACID differs from the ADS-B call
// sign of the track it is paired with: there are no 24-bit addresses or
// second surveillance sources.

// felListed reports whether the view lists a track's call sign mismatch,
// as it does those of the tracks the user owns or is being offered but for
// the entries the user has deleted. Its FDB then shows the FEL indicator.
func (ep *Scope) felListed(ctx *scope.Context, trk *sim.Track) bool {
	if trk.IsUnassociated() || trk.IsUnsupportedDB() || sim.ACID(trk.ADSBCallsign) == trk.FlightPlan.ACID {
		return false
	}
	if !ctx.UserOwnsFlightPlan(trk.FlightPlan) && !ctx.IsHandoffToUser(trk) {
		return false
	}
	acid, deleted := ep.FELDeleted[trk.ADSBCallsign]
	return !deleted || acid != trk.FlightPlan.ACID
}

// felEntries returns the tracks whose call sign mismatches the view lists,
// by CID.
func (ep *Scope) felEntries(ctx *scope.Context) []*sim.Track {
	var entries []*sim.Track
	for _, trk := range ctx.Client.State.Tracks {
		if ep.felListed(ctx, trk) {
			entries = append(entries, trk)
		}
	}
	slices.SortFunc(entries, func(a, b *sim.Track) int { return cmp.Compare(a.FlightPlan.CID, b.FlightPlan.CID) })
	return entries
}

// drawFELIndicator draws the FEL indicator, a box with a gray band across
// its top (figure 5-41), in e.
func drawFELIndicator(trid *renderer.ColoredTrianglesDrawBuilder, ld *renderer.ColoredLinesDrawBuilder,
	e math.Extent2D, ps *Preferences) {
	band := e
	band.P0[1] = e.P1[1] - e.Height()/3
	addQuad(trid, e, renderer.RGB{})
	addQuad(trid, band, ps.Brightness.DBFEL.ScaleRGB(colors.popup.backgroundGrey))
	ld.AddLineLoop(ps.Brightness.DBFEL.ScaleRGB(colors.view.border), e.Corners())
}

// drawFlightEventView draws the FLIGHT EVENT view. A pick on an entry
// selects it and offers to delete it. The view is suppressed when its last
// entry goes (4.2.13).
func (ep *Scope) drawFlightEventView(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	entries := ep.felEntries(ctx)
	if len(entries) == 0 && ep.felHadEntries {
		ps.FlightEvent.Visible = false
	}
	ep.felHadEntries = len(entries) > 0
	if !ps.FlightEvent.Visible {
		return
	}

	font := ep.ERAMFont(ps.FlightEvent.Font)
	var rows []Row
	if len(entries) > 0 {
		// The header is underlined, as in figure 4-186.
		rows = append(rows, Row{Label: "CALLSIGN MISMATCH", Centered: true,
			AfterDraw: func(_ math.Extent2D, p [2]float32, b *ViewBuilders) {
				y := p[1] - cellHeight(font) - 2
				b.Ld.AddLine([2]float32{p[0], y}, [2]float32{p[0] + float32(len("CALLSIGN MISMATCH"))*charWidth(font), y},
					ps.FlightEvent.Bright.ScaleRGB(colors.view.text))
			}})
	}
	for _, trk := range entries {
		rows = append(rows, Row{
			Label: trk.FlightPlan.CID + " " + string(trk.FlightPlan.ACID) + " " + string(trk.ADSBCallsign) + " (ADSB)",
			ID:    string(trk.FlightPlan.ACID) + " " + string(trk.ADSBCallsign),
		})
	}

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.FlightEvent.Position,
		ID:         "flight-event",
		Title:      "FLIGHT EVENT",
		Opaque:     ps.FlightEvent.Opaque,
		ShowBorder: ps.FlightEvent.ShowBorder,
		Brightness: ps.FlightEvent.Bright,
		OnMenu: ep.makeViewMenu(ctx, "flight-event", 5,
			func(pb popupBase) popup { return &flightEventPopup{popupBase: pb} }),
		OnMinimize: func() { ps.FlightEvent.Visible = false },
		RowSource: &ViewRowSource{
			Rows:                    rows,
			FontIndex:               ps.FlightEvent.Font,
			ContentChars:            40,
			MaxCols:                 1,
			VisibleRows:             visibleLines(ps.FlightEvent.Lines, len(rows)),
			RowSpacing:              RowSpacingCompact,
			ScrollState:             &ep.felScroll,
			CenterColumnsInTitleBar: true,
			SelectableState:         &ep.felSelect,
			// The header is the first row.
			OnRowDelete: func(idx int) { ep.deleteFELEntry(entries[idx-1]) },
		},
	})
}

// deleteFELEntry deletes a track's entry; it comes back if the track's
// ACID changes and still doesn't match.
func (ep *Scope) deleteFELEntry(trk *sim.Track) {
	if ep.FELDeleted == nil {
		ep.FELDeleted = make(map[av.ADSBCallsign]sim.ACID)
	}
	ep.FELDeleted[trk.ADSBCallsign] = trk.FlightPlan.ACID
}

type flightEventPopup struct {
	popupBase
}

func (p *flightEventPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	ep.DrawERAMMenu(ctx, transforms, cb, p.origin, MenuConfig{
		Title: "FEL",
		Width: viewPopupWidth,
		Font:  ep.ERAMFont(2),
		Rows: []MenuItem{
			ep.makeBooleanMenuItem(&ps.FlightEvent.Opaque, "O", "T"),
			ep.makeToggleMenuItem(&ps.FlightEvent.ShowBorder, "BORDER"),
			makeLinesMenuItem(ep, &ps.FlightEvent.Lines, 3),
			makeIntMenuItem(ep, &ps.FlightEvent.Font, "FONT", 1, 3, 1),
			makeIntMenuItem(ep, &ps.FlightEvent.Bright, "BRIGHT", 0, 100, 2),
		},
	})
}
