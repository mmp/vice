package radar

import (
	_ "embed"
	"image"
	"image/draw"
	"math/bits"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
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

// Fetch this many nm out from the center
const wxFetchDistance = 128

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

		fetchResolution := 4 * wxFetchDistance // 2x for radius->diameter, 2x to get 0.5nm blocks
		img, bounds, err := wx.FetchRadarImage(center, wxFetchDistance, fetchResolution)
		if err == nil {
			cbChan <- makeWeatherCommandBuffers(img, bounds)
			lg.Info("finish weather fetch")
		} else {
			lg.Infof("%v", err)
		}
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

func dbzToLevel(dbzs []byte) []int {
	levels := make([]int, len(dbzs))
	for i, dbz := range dbzs {
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

		levels[i] = level
	}
	return levels
}

func makeWeatherCommandBuffers(img image.Image, rb math.Extent2D) [NumWxLevels]*renderer.CommandBuffer {
	// Convert the Image returned by png.Decode to a simple 8-bit RGBA image.
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, img.Bounds(), img, image.Point{}, draw.Over)

	dbz := wx.RadarImageToDBZ(img)
	levels := dbzToLevel(dbz)

	// Now generate the command buffer for each weather level.  We don't
	// draw anything for level==0, so the indexing into cb is off by 1
	// below.
	var cb [NumWxLevels]*renderer.CommandBuffer
	tb := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(tb)

	ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
	for level := 1; level <= NumWxLevels; level++ {
		tb.Reset()
		levelHasWeather := false

		// We'd like to be somewhat efficient and not necessarily draw an
		// individual quad for each block, but on the other hand don't want
		// to make this too complicated... So we'll consider block
		// scanlines and quads across neighbors that are the same level
		// when we find them.
		for y := 0; y < ny; y++ {
			for x := 0; x < nx; x++ {
				// Skip ahead until we reach a block at the level we currently care about.
				if levels[x+y*nx] != level {
					continue
				}
				levelHasWeather = true

				// Now see how long a span of repeats we have.
				// Each quad spans [0,0]->[1,1] in texture coordinates; the
				// texture is created with repeat wrap mode, so we just pad
				// out the u coordinate into u1 accordingly.
				x0 := x
				u1 := float32(0)
				for x < nx && levels[x+y*nx] == level {
					x++
					u1++
				}

				// Corner points
				p0 := rb.Lerp([2]float32{float32(x0) / float32(nx), float32(ny-1-y) / float32(ny)})
				p1 := rb.Lerp([2]float32{float32(x) / float32(nx), float32(ny-y) / float32(ny)})

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
