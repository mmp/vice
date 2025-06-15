package radar

import (
	"fmt"
	"math/bits"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

// ScopeTransformations

type ScopeTransformations struct {
	ndcFromLatLong                       math.Matrix3
	ndcFromWindow                        math.Matrix3
	latLongFromWindow, windowFromLatLong math.Matrix3
}

// GetScopeTransformations returns a ScopeTransformations object
// corresponding to the specified radar scope center, range, and rotation
// angle.
func GetScopeTransformations(paneExtent math.Extent2D, magneticVariation float32, nmPerLongitude float32,
	center math.Point2LL, rangenm float32, rotationAngle float32) ScopeTransformations {
	width, height := paneExtent.Width(), paneExtent.Height()
	aspect := width / height
	ndcFromLatLong := math.Identity3x3().
		// Final orthographic projection including the effect of the
		// window's aspect ratio.
		Ortho(-aspect, aspect, -1, 1).
		// Account for magnetic variation and any user-specified rotation
		Rotate(-math.Radians(rotationAngle+magneticVariation)).
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

	return ScopeTransformations{
		ndcFromLatLong:    ndcFromLatLong,
		ndcFromWindow:     ndcFromWindow,
		latLongFromWindow: latLongFromWindow,
		windowFromLatLong: windowFromLatLong,
	}
}

// LoadLatLongViewingMatrices adds commands to the provided command buffer
// to load viewing matrices so that latitude-longiture positions can be
// provided for subsequent vertices.
func (st *ScopeTransformations) LoadLatLongViewingMatrices(cb *renderer.CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromLatLong)
	cb.LoadModelViewMatrix(math.Identity3x3())
}

// LoadWindowViewingMatrices adds commands to the provided command buffer
// to load viewing matrices so that window-coordinate positions can be
// provided for subsequent vertices.
func (st *ScopeTransformations) LoadWindowViewingMatrices(cb *renderer.CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromWindow)
	cb.LoadModelViewMatrix(math.Identity3x3())
}

// WindowFromLatLongP transforms a point given in latitude-longitude
// coordinates to window coordinates, snapped to a pixel center.
func (st *ScopeTransformations) WindowFromLatLongP(p math.Point2LL) [2]float32 {
	pw := st.windowFromLatLong.TransformPoint(p)
	pw[0], pw[1] = float32(int(pw[0]+0.5))+0.5, float32(int(pw[1]+0.5))+0.5
	return pw
}

// LatLongFromWindowP transforms a point p in window coordinates to
// latitude-longitude.
func (st *ScopeTransformations) LatLongFromWindowP(p [2]float32) math.Point2LL {
	return st.latLongFromWindow.TransformPoint(p)
}

// NormalizedFromWindowP transforms a point p in window coordinates to
// normalized [0,1]^2 coordinates.
func (st *ScopeTransformations) NormalizedFromWindowP(p [2]float32) [2]float32 {
	pn := st.ndcFromWindow.TransformPoint(p) // [-1,1]
	return [2]float32{(pn[0] + 1) / 2, (pn[1] + 1) / 2}
}

// LatLongFromWindowV transforms a vector in window coordinates to a vector
// in latitude-longitude coordinates.
func (st *ScopeTransformations) LatLongFromWindowV(v [2]float32) math.Point2LL {
	return st.latLongFromWindow.TransformVector(v)
}

// PixelDistanceNM returns the space between adjacent pixels expressed in
// nautical miles.
func (st *ScopeTransformations) PixelDistanceNM(nmPerLongitude float32) float32 {
	ll := st.LatLongFromWindowV([2]float32{1, 0})
	return math.NMLength2LL(ll, nmPerLongitude)
}

// Other

