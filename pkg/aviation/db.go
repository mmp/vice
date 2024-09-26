// pkg/aviation/db.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"

	"github.com/gocolly/colly/v2"
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
	if err := util.UnmarshalJSON(artccsRaw, &data); err != nil {
		fmt.Fprintf(os.Stderr, "airport_artccs.json: %v\n", err)
		os.Exit(1)
	}

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
	if err := util.UnmarshalJSON(openscopeAircraft, &acStruct); err != nil {
		fmt.Fprintf(os.Stderr, "openscope-aircraft.json: %v\n", err)
		os.Exit(1)
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

		cwt := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "NOWGT"}
		if !slices.Contains(cwt, ac.Category.CWT) {
			fmt.Fprintf(os.Stderr, "%s: invalid CWT category provided\n", ac.Category.CWT)
		}
		if ac.Rate.Climb < 500 || ac.Rate.Climb > 5000 {
			fmt.Fprintf(os.Stderr, "%s: aircraft climb rate %f seems off\n", ac.ICAO, ac.Rate.Climb)
		}
		if ac.Rate.Descent < 500 || ac.Rate.Descent > 5000 {
			fmt.Fprintf(os.Stderr, "%s: aircraft descent rate %f seems off\n", ac.ICAO, ac.Rate.Descent)
		}
		if ac.Rate.Accelerate < 2 || ac.Rate.Accelerate > 10 {
			fmt.Fprintf(os.Stderr, "%s: aircraft accelerate rate %f seems off\n", ac.ICAO, ac.Rate.Accelerate)
		}
		if ac.Rate.Decelerate < 2 || ac.Rate.Decelerate > 8 {
			fmt.Fprintf(os.Stderr, "%s: aircraft decelerate rate %f seems off\n", ac.ICAO, ac.Rate.Decelerate)
		}
		if ac.Speed.Min < 34 || ac.Speed.Min > 200 {
			fmt.Fprintf(os.Stderr, "%s: aircraft min speed %f seems off\n", ac.ICAO, ac.Speed.Min)
		}
		if ac.Speed.Landing < 40 || ac.Speed.Landing > 200 {
			fmt.Fprintf(os.Stderr, "%s: aircraft landing speed %f seems off\n", ac.ICAO, ac.Speed.Landing)
		}
		if ac.Speed.MaxTAS < 40 || ac.Speed.MaxTAS > 550 && ac.ICAO != "CONC" {
			fmt.Fprintf(os.Stderr, "%s: aircraft max TAS %f seems off\n", ac.ICAO, ac.Speed.MaxTAS)
		}
		if ac.Speed.V2 != 0 && ac.Speed.V2 > 1.5*ac.Speed.Min {
			fmt.Fprintf(os.Stderr, "%s: aircraft V2 %.0f seems suspiciously high (vs min %.01f)",
				ac.ICAO, ac.Speed.V2, ac.Speed.Min)
		}
	}

	return ap
}

