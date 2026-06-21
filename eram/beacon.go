package eram

import (
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
)

type beaconCodeViewPopup struct {
	popupBase
}

func (b *beaconCodeViewPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	rows := []ERAMMenuItem{
		ep.makeBooleanMenuItem(&ps.BeaconCodeView.Opaque, "O", "T"),
		ep.makeToggleMenuItem(&ps.BeaconCodeView.ShowBorder, "BORDER"),
		ep.makeIntMenuItem(&ps.BeaconCodeView.Lines, "LINES", 3, 24, 1),
		ep.makeIntMenuItem(&ps.BeaconCodeView.Col, "COL", 1, 5, 1),
		ep.makeIntMenuItem(&ps.BeaconCodeView.Font, "FONT", 1, 3, 1),
		ep.makeIntMenuItem(&ps.BeaconCodeView.Bright, "BRIGHT", 0, 100, 1),
		ep.makeToggleMenuItem(&ps.BeaconCodeView.SortManual, "SORT MAN"),
	}

	cfg := ERAMMenuConfig{
		Title: "CODE",
		Width: 150,
		Font:  ep.ERAMFont(2),
		Rows:  rows,
	}

	ep.DrawERAMMenu(ctx, transforms, cb, b.origin, cfg)
}

// drawBeaconCodeView renders the CODE view
func (ep *ERAMPane) drawBeaconCodeView(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()
	if !ps.BeaconCodeView.Visible {
		return
	}

	// Cell content: 4-digit code plus a trailing "." for manually-added
	// entries. Reserve the dot's width on every cell so columns line up
	// regardless of which rows actually carry it.
	cellContentW := ep.clampedFont(ps.BeaconCodeView.Font, 1, 3).LayoutBounds("0000.", 0).Width()

	ep.DrawView(ctx, transforms, cb, View{
		Position:   &ps.BeaconCodeView.Position,
		ID:         "beacon",
		Title:      "CODE",
		Opaque:     ps.BeaconCodeView.Opaque,
		ShowBorder: ps.BeaconCodeView.ShowBorder,
		Brightness: radar.Brightness(ps.BeaconCodeView.Bright),
		OnMenu: ep.makeViewMenu(ctx, "beacon", 150, (7+1)*18,
			func(pb popupBase) popup { return &beaconCodeViewPopup{popupBase: pb} }),
		MinimizeTarget: &ps.BeaconCodeView.Visible,
		RowSource: &ViewRowSource{
			Rows:                    beaconCodeRows(ctx, ep, ps),
			FontSize:                ps.BeaconCodeView.Font,
			ContentWidth:            cellContentW,
			MaxCols:                 math.Clamp(ps.BeaconCodeView.Col, 1, 5),
			VisibleRows:             math.Clamp(ps.BeaconCodeView.Lines, 3, 24),
			RowSpacing:              RowSpacingCompact,
			CenterColumnsInTitleBar: true,
		},
	})
}

// beaconCodeRows returns the flat row list shown by drawBeaconCodeView:
// manually-added codes (with trailing ".") plus the codes of aircraft whose
// tracks we own, in the order dictated by SortManual. Row.Color is left zero
// — the View fills in the default text color.
func beaconCodeRows(ctx *panes.Context, ep *ERAMPane, ps *Preferences) []Row {
	// Codes of aircraft whose tracks we own.
	var owned []av.Squawk
	for _, trk := range ctx.Client.State.Tracks {
		if trk.IsAssociated() && ctx.UserOwnsFlightPlan(trk.FlightPlan) {
			owned = append(owned, trk.Squawk)
		}
	}

	makeRow := func(code av.Squawk) Row {
		s := code.String()
		if slices.Contains(ep.AddedBeaconCodes, code) {
			s += "."
		}
		return Row{ID: code.String(), Body: s}
	}

	var rows []Row
	if ps.BeaconCodeView.SortManual {
		// First the (sorted) manually added codes, then the sorted owned codes
		// that aren't also manually added.
		added := slices.Clone(ep.AddedBeaconCodes)
		slices.Sort(added)
		for _, code := range added {
			rows = append(rows, makeRow(code))
		}
		ownedOnly := slices.Clone(owned)
		ownedOnly = slices.DeleteFunc(ownedOnly, func(c av.Squawk) bool {
			return slices.Contains(ep.AddedBeaconCodes, c)
		})
		slices.Sort(ownedOnly)
		for _, code := range ownedOnly {
			rows = append(rows, makeRow(code))
		}
	} else {
		// All codes (added + owned) merged and sorted; manual ones keep their ".".
		all := append(slices.Clone(ep.AddedBeaconCodes), owned...)
		slices.Sort(all)
		all = slices.Compact(all)
		for _, code := range all {
			rows = append(rows, makeRow(code))
		}
	}
	return rows
}
