// database.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmp/earcut-go"
	"github.com/mmp/imgui-go/v4"
	"github.com/mmp/sct2"
)

// StaticDatabase is a catch-all for data about the world that doesn't
// change after it's loaded.  It includes information from FAA databases,
// the sector file, and the position file.
type StaticDatabase struct {
	// From the FAA (et al.) databases
	FAA struct {
		navaids  map[string]Navaid
		airports map[string]Airport
		fixes    map[string]Fix
		prd      map[AirportPair][]PRDEntry
	}
	callsigns           map[string]Callsign
	AircraftTypes       map[string]AircraftType
	AircraftTypeAliases map[string]string

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

	sectorFileColors map[string]RGB

	sectorFileLoadError error

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
	geosNoColor                       []StaticDrawable
	SIDs, STARs                       []StaticDrawable
	SIDsNoColor, STARsNoColor         []StaticDrawable
	lowAirwayLabels, highAirwayLabels []Label
	labelColorBufferIndex             ColorBufferIndex
	labels                            []Label

	// From the position file
	positions             map[string][]Position // map key is e.g. JFK_TWR
	positionFileLoadError error
}

// Label represents a labeled point on a map.
type Label struct {
	name  string
	p     Point2LL
	color RGB
}

// StaticDrawable represents a fixed object (e.g., a region of the map, a
// SID or STAR, etc.) that can be drawn.
type StaticDrawable struct {
	name     string
	cb       CommandBuffer
	rgbSlice []float32
	// Bounding box in latitude-longitude coordinates
	bounds           Extent2D
	colorBufferIndex ColorBufferIndex
}

// ColorBufferIndex provides an efficient encoding of named sections of an
// RGB buffer used for rendering. More to the point, sector files include
// various objects that defined by lines where each line has a string
// associated with it that names it for color assignment during rendering
// (e.g., "RWY", "TAXI", etc.). We don't want to store a string for each
// line segment, we would like to maintain a pre-generated buffer of RGB
// values for these segments for the GPU, and we would like to be able to
// efficiently update these RGBs when the user assigns a new color for
// "TAXI" or what have you.  Thence, ColorBufferIndex.
type ColorBufferIndex struct {
	// Each color name has an integer identifier associated with it.
	m map[string]int
	// And then each line segment has an integer associated with it that
	// corresponds to its original color name.
	ids []int
}

func NewColorBufferIndex() ColorBufferIndex { return ColorBufferIndex{m: make(map[string]int)} }

// Add should be called whenever a color name is encountered when
// processing lines in a sector file. It handles the housekeeping needed to
// associate the name with the current line segment.
func (c *ColorBufferIndex) Add(name string) {
	if id, ok := c.m[name]; ok {
		// Seen it already; associate the name with the current line
		// segment.
		c.ids = append(c.ids, id)
	} else {
		// First time: generate an identifier for the name, record it, and
		// associate it with the line segment.
		newId := len(c.m) + 1
		c.m[name] = newId
		c.ids = append(c.ids, newId)
	}
}

// Visit should be called when a named color has been changed; the provided
// callback function is then called with the segment index of each line
// segment that was associated with the specified name.
func (c *ColorBufferIndex) Visit(name string, callback func(int)) {
	if matchId, ok := c.m[name]; !ok {
		// This name isn't associated with any line segments.
		return
	} else {
		// Loop over all of the segments and then call the callback for
		// each one that is associated with |name|.
		for i, id := range c.ids {
			if id == matchId {
				callback(i)
			}
		}
	}
}

var (
	//go:embed resources/ZNY_Combined_VRC.sct2.zst
	sectorFile string
	//go:embed resources/ZNY.pof.zst
	positionFile string
)

func InitializeStaticDatabase(dbChan chan *StaticDatabase) {
	start := time.Now()

	db := &StaticDatabase{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { db.FAA.navaids = parseNavaids(); wg.Done() }()
	wg.Add(1)
	go func() { db.FAA.airports = parseAirports(); wg.Done() }()
	wg.Add(1)
	go func() { db.FAA.fixes = parseFixes(); wg.Done() }()
	wg.Add(1)
	go func() { db.FAA.prd = parsePRD(); wg.Done() }()
	wg.Add(1)
	go func() { db.callsigns = parseCallsigns(); wg.Done() }()
	wg.Add(1)
	go func() { db.AircraftTypes, db.AircraftTypeAliases = parseAircraftTypes(); wg.Done() }()
	wg.Wait()

	lg.Printf("Parsed built-in databases in %v", time.Since(start))

	// These errors will appear the first time vice is launched and the
	// user hasn't yet set these up.  (And also if the chosen files are
	// moved or deleted, etc...)
	if db.LoadSectorFile("zny.sct2") != nil {
		uiAddError("Unable to load sector file. Please specify a new one using Settings/Files...",
			func() bool { return db.sectorFileLoadError == nil })
	}
	if db.LoadPositionFile("zny.pof") != nil {
		uiAddError("Unable to load position file. Please specify a new one using Settings/Files...",
			func() bool { return db.positionFileLoadError == nil })
	}

	dbChan <- db
}

///////////////////////////////////////////////////////////////////////////
// FAA databases

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

	//go:embed resources/aircraft.json
	aircraftTypesRaw string
)

// Utility function for parsing CSV files as strings; it breaks each line
// of the file into fields and calls the provided callback function for
// each one.
func mungeCSV(filename string, raw string, callback func([]string)) {
	r := bytes.NewReader([]byte(raw))
	cr := csv.NewReader(r)
	cr.ReuseRecord = true

	// Skip the first line with the legend
	if _, err := cr.Read(); err != nil {
		lg.Errorf("%s: error parsing CSV file: %s", filename, err)
		return
	}

	for {
		if record, err := cr.Read(); err == io.EOF {
			return
		} else if err != nil {
			lg.Errorf("%s: error parsing CSV file: %s", filename, err)
			return
		} else {
			callback(record)
		}
	}
}

// lat and long should be 4-long slices, e.g.: [42 7 12.68 N]
func point2LLFromComponents(lat []string, long []string) Point2LL {
	latitude := atof(lat[0]) + atof(lat[1])/60. + atof(lat[2])/3600.
	if lat[3] == "S" {
		latitude = -latitude
	}
	longitude := atof(long[0]) + atof(long[1])/60. + atof(long[2])/3600.
	if long[3] == "W" {
		longitude = -longitude
	}

	return Point2LL{float32(longitude), float32(latitude)}
}