func parseAirlines() (map[string]Airline, map[string]string) {
	openscopeAirlines := util.LoadResource("openscope-airlines.json")

	var alStruct struct {
		Airlines []Airline `json:"airlines"`
	}
	if err := util.UnmarshalJSON([]byte(openscopeAirlines), &alStruct); err != nil {
		fmt.Fprintf(os.Stderr, "openscope-airlines.json: %v\n", err)
		os.Exit(1)
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
	if err := util.UnmarshalJSON(adaptationsRaw, &adaptations); err != nil {
		fmt.Fprintf(os.Stderr, "adaptations.json: %v\n", err)
		os.Exit(1)
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
	if err := util.UnmarshalJSON(artccJSON, &artccs); err != nil {
		fmt.Fprintf(os.Stderr, "artccs.json: %v\n", err)
		os.Exit(1)
	}

	traconJSON := util.LoadResource("tracons.json")
	var tracons map[string]TRACON
	if err := util.UnmarshalJSON(traconJSON, &tracons); err != nil {
		fmt.Fprintf(os.Stderr, "tracons.json: %v\n", err)
		os.Exit(1)
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
// TFRs

// TFR represents an FAA-issued temporary flight restriction.
type TFR struct {
	ARTCC     string
	Type      string // VIP, SECURITY, EVENT, etc.
	LocalName string // Short string summarizing it.
	Effective time.Time
	Expire    time.Time
	Points    [][]math.Point2LL // One or more line loops defining its extent.
}

// TFRCache stores active TFRs that have been retrieved previously; we save
// it out on the config so that we don't download all of them each time vice is launched.
type TFRCache struct {
	TFRs map[string]TFR // URL -> TFR
	ch   chan map[string]TFR
}

func MakeTFRCache() TFRCache {
	return TFRCache{
		TFRs: make(map[string]TFR),
	}
}

// UpdateAsync kicks off an update of the TFRCache; it runs asynchronously
// with synchronization happening when Sync or TFRsForTRACON is called.
func (t *TFRCache) UpdateAsync(lg *log.Logger) {
	if t.ch != nil {
		return
	}
	t.ch = make(chan map[string]TFR)
	go fetchTFRs(util.DuplicateMap(t.TFRs), t.ch, lg)
}

// Sync synchronizes the cache, adding any newly-downloaded TFRs.  It
// returns after the given timeout passes if we haven't gotten results back
// yet.
func (t *TFRCache) Sync(timeout time.Duration, lg *log.Logger) {
	if t.ch != nil {
		select {
		case t.TFRs = <-t.ch:
			t.ch = nil
		case <-time.After(timeout):
			lg.Warn("TFR fetch timed out")
		}
	}
}

// TFRsForTRACON returns all TFRs that apply to the given TRACON.  (It
// currently return all of the ones for the TRACON's ARTCC, which is
// overkill; we should probably cull them based on distance to the center
// of the TRACON.)
func (t *TFRCache) TFRsForTRACON(tracon string, lg *log.Logger) []TFR {
	t.Sync(3*time.Second, lg)

	if tr, ok := DB.TRACONs[tracon]; !ok {
		return nil
	} else {
		var tfrs []TFR
		for _, url := range util.SortedMapKeys(t.TFRs) {
			if tfr := t.TFRs[url]; tfr.ARTCC == tr.ARTCC {
				tfrs = append(tfrs, tfr)
			}
		}
		return tfrs
	}
}

// Returns the URLs to all of the XML-formatted TFRs from the tfr.faa.gov website.
func allTFRUrls() []string {
	// Try to look legit.
	c := colly.NewCollector(
		colly.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Safari/605.1.15"))

	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Access-Control-Allow-Origin", "*")
		r.Headers.Set("Accept", "*/*")
		r.Headers.Set("Sec-Fetch-Site", "same-origin")
		r.Headers.Set("Accept-Language", "en-US,en;q=0.9")
		r.Headers.Set("Accept-Encoding", "gzip, deflate, br")
		r.Headers.Set("Sec-Fetch-Mode", "cors")
		r.Headers.Set("Access-Control-Allow-Credentials", "true")
		r.Headers.Set("Connection", "keep-alive")
		r.Headers.Set("Sec-Fetch-Dest", "empty")
	})

	// Find all links that match the pattern to a TFR.  Note that this is
	// super brittle / tied to the current webpage layout, etc. It will
	// surely break unexpectedly at some point in the future.
	var urls []string
	c.OnHTML("a", func(e *colly.HTMLElement) {
		if href := e.Attr("href"); strings.HasPrefix(href, "../save_pages") {
			// Rewrite to an absolute URL to the XML file.
			url := "https://tfr.faa.gov/" + strings.TrimPrefix(href, "../")
			url = strings.TrimSuffix(url, "html") + "xml"
			urls = append(urls, url)
		}
	})

	c.Visit("https://tfr.faa.gov/tfr2/list.html")

	// Each TFR has multiple links from the page, so uniquify them before returning them.
	slices.Sort(urls)
	return slices.Compact(urls)
}

// fetchTFRs runs in a goroutine and asynchronously downloads the TFRs from
// the FAA website, converts them to the TFR struct, and then sends the
// result on the provided chan when done.
func fetchTFRs(tfrs map[string]TFR, ch chan<- map[string]TFR, lg *log.Logger) {
	// Semaphore to limit to 4 concurrent requests.
	const n = 4
	sem := make(chan interface{}, 4)
	defer func() { close(sem) }()

	type TFROrError struct {
		URL string
		TFR TFR
		err error
	}
	fetched := make(chan TFROrError, len(tfrs))
	defer func() { close(fetched) }()

	// fetch fetches a single TFR and converts it.
	fetch := func(url string) {
		// Acquire the semaphore.
		sem <- nil
		defer func() { <-sem }()

		result := TFROrError{URL: url}
		resp, err := http.Get(url)
		if err != nil {
			result.err = err
		} else {
			defer resp.Body.Close()
			result.TFR, result.err = decodeTFRXML(url, resp.Body, lg)
		}
		fetched <- result
	}

	// Launch a goroutine to fetch each one that we don't already have
	// downloaded.
	urls := allTFRUrls()
	launched := 0
	for _, url := range urls {
		if _, ok := tfrs[url]; !ok {
			go fetch(url)
			launched++
		}
	}

	// Harvest the fetched results.
	for launched > 0 {
		result := <-fetched
		if result.err != nil {
			lg.Warnf("%s: %v", result.URL, result.err)
		} else {
			tfrs[result.URL] = result.TFR
		}
		launched--
	}

	// Cull stale TFRs.
	for url := range tfrs {
		// It's no longer on the FAA site.
		if !slices.Contains(urls, url) {
			delete(tfrs, url)
		}
	}

	ch <- tfrs
	close(ch)
}

var tfrTypes = map[string]string{
	"91.137": "HAZARDS",
	"91.138": "HI HAZARDS",
	"91.141": "VIP",
	"91.143": "SPACE OPS",
	"91.145": "EVENT",
	"99.7":   "SECURITY",
}

// XNOTAMUpdate was generated 2024-09-23 07:39:34 by
// https://xml-to-go.github.io/, using https://github.com/miku/zek. Then
// manually chopped down to the parts we care about...
type XNOTAMUpdate struct {
	Group struct {
		Add struct {
			Not struct {
				NotUid struct {
					TxtLocalName string `xml:"txtLocalName"`
				} `xml:"NotUid"`
				DateEffective          string `xml:"dateEffective"`
				DateExpire             string `xml:"dateExpire"`
				CodeTimeZone           string `xml:"codeTimeZone"`
				CodeExpirationTimeZone string `xml:"codeExpirationTimeZone"`
				CodeFacility           string `xml:"codeFacility"`
				TfrNot                 struct {
					CodeType     string `xml:"codeType"`
					TFRAreaGroup []struct {
						AbdMergedArea struct {
							Avx []struct {
								Text      string `xml:",chardata"`
								CodeDatum string `xml:"codeDatum"`
								CodeType  string `xml:"codeType"`
								GeoLat    string `xml:"geoLat"`
								GeoLong   string `xml:"geoLong"`
							} `xml:"Avx"`
						} `xml:"abdMergedArea"`
					} `xml:"TFRAreaGroup"`
				} `xml:"TfrNot"`
			} `xml:"Not"`
		} `xml:"Add"`
	} `xml:"Group"`
}

// decodeTFRXML takes an XML-formatted TFR and converts it to our struct.
func decodeTFRXML(url string, r io.Reader, lg *log.Logger) (TFR, error) {
	var tfr TFR
	var xmlTFR XNOTAMUpdate
	dec := xml.NewDecoder(r)
	if err := dec.Decode(&xmlTFR); err != nil {
		return tfr, err
	}

	notam := xmlTFR.Group.Add.Not
	tfr.ARTCC = notam.CodeFacility
	tfr.Type = tfrTypes[notam.TfrNot.CodeType]
	tfr.LocalName = notam.NotUid.TxtLocalName

	// Attempt to parse a time; these come to us as a pair of strings,
	// sometimes misformatted.
	parseTime := func(date, zone string) (time.Time, error) {
		if zone == "" {
			zone = "UTC"
		}
		return time.Parse("2006-01-02T15:04:05 MST", date+" "+zone)
	}

	// Since the provided times are often bogus, patch them up so that they
	// are currently active if we couldn't get the times.
	var err error
	tfr.Effective, err = parseTime(notam.DateEffective, notam.CodeTimeZone)
	if err != nil {
		tfr.Effective = time.Now()
		lg.Warn("%s: %v", url, err)
	}
	tfr.Expire, err = parseTime(notam.DateExpire, notam.CodeExpirationTimeZone)
	if err != nil {
		tfr.Expire = time.Now().Add(10 * 365 * 24 * time.Hour)
		lg.Warn("%s: %v", url, err)
	}

	// The extent is given as one or more line loops.
	for _, group := range notam.TfrNot.TFRAreaGroup {
		var pts []math.Point2LL
		for _, pt := range group.AbdMergedArea.Avx {
			if len(pt.GeoLat) == 0 || len(pt.GeoLong) == 0 {
				continue
			}
			pf := func(s string) (float32, error) {
				var v float64
				v, err = strconv.ParseFloat(s[:len(s)-1], 32)
				if err != nil {
					return float32(v), err
				}
				neg := s[len(s)-1] == 'S' || s[len(s)-1] == 'W'
				if neg {
					v = -v
				}
				return float32(v), nil
			}

			var p math.Point2LL
			p[0], err = pf(pt.GeoLong)
			if err != nil {
				lg.Warn("%s: %v", url, err)
				continue
			}
			p[1], err = pf(pt.GeoLat)
			if err != nil {
				lg.Warn("%s: %v", url, err)
				continue
			}
			pts = append(pts, p)
		}
		if len(pts) > 0 {
			tfr.Points = append(tfr.Points, pts)
		}
	}

	return tfr, nil
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

// CWTApproachSeparation returns the required separation between aircraft of the two
// given CWT categories. If 0 is returned, minimum radar separation should be used.
func CWTApproachSeparation(front, back string) float32 {
	if len(front) != 1 || (front[0] < 'A' && front[0] > 'I') {
		return 10
	}
	if len(back) != 1 || (back[0] < 'A' && back[0] > 'I') {
		return 10
	}

	f, b := front[0]-'A', back[0]-'A'

	// 7110.126B TBL 5-5-2
	cwtOnApproachLookUp := [9][9]float32{ // [front][back]
		{0, 5, 6, 6, 7, 7, 7, 8, 8},       // Behind A
		{0, 3, 4, 4, 5, 5, 5, 5, 6},       // Behind B
		{0, 0, 0, 0, 3.5, 3.5, 3.5, 5, 6}, // Behind C
		{0, 3, 4, 4, 5, 5, 5, 6, 6},       // Behind D
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind E
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind F
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind G
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind H
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind I
	}
	return cwtOnApproachLookUp[f][b]
}

// CWTDirectlyBehindSeparation returns the required separation between
// aircraft of the two given CWT categories. If 0 is returned, minimum
// radar separation should be used.
func CWTDirectlyBehindSeparation(front, back string) float32 {
	if len(front) != 1 || (front[0] < 'A' && front[0] > 'I') {
		return 10
	}
	if len(back) != 1 || (back[0] < 'A' && back[0] > 'I') {
		return 10
	}

	f, b := front[0]-'A', back[0]-'A'

	// 7110.126B TBL 5-5-1
	cwtBehindLookup := [9][9]float32{ // [front][back]
		{0, 5, 6, 6, 7, 7, 7, 8, 8},       // Behind A
		{0, 3, 4, 4, 5, 5, 5, 5, 5},       // Behind B
		{0, 0, 0, 0, 3.5, 3.5, 3.5, 5, 5}, // Behind C
		{0, 3, 4, 4, 5, 5, 5, 5, 5},       // Behind D
		{0, 0, 0, 0, 0, 0, 0, 0, 4},       // Behind E
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind F
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind G
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind H
		{0, 0, 0, 0, 0, 0, 0, 0, 0},       // Behind I
	}
	return cwtBehindLookup[f][b]
}
