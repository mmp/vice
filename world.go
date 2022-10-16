// world.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmp/earcut-go"
	"github.com/mmp/sct2"
)

type World struct {
	// Dynamic things
	aircraft    map[string]*Aircraft
	users       map[string]User
	controllers map[string]*Controller
	metar       map[string]METAR
	atis        map[string]string

	// Map from callsign to the controller currently tracking the aircraft (if any)
	trackingController map[string]string
	// Ones that we are tracking but have offered to another: callsign->dest. controller
	outboundHandoff map[string]string
	// Ones that we are tracking but have offered to another: callsign->offering controller
	inboundHandoff map[string]string

	lastAircraftUpdate map[*Aircraft]time.Time

	// From the FAA (et al.) databases
	FAA struct {
		navaids  map[string]Navaid
		airports map[string]Airport
		fixes    map[string]Fix
		prd      map[AirportPair][]PRDEntry
	}
	callsigns map[string]Callsign

	// From the sector file
	NmPerLatitude     float32
	NmPerLongitude    float32
	MagneticVariation float32

	defaultAirport string
	defaultCenter  Point2LL
	sectorFileId   string

	VORs     map[string]Point2LL
	NDBs     map[string]Point2LL
	fixes    map[string]Point2LL
	airports map[string]Point2LL
	runways  map[string][]Runway

	// Static things to draw; derived from the sector file
	//
	// These only store geometry; no colors; the caller should set current
	// RGB based on the active color scheme.
	runwayCommandBuffer               CommandBuffer
	lowAirwayCommandBuffer            CommandBuffer
	highAirwayCommandBuffer           CommandBuffer
	regions                           []StaticDrawable
	ARTCC                             []StaticDrawable
	ARTCCLow                          []StaticDrawable
	ARTCCHigh                         []StaticDrawable
	geos                              []StaticDrawable
	SIDs, STARs                       []StaticDrawable
	lowAirwayLabels, highAirwayLabels []Label
	labelColorMap                     ColorMap
	labels                            []Label

	// From the position file
	positions map[string][]Position // map key is e.g. JFK_TWR

	radioPrimed bool

	server           ControlServer
	sectorFileColors map[string]RGB

	changes *WorldUpdates
}

type WorldUpdates struct {
	addedAircraft    map[*Aircraft]interface{}
	modifiedAircraft map[*Aircraft]interface{}
	removedAircraft  map[*Aircraft]interface{}
	messages         []TextMessage
}

func NewWorldUpdates() *WorldUpdates {
	c := &WorldUpdates{}
	c.addedAircraft = make(map[*Aircraft]interface{})
	c.modifiedAircraft = make(map[*Aircraft]interface{})
	c.removedAircraft = make(map[*Aircraft]interface{})
	return c
}

func (c *WorldUpdates) Reset() {
	c.addedAircraft = make(map[*Aircraft]interface{})
	c.modifiedAircraft = make(map[*Aircraft]interface{})
	c.removedAircraft = make(map[*Aircraft]interface{})
	c.messages = c.messages[:0]
}

func (c *WorldUpdates) NoUpdates() bool {
	return len(c.addedAircraft) == 0 && len(c.modifiedAircraft) == 0 && len(c.removedAircraft) == 0 &&
		len(c.messages) == 0
}

type Label struct {
	name  string
	p     Point2LL
	color RGB
}

type StaticDrawable struct {
	name     string
	cb       CommandBuffer
	rgbSlice []float32
	bounds   Extent2D
	colorMap ColorMap
}

func Point2LLFromSct2(ll sct2.LatLong) Point2LL {
	return Point2LL{float32(ll.Longitude), float32(ll.Latitude)}
}

func Point2LLFromLL64(latitude, longitude float64) Point2LL {
	return Point2LL{float32(longitude), float32(latitude)}
}

// efficient mapping from names to offsets
type ColorMap struct {
	m   map[string]int
	ids []int
}

func MakeColorMap() ColorMap { return ColorMap{m: make(map[string]int)} }

func (c *ColorMap) Add(name string) {
	if id, ok := c.m[name]; ok {
		c.ids = append(c.ids, id)
	} else {
		newId := len(c.m) + 1
		c.m[name] = newId
		c.ids = append(c.ids, newId)
	}
}

// returns indices of all ids that correspond
func (c *ColorMap) Visit(name string, callback func(int)) {
	if matchId, ok := c.m[name]; !ok {
		return
	} else {
		for i, id := range c.ids {
			if id == matchId {
				callback(i)
			}
		}
	}
}

var (
	// https://www.faa.gov/air_traffic/flight_info/aeronav/aero_data/NASR_Subscription_2022-07-14/
	//go:embed resources/NAV_BASE.csv.zst
	navBaseRaw string
	//go:embed resources/APT_BASE.csv.zst
	airportsRaw string
	//go:embed resources/FIX_BASE.csv.zst
	fixesRaw string
	//go:embed resources/callsigns.csv.zst
	callsignsRaw string
	//go:embed resources/virtual-callsigns.csv.zst
	virtualCallsignsRaw string
	//go:embed resources/prefroutes_db.csv.zst
	prdRaw string

	// Via Arash Partow, MIT licensed
	// https://www.partow.net/miscellaneous/airportdatabase/
	//go:embed resources/GlobalAirportDatabase.txt.zst
	globalAirportsRaw string
)