// The above stipple masks are ordered so that they match the orientation
// of how we want them drawn on the screen, though that doesn't seem to be
// how glPolygonStipple expects them, which is with the bits in each byte
// reversed. I think that we should just be able to call
// gl.PixelStorei(gl.PACK_LSB_FIRST, gl.FALSE) and provide them as above,
// though that doesn't seem to work.  Hence, we just reverse the bytes by
// hand.
func ReverseStippleBytes(stipple [32]uint32) [32]uint32 {
	var result [32]uint32
	for i, line := range stipple {
		a, b, c, d := uint8(line>>24), uint8(line>>16), uint8(line>>8), uint8(line)
		a, b, c, d = bits.Reverse8(a), bits.Reverse8(b), bits.Reverse8(c), bits.Reverse8(d)
		result[i] = uint32(a)<<24 + uint32(b)<<16 + uint32(c)<<8 + uint32(d)
	}
	return result
}

// pt should return nm-based coordinates
func CalculateOffset(font *renderer.Font, pt func(int) ([2]float32, bool)) [2]float32 {
	prev, pok := pt(-1)
	cur, _ := pt(0)
	next, nok := pt(1)

	vecAngle := func(p0, p1 [2]float32) float32 {
		v := math.Normalize2f(math.Sub2f(p1, p0))
		return math.Atan2(v[0], v[1])
	}

	const Pi = 3.1415926535
	angle := float32(0)
	if !pok {
		if !nok {
			// wtf?
		}
		// first point
		angle = vecAngle(cur, next)
	} else if !nok {
		// last point
		angle = vecAngle(prev, cur)
	} else {
		// have both prev and next
		angle = (vecAngle(prev, cur) + vecAngle(cur, next)) / 2 // ??
	}

	if angle < 0 {
		angle -= Pi / 2
	} else {
		angle += Pi / 2
	}

	offset := math.Scale2f([2]float32{math.Sin(angle), math.Cos(angle)}, 8)

	h := math.NormalizeHeading(math.Degrees(angle))
	if (h >= 160 && h < 200) || (h >= 340 || h < 20) {
		// Center(ish) the text if the line is more or less horizontal.
		offset[0] -= 2.5 * float32(font.Size)
	}
	return offset
}

