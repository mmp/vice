// scope/tools.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package scope

import (
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

type Brightness int

func (b Brightness) ScaleRGB(r renderer.RGB) renderer.RGB {
	return r.Scale(float32(b) / 100)
}

// Transformations

type Transformations struct {
	ndcFromLatLong                       math.Matrix3
	ndcFromWindow                        math.Matrix3
	latLongFromWindow, windowFromLatLong math.Matrix3
}

// GetTransformations returns a Transformations object
// corresponding to the specified radar scope center, range, and rotation.
// rotationAngle is the angle from true north to the direction that points up
// on the scope: STARS scopes are magnetic north up, so the STARS pane passes
// the facility's magnetic variation, while ERAM scopes are always true north
// up.
func GetTransformations(drawExtent math.Extent2D, nmPerLongitude float32,
	center math.Point2LL, rangenm float32, rotationAngle float32) Transformations {
	width, height := drawExtent.Width(), drawExtent.Height()
	aspect := width / height
	ndcFromLatLong := math.Identity3x3().
		// Final orthographic projection including the effect of the
		// window's aspect ratio.
		Ortho(-aspect, aspect, -1, 1).
		// Orient the scope's up direction
		Rotate(-math.Radians(rotationAngle)).
		// Scale based on range and nm per latitude / longitude
		Scale(nmPerLongitude/rangenm, math.NMPerLatitude/rangenm).
		// Translate to center point
		Translate(-center[0], -center[1])

	ndcFromWindow := math.Identity3x3().
		Translate(-1, -1).
		Scale(2/width, 2/height)

	latLongFromNDC := ndcFromLatLong.Inverse()
	latLongFromWindow := latLongFromNDC.PostMultiply(ndcFromWindow)
	windowFromLatLong := latLongFromWindow.Inverse()

	return Transformations{
		ndcFromLatLong:    ndcFromLatLong,
		ndcFromWindow:     ndcFromWindow,
		latLongFromWindow: latLongFromWindow,
		windowFromLatLong: windowFromLatLong,
	}
}

// LoadLatLongViewingMatrices adds commands to the provided command buffer
// to load viewing matrices so that latitude-longiture positions can be
// provided for subsequent vertices.
func (st *Transformations) LoadLatLongViewingMatrices(cb *renderer.CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromLatLong)
	cb.LoadModelViewMatrix(math.Identity3x3())
}

// LoadWindowViewingMatrices adds commands to the provided command buffer
// to load viewing matrices so that window-coordinate positions can be
// provided for subsequent vertices.
func (st *Transformations) LoadWindowViewingMatrices(cb *renderer.CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromWindow)
	cb.LoadModelViewMatrix(math.Identity3x3())
}

// WindowFromLatLongP transforms a point given in latitude-longitude
// coordinates to window coordinates, snapped to a pixel center.
func (st *Transformations) WindowFromLatLongP(p math.Point2LL) [2]float32 {
	pw := st.windowFromLatLong.TransformPoint(p)
	pw[0], pw[1] = float32(int(pw[0]+0.5))+0.5, float32(int(pw[1]+0.5))+0.5
	return pw
}

// LatLongFromWindowP transforms a point p in window coordinates to
// latitude-longitude.
func (st *Transformations) LatLongFromWindowP(p [2]float32) math.Point2LL {
	return st.latLongFromWindow.TransformPoint(p)
}

// NormalizedFromWindowP transforms a point p in window coordinates to
// normalized [0,1]^2 coordinates.
func (st *Transformations) NormalizedFromWindowP(p [2]float32) [2]float32 {
	pn := st.ndcFromWindow.TransformPoint(p) // [-1,1]
	return [2]float32{(pn[0] + 1) / 2, (pn[1] + 1) / 2}
}

// LatLongFromWindowV transforms a vector in window coordinates to a vector
// in latitude-longitude coordinates.
func (st *Transformations) LatLongFromWindowV(v [2]float32) math.Point2LL {
	return st.latLongFromWindow.TransformVector(v)
}

// PixelDistanceNM returns the space between adjacent pixels expressed in
// nautical miles.
func (st *Transformations) PixelDistanceNM(nmPerLongitude float32) float32 {
	ll := st.LatLongFromWindowV([2]float32{1, 0})
	return math.NMLength2LL(ll, nmPerLongitude)
}

// Cache the aircraft state we show when paused but periodically fetch it
var pausedDisplayStates = util.NewTransientMap[av.ADSBCallsign, sim.AircraftDisplayState]()

