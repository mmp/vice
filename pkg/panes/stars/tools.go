// pkg/panes/stars/tools.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	_ "embed"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log/slog"
	gomath "math"
	"math/bits"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

///////////////////////////////////////////////////////////////////////////
// WeatherRadar

// WeatherRadar provides functionality for fetching radar images to display
// in radar scopes. Only locations in the USA are currently supported, as
// the only current data source is the US NOAA...
type WeatherRadar struct {
	active bool

	// Radar images are fetched and processed in a separate goroutine;
	// updated radar center locations are sent from the main thread via
	// reqChan and command buffers to draw each of the 6 weather levels are
	// returned by cbChan.
	reqChan chan math.Point2LL
	cbChan  chan [numWxLevels]*renderer.CommandBuffer
	cb      [numWxHistory][numWxLevels]*renderer.CommandBuffer
}

const numWxHistory = 3

const numWxLevels = 6

// Block size in pixels of the quads in the converted radar image used for
// display.
const wxBlockRes = 2

// Latitude-longitude extent of the fetched image; the requests are +/-
// this much from the current center.
const wxLatLongExtent = 2.5

// Activate must be called to initialize the WeatherRadar before weather
// radar images can be fetched.
func (w *WeatherRadar) Activate(r renderer.Renderer, lg *log.Logger) {
	if w.active {
		return
	}

	w.active = true
	if w.reqChan == nil {
		w.reqChan = make(chan math.Point2LL, 32)
	}
	w.cbChan = make(chan [numWxLevels]*renderer.CommandBuffer, 8)

	go fetchWeather(w.reqChan, w.cbChan, lg)
}

func (w *WeatherRadar) HaveWeather() [numWxLevels]bool {
	var r [numWxLevels]bool
	for i := range numWxLevels {
		r[i] = w.cb[0][i] != nil
	}
	return r
}

// UpdateCenter provides a new center point for the radar image, causing a
// new image to be fetched.
func (w *WeatherRadar) UpdateCenter(center math.Point2LL) {
	// UpdateCenter may be called before Activate, e.g. when we are loading
	// a saved sim, so at least set up the chan so that we keep the center
	// point.
	if w.reqChan == nil {
		w.reqChan = make(chan math.Point2LL, 32)
	}
	select {
	case w.reqChan <- center:
		// success
	default:
		// The channel is full..
	}
}

// A single scanline of this color map, converted to RGB bytes:
// https://opengeo.ncep.noaa.gov/geoserver/styles/reflectivity.png
//
//go:embed radar_reflectivity.rgb
var radarReflectivity []byte

type kdNode struct {
	rgb  [3]byte
	refl float32
	c    [2]*kdNode
}

var radarReflectivityKdTree *kdNode

func init() {
	type rgbRefl struct {
		rgb  [3]byte
		refl float32
	}

	var r []rgbRefl

	for i := 0; i < len(radarReflectivity); i += 3 {
		r = append(r, rgbRefl{
			rgb:  [3]byte{radarReflectivity[i], radarReflectivity[i+1], radarReflectivity[i+2]},
			refl: float32(i) / float32(len(radarReflectivity)),
		})
	}

	// Build a kd-tree over the RGB points in the color map.
	var buildTree func(r []rgbRefl, depth int) *kdNode
	buildTree = func(r []rgbRefl, depth int) *kdNode {
		if len(r) == 0 {
			return nil
		}
		if len(r) == 1 {
			return &kdNode{rgb: r[0].rgb, refl: r[0].refl}
		}

		// The split dimension cycles through RGB with tree depth.
		dim := depth % 3

		// Sort the points in the current dimension
		sort.Slice(r, func(i, j int) bool {
			return r[i].rgb[dim] < r[j].rgb[dim]
		})

		// Split in the middle and recurse
		mid := len(r) / 2
		return &kdNode{
			rgb:  r[mid].rgb,
			refl: r[mid].refl,
			c:    [2]*kdNode{buildTree(r[:mid], depth+1), buildTree(r[mid+1:], depth+1)},
		}
	}

	radarReflectivityKdTree = buildTree(r, 0)
}

func invertRadarReflectivity(rgb [3]byte) float32 {
	// All white -> 0
	if rgb[0] == 255 && rgb[1] == 255 && rgb[2] == 255 {
		return 0
	}

	// Returns the distnace between the specified RGB and the RGB passed to
	// invertRadarReflectivity.
	dist := func(o []byte) float32 {
		d2 := math.Sqr(int(o[0])-int(rgb[0])) + math.Sqr(int(o[1])-int(rgb[1])) + math.Sqr(int(o[2])-int(rgb[2]))
		return math.Sqrt(float32(d2))
	}

	var searchTree func(n *kdNode, closestNode *kdNode, closestDist float32, depth int) (*kdNode, float32)
	searchTree = func(n *kdNode, closestNode *kdNode, closestDist float32, depth int) (*kdNode, float32) {
		if n == nil {
			return closestNode, closestDist
		}

		// Check the current node
		d := dist(n.rgb[:])
		if d < closestDist {
			closestDist = d
			closestNode = n
		}

		// Split dimension as in buildTree above
		dim := depth % 3

		// Initially traverse the tree based on which side of the split
		// plane the lookup point is on.
		var first, second *kdNode
		if rgb[dim] < n.rgb[dim] {
			first, second = n.c[0], n.c[1]
		} else {
			first, second = n.c[1], n.c[0]
		}

		closestNode, closestDist = searchTree(first, closestNode, closestDist, depth+1)

		// If the distance to the split plane is less than the distance to
		// the closest point found so far, we need to check the other side
		// of the split.
		if float32(math.Abs(int(rgb[dim])-int(n.rgb[dim]))) < closestDist {
			closestNode, closestDist = searchTree(second, closestNode, closestDist, depth+1)
		}

		return closestNode, closestDist
	}

	if true {
		n, _ := searchTree(radarReflectivityKdTree, nil, 100000, 0)
		return n.refl
	} else {
		// Debugging: verify the point found is indeed the closest by
		// exhaustively checking the distance to all of points in the color
		// map.
		n, nd := searchTree(radarReflectivityKdTree, nil, 100000, 0)

		closest, closestDist := -1, float32(100000)
		for i := 0; i < len(radarReflectivity); i += 3 {
			d := dist(radarReflectivity[i : i+3])
			if d < closestDist {
				closestDist = d
				closest = i
			}
		}

		// Note that multiple points in the color map may have the same
		// distance to the lookup point; thus we only check the distance
		// here and not the reflectivity (which should be very close but is
		// not necessarily the same.)
		if nd != closestDist {
			fmt.Printf("WAH %d,%d,%d -> %d,%d,%d: dist %f vs %d,%d,%d: dist %f\n",
				int(rgb[0]), int(rgb[1]), int(rgb[2]),
				int(n.rgb[0]), int(n.rgb[1]), int(n.rgb[2]), nd,
				int(radarReflectivity[closest]), int(radarReflectivity[closest+1]), int(radarReflectivity[closest+2]),
				closestDist)
		}

		return n.refl
	}
}

