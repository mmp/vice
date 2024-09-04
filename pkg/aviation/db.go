// pkg/aviation/db.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"
)

var DB *StaticDatabase

///////////////////////////////////////////////////////////////////////////
// StaticDatabase

type StaticDatabase struct {
	Navaids             map[string]Navaid
	Airports            map[string]FAAAirport
	Fixes               map[string]Fix
	Airways             map[string][]Airway
	Callsigns           map[string]string // 3 letter -> callsign
	AircraftTypeAliases map[string]string
	AircraftPerformance map[string]AircraftPerformance
	Airlines            map[string]Airline
	MagneticGrid        MagneticGrid
	ARTCCs              map[string]ARTCC
	ERAMAdaptations     map[string]ERAMAdaptation
	TRACONs             map[string]TRACON
	MVAs                map[string][]MVA // TRACON -> MVAs
}

type FAAAirport struct {
	Id         string
	Name       string
	Elevation  int
	Location   math.Point2LL
	Runways    []Runway
	Approaches map[string][]WaypointArray
	STARs      map[string]STAR
	ARTCC      string
}

type TRACON struct {
	Name  string
	ARTCC string
}

type ARTCC struct {
	Name string
}

type Navaid struct {
	Id       string
	Type     string
	Name     string
	Location math.Point2LL
}

type Fix struct {
	Id       string
	Location math.Point2LL
}

type ERAMAdaptation struct { // add more later
	ARTCC             string                     // not in JSON
	CoordinationFixes map[string]AdaptationFixes `json:"coordination_fixes"`
}

const (
	RouteBasedFix = "route"
	ZoneBasedFix  = "zone"
)

type AdaptationFix struct {
	Name         string // not in JSON
	Type         string `json:"type"`
	ToFacility   string `json:"to"`   // controller to handoff to
	FromFacility string `json:"from"` // controller to handoff from
	Altitude     [2]int `json:"altitude"`
}

type AdaptationFixes []AdaptationFix

///////////////////////////////////////////////////////////////////////////

func (d StaticDatabase) LookupWaypoint(f string) (math.Point2LL, bool) {
	if n, ok := d.Navaids[f]; ok {
		return n.Location, true
	} else if f, ok := d.Fixes[f]; ok {
		return f.Location, true
	} else {
		return math.Point2LL{}, false
	}
}

type AircraftPerformance struct {
	Name string `json:"name"`
	ICAO string `json:"icao"`
	// engines, weight class, category
	WeightClass string  `json:"weightClass"`
	Ceiling     float32 `json:"ceiling"`
	Engine      struct {
		AircraftType string `json:"type"`
	} `json:"engines"`
	Rate struct {
		Climb      float32 `json:"climb"` // ft / minute; reduce by 500 after alt 5000 if this is >=2500
		Descent    float32 `json:"descent"`
		Accelerate float32 `json:"accelerate"` // kts / 2 seconds
		Decelerate float32 `json:"decelerate"`
	} `json:"rate"`
	Category struct {
		SRS   int    `json:"srs"`
		LAHSO int    `json:"lahso"`
		CWT   string `json:"cwt"`
	}
	Runway struct {
		Takeoff float32 `json:"takeoff"` // nm
		Landing float32 `json:"landing"` // nm
	} `json:"runway"`
	Speed struct {
		Min        float32 `json:"min"`
		V2         float32 `json:"v2"`
		Landing    float32 `json:"landing"`
		CruiseTAS  float32 `json:"cruise"`
		CruiseMach float32 `json:"cruiseM"`
		MaxTAS     float32 `json:"max"`
		MaxMach    float32 `json:"maxM"`
	} `json:"speed"`
}

type Airline struct {
	ICAO     string `json:"icao"`
	Name     string `json:"name"`
	Callsign struct {
		Name            string   `json:"name"`
		CallsignFormats []string `json:"callsignFormats"`
	} `json:"callsign"`
	JSONFleets map[string][][2]interface{} `json:"fleets"`
	Fleets     map[string][]FleetAircraft
}

type FleetAircraft struct {
	ICAO  string
	Count int
}

