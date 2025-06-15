package radar

import (
	_ "embed"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"
)

// Weather

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
	cbChan  chan [NumWxLevels]*renderer.CommandBuffer
	cb      [numWxHistory][NumWxLevels]*renderer.CommandBuffer
}

// WXHistory, Levels, and Fetch distance should eventually be ommited as they're depenent on the scope used.
const numWxHistory = 3

const NumWxLevels = 6

const wxFetchResolution = 512

// Fetch this many nm out from the center; should evenly divide wxFetchResolution
const wxFetchDistance = 128

// Pixels in the image that correspond to a WX block on the scope; we fetch
// +/- wxFetchDistance an d blocks are 0.5nm so here we go.
const wxBlockRes = wxFetchResolution / (2 * wxFetchDistance) * 0.5

// fetchWeather runs asynchronously in a goroutine, receiving requests from
// reqChan, fetching corresponding radar images from the NOAA, and sending
// the results back on cbChan.  New images are also automatically
// fetched periodically, with a wait time specified by the delay parameter.
func fetchWeather(reqChan chan math.Point2LL, cbChan chan [NumWxLevels]*renderer.CommandBuffer, lg *log.Logger) {
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

		// Figure out how far out in degrees latitude / longitude to fetch.
		// Latitude is easy: 60nm per degree
		dlat := float32(wxFetchDistance) / 60
		// Longitude: figure out nm per degree at center
		nmPerLong := 60 * math.Cos(math.Radians(center[1]))
		dlong := wxFetchDistance / nmPerLong

		// Lat-long bounds of the region we're going to request weather for.
		rb := math.Extent2D{P0: math.Sub2LL(center, math.Point2LL{dlong, dlat}),
			P1: math.Add2LL(center, math.Point2LL{dlong, dlat})}

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
		params.Add("WIDTH", fmt.Sprintf("%d", wxFetchResolution))
		params.Add("HEIGHT", fmt.Sprintf("%d", wxFetchResolution))
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
	w.cbChan = make(chan [NumWxLevels]*renderer.CommandBuffer, 8)

	go fetchWeather(w.reqChan, w.cbChan, lg)
}

func (w *WeatherRadar) HaveWeather() [NumWxLevels]bool {
	var r [NumWxLevels]bool
	for i := range NumWxLevels {
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

func makeWeatherCommandBuffers(img image.Image, rb math.Extent2D) [NumWxLevels]*renderer.CommandBuffer {
	// Convert the Image returned by png.Decode to a simple 8-bit RGBA image.
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, img.Bounds(), img, image.Point{}, draw.Over)

	ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
	nby, nbx := ny/wxBlockRes, nx/wxBlockRes

	// First determine the average dBZ for each wxBlockRes*wxBlockRes block
	// of the image.
	levels := make([]int, nbx*nby)
	for y := 0; y < nby; y++ {
		for x := 0; x < nbx; x++ {
			dbz := float32(0)
			for dy := 0; dy < wxBlockRes; dy++ {
				for dx := 0; dx < wxBlockRes; dx++ {
					px := rgba.RGBAAt(x*wxBlockRes+dx, y*wxBlockRes+dy)
					dbz += estimateDBZ([3]byte{px.R, px.G, px.B})
				}
			}

			dbz /= wxBlockRes * wxBlockRes

			// Map the dBZ value to a STARS WX level.
			level := 0
			if dbz > 55 {
				level = 6
			} else if dbz > 50 {
				level = 5
			} else if dbz > 45 {
				level = 4
			} else if dbz > 40 {
				level = 3
			} else if dbz > 30 {
				level = 2
			} else if dbz > 20 {
				level = 1
			}

			levels[x+y*nbx] = level
		}
	}

	// Now generate the command buffer for each weather level.  We don't
	// draw anything for level==0, so the indexing into cb is off by 1
	// below.
	var cb [NumWxLevels]*renderer.CommandBuffer
	tb := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(tb)

	for level := 1; level <= NumWxLevels; level++ {
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
				p0 := rb.Lerp([2]float32{float32(x0) / float32(nbx), float32(nby-1-y) / float32(nby)})
				p1 := rb.Lerp([2]float32{float32(x) / float32(nbx), float32(nby-y) / float32(nby)})

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

// A single scanline of this color map, converted to RGB bytes:
// https://opengeo.ncep.noaa.gov/geoserver/styles/reflectivity.png
//
//go:embed radar_reflectivity.rgb
var radarReflectivity []byte

type kdNode struct {
	rgb [3]byte
	dbz float32
	c   [2]*kdNode
}

var radarReflectivityKdTree *kdNode

func init() {
	type rgbRefl struct {
		rgb [3]byte
		dbz float32
	}

	var r []rgbRefl

	for i := 0; i < len(radarReflectivity); i += 3 {
		r = append(r, rgbRefl{
			rgb: [3]byte{radarReflectivity[i], radarReflectivity[i+1], radarReflectivity[i+2]},
			// Approximate range of the reflectivity color ramp
			dbz: math.Lerp(float32(i)/float32(len(radarReflectivity)), -25, 73),
		})
	}

	// Build a kd-tree over the RGB points in the color map.
	var buildTree func(r []rgbRefl, depth int) *kdNode
	buildTree = func(r []rgbRefl, depth int) *kdNode {
		if len(r) == 0 {
			return nil
		}
		if len(r) == 1 {
			return &kdNode{rgb: r[0].rgb, dbz: r[0].dbz}
		}

		// The split dimension cycles through RGB with tree depth.
		dim := depth % 3

		// Sort the points in the current dimension (we actually just need
		// to partition around the midpoint, but...)
		sort.Slice(r, func(i, j int) bool {
			return r[i].rgb[dim] < r[j].rgb[dim]
		})

		// Split in the middle and recurse
		mid := len(r) / 2
		return &kdNode{
			rgb: r[mid].rgb,
			dbz: r[mid].dbz,
			c:   [2]*kdNode{buildTree(r[:mid], depth+1), buildTree(r[mid+1:], depth+1)},
		}
	}

	radarReflectivityKdTree = buildTree(r, 0)
}

// Draw draws the current weather radar image, if available. (If none is yet
// available, it returns rather than stalling waiting for it).
func (w *WeatherRadar) Draw(ctx *panes.Context, hist int, intensity float32, contrast float32,
	active [NumWxLevels]bool, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
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
				cb.PolygonStipple(ReverseStippleBytes(wxStippleLight))
			} else if i == 2 || i == 5 {
				cb.PolygonStipple(ReverseStippleBytes(wxStippleDense))
			}
			// Draw the same quads again, just with a different color and stippled.
			cb.SetRGB(renderer.RGB{contrast, contrast, contrast})
			cb.Call(*w.cb[hist][i])
			cb.DisablePolygonStipple()
		}
	}
}

// Returns estimated dBZ (https://en.wikipedia.org/wiki/DBZ_(meteorology)) for
// an RGB by going backwards from the color ramp.
func estimateDBZ(rgb [3]byte) float32 {
	// All white -> ~nil
	if rgb[0] == 255 && rgb[1] == 255 && rgb[2] == 255 {
		return -100
	}

	// Returns the distnace between the specified RGB and the RGB passed to
	// estimateDBZ.
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
		return n.dbz
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

		return n.dbz
	}
}