// fetchWeather runs asynchronously in a goroutine, receiving requests from
// reqChan, fetching corresponding radar images from the NOAA, and sending
// the results back on cbChan.  New images are also automatically
// fetched periodically, with a wait time specified by the delay parameter.
func fetchWeather(reqChan chan math.Point2LL, cbChan chan [numWxLevels]*renderer.CommandBuffer, lg *log.Logger) {
	// STARS seems to get new radar roughly every 5 minutes
	const fetchRate = 5 * time.Minute

	// center stores the current center position of the radar image
	var center math.Point2LL
	fetchTimer := time.NewTimer(fetchRate)
	for {
		var ok bool
		// Wait until we get an updated center or we've timed out on fetchRate.
		select {
		case center, ok = <-reqChan:
			if ok {
				// Drain any additional requests so that we get the most
				// recent one.
				for len(reqChan) > 0 {
					center = <-reqChan
				}
			} else {
				// The channel is closed; wrap up.
				close(cbChan)
				return
			}
		case <-fetchTimer.C:
			// Periodically make a new request even if the center hasn't
			// changed.
		}

		fetchTimer.Reset(fetchRate)
		lg.Infof("Getting WX, center %v", center)

		// Lat-long bounds of the region we're going to request weather for.
		rb := math.Extent2D{P0: math.Sub2LL(center, math.Point2LL{wxLatLongExtent, wxLatLongExtent}),
			P1: math.Add2LL(center, math.Point2LL{wxLatLongExtent, wxLatLongExtent})}

		// The weather radar image comes via a WMS GetMap request from the NOAA.
		//
		// Relevant background:
		// https://enterprise.arcgis.com/en/server/10.3/publish-services/windows/communicating-with-a-wms-service-in-a-web-browser.htm
		// http://schemas.opengis.net/wms/1.3.0/capabilities_1_3_0.xsd
		// NOAA weather: https://opengeo.ncep.noaa.gov/geoserver/www/index.html
		// https://opengeo.ncep.noaa.gov/geoserver/conus/conus_bref_qcd/ows?service=wms&version=1.3.0&request=GetCapabilities
		params := url.Values{}
		params.Add("SERVICE", "WMS")
		params.Add("REQUEST", "GetMap")
		params.Add("FORMAT", "image/png")
		params.Add("WIDTH", "2048")
		params.Add("HEIGHT", "2048")
		params.Add("LAYERS", "conus_bref_qcd")
		params.Add("BBOX", fmt.Sprintf("%f,%f,%f,%f", rb.P0[0], rb.P0[1], rb.P1[0], rb.P1[1]))

		url := "https://opengeo.ncep.noaa.gov/geoserver/conus/conus_bref_qcd/ows?" + params.Encode()

		// Request the image
		lg.Info("Fetching weather", slog.String("url", url))
		resp, err := http.Get(url)
		if err != nil {
			lg.Infof("Weather error: %s", err)
			continue
		}
		defer resp.Body.Close()

		img, err := png.Decode(resp.Body)
		if err != nil {
			lg.Infof("Weather error: %s", err)
			continue
		}

		cbChan <- makeWeatherCommandBuffers(img, rb)

		lg.Info("finish weather fetch")
	}
}

func makeWeatherCommandBuffers(img image.Image, rb math.Extent2D) [numWxLevels]*renderer.CommandBuffer {
	// Convert the Image returned by png.Decode to a simple 8-bit RGBA image.
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, img.Bounds(), img, image.Point{}, draw.Over)

	ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
	nby, nbx := ny/wxBlockRes, nx/wxBlockRes

	// First determine the weather level for each wxBlockRes*wxBlockRes
	// block of the image.
	levels := make([]int, nbx*nby)
	for y := 0; y < nby; y++ {
		for x := 0; x < nbx; x++ {
			avg := float32(0)
			for dy := 0; dy < wxBlockRes; dy++ {
				for dx := 0; dx < wxBlockRes; dx++ {
					px := rgba.RGBAAt(x*wxBlockRes+dx, y*wxBlockRes+dy)
					avg += invertRadarReflectivity([3]byte{px.R, px.G, px.B})
				}
			}

			// levels from [0,6].
			level := int(math.Min(avg*7/(wxBlockRes*wxBlockRes), 6))
			levels[x+y*nbx] = level
		}
	}

	// Now generate the command buffer for each weather level.  We don't
	// draw anything for level==0, so the indexing into cb is off by 1
	// below.
	var cb [numWxLevels]*renderer.CommandBuffer
	tb := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(tb)

	for level := 1; level <= numWxLevels; level++ {
		tb.Reset()
		levelHasWeather := false

		// We'd like to be somewhat efficient and not necessarily draw an
		// individual quad for each block, but on the other hand don't want
		// to make this too complicated... So we'll consider block
		// scanlines and quads across neighbors that are the same level
		// when we find them.
		for y := 0; y < nby; y++ {
			for x := 0; x < nbx; x++ {
				// Skip ahead until we reach a block at the level we currently care about.
				if levels[x+y*nbx] != level {
					continue
				}
				levelHasWeather = true

				// Now see how long a span of repeats we have.
				// Each quad spans [0,0]->[1,1] in texture coordinates; the
				// texture is created with repeat wrap mode, so we just pad
				// out the u coordinate into u1 accordingly.
				x0 := x
				u1 := float32(0)
				for x < nbx && levels[x+y*nbx] == level {
					x++
					u1++
				}

				// Corner points
				p0 := rb.Lerp([2]float32{float32(x0) / float32(nbx), float32(y) / float32(nby)})
				p1 := rb.Lerp([2]float32{float32(x) / float32(nbx), float32(y+1) / float32(nby)})

				// Draw a single quad
				tb.AddQuad([2]float32{p0[0], p0[1]}, [2]float32{p1[0], p0[1]},
					[2]float32{p1[0], p1[1]}, [2]float32{p0[0], p1[1]})
			}
		}

		// Subtract one so that level==1 is drawn by cb[0], etc, since we
		// don't draw anything for level==0.
		if levelHasWeather {
			cb[level-1] = &renderer.CommandBuffer{}
			tb.GenerateCommands(cb[level-1])
		}
	}

	return cb
}

// Stipple patterns. We expect glPixelStore(GL_PACK_LSB_FIRST, GL_TRUE) to
// be set for these.
var wxStippleLight [32]uint32 = [32]uint32{
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000011000000000000000000,
	0b00000000000011000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000001100000000,
	0b00000000000000000000001100000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000001100000000000000000000000,
	0b00000001100000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000110000000000000000,
	0b00000000000000110000000000000000,
	0b00000000000000000000000000001100,
	0b00000000000000000000000000001100,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000110000000000000000000000,
	0b00000000110000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000011000000000000,
	0b00000000000000000011000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b11000000000000000000000000000000,
	0b11000000000000000000000000000000,
}

