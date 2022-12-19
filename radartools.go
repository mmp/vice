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
	"sort"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
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
func (w *WeatherRadar) Draw(intensity float32, transforms ScopeTransformations, cb *CommandBuffer) {
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
	for _, rwy := range database.runways[c.Airport] {
		if rwy.Number == n {
			return &rwy
		}
	}
	return nil
}

func (c *CRDAConfig) getRunways() (ghostSource *Runway, ghostDestination *Runway) {
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

	return
}

func runwayIntersection(a *Runway, b *Runway) (Point2LL, bool) {
	p1, p2 := ll2nm(a.Threshold), ll2nm(a.End)
	p3, p4 := ll2nm(b.Threshold), ll2nm(b.End)
	p, ok := LineLineIntersect(p1, p2, p3, p4)

	centroid := mid2f(mid2f(p1, p2), mid2f(p3, p4))
	d := distance2f(centroid, p)
	if d > 30 {
		// more like parallel; we don't care about super far away intersections...
		ok = false
	}

	return nm2ll(p), ok
}

func (c *CRDAConfig) GetGhost(ac *Aircraft) *Aircraft {
	src, dst := c.getRunways()
	if src == nil || dst == nil {
		return nil
	}

	pIntersect, ok := runwayIntersection(src, dst)
	if !ok {
		lg.Printf("No intersection between runways??!?")
		return nil
	}

	airport, ok := database.FAA.airports[c.Airport]
	if !ok {
		lg.Printf("%s: airport unknown?!", c.Airport)
		return nil
	}

	if ac.GroundSpeed() > 350 {
		return nil
	}

	if headingDifference(ac.Heading(), src.Heading) > c.HeadingTolerance {
		return nil
	}

	// Is it on the glideslope?
	// Laterally: compute the heading to the threshold and compare to the
	// glideslope's lateral spread.
	h := headingp2ll(ac.Position(), src.Threshold, database.MagneticVariation)
	if abs(h-src.Heading) > c.GlideslopeLateralSpread {
		return nil
	}

	// Vertically: figure out the range of altitudes at the distance out.
	// First figure out the aircraft's height AGL.
	agl := ac.Altitude() - airport.Elevation

	// Find the glideslope height at the aircraft's distance to the
	// threshold.
	// tan(glideslope angle) = height / threshold distance
	const nmToFeet = 6076.12
	thresholdDistance := nmToFeet * nmdistance2ll(ac.Position(), src.Threshold)
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
		p, ok := runwayIntersection(src, dst)
		if !ok {
			lg.Printf("no intersection between runways?!")
		}
		//		rs.linesDrawBuilder.AddLine(src.threshold, src.end, RGB{0, 1, 0})
		//		rs.linesDrawBuilder.AddLine(dst.threshold, dst.end, RGB{0, 1, 0})
		var pd PointsDrawBuilder
		pd.AddPoint(p, RGB{1, 0, 0})
		pd.GenerateCommands(cb)
	}

	// we have the runway heading, but we want to go the opposite direction
	// and then +/- HeadingTolerance.
	rota := src.Heading + 180 - c.GlideslopeLateralSpread - database.MagneticVariation
	rotb := src.Heading + 180 + c.GlideslopeLateralSpread - database.MagneticVariation

	// Lay out the vectors in nm space, not lat-long
	sina, cosa := sin(radians(rota)), cos(radians(rota))
	va := [2]float32{sina, cosa}
	dist := float32(25)
	va = scale2f(va, dist)

	sinb, cosb := sin(radians(rotb)), cos(radians(rotb))
	vb := scale2f([2]float32{sinb, cosb}, dist)

	// Over to lat-long to draw the lines
	vall, vbll := nm2ll(va), nm2ll(vb)
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	ld.AddLine(src.Threshold, add2ll(src.Threshold, vall), ctx.cs.Caution)
	ld.AddLine(src.Threshold, add2ll(src.Threshold, vbll), ctx.cs.Caution)
	ld.GenerateCommands(cb)
}

