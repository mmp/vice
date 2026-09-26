// eram/pointout.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"slices"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

// pointOutIndicatorActive reports whether a P/A indicator should be drawn on
// datablock line 0 for the track.
func (ep *Scope) pointOutIndicatorActive(ctx *scope.Context, trk *sim.Track) bool {
	if trk.FlightPlan == nil {
		return false
	}
	fp := trk.FlightPlan
	return len(ctx.InboundPointOuts(fp)) > 0 || len(ctx.OutboundPointOuts(fp)) > 0 ||
		len(ep.AckedPointOuts[fp.ACID]) > 0
}

// pointOutIndicatorGlyph returns the indicator character (P or A) and color for the line-0
// indicator, or zero rune if nothing should be drawn. Yellow "P" is shown if any inbound or
// outbound point out is pending; white "A" if all that remain are the user's acknowledged ones.
func (ep *Scope) pointOutIndicatorGlyph(ctx *scope.Context, trk *sim.Track, fdbBrightness scope.Brightness) (rune, renderer.RGB, bool) {
	if trk.FlightPlan == nil {
		return 0, renderer.RGB{}, false
	}
	fp := trk.FlightPlan
	if len(ctx.InboundPointOuts(fp)) > 0 || len(ctx.OutboundPointOuts(fp)) > 0 {
		return 'P', fdbBrightness.ScaleRGB(colors.yellow), true
	} else if len(ep.AckedPointOuts[fp.ACID]) > 0 {
		return 'A', fdbBrightness.ScaleRGB(colors.pointOut.white), true
	}
	return 0, renderer.RGB{}, false
}

// handlePointOutIndicatorClick handles a click on the line-0 P/A point-out indicator. Clicking the
// yellow "P" opens the pop-up menu; clicking the white "A" removes it directly. With inbound
// point outs the P always opens the receiver pop-up; otherwise it opens the originator pop-up.
// dbMain is the main datablock extent; the menu is anchored at its top-right corner so it sits
// immediately to the right of the datablock.
func (ep *Scope) handlePointOutIndicatorClick(ctx *scope.Context, trk sim.Track, dbMain math.Extent2D) {
	if trk.FlightPlan == nil {
		return
	}
	fp := trk.FlightPlan
	origin := [2]float32{dbMain.P1[0], dbMain.P1[1]}
	if len(ctx.InboundPointOuts(fp)) > 0 {
		ep.popup = &pointOutPopup{acid: fp.ACID, outbound: false, origin: origin}
	} else if len(ctx.OutboundPointOuts(fp)) > 0 {
		ep.popup = &pointOutPopup{acid: fp.ACID, outbound: true, origin: origin}
	} else {
		delete(ep.AckedPointOuts, fp.ACID)
	}
}

// pointOutPopup is the popup-interface impl for the click-through pop-up
// triggered from the line-0 point-out indicator. Per-instance state (which
// ACID, originator vs receiver view, anchor origin) lives inline rather than
// on Scope.
type pointOutPopup struct {
	acid     sim.ACID
	outbound bool
	origin   [2]float32
}

// draw renders the menu. The receiver view lists each originator (cyan boxed)
// and a click acknowledges every inbound p/o at once. The originator view
// lists each receiver, yellow-boxed for not-yet-acked and white-plain for
// already-acked; click on an acked row dismisses it locally.
func (po *pointOutPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	acid := po.acid

	label := func(p sim.ControlPosition) string {
		if ctrl := ctx.GetResolvedController(p); ctrl != nil {
			return shortFieldERAMID(ctrl.FacilityIdentifier, ctrl.Position, ctx.Client.State.HandoffIDs)
		}
		return string(p)
	}

	trk, ok := ctx.Client.State.GetTrackByACID(acid)
	if !ok {
		ep.popup = nil
		return
	}

	var rows []MenuItem
	if po.outbound {
		pending := ctx.OutboundPointOuts(trk.FlightPlan)
		for _, p := range pending {
			rows = append(rows, MenuItem{
				Label:       "P",
				BoxedSuffix: label(p.ToController),
				Color:       colors.yellow, // todo: scale by some brightness?
				// Originator can't ack their own p/o; the click is a no-op but still closes the
				// menu.
				OnClick: func(_ MenuClickType) bool { return false },
			})
		}
		for _, receiver := range ep.AckedPointOuts[acid] {
			rows = append(rows, MenuItem{
				Label:       "A",
				BoxedSuffix: label(receiver),
				Color:       colors.pointOut.white, // TODO brightness???
				OnClick: func(_ MenuClickType) bool {
					ep.removeAckedPointOut(acid, receiver)
					return len(pending) == 0 && len(ep.AckedPointOuts[acid]) == 0
				},
			})
		}
	} else {
		for _, p := range ctx.InboundPointOuts(trk.FlightPlan) {
			rows = append(rows, MenuItem{
				Label:       "P",
				BoxedSuffix: label(p.FromController),
				Color:       colors.pointOut.cyan,
				OnClick: func(_ MenuClickType) bool {
					ep.acknowledgePointOut(ctx, trk)
					return true
				},
			})
		}
	}
	if len(rows) == 0 {
		ep.popup = nil
		return
	}

	ps := ep.currentPrefs()
	rowFont := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	titleW := titleFont.LayoutBounds(string(acid), 0).Width()
	xW := titleFont.LayoutBounds("X", 0).Width()
	// Left pad (4) + title + gap (xW) + X button (xW + 2*xPad) + right pad (2).
	width := titleW + 2*xW + 12

	cfg := MenuConfig{
		Title:              string(acid),
		TitleLeftJustified: true,
		Width:              width,
		Font:               rowFont,
		TitleFont:          titleFont,
		Rows:               rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, po.origin, cfg)
}

// removeAckedPointOut dismisses receiver's acknowledgment of the user's point
// out.
func (ep *Scope) removeAckedPointOut(acid sim.ACID, receiver sim.ControlPosition) {
	ep.AckedPointOuts[acid] = slices.DeleteFunc(ep.AckedPointOuts[acid],
		func(p sim.ControlPosition) bool { return p == receiver })
	if len(ep.AckedPointOuts[acid]) == 0 {
		delete(ep.AckedPointOuts, acid)
	}
}