// Bool indicates whether created
func (w *World) GetOrCreateAircraft(callsign string) (*Aircraft, bool) {
	t := w.CurrentTime()
	if ac, ok := w.aircraft[callsign]; !ok {
		ac = &Aircraft{firstSeen: t}
		ac.flightPlan.callsign = callsign
		w.aircraft[callsign] = ac
		w.lastAircraftUpdate[ac] = t
		return ac, true
	} else {
		w.lastAircraftUpdate[ac] = t
		return ac, false
	}
}

func (w *World) GetAircraft(callsign string) *Aircraft {
	if ac, ok := w.aircraft[callsign]; !ok {
		return nil
	} else {
		return ac
	}
}

func NewWorld() *World {
	rand.Seed(time.Now().UnixNano())

	w := &World{}
	w.aircraft = make(map[string]*Aircraft)
	w.controllers = make(map[string]*Controller)
	w.metar = make(map[string]METAR)
	w.atis = make(map[string]string)
	w.users = make(map[string]User)
	w.lastAircraftUpdate = make(map[*Aircraft]time.Time)
	w.trackingController = make(map[string]string)
	w.outboundHandoff = make(map[string]string)
	w.inboundHandoff = make(map[string]string)

	w.changes = NewWorldUpdates()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { w.FAA.navaids = parseNavaids(); wg.Done() }()
	wg.Add(1)
	go func() { w.FAA.airports = parseAirports(); wg.Done() }()
	wg.Add(1)
	go func() { w.FAA.fixes = parseFixes(); wg.Done() }()
	wg.Add(1)
	go func() { w.FAA.prd = parsePRD(); wg.Done() }()
	wg.Add(1)
	go func() { w.callsigns = parseCallsigns(); wg.Done() }()
	wg.Wait()
	lg.Printf("Finished parsing built-in databases")

	return w
}

