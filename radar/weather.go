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
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

// Weather

// WeatherRadar provides functionality for fetching precipitation data
// from the server and displaying it in radar scopes.
type WeatherRadar struct {
	facility        string
	nextFetchTime   sim.Time
	fetchInProgress bool
	precipCh        chan *wx.Precip
	errCh           chan error
	cb              [numWxHistory][NumWxLevels]*renderer.CommandBuffer
	latestPrecip    *wx.Precip // raw, for non-STARS consumers
	generation      int        // bumps each time latestPrecip changes
	mu              util.LoggingMutex
}

// LatestPrecip advances the fetch state machine and returns the most
// recent decoded precipitation blob (may be nil) along with a generation
// counter that bumps each time the blob changes. Callers that build their
// own command buffers should rebuild only when the counter changes.
func (w *WeatherRadar) LatestPrecip(ctx *panes.Context) (*wx.Precip, int) {
	w.mu.Lock(ctx.Lg)
	defer w.mu.Unlock(ctx.Lg)
	w.tick(ctx)
	return w.latestPrecip, w.generation
}

// tick performs the per-frame fetch maintenance shared by Draw and
// LatestPrecip. Caller must hold w.mu.
func (w *WeatherRadar) tick(ctx *panes.Context) {
	select {
	case err := <-w.errCh:
		ctx.Lg.Warnf("%v", err)
		w.fetchInProgress = false
	case precip := <-w.precipCh:
		w.installPrecip(precip)
		w.fetchInProgress = false
	default:
	}

	if w.facility != ctx.Client.State.Facility {
		w.facility = ctx.Client.State.Facility
		w.fetchInProgress = false
		w.nextFetchTime = sim.Time{}
		w.cb = [numWxHistory][NumWxLevels]*renderer.CommandBuffer{}
		w.latestPrecip = nil
	}

	if ctx.InterpolatedSimTime.After(w.nextFetchTime) && !w.fetchInProgress {
		w.fetchPrecipitation(ctx)
	}
}

// installPrecip stores a new precip blob, regenerates STARS CBs, and bumps
// the generation counter. Caller must hold w.mu.
func (w *WeatherRadar) installPrecip(precip *wx.Precip) {
	w.cb[2], w.cb[1] = w.cb[1], w.cb[0]
	w.cb[0] = makeWeatherCommandBuffers(precip)
	w.latestPrecip = precip
	w.generation++
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

	fetchTime := ctx.InterpolatedSimTime
	ctx.Client.GetPrecipURL(fetchTime, func(url string, nextTime sim.Time, err error) {
		w.mu.Lock(ctx.Lg)
		defer w.mu.Unlock(ctx.Lg)

		if err != nil {
			w.nextFetchTime = fetchTime.Add(time.Minute)
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

// WxScheme captures how a scope bins NEXRAD dBZ samples into discrete
// display levels. Scopes (STARS, ERAM, ...) supply their own scheme and
// use MakeCommandBuffers to turn raw precip data into per-level command
// buffers.
type WxScheme struct {
	// Thresholds is an ascending list of dBZ values; a sample whose dBZ
	// exceeds Thresholds[i] but not Thresholds[i+1] maps to level i+1.
	Thresholds []byte
}

func (s WxScheme) NumLevels() int { return len(s.Thresholds) }

func (s WxScheme) LevelForDBZ(dbz byte) int {
	for i := len(s.Thresholds) - 1; i >= 0; i-- {
		if dbz > s.Thresholds[i] {
			return i + 1
		}
	}
	return 0
}

// MakeCommandBuffers walks the precip grid once per level, merging
// horizontal runs of same-level cells into single quads, and returns one
// CommandBuffer per level (nil entries for empty levels).
func (s WxScheme) MakeCommandBuffers(precip *wx.Precip) []*renderer.CommandBuffer {
	nx, ny := precip.Resolution, precip.Resolution
	bounds := precip.BoundsLL()

	tb := renderer.GetTrianglesDrawBuilder()
	defer renderer.ReturnTrianglesDrawBuilder(tb)

	cellLevel := func(x, y int) int {
		idx := x + y*nx
		if idx >= len(precip.DBZ) {
			return 0
		}
		return s.LevelForDBZ(precip.DBZ[idx])
	}

	out := make([]*renderer.CommandBuffer, s.NumLevels())
	for level := 1; level <= s.NumLevels(); level++ {
		tb.Reset()
		any := false
		for y := range ny {
			for x := 0; x < nx; {
				if cellLevel(x, y) != level {
					x++
					continue
				}
				any = true
				x0 := x
				for x < nx && cellLevel(x, y) == level {
					x++
				}
				p0 := bounds.Lerp([2]float32{float32(x0) / float32(nx), float32(ny-1-y) / float32(ny)})
				p1 := bounds.Lerp([2]float32{float32(x) / float32(nx), float32(ny-y) / float32(ny)})
				tb.AddQuad([2]float32{p0[0], p0[1]}, [2]float32{p1[0], p0[1]},
					[2]float32{p1[0], p1[1]}, [2]float32{p0[0], p1[1]})
			}
		}
		if any {
			out[level-1] = &renderer.CommandBuffer{}
			tb.GenerateCommands(out[level-1])
		}
	}
	return out
}

var starsWxScheme = WxScheme{Thresholds: []byte{20, 30, 40, 45, 50, 55}}

func makeWeatherCommandBuffers(precip *wx.Precip) [NumWxLevels]*renderer.CommandBuffer {
	cbs := starsWxScheme.MakeCommandBuffers(precip)
	var out [NumWxLevels]*renderer.CommandBuffer
	copy(out[:], cbs)
	return out
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
func (w *WeatherRadar) Draw(ctx *panes.Context, hist int, intensity float32,
	wxColors [NumWxLevels]renderer.RGB, wxStippleColor renderer.RGB, wxLevelStipple [NumWxLevels]int,
	active [NumWxLevels]bool, transforms ScopeTransformations, cb *renderer.CommandBuffer) {
	w.mu.Lock(ctx.Lg)
	defer w.mu.Unlock(ctx.Lg)

	w.tick(ctx)

	hist = math.Clamp(hist, 0, len(w.cb)-1)
	transforms.LoadLatLongViewingMatrices(cb)
	for i := range w.cb[hist] {
		if active[i] && w.cb[hist][i] != nil {
			cb.SetRGB(wxColors[i].Scale(intensity))
			cb.Call(*w.cb[hist][i])

			if wxLevelStipple[i] > 0 {
				cb.EnablePolygonStipple()
				if wxLevelStipple[i] == 1 {
					cb.PolygonStipple(reverseStippleBytes(wxStippleLight))
				} else {
					cb.PolygonStipple(reverseStippleBytes(wxStippleDense))
				}
				cb.SetRGB(wxStippleColor)
				cb.Call(*w.cb[hist][i])
				cb.DisablePolygonStipple()
			}
		}
	}
}
