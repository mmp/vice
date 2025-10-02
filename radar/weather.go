package radar

import (
	"fmt"
	"math/bits"
	"net/http"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// Weather

// WeatherRadar provides functionality for fetching precipitation data
// from the server and displaying it in radar scopes.
type WeatherRadar struct {
	tracon          string
	nextFetchTime   time.Time
	fetchInProgress bool
	precipCh        chan *wx.Precip
	errCh           chan error
	cb              [numWxHistory][NumWxLevels]*renderer.CommandBuffer
	mu              util.LoggingMutex
}

// WXHistory and Levels should eventually be omitted as they're dependent on the scope used.
const numWxHistory = 3

const NumWxLevels = 6

// fetchPrecipitation fetches precipitation data from GCS via RPC
func (w *WeatherRadar) fetchPrecipitation(ctx *panes.Context) {
	w.fetchInProgress = true
	if w.precipCh == nil {
		w.precipCh = make(chan *wx.Precip)
	}
	if w.errCh == nil {
		w.errCh = make(chan error)
	}

	ctx.Client.GetPrecipURL(ctx.Client.State.SimTime, func(url string, nextTime time.Time, err error) {
		w.mu.Lock(ctx.Lg)
		defer w.mu.Unlock(ctx.Lg)

		if err != nil {
			w.nextFetchTime = time.Now().Add(time.Minute)
			ctx.Lg.Infof("Failed to get precip URL: %v", err)
			w.fetchInProgress = false
			return
		}

		w.nextFetchTime = nextTime

		go w.fetchPrecip(url, ctx.Lg)
	})
}

func (w *WeatherRadar) fetchPrecip(url string, lg *log.Logger) {
	resp, err := http.Get(url)
	if err != nil {
		w.errCh <- err
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.errCh <- fmt.Errorf("HTTP error: %d", resp.StatusCode)
		return
	}

	precip, err := wx.DecodePrecip(resp.Body)
	if err != nil {
		w.errCh <- err
	} else {
		w.precipCh <- precip
	}
}

func (w *WeatherRadar) HaveWeather() [NumWxLevels]bool {
	var r [NumWxLevels]bool
	for i := range NumWxLevels {
		r[i] = w.cb[0][i] != nil
	}
	return r
}

func dbzToLevel(dbz byte) int {
	// Map the dBZ value to a STARS WX level.
	if dbz > 55 {
		return 6
	} else if dbz > 50 {
		return 5
	} else if dbz > 45 {
		return 4
	} else if dbz > 40 {
		return 3
	} else if dbz > 30 {
		return 2
	} else if dbz > 20 {
		return 1
	}
	return 0
}

func makeWeatherCommandBuffers(precip *wx.Precip) [NumWxLevels]*renderer.CommandBuffer {
	nx, ny := precip.Resolution, precip.Resolution
	bounds := precip.BoundsLL()

	// Now generate the command buffer for each weather level.
	var cb [NumWxLevels]*renderer.CommandBuffer
	tb := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(tb)

	for level := 1; level <= NumWxLevels; level++ {
		tb.Reset()
		levelHasWeather := false

		// Process each row of the precipitation grid
		for y := 0; y < ny; y++ {
			for x := 0; x < nx; x++ {
				idx := x + y*nx
				if idx >= len(precip.DBZ) {
					continue
				}

				dbzLevel := dbzToLevel(precip.DBZ[idx])
				if dbzLevel != level {
					continue
				}
				levelHasWeather = true

				// Find the span of consecutive pixels at this level
				x0 := x
				for x < nx && x+y*nx < len(precip.DBZ) && dbzToLevel(precip.DBZ[x+y*nx]) == level {
					x++
				}

				// Convert grid coordinates to lat/long positions
				p0 := bounds.Lerp([2]float32{float32(x0) / float32(nx), float32(ny-1-y) / float32(ny)})
				p1 := bounds.Lerp([2]float32{float32(x) / float32(nx), float32(ny-y) / float32(ny)})

				// Draw a quad for this span
				tb.AddQuad([2]float32{p0[0], p0[1]}, [2]float32{p1[0], p0[1]},
					[2]float32{p1[0], p1[1]}, [2]float32{p0[0], p1[1]})
			}
		}

		// Store command buffer for this level (level 1 goes to index 0)
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

// Draw draws the current weather radar data, if available.
func (w *WeatherRadar) Draw(ctx *panes.Context, hist int, intensity float32, contrast float32,
	active [NumWxLevels]bool, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	w.mu.Lock(ctx.Lg)
	defer w.mu.Unlock(ctx.Lg)

	select {
	case err := <-w.errCh:
		ctx.Lg.Warnf("%v", err)
		w.fetchInProgress = false
	case precip := <-w.precipCh:
		// Shift history down before storing the latest
		w.cb[2], w.cb[1] = w.cb[1], w.cb[0]
		w.cb[0] = makeWeatherCommandBuffers(precip)

		w.fetchInProgress = false
	default:
	}

	traconChanged := w.tracon != ctx.Client.State.TRACON
	if traconChanged {
		w.tracon = ctx.Client.State.TRACON
		w.fetchInProgress = false
		w.nextFetchTime = time.Time{}
		w.cb = [numWxHistory][NumWxLevels]*renderer.CommandBuffer{}
	}
	shouldFetch := ctx.Client.State.SimTime.After(w.nextFetchTime) && !w.fetchInProgress

	if shouldFetch {
		w.fetchPrecipitation(ctx)
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
