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

	user struct {
		callsign string
		facility Facility
		position *Position // may be nil (e.g., OBS)
	}

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

	VORs      map[string]Point2LL
	NDBs      map[string]Point2LL
	fixes     map[string]Point2LL
	airports  map[string]Point2LL
	runways   map[string][]Runway
	ARTCC     []ARTCC
	ARTCCLow  []ARTCC
	ARTCCHigh []ARTCC

	// Static things to draw; derived from the sector file
	runwayGeom                        LinesDrawable
	geos                              []Geo
	regionGeom                        []Region
	regionColorMap                    ColorMap
	SIDs, STARs                       []SidStar
	lowAirwayLabels, highAirwayLabels []Label
	lowAirwayGeom, highAirwayGeom     LinesDrawable
	labelColorMap                     ColorMap
	labels                            []Label

	// From the position file
	positions map[string][]Position // map key is e.g. JFK_TWR

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
	return len(c.addedAircraft) == 0 && len(c.modifiedAircraft) == 0 && len(c.removedAircraft) == 0
}

type Label struct {
	name  string
	p     Point2LL
	color RGB
}

type ARTCC struct {
	name  string
	lines LinesDrawable
}

type Geo struct {
	name     string
	lines    LinesDrawable
	colorMap ColorMap
}

type Region struct {
	tris   TrianglesDrawable
	bounds Extent2D
}

type SidStar struct {
	name     string
	lines    LinesDrawable
	bounds   Extent2D
	colorMap ColorMap
}

func Point2LLFromSct2(ll sct2.LatLong) Point2LL {
	return Point2LL{float32(ll.Longitude), float32(ll.Latitude)}
}

