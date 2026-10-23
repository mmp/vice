// eram/maps.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
)

// What is drawn under the tracks: the NEXRAD weather, the geomaps (video
// maps) and the MR command that changes groups, the scenario's routes
// chosen in the info window, and the .DRAWROUTE debugging aid.

///////////////////////////////////////////////////////////////////////////
// Geomaps

// drawMaps draws what is under the annotations and tracks: the weather,
// the video maps, the SAAs, the scenario's routes, and the points of a
// .DRAWROUTE route.
func (ep *Scope) drawMaps(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ep.drawWeatherRadar(ctx, transforms, cb)
	ep.drawVideoMaps(ctx, transforms, cb)
	ep.drawSAAs(ctx, transforms, cb)
	ep.ScenarioRoutes.Draw(ctx, transforms, ep.ERAMFont(1), ep.systemFont[3], cb)
	scope.DrawRoutePoints(ctx, transforms, cb, ep.drawRoutePoints)
}

func (ep *Scope) drawVideoMaps(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	ps := ep.currentPrefs()

	// Precompute a BCGIndex → RGB lookup table once per frame so the hot
	// path becomes an array index. The 0 slot and any out-of-range or
	// empty-named slot stay at the zero value (black/invisible), matching
	// the previous defensive bcgColor closure.
	var bcgRGB [256]renderer.RGB
	base := colors.videoMapBase
	for i, name := range ep.bcgNames {
		if name == "" {
			continue
		}
		bcgRGB[i+1] = ps.VideoMapBrightness[name].ScaleRGB(base)
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	var solidLineBuf [][2]float32 // reuse across all lines/maps

	// The current group's base map is always drawn; it has no filter-menu
	// button for the controller to turn off.
	bm := ep.baseVideoMap
	scope.DrawMapFeatures(bm.Lines, bm.Symbols, bm.Labels, &bcgRGB, ep, transforms, ld, td, &solidLineBuf)

	for _, vm := range ep.allVideoMaps {
		if _, ok := ps.VideoMapVisible[vm.Label()]; !ok {
			continue
		}
		scope.DrawMapFeatures(vm.Lines, vm.Symbols, vm.Labels, &bcgRGB, ep, transforms, ld, td, &solidLineBuf)
	}

	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (ep *Scope) makeMaps(client *client.ControlClient, lg *log.Logger) {
	ss := client.State
	ps := ep.currentPrefs()
	vmf, err := client.LoadVideoMapLibrary(ss.ControllerVideoMapFile)
	if err != nil {
		lg.Errorf("%v", err)
		return
	}

	ep.setVideoMapGroup(vmf, ps.VideoMapGroup)

	for _, name := range ss.ControllerDefaultVideoMaps {
		if slices.ContainsFunc(ep.allVideoMaps, func(v videomaps.ERAMMap) bool { return v.Label() == name }) {
			ps.VideoMapVisible[name] = nil
		}
	}
}

// setVideoMapGroup makes the named geomap group the current one: it fills in
// the filter menu and gives any BCG that doesn't have a brightness yet the
// default one.
func (ep *Scope) setVideoMapGroup(vmf *videomaps.Library, group string) {
	ps := ep.currentPrefs()
	maps := vmf.ERAMMapGroups[group]

	ps.VideoMapGroup = group
	if ps.VideoMapVisible == nil {
		ps.VideoMapVisible = make(map[string]any)
	}

	ep.allVideoMaps = maps.Maps
	ep.baseVideoMap = maps.BaseMap
	ep.bcgNames = maps.BCGNames
	ep.videoMapLabel = combine(maps.LabelLine1, maps.LabelLine2, "\n")

	for _, name := range maps.BCGNames {
		if name == "" { // empty placeholder slot
			continue
		}
		if _, ok := ps.VideoMapBrightness[name]; !ok { // only set default if missing
			ps.VideoMapBrightness[name] = 12
		}
	}
}

func combine(x, y, sep string) string {
	x = strings.TrimSpace(x)
	y = strings.TrimSpace(y)

	if x == "" {
		return y
	}
	if y == "" {
		return x
	}
	return x + sep + y
}

///////////////////////////////////////////////////////////////////////////
// MR - Map Request Handlers

func handleMapRequestList(ep *Scope, ctx *scope.Context) (CommandStatus, error) {
	vmf, err := ctx.Client.LoadVideoMapLibrary(ctx.Client.State.ControllerVideoMapFile)
	if err != nil {
		return CommandStatus{}, err
	}

	visibleNames := slices.Collect(maps.Keys(vmf.ERAMMapGroups))
	return CommandStatus{
		responseArea: []string{fmt.Sprintf("AVAILABLE GEOMAPS: %s", strings.Join(visibleNames, " "))},
	}, nil
}

func handleMapRequestLoad(ep *Scope, ctx *scope.Context, groupName string) (CommandStatus, error) {
	vmf, err := ctx.Client.LoadVideoMapLibrary(ctx.Client.State.ControllerVideoMapFile)
	if err != nil {
		return CommandStatus{}, err
	}

	if _, ok := vmf.ERAMMapGroups[groupName]; !ok {
		return CommandStatus{}, ErrMapUnavailable
	}

	ps := ep.currentPrefs()

	// Get rid of all visible maps
	ps.VideoMapVisible = make(map[string]any)

	ep.setVideoMapGroup(vmf, groupName)

	return CommandStatus{
		feedbackArea: []string{"ACCEPT", "MAP REQUEST", ps.VideoMapGroup},
	}, nil
}

///////////////////////////////////////////////////////////////////////////
// NEXRAD

// ERAM NEXRAD has three real levels (Moderate / Heavy / Extreme). Heavy
// is drawn through a checkerboard polygon stipple — the skipped screen
// pixels let the scope background show through, matching the "Heavy =
// checkered cyan and black" look in the ERAM docs.

var nexradScheme = scope.WxScheme{
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

func (ep *Scope) drawWeatherRadar(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
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
// OFF; middle-click adds one back). As with other toolbar value
// adjustments, the cycle doesn't wrap: a click past either end leaves the
// value alone and briefly shows the invalid-select/-enter cursor.
var nexradLevelCycle = []int{NexradToolbarAll, NexradToolbarHeavy, NexradToolbarExtreme, NexradToolbarOff}

func nexradLevelLabel(level int) string {
	if level == NexradToolbarOff {
		return "OFF"
	}
	return fmt.Sprintf("%d", level)
}

// clickNexradLevel drops the lowest displayed NEXRAD level on a left click
// and adds one back on a middle click.
func clickNexradLevel(ep *Scope, c toolbarClick) error {
	ps := ep.currentPrefs()
	idx := max(0, slices.Index(nexradLevelCycle, ps.NexradLevel))
	if c.clicked[platform.MouseButtonTertiary] {
		if idx == 0 {
			return errToolbarLimit
		}
		idx--
	} else {
		if idx+1 == len(nexradLevelCycle) {
			return errToolbarLimit
		}
		idx++
	}
	ps.NexradLevel = nexradLevelCycle[idx]
	return nil
}

///////////////////////////////////////////////////////////////////////////
// .DRAWROUTE - Custom command

// handleDrawRouteMode starts drawing a route: until Escape, clicks add
// points to it and Backspace removes the last one.
func handleDrawRouteMode(ep *Scope, ctx *scope.Context) CommandStatus {
	ep.drawingRoute = true
	ep.setDrawRoutePoints(ctx, nil)
	return CommandStatus{}
}

// setDrawRoutePoints sets the points of the route being drawn and copies
// them to the clipboard.
func (ep *Scope) setDrawRoutePoints(ctx *scope.Context, pts []math.Point2LL) {
	ep.drawRoutePoints = pts
	if len(pts) == 0 {
		ep.responseArea = "DRAWROUTE"
		return
	}
	ctx.Platform.GetClipboard().SetClipboard(strings.Join(util.MapSlice(pts, func(p math.Point2LL) string {
		return strings.ReplaceAll(p.DMSString(), " ", "")
	}), " "))
	ep.responseArea = fmt.Sprintf("DRAWROUTE: %d POINTS", len(pts))
}
