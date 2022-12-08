// radartools.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/url"
	"time"

	"github.com/mmp/imgui-go/v4"
	"github.com/nfnt/resize"
)

///////////////////////////////////////////////////////////////////////////
// WeatherRadar

// WeatherRadar provides functionality for fetching radar images to display
// in radar scopes. Only locations in the USA are currently supported, as
// the only current data source is the US NOAA. (TODO: find more sources
// and add support for them!)
type WeatherRadar struct {
	active bool

	// Images are fetched in a separate goroutine; updated radar center
	// locations are sent from the main thread via reqChan and downloaded
	// radar images are returned via imageChan.
	reqChan   chan Point2LL
	imageChan chan ImageAndBounds

	// radarBounds records the lat-long bounding box of the most recently
	// received radar image, which has texId as its GPU texture it.
	radarBounds Extent2D
	texId       uint32

	lastDraw time.Time

	// BlendFactor controls the blending of the radar image; 0 hides it and 1
	// shows it as received from the sender (which is normally far too bright
	// and obscures other things on the scope). Values around 0.1 or 0.2 are
	// generally reasonable.
	BlendFactor float32
}

// Latitude-longitude extent of the fetched image; the requests are +/-
// this much from the current center.
const weatherLatLongExtent = 5

type ImageAndBounds struct {
	img    image.Image
	bounds Extent2D
}

// Activate must be called for the WeatherRadar to start fetching weather
// radar images; it is called with an initial center position in
// latitude-longitude coordinates.
func (w *WeatherRadar) Activate(center Point2LL) {
	if w.active {
		lg.Errorf("Called Activate on already-active WeatherRadar")
		return
	}
	w.active = true

	w.reqChan = make(chan Point2LL, 1000) // lots of buffering
	w.reqChan <- center
	w.imageChan = make(chan ImageAndBounds) // unbuffered channel

	// NOAA posts new maps every 2 minutes, so fetch a new map at minimum
	// every 100s to stay current.
	go fetchWeather(w.reqChan, w.imageChan, 100*time.Second)
}

// Deactivate causes the WeatherRadar to stop fetching weather updates;
// it is important that this method be called when a radar scope is
// deactivated so that we don't continue to consume bandwidth fetching
// unneeded weather images.
func (w *WeatherRadar) Deactivate() {
	close(w.reqChan)
	w.active = false
}

// UpdateCenter provides a new center point for the radar image, causing a
// new image to be fetched.
func (w *WeatherRadar) UpdateCenter(center Point2LL) {
	select {
	case w.reqChan <- center:
		// success
	default:
		// The channel is full; this may happen if the user is continuously
		// dragging the radar scope around. Worst case, we drop some
		// position update requests, which is generally no big deal.
	}
}

func (w *WeatherRadar) DrawUI() {
	imgui.SliderFloatV("Weather radar blending factor", &w.BlendFactor, 0, 1, "%.2f", 0)
}