func init() {
	db := &StaticDatabase{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { db.Airports = parseAirports(); wg.Done() }()
	wg.Add(1)
	go func() { db.AircraftPerformance = parseAircraftPerformance(); wg.Done() }()
	wg.Add(1)
	go func() { db.Airlines, db.Callsigns = parseAirlines(); wg.Done() }()
	var airports map[string]FAAAirport
	wg.Add(1)
	go func() { airports, db.Navaids, db.Fixes, db.Airways = parseCIFP(); wg.Done() }()
	wg.Add(1)
	go func() { db.MagneticGrid = parseMagneticGrid(); wg.Done() }()
	wg.Add(1)
	go func() { db.ARTCCs, db.TRACONs = parseARTCCsAndTRACONs(); wg.Done() }()
	wg.Add(1)
	go func() { db.MVAs = parseMVAs(); wg.Done() }()
	wg.Add(1)
	go func() { db.ERAMAdaptations = parseAdaptations(); wg.Done() }()
	wg.Wait()

	for icao, ap := range airports {
		db.Airports[icao] = ap
	}

	DB = db
}

///////////////////////////////////////////////////////////////////////////
// FAA (and other) databases

// Utility function for parsing CSV files as strings; it breaks each line
// of the file into fields and calls the provided callback function for
// each one.
func mungeCSV(filename string, raw string, fields []string, callback func([]string)) {
	r := bytes.NewReader([]byte(raw))
	cr := csv.NewReader(r)
	cr.ReuseRecord = true

	// Find the index of each field the caller requested
	var fieldIndices []int
	if header, err := cr.Read(); err != nil {
		panic(fmt.Sprintf("%s: error parsing CSV file: %s", filename, err))
	} else {
		for fi, f := range fields {
			for hi, h := range header {
				if f == strings.TrimSpace(h) {
					fieldIndices = append(fieldIndices, hi)
					break
				}
			}
			if len(fieldIndices) != fi+1 {
				panic(fmt.Sprintf("%s: did not find requested field header", f))
			}
		}
	}

	var strs []string
	for {
		if record, err := cr.Read(); err == io.EOF {
			return
		} else if err != nil {
			panic(fmt.Sprintf("%s: error parsing CSV file: %s", filename, err))
		} else {
			for _, i := range fieldIndices {
				strs = append(strs, record[i])
			}
			callback(strs)
			strs = strs[:0]
		}
	}
}

func parseAirports() map[string]FAAAirport {
	airports := make(map[string]FAAAirport)

	airportsRaw := util.LoadResource("airports.csv.zst") // https://ourairports.com/data/

	parse := func(s string) math.Point2LL {
		loc, err := math.ParseLatLong([]byte(s))
		if err != nil {
			panic(err)
		}
		return loc
	}

	// These aren't in the FAA database but we need to have them defined
	// for the AAC scenario...
	airports["4V4"] = FAAAirport{Id: "4V4", Name: "", Elevation: 623,
		Location: parse("N036.07.26.937,W095.20.20.361"),
		Runways: []Runway{
			Runway{Id: "16", Heading: 160, Threshold: parse("N35.58.34.000,W095.23.03.000"), Elevation: 623},
			Runway{Id: "34", Heading: 340, Threshold: parse("N35.58.43.000,W095.23.07.000"), Elevation: 623},
		}}
	airports["4Y3"] = FAAAirport{Id: "4Y3", Name: "", Elevation: 624,
		Location: parse("N036.23.35.505,W095.21.13.590"),
		Runways: []Runway{
			Runway{Id: "30", Heading: 300, Threshold: parse("N036.23.20.482,W095.20.46.343"), Elevation: 624},
			Runway{Id: "12", Heading: 120, Threshold: parse("N036.23.39.117,W095.21.26.911"), Elevation: 624},
		}}
	airports["KAAC"] = FAAAirport{Id: "KAAC", Name: "", Elevation: 677,
		Location: parse("N036.10.01.611,W095.40.40.365"),
		Runways: []Runway{
			Runway{Id: "28L", Heading: 280, Threshold: parse("N036.09.45.000,W095.38.40.000"), Elevation: 677},
			Runway{Id: "28R", Heading: 280, Threshold: parse("N036.10.28.308,W095.38.35.972"), Elevation: 677},
			Runway{Id: "10L", Heading: 100, Threshold: parse("N036.10.44.801,W095.40.32.977"), Elevation: 677},
			Runway{Id: "10R", Heading: 100, Threshold: parse("N036.10.01.611,W095.40.40.365"), Elevation: 677},
		}}
	airports["KBRT"] = FAAAirport{Id: "KBRT", Name: "", Elevation: 689,
		Location: parse("N036.26.42.685,W095.56.39.032"),
		Runways: []Runway{
			Runway{Id: "13", Heading: 130, Threshold: parse("N36.27.16.000,W095.57.27.000"), Elevation: 689},
			Runway{Id: "31", Heading: 310, Threshold: parse("N36.26.32.000,W095.56.25.000"), Elevation: 689},
			Runway{Id: "4", Heading: 40, Threshold: parse("N36.27.02.000,W095.56.21.000"), Elevation: 689},
			Runway{Id: "22", Heading: 220, Threshold: parse("N36.26.39.000,W095.56.45.000"), Elevation: 689},
		}}
	airports["KJKE"] = FAAAirport{Id: "KJKE", Name: "", Elevation: 608,
		Location: parse("N035.54.58.809,W095.37.01.600"),
		Runways: []Runway{
			Runway{Id: "4", Heading: 39, Threshold: parse("N35.53.50.000,W095.37.24.000"), Elevation: 608},
			Runway{Id: "22", Heading: 219, Threshold: parse("N35.55.17.000,W095.35.49.000"), Elevation: 608},
			Runway{Id: "27", Heading: 270, Threshold: parse("N35.55.29.000,W95.35.55.000"), Elevation: 608},
			Runway{Id: "9", Heading: 90, Threshold: parse("N35.55.29.000,W095.38.00.000"), Elevation: 608},
		}}

	// FAA database
	mungeCSV("airports", string(airportsRaw),
		[]string{"latitude_deg", "longitude_deg", "elevation_ft", "gps_code", "name"},
		func(s []string) {
			atof := func(s string) float64 {
				v, err := util.Atof(s)
				if err != nil {
					panic(err)
				}
				return v
			}

			elevation := float64(0)
			if s[2] != "" {
				elevation = atof(s[2])
			}
			loc := math.Point2LL{float32(atof(s[1])), float32(atof(s[0]))}
			ap := FAAAirport{Id: s[3], Name: s[4], Location: loc, Elevation: int(elevation)}
			if ap.Id != "" {
				airports[ap.Id] = ap
			}
		})

	artccsRaw := util.LoadResource("airport_artccs.json")
	data := make(map[string]string) // Airport -> ARTCC
	json.Unmarshal(artccsRaw, &data)

	for name, artcc := range data {
		if entry, ok := airports[name]; ok {
			entry.ARTCC = artcc
			airports[name] = entry
		}
	}

	return airports
}

func parseAircraftPerformance() map[string]AircraftPerformance {
	openscopeAircraft := util.LoadResource("openscope-aircraft.json")

	var acStruct struct {
		Aircraft []AircraftPerformance `json:"aircraft"`
	}
	if err := json.Unmarshal(openscopeAircraft, &acStruct); err != nil {
		panic(fmt.Sprintf("error in JSON unmarshal of openscope-aircraft: %v", err))
	}

	ap := make(map[string]AircraftPerformance)
	for _, ac := range acStruct.Aircraft {
		// If we have mach but not TAS, do the conversion; the nav code
		// works with TAS..
		if ac.Speed.CruiseMach != 0 && ac.Speed.CruiseTAS == 0 {
			ac.Speed.CruiseTAS = 666.739 * ac.Speed.CruiseMach
		}
		if ac.Speed.MaxMach != 0 && ac.Speed.MaxTAS == 0 {
			ac.Speed.MaxTAS = 666.739 * ac.Speed.MaxMach
		}

		ap[ac.ICAO] = ac

		if ac.Speed.V2 != 0 && ac.Speed.V2 > 1.5*ac.Speed.Min {
			panic(fmt.Sprintf("%s: aircraft V2 %.0f seems suspiciously high (vs min %.01f)",
				ac.ICAO, ac.Speed.V2, ac.Speed.Min))
		}
	}

	return ap
}

func parseAirlines() (map[string]Airline, map[string]string) {
	openscopeAirlines := util.LoadResource("openscope-airlines.json")

	var alStruct struct {
		Airlines []Airline `json:"airlines"`
	}
	if err := json.Unmarshal([]byte(openscopeAirlines), &alStruct); err != nil {
		panic(fmt.Sprintf("error in JSON unmarshal of openscope-airlines: %v", err))
	}

	airlines := make(map[string]Airline)
	callsigns := make(map[string]string)
	for _, al := range alStruct.Airlines {
		fixedAirline := al
		fixedAirline.Fleets = make(map[string][]FleetAircraft)
		for name, aircraft := range fixedAirline.JSONFleets {
			for _, ac := range aircraft {
				fleetAC := FleetAircraft{
					ICAO:  strings.ToUpper(ac[0].(string)),
					Count: int(ac[1].(float64)),
				}
				fixedAirline.Fleets[name] = append(fixedAirline.Fleets[name], fleetAC)
			}
		}
		fixedAirline.JSONFleets = nil

		airlines[strings.ToUpper(al.ICAO)] = fixedAirline
		callsigns[strings.ToUpper(al.ICAO)] = al.Callsign.Name
	}
	return airlines, callsigns
}

// FAA Coded Instrument Flight Procedures (CIFP)
// https://www.faa.gov/air_traffic/flight_info/aeronav/digital_products/cifp/download/
func parseCIFP() (map[string]FAAAirport, map[string]Navaid, map[string]Fix, map[string][]Airway) {
	return ParseARINC424(util.LoadRawResource("FAACIFP18.zst"))
}

type MagneticGrid struct {
	MinLatitude, MaxLatitude   float32
	MinLongitude, MaxLongitude float32
	LatLongStep                float32
	Samples                    []float32
}

func parseMagneticGrid() MagneticGrid {
	/*
		1. Download software and coefficients from https://www.ncei.noaa.gov/products/world-magnetic-model
		2. Build wmm_grid, run with the parameters in the MagneticGrid initializer below, year 2024,
		   altitude 0 -> 0, select "declination" for output.
		3. awk '{print $5}' < GridResults.txt | zstd -19 -o magnetic_grid.txt.zst
	*/
	mg := MagneticGrid{
		MinLatitude:  24,
		MaxLatitude:  50,
		MinLongitude: -125,
		MaxLongitude: -66,
		LatLongStep:  0.25,
	}

	samples := util.LoadResource("magnetic_grid.txt.zst")
	r := bufio.NewReader(bytes.NewReader(samples))

	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}

		if v, err := strconv.ParseFloat(strings.TrimSpace(line), 32); err != nil {
			panic(line + ": parsing error: " + err.Error())
		} else {
			mg.Samples = append(mg.Samples, float32(v))
		}
	}

	nlat := int(1 + (mg.MaxLatitude-mg.MinLatitude)/mg.LatLongStep)
	nlong := int(1 + (mg.MaxLongitude-mg.MinLongitude)/mg.LatLongStep)
	if len(mg.Samples) != nlat*nlong {
		panic(fmt.Sprintf("found %d magnetic grid samples, expected %d x %d = %d",
			len(mg.Samples), nlat, nlong, nlat*nlong))
	}

	return mg
}