// Note that the basis pattern in the lower 16x16 is repeated both
// horizontally and vertically.
var wxStippleDense [32]uint32 = [32]uint32{
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00001000000000000000100000000000,
	0b00001000000000000000100000000000,
	0b00000000000110000000000000011000,
	0b01000000000000000100000000000000,
	0b01000000000000000100000000000000,
	0b00000001100000000000000110000000,
	0b00000000000000000000000000000000,
	0b00000000000000110000000000000011,
	0b00000000000000000000000000000000,
	0b00011000000000000001100000000000,
	0b00000000000000000000000000000000,
	0b00000000001000000000000000100000,
	0b00000000001000000000000000100000,
	0b11000000000000001100000000000000,
	0b00000000000000000000000000000000,
	0b00000000000000000000000000000000,
	0b00001000000000000000100000000000,
	0b00001000000000000000100000000000,
	0b00000000000110000000000000011000,
	0b01000000000000000100000000000000,
	0b01000000000000000100000000000000,
	0b00000001100000000000000110000000,
	0b00000000000000000000000000000000,
	0b00000000000000110000000000000011,
	0b00000000000000000000000000000000,
	0b00011000000000000001100000000000,
	0b00000000000000000000000000000000,
	0b00000000001000000000000000100000,
	0b00000000001000000000000000100000,
	0b11000000000000001100000000000000,
}

// The above stipple masks are ordered so that they match the orientation
// of how we want them drawn on the screen, though that doesn't seem to be
// how glPolygonStipple expects them, which is with the bits in each byte
// reversed. I think that we should just be able to call
// gl.PixelStorei(gl.PACK_LSB_FIRST, gl.FALSE) and provide them as above,
// though that doesn't seem to work.  Hence, we just reverse the bytes by
// hand.
func reverseStippleBytes(stipple [32]uint32) [32]uint32 {
	var result [32]uint32
	for i, line := range stipple {
		a, b, c, d := uint8(line>>24), uint8(line>>16), uint8(line>>8), uint8(line)
		a, b, c, d = bits.Reverse8(a), bits.Reverse8(b), bits.Reverse8(c), bits.Reverse8(d)
		result[i] = uint32(a)<<24 + uint32(b)<<16 + uint32(c)<<8 + uint32(d)
	}
	return result
}

