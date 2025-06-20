package eram

import (
	"strings"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/radar"
)

type CommandMode int

func (ep *ERAMPane) consumeMouseEvents(ctx *panes.Context, transforms radar.ScopeTransformations) {
	mouse := ctx.Mouse
	if mouse == nil {
		return
	}
	if (ctx.Mouse.Clicked[platform.MouseButtonPrimary] || ctx.Mouse.Clicked[platform.MouseButtonSecondary] ||
		ctx.Mouse.Clicked[platform.MouseButtonTertiary]) && !ctx.HaveFocus {
		ctx.KeyboardFocus.Take(ep)
	}
	if mouse.Released[platform.MouseButtonPrimary] {
		if ctx.Keyboard != nil && ctx.Keyboard.KeyShift() && ctx.Keyboard.KeyControl() {
			mouseLatLong := transforms.LatLongFromWindowP(mouse.Pos)
			ctx.Platform.GetClipboard().SetClipboard(strings.ReplaceAll(mouseLatLong.DMSString(), " ", ""))

		}
	}
	ps := ep.currentPrefs()
	// try get closest track 
	
	// pan 

	if mouse.Dragging[platform.MouseButtonSecondary] {
			delta := mouse.DragDelta
			if delta[0] != 0 || delta[1] != 0 {
				deltaLL := transforms.LatLongFromWindowV(delta)
				ps.CurrentCenter = math.Sub2f(ps.CurrentCenter, deltaLL)
			}
		}

	// zoom 
	if mouse.Wheel[1] != 0 {
		
			r := ps.Range
			ps.Range += func() float32 {
				if ctx.Keyboard != nil {
					if ctx.Keyboard.KeyControl() {
						return 3 * mouse.Wheel[1]
					}
				}
				return mouse.Wheel[1]
			}()
			ps.Range = math.Clamp(ps.Range, .25, 1300) // 4-33

			// We want to zoom in centered at the mouse position; this affects
			// the scope center after the zoom, so we'll find the
			// transformation that gives the new center position.
			mouseLL := transforms.LatLongFromWindowP(mouse.Pos)
			scale := ps.Range / r
			centerTransform := math.Identity3x3().
				Translate(mouseLL[0], mouseLL[1]).
				Scale(scale, scale).
				Translate(-mouseLL[0], -mouseLL[1])

			ps.CurrentCenter = centerTransform.TransformPoint(ps.CurrentCenter)	
			
		}
}