func (w *World) LoadSectorFile(filename string) error {
	lg.Printf("%s: loading sector file", filename)
	sectorFile, err := parseSectorFile(filename)
	if err != nil {
		return err
	}

	// Copy over some basic stuff from the sector file
	w.defaultAirport = sectorFile.DefaultAirport
	w.defaultCenter = Point2LLFromSct2(sectorFile.Center)
	w.NmPerLatitude = float32(sectorFile.NmPerLatitude)
	w.NmPerLongitude = float32(sectorFile.NmPerLongitude)
	w.MagneticVariation = float32(sectorFile.MagneticVariation)
	w.sectorFileId = sectorFile.Id

	// Clear out everything that is copied from the sector file in case
	// we're loading a new one.
	w.VORs = make(map[string]Point2LL)
	w.NDBs = make(map[string]Point2LL)
	w.fixes = make(map[string]Point2LL)
	w.airports = make(map[string]Point2LL)
	w.runways = make(map[string][]Runway)
	w.ARTCC = nil
	w.ARTCCLow = nil
	w.ARTCCHigh = nil

	// Static things to draw; derived from the sector file
	w.runwayCommandBuffer = CommandBuffer{}
	w.geos = nil
	w.regions = nil
	w.SIDs = nil
	w.STARs = nil
	w.lowAirwayLabels = nil
	w.highAirwayLabels = nil
	w.lowAirwayCommandBuffer = CommandBuffer{}
	w.highAirwayCommandBuffer = CommandBuffer{}
	w.labelColorMap = ColorMap{}
	w.labels = nil

	loc := func(ls []sct2.NamedLatLong) map[string]Point2LL {
		m := make(map[string]Point2LL)
		for _, loc := range ls {
			m[loc.Name] = Point2LLFromLL64(loc.Latitude, loc.Longitude)
		}
		return m
	}
	w.VORs = loc(sectorFile.VORs)
	w.NDBs = loc(sectorFile.NDBs)
	w.fixes = loc(sectorFile.Fixes)
	w.airports = loc(sectorFile.Airports)

	w.runways = make(map[string][]Runway)
	for _, r := range sectorFile.Runways {
		// Two entries--one for each end of the runway.
		for i := 0; i < 2; i++ {
			w.runways[r.Airport] = append(w.runways[r.Airport],
				Runway{
					number:    r.Number[i],
					heading:   float32(r.Heading[i]),
					threshold: Point2LLFromSct2(r.P[i]),
					end:       Point2LLFromSct2(r.P[i^1])})
		}
	}

	setupARTCC := func(sfARTCC []sct2.ARTCC) []StaticDrawable {
		var artccs []StaticDrawable
		for _, artcc := range sfARTCC {
			ld := LinesDrawBuilder{}
			for _, seg := range artcc.Segs {
				if seg.P[0].Latitude != 0 || seg.P[0].Longitude != 0 {
					v0 := Point2LLFromSct2(seg.P[0])
					v1 := Point2LLFromSct2(seg.P[1])
					ld.AddLine(v0, v1)
				}
			}

			sd := StaticDrawable{name: artcc.Name, bounds: ld.Bounds()}
			ld.GenerateCommands(&sd.cb)
			artccs = append(artccs, sd)
		}
		return artccs
	}
	w.ARTCC = setupARTCC(sectorFile.ARTCC)
	w.ARTCCLow = setupARTCC(sectorFile.ARTCCLow)
	w.ARTCCHigh = setupARTCC(sectorFile.ARTCCHigh)

	currentRegionName := "___UNSET___"
	td := TrianglesDrawBuilder{}
	for i, r := range sectorFile.Regions {
		if len(r.P) == 0 {
			lg.Printf("zero vertices in region \"%s\"?", r.Name)
			continue
		}

		if r.Name != currentRegionName {
			if len(td.indices) > 0 {
				region := StaticDrawable{name: currentRegionName, bounds: td.Bounds()}
				td.GenerateCommands(&region.cb)
				w.regions = append(w.regions, region)
				td.Reset()
			}
			currentRegionName = r.Name
		}

		var poly earcut.Polygon
		for _, p := range r.P {
			v := earcut.Vertex{P: [2]float64{p.Longitude, p.Latitude}}
			poly.Vertices = append(poly.Vertices, v)
		}
		tris := earcut.Triangulate(poly)

		for _, tri := range tris {
			v0 := Point2LL{float32(tri.Vertices[0].P[0]), float32(tri.Vertices[0].P[1])}
			v1 := Point2LL{float32(tri.Vertices[1].P[0]), float32(tri.Vertices[1].P[1])}
			v2 := Point2LL{float32(tri.Vertices[2].P[0]), float32(tri.Vertices[2].P[1])}
			td.AddTriangle(v0, v1, v2)
		}

		if i+1 == len(sectorFile.Regions) && len(td.indices) > 0 {
			region := StaticDrawable{name: r.Name, bounds: td.Bounds()}
			td.GenerateCommands(&region.cb)
			w.regions = append(w.regions, region)
		}
	}

	rld := LinesDrawBuilder{}
	for _, runway := range sectorFile.Runways {
		if runway.P[0].Latitude != 0 || runway.P[0].Longitude != 0 {
			rld.AddLine(Point2LLFromSct2(runway.P[0]), Point2LLFromSct2(runway.P[1]))
		}
	}
	rld.GenerateCommands(&w.runwayCommandBuffer)

	w.labelColorMap = MakeColorMap()
	for _, label := range sectorFile.Labels {
		l := Label{name: label.Name, p: Point2LLFromSct2(label.P)}
		w.labelColorMap.Add(label.Color)
		w.labels = append(w.labels, l)
	}

	w.lowAirwayCommandBuffer, w.lowAirwayLabels = getAirwayCommandBuffers(sectorFile.LowAirways)
	w.highAirwayCommandBuffer, w.highAirwayLabels = getAirwayCommandBuffers(sectorFile.HighAirways)

	staticColoredLines := func(name string, cs []sct2.ColoredSegment, defaultColorName string) StaticDrawable {
		ld := ColoredLinesDrawBuilder{}
		colorMap := MakeColorMap()

		for _, seg := range cs {
			if seg.P[0].Latitude != 0 || seg.P[0].Longitude != 0 {
				if seg.Color != "" {
					colorMap.Add(seg.Color)
				} else {
					colorMap.Add(defaultColorName)
				}
				ld.AddLine(Point2LLFromSct2(seg.P[0]), Point2LLFromSct2(seg.P[1]), RGB{})
			}
		}
		cb := CommandBuffer{}
		start, len := ld.GenerateCommands(&cb)

		return StaticDrawable{
			name:     name,
			cb:       cb,
			rgbSlice: cb.FloatSlice(start, len),
			bounds:   ld.Bounds(),
			colorMap: colorMap}
	}

	for _, sid := range sectorFile.SIDs {
		w.SIDs = append(w.SIDs, staticColoredLines(sid.Name, sid.Segs, "SID"))
	}
	for _, star := range sectorFile.STARs {
		w.STARs = append(w.STARs, staticColoredLines(star.Name, star.Segs, "STAR"))
	}
	for _, geo := range sectorFile.Geo {
		w.geos = append(w.geos, staticColoredLines(geo.Name, geo.Segments, "Geo"))
	}

	w.sectorFileColors = make(map[string]RGB)
	for _, color := range sectorFile.Colors {
		w.sectorFileColors[color.Name] = RGB{R: color.R, G: color.G, B: color.B}
	}

	// Various post-load tidying.
	for _, scheme := range globalConfig.ColorSchemes {
		// Add any colors in the sector file that aren't in scopes'
		// color schemes.
		for name, color := range w.sectorFileColors {
			if _, ok := scheme.DefinedColors[name]; !ok {
				c := color
				scheme.DefinedColors[name] = &c
			}
		}
	}

	lg.Printf("%s: finished loading sector file", filename)

	return nil
}

func (w *World) LoadPositionFile(filename string) error {
	lg.Printf("%s: loading position file", filename)
	var err error
	w.positions, err = parsePositionFile(filename)
	lg.Printf("%s: finished loading position file", filename)

	// Associate controllers with positions
	for callsign, controller := range w.controllers {
		// Clear position from previous position file
		controller.position = nil

		for i, pos := range w.positions[callsign] {
			if pos.frequency == controller.frequency {
				controller.position = &w.positions[callsign][i]
				break
			}
		}
	}

	return err
}

func (w *World) Connected() bool {
	return w.server != nil && w.server.Connected()
}

func (w *World) ConnectFlightRadar() error {
	if w.server != nil {
		return fmt.Errorf("Already connected")
	}

	w.server = NewFlightRadarServer(w)

	return nil
}

func (w *World) ConnectVATSIM(address string) error {
	if w.server != nil {
		return fmt.Errorf("Already connected")
	}
	s, err := NewVATSIMServer(address, w)
	if err != nil {
		return err
	}
	w.server = s

	return nil
}

func (w *World) ConnectVATSIMReplay(filename string, offsetSeconds int, replayRate float32) error {
	if w.server != nil {
		return fmt.Errorf("Already connected")
	}
	s, err := NewVATSIMReplayServer(filename, offsetSeconds, replayRate, w)
	if err != nil {
		return err
	}
	w.server = s

	return nil
}