// fetchWeather runs asynchronously in a goroutine, receiving requests from
// reqChan, fetching corresponding radar images from the NOAA, and sending
// the results back on imageChan.  New images are also automatically
// fetched periodically, with a wait time specified by the delay parameter.
func fetchWeather(reqChan chan Point2LL, imageChan chan ImageAndBounds, delay time.Duration) {
	// center stores the current center position of the radar image
	var center Point2LL
	for {
		var ok, timedOut bool
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
				close(imageChan)
				return
			}
		case <-time.After(delay):
			// Periodically make a new request even if the center hasn't
			// changed.
			timedOut = true
		}

		// Lat-long bounds of the region we're going to request weater for.
		rb := Extent2D{p0: sub2ll(center, Point2LL{weatherLatLongExtent, weatherLatLongExtent}),
			p1: add2ll(center, Point2LL{weatherLatLongExtent, weatherLatLongExtent})}

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
		params.Add("WIDTH", "1024")
		params.Add("HEIGHT", "1024")
		params.Add("LAYERS", "conus_bref_qcd")
		params.Add("BBOX", fmt.Sprintf("%f,%f,%f,%f", rb.p0[0], rb.p0[1], rb.p1[0], rb.p1[1]))

		url := "https://opengeo.ncep.noaa.gov/geoserver/conus/conus_bref_qcd/ows?" + params.Encode()
		lg.Printf("Fetching weather: %s", url)

		// Request the image
		resp, err := http.Get(url)
		if err != nil {
			lg.Printf("Weather error: %s", err)
			continue
		}
		defer resp.Body.Close()

		img, err := png.Decode(resp.Body)
		if err != nil {
			lg.Printf("Weather error: %s", err)
			continue
		}

		// Convert the Image returned by png.Decode to an RGBA image so
		// that we can patch up some of the pixel values.
		rgba := image.NewRGBA(img.Bounds())
		draw.Draw(rgba, img.Bounds(), img, image.Point{}, draw.Over)
		ny, nx := img.Bounds().Dy(), img.Bounds().Dx()
		for y := 0; y < ny; y++ {
			for x := 0; x < nx; x++ {
				r, g, b, a := img.At(x, y).RGBA()
				// Convert all-white to black and an alpha channel of zero, so
				// that where there's no weather, nothing is drawn.
				if r == 0xffff && g == 0xffff && b == 0xffff && a == 0xffff {
					rgba.Set(x, y, color.RGBA{})
				}
			}
		}

		// The image we get back is relatively low resolution (and doesn't
		// even have 1024x1024 pixels of actual detail); use a decent
		// filter to upsample it, which looks better than relying on GPU
		// bilinear interpolation...
		resized := resize.Resize(2048, 2048, rgba, resize.MitchellNetravali)

		// Send it back to the main thread.
		imageChan <- ImageAndBounds{img: resized, bounds: rb}
		lg.Printf("finish weather fetch")

		if !timedOut {
			time.Sleep(15 * time.Second)
		}
	}
}

// Draw draws the current weather radar image, if available. (If none is yet
// available, it returns rather than stalling waiting for it). The provided
// CommandBuffer should be set up with viewing matrices such that vertex
// coordinates are provided in latitude-longitude.
func (w *WeatherRadar) Draw(cb *CommandBuffer) {
	// Try to receive an updated image from the fetchWather goroutine, if
	// one is available.
	select {
	case ib, ok := <-w.imageChan:
		if ok {
			w.radarBounds = ib.bounds
			if w.texId == 0 {
				w.texId = renderer.CreateTextureFromImage(ib.img, false)
			} else {
				renderer.UpdateTextureFromImage(w.texId, ib.img, false)
			}
		}
	default:
		// no message
	}

	// Note that we always go ahead and drain the imageChan, even if if the
	// WeatherRadar is inactive. This way the chan is ready for the
	// future...
	if !w.active {
		return
	}

	if w.texId == 0 {
		// Presumably we haven't yet gotten a response to the initial
		// request...
		return
	}

	// We have a valid radar image, so draw it.
	cb.SetRGBA(RGBA{1, 1, 1, w.BlendFactor})
	cb.Blend()
	cb.EnableTexture(w.texId)

	// Draw the lat-long space quad corresponding to the region that we
	// have weather for; just stuff the vertex and index buffers into the
	// CommandBuffer directly rather than bothering with a
	// TrianglesDrawable or the like.
	rb := w.radarBounds
	p := [4][2]float32{[2]float32{rb.p0[0], rb.p0[1]}, [2]float32{rb.p1[0], rb.p0[1]},
		[2]float32{rb.p1[0], rb.p1[1]}, [2]float32{rb.p0[0], rb.p1[1]}}
	pidx := cb.Float2Buffer(p[:])
	cb.VertexArray(pidx, 2, 2*4)

	uv := [4][2]float32{[2]float32{0, 1}, [2]float32{1, 1}, [2]float32{1, 0}, [2]float32{0, 0}}
	uvidx := cb.Float2Buffer(uv[:])
	cb.TexCoordArray(uvidx, 2, 2*4)

	indidx := cb.IntBuffer([]int32{0, 1, 2, 3})
	cb.DrawQuads(indidx, 4)

	cb.DisableTexture()
	cb.DisableBlend()
}