func point2LLFromStrings(lat, long string) Point2LL {
	return Point2LL{float32(atof(long)), float32(atof(lat))}
}

func parseNavaids() map[string]Navaid {
	navaids := make(map[string]Navaid)

	mungeCSV("navaids", decompressZstd(navBaseRaw), func(s []string) {
		n := Navaid{Id: s[1], Type: s[2], Name: s[7],
			Location: point2LLFromComponents(s[22:26], s[26:30])}
		if n.Id != "" {
			navaids[n.Id] = n
		}
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
			loc := point2LLFromComponents(s[15:19], s[19:23])
			ap := Airport{Id: s[98], Name: s[12], Location: loc, Elevation: int(elevation)}
			if ap.Id == "" {
				ap.Id = s[4] // No ICAO code so grab the FAA airport id
			}
			if ap.Id != "" {
				airports[ap.Id] = ap
			}
		}
	})

	// Global database; this isn't in CSV, so we need to parse it manually.
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

			ap := Airport{
				Id:        f[0],
				Name:      f[2],
				Location:  point2LLFromStrings(f[14], f[15]),
				Elevation: int(elevation)}
			if ap.Id != "" {
				airports[ap.Id] = ap
			}
		}
	}

	return airports
}

func parseFixes() map[string]Fix {
	fixes := make(map[string]Fix)

	mungeCSV("fixes", decompressZstd(fixesRaw), func(s []string) {
		f := Fix{
			Id:       s[1],
			Location: point2LLFromComponents(s[5:9], s[9:13])}
		if f.Id != "" {
			fixes[f.Id] = f
		}
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
		if entry.Depart != "" && entry.Arrive != "" {
			prd[AirportPair{entry.Depart, entry.Arrive}] =
				append(prd[AirportPair{entry.Depart, entry.Arrive}], entry)
		}
	})

	return prd
}

func parseCallsigns() map[string]Callsign {
	callsigns := make(map[string]Callsign)

	addCallsign := func(s []string) {
		fix := func(s string) string { return stopShouting(strings.TrimSpace(s)) }

		cs := Callsign{
			Company:     fix(s[0]),
			Country:     fix(s[1]),
			Telephony:   fix(s[2]),
			ThreeLetter: strings.TrimSpace(s[3])}
		if cs.ThreeLetter != "" && cs.ThreeLetter != "..." {
			callsigns[cs.ThreeLetter] = cs
		}
	}

	mungeCSV("callsigns", decompressZstd(callsignsRaw), addCallsign)
	// Do virtual second since we let them take precedence
	mungeCSV("virtual callsigns", decompressZstd(virtualCallsignsRaw), addCallsign)

	return callsigns
}

func parseAircraftTypes() (map[string]AircraftType, map[string]string) {
	var ac struct {
		AircraftTypes       map[string]AircraftType `json:"Aircraft"`
		AircraftTypeAliases map[string]string       `json:"Aliases"`
	}
	ac.AircraftTypes = make(map[string]AircraftType)
	ac.AircraftTypeAliases = make(map[string]string)

	if err := json.Unmarshal([]byte(aircraftTypesRaw), &ac); err != nil {
		lg.Errorf("%v", err)
	}

	return ac.AircraftTypes, ac.AircraftTypeAliases
}

///////////////////////////////////////////////////////////////////////////
// The Sector File

func Point2LLFromSct2(ll sct2.LatLong) Point2LL {
	return Point2LL{float32(ll.Longitude), float32(ll.Latitude)}
}

func Point2LLFromLL64(latitude, longitude float64) Point2LL {
	return Point2LL{float32(longitude), float32(latitude)}
}