func (c *CRDAConfig) DrawUI() bool {
	updateGhosts := false

	flags := imgui.InputTextFlagsCharsUppercase | imgui.InputTextFlagsCharsNoBlank
	imgui.InputTextV("Airport", &c.Airport, flags, nil)
	if runways, ok := database.runways[c.Airport]; !ok {
		if c.Airport != "" {
			color := positionConfig.GetColorScheme().TextError
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
}

///////////////////////////////////////////////////////////////////////////
// DataBlockFormat

// Loosely patterened after https://vrc.rosscarlson.dev/docs/single_page.html#the_various_radar_modes
const (
	DataBlockFormatNone = iota
	DataBlockFormatSimple
	DataBlockFormatGround
	DataBlockFormatTower
	DataBlockFormatFull
	DataBlockFormatCount
)

type DataBlockFormat int

func (d DataBlockFormat) String() string {
	return [...]string{"None", "Simple", "Ground", "Tower", "Full"}[d]
}

func (d *DataBlockFormat) DrawUI() bool {
	changed := false
	if imgui.BeginCombo("Data block format", d.String()) {
		var i DataBlockFormat
		for ; i < DataBlockFormatCount; i++ {
			if imgui.SelectableV(DataBlockFormat(i).String(), i == *d, 0, imgui.Vec2{}) {
				*d = i
				changed = true
			}
		}
		imgui.EndCombo()
	}
	return changed
}

func (d DataBlockFormat) Format(ac *Aircraft, duplicateSquawk bool, flashcycle int) string {
	if d == DataBlockFormatNone {
		return ""
	}

	alt100s := (ac.Altitude() + 50) / 100
	speed := ac.GroundSpeed()
	fp := ac.FlightPlan

	if fp == nil {
		return ac.Squawk.String() + fmt.Sprintf(" %03d", alt100s)
	}

	actype := fp.TypeWithoutSuffix()
	if actype != "" {
		// So we can unconditionally print it..
		actype += " "
	}

	var datablock strings.Builder
	datablock.Grow(64)

	// All of the modes always start with the callsign and the voicce indicator
	datablock.WriteString(ac.Callsign)
	// Otherwise a 3 line datablock
	// Line 1: callsign and voice indicator
	if ac.VoiceCapability == VoiceReceive {
		datablock.WriteString("/r")
	} else if ac.VoiceCapability == VoiceText {
		datablock.WriteString("/t")
	}

	switch d {
	case DataBlockFormatSimple:
		return datablock.String()

	case DataBlockFormatGround:
		datablock.WriteString("\n")
		// Line 2: a/c type and groundspeed
		datablock.WriteString(actype)

		// normally it's groundspeed next, unless there's a squawk
		// situation that we need to flag...
		if duplicateSquawk && ac.Mode != Standby && ac.Squawk != Squawk(0o1200) && ac.Squawk != 0 && flashcycle&1 == 0 {
			datablock.WriteString("CODE")
		} else if !duplicateSquawk && ac.Mode != Standby && ac.Squawk != ac.AssignedSquawk && flashcycle&1 == 0 {
			datablock.WriteString(ac.Squawk.String())
		} else {
			datablock.WriteString(fmt.Sprintf("%02d", speed))
			if fp.Rules == VFR {
				datablock.WriteString("V")
			}
		}
		return datablock.String()

	case DataBlockFormatTower:
		// Line 2: first flash is [alt speed/10]. If we don't have
		// destination and a/c type then just always show this rather than
		// flashing a blank line.
		datablock.WriteString("\n")
		if flashcycle&1 == 0 || (fp.ArrivalAirport == "" && actype == "") {
			datablock.WriteString(fmt.Sprintf("%03d %02d", alt100s, (speed+5)/10))
			if fp.Rules == VFR {
				datablock.WriteString("V")
			}
		} else {
			// Second flash normally alternates between scratchpad (or dest) and
			// filed altitude for the first thing, then has *[actype]
			if flashcycle&2 == 0 {
				if ac.Scratchpad != "" {
					datablock.WriteString(ac.Scratchpad)
				} else {
					datablock.WriteString(fp.ArrivalAirport)
				}
			} else {
				// Second field is the altitude
				datablock.WriteString(fmt.Sprintf("%03d", fp.Altitude/100))
			}

			datablock.WriteString("*")
			// Flag squawk issues
			if duplicateSquawk && ac.Mode != Standby && ac.Squawk != 0 && flashcycle&1 == 0 {
				datablock.WriteString("CODE")
			} else if !duplicateSquawk && ac.Mode != Standby && ac.Squawk != ac.AssignedSquawk && flashcycle&1 == 0 {
				datablock.WriteString(ac.Squawk.String())
			} else {
				datablock.WriteString(actype)
			}
		}
		return datablock.String()

	case DataBlockFormatFull:
		if ac.Mode == Standby {
			return datablock.String()
		}

		dalt := ac.AltitudeChange()
		ascending, descending := dalt > 250, dalt < -250
		altAnnotation := " "
		if ac.TempAltitude != 0 && abs(ac.Altitude()-ac.TempAltitude) < 300 {
			altAnnotation = "T "
		} else if ac.FlightPlan.Altitude != 0 &&
			abs(ac.Altitude()-ac.FlightPlan.Altitude) < 300 {
			altAnnotation = "C "
		} else if ascending {
			altAnnotation = FontAwesomeIconArrowUp + " "
		} else if descending {
			altAnnotation = FontAwesomeIconArrowDown + " "
		}

		if ac.Squawk == Squawk(0o1200) {
			// VFR
			datablock.WriteString(fmt.Sprintf(" %03d", alt100s))
			datablock.WriteString(altAnnotation)
			return datablock.String()
		}
		datablock.WriteString("\n")

		// Line 2: altitude, then scratchpad or temp/assigned altitude.
		datablock.WriteString(fmt.Sprintf("%03d", alt100s))
		datablock.WriteString(altAnnotation)
		// TODO: Here add level if at wrong alt...

		// Have already established it's not squawking standby.
		if duplicateSquawk && ac.Squawk != Squawk(0o1200) && ac.Squawk != 0 {
			if flashcycle&1 == 0 {
				datablock.WriteString("CODE")
			} else {
				datablock.WriteString(ac.Squawk.String())
			}
		} else if ac.Squawk != ac.AssignedSquawk {
			// show what they are actually squawking
			datablock.WriteString(ac.Squawk.String())
		} else {
			if flashcycle&1 == 0 {
				if ac.Scratchpad != "" {
					datablock.WriteString(ac.Scratchpad)
				} else if ac.TempAltitude != 0 {
					datablock.WriteString(fmt.Sprintf("%03dT", ac.TempAltitude/100))
				} else {
					datablock.WriteString(fmt.Sprintf("%03d", fp.Altitude/100))
				}
			} else {
				if fp.ArrivalAirport != "" {
					datablock.WriteString(fp.ArrivalAirport)
				} else {
					datablock.WriteString("????")
				}
			}
		}
		datablock.WriteString("\n")

		// Line 3: a/c type and groundspeed
		datablock.WriteString(actype)
		datablock.WriteString(fmt.Sprintf("%03d", (speed+5)/10*10))
		if fp.Rules == VFR {
			datablock.WriteString("V")
		}

		if ac.Mode == Ident && flashcycle&1 == 0 {
			datablock.WriteString("ID")
		}

		return datablock.String()

	default:
		lg.Printf("%d: unhandled datablock format", d)
		return "ERROR"
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
			//lg.Printf("no isect?! p %+v dir %+v bounds %+v", pw, dir, ctx.paneExtent)
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
				lg.Printf("Edge borkage! pEdge %+v, bounds %+v", pEdge, bounds)
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
func DrawRangeRings(center Point2LL, radius float32, color RGB, transforms ScopeTransformations,
	cb *CommandBuffer) {
	pixelDistanceNm := transforms.PixelDistanceNM()
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
	ndcFromLatLong                       mgl32.Mat4
	ndcFromWindow                        mgl32.Mat4
	latLongFromWindow, windowFromLatLong mgl32.Mat4
}

// GetScopeTransformations returns a ScopeTransformations object
// corresponding to the specified radar scope center, range, and rotation
// angle.
func GetScopeTransformations(ctx *PaneContext, center Point2LL, rangenm float32, rotationAngle float32) ScopeTransformations {
	// Translate to the center point
	ndcFromLatLong := mgl32.Translate3D(-center[0], -center[1], 0)

	// Scale based on range and nm per latitude / longitude
	sc := mgl32.Scale3D(database.NmPerLongitude/rangenm, database.NmPerLatitude/rangenm, 1)
	ndcFromLatLong = sc.Mul4(ndcFromLatLong)

	// Account for magnetic variation and any user-specified rotation
	rot := -radians(rotationAngle + database.MagneticVariation)
	magRot := mgl32.HomogRotate3DZ(rot)
	ndcFromLatLong = magRot.Mul4(ndcFromLatLong)

	// Final orthographic projection including the effect of the
	// window's aspect ratio.
	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	aspect := width / height
	ortho := mgl32.Ortho2D(-aspect, aspect, -1, 1)
	ndcFromLatLong = ortho.Mul4(ndcFromLatLong)

	// FIXME: it's silly to have NDC at all involved here; we can compute
	// latlong from window much more directly.
	latLongFromNDC := ndcFromLatLong.Inv()
	ndcFromWindow := mgl32.Scale3D(2/width, 2/height, 1)
	ndcFromWindow = mgl32.Translate3D(-1, -1, 0).Mul4(ndcFromWindow)
	latLongFromWindow := latLongFromNDC.Mul4(ndcFromWindow)
	windowFromLatLong := latLongFromWindow.Inv()

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
	cb.LoadModelViewMatrix(mgl32.Ident4())
}

// LoadWindowViewingMatrices adds commands to the provided command buffer
// to load viewing matrices so that window-coordinate positions can be
// provided for subsequent vertices.
func (st *ScopeTransformations) LoadWindowViewingMatrices(cb *CommandBuffer) {
	cb.LoadProjectionMatrix(st.ndcFromWindow)
	cb.LoadModelViewMatrix(mgl32.Ident4())
}

func mul4v(m *mgl32.Mat4, v [2]float32) [2]float32 {
	return [2]float32{m[0]*v[0] + m[4]*v[1], m[1]*v[0] + m[5]*v[1]}
}

func mul4p(m *mgl32.Mat4, p [2]float32) [2]float32 {
	return add2f(mul4v(m, p), [2]float32{m[12], m[13]})
}

// WindowFromLatLongP transforms a point given in latitude-longitude
// coordinates to window coordinates.
func (st *ScopeTransformations) WindowFromLatLongP(p Point2LL) [2]float32 {
	return mul4p(&st.windowFromLatLong, p)
}

// LatLongFromWindowP transforms a point p in window coordinates to
// latitude-longitude.
func (st *ScopeTransformations) LatLongFromWindowP(p [2]float32) Point2LL {
	return mul4p(&st.latLongFromWindow, p)
}

// NormalizedFromWindowP transforms a point p in window coordinates to
// normalized [0,1]^2 coordinates.
func (st *ScopeTransformations) NormalizedFromWindowP(p [2]float32) [2]float32 {
	pn := mul4p(&st.ndcFromWindow, p) // [-1,1]
	return [2]float32{(pn[0] + 1) / 2, (pn[1] + 1) / 2}
}

// LatLongFromWindowV transforms a vector in window coordinates to a vector
// in latitude-longitude coordinates.
func (st *ScopeTransformations) LatLongFromWindowV(p [2]float32) Point2LL {
	return mul4v(&st.latLongFromWindow, p)
}

// PixelDistanceNM returns the space between adjacent pixels expressed in
// nautical miles.
func (st *ScopeTransformations) PixelDistanceNM() float32 {
	ll := st.LatLongFromWindowV([2]float32{1, 0})
	return nmlength2ll(ll)
}

///////////////////////////////////////////////////////////////////////////
// Measuring line

// MeasuringLine wraps up the functionality for measuring distances and
// angles on radar scopes.  When a double-click is detected on a scope, it
// records a starting location and then draws a measurement line between
// that point and the current point as the user drags.  When the mouse
// button is released, the line disappears.
type MeasuringLine struct {
	active bool
	// Button selects which mouse button to monitor for measurements.  When zero-initialized,
	// mouseButtonPrimary is used.
	Button             int
	dragStart, dragEnd [2]float32
}

// Draw processes mouse events and draws the measuring line, if it's active.
func (ml *MeasuringLine) Draw(ctx *PaneContext, font *Font, transforms ScopeTransformations, cb *CommandBuffer) {
	if ctx.mouse != nil && ctx.mouse.DoubleClicked[ml.Button] {
		ml.active = true
		ml.dragStart = ctx.mouse.Pos
		ml.dragEnd = ml.dragStart
	} else if ctx.mouse != nil && ctx.mouse.Dragging[ml.Button] && ml.active {
		ml.dragEnd = add2f(ml.dragEnd, ctx.mouse.DragDelta)
	} else {
		ml.active = false
	}

	if !ml.active {
		return
	}

	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	// TODO: separate color for this rather than piggybacking?
	ld.AddLine(ml.dragStart, ml.dragEnd, ctx.cs.SelectedDataBlock)

	// distance between the two points in nm
	p0 := transforms.LatLongFromWindowP(ml.dragStart)
	p1 := transforms.LatLongFromWindowP(ml.dragEnd)
	dist := nmdistance2ll(p0, p1)

	// heading and reciprocal
	hdg := int(headingp2ll(p0, p1, database.MagneticVariation) + 0.5)
	if hdg == 0 {
		hdg = 360
	}
	rhdg := hdg + 180
	if rhdg > 360 {
		rhdg -= 360
	}
	label := fmt.Sprintf(" %.1f nm \n%d / %d", dist, hdg, rhdg)
	style := TextStyle{
		Font:            font,
		Color:           ctx.cs.SelectedDataBlock,
		DrawBackground:  true,
		BackgroundColor: ctx.cs.Background}
	textPos := mid2f(ml.dragStart, ml.dragEnd)

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	td.AddTextCentered(label, textPos, style)

	transforms.LoadWindowViewingMatrices(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
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
		centerTransform := mgl32.Translate3D(-mouseLL[0], -mouseLL[1], 0)
		centerTransform = mgl32.Scale3D(scale, scale, 1).Mul4(centerTransform)
		centerTransform = mgl32.Translate3D(mouseLL[0], mouseLL[1], 0).Mul4(centerTransform)

		*center = mul4p(&centerTransform, *center)
		*rangeNM *= scale
		moved = true
	}
	return
}

// If the user has run the "find" command to highlight a point in the
// world, draw a red circle around that point for a few seconds.
func DrawHighlighted(ctx *PaneContext, transforms ScopeTransformations, cb *CommandBuffer) {
	remaining := time.Until(positionConfig.highlightedLocationEndTime)
	if remaining < 0 {
		return
	}

	color := ctx.cs.Error
	fade := 1.5
	if sec := remaining.Seconds(); sec < fade {
		x := float32(sec / fade)
		color = lerpRGB(x, ctx.cs.Background, color)
	}

	p := transforms.WindowFromLatLongP(positionConfig.highlightedLocation)
	radius := float32(10) // 10 pixel radius
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)
	ld.AddCircle(p, radius, 360, color)

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(3)
	ld.GenerateCommands(cb)
}

////////////////////////////////////////////////////////////////////////////
// Range limits

type RangeLimitList [NumRangeTypes]RangeLimits

func (rl *RangeLimitList) DrawUI() {
	if imgui.BeginTable("RangeLimits", 4) {
		for i := range rl {
			rules := RangeLimitFlightRules(i).String()
			imgui.TableNextColumn()
			imgui.Text(rules)
			imgui.TableNextColumn()
			imgui.Text("Warning")
			imgui.TableNextColumn()
			imgui.SliderFloatV("Lateral (nm)##warn"+rules, &rl[i].WarningLateral, 0, 10, "%.1f", 0)
			imgui.TableNextColumn()
			imgui.InputIntV("Vertical (feet)##warn"+rules, &rl[i].WarningVertical, 100, 100, 0)

			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			imgui.Text("Violation")
			imgui.TableNextColumn()
			imgui.SliderFloatV("Lateral (nm)##viol"+rules, &rl[i].ViolationLateral,
				0, 10, "%.1f", 0)
			imgui.TableNextColumn()
			imgui.InputIntV("Vertical (feet)##viol"+rules, &rl[i].ViolationVertical, 100, 100, 0)
		}
		imgui.EndTable()
	}
}
