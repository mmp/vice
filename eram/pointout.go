package eram

import (
	"slices"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
)

// outboundPointOut tracks one receiver of a point out the user initiated.  Acked flips to true when
// an AcknowledgedPointOutEvent for the matching (acid, receiver) arrives; the entry persists
// locally until the user clicks the white "A" row in the pop-up to dismiss it.
type outboundPointOut struct {
	Receiver sim.ControlPosition
	Acked    bool
}

// pointOutIndicatorActive reports whether a P/A indicator should be drawn on
// datablock line 0 for the track.
func (ep *ERAMPane) pointOutIndicatorActive(trk *sim.Track) bool {
	if trk.FlightPlan == nil {
		return false
	}
	return len(ep.InboundPointOuts[trk.FlightPlan.ACID]) > 0 ||
		len(ep.OutboundPointOuts[trk.FlightPlan.ACID]) > 0
}

// pointOutIndicatorGlyph returns the indicator character (P or A) and color for the line-0
// indicator, or zero rune if nothing should be drawn. Yellow "P" is shown if any inbound or any
// unacked outbound entry exists; white "A" if outbound is non-empty and every entry is already
// acked.
func (ep *ERAMPane) pointOutIndicatorGlyph(trk *sim.Track, fdbBrightness radar.Brightness) (rune, renderer.RGB, bool) {
	if trk.FlightPlan == nil {
		return 0, renderer.RGB{}, false
	}
	acid := trk.FlightPlan.ACID
	y := fdbBrightness.ScaleRGB(colors.yellow)
	if len(ep.InboundPointOuts[acid]) > 0 {
		return 'P', y, true
	}

	outbound := ep.OutboundPointOuts[acid]
	if len(outbound) == 0 {
		return 0, renderer.RGB{}, false
	} else if slices.ContainsFunc(outbound, func(po outboundPointOut) bool { return !po.Acked }) {
		return 'P', y, true
	} else {
		white := fdbBrightness.ScaleRGB(colors.pointOut.white)
		return 'A', white, true
	}
}

// handlePointOutIndicatorClick handles a click on the line-0 P/A point-out indicator. Clicking the
// yellow "P" opens the pop-up menu; clicking the white "A" removes it directly. With inbound
// entries the P always wins; otherwise we open the originator pop-up if anything outbound is still
// pending and direct-dismiss if every outbound entry is already acked (the "A" case). dbMain is
// the main datablock extent; the menu is anchored at its top-right corner so it sits immediately
// to the right of the datablock.
func (ep *ERAMPane) handlePointOutIndicatorClick(ctx *panes.Context, trk sim.Track, dbMain math.Extent2D) {
	if trk.FlightPlan == nil {
		return
	}
	acid := trk.FlightPlan.ACID
	origin := [2]float32{dbMain.P1[0], dbMain.P1[1]}
	if len(ep.InboundPointOuts[acid]) > 0 {
		ep.popup = &pointOutPopup{acid: acid, outbound: false, origin: origin}
		return
	}

	outbound := ep.OutboundPointOuts[acid]
	if len(outbound) == 0 {
		return
	} else if slices.ContainsFunc(outbound, func(po outboundPointOut) bool {
		return po.Acked
	}) {
		// Have unacknowledged p/os
		ep.popup = &pointOutPopup{acid: acid, outbound: true, origin: origin}
	} else {
		delete(ep.OutboundPointOuts, acid)
	}
}

// pointOutPopup is the popup-interface impl for the click-through pop-up
// triggered from the line-0 point-out indicator. Per-instance state (which
// ACID, originator vs receiver view, anchor origin) lives inline rather than
// on ERAMPane.
type pointOutPopup struct {
	acid     sim.ACID
	outbound bool
	origin   [2]float32
}

