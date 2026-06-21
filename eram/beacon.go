package eram

import (
	"fmt"
	"slices"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

type beaconCodeViewPopup struct {
	origin [2]float32
}

func (b *beaconCodeViewPopup) draw(ep *ERAMPane, ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	// T/O button background
	toBg := util.Select(ps.BeaconCodeView.Opaque, colors.popup.backgroundGrey, colors.popup.backgroundBlack)

	// BORDER button - grey when ON, black when OFF
	borderBg := util.Select(ps.BeaconCodeView.ShowBorder, colors.popup.backgroundGrey, colors.popup.backgroundBlack)

	rows := []ERAMMenuItem{
		{Label: util.Select(ps.BeaconCodeView.Opaque, "O", "T"), BgColor: toBg, Color: colors.popup.text, Centered: true,
			OnClick: func(_ ERAMMenuClickType) bool {
				ps.BeaconCodeView.Opaque = !ps.BeaconCodeView.Opaque
				return false
			}},
		{Label: "BORDER", BgColor: borderBg, Color: colors.popup.text, Centered: false,
			OnClick: func(_ ERAMMenuClickType) bool {
				ps.BeaconCodeView.ShowBorder = !ps.BeaconCodeView.ShowBorder
				return false
			}},
		{Label: fmt.Sprintf("LINES %d", ps.BeaconCodeView.Lines), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.BeaconCodeView.Lines, 3, 24, 1)
				return false
			}},
		{Label: fmt.Sprintf("COL %d", ps.BeaconCodeView.Col), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.BeaconCodeView.Col, 1, 5, 1)
				return false
			}},
		{Label: fmt.Sprintf("FONT %d", ps.BeaconCodeView.Font), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.BeaconCodeView.Font, 1, 3, 1)
				return false
			}},
		{Label: fmt.Sprintf("BRIGHT %d", ps.BeaconCodeView.Bright), BgColor: colors.popup.backgroundGreen, Color: colors.popup.text, Centered: false,
			OnClick: func(ct ERAMMenuClickType) bool {
				handleClick(ep, &ps.BeaconCodeView.Bright, 0, 100, 1)
				return false
			}},
		{Label: "SORT MAN",
			BgColor: util.Select(ps.BeaconCodeView.SortManual, colors.popup.backgroundGrey, colors.popup.backgroundBlack),
			Color:   colors.popup.text,
			OnClick: func(_ ERAMMenuClickType) bool {
				ps.BeaconCodeView.SortManual = !ps.BeaconCodeView.SortManual
				return false
			}},
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

	fontNum := math.Clamp(ps.BeaconCodeView.Font, 1, 3)
	bright := radar.Brightness(ps.BeaconCodeView.Bright)
	listFont := ep.ERAMFont(fontNum)
	titleFont := ep.ERAMFont(2)
	lineH := listFont.LayoutBounds("0", 0).Height() + 2

	visibleRows := math.Clamp(ps.BeaconCodeView.Lines, 3, 24)
	numCols := math.Clamp(ps.BeaconCodeView.Col, 1, 5)

	sidePad := lineH / 4
	// Cell content: 4-digit code plus a trailing "." for manually-added entries.
	// Reserve the dot's width on every cell so columns line up regardless of
	// which rows actually carry it.
	cellContentW := listFont.LayoutBounds("0000.", 0).Width()
	cellWidth := sidePad + cellContentW + sidePad

	textColor := bright.ScaleRGB(colors.view.text)

	// Codes of aircraft whose tracks we own.
	var owned []av.Squawk
	for _, trk := range ctx.Client.State.Tracks {
		if trk.IsAssociated() && ctx.UserOwnsFlightPlan(trk.FlightPlan) {
			owned = append(owned, trk.Squawk)
		}
	}

	// Manually-added codes (via QB) display with a trailing ".".
	makeRow := func(code av.Squawk) Row {
		s := code.String()
		if slices.Contains(ep.AddedBeaconCodes, code) {
			s += "."
		}
		return Row{ID: code.String(), AltText: s, Color: textColor}
	}

	// Build the sorted code list as a flat slice of Rows; distribution into
	// columns happens below.
	var codes []Row
	if ps.BeaconCodeView.SortManual {
		// First the (sorted) manually added codes, then the sorted owned codes
		// that aren't also manually added.
		added := slices.Clone(ep.AddedBeaconCodes)
		slices.Sort(added)
		for _, code := range added {
			codes = append(codes, makeRow(code))
		}
		ownedOnly := slices.Clone(owned)
		ownedOnly = slices.DeleteFunc(ownedOnly, func(c av.Squawk) bool {
			return slices.Contains(ep.AddedBeaconCodes, c)
		})
		slices.Sort(ownedOnly)
		for _, code := range ownedOnly {
			codes = append(codes, makeRow(code))
		}
	} else {
		// All codes (added + owned) merged and sorted; manual ones keep their ".".
		all := append(slices.Clone(ep.AddedBeaconCodes), owned...)
		slices.Sort(all)
		all = slices.Compact(all)
		for _, code := range all {
			codes = append(codes, makeRow(code))
		}
	}

	// Only allocate as many columns as we'll actually populate: ceil(len/rows)
	// clamped to [1, numCols]. Sizing the view to a fixed numCols would leave
	// empty columns of wasted width when there are few codes.
	actualCols := math.Clamp((len(codes)+visibleRows-1)/visibleRows, 1, numCols)

	// Distribute column-major: first `visibleRows` codes fill column 0, next
	// `visibleRows` fill column 1, etc. Extra codes beyond numCols*visibleRows
	// are dropped (no scroll for the CODE view).
	cols := make([]*RowList, actualCols)
	for c := range cols {
		cols[c] = &RowList{
			Font:          listFont,
			Width:         cellWidth,
			LineHeight:    lineH,
			ListTopPad:    lineH / 2,
			ListBottomPad: lineH / 4,
			BottomGap:     lineH / 4,
			SidePad:       sidePad,
			LabelGap:      0,
			MaxLines:      visibleRows,
		}
	}
	for i, row := range codes {
		c := i / visibleRows
		if c >= actualCols {
			break
		}
		cols[c].Rows = append(cols[c].Rows, row)
	}

	bodyHeight := float32(0)
	for _, col := range cols {
		if h := col.Measure(); h > bodyHeight {
			bodyHeight = h
		}
	}

	// View width: enough to fit either the title bar (M button + centered
	// "CODE" + minimize button, matching DrawView's layout) or the columns of
	// beacon codes, whichever is wider. The view shrinks down to its smallest
	// usable size and grows only when the codes demand it.
	const mPad = float32(2)
	mButtonW := mPad + titleFont.LayoutBounds("M", 0).Width() + mPad
	minButtonW := mPad + titleFont.LayoutBounds("-", 0).Width() + mPad
	titleTextW := titleFont.LayoutBounds("CODE", 0).Width()
	// Title text is centered at width/2; clamp by the wider of the two side
	// buttons so neither overlaps the title.
	titleBarMinW := titleTextW + 2*max(mButtonW, minButtonW)
	colsTotalW := float32(actualCols) * cellWidth
	width := max(titleBarMinW, colsTotalW)
	// Center the column stack when the title bar is the binding constraint.
	colsStart := (width - colsTotalW) / 2

	colBodyExtent := func(c int, body math.Extent2D) math.Extent2D {
		return math.Extent2D{
			P0: [2]float32{body.P0[0] + colsStart + float32(c)*cellWidth, body.P0[1]},
			P1: [2]float32{body.P0[0] + colsStart + float32(c+1)*cellWidth, body.P1[1]},
		}
	}

	v := View{
		Position:   &ps.BeaconCodeView.Position,
		Reposition: &ep.beaconRepo,
		Width:      width,
		BodyHeight: bodyHeight,
		Title:      "CODE",
		TitleFont:  titleFont,
		Opaque:     ps.BeaconCodeView.Opaque,
		ShowBorder: ps.BeaconCodeView.ShowBorder,
		Brightness: bright,
		OnMenu: func(host math.Extent2D) popup {
			if _, open := ep.popup.(*beaconCodeViewPopup); open {
				return nil
			}
			origin := ep.OpenPopupAt(ctx, [2]float32{host.P1[0], host.P1[1]},
				150, (7+1)*18, titleFont, host)
			return &beaconCodeViewPopup{origin: origin}
		},
		OnMinimize: func() { ps.BeaconCodeView.Visible = false },
		Body: func(body math.Extent2D, b *ViewBuilders) {
			for c, col := range cols {
				col.Draw(colBodyExtent(c, body), b)
			}
		},
	}
	ep.DrawView(ctx, transforms, cb, v)
}