func GenerateRouteDrawingCommands(cb *renderer.CommandBuffer, transforms ScopeTransformations, ctx *panes.Context,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, td *renderer.TextDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {
	transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	pd.GenerateCommands(cb)
	td.GenerateCommands(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ldr.GenerateCommands(cb)
}

func DrawWaypoints(ctx *panes.Context, waypoints []av.Waypoint, drawnWaypoints map[string]interface{},
	transforms ScopeTransformations, td *renderer.TextDrawBuilder, style renderer.TextStyle,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder, color renderer.RGB) {

	// Draw an arrow at the point p (in nm coordinates) pointing in the
	// direction given by the angle a.
	drawArrow := func(p [2]float32, a float32) {
		aa := a + math.Radians(180+30)
		pa := math.Add2f(p, math.Scale2f([2]float32{math.Sin(aa), math.Cos(aa)}, 0.5))
		ld.AddLine(math.NM2LL(p, ctx.NmPerLongitude), math.NM2LL(pa, ctx.NmPerLongitude), color)

		ba := a - math.Radians(180+30)
		pb := math.Add2f(p, math.Scale2f([2]float32{math.Sin(ba), math.Cos(ba)}, 0.5))
		ld.AddLine(math.NM2LL(p, ctx.NmPerLongitude), math.NM2LL(pb, ctx.NmPerLongitude), color)
	}

	for i, wp := range waypoints {
		if wp.Heading != 0 {
			// Don't draw a segment to the next waypoint (if there is one)
			// but instead draw an arrow showing the heading.
			a := math.Radians(float32(wp.Heading) - ctx.MagneticVariation)
			v := [2]float32{math.Sin(a), math.Cos(a)}
			v = math.Scale2f(v, 2)
			pend := math.LL2NM(waypoints[i].Location, ctx.NmPerLongitude)
			pend = math.Add2f(pend, v)

			// center line
			ld.AddLine(waypoints[i].Location, math.NM2LL(pend, ctx.NmPerLongitude), color)

			// arrowhead at the end
			drawArrow(pend, a)
		} else if i+1 < len(waypoints) {
			if wp.Arc != nil {
				// Draw DME arc. One subtlety is that although the arc's
				// radius should cause it to pass through the waypoint, it
				// may be slightly off due to error from using nm
				// coordinates and the approximation of a fixed nm per
				// longitude value.  So, we'll compute the radius to the
				// point in nm coordinates and store it in r0 and do the
				// same for the end point. Then we will interpolate those
				// radii along the arc.
				pc := math.LL2NM(wp.Arc.Center, ctx.NmPerLongitude)
				p0 := math.LL2NM(waypoints[i].Location, ctx.NmPerLongitude)
				r0 := math.Distance2f(p0, pc)
				v0 := math.Normalize2f(math.Sub2f(p0, pc))
				a0 := math.NormalizeHeading(math.Degrees(math.Atan2(v0[0], v0[1]))) // angle w.r.t. the arc center

				p1 := math.LL2NM(waypoints[i+1].Location, ctx.NmPerLongitude)
				r1 := math.Distance2f(p1, pc)
				v1 := math.Normalize2f(math.Sub2f(p1, pc))
				a1 := math.NormalizeHeading(math.Degrees(math.Atan2(v1[0], v1[1])))

				// Draw a segment every degree
				n := int(math.HeadingDifference(a0, a1))
				a := a0
				pprev := waypoints[i].Location
				for i := 1; i < n-1; i++ {
					if wp.Arc.Clockwise {
						a += 1
					} else {
						a -= 1
					}
					a = math.NormalizeHeading(a)
					r := math.Lerp(float32(i)/float32(n), r0, r1)
					v := math.Scale2f([2]float32{math.Sin(math.Radians(a)), math.Cos(math.Radians(a))}, r)
					pnext := math.NM2LL(math.Add2f(pc, v), ctx.NmPerLongitude)
					ld.AddLine(pprev, pnext, color)
					pprev = pnext

					if i == n/2 {
						// Draw an arrow at the midpoint showing the arc's direction
						drawArrow(math.Add2f(pc, v), util.Select(wp.Arc.Clockwise, math.Radians(a+90), math.Radians(a-90)))
					}
				}
				ld.AddLine(pprev, waypoints[i+1].Location, color)
			} else {
				// Regular segment between waypoints: draw the line
				ld.AddLine(waypoints[i].Location, waypoints[i+1].Location, color)

				if waypoints[i+1].ProcedureTurn == nil &&
					!(waypoints[i].ProcedureTurn != nil && waypoints[i].ProcedureTurn.Type == av.PTStandard45) {
					// Draw an arrow indicating direction of flight along
					// the segment, unless the next waypoint has a
					// procedure turn. In that case, we'll let the PT draw
					// the arrow..
					p0 := math.LL2NM(waypoints[i].Location, ctx.NmPerLongitude)
					p1 := math.LL2NM(waypoints[i+1].Location, ctx.NmPerLongitude)
					v := math.Sub2f(p1, p0)
					drawArrow(math.Mid2f(p0, p1), math.Atan2(v[0], v[1]))
				}
			}
		}

		if pt := wp.ProcedureTurn; pt != nil {
			if i+1 >= len(waypoints) {
				ctx.Lg.Errorf("Expected another waypoint after the procedure turn?")
			} else {
				// In the following, we will draw a canonical procedure
				// turn of the appropriate type. e.g., for a racetrack, we
				// generate points for a canonical racetrack
				// vertically-oriented, with width 2, and with the origin
				// at the left side of the arc at the top.  The toNM
				// transformation takes that to nm coordinates which we'll
				// later transform to lat-long to draw on the scope.
				toNM := math.Identity3x3()

				pnm := math.LL2NM(wp.Location, ctx.NmPerLongitude)
				toNM = toNM.Translate(pnm[0], pnm[1])

				p1nm := math.LL2NM(waypoints[i+1].Location, ctx.NmPerLongitude)
				v := math.Sub2f(p1nm, pnm)
				hdg := math.Atan2(v[0], v[1])
				toNM = toNM.Rotate(-hdg)
				if !pt.RightTurns {
					toNM = toNM.Scale(-1, 1)
				}

				// FIXME: reuse the logic in nav.go to compute the leg lengths.
				len := float32(pt.NmLimit)
				if len == 0 {
					len = float32(pt.MinuteLimit * 3) // assume 180 GS...
				}
				if len == 0 {
					len = 4
				}

				var lines [][2][2]float32
				addseg := func(a, b [2]float32) {
					lines = append(lines, [2][2]float32{toNM.TransformPoint(a), toNM.TransformPoint(b)})
				}
				drawarc := func(center [2]float32, a0, a1 int) [2]float32 {
					var prev [2]float32
					step := util.Select(a0 < a1, 1, -1)
					for i := a0; i != a1; i += step {
						v := [2]float32{math.Sin(math.Radians(float32(i))), math.Cos(math.Radians(float32(i)))}
						pt := math.Add2f(center, v)
						if i != a0 {
							addseg(prev, pt)
						}
						prev = pt
					}
					return prev
				}

				if pt.Type == av.PTRacetrack {
					// Lines for the two sides
					addseg([2]float32{0, 0}, [2]float32{0, -len})
					addseg([2]float32{2, 0}, [2]float32{2, -len})

					// Arcs at each end; all of this is slightly simpler since
					// the width of the racetrack is 2, so the radius of the
					// arcs is 1...
					drawarc([2]float32{1, 0}, -90, 90)
					drawarc([2]float32{1, -len}, 90, 270)

					drawArrow(toNM.TransformPoint([2]float32{0, -len / 2}), hdg)
					drawArrow(toNM.TransformPoint([2]float32{2, -len / 2}), hdg+math.Radians(180))
				} else if pt.Type == av.PTStandard45 {
					// Line outbound to the next fix
					addseg([2]float32{0, 0}, [2]float32{0, len / 2})

					// 45 degrees off from that for 4nm
					const sqrt2over2 = 0.70710678
					pe := [2]float32{4 * sqrt2over2, len/2 + 4*sqrt2over2}
					addseg([2]float32{0, len / 2}, pe)

					// Draw an arc from the previous leg around to the inbound course.
					pae := drawarc(math.Add2f(pe, [2]float32{-sqrt2over2, sqrt2over2}), 135, -45)
					// Intercept of the 45 degree line from the end of the
					// arc back to the y axis.
					pint := [2]float32{0, pae[1] - pae[0]}
					addseg(pae, pint)

					// inbound course + arrow
					pinb := math.Add2f(pint, [2]float32{0, -1})
					addseg(pint, pinb)
					drawArrow(toNM.TransformPoint(pinb), hdg+math.Radians(180))
				} else {
					ctx.Lg.Errorf("unhandled PT type in drawWaypoints")
				}

				for _, l := range lines {
					l0, l1 := math.NM2LL(l[0], ctx.NmPerLongitude), math.NM2LL(l[1], ctx.NmPerLongitude)
					ld.AddLine(l0, l1, color)
				}
			}
		}

		drawName := wp.Fix[0] != '_'
		if _, err := math.ParseLatLong([]byte(wp.Fix)); err == nil {
			// Also don't draw names that are directly specified as latlongs.
			drawName = false
		}

		if _, ok := drawnWaypoints[wp.Fix]; ok {
			// And if we're given the same fix more than once (as may
			// happen with T-shaped RNAV arrivals for example), only draw
			// it once. We'll assume/hope that we're not seeing it with
			// different restrictions...
			continue
		}

		// Record that we have drawn this waypoint
		drawnWaypoints[wp.Fix] = nil

		// Draw a circle at the waypoint's location
		const pointRadius = 2.5
		const nSegments = 8
		pd.AddCircle(transforms.WindowFromLatLongP(wp.Location), pointRadius, nSegments, color)

		// If /radius has been specified, draw a corresponding circle
		if wp.Radius > 0 {
			ld.AddLatLongCircle(wp.Location, ctx.NmPerLongitude,
				wp.Radius, 32)
		}

		// For /shift, extend the line beyond the waypoint (just in case)
		// and draw perpendicular bars at the ends.
		if wp.Shift > 0 {
			prev := waypoints[i-1]
			v := math.Sub2f(wp.Location, prev.Location)
			v = math.Scale2f(v, 1/math.NMDistance2LL(wp.Location, prev.Location)) // ~1nm length
			v = math.Scale2f(v, wp.Shift/2)

			// extend the line
			e0, e1 := math.Sub2f(wp.Location, v), math.Add2f(wp.Location, v)
			ld.AddLine(wp.Location, e1, color)

			perp := [2]float32{-v[1], v[0]}
			perp = math.Scale2f(perp, 0.125) // shorter

			ld.AddLine(math.Sub2f(e0, perp), math.Add2f(e0, perp), color)
			ld.AddLine(math.Sub2f(e1, perp), math.Add2f(e1, perp), color)
		}

		offset := CalculateOffset(style.Font, func(j int) ([2]float32, bool) {
			idx := i + j
			if idx < 0 || idx >= len(waypoints) {
				return [2]float32{}, false
			}
			return math.LL2NM(waypoints[idx].Location, ctx.NmPerLongitude), true
		})

		// Draw the text for the waypoint, including fix name, any
		// properties, and altitude/speed restrictions.
		p := transforms.WindowFromLatLongP(wp.Location)
		p = math.Add2f(p, offset)
		if drawName {
			p = td.AddText(wp.Fix+"\n", p, style)
		}

		if wp.IAF || wp.IF || wp.FAF || wp.NoPT || wp.FlyOver {
			var s []string
			if wp.IAF {
				s = append(s, "IAF")
			}
			if wp.IF {
				s = append(s, "IF")
			}
			if wp.FAF {
				s = append(s, "FAF")
			}
			if wp.NoPT {
				s = append(s, "NoPT")
			}
			if wp.FlyOver {
				s = append(s, "FlyOver")
			}
			p = td.AddText(strings.Join(s, "/")+"\n", p, style)
		}

		if wp.Speed != 0 || wp.AltitudeRestriction != nil {
			p[1] -= 0.25 * float32(style.Font.Size) // extra space for lines above if needed

			if ar := wp.AltitudeRestriction; ar != nil {
				pt := p       // draw position for text
				var w float32 // max width of altitudes drawn
				if ar.Range[1] != 0 {
					// Upper altitude
					pp := td.AddText(av.FormatAltitude(ar.Range[1]), pt, style)
					w = pp[0] - pt[0]
					pt[1] -= float32(style.Font.Size)
				}
				if ar.Range[0] != 0 && ar.Range[0] != ar.Range[1] {
					// Lower altitude, if present and different than upper.
					pp := td.AddText(av.FormatAltitude(ar.Range[0]), pt, style)
					w = max(w, pp[0]-pt[0])
					pt[1] -= float32(style.Font.Size)
				}

				// Now that we have w, we can draw lines the specify the
				// restrictions.
				if ar.Range[1] != 0 {
					// At or below (or at)
					ldr.AddLine([2]float32{p[0], p[1] + 2}, [2]float32{p[0] + w, p[1] + 2}, color)
				}
				if ar.Range[0] != 0 {
					// At or above (or at)
					ldr.AddLine([2]float32{p[0], pt[1] - 2}, [2]float32{p[0] + w, pt[1] - 2}, color)
				}

				// update text draw position so that speed restrictions are
				// drawn in a reasonable place; note that we maintain the
				// original p[1] regardless of how many lines were drawn
				// for altitude restrictions.
				p[0] += w + 4
			}

			if wp.Speed != 0 {
				p0 := p
				p1 := td.AddText(fmt.Sprintf("%dK", wp.Speed), p, style)
				p1[1] -= float32(style.Font.Size)

				// All speed restrictions are currently 'at'...
				ldr.AddLine([2]float32{p0[0], p0[1] + 2}, [2]float32{p1[0], p0[1] + 2}, color)
				ldr.AddLine([2]float32{p0[0], p1[1] - 2}, [2]float32{p1[0], p1[1] - 2}, color)
			}
		}
	}
}