// draw renders the menu. The receiver view lists each originator (cyan boxed)
// and a click acknowledges every inbound p/o at once. The originator view
// lists each receiver, yellow-boxed for not-yet-acked and white-plain for
// already-acked; click on an acked row dismisses it locally.
func (po *pointOutPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	acid := po.acid

	label := func(p sim.ControlPosition) string {
		if ctrl := ctx.GetResolvedController(p); ctrl != nil {
			return shortFieldERAMID(ctrl.FacilityIdentifier, ctrl.Position, ctx.Client.State.HandoffIDs)
		}
		return string(p)
	}

	var rows []ERAMMenuItem
	if po.outbound {
		entries := ep.OutboundPointOuts[acid]
		if len(entries) == 0 {
			ep.popup = nil
			return
		}
		for i, entry := range entries {
			if entry.Acked {
				rows = append(rows, ERAMMenuItem{
					Label:       "A",
					BoxedSuffix: label(entry.Receiver),
					Color:       colors.pointOut.white, // TODO brightness???
					OnClick: func(_ ERAMMenuClickType) bool {
						ep.removeOutboundPointOut(acid, i)
						return len(ep.OutboundPointOuts[acid]) == 0
					},
				})
			} else {
				rows = append(rows, ERAMMenuItem{
					Label:       "P",
					BoxedSuffix: label(entry.Receiver),
					Color:       colors.yellow, // todo: scale by some brightness?
					// Originator can't ack their own p/o; the click is a no-op but still closes the
					// menu.
					OnClick: func(_ ERAMMenuClickType) bool { return false },
				})
			}
		}
	} else {
		senders := ep.InboundPointOuts[acid]
		if len(senders) == 0 {
			ep.popup = nil
			return
		}

		for _, sender := range senders {
			rows = append(rows, ERAMMenuItem{
				Label:       "P",
				BoxedSuffix: label(sender),
				Color:       colors.pointOut.cyan,
				OnClick: func(_ ERAMMenuClickType) bool {
					if trk, ok := ctx.Client.State.GetTrackByACID(acid); ok {
						ep.acknowledgePointOut(ctx, trk)
					}
					// Not sure what else to do if the lookup fails...
					return true
				},
			})
		}
	}

	ps := ep.currentPrefs()
	rowFont := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	titleW := titleFont.LayoutBounds(string(acid), 0).Width()
	xW := titleFont.LayoutBounds("X", 0).Width()
	// Left pad (4) + title + gap (xW) + X button (xW + 2*xPad) + right pad (2).
	width := titleW + 2*xW + 12

	cfg := ERAMMenuConfig{
		Title:              string(acid),
		TitleLeftJustified: true,
		Width:              width,
		Font:               rowFont,
		TitleFont:          titleFont,
		Rows:               rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, po.origin, cfg)
}

// removeOutboundPointOut deletes a single outbound entry by index, cleaning
// up the map slot if empty.
func (ep *ERAMPane) removeOutboundPointOut(acid sim.ACID, idx int) {
	entries := ep.OutboundPointOuts[acid]
	if idx < 0 || idx >= len(entries) {
		return
	}
	ep.OutboundPointOuts[acid] = slices.Delete(entries, idx, idx+1)
	if len(ep.OutboundPointOuts[acid]) == 0 {
		delete(ep.OutboundPointOuts, acid)
	}
}

// removeOutboundPointOutByReceiver removes the first outbound entry whose
// Receiver matches.
func (ep *ERAMPane) removeOutboundPointOutByReceiver(acid sim.ACID, receiver sim.ControlPosition) {
	if i := slices.IndexFunc(ep.OutboundPointOuts[acid], func(e outboundPointOut) bool {
		return e.Receiver == receiver
	}); i >= 0 {
		ep.removeOutboundPointOut(acid, i)
	}
}

// removeInboundPointOut removes the first inbound entry from the given sender.
func (ep *ERAMPane) removeInboundPointOut(acid sim.ACID, sender sim.ControlPosition) {
	senders := ep.InboundPointOuts[acid]
	if i := slices.Index(senders, sender); i >= 0 {
		ep.InboundPointOuts[acid] = slices.Delete(senders, i, i+1)
		if len(ep.InboundPointOuts[acid]) == 0 {
			delete(ep.InboundPointOuts, acid)
		}
	}
}

// markOutboundPointOutAcked finds the outbound entry for receiver and flips
// its Acked flag.
func (ep *ERAMPane) markOutboundPointOutAcked(acid sim.ACID, receiver sim.ControlPosition) {
	entries := ep.OutboundPointOuts[acid]
	for i := range entries {
		if entries[i].Receiver == receiver && !entries[i].Acked {
			entries[i].Acked = true
			return
		}
	}
}