// Draw draws the current weather radar image, if available. (If none is yet
// available, it returns rather than stalling waiting for it).
func (w *WeatherRadar) Draw(ctx *panes.Context, hist int, intensity float32, contrast float32,
	active [numWxLevels]bool, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	select {
	case cb := <-w.cbChan:
		// Got updated command buffers, yaay.  Note that we always drain
		// the cbChan, even if if the WeatherRadar is inactive.

		// Shift history down before storing the latest
		w.cb[2], w.cb[1] = w.cb[1], w.cb[0]
		w.cb[0] = cb

	default:
		// no message
	}

	if !w.active {
		return
	}

	hist = math.Clamp(hist, 0, len(w.cb)-1)
	transforms.LoadLatLongViewingMatrices(cb)
	for i := range w.cb[hist] {
		if active[i] && w.cb[hist][i] != nil {
			// RGBs from STARS Manual, B-5
			baseColor := util.Select(i < 3,
				renderer.RGBFromUInt8(37, 77, 77), renderer.RGBFromUInt8(100, 100, 51))
			cb.SetRGB(baseColor.Scale(intensity))
			cb.Call(*w.cb[hist][i])

			if i == 0 || i == 3 {
				// No stipple
				continue
			}

			cb.EnablePolygonStipple()
			if i == 1 || i == 4 {
				cb.PolygonStipple(reverseStippleBytes(wxStippleLight))
			} else if i == 2 || i == 5 {
				cb.PolygonStipple(reverseStippleBytes(wxStippleDense))
			}
			// Draw the same quads again, just with a different color and stippled.
			cb.SetRGB(renderer.RGB{contrast, contrast, contrast})
			cb.Call(*w.cb[hist][i])
			cb.DisablePolygonStipple()
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Additional useful things we may draw on radar scopes...

// DrawCompass emits drawing commands to draw compass heading directions at
// the edges of the current window. It takes a center point p in lat-long
// coordinates, transformation functions and the radar scope's current
// rotation angle, if any.  Drawing commands are added to the provided
// command buffer, which is assumed to have projection matrices set up for
// drawing using window coordinates.
func (sp *STARSPane) drawCompass(ctx *panes.Context, scopeExtent math.Extent2D, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()
	if ps.Brightness.Compass == 0 {
		return
	}

	// Window coordinates of the center point.
	// TODO: should we explicitly handle the case of this being outside the window?
	pw := transforms.WindowFromLatLongP(ps.CurrentCenter)
	bounds := math.Extent2D{P1: [2]float32{scopeExtent.Width(), scopeExtent.Height()}}
	font := sp.systemFont[ps.CharSize.Tools]
	color := ps.Brightness.Compass.ScaleRGB(STARSCompassColor)

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	// Draw lines at a 5 degree spacing.
	for h := float32(5); h <= 360; h += 5 {
		hr := h
		dir := [2]float32{math.Sin(math.Radians(hr)), math.Cos(math.Radians(hr))}
		// Find the intersection of the line from the center point to the edge of the window.
		isect, _, t := bounds.IntersectRay(pw, dir)
		if !isect {
			// Happens on initial launch w/o a sector file...
			//lg.Infof("no isect?! p %+v dir %+v bounds %+v", pw, dir, ctx.paneExtent)
			continue
		}

		// Draw a short line from the intersection point at the edge to the
		// point ten pixels back inside the window toward the center.
		pEdge := math.Add2f(pw, math.Scale2f(dir, t))
		pInset := math.Add2f(pw, math.Scale2f(dir, t-10))
		ld.AddLine(pEdge, pInset)

		// Every 10 degrees draw a heading label.
		if int(h)%10 == 0 {
			// Generate the label ourselves rather than via fmt.Sprintf,
			// out of some probably irrelevant attempt at efficiency.
			label := []byte{'0', '0', '0'}
			hi := int(h)
			for i := 2; i >= 0 && hi != 0; i-- {
				label[i] = byte('0' + hi%10)
				hi /= 10
			}

			bx, by := font.BoundText(string(label), 0)

			// Initial inset to place the text--a little past the end of
			// the line.
			pText := math.Add2f(pw, math.Scale2f(dir, t-14))

			// Finer text positioning depends on which edge of the window
			// pane we're on; this is made more grungy because text drawing
			// is specified w.r.t. the position of the upper-left corner...
			if math.Abs(pEdge[0]) < .125 {
				// left edge
				pText[1] += float32(by) / 2
			} else if math.Abs(pEdge[0]-bounds.P1[0]) < .125 {
				// right edge
				pText[0] -= float32(bx)
				pText[1] += float32(by) / 2
			} else if math.Abs(pEdge[1]) < .125 {
				// bottom edge
				pText[0] -= float32(bx) / 2
				pText[1] += float32(by)
			} else if math.Abs(pEdge[1]-bounds.P1[1]) < .125 {
				// top edge
				pText[0] -= float32(bx) / 2
			} else {
				ctx.Lg.Infof("Edge borkage! pEdge %+v, bounds %+v", pEdge, bounds)
			}

			td.AddText(string(label), pText, renderer.TextStyle{Font: font, Color: color})
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	cb.SetRGB(color)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// DrawRangeRings draws ten circles around the specified lat-long point in
// steps of the specified radius (in nm).
func (sp *STARSPane) drawRangeRings(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()
	if ps.Brightness.RangeRings == 0 {
		return
	}

	pixelDistanceNm := transforms.PixelDistanceNM(ctx.ControlClient.NmPerLongitude)
	ctr := util.Select(ps.RangeRingsUserCenter, ps.RangeRingsCenter, ps.Center)
	centerWindow := transforms.WindowFromLatLongP(ctr)

	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	for i := 1; i < 40; i++ {
		// Radius of this ring in pixels
		r := float32(i) * float32(ps.RangeRingRadius) / pixelDistanceNm
		ld.AddCircle(centerWindow, r, 360)
	}

	cb.LineWidth(1, ctx.DPIScale)
	color := ps.Brightness.RangeRings.ScaleRGB(STARSRangeRingColor)
	cb.SetRGB(color)
	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// ScopeTransformations

// ScopeTransformations manages various transformation matrices that are
// useful when drawing radar scopes and provides a number of useful methods
// to transform among related coordinate spaces.
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

///////////////////////////////////////////////////////////////////////////
// Other utilities

// If distance to a significant point is being displayed or if the user has
// run the "find" command to highlight a point in the world, draw a blinking
// square at that point for a few seconds.
func (sp *STARSPane) drawHighlighted(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	remaining := time.Until(sp.highlightedLocationEndTime)
	if remaining < 0 {
		return
	}

	// "The color of the blinking square is the same as that for blinking
	// data block information"(?)
	ps := sp.currentPrefs()
	color := ps.Brightness.FullDatablocks.ScaleRGB(STARSUntrackedAircraftColor)
	halfSeconds := ctx.Now.UnixMilli() / 500
	blinkDim := halfSeconds&1 == 0
	if blinkDim {
		color = color.Scale(0.5)
	}

	p := transforms.WindowFromLatLongP(sp.highlightedLocation)
	delta := float32(4)
	td := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(td)
	td.AddQuad(math.Add2f(p, [2]float32{-delta, -delta}), math.Add2f(p, [2]float32{delta, -delta}),
		math.Add2f(p, [2]float32{delta, delta}), math.Add2f(p, [2]float32{-delta, delta}))

	transforms.LoadWindowViewingMatrices(cb)
	cb.SetRGB(color)
	td.GenerateCommands(cb)
}

// Draw all of the range-bearing lines that have been specified.
func (sp *STARSPane) drawRBLs(aircraft []*av.Aircraft, ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	ps := sp.currentPrefs()
	color := ps.Brightness.Lines.RGB() // check
	style := renderer.TextStyle{
		Font:  sp.systemFont[ps.CharSize.Tools],
		Color: color,
	}

	drawRBL := func(p0 math.Point2LL, p1 math.Point2LL, idx int, gs float32) {
		// Format the range-bearing line text for the two positions.
		hdg := math.Heading2LL(p0, p1, ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation)
		dist := math.NMDistance2LL(p0, p1)
		text := fmt.Sprintf(" %03d/%.2f", int(hdg+.5), dist) // leading space for alignment
		if gs != 0 {
			// Add ETA in minutes
			eta := 60 * dist / gs
			text += fmt.Sprintf("/%d", int(eta+.5))
		}
		text += fmt.Sprintf("-%d", idx)

		// And draw the line and the text.
		pText := transforms.WindowFromLatLongP(p1) // draw at right endpoint
		//pText[1] += float32(style.Font.Size / 2)   // vertically align
		td.AddText(text, pText, style)
		ld.AddLine(p0, p1, color)
	}

	// Maybe draw a wip RBL with p1 as the mouse's position
	if sp.wipRBL != nil {
		wp := sp.wipRBL.P[0]
		if ctx.Mouse != nil {
			p1 := transforms.LatLongFromWindowP(ctx.Mouse.Pos)
			if wp.Callsign != "" {
				if ac := ctx.ControlClient.Aircraft[wp.Callsign]; ac != nil && sp.datablockVisible(ac, ctx) &&
					slices.Contains(aircraft, ac) {
					if state, ok := sp.Aircraft[wp.Callsign]; ok {
						drawRBL(state.TrackPosition(), p1, len(sp.RangeBearingLines)+1, ac.GS())
					}
				}
			} else {
				drawRBL(wp.Loc, p1, len(sp.RangeBearingLines)+1, 0)
			}
		}
	}

	for i, rbl := range sp.RangeBearingLines {
		if p0, p1 := rbl.GetPoints(ctx, aircraft, sp); !p0.IsZero() && !p1.IsZero() {
			gs := float32(0)

			// If one but not both are tracks, get the groundspeed so we
			// can display an ETA.
			if rbl.P[0].Callsign != "" {
				if rbl.P[1].Callsign == "" {
					if ac := ctx.ControlClient.Aircraft[rbl.P[0].Callsign]; ac != nil {
						gs = ac.GS()
					}
				}
			} else if rbl.P[1].Callsign != "" {
				if rbl.P[0].Callsign == "" {
					if ac := ctx.ControlClient.Aircraft[rbl.P[1].Callsign]; ac != nil {
						gs = ac.GS()
					}
				}
			}

			drawRBL(p0, p1, i+1, gs)
		}
	}

	// Remove stale ones that include aircraft that have landed, etc.
	sp.RangeBearingLines = util.FilterSlice(sp.RangeBearingLines, func(rbl STARSRangeBearingLine) bool {
		p0, p1 := rbl.GetPoints(ctx, aircraft, sp)
		return !p0.IsZero() && !p1.IsZero()
	})

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

// Draw the minimum separation line between two aircraft, if selected.
func (sp *STARSPane) drawMinSep(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	cs0, cs1 := sp.MinSepAircraft[0], sp.MinSepAircraft[1]
	if cs0 == "" || cs1 == "" {
		// Two aircraft haven't been specified.
		return
	}
	ac0, ok0 := ctx.ControlClient.Aircraft[cs0]
	ac1, ok1 := ctx.ControlClient.Aircraft[cs1]
	if !ok0 || !ok1 {
		// Missing aircraft
		return
	}

	ps := sp.currentPrefs()
	color := ps.Brightness.Lines.RGB()

	s0, ok0 := sp.Aircraft[ac0.Callsign]
	s1, ok1 := sp.Aircraft[ac1.Callsign]
	if !ok0 || !ok1 {
		return
	}

	// Go ahead and draw the minimum separation lines and text.
	p0ll, p1ll := s0.TrackPosition(), s1.TrackPosition()
	d0ll := s0.HeadingVector(ac0.NmPerLongitude(), ac0.MagneticVariation())
	d1ll := s1.HeadingVector(ac1.NmPerLongitude(), ac1.MagneticVariation())

	p0, d0 := math.LL2NM(p0ll, ac0.NmPerLongitude()), math.LL2NM(d0ll, ac0.NmPerLongitude())
	p1, d1 := math.LL2NM(p1ll, ac1.NmPerLongitude()), math.LL2NM(d1ll, ac1.NmPerLongitude())

	// Find the parametric distance along the respective rays of the
	// aircrafts' courses where they at at a minimum distance; this is
	// linearly extrapolating their positions.
	tmin := math.RayRayMinimumDistance(p0, d0, p1, d1)

	// If something blew up in RayRayMinimumDistance then just bail out here.
	if gomath.IsInf(float64(tmin), 0) || gomath.IsNaN(float64(tmin)) {
		return
	}

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	font := sp.systemFont[ps.CharSize.Tools]

	// Draw the separator lines (and triangles, if appropriate.)
	var pw0, pw1 [2]float32          // Window coordinates of the points of minimum approach
	var p0tmin, p1tmin math.Point2LL // Lat-long coordinates of the points of minimum approach
	if tmin < 0 {
		// The closest approach was in the past; just draw a line between
		// the two tracks and initialize the above coordinates.
		ld.AddLine(p0ll, p1ll, color)
		p0tmin, p1tmin = p0ll, p1ll
		pw0, pw1 = transforms.WindowFromLatLongP(p0ll), transforms.WindowFromLatLongP(p1ll)
	} else {
		// Closest approach in the future: draw a line from each track to
		// the minimum separation line as well as the minimum separation
		// line itself.
		p0tmin = math.NM2LL(math.Add2f(p0, math.Scale2f(d0, tmin)), ac0.NmPerLongitude())
		p1tmin = math.NM2LL(math.Add2f(p1, math.Scale2f(d1, tmin)), ac1.NmPerLongitude())
		ld.AddLine(p0ll, p0tmin, color)
		ld.AddLine(p0tmin, p1tmin, color)
		ld.AddLine(p1tmin, p1ll, color)

		// Draw filled triangles centered at p0tmin and p1tmin.
		pw0, pw1 = transforms.WindowFromLatLongP(p0tmin), transforms.WindowFromLatLongP(p1tmin)
		style := renderer.TextStyle{Font: font, Color: color}
		td.AddTextCentered(STARSFilledUpTriangle, pw0, style)
		td.AddTextCentered(STARSFilledUpTriangle, pw1, style)
	}

	// Draw the text for the minimum distance
	// Center the text along the minimum distance line
	pText := math.Mid2f(pw0, pw1)
	style := renderer.TextStyle{
		Font:            font,
		Color:           color,
		DrawBackground:  true,
		BackgroundColor: renderer.RGB{},
	}
	text := fmt.Sprintf("%.2fNM", math.NMDistance2LL(p0tmin, p1tmin))
	if tmin < 0 {
		text = "NO XING\n" + text
	}
	td.AddTextCentered(text, pText, style)

	// Add the corresponding drawing commands to the CommandBuffer.
	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	cb.SetRGB(color)
	trid.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawAirspace(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	ps := sp.currentPrefs()
	rgb := ps.Brightness.Lists.ScaleRGB(STARSListColor)

	drawSectors := func(volumes []sim.ControllerAirspaceVolume) {
		for _, v := range volumes {
			e := math.EmptyExtent2D()

			for _, pts := range v.Boundaries {
				for i := range pts {
					e = math.Union(e, pts[i])
					if i < len(pts)-1 {
						ld.AddLine(pts[i], pts[i+1], rgb)
					}
				}
			}

			center := e.Center()
			ps := sp.currentPrefs()
			style := renderer.TextStyle{
				Font:           sp.systemFont[ps.CharSize.Tools],
				Color:          rgb,
				DrawBackground: true, // default BackgroundColor is fine
			}
			alts := fmt.Sprintf("%d-%d", v.LowerLimit/100, v.UpperLimit/100)
			td.AddTextCentered(alts, transforms.WindowFromLatLongP(center), style)
		}
	}

	if sp.drawApproachAirspace {
		drawSectors(ctx.ControlClient.ApproachAirspace)
	}

	if sp.drawDepartureAirspace {
		drawSectors(ctx.ControlClient.DepartureAirspace)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
	transforms.LoadWindowViewingMatrices(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawScenarioRoutes(ctx *panes.Context, transforms ScopeTransformations, font *renderer.Font, color renderer.RGB,
	cb *renderer.CommandBuffer) {
	drawArrivals := ctx.ControlClient.ScopeDrawArrivals()
	drawApproaches := ctx.ControlClient.ScopeDrawApproaches()
	drawDepartures := ctx.ControlClient.ScopeDrawDepartures()
	drawOverflights := ctx.ControlClient.ScopeDrawOverflights()

	if len(drawArrivals) == 0 && len(drawApproaches) == 0 && len(drawDepartures) == 0 && len(drawOverflights) == 0 {
		return
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)
	pd := renderer.GetTrianglesDrawBuilder() // for circles
	defer renderer.ReturnTrianglesDrawBuilder(pd)
	ldr := renderer.GetLinesDrawBuilder() // for restrictions--in window coords...
	defer renderer.ReturnLinesDrawBuilder(ldr)

	// Track which waypoints have been drawn so that we don't repeatedly
	// draw the same one.  (This is especially important since the
	// placement of the labels depends on the inbound/outbound segments,
	// which may be different for different uses of the waypoint...)
	drawnWaypoints := make(map[string]interface{})

	style := renderer.TextStyle{
		Font:           font,
		Color:          color,
		DrawBackground: true}

	// STARS
	if drawArrivals != nil {
		for _, name := range util.SortedMapKeys(ctx.ControlClient.InboundFlows) {
			if drawArrivals[name] == nil {
				continue
			}

			arrivals := ctx.ControlClient.InboundFlows[name].Arrivals
			for i, arr := range arrivals {
				if drawArrivals == nil || !drawArrivals[name][i] {
					continue
				}

				drawWaypoints(ctx, arr.Waypoints, drawnWaypoints, transforms, td, style, ld, pd, ldr)

				// Draw runway-specific waypoints
				for _, ap := range util.SortedMapKeys(arr.RunwayWaypoints) {
					for _, rwy := range util.SortedMapKeys(arr.RunwayWaypoints[ap]) {
						wp := arr.RunwayWaypoints[ap][rwy]
						drawWaypoints(ctx, wp, drawnWaypoints, transforms, td, style, ld, pd, ldr)

						if len(wp) > 1 {
							// Draw the runway number in the middle of the line
							// between the first two waypoints.
							pmid := math.Mid2LL(wp[0].Location, wp[1].Location)
							td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pmid), style)
						} else if wp[0].Heading != 0 {
							// This should be the only other case... The heading arrow is drawn
							// up to 2nm out, so put the runway 1nm along its axis.
							a := math.Radians(float32(wp[0].Heading) - ctx.ControlClient.MagneticVariation)
							v := [2]float32{math.Sin(a), math.Cos(a)}
							pend := math.LL2NM(wp[0].Location, ctx.ControlClient.NmPerLongitude)
							pend = math.Add2f(pend, v)
							pell := math.NM2LL(pend, ctx.ControlClient.NmPerLongitude)
							td.AddTextCentered(rwy, transforms.WindowFromLatLongP(pell), style)
						}
					}
				}
			}
		}
	}

	// Approaches
	if drawApproaches != nil {
		for _, rwy := range ctx.ControlClient.ArrivalRunways {
			if drawApproaches[rwy.Airport] == nil {
				continue
			}
			ap := ctx.ControlClient.Airports[rwy.Airport]
			for _, name := range util.SortedMapKeys(ap.Approaches) {
				appr := ap.Approaches[name]
				if appr.Runway == rwy.Runway && drawApproaches[rwy.Airport][name] {
					for _, wp := range appr.Waypoints {
						drawWaypoints(ctx, wp, drawnWaypoints, transforms, td, style, ld, pd, ldr)
					}
				}
			}
		}
	}

	// Departure routes
	if drawDepartures != nil {
		for _, name := range util.SortedMapKeys(ctx.ControlClient.Airports) {
			if drawDepartures[name] == nil {
				continue
			}

			ap := ctx.ControlClient.Airports[name]
			for _, rwy := range util.SortedMapKeys(ap.DepartureRoutes) {
				if drawDepartures[name][rwy] == nil {
					continue
				}

				exitRoutes := ap.DepartureRoutes[rwy]
				for _, exit := range util.SortedMapKeys(exitRoutes) {
					if drawDepartures[name][rwy][exit] {
						drawWaypoints(ctx, exitRoutes[exit].Waypoints, drawnWaypoints, transforms,
							td, style, ld, pd, ldr)
					}
				}
			}
		}
	}

	// Overflights
	if drawOverflights != nil {
		for _, name := range util.SortedMapKeys(ctx.ControlClient.InboundFlows) {
			if drawOverflights[name] == nil {
				continue
			}

			overflights := ctx.ControlClient.InboundFlows[name].Overflights
			for i, of := range overflights {
				if drawOverflights == nil || !drawOverflights[name][i] {
					continue
				}

				drawWaypoints(ctx, of.Waypoints, drawnWaypoints, transforms, td, style, ld, pd, ldr)
			}
		}
	}

	// And now finally update the command buffer with everything we've
	// drawn.
	cb.SetRGB(color)
	transforms.LoadLatLongViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)

	transforms.LoadWindowViewingMatrices(cb)
	pd.GenerateCommands(cb)
	td.GenerateCommands(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ldr.GenerateCommands(cb)
}

// pt should return nm-based coordinates
func calculateOffset(font *renderer.Font, pt func(int) ([2]float32, bool)) [2]float32 {
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

func drawWaypoints(ctx *panes.Context, waypoints []av.Waypoint, drawnWaypoints map[string]interface{},
	transforms ScopeTransformations, td *renderer.TextDrawBuilder, style renderer.TextStyle,
	ld *renderer.LinesDrawBuilder, pd *renderer.TrianglesDrawBuilder, ldr *renderer.LinesDrawBuilder) {

	// Draw an arrow at the point p (in nm coordinates) pointing in the
	// direction given by the angle a.
	drawArrow := func(p [2]float32, a float32) {
		aa := a + math.Radians(180+30)
		pa := math.Add2f(p, math.Scale2f([2]float32{math.Sin(aa), math.Cos(aa)}, 0.5))
		ld.AddLine(math.NM2LL(p, ctx.ControlClient.NmPerLongitude), math.NM2LL(pa, ctx.ControlClient.NmPerLongitude))

		ba := a - math.Radians(180+30)
		pb := math.Add2f(p, math.Scale2f([2]float32{math.Sin(ba), math.Cos(ba)}, 0.5))
		ld.AddLine(math.NM2LL(p, ctx.ControlClient.NmPerLongitude), math.NM2LL(pb, ctx.ControlClient.NmPerLongitude))
	}

	for i, wp := range waypoints {
		if wp.Heading != 0 {
			// Don't draw a segment to the next waypoint (if there is one)
			// but instead draw an arrow showing the heading.
			a := math.Radians(float32(wp.Heading) - ctx.ControlClient.MagneticVariation)
			v := [2]float32{math.Sin(a), math.Cos(a)}
			v = math.Scale2f(v, 2)
			pend := math.LL2NM(waypoints[i].Location, ctx.ControlClient.NmPerLongitude)
			pend = math.Add2f(pend, v)

			// center line
			ld.AddLine(waypoints[i].Location, math.NM2LL(pend, ctx.ControlClient.NmPerLongitude))

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
				pc := math.LL2NM(wp.Arc.Center, ctx.ControlClient.NmPerLongitude)
				p0 := math.LL2NM(waypoints[i].Location, ctx.ControlClient.NmPerLongitude)
				r0 := math.Distance2f(p0, pc)
				v0 := math.Normalize2f(math.Sub2f(p0, pc))
				a0 := math.NormalizeHeading(math.Degrees(math.Atan2(v0[0], v0[1]))) // angle w.r.t. the arc center

				p1 := math.LL2NM(waypoints[i+1].Location, ctx.ControlClient.NmPerLongitude)
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
					pnext := math.NM2LL(math.Add2f(pc, v), ctx.ControlClient.NmPerLongitude)
					ld.AddLine(pprev, pnext)
					pprev = pnext

					if i == n/2 {
						// Draw an arrow at the midpoint showing the arc's direction
						drawArrow(math.Add2f(pc, v), util.Select(wp.Arc.Clockwise, math.Radians(a+90), math.Radians(a-90)))
					}
				}
				ld.AddLine(pprev, waypoints[i+1].Location)
			} else {
				// Regular segment between waypoints: draw the line
				ld.AddLine(waypoints[i].Location, waypoints[i+1].Location)

				if waypoints[i+1].ProcedureTurn == nil {
					// Draw an arrow indicating direction of flight along
					// the segment, unless the next waypoint has a
					// procedure turn. In that case, we'll let the PT draw
					// the arrow..
					p0 := math.LL2NM(waypoints[i].Location, ctx.ControlClient.NmPerLongitude)
					p1 := math.LL2NM(waypoints[i+1].Location, ctx.ControlClient.NmPerLongitude)
					v := math.Sub2f(p1, p0)
					drawArrow(math.Mid2f(p0, p1), math.Atan2(v[0], v[1]))
				}
			}
		}

		if pt := wp.ProcedureTurn; pt != nil {
			if i+1 >= len(waypoints) {
				ctx.Lg.Errorf("Expected another waypoint after the procedure turn?")
			} else {
				// In the following, we will generate points a canonical
				// racetrack vertically-oriented, with width 2, and with
				// the origin at the left side of the arc at the top.  The
				// toNM transformation takes that to nm coordinates which
				// we'll later transform to lat-long to draw on the scope.
				toNM := math.Identity3x3()

				pnm := math.LL2NM(wp.Location, ctx.ControlClient.NmPerLongitude)
				toNM = toNM.Translate(pnm[0], pnm[1])

				p1nm := math.LL2NM(waypoints[i+1].Location, ctx.ControlClient.NmPerLongitude)
				v := math.Sub2f(p1nm, pnm)
				hdg := math.Atan2(v[0], v[1])
				toNM = toNM.Rotate(-hdg)
				if !pt.RightTurns {
					toNM = toNM.Translate(-2, 0)
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
				// Lines for the two sides
				lines = append(lines,
					[2][2]float32{
						toNM.TransformPoint([2]float32{0, 0}),
						toNM.TransformPoint([2]float32{0, -len})},
					[2][2]float32{
						toNM.TransformPoint([2]float32{2, 0}),
						toNM.TransformPoint([2]float32{2, -len})})

				// Arcs at each end; all of this is slightly simpler since
				// the width of the racetrack is 2, so the radius of the
				// arcs is 1...
				// previous top and bottom points
				prevt := toNM.TransformPoint([2]float32{0, 0})
				prevb := toNM.TransformPoint([2]float32{2, -len})
				for i := -90; i <= 90; i++ {
					v := [2]float32{math.Sin(math.Radians(float32(i))), math.Cos(math.Radians(float32(i)))}

					// top
					pt := math.Add2f([2]float32{1, 0}, v)
					pt = toNM.TransformPoint(pt)
					lines = append(lines, [2][2]float32{prevt, pt})
					prevt = pt

					// bottom
					pb := math.Sub2f([2]float32{1, -len}, v)
					pb = toNM.TransformPoint(pb)
					lines = append(lines, [2][2]float32{prevb, pb})
					prevb = pb
				}

				for _, l := range lines {
					l0, l1 := math.NM2LL(l[0], ctx.ControlClient.NmPerLongitude), math.NM2LL(l[1], ctx.ControlClient.NmPerLongitude)
					ld.AddLine(l0, l1)
				}

				drawArrow(toNM.TransformPoint([2]float32{0, -len / 2}), hdg)
				drawArrow(toNM.TransformPoint([2]float32{2, -len / 2}), hdg+math.Radians(180))
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
		pd.AddCircle(transforms.WindowFromLatLongP(wp.Location), pointRadius, nSegments)

		offset := calculateOffset(style.Font, func(j int) ([2]float32, bool) {
			idx := i + j
			if idx < 0 || idx >= len(waypoints) {
				return [2]float32{}, false
			}
			return math.LL2NM(waypoints[idx].Location, ctx.ControlClient.NmPerLongitude), true
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
					w = math.Max(w, pp[0]-pt[0])
					pt[1] -= float32(style.Font.Size)
				}

				// Now that we have w, we can draw lines the specify the
				// restrictions.
				if ar.Range[1] != 0 {
					// At or below (or at)
					ldr.AddLine([2]float32{p[0], p[1] + 2}, [2]float32{p[0] + w, p[1] + 2})
				}
				if ar.Range[0] != 0 {
					// At or above (or at)
					ldr.AddLine([2]float32{p[0], pt[1] - 2}, [2]float32{p[0] + w, pt[1] - 2})
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
				ldr.AddLine([2]float32{p0[0], p0[1] + 2}, [2]float32{p1[0], p0[1] + 2})
				ldr.AddLine([2]float32{p0[0], p1[1] - 2}, [2]float32{p1[0], p1[1] - 2})
			}
		}
	}
}

func (sp *STARSPane) drawPTLs(aircraft []*av.Aircraft, ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	ps := sp.currentPrefs()

	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)

	color := ps.Brightness.Lines.RGB()

	now := ctx.ControlClient.SimTime
	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) || !state.HaveHeading() {
			continue
		}

		trk := sp.getTrack(ctx, ac)
		ourTrack := trk != nil && trk.TrackOwner == ctx.ControlClient.Callsign
		if !state.DisplayPTL && !ps.PTLAll && !(ps.PTLOwn && ourTrack) {
			continue
		}

		if ps.PTLLength == 0 {
			continue
		}

		// convert PTL length (minutes) to estimated distance a/c will travel
		dist := float32(state.TrackGroundspeed()) / 60 * ps.PTLLength

		// h is a vector in nm coordinates with length l=dist
		hdg := state.TrackHeading(ac.NmPerLongitude())
		h := [2]float32{math.Sin(math.Radians(hdg)), math.Cos(math.Radians(hdg))}
		h = math.Scale2f(h, dist)
		end := math.Add2f(math.LL2NM(state.TrackPosition(), ac.NmPerLongitude()), h)

		ld.AddLine(state.TrackPosition(), math.NM2LL(end, ac.NmPerLongitude()), color)
	}

	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

func (sp *STARSPane) drawRingsAndCones(aircraft []*av.Aircraft, ctx *panes.Context, transforms ScopeTransformations,
	cb *renderer.CommandBuffer) {
	now := ctx.ControlClient.SimTime
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	trid := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(trid)

	ps := sp.currentPrefs()
	font := sp.systemFont[ps.CharSize.Datablocks]
	color := ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor)

	for _, ac := range aircraft {
		state := sp.Aircraft[ac.Callsign]
		if state.LostTrack(now) {
			continue
		}

		// Format a radius/length for printing, ditching the ".0" if it's
		// an integer value.
		format := func(v float32) string {
			if v == float32(int(v)) {
				return strconv.Itoa(int(v))
			} else {
				return fmt.Sprintf("%.1f", v)
			}
		}

		if state.JRingRadius > 0 {
			const nsegs = 360
			pc := transforms.WindowFromLatLongP(state.TrackPosition())
			radius := state.JRingRadius / transforms.PixelDistanceNM(ctx.ControlClient.NmPerLongitude)
			ld.AddCircle(pc, radius, nsegs, color)

			if ps.DisplayTPASize || (state.DisplayTPASize != nil && *state.DisplayTPASize) {
				// draw the ring size around 7.5 o'clock
				// vector from center to the circle there
				v := [2]float32{-.707106 * radius, -.707106 * radius} // -sqrt(2)/2
				// move up to make space for the text
				v[1] += float32(font.Size) + 3
				pt := math.Add2f(pc, v)
				textStyle := renderer.TextStyle{Font: font, Color: color}
				td.AddText(format(state.JRingRadius), pt, textStyle)
			}
		}
		atpaStatus := state.ATPAStatus // this may change

		// If warning/alert cones are inhibited but monitor cones are not,
		// we may still draw a monitor cone.
		if (atpaStatus == ATPAStatusWarning || atpaStatus == ATPAStatusAlert) &&
			(!ps.DisplayATPAWarningAlertCones || (state.DisplayATPAWarnAlert != nil && !*state.DisplayATPAWarnAlert)) {
			atpaStatus = ATPAStatusMonitor
		}

		drawATPAMonitor := atpaStatus == ATPAStatusMonitor && ps.DisplayATPAMonitorCones &&
			(state.DisplayATPAMonitor == nil || *state.DisplayATPAMonitor) &&
			state.IntrailDistance-state.MinimumMIT <= 2 // monitor only if within 2nm of MIT requirement
		drawATPAWarning := atpaStatus == ATPAStatusWarning && ps.DisplayATPAWarningAlertCones &&
			(state.DisplayATPAWarnAlert == nil || *state.DisplayATPAWarnAlert)
		drawATPAAlert := atpaStatus == ATPAStatusAlert && ps.DisplayATPAWarningAlertCones &&
			(state.DisplayATPAWarnAlert == nil || *state.DisplayATPAWarnAlert)
		drawATPACone := drawATPAMonitor || drawATPAWarning || drawATPAAlert

		if state.HaveHeading() && (state.ConeLength > 0 || drawATPACone) {
			// Find the length of the cone in pixel coordinates)
			lengthNM := math.Max(state.ConeLength, state.MinimumMIT)
			length := lengthNM / transforms.PixelDistanceNM(ctx.ControlClient.NmPerLongitude)

			// Form a triangle; the end of the cone is 10 pixels wide
			pts := [3][2]float32{{0, 0}, {-5, length}, {5, length}}

			// Now we'll rotate the vertices so that it points in the
			// appropriate direction.
			var coneHeading float32
			if drawATPACone {
				// The cone is oriented to point toward the leading aircraft.
				if sfront, ok := sp.Aircraft[state.ATPALeadAircraftCallsign]; ok {
					coneHeading = math.Heading2LL(state.TrackPosition(), sfront.TrackPosition(),
						ac.NmPerLongitude(), ac.MagneticVariation())

				}
			} else {
				// The cone is oriented along the aircraft's heading.
				coneHeading = state.TrackHeading(ac.NmPerLongitude()) + ac.MagneticVariation()
			}
			rot := math.Rotator2f(coneHeading)
			for i := range pts {
				pts[i] = rot(pts[i])
			}

			coneColor := ps.Brightness.Lines.ScaleRGB(STARSJRingConeColor)
			if atpaStatus == ATPAStatusWarning {
				coneColor = ps.Brightness.Lines.ScaleRGB(STARSATPAWarningColor)
			} else if atpaStatus == ATPAStatusAlert {
				coneColor = ps.Brightness.Lines.ScaleRGB(STARSATPAAlertColor)
			}

			// We've got what we need to draw a polyline with the
			// aircraft's position as an anchor.
			pw := transforms.WindowFromLatLongP(state.TrackPosition())
			for i := range pts {
				pts[i] = math.Add2f(pts[i], pw)
			}
			ld.AddLineLoop(coneColor, pts[:])

			if ps.DisplayTPASize || (state.DisplayTPASize != nil && *state.DisplayTPASize) {
				textStyle := renderer.TextStyle{Font: font, Color: coneColor}

				pCenter := math.Add2f(pw, rot(math.Scale2f([2]float32{0, 0.5}, length)))

				// Draw a quad in the background color behind the text
				text := format(lengthNM)
				bx, by := textStyle.Font.BoundText(" "+text+" ", 0)
				fbx, fby := float32(bx), float32(by+2)
				trid.AddQuad(math.Add2f(pCenter, [2]float32{-fbx / 2, -fby / 2}),
					math.Add2f(pCenter, [2]float32{fbx / 2, -fby / 2}),
					math.Add2f(pCenter, [2]float32{fbx / 2, fby / 2}),
					math.Add2f(pCenter, [2]float32{-fbx / 2, fby / 2}))

				td.AddTextCentered(text, pCenter, textStyle)
			}
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	cb.SetRGB(ps.Brightness.BackgroundContrast.ScaleRGB(STARSBackgroundColor))
	trid.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (sp *STARSPane) drawSelectedRoute(ctx *panes.Context, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	if sp.drawRouteAircraft == "" {
		return
	}
	ac, ok := ctx.ControlClient.Aircraft[sp.drawRouteAircraft]
	if !ok {
		sp.drawRouteAircraft = ""
		return
	}

	ld := renderer.GetLinesDrawBuilder()
	defer renderer.ReturnLinesDrawBuilder(ld)

	prev := ac.Position()
	for _, wp := range ac.Nav.Waypoints {
		ld.AddLine(prev, wp.Location)
		prev = wp.Location
	}

	prefs := sp.currentPrefs()
	cb.LineWidth(1, ctx.DPIScale)
	cb.SetRGB(prefs.Brightness.Lines.ScaleRGB(STARSJRingConeColor))
	transforms.LoadLatLongViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

type STARSRangeBearingLine struct {
	P [2]struct {
		// If callsign is given, use that aircraft's position;
		// otherwise we have a fixed position.
		Loc      math.Point2LL
		Callsign string
	}
}

func (rbl STARSRangeBearingLine) GetPoints(ctx *panes.Context, visibleAircraft []*av.Aircraft, sp *STARSPane) (p0, p1 math.Point2LL) {
	// Each line endpoint may be specified either by an aircraft's
	// position or by a fixed position. We'll start with the fixed
	// position and then override it if there's a valid *Aircraft.
	p0, p1 = rbl.P[0].Loc, rbl.P[1].Loc
	if ac := ctx.ControlClient.Aircraft[rbl.P[0].Callsign]; ac != nil {
		state, ok := sp.Aircraft[ac.Callsign]
		if ok && !state.LostTrack(ctx.ControlClient.SimTime) && slices.Contains(visibleAircraft, ac) {
			p0 = state.TrackPosition()
		}
	}
	if ac := ctx.ControlClient.Aircraft[rbl.P[1].Callsign]; ac != nil {
		state, ok := sp.Aircraft[ac.Callsign]
		if ok && !state.LostTrack(ctx.ControlClient.SimTime) && slices.Contains(visibleAircraft, ac) {
			p1 = state.TrackPosition()
		}
	}
	return
}

func rblSecondClickHandler(ctx *panes.Context, sp *STARSPane) func([2]float32, ScopeTransformations) (status CommandStatus) {
	return func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
		if sp.wipRBL == nil {
			// this shouldn't happen, but let's not crash if it does...
			return
		}

		rbl := *sp.wipRBL
		sp.wipRBL = nil
		if ac, _ := sp.tryGetClosestAircraft(ctx, pw, transforms); ac != nil {
			rbl.P[1].Callsign = ac.Callsign
		} else {
			rbl.P[1].Loc = transforms.LatLongFromWindowP(pw)
		}
		sp.RangeBearingLines = append(sp.RangeBearingLines, rbl)
		status.clear = true
		return
	}
}

func (sp *STARSPane) displaySignificantPointInfo(p0, p1 math.Point2LL, nmPerLongitude, magneticVariation float32) (status CommandStatus) {
	// Find the closest significant point to p1.
	minDist := float32(1000000)
	var closest *sim.SignificantPoint
	for _, sigpt := range sp.significantPointsSlice {
		d := math.NMDistance2LL(sigpt.Location, p1)
		if d < minDist {
			minDist = d
			closest = &sigpt
		}
	}

	sp.wipSignificantPoint = nil
	status.clear = true

	if closest == nil {
		// No significant points defined?
		return
	}

	// Display a blinking square at the point
	sp.highlightedLocation = closest.Location
	sp.highlightedLocationEndTime = time.Now().Add(5 * time.Second)

	// 6-148
	format := func(sig sim.SignificantPoint) string {
		d := math.NMDistance2LL(p0, sig.Location)
		str := ""
		if d > 1 { // no bearing range if within 1nm
			hdg := math.Heading2LL(p0, sig.Location, nmPerLongitude, magneticVariation)
			str = fmt.Sprintf("%03d/%.2f ", int(hdg), d)
			for len(str) < 9 {
				str += " "
			}
		}

		if sig.Description != "" {
			return str + strings.ToUpper(sig.Description)
		} else {
			return str + sig.Name
		}
	}

	str := format(*closest)

	// Up to 5 additional, if they are within 1nm of the selected point
	n := 0
	for _, sig := range sp.significantPointsSlice {
		if sig.Name != closest.Name && math.NMDistance2LL(sig.Location, closest.Location) < 1 {
			str += "\n" + format(sig)
			n++
			if n == 5 {
				break
			}
		}
	}

	status.output = str

	return
}

func toSignificantPointClickHandler(ctx *panes.Context, sp *STARSPane) func([2]float32, ScopeTransformations) (status CommandStatus) {
	return func(pw [2]float32, transforms ScopeTransformations) (status CommandStatus) {
		if sp.wipSignificantPoint == nil {
			status.clear = true
			return
		} else {
			p1 := transforms.LatLongFromWindowP(pw)
			return sp.displaySignificantPointInfo(*sp.wipSignificantPoint, p1,
				ctx.ControlClient.NmPerLongitude, ctx.ControlClient.MagneticVariation)
		}
	}
}