func (w *World) CurrentTime() time.Time {
	if w.server != nil {
		return w.server.CurrentTime()
	}
	return time.Now()
}

func (w *World) Locate(name string) (Point2LL, bool) {
	name = strings.ToUpper(name)
	// We'll start with the sector file and then move on to the FAA
	// database if we don't find it.
	if pos, ok := w.VORs[name]; ok {
		return pos, ok
	} else if pos, ok := w.NDBs[name]; ok {
		return pos, ok
	} else if pos, ok := w.fixes[name]; ok {
		return pos, ok
	} else if pos, ok := w.airports[name]; ok {
		return pos, ok
	} else if n, ok := w.FAA.navaids[name]; ok {
		return n.location, ok
	} else if f, ok := w.FAA.fixes[name]; ok {
		return f.location, ok
	} else if ap, ok := w.FAA.airports[name]; ok {
		return ap.location, ok
	} else {
		return Point2LL{}, false
	}
}

func (w *World) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var ret []*Aircraft
	for _, ac := range w.aircraft {
		if !filter(ac) {
			ret = append(ret, ac)
		}
	}
	return ret
}

func (w *World) Update() *WorldUpdates {
	w.changes.Reset()

	if w.server == nil {
		if len(w.aircraft) > 0 {
			// First Update() call since a disconnect; let the caller know
			// that all of their aircraft are gone before we clear out
			// w.aircraft.
			updates := NewWorldUpdates()
			for _, ac := range w.aircraft {
				updates.removedAircraft[ac] = nil
			}
			w.aircraft = make(map[string]*Aircraft)
			w.lastAircraftUpdate = make(map[*Aircraft]time.Time)
			w.trackingController = make(map[string]string)
			w.outboundHandoff = make(map[string]string)
			w.inboundHandoff = make(map[string]string)

			return updates
		} else {
			return nil
		}
	}

	w.server.GetUpdates()

	// Clean up anyone who we haven't heard from in 30 minutes
	now := w.CurrentTime()
	for callsign, ac := range w.aircraft {
		if now.Sub(w.lastAircraftUpdate[ac]).Minutes() > 30. {
			delete(w.aircraft, callsign)
			delete(w.trackingController, callsign)
			delete(w.outboundHandoff, callsign)
			delete(w.inboundHandoff, callsign)
			delete(w.lastAircraftUpdate, ac)
			delete(w.changes.addedAircraft, ac)    // just in case
			delete(w.changes.modifiedAircraft, ac) // just in case
			w.changes.removedAircraft[ac] = nil
		}
	}

	// Audio for any new arrivals
	for ac := range w.changes.addedAircraft {
		if positionConfig.IsActiveAirport(ac.flightPlan.arrive) {
			globalConfig.AudioSettings.HandleEvent(AudioEventNewArrival)
			// Only once.
			break
		}
	}

	if len(w.changes.messages) > 0 {
		globalConfig.AudioSettings.HandleEvent(AudioEventReceivedMessage)
	}

	return w.changes
}

func (w *World) SetColorScheme(cs *ColorScheme) {
	for name, color := range cs.DefinedColors {
		w.NamedColorChanged(name, *color)
	}
	w.NamedColorChanged("Geo", cs.Geo)
	w.NamedColorChanged("SID", cs.SID)
	w.NamedColorChanged("STAR", cs.STAR)
}

func (w *World) NamedColorChanged(name string, rgb RGB) {
	update := func(c ColorMap, slice []float32, name string, rgb RGB) {
		c.Visit(name, func(i int) {
			idx := 6 * i
			slice[idx] = rgb.R
			slice[idx+1] = rgb.G
			slice[idx+2] = rgb.B
			slice[idx+3] = rgb.R
			slice[idx+4] = rgb.G
			slice[idx+5] = rgb.B
		})
	}

	switch name {
	case "Geo":
		for _, geo := range w.geos {
			update(geo.colorMap, geo.rgbSlice, "Geo", rgb)
		}

	case "SID":
		for _, sid := range w.SIDs {
			update(sid.colorMap, sid.rgbSlice, "SID", rgb)
		}

	case "STAR":
		for _, star := range w.STARs {
			update(star.colorMap, star.rgbSlice, "STAR", rgb)
		}

	default:
		w.labelColorMap.Visit(name, func(i int) {
			w.labels[i].color = rgb
		})

		for _, geo := range w.geos {
			update(geo.colorMap, geo.rgbSlice, name, rgb)
		}
		for _, sid := range w.SIDs {
			update(sid.colorMap, sid.rgbSlice, name, rgb)
		}
		for _, star := range w.STARs {
			update(star.colorMap, star.rgbSlice, name, rgb)
		}
	}
}

func mungeCSV(filename string, raw string, callback func([]string)) {
	r := bytes.NewReader([]byte(raw))
	cr := csv.NewReader(r)
	cr.ReuseRecord = true

	// Skip the first line with the legend
	_, err := cr.Read()
	if err != nil {
		lg.Errorf("%s: error parsing CSV file: %s", filename, err)
		return
	}

	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			lg.Errorf("%s: error parsing CSV file: %s", filename, err)
			return
		}
		callback(record)
	}
}

func atof(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
		lg.ErrorfUp1("%s: error converting to float: %s", s, err)
		return 0
	} else {
		return v
	}
}

func parseNavaids() map[string]Navaid {
	navaids := make(map[string]Navaid)

	mungeCSV("navaids", decompressZstd(navBaseRaw), func(s []string) {
		n := Navaid{id: s[1], navtype: s[2], name: s[7],
			location: Point2LLFromComponents(s[22:26], s[26:30])}
		navaids[n.id] = n
	})

	return navaids
}

