package eram

import (
	"fmt"

	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/radar"
	"github.com/mmp/vice/renderer"
)

// ERAM NEXRAD has three real levels (Moderate / Heavy / Extreme). Heavy
// is drawn through a checkerboard polygon stipple — the skipped screen
// pixels let the scope background show through, matching the "Heavy =
// checkered cyan and black" look in the ERAM docs.
const (
	nexradLevelModerate = iota + 1
	nexradLevelHeavy
	nexradLevelExtreme
)

var nexradScheme = radar.WxScheme{
	// Moderate: dBZ 25-35, Heavy: 35-45, Extreme: 45+.
	Thresholds: []byte{25, 35, 45},
}

// nexradHeavyStipple is a checkerboard of 4x4-pixel squares. Polygon
// stipple patterns are anchored to window coordinates, so the checker
// stays the same size on screen regardless of the scope's zoom.
var nexradHeavyStipple = func() [32]uint32 {
	var p [32]uint32
	for y := range 32 {
		if (y/4)%2 == 0 {
			p[y] = 0xf0f0f0f0
		} else {
			p[y] = 0x0f0f0f0f
		}
	}
	return p
}()

// ERAM NEXRAD colors. Brightness is applied at draw time via
// ps.Brightness.NEXRAD.
var (
	nexradModerateColor = renderer.RGB{R: 0, G: 0, B: 1}
	nexradHeavyColor    = renderer.RGB{R: 0, G: 1, B: 1}
	nexradExtremeColor  = renderer.RGB{R: 0, G: 1, B: 1}
)

// nexradFilter is the set of ERAM levels enabled by the NX LVL toolbar
// button.
type nexradFilter struct{ Moderate, Heavy, Extreme bool }

func nexradFilterFromPref(level int) nexradFilter {
	return nexradFilter{
		Moderate: level == NexradToolbarAll,
		Heavy:    level == NexradToolbarAll || level == NexradToolbarHeavy,
		Extreme:  level == NexradToolbarAll || level == NexradToolbarHeavy || level == NexradToolbarExtreme,
	}
}

// nexradCBs caches the three ERAM-level command buffers along with the
// WeatherRadar generation counter they were built from, so we rebuild
// only when the underlying precip blob changes.
type nexradCBs struct {
	moderate, heavy, extreme *renderer.CommandBuffer
	generation               int
}

func (ep *ERAMPane) drawWeatherRadar(ctx *panes.Context, transforms radar.ScopeTransformations, cb *renderer.CommandBuffer) {
	precip, gen := ep.weatherRadar.LatestPrecip(ctx)
	if precip == nil {
		return
	}
	if ep.nexrad.generation != gen {
		cbs := nexradScheme.MakeCommandBuffers(precip)
		ep.nexrad = nexradCBs{
			moderate:   cbs[0],
			heavy:      cbs[1],
			extreme:    cbs[2],
			generation: gen,
		}
	}

	ps := ep.currentPrefs()
	intensity := float32(ps.Brightness.NEXRAD) / 100
	filter := nexradFilterFromPref(ps.NexradLevel)

	transforms.LoadLatLongViewingMatrices(cb)
	if filter.Moderate && ep.nexrad.moderate != nil {
		cb.SetRGB(nexradModerateColor.Scale(intensity))
		cb.Call(*ep.nexrad.moderate)
	}
	if filter.Heavy && ep.nexrad.heavy != nil {
		cb.SetRGB(nexradHeavyColor.Scale(intensity))
		cb.EnablePolygonStipple()
		cb.PolygonStipple(nexradHeavyStipple)
		cb.Call(*ep.nexrad.heavy)
		cb.DisablePolygonStipple()
	}
	if filter.Extreme && ep.nexrad.extreme != nil {
		cb.SetRGB(nexradExtremeColor.Scale(intensity))
		cb.Call(*ep.nexrad.extreme)
	}
}

// NX LVL toolbar button cycle (left-click drops a level: 123 → 23 → 3 →
// OFF; middle-click reverses).
var nexradLevelCycle = []int{NexradToolbarAll, NexradToolbarHeavy, NexradToolbarExtreme, NexradToolbarOff}

func nexradLevelLabel(level int) string {
	if level == NexradToolbarOff {
		return "OFF"
	}
	return fmt.Sprintf("%d", level)
}

func handleNexradLevelClick(ep *ERAMPane, pref *int) {
	mouse := toolbarDrawState.mouse
	if mouse == nil {
		return
	}

	idx := 0
	for i, v := range nexradLevelCycle {
		if v == *pref {
			idx = i
			break
		}
	}

	switch {
	case ep.mousePrimaryClicked(mouse):
		idx = (idx + 1) % len(nexradLevelCycle)
	case ep.mouseTertiaryClicked(mouse):
		idx = (idx - 1 + len(nexradLevelCycle)) % len(nexradLevelCycle)
	}
	*pref = nexradLevelCycle[idx]
}