func parseAdaptations() map[string]ERAMAdaptation {
	adaptations := make(map[string]ERAMAdaptation)

	adaptationsRaw := util.LoadResource("adaptations.json")
	if err := json.Unmarshal(adaptationsRaw, &adaptations); err != nil {
		panic(err)
	}

	// Wire up names in the structs
	for artcc, adapt := range adaptations {
		adapt.ARTCC = artcc

		for fix, fixes := range adapt.CoordinationFixes {
			for i := range fixes {
				fixes[i].Name = fix
			}
		}

		adaptations[artcc] = adapt
	}

	return adaptations
}

func (mg *MagneticGrid) Lookup(p math.Point2LL) (float32, error) {
	if p[0] < mg.MinLongitude || p[0] > mg.MaxLongitude ||
		p[1] < mg.MinLatitude || p[1] > mg.MaxLatitude {
		return 0, fmt.Errorf("lookup point outside sampled grid")
	}

	nlat := int(1 + (mg.MaxLatitude-mg.MinLatitude)/mg.LatLongStep)
	nlong := int(1 + (mg.MaxLongitude-mg.MinLongitude)/mg.LatLongStep)

	// Round to nearest
	lat := math.Min(int((p[1]-mg.MinLatitude)/mg.LatLongStep+0.5), nlat-1)
	long := math.Min(int((p[0]-mg.MinLongitude)/mg.LatLongStep+0.5), nlong-1)

	// Note: we flip the sign
	return -mg.Samples[long+nlong*lat], nil
}