func parseAirports() map[string]Airport {
	airports := make(map[string]Airport)

	// FAA database
	mungeCSV("airports", decompressZstd(airportsRaw), func(s []string) {
		if elevation, err := strconv.ParseFloat(s[24], 64); err != nil {
			lg.Errorf("%s: error parsing elevation: %s", s[24], err)
		} else {
			loc := Point2LLFromComponents(s[15:19], s[19:23])
			ap := Airport{id: s[98], name: s[12], location: loc, elevation: int(elevation)}
			airports[ap.id] = ap
		}
	})

	// Global database
	r := bytes.NewReader([]byte(decompressZstd(globalAirportsRaw)))
	scan := bufio.NewScanner(r)
	for scan.Scan() {
		line := scan.Text()
		f := strings.Split(line, ":")
		if len(f) != 16 {
			lg.Errorf("Expected 16 fields, got %d: %s", len(f), line)
		}

		if elevation, err := strconv.ParseFloat(f[13], 64); err != nil {
			lg.Errorf("%s: error parsing elevation: %s", f[13], err)
		} else {
			elevation *= 3.28084 // meters to feet

			ap := Airport{id: f[0], name: f[2],
				location:  Point2LLFromStrings(f[14], f[15]),
				elevation: int(elevation)}
			airports[ap.id] = ap
		}
	}

	return airports
}

func parseFixes() map[string]Fix {
	fixes := make(map[string]Fix)

	mungeCSV("fixes", decompressZstd(fixesRaw), func(s []string) {
		fix := Fix{id: s[1],
			location: Point2LLFromComponents(s[5:9], s[9:13])}
		fixes[fix.id] = fix
	})

	return fixes
}

func parsePRD() map[AirportPair][]PRDEntry {
	prd := make(map[AirportPair][]PRDEntry)

	mungeCSV("prd", decompressZstd(prdRaw), func(s []string) {
		entry := PRDEntry{
			Depart:       s[0],
			Route:        s[1],
			Arrive:       s[2],
			Hours:        [3]string{s[3], s[4], s[5]},
			Type:         s[6],
			Area:         s[7],
			Altitude:     s[8],
			Aircraft:     s[9],
			Direction:    s[10],
			Seq:          s[11],
			DepCenter:    s[12],
			ArriveCenter: s[13]}
		prd[AirportPair{entry.Depart, entry.Arrive}] =
			append(prd[AirportPair{entry.Depart, entry.Arrive}], entry)
	})

	return prd
}

func parseSectorFile(sectorFilename string) (*sct2.SectorFile, error) {
	contents, err := os.ReadFile(sectorFilename)
	if err != nil {
		return nil, err
	}

	type SctResult struct {
		sf  *sct2.SectorFile
		err error
	}
	ch := make(chan SctResult)
	panicStack := ""

	// Parse the sector file in a goroutine so that we can catch any panics, put up
	// a friendly error message, but continue running.
	go func() {
		var err error
		var sf *sct2.SectorFile
		defer func() {
			if perr := recover(); perr != nil {
				panicStack = string(debug.Stack())
				lg.Errorf("Panic stack: %s", panicStack)
				err = fmt.Errorf("sct2.Parse panicked: %v", perr)
			}

			// Use a channel for the result so that we wait for the
			// goroutine to finish.
			if err != nil {
				ch <- SctResult{err: err}
			} else {
				ch <- SctResult{sf: sf}
			}

			close(ch)
		}()

		errorCallback := func(err string) {
			lg.Errorf("%s: error parsing sector file: %s", sectorFilename, err)
		}
		sf, err = sct2.Parse(contents, sectorFilename, errorCallback)
	}()

	r := <-ch

	if panicStack != "" {
		// Have to do this here so that it's in the main thread...
		ShowFatalErrorDialog("Unfortunately an unexpected error has occurred while parsing the sector file:\n" +
			sectorFilename + "\n" +
			"Apologies! Please do file a bug and include the vice.log file for this session\nso that " +
			"this bug can be fixed.")
	}

	return r.sf, r.err
}

func parsePositionFile(filename string) (map[string][]Position, error) {
	m := make(map[string][]Position)

	r, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	scan := bufio.NewScanner(r)
	for scan.Scan() {
		line := scan.Text()
		if line == "" || line[0] == ';' {
			continue
		}

		fields := strings.Split(line, ":")
		if len(fields) != 11 {
			lg.Printf("%s: expected 11 fields, got %d: [%+v]", filename, len(fields), fields)
			continue
		}

		var frequency float64
		frequency, err := strconv.ParseFloat(fields[2], 32)
		if err != nil {
			lg.Printf("%s: error parsing frequency: [%+v]", err, fields)
			continue
		}
		// Note: parse as octal!
		var lowSquawk, highSquawk int64
		lowSquawk, err = strconv.ParseInt(fields[9], 8, 32)
		if err != nil {
			// This happens for e.g. entries for neighboring ARTCCs
			lowSquawk = -1
		}
		highSquawk, err = strconv.ParseInt(fields[10], 8, 32)
		if err != nil {
			// This happens for e.g. entries for neighboring ARTCCs
			highSquawk = -1
		}

		id := fields[5] + "_" + fields[6]
		p := Position{
			name:      fields[0],
			callsign:  fields[1],
			frequency: Frequency(frequency),
			sectorId:  fields[3],
			scope:     fields[4],
			id:        id,
			// ignore fields 7/8
			lowSquawk:  Squawk(lowSquawk),
			highSquawk: Squawk(highSquawk)}

		m[id] = append(m[id], p)
	}
	return m, nil
}