func (db *StaticDatabase) LoadSectorFile(filename string) error {
	lg.Printf("%s: loading sector file", filename)
	sectorFile, err := parseSectorFile(filename)
	db.sectorFileLoadError = err
	if err != nil {
		return err
	}

	// Copy over some basic stuff from the sector file
	db.defaultAirport = strings.TrimSpace(sectorFile.DefaultAirport) // TODO: sct2 should do this
	db.defaultCenter = Point2LLFromSct2(sectorFile.Center)
	db.NmPerLatitude = float32(sectorFile.NmPerLatitude)
	db.NmPerLongitude = float32(sectorFile.NmPerLongitude)
	db.MagneticVariation = float32(sectorFile.MagneticVariation)
	db.sectorFileId = sectorFile.Id

	// Clear out everything that is set from the sector file contents in
	// case we're loading a new one.
	db.VORs = make(map[string]Point2LL)
	db.NDBs = make(map[string]Point2LL)
	db.fixes = make(map[string]Point2LL)
	db.airports = make(map[string]Point2LL)
	db.runways = make(map[string][]Runway)
	db.ARTCC = nil
	db.ARTCCLow = nil
	db.ARTCCHigh = nil

	// Also clear out all of the things to draw that are derived from the
	// sector file.
	db.runwayCommandBuffer = CommandBuffer{}
	db.geos = nil
	db.geosNoColor = nil
	db.regions = nil
	db.SIDs = nil
	db.SIDsNoColor = nil
	db.STARs = nil
	db.STARsNoColor = nil
	db.lowAirwayLabels = nil
	db.highAirwayLabels = nil
	db.lowAirwayCommandBuffer = CommandBuffer{}
	db.highAirwayCommandBuffer = CommandBuffer{}
	db.labelColorBufferIndex = ColorBufferIndex{}
	db.labels = nil

	// First initialize various databases from the sector file. Start with
	// named locations--VORs, airports, etc.
	loc := func(ls []sct2.NamedLatLong) map[string]Point2LL {
		m := make(map[string]Point2LL)
		for _, loc := range ls {
			if loc.Name != "" {
				m[loc.Name] = Point2LLFromLL64(loc.Latitude, loc.Longitude)
			}
		}
		return m
	}
	db.VORs = loc(sectorFile.VORs)
	db.NDBs = loc(sectorFile.NDBs)
	db.fixes = loc(sectorFile.Fixes)
	db.airports = loc(sectorFile.Airports)

	// Initialize the runways map, which is from airports to a slice of all
	// of their runways.
	db.runways = make(map[string][]Runway)
	for _, r := range sectorFile.Runways {
		if r.Airport == "" {
			continue
		}
		// Two entries--one for each end of the runway.
		for i := 0; i < 2; i++ {
			db.runways[r.Airport] = append(db.runways[r.Airport],
				Runway{
					Number:    r.Number[i],
					Heading:   float32(r.Heading[i]),
					Threshold: Point2LLFromSct2(r.P[i]),
					End:       Point2LLFromSct2(r.P[i^1])})
		}
	}

	// Now initialize assorted StaticDrawables for things in the sector
	// file.
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
	db.ARTCC = setupARTCC(sectorFile.ARTCC)
	db.ARTCCLow = setupARTCC(sectorFile.ARTCCLow)
	db.ARTCCHigh = setupARTCC(sectorFile.ARTCCHigh)

	// Regions are more complicated, both because they are general polygons
	// that we need to triangulate but also because we merge adjacent
	// regions with the same name (as happens often in sector files) into a
	// single StaticDrawable; doing so reduces the number of draw calls
	// issued when rendering, which improves performance.
	//
	// (Note that we generally do not want to merge all regions with the
	// same name into a single StaticDrawable; in that case we might have
	// all of the runways for many airports all together, which would
	// reduce the effectiveness of culling; this way tends to e.g., collect
	// the runways for a single airport, given how sector files usually
	// seem to be organized.)
	currentRegionName := "___UNSET___"
	td := TrianglesDrawBuilder{}
	for i, r := range sectorFile.Regions {
		if len(r.P) == 0 {
			lg.Printf("zero vertices in region \"%s\"?", r.Name)
			continue
		}

		if r.Name != currentRegionName {
			// We've come across a new region name; flush out any accumulated triangles
			// from the previous region
			if len(td.indices) > 0 {
				region := StaticDrawable{name: currentRegionName, bounds: td.Bounds()}
				td.GenerateCommands(&region.cb)
				db.regions = append(db.regions, region)
				td.Reset()
			}
			currentRegionName = r.Name
		}

		// Triangulate
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

		// And when we're at the last region, also flush out its
		// StaticDrawable.
		if i+1 == len(sectorFile.Regions) && len(td.indices) > 0 {
			region := StaticDrawable{name: r.Name, bounds: td.Bounds()}
			td.GenerateCommands(&region.cb)
			db.regions = append(db.regions, region)
		}
	}

	// Runway lines
	rld := LinesDrawBuilder{}
	for _, runway := range sectorFile.Runways {
		if runway.P[0].Latitude != 0 || runway.P[0].Longitude != 0 {
			rld.AddLine(Point2LLFromSct2(runway.P[0]), Point2LLFromSct2(runway.P[1]))
		}
	}
	rld.GenerateCommands(&db.runwayCommandBuffer)

	// Labels (e.g., taxiways and runways.)
	db.labelColorBufferIndex = NewColorBufferIndex()
	for _, label := range sectorFile.Labels {
		l := Label{name: label.Name, p: Point2LLFromSct2(label.P)}
		db.labelColorBufferIndex.Add(label.Color)
		db.labels = append(db.labels, l)
	}
	db.lowAirwayCommandBuffer, db.lowAirwayLabels = getAirwayCommandBuffers(sectorFile.LowAirways)
	db.highAirwayCommandBuffer, db.highAirwayLabels = getAirwayCommandBuffers(sectorFile.HighAirways)

	// Various things are represented by colored line segments where their
	// color is either given by a color that is #defined in the sector file
	// or by their general type ("SID"). staticColoredLines handles the
	// details of creating a StaticDrawable for such things, including the
	// ColorBufferIndex initialization so that the actual colors used for
	// drawing them can be changed by the user.
	staticColoredLines := func(name string, cs []sct2.ColoredSegment, defaultColorName string) StaticDrawable {
		ld := GetColoredLinesDrawBuilder()
		defer ReturnColoredLinesDrawBuilder(ld)
		colorBufferIndex := NewColorBufferIndex()

		for _, seg := range cs {
			// Ignore (0,0) positions, which are sometimes left in sector
			// files to delineate different sections. They are obviously
			// unhelpful as far as the bounding boxes for culling...
			if seg.P[0].Latitude != 0 || seg.P[0].Longitude != 0 {
				if seg.Color != "" {
					colorBufferIndex.Add(seg.Color)
				} else {
					colorBufferIndex.Add(defaultColorName)
				}
				ld.AddLine(Point2LLFromSct2(seg.P[0]), Point2LLFromSct2(seg.P[1]), RGB{})
			}
		}
		cb := CommandBuffer{}
		start, len := ld.GenerateCommands(&cb)

		return StaticDrawable{
			name:             name,
			cb:               cb,
			rgbSlice:         cb.FloatSlice(start, len),
			bounds:           ld.Bounds(),
			colorBufferIndex: colorBufferIndex}
	}
	// We'll also make a StaticDrawable for such things that doesn't
	// include any color settings, for the use of scopes that prefer to set
	// the color for these things themselves.
	staticLines := func(name string, cs []sct2.ColoredSegment) StaticDrawable {
		ld := GetLinesDrawBuilder()
		defer ReturnLinesDrawBuilder(ld)

		for _, seg := range cs {
			if seg.P[0].Latitude != 0 || seg.P[0].Longitude != 0 {
				ld.AddLine(Point2LLFromSct2(seg.P[0]), Point2LLFromSct2(seg.P[1]))
			}
		}
		cb := CommandBuffer{}
		ld.GenerateCommands(&cb)

		return StaticDrawable{
			name:   name,
			cb:     cb,
			bounds: ld.Bounds()}
	}

	for _, sid := range sectorFile.SIDs {
		db.SIDs = append(db.SIDs, staticColoredLines(sid.Name, sid.Segs, "SID"))
		db.SIDsNoColor = append(db.SIDsNoColor, staticLines(sid.Name, sid.Segs))
	}
	for _, star := range sectorFile.STARs {
		db.STARs = append(db.STARs, staticColoredLines(star.Name, star.Segs, "STAR"))
		db.STARsNoColor = append(db.STARsNoColor, staticLines(star.Name, star.Segs))
	}
	for _, geo := range sectorFile.Geo {
		db.geos = append(db.geos, staticColoredLines(geo.Name, geo.Segments, "Geo"))
		db.geosNoColor = append(db.geosNoColor, staticLines(geo.Name, geo.Segments))
	}

	// Record all of the names of colors set via #define statements in the
	// sector file so that the use is able to redefine them.
	db.sectorFileColors = make(map[string]RGB)
	for _, color := range sectorFile.Colors {
		db.sectorFileColors[color.Name] = RGB{R: color.R, G: color.G, B: color.B}
	}

	lg.Printf("%s: finished loading sector file", filename)

	return nil
}