func Point2LLFromLL64(latitude, longitude float64) Point2LL {
	return Point2LL{float32(longitude), float32(latitude)}
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
	if ac, ok := w.aircraft[callsign]; !ok {
		ac = &Aircraft{firstSeen: time.Now()}
		ac.flightPlan.callsign = callsign
		w.aircraft[callsign] = ac
		ac.lastSeen = time.Now()
		return ac, true
	} else {
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
	w.runwayGeom = LinesDrawable{}
	w.geos = nil
	w.regionGeom = nil
	w.regionColorMap = ColorMap{}
	w.SIDs = nil
	w.STARs = nil
	w.lowAirwayLabels = nil
	w.highAirwayLabels = nil
	w.lowAirwayGeom = LinesDrawable{}
	w.highAirwayGeom = LinesDrawable{}
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

	setupARTCC := func(sfARTCC []sct2.ARTCC) []ARTCC {
		var artccs []ARTCC
		for _, artcc := range sfARTCC {
			a := ARTCC{name: artcc.Name}
			for _, seg := range artcc.Segs {
				if seg.P[0].Latitude != 0 || seg.P[0].Longitude != 0 {
					v0 := Point2LLFromSct2(seg.P[0])
					v1 := Point2LLFromSct2(seg.P[1])
					a.lines.AddLine(v0, v1, RGB{})
				}
			}
			artccs = append(artccs, a)
		}
		return artccs
	}
	w.ARTCC = setupARTCC(sectorFile.ARTCC)
	w.ARTCCLow = setupARTCC(sectorFile.ARTCCLow)
	w.ARTCCHigh = setupARTCC(sectorFile.ARTCCHigh)

	w.regionColorMap = MakeColorMap()
	for _, r := range sectorFile.Regions {
		if len(r.P) == 0 {
			lg.Printf("zero vertices in region \"%s\"?", r.Name)
			continue
		}

		var poly earcut.Polygon
		for _, p := range r.P {
			v := earcut.Vertex{P: [2]float64{p.Longitude, p.Latitude}}
			poly.Vertices = append(poly.Vertices, v)
		}
		tris := earcut.Triangulate(poly)

		td := TrianglesDrawable{}
		for _, tri := range tris {
			v0 := Point2LL{float32(tri.Vertices[0].P[0]), float32(tri.Vertices[0].P[1])}
			v1 := Point2LL{float32(tri.Vertices[1].P[0]), float32(tri.Vertices[1].P[1])}
			v2 := Point2LL{float32(tri.Vertices[2].P[0]), float32(tri.Vertices[2].P[1])}
			td.AddTriangle(v0, v1, v2, RGB{})
		}

		w.regionGeom = append(w.regionGeom, Region{tris: td, bounds: td.Bounds()})
		if r.Name == "" {
			w.regionColorMap.Add("Region")
		} else {
			w.regionColorMap.Add(r.Name)
		}
	}

	for _, runway := range sectorFile.Runways {
		if runway.P[0].Latitude != 0 || runway.P[0].Longitude != 0 {
			w.runwayGeom.AddLine(Point2LLFromSct2(runway.P[0]), Point2LLFromSct2(runway.P[1]), RGB{})
		}
	}

	w.labelColorMap = MakeColorMap()
	for _, label := range sectorFile.Labels {
		l := Label{name: label.Name, p: Point2LLFromSct2(label.P)}
		w.labelColorMap.Add(label.Color)
		w.labels = append(w.labels, l)
	}

	w.lowAirwayGeom, w.lowAirwayLabels = getAirwayDrawables(sectorFile.LowAirways)
	w.highAirwayGeom, w.highAirwayLabels = getAirwayDrawables(sectorFile.HighAirways)

	sidStarGeom := func(cs []sct2.ColoredSegment, defaultColorName string) (LinesDrawable, ColorMap) {
		ld := LinesDrawable{}
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
		return ld, colorMap
	}
	for _, sid := range sectorFile.SIDs {
		lines, colorMap := sidStarGeom(sid.Segs, "SID")
		w.SIDs = append(w.SIDs, SidStar{
			name:     sid.Name,
			lines:    lines,
			bounds:   lines.Bounds(),
			colorMap: colorMap})
	}
	for _, star := range sectorFile.STARs {
		lines, colorMap := sidStarGeom(star.Segs, "STAR")
		w.STARs = append(w.STARs, SidStar{
			name:     star.Name,
			lines:    lines,
			bounds:   lines.Bounds(),
			colorMap: colorMap})
	}

	for _, geo := range sectorFile.Geo {
		worldGeo := Geo{name: geo.Name, colorMap: MakeColorMap()}
		for i, seg := range geo.Segments {
			worldGeo.lines.AddLine(Point2LLFromSct2(seg.P[0]), Point2LLFromSct2(seg.P[1]), RGB{})
			if geo.Colors[i] == "" {
				worldGeo.colorMap.Add("Geo")
			} else {
				worldGeo.colorMap.Add(geo.Colors[i])
			}
		}
		w.geos = append(w.geos, worldGeo)
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
	return w.server != nil
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
	s, err := NewVATSIMServer("CALLSIGN", FacilityOBS, nil, w, address)
	if err != nil {
		return err
	}
	w.server = s

	/*
		w.user.callsign = callsign
		w.user.facility = facility
		w.user.position = position
	*/

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
			return updates
		} else {
			return nil
		}
	}

	w.server.GetUpdates()

	// Clean up anyone who we haven't heard from in 30 minutes
	for callsign, ac := range w.aircraft {
		if time.Since(ac.lastSeen).Minutes() > 30. {
			delete(w.aircraft, callsign)
			delete(w.changes.addedAircraft, ac)    // just in case
			delete(w.changes.modifiedAircraft, ac) // just in case
			//lg.Printf("removed 30 mins %s: %+v", callsign, ac)
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
	w.NamedColorChanged("ARTCC", cs.ARTCC)
	w.NamedColorChanged("Geo", cs.Geo)
	w.NamedColorChanged("HighAirway", cs.HighAirway)
	w.NamedColorChanged("LowAirway", cs.LowAirway)
	w.NamedColorChanged("Region", cs.Region)
	w.NamedColorChanged("Runway", cs.Runway)
	w.NamedColorChanged("SID", cs.SID)
	w.NamedColorChanged("STAR", cs.STAR)
}

func (w *World) NamedColorChanged(name string, rgb RGB) {
	switch name {
	case "ARTCC":
		updateARTCC := func(artcc []ARTCC) {
			for _, a := range artcc {
				for i := range a.lines.color {
					a.lines.color[i] = rgb
				}
			}
		}
		updateARTCC(w.ARTCC)
		updateARTCC(w.ARTCCLow)
		updateARTCC(w.ARTCCHigh)

	case "Geo":
		for _, geo := range w.geos {
			geo.colorMap.Visit("Geo", func(i int) {
				geo.lines.color[2*i] = rgb
				geo.lines.color[2*i+1] = rgb
			})
		}

	case "HighAirway":
		for i := range w.highAirwayGeom.color {
			w.highAirwayGeom.color[i] = rgb
		}
		for i := range w.highAirwayLabels {
			w.highAirwayLabels[i].color = rgb
		}

	case "LowAirway":
		for i := range w.lowAirwayGeom.color {
			w.lowAirwayGeom.color[i] = rgb
		}
		for i := range w.lowAirwayLabels {
			w.lowAirwayLabels[i].color = rgb
		}

	case "Region":
		w.regionColorMap.Visit("Region", func(i int) {
			for j := range w.regionGeom[i].tris.color {
				w.regionGeom[i].tris.color[j] = rgb
			}
		})

	case "Runway":
		for i := range w.runwayGeom.color {
			w.runwayGeom.color[i] = rgb
		}

	case "SID":
		for _, sid := range w.SIDs {
			sid.colorMap.Visit("SID", func(i int) {
				sid.lines.color[2*i] = rgb
				sid.lines.color[2*i+1] = rgb
			})
		}

	case "STAR":
		for _, star := range w.STARs {
			star.colorMap.Visit("STAR", func(i int) {
				star.lines.color[2*i] = rgb
				star.lines.color[2*i+1] = rgb
			})
		}

	default:
		w.labelColorMap.Visit(name, func(i int) {
			w.labels[i].color = rgb
		})
		w.regionColorMap.Visit(name, func(i int) {
			for j := range w.regionGeom[i].tris.color {
				w.regionGeom[i].tris.color[j] = rgb
			}
		})
		for _, geo := range w.geos {
			geo.colorMap.Visit(name, func(i int) {
				geo.lines.color[i] = rgb
			})
		}

		updateColors := func(sidstar []SidStar) {
			for _, ss := range sidstar {
				ss.colorMap.Visit(name, func(i int) {
					ss.lines.color[2*i] = rgb
					ss.lines.color[2*i+1] = rgb
				})
			}
		}
		updateColors(w.SIDs)
		updateColors(w.STARs)
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
	errorCallback := func(err string) {
		lg.Errorf("%s: error parsing sector file: %s", sectorFilename, err)
	}
	sf, err := sct2.Parse(contents, sectorFilename, errorCallback)
	if err != nil {
		return nil, err
	}

	return sf, nil
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

func getAirwayDrawables(airways []sct2.Airway) (LinesDrawable, []Label) {
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

	lines := LinesDrawable{}
	var labels []Label
	for seg, l := range m {
		label := strings.Join(l, "/")
		mid := sct2.LatLong{Latitude: (seg.P[0].Latitude + seg.P[1].Latitude) / 2,
			Longitude: (seg.P[0].Longitude + seg.P[1].Longitude) / 2}
		labels = append(labels, Label{name: label, p: Point2LLFromSct2(mid), color: RGB{}})

		lines.AddLine(Point2LLFromSct2(seg.P[0]), Point2LLFromSct2(seg.P[1]), RGB{})
	}

	return lines, labels
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
			w.changes.addedAircraft[ac] = ac
		} else {
			w.changes.modifiedAircraft[ac] = ac
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

func (w *World) SquawkAssigned(callsign string, squawk Squawk) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	ac.assignedSquawk = squawk
}

func (w *World) FlightPlanReceived(fp FlightPlan) {
	ac, created := w.GetOrCreateAircraft(fp.callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
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
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	ac.squawk = squawk
	ac.mode = mode
	ac.lastSeen = time.Now()

	// Move everthing forward one to make space for the new one. We could
	// be clever and use a circular buffer to skip the copies, though at
	// the cost of more painful indexing elsewhere...
	copy(ac.tracks[1:], ac.tracks[:len(ac.tracks)-1])
	ac.tracks[0] = pos
}

func (w *World) AltitudeAssigned(callsign string, altitude int) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	ac.flightPlan.altitude = altitude
}

func (w *World) TemporaryAltitudeAssigned(callsign string, altitude int) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	ac.tempAltitude = altitude
}

func (w *World) VoiceSet(callsign string, vc VoiceCapability) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	ac.voiceCapability = vc
}

func (w *World) ScratchpadSet(callsign string, contents string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	ac.scratchpad = contents
}

func (w *World) TrackInitiated(callsign string, controller string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	if ac.trackingController != "" {
		lg.Printf("%s: %s is tracking controller but %s initiated track?", callsign,
			ac.trackingController, controller)
	}
	ac.trackingController = controller
}

func (w *World) TrackDropped(callsign string, controller string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	if ac.trackingController != controller {
		// This fires a bunch, with trackingController == ""...
		//lg.Printf("%s: %s dropped track but tracking controller is %s?",
		//callsign, controller, ac.trackingController)
	}
	ac.trackingController = ""
}

func (w *World) PointOutReceived(callsign string, fromController string, toController string) {
	lg.Printf("%s: point out from %s to %s", callsign, fromController, toController)

	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	if toController == w.user.callsign {
		globalConfig.AudioSettings.HandleEvent(AudioEventPointOut)
	}

	// TODO
}

func (w *World) HandoffRequested(callsign string, from string, to string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	if ac.trackingController != from {
		// But allow it anyway...
		lg.Printf("%s offering h/o of %s to %s but doesn't control the a/c?",
			to, callsign, from)
		ac.trackingController = from
	}
	if ac.hoController != "" {
		lg.Printf("%s: handoff has already been offered to %s", callsign, ac.hoController)
	}

	ac.hoController = to

	if to == w.user.callsign {
		globalConfig.AudioSettings.HandleEvent(AudioEventHandoffRequest)
	}
}

// Note that this is only called when other controllers accept handoffs
func (w *World) HandoffAccepted(callsign string, from string, to string) {
	ac, created := w.GetOrCreateAircraft(callsign)
	if created {
		w.changes.addedAircraft[ac] = ac
	} else {
		w.changes.modifiedAircraft[ac] = ac
	}

	if ac.hoController != from {
		// This fires a lot, generally with hoController == ""
		//lg.Printf("%s: %s is recorded as h/o controller but it was accepted by %s from %s",
		// callsign, ac.hoController, to, from)
	}

	if ac.hoController == w.user.callsign {
		// from user
		globalConfig.AudioSettings.HandleEvent(AudioEventHandoffAccepted)
	}

	ac.trackingController = to
	ac.hoController = ""
}

func (w *World) HandoffRejected(callsign string, fromController string, toController string) {
	// TODO
	globalConfig.AudioSettings.HandleEvent(AudioEventHandoffRejected)
}

func (w *World) TextMessageReceived(sender string, m TextMessage) {
	switch m.messageType {
	case TextBroadcast:
		w.changes.messages = append(w.changes.messages, m)

	case TextWallop:
		// Ignore

	case TextATC:
		w.changes.messages = append(w.changes.messages, m)

	case TextFrequency:
		// TODO: allow monitoring multiple frequencies
		if w.user.position != nil && w.user.position.frequency == m.frequency {
			w.changes.messages = append(w.changes.messages, m)
		}

	case TextPrivate:
		w.changes.messages = append(w.changes.messages, m)
	}
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

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) SetSquawkAutomatic(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if ac.flightPlan.rules != IFR {
		return errors.New("non-IFR squawk codes must be set manually")
	} else if w.user.position == nil {
		return errors.New("Must be signed in to a control position")
	} else {
		pos := w.user.position
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

				w.changes.modifiedAircraft[ac] = ac
				return nil
			}
		}
		return fmt.Errorf("No free squawk codes between %s and %s(!)",
			pos.lowSquawk, pos.highSquawk)
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

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) SetRoute(callsign string, route string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.flightPlan.route = route
		w.server.SetRoute(callsign, route)

		w.changes.modifiedAircraft[ac] = ac
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

		w.changes.modifiedAircraft[ac] = ac
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

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) SetAltitude(callsign string, altitude int) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.flightPlan.altitude = altitude
		w.server.SetAltitude(callsign, altitude)

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) SetTemporaryAltitude(callsign string, altitude int) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.tempAltitude = altitude
		w.server.SetTemporaryAltitude(callsign, altitude)

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) SetAircraftType(callsign string, actype string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.flightPlan.actype = actype
		w.server.SetAircraftType(callsign, actype)

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) SetFlightRules(callsign string, r FlightRules) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		ac.flightPlan.rules = r
		w.server.SetFlightRules(callsign, r)

		w.changes.modifiedAircraft[ac] = ac
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
		if ac.trackingController != "" {
			return ErrOtherControllerHasTrack
		}
		ac.trackingController = w.user.callsign
		w.server.InitiateTrack(callsign)

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) Handoff(callsign string, controller string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		if ac.trackingController != w.user.callsign {
			return ErrNotTrackedByMe
		}
		if ac.hoController != "" {
			// TODO: do we need to cancel handoff request first?
		}
		ac.hoController = controller
		w.server.Handoff(callsign, controller)

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) AcceptHandoff(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		if ac.hoController != w.user.callsign {
			return ErrNotBeingHandedOffToMe
		}
		ac.hoController = ""
		ac.trackingController = w.user.callsign
		w.server.AcceptHandoff(callsign)

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) RejectHandoff(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		if ac.hoController != w.user.callsign {
			return ErrNotBeingHandedOffToMe
		}
		ac.hoController = ""
		w.server.RejectHandoff(callsign)

		w.changes.modifiedAircraft[ac] = ac
		return nil
	}
}

func (w *World) DropTrack(callsign string) error {
	if ac := w.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else {
		if ac.trackingController != w.user.callsign {
			return ErrNotTrackedByMe
		}
		ac.trackingController = ""
		w.server.DropTrack(callsign)

		w.changes.modifiedAircraft[ac] = ac
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
	// TODO
	w.server.SendTextMessage(m)
	return nil
}