func parseCallsigns() map[string]Callsign {
	callsigns := make(map[string]Callsign)

	addCallsign := func(s []string) {
		for idx, str := range s {
			s[idx] = strings.TrimSpace(str)
		}

		cs := Callsign{company: stopShouting(s[0]),
			country:   stopShouting(s[1]),
			telephony: stopShouting(s[2]),
			threeltr:  s[3]}
		if cs.threeltr == "..." {
			// Not sure what these are about...
			return
		}
		callsigns[cs.threeltr] = cs
	}

	mungeCSV("callsigns", decompressZstd(callsignsRaw), addCallsign)
	// Do virtual second since we let them take precedence
	mungeCSV("virtual callsigns", decompressZstd(virtualCallsignsRaw), addCallsign)

	return callsigns
}

func getAirwayCommandBuffers(airways []sct2.Airway) (CommandBuffer, []Label) {
	m := make(map[sct2.Segment][]string)
	for _, a := range airways {
		for _, seg := range a.Segs {
			if seg.P[0].Latitude == 0 && seg.P[0].Longitude == 0 {
				continue
			}

			// canonical order
			if seg.P[0].Latitude > seg.P[1].Latitude {
				seg.P[0], seg.P[1] = seg.P[1], seg.P[0]
			}

			// has it already been labeled? (shouldn't be, but...)
			if labels, ok := m[seg]; ok {
				labeled := false
				for _, l := range labels {
					if l == a.Name {
						labeled = true
						break
					}
				}
				if labeled {
					lg.Errorf("Unexpectedly labeled airway: %s in %+v", a.Name, labels)
					continue
				}
				m[seg] = append(labels, a.Name)
			} else {
				m[seg] = []string{a.Name}
			}
		}
	}

	lines := LinesDrawBuilder{}
	var labels []Label
	for seg, l := range m {
		label := strings.Join(l, "/")
		mid := sct2.LatLong{Latitude: (seg.P[0].Latitude + seg.P[1].Latitude) / 2,
			Longitude: (seg.P[0].Longitude + seg.P[1].Longitude) / 2}
		labels = append(labels, Label{name: label, p: Point2LLFromSct2(mid), color: RGB{}})

		lines.AddLine(Point2LLFromSct2(seg.P[0]), Point2LLFromSct2(seg.P[1]))
	}

	cb := CommandBuffer{}
	lines.GenerateCommands(&cb)

	return cb, labels
}

///////////////////////////////////////////////////////////////////////////
// Methods implementing the ControlClient interface

func (w *World) METARReceived(m METAR) {
	w.metar[m.airport] = m
}

func (w *World) UserAdded(callsign string, user User) {
	w.users[callsign] = user
}

func (w *World) ATISReceived(issuer string, letter byte, contents string) {
	globalConfig.AudioSettings.HandleEvent(AudioEventUpdatedATIS)
	w.atis[issuer] = string(letter) + " " + contents
}

func (w *World) PilotAdded(pilot Pilot) {
	// TODO
	/*
		ac, created := w.GetOrCreateAircraft(pilot.callsign)
		if created {
			w.changes.addedAircraft[ac] = nil
		} else {
			w.changes.modifiedAircraft[ac] = nil
		}
	*/
}

func (w *World) PilotRemoved(callsign string) {
	if ac, ok := w.aircraft[callsign]; !ok {
		//lg.Printf("%s: disconnected but not in active aircraft", callsign)
	} else {
		delete(w.aircraft, callsign)
		delete(w.changes.addedAircraft, ac)    // just in case
		delete(w.changes.modifiedAircraft, ac) // just in case
		w.changes.removedAircraft[ac] = nil
	}
}

func (w *World) ControllerAdded(controller Controller) {
	if c, ok := w.controllers[controller.callsign]; ok {
		// it already exists; merge fields...
		if controller.name != "" {
			c.name = controller.name
		}
		if controller.cid != "" {
			c.cid = controller.cid
		}
		if controller.rating != 0 {
			c.rating = controller.rating
		}
		if controller.frequency != 0 {
			c.frequency = controller.frequency
		}
		if controller.scopeRange != 0 {
			c.scopeRange = controller.scopeRange
		}
		if controller.facility != 0 {
			c.facility = controller.facility
		}
		if controller.location[0] != 0 && controller.location[1] != 0 {
			c.location = controller.location
		}
	} else {
		w.controllers[controller.callsign] = &controller
	}

	// Find the associated position
	if controller.frequency != 0 {
		callsign := controller.callsign
		c := w.controllers[callsign]
		c.position = nil

		// compute the basic callsign: e.g. NY_1_CTR -> NY_CTR, PHL_ND_APP -> PHL_APP
		cf := strings.Split(callsign, "_")
		if len(cf) > 2 {
			callsign = cf[0] + "_" + cf[len(cf)-1]
		}

		for i, pos := range w.positions[callsign] {
			if pos.frequency == c.frequency {
				c.position = &w.positions[callsign][i]
				break
			}
		}
	}
}

func (w *World) ControllerRemoved(position string) {
	if _, ok := w.controllers[position]; !ok {
		//lg.Printf("Attempting to remove unknown controller: %s", position)
	} else {
		delete(w.controllers, position)
	}
}