// DrawPausedAircraftState draws the sim's summary of the aircraft's flight
// and the instructions it has been given beside its track, which is drawn
// at pw in window coordinates. Scopes call it for the track under the
// mouse while the sim is paused.
func DrawPausedAircraftState(ctx *Context, trk *sim.Track, pw [2]float32, style renderer.TextStyle,
	transforms Transformations, cb *renderer.CommandBuffer) {
	if trk.IsUnsupportedDB() {
		return
	}

	ads, ok := pausedDisplayStates.Get(trk.ADSBCallsign)
	if !ok {
		var err error
		if ads, err = ctx.Client.GetAircraftDisplayState(trk.ADSBCallsign); err != nil {
			ctx.Lg.Errorf("%s: error fetching display state: %s", trk.ADSBCallsign, err)
		} else {
			pausedDisplayStates.Add(trk.ADSBCallsign, ads, 2*time.Second)
		}
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	// Long routes and transcripts are wrapped, with continuation lines
	// indented, so that the text doesn't sprawl across the scope.
	wrap := func(s string) (string, int) { return util.WrapText(s, 70, 4, true, false) }

	// Upper-left corner of where we start drawing the text
	pad := float32(5)
	ptext := math.Add2f([2]float32{2 * pad, 0}, pw)
	font := style.Font
	flightState, _ := wrap(ads.FlightState)
	td.AddText(flightState, ptext, style)
	ext := font.LayoutBounds(flightState, style.LineSpacing)
	bx, by := ext.Width(), ext.Height()

	if len(ads.ControlTransmissions) > 0 {
		// The commands go under a header, one transmission per line, with
		// the transcripts of spoken ones in a column to their right. Blank
		// lines after a transmission's commands leave room for its
		// transcript's continuation lines.
		var cmds, transcripts []string
		for _, t := range ads.ControlTransmissions {
			transcript, n := wrap(t.Transcript)
			cmds = append(cmds, "  "+t.Commands+strings.Repeat("\n", n-1))
			transcripts = append(transcripts, transcript)
		}

		const header = "Instructions:"
		pheader := math.Add2f(ptext, [2]float32{0, -by})
		td.AddText(header, pheader, style)
		bx = max(bx, font.LayoutBounds(header, style.LineSpacing).Width())

		pcmds := math.Add2f(pheader, [2]float32{0, -float32(font.Size + style.LineSpacing)})
		cmdText := strings.Join(cmds, "\n")
		td.AddText(cmdText, pcmds, style)
		cext := font.LayoutBounds(cmdText, style.LineSpacing)

		gap := font.LayoutBounds("  ", 0).Width()
		transcriptText := strings.Join(transcripts, "\n")
		td.AddText(transcriptText, math.Add2f(pcmds, [2]float32{cext.Width() + gap, 0}), style)
		tw := font.LayoutBounds(transcriptText, style.LineSpacing).Width()

		bx = max(bx, util.Select(tw > 0, cext.Width()+gap+tw, cext.Width()))
		by = ptext[1] - pcmds[1] + cext.Height()
	}

	// Draw an alpha-blended quad behind the text to make it more legible.
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)
	trid.AddQuad(math.Add2f(ptext, [2]float32{-pad, 0}),
		math.Add2f(ptext, [2]float32{bx + pad, 0}),
		math.Add2f(ptext, [2]float32{bx + pad, -by - pad}),
		math.Add2f(ptext, [2]float32{-pad, -by - pad}))

	// Get it all into the command buffer
	transforms.LoadWindowViewingMatrices(cb)
	cb.SetRGBA(renderer.RGBA{R: 0.25, G: 0.25, B: 0.25, A: 0.75})
	cb.Blend()
	trid.GenerateCommands(cb)
	cb.DisableBlend()
	td.GenerateCommands(cb)
}

// DrawRoutePoints draws the points of a route being drawn with the scope's
// route drawing debugging command, joined in order.
func DrawRoutePoints(ctx *Context, transforms Transformations, cb *renderer.CommandBuffer, points []math.Point2LL) {
	if len(points) == 0 {
		return
	}

	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i, pt := range points {
		pwin := transforms.WindowFromLatLongP(pt)
		ld.AddCircle(pwin, 10, 30)
		if i+1 < len(points) {
			ld.AddLine(pwin, transforms.WindowFromLatLongP(points[i+1]))
		}
	}
	cb.LineWidth(1, ctx.DPIScale)
	cb.SetRGB(renderer.RGB{R: 1, G: .3, B: .3})
	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

// DrawPauseOverlay draws the SIMULATION PAUSED banner near the top of the
// scope while the sim is paused.
func DrawPauseOverlay(ctx *Context, cb *renderer.CommandBuffer, font *renderer.Font, background, text renderer.RGB) {
	if !ctx.Client.State.Paused {
		return
	}

	// Fixed position from top
	width := ctx.DrawExtent.Width()
	height := ctx.DrawExtent.Height()
	topOffset := height - 140
	textY := topOffset + 30      // Text will be 30px below top (in middle of background quad)
	quadTop := topOffset + 45    // Background extends 15px above text
	quadBottom := topOffset + 15 // Background extends 15px below text

	// Draw background quad (fixed width of 360px centered horizontally)
	quad := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(quad)
	quad.AddQuad(
		[2]float32{width/2 - 180, quadTop},    // Left-top
		[2]float32{width/2 + 180, quadTop},    // Right-top
		[2]float32{width/2 + 180, quadBottom}, // Right-bottom
		[2]float32{width/2 - 180, quadBottom}, // Left-bottom
		background)

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	td.AddTextCentered("SIMULATION PAUSED", [2]float32{width / 2, textY}, renderer.TextStyle{
		Font:  font,
		Color: text,
	})

	transforms := GetTransformations(ctx.DrawExtent, 0, [2]float32{}, 0, 0)
	transforms.LoadWindowViewingMatrices(cb)
	quad.GenerateCommands(cb)
	td.GenerateCommands(cb)
}
