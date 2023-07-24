// radartools.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"net/url"
	"time"

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
	if !w.active {
		return
	}
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
		lg.Infof("Fetching weather: %s", url)

		// Request the image
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
		lg.Infof("finish weather fetch")

		if !timedOut {
			time.Sleep(15 * time.Second)
		}
	}
}

// Draw draws the current weather radar image, if available. (If none is yet
// available, it returns rather than stalling waiting for it). The provided
// CommandBuffer should be set up with viewing matrices such that vertex
// coordinates are provided in latitude-longitude.
func (w *WeatherRadar) Draw(ctx *PaneContext, intensity float32, transforms ScopeTransformations, cb *CommandBuffer) {
	// Try to receive an updated image from the fetchWather goroutine, if
	// one is available.
	select {
	case ib, ok := <-w.imageChan:
		if ok {
			w.radarBounds = ib.bounds
			if w.texId == 0 {
				w.texId = ctx.renderer.CreateTextureFromImage(ib.img)
			} else {
				ctx.renderer.UpdateTextureFromImage(w.texId, ib.img)
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
	transforms.LoadLatLongViewingMatrices(cb)
	cb.SetRGBA(RGBA{1, 1, 1, intensity})
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

///////////////////////////////////////////////////////////////////////////
// CRDA

type CRDAConfig struct {
	Airport                  string
	PrimaryRunway            string
	SecondaryRunway          string
	Mode                     int
	TieStaggerDistance       float32
	ShowGhostsOnPrimary      bool
	HeadingTolerance         float32
	GlideslopeLateralSpread  float32
	GlideslopeVerticalSpread float32
	GlideslopeAngle          float32
	ShowCRDARegions          bool
}

const (
	CRDAModeStagger = iota
	CRDAModeTie
)

func NewCRDAConfig() CRDAConfig {
	return CRDAConfig{
		Mode:                     CRDAModeStagger,
		TieStaggerDistance:       3,
		HeadingTolerance:         110,
		GlideslopeLateralSpread:  10,
		GlideslopeVerticalSpread: 10,
		GlideslopeAngle:          3}

}

func (c *CRDAConfig) getRunway(n string) *Runway {
	panic("FIXME")
	/*
			for _, rwy := range database.runways[c.Airport] {
				if rwy.Number == n {
					return &rwy
				}
			}
		return nil
	*/
}

func (c *CRDAConfig) getRunways() (ghostSource *Runway, ghostDestination *Runway) {
	/*
		for i, rwy := range database.runways[c.Airport] {
			if rwy.Number == c.PrimaryRunway {
				ghostSource = &database.runways[c.Airport][i]
			}
			if rwy.Number == c.SecondaryRunway {
				ghostDestination = &database.runways[c.Airport][i]
			}
		}

		if c.ShowGhostsOnPrimary {
			ghostSource, ghostDestination = ghostDestination, ghostSource
		}
	*/

	return
}

func runwayIntersection(w *World, a *Runway, b *Runway) (Point2LL, bool) {
	p1, p2 := ll2nm(a.Threshold, w.NmPerLongitude), ll2nm(a.End, w.NmPerLongitude)
	p3, p4 := ll2nm(b.Threshold, w.NmPerLongitude), ll2nm(b.End, w.NmPerLongitude)
	p, ok := LineLineIntersect(p1, p2, p3, p4)

	centroid := mid2f(mid2f(p1, p2), mid2f(p3, p4))
	d := distance2f(centroid, p)
	if d > 30 {
		// more like parallel; we don't care about super far away intersections...
		ok = false
	}

	return nm2ll(p, w.NmPerLongitude), ok
}

func (c *CRDAConfig) GetGhost(ac *Aircraft) *Aircraft {
	/*
		src, dst := c.getRunways()
		if src == nil || dst == nil {
			return nil
		}

		pIntersect, ok := runwayIntersection(src, dst)
		if !ok {
			lg.Infof("No intersection between runways??!?")
			return nil
		}

			airport, ok := database.FAA.airports[c.Airport]
			if !ok {
				lg.Infof("%s: airport unknown?!", c.Airport)
				return nil
			}

			if ac.TrackGroundspeed() > 350 {
				return nil
			}

			if headingDifference(ac.TrackHeading(), src.Heading) > c.HeadingTolerance {
				return nil
			}

			// Is it on the glideslope?
			// Laterally: compute the heading to the threshold and compare to the
			// glideslope's lateral spread.
			h := headingp2ll(ac.TrackPosition(), src.Threshold, MagneticVariation)
			if headingDifference(h, src.Heading) > c.GlideslopeLateralSpread {
				return nil
			}

			// Vertically: figure out the range of altitudes at the distance out.
			// First figure out the aircraft's height AGL.
			agl := ac.TrackAltitude() - airport.Elevation

			// Find the glideslope height at the aircraft's distance to the
			// threshold.
			// tan(glideslope angle) = height / threshold distance
			const nmToFeet = 6076.12
			thresholdDistance := nmToFeet * nmdistance2ll(ac.TrackPosition(), src.Threshold)
			height := thresholdDistance * tan(radians(c.GlideslopeAngle))
			// Assume 100 feet at the threshold
			height += 100

			// Similarly, find the allowed altitude difference
			delta := thresholdDistance * tan(radians(c.GlideslopeVerticalSpread))

			if abs(float32(agl)-height) > delta {
				return nil
			}

			// This aircraft gets a ghost.

			// This is a little wasteful, but we're going to copy the entire
			// Aircraft structure just to be sure we carry along everything we
			// might want to have available when drawing the track and
			// datablock for the ghost.
			ghost := *ac

			// Now we just need to update the track positions to be those for
			// the ghost. We'll again do this in nm space before going to
			// lat-long in the end.
			pi := ll2nm(pIntersect)
			for i, t := range ghost.Tracks {
				// Vector from the intersection point to the track location
				v := sub2f(ll2nm(t.Position), pi)

				// For tie mode, offset further by the specified distance.
				if c.Mode == CRDAModeTie {
					length := length2f(v)
					v = scale2f(v, (length+c.TieStaggerDistance)/length)
				}

				// Rotate it angle degrees clockwise
				angle := dst.Heading - src.Heading
				s, c := sin(radians(angle)), cos(radians(angle))
				vr := [2]float32{c*v[0] + s*v[1], -s*v[0] + c*v[1]}
				// Point along the other runway
				pr := add2f(pi, vr)

				// TODO: offset it as appropriate
				ghost.Tracks[i].Position = nm2ll(pr)
			}
			return &ghost
	*/
	return nil
}

func (c *CRDAConfig) DrawRegions(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	if !c.ShowCRDARegions {
		return
	}

	transforms.LoadLatLongViewingMatrices(cb)

	// Find the intersection of the two runways.  Work in nm space, not lat-long
	src, dst := c.getRunways()
	if src == nil {
		return
	}

	if dst != nil {
		p, ok := runwayIntersection(ctx.world, src, dst)
		if !ok {
			lg.Infof("no intersection between runways?!")
		}
		//		rs.linesDrawBuilder.AddLine(src.threshold, src.end, RGB{0, 1, 0})
		//		rs.linesDrawBuilder.AddLine(dst.threshold, dst.end, RGB{0, 1, 0})
		var pd PointsDrawBuilder
		pd.AddPoint(p, RGB{1, 0, 0})
		pd.GenerateCommands(cb)
	}

	// we have the runway heading, but we want to go the opposite direction
	// and then +/- HeadingTolerance.
	rota := src.Heading + 180 - c.GlideslopeLateralSpread - ctx.world.MagneticVariation
	rotb := src.Heading + 180 + c.GlideslopeLateralSpread - ctx.world.MagneticVariation

	// Lay out the vectors in nm space, not lat-long
	sina, cosa := sin(radians(rota)), cos(radians(rota))
	va := [2]float32{sina, cosa}
	dist := float32(25)
	va = scale2f(va, dist)

	sinb, cosb := sin(radians(rotb)), cos(radians(rotb))
	vb := scale2f([2]float32{sinb, cosb}, dist)

	// Over to lat-long to draw the lines
	vall, vbll := nm2ll(va, ctx.world.NmPerLongitude), nm2ll(vb, ctx.world.NmPerLongitude)
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	ld.AddLine(src.Threshold, add2ll(src.Threshold, vall), UICautionColor)
	ld.AddLine(src.Threshold, add2ll(src.Threshold, vbll), UICautionColor)
	ld.GenerateCommands(cb)
}

func (c *CRDAConfig) DrawUI() bool {
	panic("FIXME")
	/*
		updateGhosts := false

		flags := imgui.InputTextFlagsCharsUppercase | imgui.InputTextFlagsCharsNoBlank
		imgui.InputTextV("Airport", &c.Airport, flags, nil)
		if runways, ok := database.runways[c.Airport]; !ok {
			if c.Airport != "" {
				color := globalConfig.GetColorScheme().TextError
				imgui.PushStyleColor(imgui.StyleColorText, color.imgui())
				imgui.Text("Airport unknown!")
				imgui.PopStyleColor()
			}
		} else {
			sort.Slice(runways, func(i, j int) bool { return runways[i].Number < runways[j].Number })

			primary, secondary := c.getRunway(c.PrimaryRunway), c.getRunway(c.SecondaryRunway)
			if imgui.BeginComboV("Primary runway", c.PrimaryRunway, imgui.ComboFlagsHeightLarge) {
				if imgui.SelectableV("(None)", c.PrimaryRunway == "", 0, imgui.Vec2{}) {
					updateGhosts = true
					c.PrimaryRunway = ""
				}
				for _, rwy := range runways {
					if secondary != nil {
						// Don't include the selected secondary runway
						if rwy.Number == secondary.Number {
							continue
						}
						// Only list intersecting runways
						if _, ok := runwayIntersection(&rwy, secondary); !ok {
							continue
						}
					}
					if imgui.SelectableV(rwy.Number, rwy.Number == c.PrimaryRunway, 0, imgui.Vec2{}) {
						updateGhosts = true
						c.PrimaryRunway = rwy.Number
					}
				}
				imgui.EndCombo()
			}
			if imgui.BeginComboV("Secondary runway", c.SecondaryRunway, imgui.ComboFlagsHeightLarge) {
				// Note: this is the exact same logic for primary runways
				// above, just with the roles switched...
				if imgui.SelectableV("(None)", c.SecondaryRunway == "", 0, imgui.Vec2{}) {
					updateGhosts = true
					c.SecondaryRunway = ""
				}
				for _, rwy := range runways {
					if primary != nil {
						// Don't include the selected primary runway
						if rwy.Number == primary.Number {
							continue
						}
						// Only list intersecting runways
						if _, ok := runwayIntersection(&rwy, primary); !ok {
							continue
						}
					}
					if imgui.SelectableV(rwy.Number, rwy.Number == c.SecondaryRunway, 0, imgui.Vec2{}) {
						updateGhosts = true
						c.SecondaryRunway = rwy.Number
					}
				}
				imgui.EndCombo()
			}
			if imgui.Checkbox("Ghosts on primary", &c.ShowGhostsOnPrimary) {
				updateGhosts = true
			}
			imgui.Text("Mode")
			imgui.SameLine()
			updateGhosts = imgui.RadioButtonInt("Stagger", &c.Mode, 0) || updateGhosts
			imgui.SameLine()
			updateGhosts = imgui.RadioButtonInt("Tie", &c.Mode, 1) || updateGhosts
			if c.Mode == CRDAModeTie {
				imgui.SameLine()
				updateGhosts = imgui.SliderFloatV("Tie stagger distance", &c.TieStaggerDistance, 0.1, 10, "%.1f", 0) ||
					updateGhosts
			}
			updateGhosts = imgui.SliderFloatV("Heading tolerance (deg)", &c.HeadingTolerance, 5, 180, "%.0f", 0) || updateGhosts
			updateGhosts = imgui.SliderFloatV("Glideslope angle (deg)", &c.GlideslopeAngle, 2, 5, "%.1f", 0) || updateGhosts
			updateGhosts = imgui.SliderFloatV("Glideslope lateral spread (deg)", &c.GlideslopeLateralSpread, 1, 20, "%.0f", 0) || updateGhosts
			updateGhosts = imgui.SliderFloatV("Glideslope vertical spread (deg)", &c.GlideslopeVerticalSpread, 1, 10, "%.1f", 0) || updateGhosts
			updateGhosts = imgui.Checkbox("Show CRDA regions", &c.ShowCRDARegions) || updateGhosts
		}

		return updateGhosts
	*/
}

///////////////////////////////////////////////////////////////////////////
// Additional useful things we may draw on radar scopes...

// DrawCompass emits drawing commands to draw compass heading directions at
// the edges of the current window. It takes a center point p in lat-long
// coordinates, transformation functions and the radar scope's current
// rotation angle, if any.  Drawing commands are added to the provided
// command buffer, which is assumed to have projection matrices set up for
// drawing using window coordinates.
func DrawCompass(p Point2LL, ctx *PaneContext, rotationAngle float32, font *Font, color RGB,
	bounds Extent2D, transforms ScopeTransformations, cb *CommandBuffer) {
	// Window coordinates of the center point.
	// TODO: should we explicitly handle the case of this being outside the window?
	pw := transforms.WindowFromLatLongP(p)

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	// Draw lines at a 5 degree spacing.
	for h := float32(5); h <= 360; h += 5 {
		hr := h + rotationAngle
		dir := [2]float32{sin(radians(hr)), cos(radians(hr))}
		// Find the intersection of the line from the center point to the edge of the window.
		isect, _, t := bounds.IntersectRay(pw, dir)
		if !isect {
			// Happens on initial launch w/o a sector file...
			//lg.Infof("no isect?! p %+v dir %+v bounds %+v", pw, dir, ctx.paneExtent)
			continue
		}

		// Draw a short line from the intersection point at the edge to the
		// point ten pixels back inside the window toward the center.
		pEdge := add2f(pw, scale2f(dir, t))
		pInset := add2f(pw, scale2f(dir, t-10))
		ld.AddLine(pEdge, pInset, color)

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
			pText := add2f(pw, scale2f(dir, t-14))

			// Finer text positioning depends on which edge of the window
			// pane we're on; this is made more grungy because text drawing
			// is specified w.r.t. the position of the upper-left corner...
			if abs(pEdge[0]) < .125 {
				// left edge
				pText[1] += float32(by) / 2
			} else if abs(pEdge[0]-bounds.p1[0]) < .125 {
				// right edge
				pText[0] -= float32(bx)
				pText[1] += float32(by) / 2
			} else if abs(pEdge[1]) < .125 {
				// bottom edge
				pText[0] -= float32(bx) / 2
				pText[1] += float32(by)
			} else if abs(pEdge[1]-bounds.p1[1]) < .125 {
				// top edge
				pText[0] -= float32(bx) / 2
			} else {
				lg.Infof("Edge borkage! pEdge %+v, bounds %+v", pEdge, bounds)
			}

			td.AddText(string(label), pText, TextStyle{Font: font, Color: color})
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

// DrawRangeRings draws ten circles around the specified lat-long point in
// steps of the specified radius (in nm).
func DrawRangeRings(ctx *PaneContext, center Point2LL, radius float32, color RGB, transforms ScopeTransformations,
	cb *CommandBuffer) {
	pixelDistanceNm := transforms.PixelDistanceNM(ctx.world.NmPerLongitude)
	centerWindow := transforms.WindowFromLatLongP(center)

	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	for i := 1; i < 40; i++ {
		// Radius of this ring in pixels
		r := float32(i) * radius / pixelDistanceNm
		ld.AddCircle(centerWindow, r, 360, color)
	}

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// ScopeTransformations

// ScopeTransformations manages various transformation matrices that are
// useful when drawing radar scopes and provides a number of useful methods
// to transform among related coordinate spaces.
type ScopeTransformations struct {
	ndcFromLatLong                       Matrix3
	ndcFromWindow                        Matrix3
	latLongFromWindow, windowFromLatLong Matrix3
}

// GetScopeTransformations returns a ScopeTransformations object
// corresponding to the specified radar scope center, range, and rotation
// angle.
func GetScopeTransformations(ctx *PaneContext, center Point2LL, rangenm float32, rotationAngle float32) ScopeTransformations {
	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	aspect := width / height
	ndcFromLatLong := Identity3x3().
		// Final orthographic projection including the effect of the
		// window's aspect ratio.
		Ortho(-aspect, aspect, -1, 1).
		// Account for magnetic variation and any user-specified rotation
		Rotate(-radians(rotationAngle+ctx.world.MagneticVariation)).
		// Scale based on range and nm per latitude / longitude
		Scale(ctx.world.NmPerLongitude/rangenm, nmPerLatitude/rangenm).
		// Translate to center point
		Translate(-center[0], -center[1])

	ndcFromWindow := Identity3x3().
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
func (st *ScopeTransformations) LoadLatLongViewingMatrices(cb *CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromLatLong)
	cb.LoadModelViewMatrix(Identity3x3())
}

// LoadWindowViewingMatrices adds commands to the provided command buffer
// to load viewing matrices so that window-coordinate positions can be
// provided for subsequent vertices.
func (st *ScopeTransformations) LoadWindowViewingMatrices(cb *CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromWindow)
	cb.LoadModelViewMatrix(Identity3x3())
}

// WindowFromLatLongP transforms a point given in latitude-longitude
// coordinates to window coordinates.
func (st *ScopeTransformations) WindowFromLatLongP(p Point2LL) [2]float32 {
	return st.windowFromLatLong.TransformPoint(p)
}

// LatLongFromWindowP transforms a point p in window coordinates to
// latitude-longitude.
func (st *ScopeTransformations) LatLongFromWindowP(p [2]float32) Point2LL {
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
func (st *ScopeTransformations) LatLongFromWindowV(v [2]float32) Point2LL {
	return st.latLongFromWindow.TransformVector(v)
}

// PixelDistanceNM returns the space between adjacent pixels expressed in
// nautical miles.
func (st *ScopeTransformations) PixelDistanceNM(nmPerLongitude float32) float32 {
	ll := st.LatLongFromWindowV([2]float32{1, 0})
	return nmlength2ll(ll, nmPerLongitude)
}

///////////////////////////////////////////////////////////////////////////
// Other utilities

func UpdateScopePosition(mouse *MouseState, button int, transforms ScopeTransformations,
	center *Point2LL, rangeNM *float32) (moved bool) {
	if mouse == nil {
		return
	}

	// Handle dragging the scope center
	if mouse.Dragging[button] {
		delta := mouse.DragDelta
		if delta[0] != 0 || delta[1] != 0 {
			deltaLL := transforms.LatLongFromWindowV(delta)
			*center = sub2f(*center, deltaLL)
			moved = true
		}
	}

	// Consume mouse wheel
	if mouse.Wheel[1] != 0 {
		scale := pow(1.05, mouse.Wheel[1])

		// We want to zoom in centered at the mouse position; this affects
		// the scope center after the zoom, so we'll find the
		// transformation that gives the new center position.
		mouseLL := transforms.LatLongFromWindowP(mouse.Pos)
		centerTransform := Identity3x3().
			Translate(mouseLL[0], mouseLL[1]).
			Scale(scale, scale).
			Translate(-mouseLL[0], -mouseLL[1])

		*center = centerTransform.TransformPoint(*center)
		*rangeNM *= scale
		moved = true
	}
	return
}

// If the user has run the "find" command to highlight a point in the
// world, draw a red circle around that point for a few seconds.
func DrawHighlighted(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	remaining := time.Until(globalConfig.highlightedLocationEndTime)
	if remaining < 0 {
		return
	}

	color := UIErrorColor
	fade := 1.5
	if sec := remaining.Seconds(); sec < fade {
		x := float32(sec / fade)
		color = lerpRGB(x, RGB{}, color)
	}

	p := transforms.WindowFromLatLongP(globalConfig.highlightedLocation)
	radius := float32(10) // 10 pixel radius
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	ld.AddCircle(p, radius, 360, color)

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(3)
	ld.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// Plane icon

var (
	planeIconTextureId uint32

	//go:embed resources/plane-solid.png
	planeIconPNG string
)

// PlaneIconSpec is a simple structure that specifies the position,
// heading, and size of an aircraft icon to be drawn by DrawPlaneIcons.
type PlaneIconSpec struct {
	P       [2]float32 // should be window coordinates
	Heading float32
	Size    float32
}

// DrawPlaneIcons issues draw commands to the provided command buffer that
// draw aircraft icons with the specified color, as specified by the slice
// of PlaneIconSpec structs.
func DrawPlaneIcons(ctx *PaneContext, specs []PlaneIconSpec, color RGB, cb *CommandBuffer) {
	if planeIconTextureId == 0 {
		if iconImage, err := png.Decode(bytes.NewReader([]byte(planeIconPNG))); err != nil {
			lg.Errorf("Unable to decode plane icon PNG: %v", err)
		} else {
			pyramid := GenerateImagePyramid(iconImage)
			planeIconTextureId = ctx.renderer.CreateTextureFromImages(pyramid)
		}
	}

	td := GetTexturedTrianglesDrawBuilder()
	defer ReturnTexturedTrianglesDrawBuilder(td)

	for _, s := range specs {
		// Start with a one-pixel big quad
		p := [4][2]float32{[2]float32{-.5, -.5}, [2]float32{.5, -.5}, [2]float32{.5, .5}, [2]float32{-.5, .5}}
		uv := [4][2]float32{[2]float32{0, 0}, [2]float32{1, 0}, [2]float32{1, 1}, [2]float32{0, 1}}

		// Transform the corner vertices: scale, rotate, translate...
		for i := range p {
			p[i] = scale2f(p[i], s.Size)
			rot := rotator2f(s.Heading - 90)
			p[i] = rot(p[i])
			p[i] = add2f(p[i], s.P)
		}
		td.AddQuad(p[0], p[1], p[2], p[3], uv[0], uv[1], uv[2], uv[3])
	}

	cb.SetRGB(color)
	td.GenerateCommands(planeIconTextureId, cb)
}

///////////////////////////////////////////////////////////////////////////
// Minimum separation lines

// DrawMinimumSeparationLine estimates the time at which the given two
// aircraft will be the closest together and then draws lines indicating
// where they will be at that point and also text indicating their
// estimated separation then.
func DrawMinimumSeparationLine(p0ll, d0ll, p1ll, d1ll Point2LL, nmPerLongitude float32, color RGB, backgroundColor RGB,
	font *Font, ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	p0, d0 := ll2nm(p0ll, nmPerLongitude), ll2nm(d0ll, nmPerLongitude)
	p1, d1 := ll2nm(p1ll, nmPerLongitude), ll2nm(d1ll, nmPerLongitude)

	// Find the parametric distance along the respective rays of the
	// aircrafts' courses where they at at a minimum distance; this is
	// linearly extrapolating their positions.
	tmin := RayRayMinimumDistance(p0, d0, p1, d1)

	// If something blew up in RayRayMinimumDistance then just bail out here.
	if math.IsInf(float64(tmin), 0) || math.IsNaN(float64(tmin)) {
		return
	}

	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	trid := GetTrianglesDrawBuilder()
	defer ReturnTrianglesDrawBuilder(trid)

	// Draw the separator lines (and triangles, if appropriate.)
	var pw0, pw1 [2]float32     // Window coordinates of the points of minimum approach
	var p0tmin, p1tmin Point2LL // Lat-long coordinates of the points of minimum approach
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
		p0tmin = nm2ll(add2f(p0, scale2f(d0, tmin)), nmPerLongitude)
		p1tmin = nm2ll(add2f(p1, scale2f(d1, tmin)), nmPerLongitude)
		ld.AddLine(p0ll, p0tmin, color)
		ld.AddLine(p0tmin, p1tmin, color)
		ld.AddLine(p1tmin, p1ll, color)

		// Draw small filled triangles centered at p0tmin and p1tmin.
		pw0, pw1 = transforms.WindowFromLatLongP(p0tmin), transforms.WindowFromLatLongP(p1tmin)
		uptri := EquilateralTriangleVertices(6)
		trid.AddTriangle(add2f(pw0, uptri[0]), add2f(pw0, uptri[1]), add2f(pw0, uptri[2]))
		trid.AddTriangle(add2f(pw1, uptri[0]), add2f(pw1, uptri[1]), add2f(pw1, uptri[2]))
	}

	// Draw the text for the minimum distance
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	// Center the text along the minimum distance line
	pText := mid2f(pw0, pw1)
	style := TextStyle{
		Font:            font,
		Color:           color,
		DrawBackground:  true,
		BackgroundColor: backgroundColor,
	}
	text := fmt.Sprintf("%.2f nm", nmdistance2ll(p0tmin, p1tmin))
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