func (w *World) RequestRelief(callsign string) {
	if c, ok := w.controllers[callsign]; ok {
		c.requestRelief = true
	}
}

func (w *World) CancelRequestRelief(callsign string) {
	if c, ok := w.controllers[callsign]; ok {
		c.requestRelief = false
	}
}

func (w *World) SquawkAssigned(callsign string, squawk Squawk) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	ac.assignedSquawk = squawk
}

func (w *World) FlightPlanReceived(fp FlightPlan) {
	ac, created := w.GetOrCreateAircraft(fp.callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	ac.flightPlan = fp

	if positionConfig.IsActiveAirport(fp.depart) {
		globalConfig.AudioSettings.HandleEvent(AudioEventFlightPlanFiled)
	}
}

func (w *World) FlightStripPushed(from string, to string, fs FlightStrip) {
}

func (w *World) PositionReceived(callsign string, pos RadarTrack, squawk Squawk, mode TransponderMode) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	ac.squawk = squawk
	ac.mode = mode

	// Move everthing forward one to make space for the new one. We could
	// be clever and use a circular buffer to skip the copies, though at
	// the cost of more painful indexing elsewhere...
	copy(ac.tracks[1:], ac.tracks[:len(ac.tracks)-1])
	ac.tracks[0] = pos
}

func (w *World) AltitudeAssigned(callsign string, altitude int) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	ac.flightPlan.altitude = altitude
}

func (w *World) TemporaryAltitudeAssigned(callsign string, altitude int) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	ac.tempAltitude = altitude
}

func (w *World) VoiceSet(callsign string, vc VoiceCapability) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	ac.voiceCapability = vc
}

func (w *World) ScratchpadSet(callsign string, contents string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	ac.scratchpad = contents
}

func (w *World) TrackInitiated(callsign string, controller string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	if tc, ok := w.trackingController[callsign]; ok && tc != controller {
		lg.Printf("%s: %s is tracking controller but %s initiated track?", callsign,
			tc, controller)
	}
	w.trackingController[callsign] = controller
}

func (w *World) TrackDropped(callsign string, controller string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	if tc, ok := w.trackingController[callsign]; ok && tc != controller {
		lg.Printf("%s: %s dropped track but tracking controller is %s?",
			callsign, controller, tc)
	}
	delete(w.trackingController, callsign)
}

func (w *World) PointOutReceived(callsign string, controller string) {
	lg.Printf("%s: point out from %s", callsign, controller)

	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	globalConfig.AudioSettings.HandleEvent(AudioEventPointOut)

	// TODO
}

func (w *World) HandoffOffered(callsign string, controller string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	if tc, ok := w.trackingController[callsign]; !ok || tc != controller {
		// But allow it anyway...
		lg.Printf("%s offering h/o of %s but doesn't control the a/c?",
			controller, callsign)
	}

	w.inboundHandoff[callsign] = controller

	globalConfig.AudioSettings.HandleEvent(AudioEventHandoffRequest)
}

// Note that this is only called when other controllers accept handoffs
func (w *World) HandoffAccepted(callsign string, from string, to string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = nil
	} else {
		w.changes.modifiedAircraft[ac] = nil
	}

	if tc, ok := w.trackingController[callsign]; !ok || tc != from {
		lg.Printf("%s: %s is tracking but h/o accepted from %s to %s?", callsign, tc, from, to)
	} else if from == w.server.Callsign() {
		// from us!
		globalConfig.AudioSettings.HandleEvent(AudioEventHandoffAccepted)
		delete(w.outboundHandoff, callsign)
	}

	w.trackingController[callsign] = to
}

func (w *World) TextMessageReceived(sender string, m TextMessage) {
	if len(w.MonitoredFrequencies(m.frequencies)) > 0 {
		w.changes.messages = append(w.changes.messages, m)
	}
}

func (w *World) MonitoredFrequencies(frequencies []Frequency) []Frequency {
	var monitored []Frequency
	for _, f := range frequencies {
		// For now it's just the primed frequency...
		if w.radioPrimed && f == positionConfig.PrimaryFrequency {
			monitored = append(monitored, f)
		}
	}
	return monitored
}

func (w *World) PrimaryFrequency() Frequency {
	if !w.radioPrimed {
		return Frequency(0)
	}
	return positionConfig.PrimaryFrequency
}

func (w *World) Disconnect() error {
	if w.server == nil {
		return fmt.Errorf("Not connected")
	}
	w.server.Disconnect()

	// Clear everything but aircraft out; keep them around until the next
	// Update() call so that we can report them as removed then..
	w.users = make(map[string]User)
	w.controllers = make(map[string]*Controller)
	w.metar = make(map[string]METAR)
	w.atis = make(map[string]string)
	w.radioPrimed = false

	w.server = nil

	return nil
}

func (w *World) WindowTitle() string {
	if w.server == nil {
		return "[Disconnected]"
	}
	return w.server.GetWindowTitle()
}

///////////////////////////////////////////////////////////////////////////
// Control

// The following methods parallel much of the ControlServer interface,
// though with the ability to return errors for nonsensical/invalid
// requests (as far as we can tell, at least.)  This allows World to do its
// bookkeeping before forwarding the request on to the server. Note that
// they all call GetAircraft() rather than GetOrCreateAircraft() since they
// should only be called for existing aircraft (in contrast to things that
// come on the stream from VATSIM, where messages may be the first we've
// heard of something, but we should pay attention...)