type MVA struct {
	MinimumLimit          int                      `xml:"minimumLimit"`
	MinimumLimitReference string                   `xml:"minimumLimitReference"`
	Proj                  *MVAHorizontalProjection `xml:"horizontalProjection"`
	Bounds                math.Extent2D
	ExteriorRing          [][2]float32
	InteriorRings         [][][2]float32
}

func (m *MVA) Inside(p [2]float32) bool {
	if !m.Bounds.Inside(p) {
		return false
	}
	if !math.PointInPolygon(p, m.ExteriorRing) {
		return false
	}
	for _, in := range m.InteriorRings {
		if math.PointInPolygon(p, in) {
			return false
		}
	}
	return true
}

type MVALinearRing struct {
	PosList string `xml:"posList"`
}

func (r MVALinearRing) Vertices() [][2]float32 {
	var v [][2]float32
	f := strings.Fields(r.PosList)
	if len(f)%2 != 0 {
		panic("odd number of floats?")
	}

	for i := 0; i < len(f); i += 2 {
		v0, err := strconv.ParseFloat(f[i], 32)
		if err != nil {
			panic(err)
		}
		v1, err := strconv.ParseFloat(f[i+1], 32)
		if err != nil {
			panic(err)
		}
		v = append(v, [2]float32{float32(v0), float32(v1)})
	}

	return v
}