func parseSectorFile(sectorFilename string) (*sct2.SectorFile, error) {
	contents := decompressZstd(sectorFile)

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
		sf, err = sct2.Parse([]byte(contents), sectorFilename, errorCallback)
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

func getAirwayCommandBuffers(airways []sct2.Airway) (CommandBuffer, []Label) {
	// Airways are tricky since the sector file will have the same segment
	// multiple times when multiple airways are coincident. We'd like to
	// have a single label for each such segment that includes all of the
	// airways names.
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

			// Has it already been labeled for this segment? (It shouldn't
			// be, but...)
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
				} else {
					m[seg] = append(labels, a.Name)
				}
			} else {
				m[seg] = []string{a.Name}
			}
		}
	}

	// Now get working on the command buffer.
	lines := LinesDrawBuilder{}
	var labels []Label
	for seg, l := range m {
		// Join the airway names with slashes and draw them at the midpoint
		// of each airway segment.
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
// The Position File

func (db *StaticDatabase) LoadPositionFile(filename string) error {
	lg.Printf("%s: loading position file", filename)

	db.positions, db.positionFileLoadError = parsePositionFile(filename)

	lg.Printf("%s: finished loading position file", filename)

	return db.positionFileLoadError
}

func parsePositionFile(filename string) (map[string][]Position, error) {
	m := make(map[string][]Position)
	contents := decompressZstd(positionFile)

	scan := bufio.NewScanner(bytes.NewReader([]byte(contents)))
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
			Name:      fields[0],
			Callsign:  fields[1],
			Frequency: NewFrequency(float32(frequency)),
			SectorId:  fields[3],
			Scope:     fields[4],
			Id:        id,
			// ignore fields 7/8
			LowSquawk:  Squawk(lowSquawk),
			HighSquawk: Squawk(highSquawk)}

		m[id] = append(m[id], p)
	}
	return m, nil
}

///////////////////////////////////////////////////////////////////////////
// Utility methods

// Locate returns the location of a (static) named thing, if we've heard of it.
func (db *StaticDatabase) Locate(name string) (Point2LL, bool) {
	name = strings.ToUpper(name)
	// We'll start with the sector file and then move on to the FAA
	// database if we don't find it.
	if pos, ok := db.VORs[name]; ok {
		return pos, ok
	} else if pos, ok := db.NDBs[name]; ok {
		return pos, ok
	} else if pos, ok := db.fixes[name]; ok {
		return pos, ok
	} else if pos, ok := db.airports[name]; ok {
		return pos, ok
	} else if n, ok := db.FAA.navaids[name]; ok {
		return n.Location, ok
	} else if f, ok := db.FAA.fixes[name]; ok {
		return f.Location, ok
	} else if ap, ok := db.FAA.airports[name]; ok {
		return ap.Location, ok
	} else {
		return Point2LL{}, false
	}
}

func (db *StaticDatabase) LookupPosition(callsign string, frequency Frequency) *Position {
	// compute the basic callsign: e.g. NY_1_CTR -> NY_CTR, PHL_ND_APP -> PHL_APP
	cf := strings.Split(callsign, "_")
	if len(cf) > 2 {
		callsign = cf[0] + "_" + cf[len(cf)-1]
	}

	for i, pos := range db.positions[callsign] {
		if pos.Frequency == frequency {
			return &db.positions[callsign][i]
		}
	}
	return nil
}

func (db *StaticDatabase) LookupAircraftType(ac string) (AircraftType, bool) {
	if t, ok := db.AircraftTypes[ac]; ok {
		return t, true
	}
	if ac, ok := db.AircraftTypeAliases[ac]; ok {
		t, ok := db.AircraftTypes[ac]
		if !ok {
			lg.Errorf("%s: alias not found in aircraft types database", ac)
		}
		return t, ok
	}
	return AircraftType{}, false
}

func (db *StaticDatabase) SetColorScheme(cs *ColorScheme) {
	// Set the sector file colors by default; they may be overridden
	// shortly, but no need to be more clever here.
	for name, color := range db.sectorFileColors {
		db.NamedColorChanged(name, color)
	}
	for name, color := range cs.DefinedColors {
		db.NamedColorChanged(name, *color)
	}
	db.NamedColorChanged("Geo", cs.Geo)
	db.NamedColorChanged("SID", cs.SID)
	db.NamedColorChanged("STAR", cs.STAR)
}