var (
	ErrNoAircraftForCallsign   = errors.New("No aircraft exists with specified callsign")
	ErrScratchpadTooLong       = errors.New("Scratchpad too long: 3 character limit")
	ErrAirportTooLong          = errors.New("Airport name too long: 4 character limit")
	ErrOtherControllerHasTrack = errors.New("Another controller is already tracking the aircraft")
	ErrNotTrackedByMe          = errors.New("Aircraft is not tracked by current controller")
	ErrNotBeingHandedOffToMe   = errors.New("Aircraft not being handed off to current controller")
)

func (w *World) SetSquawk(callsign string, squawk Squawk) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.assignedSquawk = squawk
		w.server.SetSquawk(callsign, squawk)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) SetSquawkAutomatic(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if ac.flightPlan.rules != IFR {
		return errors.New("non-IFR squawk codes must be set manually")
	} else {
		if c, ok := w.controllers[w.server.Callsign()]; !ok {
			lg.Errorf("%s: no Controller for me?", w.server.Callsign())
			return errors.New("Must be signed in to a control position")
		} else {
			pos := c.position
			if pos.lowSquawk == pos.highSquawk {
				return errors.New("Current position has not been assigned a squawk code range")
			}

			squawkUnused := func(sq Squawk) bool {
				for _, ac := range w.aircraft {
					if ac.assignedSquawk == sq {
						return false
					}
				}
				return true
			}

			// Start at a random point in the range and then go linearly from
			// there.
			n := int(pos.highSquawk - pos.lowSquawk)
			offset := rand.Int() % n
			for i := 0; i < n; i++ {
				sq := pos.lowSquawk + Squawk((i+offset)%n)
				if squawkUnused(sq) {
					ac.assignedSquawk = sq
					w.server.SetSquawk(callsign, sq)

					w.changes.modifiedAircraft[ac] = nil
					return nil
				}
			}
			return fmt.Errorf("No free squawk codes between %s and %s(!)",
				pos.lowSquawk, pos.highSquawk)
		}
	}
}

func (w *World) SetScratchpad(callsign string, scratchpad string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if len(scratchpad) > 3 {
		return ErrScratchpadTooLong
	} else {
		ac.scratchpad = scratchpad
		w.server.SetScratchpad(callsign, scratchpad)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) SetRoute(callsign string, route string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.flightPlan.route = route
		w.server.SetRoute(callsign, route)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) SetDeparture(callsign string, airport string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if len(airport) > 4 {
		return ErrAirportTooLong
	} else {
		ac.flightPlan.depart = airport
		w.server.SetDeparture(callsign, airport)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) SetArrival(callsign string, airport string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if len(airport) > 4 {
		return ErrAirportTooLong
	} else {
		ac.flightPlan.arrive = airport
		w.server.SetArrival(callsign, airport)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) SetAltitude(callsign string, altitude int) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.flightPlan.altitude = altitude
		w.server.SetAltitude(callsign, altitude)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) SetTemporaryAltitude(callsign string, altitude int) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.tempAltitude = altitude
		w.server.SetTemporaryAltitude(callsign, altitude)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) SetAircraftType(callsign string, actype string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.flightPlan.actype = actype
		w.server.SetAircraftType(callsign, actype)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) SetFlightRules(callsign string, r FlightRules) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.flightPlan.rules = r
		w.server.SetFlightRules(callsign, r)

		w.changes.modifiedAircraft[ac] = nil
		return nil
	}
}

func (w *World) PushFlightStrip(callsign string, controller string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		// TODO
		w.server.PushFlightStrip(callsign, controller)
		return nil
	}
}

func (w *World) InitiateTrack(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		// ??? Or should we always go for it, send the message, and let the
		// server tell us if we're unable?
		if _, ok := w.trackingController[callsign]; ok {
			return ErrOtherControllerHasTrack
		}

		w.trackingController[callsign] = w.server.Callsign()
		w.changes.modifiedAircraft[ac] = nil

		w.server.InitiateTrack(callsign)
		return nil
	}
}

func (w *World) Handoff(callsign string, controller string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		if tc, ok := w.trackingController[callsign]; !ok || tc != w.server.Callsign() {
			return ErrNotTrackedByMe
		}

		w.outboundHandoff[callsign] = controller
		w.changes.modifiedAircraft[ac] = nil

		w.server.Handoff(callsign, controller)
		return nil
	}
}

func (w *World) AcceptHandoff(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		if _, ok := w.inboundHandoff[callsign]; !ok {
			return ErrNotBeingHandedOffToMe
		}
		delete(w.inboundHandoff, callsign)
		w.trackingController[callsign] = w.server.Callsign()
		w.changes.modifiedAircraft[ac] = ac

		w.server.AcceptHandoff(callsign)
		return nil
	}
}

func (w *World) RejectHandoff(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		if _, ok := w.inboundHandoff[callsign]; !ok {
			return ErrNotBeingHandedOffToMe
		}
		delete(w.inboundHandoff, callsign)
		w.server.RejectHandoff(callsign)

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) DropTrack(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		if tc, ok := w.trackingController[callsign]; !ok || tc != w.server.Callsign() {
			return ErrNotTrackedByMe
		}
		delete(w.trackingController, callsign)
		w.changes.modifiedAircraft[ac] = nil
		w.server.DropTrack(callsign)
		return nil
	}
}

func (w *World) PointOut(callsign string, controller string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		// TODO: anything here?
		w.server.PointOut(callsign, controller)
		// TODO: add to modified aircraft?
		return nil
	}
}

func (w *World) SendTextMessage(m TextMessage) error {
	w.server.SendTextMessage(m)
	return nil
}
