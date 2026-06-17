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
	yellow := fdbBrightness.ScaleRGB(ERAMYellow)
	if len(ep.InboundPointOuts[acid]) > 0 {
		return 'P', yellow, true
	}

	outbound := ep.OutboundPointOuts[acid]
	if len(outbound) == 0 {
		return 0, renderer.RGB{}, false
	} else if slices.ContainsFunc(outbound, func(po outboundPointOut) bool { return !po.Acked }) {
		return 'P', yellow, true
	} else {
		white := fdbBrightness.ScaleRGB(renderer.RGB{R: 1, G: 1, B: 1})
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
		ep.pointOutMenuACID = acid
		ep.pointOutMenuOutbound = false
		ep.pointOutMenuOrigin = origin
		return
	}

	outbound := ep.OutboundPointOuts[acid]
	if len(outbound) == 0 {
		return
	} else if slices.ContainsFunc(outbound, func(po outboundPointOut) bool {
		return po.Acked
	}) {
		// Have unacknowledged p/os
		ep.pointOutMenuACID = acid
		ep.pointOutMenuOutbound = true
		ep.pointOutMenuOrigin = origin
	} else {
		delete(ep.OutboundPointOuts, acid)
	}
}

// drawPointOutMenu renders the click-through pop-up for the point-out indicator. The receiver view
// lists each originator (cyan boxed) and a click acknowledges every inbound p/o at once. The
// originator view lists each receiver, yellow-boxed for not-yet-acked and white-plain for
// already-acked; click on an acked row dismisses it locally.
func (ep *ERAMPane) drawPointOutMenu(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	acid := ep.pointOutMenuACID
	if acid == "" {
		return
	}

	label := func(p sim.ControlPosition) string {
		if ctrl := ctx.GetResolvedController(p); ctrl != nil {
			return shortFieldERAMID(ctrl.FacilityIdentifier, ctrl.Position, ctx.Client.State.HandoffIDs)
		}
		return string(p)
	}

	var rows []ERAMMenuItem
	if ep.pointOutMenuOutbound {
		entries := ep.OutboundPointOuts[acid]
		if len(entries) == 0 {
			ep.pointOutMenuACID = ""
			return
		}
		for i, entry := range entries {
			if entry.Acked {
				rows = append(rows, ERAMMenuItem{
					Label:       "A",
					BoxedSuffix: label(entry.Receiver),
					Color:       renderer.RGB{R: 1, G: 1, B: 1}, // TODO brightness???
					OnClick: func(_ ERAMMenuClickType) bool {
						ep.removeOutboundPointOut(acid, i)
						return len(ep.OutboundPointOuts[acid]) == 0
					},
				})
			} else {
				rows = append(rows, ERAMMenuItem{
					Label:       "P",
					BoxedSuffix: label(entry.Receiver),
					Color:       ERAMYellow, // todo: scale by some brightness?
					// Originator can't ack their own p/o; the click is a no-op but still closes the
					// menu.
					OnClick: func(_ ERAMMenuClickType) bool { return false },
				})
			}
		}
	} else {
		senders := ep.InboundPointOuts[acid]
		if len(senders) == 0 {
			ep.pointOutMenuACID = ""
			return
		}

		for _, sender := range senders {
			rows = append(rows, ERAMMenuItem{
				Label:       "P",
				BoxedSuffix: label(sender),
				Color:       renderer.RGB{R: 0, G: 1, B: 1},
				OnClick: func(_ ERAMMenuClickType) bool {
					ep.acknowledgePointOut(ctx, acid)
					return true
				},
			})
		}
	}

	ps := ep.currentPrefs()
	rowFont := ep.ERAMFont(ps.FDBSize)
	titleFont := ep.ERAMFont(2)
	titleW, _ := titleFont.BoundText(string(acid), 0)
	xW, _ := titleFont.BoundText("X", 0)
	// Left pad (4) + title + gap (xW) + X button (xW + 2*xPad) + right pad (2).
	width := float32(titleW) + 2*float32(xW) + 12

	cfg := ERAMMenuConfig{
		Title:                 string(acid),
		TitleLeftJustified:    true,
		OnClose:               func() { ep.pointOutMenuACID = "" },
		Width:                 width,
		Font:                  rowFont,
		TitleFont:             titleFont,
		ShowBorder:            true,
		BorderColor:           renderer.RGB{R: 213.0 / 255.0, G: 213.0 / 255.0, B: 213.0 / 255.0},
		DismissOnClickOutside: true,
		Rows:                  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, ep.pointOutMenuOrigin, cfg)
}

// removeOutboundPointOut deletes a single outbound entry by index, cleaning
// up the map slot if empty.
func (ep *ERAMPane) removeOutboundPointOut(acid sim.ACID, idx int) {
	entries := ep.OutboundPointOuts[acid]
	if idx < 0 || idx >= len(entries) {
		return
	}
	ep.OutboundPointOuts[acid] = append(entries[:idx], entries[idx+1:]...)
	if len(ep.OutboundPointOuts[acid]) == 0 {
		delete(ep.OutboundPointOuts, acid)
	}
}

// removeOutboundPointOutByReceiver removes the first outbound entry whose
// Receiver matches.
func (ep *ERAMPane) removeOutboundPointOutByReceiver(acid sim.ACID, receiver sim.ControlPosition) {
	for i, entry := range ep.OutboundPointOuts[acid] {
		if entry.Receiver == receiver {
			ep.removeOutboundPointOut(acid, i)
			return
		}
	}
}

// removeInboundPointOut removes the first inbound entry from the given sender.
func (ep *ERAMPane) removeInboundPointOut(acid sim.ACID, sender sim.ControlPosition) {
	senders := ep.InboundPointOuts[acid]
	for i, s := range senders {
		if s == sender {
			ep.InboundPointOuts[acid] = append(senders[:i], senders[i+1:]...)
			if len(ep.InboundPointOuts[acid]) == 0 {
				delete(ep.InboundPointOuts, acid)
			}
			return
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