// NamedColorChanged should be called when a color in the color scheme has
// been updated; it takes care of updating all of the RGB buffers in the
// assorted rendering command buffers to reflect the change.
func (db *StaticDatabase) NamedColorChanged(name string, rgb RGB) {
	update := func(c ColorBufferIndex, slice []float32, name string, rgb RGB) {
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
		for _, geo := range db.geos {
			update(geo.colorBufferIndex, geo.rgbSlice, "Geo", rgb)
		}

	case "SID":
		for _, sid := range db.SIDs {
			update(sid.colorBufferIndex, sid.rgbSlice, "SID", rgb)
		}

	case "STAR":
		for _, star := range db.STARs {
			update(star.colorBufferIndex, star.rgbSlice, "STAR", rgb)
		}

	default:
		db.labelColorBufferIndex.Visit(name, func(i int) {
			db.labels[i].color = rgb
		})

		for _, geo := range db.geos {
			update(geo.colorBufferIndex, geo.rgbSlice, name, rgb)
		}
		for _, sid := range db.SIDs {
			update(sid.colorBufferIndex, sid.rgbSlice, name, rgb)
		}
		for _, star := range db.STARs {
			update(star.colorBufferIndex, star.rgbSlice, name, rgb)
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// StaticDrawConfig

// StaticDrawConfig tracks a subset of all of the drawable items that are
// available in the StaticDatabase that are to be drawn for some purpose;
// generally, each radar scope will maintain one or more of these, possibly
// selecting among them in different situations.
type StaticDrawConfig struct {
	// For development and performance tests--draw absolutely everything.
	DrawEverything bool

	DrawRunways     bool
	DrawRegions     bool
	DrawLabels      bool
	DrawLowAirways  bool
	DrawHighAirways bool

	// For each of VORs, NDBs, fixes, and airports, the user can both
	// request that all of them be drawn or can select a subset of them to
	// be drawn.  Further, text for their names is drawn optionally.
	DrawVORs     bool
	DrawVORNames bool
	VORsToDraw   map[string]interface{}

	DrawNDBs     bool
	DrawNDBNames bool
	NDBsToDraw   map[string]interface{}

	DrawFixes    bool
	DrawFixNames bool
	FixesToDraw  map[string]interface{}

	DrawAirports     bool
	DrawAirportNames bool
	AirportsToDraw   map[string]interface{}

	// Geo, SIDs, STARs, and ARTCCs are individually selected from the ones
	// listed in the sector file.
	GeoDrawSet       map[string]interface{}
	SIDDrawSet       map[string]interface{}
	STARDrawSet      map[string]interface{}
	ARTCCDrawSet     map[string]interface{}
	ARTCCLowDrawSet  map[string]interface{}
	ARTCCHighDrawSet map[string]interface{}

	// Various persistent state used in the ui but not maintained across
	// sessions.
	vorsComboState, ndbsComboState      *ComboBoxState
	fixesComboState, airportsComboState *ComboBoxState
}

func NewStaticDrawConfig() *StaticDrawConfig {
	s := &StaticDrawConfig{}

	s.DrawRegions = true
	s.DrawLabels = true

	s.VORsToDraw = make(map[string]interface{})
	s.NDBsToDraw = make(map[string]interface{})
	s.FixesToDraw = make(map[string]interface{})
	s.AirportsToDraw = make(map[string]interface{})
	s.GeoDrawSet = make(map[string]interface{})
	s.SIDDrawSet = make(map[string]interface{})
	s.STARDrawSet = make(map[string]interface{})
	s.ARTCCDrawSet = make(map[string]interface{})
	s.ARTCCLowDrawSet = make(map[string]interface{})
	s.ARTCCHighDrawSet = make(map[string]interface{})

	s.vorsComboState = NewComboBoxState(1)
	s.ndbsComboState = NewComboBoxState(1)
	s.fixesComboState = NewComboBoxState(1)
	s.airportsComboState = NewComboBoxState(1)

	return s
}

func (s *StaticDrawConfig) Duplicate() *StaticDrawConfig {
	dupe := &StaticDrawConfig{}
	// Copy everything over for starters, but then make copies of things
	// that shouldn't be shared.
	*dupe = *s

	dupe.VORsToDraw = DuplicateMap(s.VORsToDraw)
	dupe.NDBsToDraw = DuplicateMap(s.NDBsToDraw)
	dupe.FixesToDraw = DuplicateMap(s.FixesToDraw)
	dupe.AirportsToDraw = DuplicateMap(s.AirportsToDraw)
	dupe.GeoDrawSet = DuplicateMap(s.GeoDrawSet)
	dupe.SIDDrawSet = DuplicateMap(s.SIDDrawSet)
	dupe.STARDrawSet = DuplicateMap(s.STARDrawSet)
	dupe.ARTCCDrawSet = DuplicateMap(s.ARTCCDrawSet)
	dupe.ARTCCLowDrawSet = DuplicateMap(s.ARTCCLowDrawSet)
	dupe.ARTCCHighDrawSet = DuplicateMap(s.ARTCCHighDrawSet)

	dupe.vorsComboState = NewComboBoxState(1)
	dupe.ndbsComboState = NewComboBoxState(1)
	dupe.fixesComboState = NewComboBoxState(1)
	dupe.airportsComboState = NewComboBoxState(1)

	return dupe
}

// Activate should be called before the either of the Draw or DrawUI
// methods is called to make sure that assorted internal data structures
// are initialized.
func (s *StaticDrawConfig) Activate() {
	if s.GeoDrawSet == nil {
		s.GeoDrawSet = make(map[string]interface{})
	}
	if s.VORsToDraw == nil {
		s.VORsToDraw = make(map[string]interface{})
	}
	if s.NDBsToDraw == nil {
		s.NDBsToDraw = make(map[string]interface{})
	}
	if s.FixesToDraw == nil {
		s.FixesToDraw = make(map[string]interface{})
	}
	if s.AirportsToDraw == nil {
		s.AirportsToDraw = make(map[string]interface{})
	}
	if s.vorsComboState == nil {
		s.vorsComboState = NewComboBoxState(1)
	}
	if s.ndbsComboState == nil {
		s.ndbsComboState = NewComboBoxState(1)
	}
	if s.fixesComboState == nil {
		s.fixesComboState = NewComboBoxState(1)
	}
	if s.airportsComboState == nil {
		s.airportsComboState = NewComboBoxState(1)
	}
}

func (s *StaticDrawConfig) Deactivate() {
}

// DrawUI draws a user interface that makes it possible to select which
// items to draw with the StaticDrawConfig.
func (s *StaticDrawConfig) DrawUI() {
	imgui.PushID(fmt.Sprintf("%p", s))

	if *devmode || s.DrawEverything {
		imgui.Checkbox("Draw everything", &s.DrawEverything)
	}

	if imgui.BeginTable("drawbuttons", 5) {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Checkbox("Regions", &s.DrawRegions)
		imgui.TableNextColumn()
		imgui.Checkbox("Labels", &s.DrawLabels)
		imgui.TableNextColumn()
		imgui.Checkbox("Low Airways", &s.DrawLowAirways)
		imgui.TableNextColumn()
		imgui.Checkbox("High Airways", &s.DrawHighAirways)
		imgui.TableNextColumn()
		imgui.Checkbox("Runways", &s.DrawRunways)
		imgui.EndTable()
	}

	if imgui.BeginTable("voretal", 4) {
		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Text("VORs")
		imgui.TableNextColumn()
		imgui.Text("NDBs")
		imgui.TableNextColumn()
		imgui.Text("Fixes")
		imgui.TableNextColumn()
		imgui.Text("Airports")

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Checkbox("Draw All##VORs", &s.DrawVORs)
		imgui.SameLine()
		imgui.Checkbox("Show Names##VORs", &s.DrawVORNames)
		imgui.TableNextColumn()
		imgui.Checkbox("Draw All##NDBs", &s.DrawNDBs)
		imgui.SameLine()
		imgui.Checkbox("Show Names##NDBs", &s.DrawNDBNames)
		imgui.TableNextColumn()
		imgui.Checkbox("Draw All##Fixes", &s.DrawFixes)
		imgui.SameLine()
		imgui.Checkbox("Show Names##Fixes", &s.DrawFixNames)
		imgui.TableNextColumn()
		imgui.Checkbox("Draw All##Airports", &s.DrawAirports)
		imgui.SameLine()
		imgui.Checkbox("Show Names##Airports", &s.DrawAirportNames)

		// Allow VORs, NDBs, fixes, and airports to be selected individually.
		imgui.TableNextRow()
		imgui.TableNextColumn()
		config := ComboBoxDisplayConfig{
			ColumnHeaders:    []string{"##name"},
			DrawHeaders:      false,
			SelectAllColumns: true,
			EntryNames:       []string{"##name"},
			InputFlags:       []imgui.InputTextFlags{imgui.InputTextFlagsCharsUppercase},
			FixedDisplayed:   8,
		}
		DrawComboBox(s.vorsComboState, config, SortedMapKeys(s.VORsToDraw), nil,
			/* valid */ func(entries []*string) bool {
				e := *entries[0]
				_, ok := database.VORs[e]
				return e != "" && ok
			},
			/* add */ func(entries []*string) {
				s.VORsToDraw[*entries[0]] = nil
			},
			/* delete */ func(selected map[string]interface{}) {
				for k := range selected {
					delete(s.VORsToDraw, k)
				}
			})

		imgui.TableNextColumn()
		DrawComboBox(s.ndbsComboState, config, SortedMapKeys(s.NDBsToDraw), nil,
			/* valid */ func(entries []*string) bool {
				e := *entries[0]
				_, ok := database.NDBs[e]
				return e != "" && ok
			},
			/* add */ func(entries []*string) {
				s.NDBsToDraw[*entries[0]] = nil
			},
			/* delete */ func(selected map[string]interface{}) {
				for k := range selected {
					delete(s.NDBsToDraw, k)
				}
			})

		imgui.TableNextColumn()
		DrawComboBox(s.fixesComboState, config, SortedMapKeys(s.FixesToDraw), nil,
			/* valid */ func(entries []*string) bool {
				e := *entries[0]
				_, ok := database.fixes[e]
				return e != "" && ok
			},
			/* add */ func(entries []*string) {
				s.FixesToDraw[*entries[0]] = nil
			},
			/* delete */ func(selected map[string]interface{}) {
				for k := range selected {
					delete(s.FixesToDraw, k)
				}
			})

		imgui.TableNextColumn()
		DrawComboBox(s.airportsComboState, config, SortedMapKeys(s.AirportsToDraw), nil,
			/* valid */ func(entries []*string) bool {
				e := *entries[0]
				_, ok := database.airports[e]
				return e != "" && ok
			},
			/* add */ func(entries []*string) {
				s.AirportsToDraw[*entries[0]] = nil
			},
			/* delete */ func(selected map[string]interface{}) {
				for k := range selected {
					delete(s.AirportsToDraw, k)
				}
			})

		imgui.EndTable()
	}

	if len(database.geos) > 0 && imgui.TreeNode("Geo") {
		// Draw a check box to select each separate item in the Geo list.
		for _, geo := range database.geos {
			_, draw := s.GeoDrawSet[geo.name]
			imgui.Checkbox(geo.name, &draw)
			if draw {
				s.GeoDrawSet[geo.name] = nil
			} else {
				delete(s.GeoDrawSet, geo.name)
			}
		}
		imgui.TreePop()
	}

	// SIDs and STARS are presented hierarchically, where names that start
	// with "===" are interpreted as separators that in turn are rendered
	// using tree nodes so that the user can expand them individually.
	sidStarHierarchy := func(title string, sidstar []StaticDrawable, drawSet map[string]interface{}) {
		if imgui.TreeNode(title) {
			depth := 1
			active := true
			for _, ss := range sidstar {
				if strings.HasPrefix(ss.name, "===") {
					if active && depth > 1 {
						// We've gone into a subtree for another item, so
						// end that one before we start one for the next
						// one.
						imgui.TreePop()
						depth--
					}
					// Chop off the equals signs for the UI
					n := strings.TrimLeft(ss.name, "= ")
					n = strings.TrimRight(n, "= ")

					// And start a new subtree; increment the current depth
					// if the user has expanded it.
					active = imgui.TreeNode(n)
					if active {
						depth++
					}
				} else if active {
					// It's a regular entry; draw the checkbox for it.
					_, draw := drawSet[ss.name]
					imgui.Checkbox(ss.name, &draw)
					if draw {
						drawSet[ss.name] = nil
					} else {
						delete(drawSet, ss.name)
					}
				}
			}
			// Done; close any open subtrees.
			for depth > 0 {
				imgui.TreePop()
				depth--
			}
		}
	}
	sidStarHierarchy("SIDs", database.SIDs, s.SIDDrawSet)
	sidStarHierarchy("STARs", database.STARs, s.STARDrawSet)

	// For the ARTCCs, just present a flat list of them with checkboxes.
	artccCheckboxes := func(name string, artcc []StaticDrawable, drawSet map[string]interface{}) {
		if len(artcc) > 0 && imgui.TreeNode(name) {
			for i, a := range artcc {
				_, draw := drawSet[a.name]
				imgui.Checkbox(artcc[i].name, &draw)
				if draw {
					drawSet[a.name] = nil
				} else {
					delete(drawSet, a.name)
				}
			}
			imgui.TreePop()
		}
	}
	artccCheckboxes("ARTCC", database.ARTCC, s.ARTCCDrawSet)
	artccCheckboxes("ARTCC Low", database.ARTCCLow, s.ARTCCLowDrawSet)
	artccCheckboxes("ARTCC High", database.ARTCCHigh, s.ARTCCHighDrawSet)

	imgui.PopID()
}

// Draw draws all of the items that are selected in the StaticDrawConfig.
// If color is nil, colors are taken from the PaneContext's ColorScheme;
// otherwise it overrides all color selections.
func (s *StaticDrawConfig) Draw(ctx *PaneContext, labelFont *Font, color *RGB,
	transforms ScopeTransformations, cb *CommandBuffer) {
	width, height := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	inWindow := func(p [2]float32) bool {
		return p[0] >= 0 && p[0] < width && p[1] >= 0 && p[1] < height
	}

	// Start out with matrices set up for drawing vertices in lat-long
	// space.  (We'll switch to window coordinates for text labels toward
	// the end of this method.)
	transforms.LoadLatLongViewingMatrices(cb)

	// Compute bounds for culling; need all four corners for viewBounds due
	// to possible scope rotation...
	p0 := transforms.LatLongFromWindowP([2]float32{0, 0})
	p1 := transforms.LatLongFromWindowP([2]float32{width, 0})
	p2 := transforms.LatLongFromWindowP([2]float32{0, height})
	p3 := transforms.LatLongFromWindowP([2]float32{width, height})
	viewBounds := Extent2DFromPoints([][2]float32{p0, p1, p2, p3})

	// shrink bounds for debugging culling
	/*
		dx := .1 * (s.viewBounds.p1[0] - s.viewBounds.p0[0])
		dy := .1 * (s.viewBounds.p1[1] - s.viewBounds.p0[1])
		s.viewBounds.p0[0] += dx
		s.viewBounds.p1[0] -= dx
		s.viewBounds.p0[1] += dy
		s.viewBounds.p1[1] -= dy
	*/

	// Helper that returns the override color if provided.
	filterColor := func(c RGB) RGB {
		if color != nil {
			return *color
		}
		return c
	}
	if color != nil {
		cb.SetRGB(*color)
	}

	if s.DrawEverything || s.DrawRunways {
		// Runways are easy; there's no culling and a pregenerated command
		// buffer already available.
		cb.SetRGB(filterColor(ctx.cs.Runway))
		cb.Call(database.runwayCommandBuffer)
	}

	if s.DrawEverything || s.DrawRegions {
		for _, region := range database.regions {
			// Since regions are more work to draw (filled triangles!), we
			// cull the ones that aren't visible.  For the visible ones,
			// it's then just a matter of setting the right caller and
			// calling out to its preexisting command buffer.
			if Overlaps(region.bounds, viewBounds) {
				if region.name == "" {
					cb.SetRGB(filterColor(ctx.cs.Region))
				} else if rgb, ok := ctx.cs.DefinedColors[region.name]; ok {
					cb.SetRGB(filterColor(*rgb))
				} else if rgb, ok := database.sectorFileColors[region.name]; ok {
					cb.SetRGB(filterColor(rgb))
				} else {
					lg.Errorf("%s: defined color not found for region", region.name)
					cb.SetRGB(filterColor(RGB{0.5, 0.5, 0.5}))
				}
				cb.Call(region.cb)
			}
		}
	}

	// ARTCCs
	drawARTCCLines := func(artcc []StaticDrawable, drawSet map[string]interface{}) {
		for _, artcc := range artcc {
			if _, draw := drawSet[artcc.name]; (draw || s.DrawEverything) && Overlaps(artcc.bounds, viewBounds) {
				cb.Call(artcc.cb)
			}
		}
	}
	cb.SetRGB(filterColor(ctx.cs.ARTCC))
	drawARTCCLines(database.ARTCC, s.ARTCCDrawSet)
	drawARTCCLines(database.ARTCCLow, s.ARTCCLowDrawSet)
	drawARTCCLines(database.ARTCCHigh, s.ARTCCHighDrawSet)

	// SIDs, STARs, and Geos. These all have simple bounds checks for
	// culling before calling out to pregenerated command buffers.
	sids := database.SIDs
	if color != nil {
		sids = database.SIDsNoColor
	}
	for _, sid := range sids {
		_, draw := s.SIDDrawSet[sid.name]
		if (s.DrawEverything || draw) && Overlaps(sid.bounds, viewBounds) {
			cb.Call(sid.cb)
		}
	}
	stars := database.STARs
	if color != nil {
		stars = database.STARsNoColor
	}
	for _, star := range stars {
		_, draw := s.STARDrawSet[star.name]
		if (s.DrawEverything || draw) && Overlaps(star.bounds, viewBounds) {
			cb.Call(star.cb)
		}
	}
	geos := database.geos
	if color != nil {
		geos = database.geosNoColor
	}
	for _, geo := range geos {
		_, draw := s.GeoDrawSet[geo.name]
		if (s.DrawEverything || draw) && Overlaps(geo.bounds, viewBounds) {
			cb.Call(geo.cb)
		}
	}

	// Airways. For now just draw the lines, if requested. Labels will come
	// shortly.
	if s.DrawEverything || s.DrawLowAirways {
		cb.SetRGB(filterColor(ctx.cs.LowAirway))
		cb.Call(database.lowAirwayCommandBuffer)
	}
	if s.DrawEverything || s.DrawHighAirways {
		cb.SetRGB(filterColor(ctx.cs.HighAirway))
		cb.Call(database.highAirwayCommandBuffer)
	}

	// Everything after this is either very small (VOR markers, etc) or is
	// text, so we won't include it if we're just rendering a thumbnail.
	if ctx.thumbnail {
		return
	}

	// Now switch to window coordinates for drawing text, VORs, NDBs, fixes, and airports
	transforms.LoadWindowViewingMatrices(cb)

	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	// VORs are indicated by small squares
	VORsquare := [][2]float32{[2]float32{-2, -2}, [2]float32{2, -2}, [2]float32{2, 2}, [2]float32{-2, 2}}
	if s.DrawEverything || s.DrawVORs {
		for _, vor := range database.VORs {
			ld.AddPolyline(transforms.WindowFromLatLongP(vor), filterColor(ctx.cs.VOR), VORsquare)
		}
	} else {
		for name := range s.VORsToDraw {
			if pos, ok := database.VORs[name]; ok {
				ld.AddPolyline(transforms.WindowFromLatLongP(pos), filterColor(ctx.cs.VOR), VORsquare)
			}
		}
	}

	// NDBs are shown with down-pointing triangles
	NDBtri := EquilateralTriangleVertices(-5)
	if s.DrawEverything || s.DrawNDBs {
		for _, ndb := range database.NDBs {
			// flipped triangles
			ld.AddPolyline(transforms.WindowFromLatLongP(ndb), filterColor(ctx.cs.NDB), NDBtri[:])
		}
	} else {
		for name := range s.NDBsToDraw {
			if pos, ok := database.NDBs[name]; ok {
				ld.AddPolyline(transforms.WindowFromLatLongP(pos), filterColor(ctx.cs.NDB), NDBtri[:])
			}
		}
	}

	// Fixes get triangles that point up
	fixTri := EquilateralTriangleVertices(5)
	if s.DrawEverything || s.DrawFixes {
		for _, fix := range database.fixes {
			ld.AddPolyline(transforms.WindowFromLatLongP(fix), filterColor(ctx.cs.Fix), fixTri[:])
		}
	} else {
		for name := range s.FixesToDraw {
			if pos, ok := database.fixes[name]; ok {
				ld.AddPolyline(transforms.WindowFromLatLongP(pos), filterColor(ctx.cs.Fix), fixTri[:])
			}
		}
	}

	// Airports are squares (like VORs)
	airportSquare := [][2]float32{[2]float32{-2, -2}, [2]float32{2, -2}, [2]float32{2, 2}, [2]float32{-2, 2}}
	if s.DrawEverything || s.DrawAirports {
		for _, ap := range database.airports {
			ld.AddPolyline(transforms.WindowFromLatLongP(ap), filterColor(ctx.cs.Airport), airportSquare)
		}
	} else {
		for name := range s.AirportsToDraw {
			if pos, ok := database.airports[name]; ok {
				ld.AddPolyline(transforms.WindowFromLatLongP(pos), filterColor(ctx.cs.Airport), airportSquare)
			}
		}
	}

	ld.GenerateCommands(cb)
	ld.Reset() // after GenerateCommands...

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	// Helper function to draw airway labels.
	drawAirwayLabels := func(labels []Label, color RGB) {
		for _, label := range labels {
			textPos := transforms.WindowFromLatLongP(label.p)
			if inWindow(textPos) {
				// Draw filled quads around each character with the
				// background color so that the text stands out over things
				// drawn so far (in particular, from the airway lines).
				style := TextStyle{
					Font:            labelFont,
					Color:           color,
					DrawBackground:  true,
					BackgroundColor: ctx.cs.Background}
				td.AddTextCentered(label.name, textPos, style)
			}
		}
	}

	// Draw low and high airway labels, as requested.
	if s.DrawEverything || s.DrawLowAirways {
		drawAirwayLabels(database.lowAirwayLabels, filterColor(ctx.cs.LowAirway))
	}
	if s.DrawEverything || s.DrawHighAirways {
		drawAirwayLabels(database.highAirwayLabels, filterColor(ctx.cs.HighAirway))
	}

	// Draw regular labels (e.g. taxiway letters) from the sector file.
	if s.DrawEverything || s.DrawLabels {
		for _, label := range database.labels {
			if viewBounds.Inside(label.p) {
				style := TextStyle{
					Font:            labelFont,
					Color:           filterColor(label.color),
					DropShadow:      true,
					DropShadowColor: ctx.cs.Background}
				td.AddTextCentered(label.name, transforms.WindowFromLatLongP(label.p), style)
			}
		}
	}

	// Helper function for drawing text for VORs, NDBs, fixes, and
	// airports. Takes a latlong point at which to draw the label as well
	// as an enum that indicates to which side of the point the label
	// should be drawn.  This lets us, for example, draw an airport label
	// in a different place relative to a point than a VOR label, so that
	// text doesn't overlap if an airport and VOR are coincident.
	const (
		DrawLeft = iota
		DrawRight
		DrawBelow
	)
	fixtext := func(name string, p Point2LL, color RGB, mode int) {
		var offset [2]float32
		switch mode {
		case DrawLeft:
			bx, _ := labelFont.BoundText(name, 0)
			offset = [2]float32{float32(-5 - bx), 1 + float32(labelFont.size/2)}
		case DrawRight:
			offset = [2]float32{7, 1 + float32(labelFont.size/2)}
		case DrawBelow:
			offset = [2]float32{0, float32(-labelFont.size)}
		}

		if viewBounds.Inside(p) {
			pw := add2f(transforms.WindowFromLatLongP(p), offset)
			if inWindow(pw) {
				if mode == DrawBelow {
					td.AddTextCentered(name, pw, TextStyle{Font: labelFont, Color: color})
				} else {
					td.AddText(name, pw, TextStyle{Font: labelFont, Color: color})
				}
			}
		}
	}

	// Helper function for drawing VOR, etc., labels that takes care of
	// iterating over the stuff to be drawn and then dispatching to
	// fixtext.
	drawloc := func(drawEverything bool, selected map[string]interface{},
		session map[string]interface{}, items map[string]Point2LL, color RGB, mode int) {
		if drawEverything {
			for name, p := range items {
				fixtext(name, p, color, mode)
			}
		} else {
			for name := range selected {
				if p, ok := items[name]; ok {
					fixtext(name, p, color, mode)
				}
			}
			for name := range session {
				if p, ok := items[name]; ok {
					fixtext(name, p, color, mode)
				}
			}
		}
	}

	if s.DrawVORNames {
		drawloc(s.DrawEverything || s.DrawVORs, s.VORsToDraw, nil,
			database.VORs, filterColor(ctx.cs.VOR), DrawLeft)
	}
	if s.DrawNDBNames {
		drawloc(s.DrawEverything || s.DrawNDBs, s.NDBsToDraw, nil,
			database.NDBs, filterColor(ctx.cs.NDB), DrawLeft)
	}
	if s.DrawFixNames {
		drawloc(s.DrawEverything || s.DrawFixes, s.FixesToDraw, nil,
			database.fixes, filterColor(ctx.cs.Fix), DrawRight)
	}
	if s.DrawAirportNames {
		drawloc(s.DrawEverything || s.DrawAirports, s.AirportsToDraw, nil,
			database.airports, filterColor(ctx.cs.Airport), DrawBelow)
	}

	td.GenerateCommands(cb)
}