type MVAExterior struct {
	LinearRing MVALinearRing `xml:"LinearRing"`
}

type MVAInterior struct {
	LinearRing MVALinearRing `xml:"LinearRing"`
}

type MVAPolygonPatch struct {
	Exterior  MVAExterior   `xml:"exterior"`
	Interiors []MVAInterior `xml:"interior"`
}

type MVAPatches struct {
	PolygonPatch MVAPolygonPatch `xml:"PolygonPatch"`
}

type MVASurface struct {
	Patches MVAPatches `xml:"patches"`
}

type MVAHorizontalProjection struct {
	Surface MVASurface `xml:"Surface"`
}

// To update the MVA data:
// % go run util/scrapemva.go # download the XML files
// % parallel zstd -19 {} ::: *xml
// % zip mva-fus3.zip *FUS3_*zst
// % mv mva*zip ~/vice/resources/
// % /bin/rm MVA_*zst MVA_*xml

func parseMVAs() map[string][]MVA {
	// The MVA files are stored in a zip file to avoid the overhead of
	// opening lots of files to read them in.
	z := util.LoadResource("mva-fus3.zip")
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		panic(err)
	}

	type mvaTracon struct {
		TRACON string
		MVAs   []MVA
	}
	mvaChan := make(chan mvaTracon, len(zr.File))

	for _, f := range zr.File {
		// Launch a goroutine for each one so that we load them in
		// parallel.
		go func(f *zip.File) {
			r, err := f.Open()
			if err != nil {
				// Errors are panics since this all happens at startup time
				// with data that's fixed at release time.
				panic(err)
			}

			b, err := io.ReadAll(r)
			if err != nil {
				panic(err)
			}

			contents, err := util.DecompressZstd(string(b))
			if err != nil {
				panic(err)
			}

			decoder := xml.NewDecoder(strings.NewReader(contents))

			var mvas []MVA
			tracon := ""
			for {
				// The full XML schema is fairly complex so rather than
				// declaring a ton of helper types to represent the full
				// nested complexity, we'll instead walk through until we
				// find the sections where the MVA altitudes and polygons
				// are defined.
				token, _ := decoder.Token()
				if token == nil {
					break
				}

				if se, ok := token.(xml.StartElement); ok {
					switch se.Name.Local {
					case "description":
						// The first <ns1:description> in the file will be
						// of the form ABE_MVA_FUS3_2022, which gives us
						// the name of the TRACON we've got (ABE, in that
						// case). Subsequent descriptions should all be
						// "MINIMUM VECTORING ALTITUDE (MVA)"
						var desc string
						if err := decoder.DecodeElement(&desc, &se); err != nil {
							panic(fmt.Sprintf("Error decoding element: %v", err))
						}

						if tracon == "" {
							var ok bool
							tracon, _, ok = strings.Cut(desc, "_")
							if !ok {
								panic(desc + ": unexpected description string")
							}
						} else if desc != "MINIMUM VECTORING ALTITUDE (MVA)" {
							panic(desc)
						}

					case "AirspaceVolume":
						var m MVA
						if err := decoder.DecodeElement(&m, &se); err != nil {
							panic(fmt.Sprintf("Error decoding element: %v", err))
						}

						// Parse the floats and initialize the rings
						patch := m.Proj.Surface.Patches.PolygonPatch
						m.ExteriorRing = patch.Exterior.LinearRing.Vertices()
						for _, in := range patch.Interiors {
							m.InteriorRings = append(m.InteriorRings, in.LinearRing.Vertices())
						}

						m.Proj = nil // Don't hold on to the strings

						// Initialize the bounding box
						m.Bounds = math.Extent2DFromPoints(m.ExteriorRing)

						mvas = append(mvas, m)
					}
				}
			}

			r.Close()

			mvaChan <- mvaTracon{TRACON: tracon, MVAs: mvas}
		}(f)
	}

	mvas := make(map[string][]MVA)
	for range zr.File {
		m := <-mvaChan
		mvas[m.TRACON] = m.MVAs
	}

	return mvas
}

