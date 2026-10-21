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
// The TEXT brightness control sets the indicator's brightness (5.1.5.11.2),
// and dwell emphasis adds to it (5.1.5.8).
func (ep *Scope) pointOutIndicatorGlyph(ctx *scope.Context, trk *sim.Track) (rune, renderer.RGB, bool) {
	if trk.FlightPlan == nil {
		return 0, renderer.RGB{}, false
	}
	fp := trk.FlightPlan
	bright := ep.currentPrefs().Brightness.Text + ep.dwellBrightness(*trk)
	if len(ctx.InboundPointOuts(fp)) > 0 || len(ctx.OutboundPointOuts(fp)) > 0 {
		return 'P', bright.ScaleRGB(colors.yellow), true
	} else if len(ep.AckedPointOuts[fp.ACID]) > 0 {
		return 'A', bright.ScaleRGB(colors.pointOut.white), true
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
	// originators lists the sectors in a receiver's pop-up; those the user
	// acknowledges stay listed until removed.
	originators []pointOutOriginator
}

type pointOutOriginator struct {
	pos   sim.ControlPosition
	acked bool
}

// draw renders the menu, whose one row row returns.
func (po *pointOutPopup) draw(ep *Scope, ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	trk, ok := ctx.Client.State.GetTrackByACID(po.acid)
	if !ok {
		ep.popup = nil
		return
	}
	row := po.row(ep, ctx, trk)
	if len(row.Segments) == 0 {
		ep.popup = nil
		return
	}

	ps := ep.currentPrefs()
	rowFont := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	titleW := titleFont.LayoutBounds(string(po.acid), 0).Width()
	xW := titleFont.LayoutBounds("X", 0).Width()
	// Left pad (4) + title + gap (xW) + X button (xW + 2*xPad) + right pad (2).
	width := max(titleW+2*xW+12, menuRowWidth(rowFont, row))

	cfg := MenuConfig{
		Title:              string(po.acid),
		TitleLeftJustified: true,
		Width:              width,
		Font:               rowFont,
		TitleFont:          titleFont,
		Rows:               []MenuItem{row},
	}

	ep.DrawERAMMenu(ctx, transforms, cb, po.origin, cfg)
}

// row returns the pop-up's row: a P followed by the other sectors of the
// point outs. In the originator's view, receivers that have not acknowledged
// are yellow and boxed and those that have are white; a click on one of
// those removes it (5.1.5.11.1). In the receiver's view, originators are
// cyan and boxed; a click acknowledges one, turning it white, and another
// click on it removes it. The P turns white once all are acknowledged
// (5.1.5.11.2).
func (po *pointOutPopup) row(ep *Scope, ctx *scope.Context, trk *sim.Track) MenuItem {
	row := MenuItem{Label: "P"}
	if po.outbound {
		row.Color = colors.yellow
		for _, p := range ctx.OutboundPointOuts(trk.FlightPlan) {
			row.Segments = append(row.Segments,
				MenuSegment{Text: sectorDisplayID(ctx, p.ToController), Color: colors.yellow, Boxed: true})
		}
		for _, receiver := range ep.AckedPointOuts[po.acid] {
			row.Segments = append(row.Segments, MenuSegment{
				Text:  sectorDisplayID(ctx, receiver),
				Color: colors.pointOut.white,
				OnClick: func(_ MenuClickType) bool {
					ep.removeAckedPointOut(po.acid, receiver)
					return false
				},
			})
		}
		return row
	}

	po.updateOriginators(ctx.InboundPointOuts(trk.FlightPlan))
	row.Color = colors.pointOut.white
	for i, o := range po.originators {
		seg := MenuSegment{Text: sectorDisplayID(ctx, o.pos), Color: colors.pointOut.white}
		if o.acked {
			seg.OnClick = func(_ MenuClickType) bool {
				po.originators = slices.Delete(po.originators, i, i+1)
				return false
			}
		} else {
			row.Color = colors.pointOut.cyan
			seg.Color, seg.Boxed = colors.pointOut.cyan, true
			seg.OnClick = func(_ MenuClickType) bool {
				po.originators[i].acked = true
				ep.acknowledgePointOut(ctx, trk, o.pos)
				return false
			}
		}
		row.Segments = append(row.Segments, seg)
	}
	return row
}

// updateOriginators brings a receiver's list of originating sectors up to
// date with the pending point outs: new ones join it, and unacknowledged ones
// that are no longer pending, having been recalled, leave it.
func (po *pointOutPopup) updateOriginators(pending []sim.PointOut) {
	isPending := func(p sim.ControlPosition) bool {
		return slices.ContainsFunc(pending, func(o sim.PointOut) bool { return o.FromController == p })
	}
	po.originators = slices.DeleteFunc(po.originators, func(o pointOutOriginator) bool {
		return !o.acked && !isPending(o.pos)
	})
	for _, p := range pending {
		if !slices.ContainsFunc(po.originators, func(o pointOutOriginator) bool { return o.pos == p.FromController }) {
			po.originators = append(po.originators, pointOutOriginator{pos: p.FromController})
		}
	}
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