func parseARTCCsAndTRACONs() (map[string]ARTCC, map[string]TRACON) {
	artccJSON := util.LoadResource("artccs.json")
	var artccs map[string]ARTCC
	if err := json.Unmarshal(artccJSON, &artccs); err != nil {
		panic(fmt.Sprintf("error unmarshalling ARTCCs: %v", err))
	}

	traconJSON := util.LoadResource("tracons.json")
	var tracons map[string]TRACON
	if err := json.Unmarshal(traconJSON, &tracons); err != nil {
		panic(fmt.Sprintf("error unmarshalling TRACONs: %v", err))
	}

	// Validate that all of the TRACON ARTCCs are known.
	for name, tracon := range tracons {
		if _, ok := artccs[tracon.ARTCC]; !ok {
			panic(tracon.ARTCC + ": ARTCC unknown for TRACON " + name)
		}
	}

	return artccs, tracons
}

func (ap FAAAirport) ValidRunways() string {
	return strings.Join(util.MapSlice(ap.Runways, func(r Runway) string { return r.Id }), ", ")
}

func PrintCIFPRoutes(airport string) error {
	ap, ok := DB.Airports[airport]
	if !ok {
		return fmt.Errorf("%s: airport not present in database\n", airport)
	}

	fmt.Printf("STARs:\n")
	for _, s := range util.SortedMapKeys(ap.STARs) {
		ap.STARs[s].Print(s)
	}
	fmt.Printf("\nApproaches:\n")
	for _, appr := range util.SortedMapKeys(ap.Approaches) {
		fmt.Printf("%-5s: ", appr)
		for i, wp := range ap.Approaches[appr] {
			if i > 0 {
				fmt.Printf("       ")
			}
			fmt.Println(wp.Encode())
		}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////

func (ea ERAMAdaptation) FixForRouteAndAltitude(route string, altitude string) *AdaptationFix {
	waypoints := strings.Fields(route)
	for fix, adaptationFixes := range ea.CoordinationFixes {
		if slices.Contains(waypoints, fix) {
			adaptationFix, err := adaptationFixes.Fix(altitude)
			if err == nil && adaptationFix.Type != ZoneBasedFix {
				return &adaptationFix
			}
		}
	}
	return nil
}

func (ea ERAMAdaptation) AdaptationFixForAltitude(fix string, altitude string) *AdaptationFix {
	if adaptationFixes, ok := ea.CoordinationFixes[fix]; !ok {
		return nil
	} else if af, err := adaptationFixes.Fix(altitude); err != nil {
		return nil
	} else {
		return &af
	}
}

func (fixes AdaptationFixes) Fix(altitude string) (AdaptationFix, error) {
	switch len(fixes) {
	case 0:
		return AdaptationFix{}, ErrNoMatchingFix

	case 1:
		return fixes[0], nil

	default:
		// TODO: eventually make a function to parse a string that has a block altitude (for example)
		// and return an int (figure out how STARS handles that). For now strconv.Atoi can be used
		if alt, err := strconv.Atoi(altitude); err != nil {
			return AdaptationFix{}, err
		} else {
			for _, fix := range fixes {
				if alt >= fix.Altitude[0] && alt <= fix.Altitude[1] {
					return fix, nil
				}
			}
			return AdaptationFix{}, ErrNoMatchingFix
		}
	}
}
