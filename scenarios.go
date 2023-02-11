// scenarios.go

package main

import (
	_ "embed"

	"github.com/mmp/sct2"
)

var (
	//go:embed resources/ZNY_Combined_VRC.sct2.zst
	sectorFile string
)

func mustParseLatLong(l string) Point2LL {
	ll, err := ParseLatLong(l)
	if err != nil {
		panic(l + ": " + err.Error())
	}
	return ll
}

///////////////////////////////////////////////////////////////////////////
// ZNY

/*
	stars.Facility.Airports = append(stars.Facility.Airports,
		STARSAirport{ICAOCode: "KJFK", Range: 50, IncludeInSSA: true, TowerListIndex: 1},
		STARSAirport{ICAOCode: "KFRG", Range: 30, IncludeInSSA: true, TowerListIndex: 2})
*/

func LoadZNY() *TRACON {
	t := &TRACON{
		Name:              "ZNY",
		NmPerLatitude:     60,
		NmPerLongitude:    45,
		MagneticVariation: 13.3,
		PrimaryAirport:    "KJFK",

		Airports: map[string]*Airport{
			"KJFK": JFKAirport(),
			"KLGA": LGAAirport(),
			"KISP": ISPAirport(),
			"KFRG": FRGAirport(),
		},
		VideoMaps: loadVideoMaps(),
		Scenarios: make(map[string]*Scenario),
		ControlPositions: map[string]*Controller{
			"BOS_E_CTR": &Controller{Callsign: "BOS_E_CTR", Frequency: NewFrequency(133.45), SectorId: "B17", Scope: "C"},
			"ISP_APP":   &Controller{Callsign: "ISP_APP", Frequency: NewFrequency(120.05), SectorId: "3H", Scope: "H"},
			"JFK_DEP":   &Controller{Callsign: "JFK_DEP", Frequency: NewFrequency(135.9), SectorId: "2J", Scope: "J"},
			"JFK_APP":   &Controller{Callsign: "JFK_APP", Frequency: NewFrequency(128.125), SectorId: "2G", Scope: "G"},
			"JFK_TWR":   &Controller{Callsign: "JFK_TWR", Frequency: NewFrequency(119.1), SectorId: "2W", Scope: "T"},
			"LGA_DEP":   &Controller{Callsign: "LGA_DEP", Frequency: NewFrequency(120.4), SectorId: "1L", Scope: "L"},
			"NY_B_CTR":  &Controller{Callsign: "NY_B_CTR", Frequency: NewFrequency(125.325), SectorId: "N56", Scope: "C"},
			"NY_C_CTR":  &Controller{Callsign: "NY_C_CTR", Frequency: NewFrequency(132.175), SectorId: "N34", Scope: "C"},
			"NY_F_CTR":  &Controller{Callsign: "NY_F_CTR", Frequency: NewFrequency(128.3), SectorId: "N66", Scope: "C"},
			"NY_LE_DEP": &Controller{Callsign: "NY_LE_DEP", Frequency: NewFrequency(126.8), SectorId: "5E", Scope: "E"},
			"NY_LS_DEP": &Controller{Callsign: "NY_LS_DEP", Frequency: NewFrequency(124.75), SectorId: "5S", Scope: "S"},
		},

		Scratchpads: map[string]string{
			"WAVEY":  "WAV",
			"SHIPP":  "SHI",
			"HAPIE":  "HAP",
			"BETTE":  "BET",
			"MERIT":  "MER",
			"GREKI":  "GRE",
			"BAYYS":  "BAY",
			"BDR":    "BDR",
			"DIXIE":  "DIX",
			"WHITE":  "WHI",
			"RBV":    "RBV",
			"ARD":    "ARD",
			"COATE":  "COA",
			"NEION":  "NEI",
			"HAAYS":  "HAY",
			"GAYEL":  "GAY",
			"DEEZZ":  "DEZ",
			"DEEZZ5": "DEZ",
		},

		Center: Point2LL{-73.7765, 40.6401},
		RadarSites: []RadarSite{
			RadarSite{Char: "E",
				Id:             "EWR",
				Position:       "KEWR",
				Elevation:      136,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			},
			RadarSite{Char: "J",
				Id:             "JFK",
				Position:       "KJFK",
				Elevation:      143,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			},
			RadarSite{Char: "I",
				Id:             "ISP",
				Position:       "KISP",
				Elevation:      185,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			},
			RadarSite{Char: "H",
				Id:             "HPN",
				Position:       "KHPN",
				Elevation:      708,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			},
			RadarSite{Char: "S",
				Id:             "SWF",
				Position:       "KSWF",
				Elevation:      972,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			},
		},

		STARSMaps: []STARSMap{
			STARSMap{Label: "JFK4", Group: 0, Name: "N90 JFK - 4s"},
			STARSMap{Label: "JFK13", Group: 0, Name: "N90 JFK - 13s"},
			STARSMap{Label: "JFK22", Group: 0, Name: "N90 JFK - 22s"},
			STARSMap{Label: "JFK31", Group: 0, Name: "N90 JFK - 31s"},
			STARSMap{Label: "NY B", Group: 1, Name: "New York Class B"},
			STARSMap{Label: "MVA", Group: 1, Name: "N90 - MVA"},
			STARSMap{Label: "JFK22 ILS", Group: 0, Name: "N90 JFK - ILS 22s"},
			STARSMap{Label: "JFK31 NTZ", Group: 0, Name: "N90 JFK - 31s NTZ"},
			STARSMap{Label: "LGA", Group: 0, Name: "N90 LGA - Video Map"},
			STARSMap{Label: "LIB D", Group: 0, Name: "N90 LIB - Departure"},
			STARSMap{Label: "LIB C", Group: 0, Name: "N90 LIB - Catskill"},
			STARSMap{Label: "EWR", Group: 0, Name: "N90 EWR - Video Map"},
			STARSMap{Label: "EWR SAT", Group: 0, Name: "N90 EWR - Satellite"},
			STARSMap{Label: "EWR CRDA", Group: 0, Name: "N90 EWR - CRDA"},
			STARSMap{Label: "ISP", Group: 0, Name: "N90 ISP - Video Map"},
		},
	}

	sa4 := &Scenario{
		Name:                   "KJFK Depart 4L Land 4L/R",
		Callsign:               "JFK_APP",
		DepartureRunwayStrings: []string{"KJFK/4L", "KLGA/4", "KISP/6", "KFRG/1"},
		ArrivalRunwayStrings:   []string{"KJFK/4R", "KJFK/4L"},
		Wind:                   Wind{Direction: 30, Speed: 19, Gust: 25},
		Controllers:            []string{"JFK_APP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_B_CTR", "NY_C_CTR", "NY_F_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sa4.Name] = sa4

	sd4 := &Scenario{
		Name:                   "KJFK Depart 4L",
		Callsign:               "JFK_DEP",
		DepartureRunwayStrings: []string{"KJFK/4L", "KLGA/4", "KISP/6", "KFRG/1"},
		Wind:                   Wind{Direction: 30, Speed: 8, Gust: 0},
		Controllers:            []string{"JFK_DEP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_C_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sd4.Name] = sd4

	sa13 := &Scenario{
		Name:                   "KJFK Depart 13R Land 13L/22L",
		Callsign:               "JFK_APP",
		DepartureRunwayStrings: []string{"KJFK/13R", "KLGA/13", "KISP/15R", "KFRG/14"},
		ArrivalRunwayStrings:   []string{"KJFK/13L", "KJFK/22L"},
		Wind:                   Wind{Direction: 140, Speed: 12, Gust: 17},
		Controllers:            []string{"JFK_APP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_B_CTR", "NY_C_CTR", "NY_F_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sa13.Name] = sa13

	sd13 := &Scenario{
		Name:                   "KJFK Depart 13R",
		Callsign:               "JFK_DEP",
		DepartureRunwayStrings: []string{"KJFK/13R", "KLGA/13", "KISP/15R", "KFRG/14"},
		Wind:                   Wind{Direction: 140, Speed: 24, Gust: 0},
		Controllers:            []string{"JFK_DEP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_C_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sd13.Name] = sd13

	sa22 := &Scenario{
		Name:                   "KJFK Depart 22R Land 22 L/R",
		Callsign:               "JFK_APP",
		DepartureRunwayStrings: []string{"KJFK/22R", "KLGA/22", "KISP/24", "KFRG/19"},
		ArrivalRunwayStrings:   []string{"KJFK/22L", "KJFK/22R"},
		Wind:                   Wind{Direction: 190, Speed: 12, Gust: 17},
		Controllers:            []string{"JFK_APP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_B_CTR", "NY_C_CTR", "NY_F_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sa22.Name] = sa22

	sd22 := &Scenario{
		Name:                   "KJFK Depart 22R",
		Callsign:               "JFK_DEP",
		DepartureRunwayStrings: []string{"KJFK/22R", "KLGA/22", "KISP/24", "KFRG/19"},
		Wind:                   Wind{Direction: 190, Speed: 24, Gust: 0},
		Controllers:            []string{"JFK_DEP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_C_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sd22.Name] = sd22

	sa13x := &Scenario{
		Name:                   "KJFK Depart 13R Land 13L",
		Callsign:               "JFK_APP",
		DepartureRunwayStrings: []string{"KJFK/13R", "KLGA/13", "KISP/15R", "KFRG/14"},
		ArrivalRunwayStrings:   []string{"KJFK/13R"},
		Wind:                   Wind{Direction: 140, Speed: 12, Gust: 17},
		Controllers:            []string{"JFK_APP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_B_CTR", "NY_C_CTR", "NY_F_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sa13x.Name] = sa13x

	sa31 := &Scenario{
		Name:                   "KJFK Depart 31L Land 31L/31R",
		Callsign:               "JFK_APP",
		DepartureRunwayStrings: []string{"KJFK/31L", "KLGA/31", "KISP/33L", "KFRG/32"},
		ArrivalRunwayStrings:   []string{"KJFK/31R", "KJFK/31L"},
		Wind:                   Wind{Direction: 330, Speed: 12, Gust: 17},
		Controllers:            []string{"JFK_APP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_B_CTR", "NY_C_CTR", "NY_F_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sa31.Name] = sa31

	sd31 := &Scenario{
		Name:                   "KJFK Depart 31L",
		Callsign:               "JFK_DEP",
		DepartureRunwayStrings: []string{"KJFK/31L", "KLGA/31", "KISP/33L", "KFRG/32"},
		Wind:                   Wind{Direction: 330, Speed: 12, Gust: 17},
		Controllers:            []string{"JFK_DEP", "BOS_E_CTR", "ISP_APP", "JFK_TWR", "LGA_DEP", "NY_C_CTR", "NY_LE_DEP", "NY_LS_DEP"},
	}
	t.Scenarios[sd31.Name] = sd31

	return t
}

/*
func jfk31RDepartureRunway() *DepartureConfig {
	c := jfkDepartureRunway()
	delete(c.categoryEnabled, "Southwest")

	c.name = "31R"
	c.makeSpawner = func(config *DepartureConfig) *AircraftSpawner {
		var routeTemplates []RouteTemplate

		rp := jfkPropProto()

		if *config.categoryEnabled["Water"] {
			routeTemplates = append(routeTemplates, jfkWater.GetRouteTemplates(rp, "_JFK_31R _JFK_13L #090", "JFK5")...)
		}
		if *config.categoryEnabled["East"] {
			routeTemplates = append(routeTemplates, jfkEast.GetRouteTemplates(rp, "_JFK_31R _JFK_13L #090", "JFK5")...)
		}
		if *config.categoryEnabled["North"] {
			routeTemplates = append(routeTemplates, jfkNorth.GetRouteTemplates(rp, "_JFK_31R _JFK_13L #090", "JFK5")...)
		}

		return &AircraftSpawner{
			rate:           int(config.rate),
			challenge:      config.challenge,
			routeTemplates: routeTemplates,
		}
	}
	return c
}
*/

func loadVideoMaps() map[string]*VideoMap {
	maps := make(map[string]*VideoMap)

	// Initialize video maps from the embedded sector file (for now...)
	contents := decompressZstd(sectorFile)
	errorCallback := func(err string) {
		lg.Errorf("error parsing sector file: %s", err)
	}
	sectorFile, err := sct2.Parse([]byte(contents), "zny.sct2", errorCallback)
	if err != nil {
		panic(err)
	}

	include := false
	for _, sid := range sectorFile.SIDs {
		//lg.Errorf("SID %s", sid.Name)
		if sid.Name == "======== Outlines ========" {
			include = false
		}
		if include && sid.Name[0] != '=' {
			var segs []Point2LL
			for _, seg := range sid.Segs {
				p0 := Point2LL{float32(seg.Segment.P[0].Longitude), float32(seg.Segment.P[0].Latitude)}
				p1 := Point2LL{float32(seg.Segment.P[1].Longitude), float32(seg.Segment.P[1].Latitude)}
				if !p0.IsZero() && !p1.IsZero() {
					segs = append(segs, p0, p1)
				}
			}
			maps[sid.Name] = &VideoMap{
				Name:     sid.Name,
				Segments: segs,
			}
		}
		if sid.Name == "====== TRACON Maps =======" {
			include = true
		}
	}

	for _, star := range sectorFile.STARs {
		//lg.Errorf("STAR %s", star.Name)
		if star.Name == "========= Fixes ==========" || star.Name == "======== Text IDs ========" {
			include = false
		}
		if include && star.Name[0] != '=' {
			var segs []Point2LL
			for _, seg := range star.Segs {
				p0 := Point2LL{float32(seg.Segment.P[0].Longitude), float32(seg.Segment.P[0].Latitude)}
				p1 := Point2LL{float32(seg.Segment.P[1].Longitude), float32(seg.Segment.P[1].Latitude)}
				if !p0.IsZero() && !p1.IsZero() {
					segs = append(segs, p0, p1)
				}
			}
			maps[star.Name] = &VideoMap{
				Name:     star.Name,
				Segments: segs,
			}
		}
		if star.Name == "======== Airspace ========" || star.Name == "======= Geography ========" {
			include = true
		}
	}

	for _, vm := range maps {
		vm.InitializeCommandBuffer()
	}

	return maps
}

func JFKAirport() *Airport {
	ac := &Airport{
		ICAO:      "KJFK",
		Elevation: 13,
		Location:  Point2LL{-73.780968, 40.641766},
	}
	ac.NamedLocations = map[string]Point2LL{
		"_JFK_31L": mustParseLatLong("N040.37.41.000, W073.46.20.227"),
		"_JFK_31R": mustParseLatLong("N040.38.35.986, W073.45.31.503"),
		"_JFK_22R": mustParseLatLong("N040.39.00.362, W073.45.49.053"),
		"_JFK_22L": mustParseLatLong("N040.38.41.232, W073.45.18.511"),
		"_JFK_4L":  mustParseLatLong("N040.37.19.370, W073.47.08.045"),
		"_JFK_4La": mustParseLatLong("N040.39.21.332, W073.45.32.849"),
		"_JFK_4R":  mustParseLatLong("N040.37.31.661, W073.46.12.894"),
		"_JFK_13R": mustParseLatLong("N040.38.53.537, W073.49.00.188"),
		"_JFK_13L": mustParseLatLong("N040.39.26.976, W073.47.24.277"),
	}

	ac.ExitCategories = map[string]string{
		"WAVEY": "Water",
		"SHIPP": "Water",
		"HAPIE": "Water",
		"BETTE": "Water",
		"MERIT": "East",
		"GREKI": "East",
		"BAYYS": "East",
		"BDR":   "East",
		"DIXIE": "Southwest",
		"WHITE": "Southwest",
		"RBV":   "Southwest",
		"ARD":   "Southwest",
		"COATE": "North",
		"NEION": "North",
		"HAAYS": "North",
		"GAYEL": "North",
		"DEEZZ": "North",
	}

	ac.ArrivalRunwayNames = []string{"4L", "4R", "13L", "13R", "22L", "22R", "31L", "31R"}

	i4l := Approach{
		ShortName: "I4L",
		FullName:  "ILS 4 Left",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "AROKE", Altitude: 2000},
			Waypoint{Fix: "KRSTL", Altitude: 1500},
			Waypoint{Fix: "_JFK_4L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, i4l)

	i4r := Approach{
		ShortName: "I4R",
		FullName:  "ILS 4 Right",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "ZETAL", Altitude: 2000},
			Waypoint{Fix: "EBBEE", Altitude: 1500},
			Waypoint{Fix: "_JFK_4R", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, i4r)

	rz4l := Approach{
		ShortName: "R4L",
		FullName:  "RNAV Zulu 4 Left",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "REPRE", Altitude: 2000},
			Waypoint{Fix: "KRSTL", Altitude: 1500},
			Waypoint{Fix: "_JFK_4L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz4l)

	rz4r := Approach{
		ShortName: "R4R",
		FullName:  "RNAV Zulu 4 Right",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "VERRU", Altitude: 2000},
			Waypoint{Fix: "EBBEE", Altitude: 1500},
			Waypoint{Fix: "_JFK_4R", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz4r)

	i13l := Approach{
		ShortName: "I3L",
		FullName:  "ILS 13 Left",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "COVIR", Altitude: 3000},
			Waypoint{Fix: "KMCHI", Altitude: 2900},
			Waypoint{Fix: "BUZON", Altitude: 2900},
			Waypoint{Fix: "TELEX", Altitude: 2100},
			Waypoint{Fix: "CAXUN", Altitude: 1500},
			Waypoint{Fix: "UXHUB", Altitude: 680},
			Waypoint{Fix: "_JFK_13L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, i13l)

	rz13l := Approach{
		ShortName: "R3L",
		FullName:  "RNAV Zulu 13 Left",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "ASALT", Altitude: 3000, Speed: 210},
			Waypoint{Fix: "CNRSE", Altitude: 2000},
			Waypoint{Fix: "LEISA", Altitude: 1246},
			Waypoint{Fix: "SILJY", Altitude: 835},
			Waypoint{Fix: "ROBJE", Altitude: 456},
			Waypoint{Fix: "_JFK_13L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz13l)

	rz13r := Approach{
		ShortName: "R3R",
		FullName:  "RNAV Zulu 13 Right",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "ASALT", Altitude: 3000, Speed: 210},
			Waypoint{Fix: "NUCRI", Altitude: 2000},
			Waypoint{Fix: "PEEBO", Altitude: 921},
			Waypoint{Fix: "MAYMA", Altitude: 520},
			Waypoint{Fix: "_JFK_13R", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz13r)

	i22l := Approach{
		ShortName: "I2L",
		FullName:  "ILS 22 Left",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "CIMBL", Altitude: 14000},
				Waypoint{Fix: "HAIRR", Altitude: 14000},
				Waypoint{Fix: "CEEGL", Altitude: 10000},
				Waypoint{Fix: "TAPPR", Altitude: 8000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "ROSLY", Altitude: 3000},
				Waypoint{Fix: "ZALPO", Altitude: 1800},
				Waypoint{Fix: "_JFK_22L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "NRTON", Altitude: 10000},
				Waypoint{Fix: "SAJUL", Altitude: 10000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "ROSLY", Altitude: 3000},
				Waypoint{Fix: "ZALPO", Altitude: 1800},
				Waypoint{Fix: "_JFK_22L", Altitude: 50},
			}},
	}
	ac.Approaches = append(ac.Approaches, i22l)

	i22r := Approach{
		ShortName: "I2R",
		FullName:  "ILS 22 Right",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "CIMBL", Altitude: 14000},
				Waypoint{Fix: "HAIRR", Altitude: 14000},
				Waypoint{Fix: "CEEGL", Altitude: 10000},
				Waypoint{Fix: "TAPPR", Altitude: 8000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "CORVT", Altitude: 3000},
				Waypoint{Fix: "MATTR", Altitude: 1800},
				Waypoint{Fix: "_JFK_22R", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "NRTON", Altitude: 10000},
				Waypoint{Fix: "SAJUL", Altitude: 10000},
				Waypoint{Fix: "DETGY", Altitude: 7000},
				Waypoint{Fix: "HAUPT", Altitude: 6000},
				Waypoint{Fix: "LEFER", Altitude: 4000},
				Waypoint{Fix: "CORVT", Altitude: 3000},
				Waypoint{Fix: "MATTR", Altitude: 1900},
				Waypoint{Fix: "_JFK_22R", Altitude: 50},
			}},
	}
	ac.Approaches = append(ac.Approaches, i22r)

	r22l := Approach{
		ShortName: "R2L",
		FullName:  "RNAV X-Ray 22 Left",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "WIKOL", Altitude: 3000},
			Waypoint{Fix: "GIGPE", Altitude: 2900},
			Waypoint{Fix: "CAPIT", Altitude: 2900},
			Waypoint{Fix: "ENEEE", Altitude: 1700},
			Waypoint{Fix: "ZOSDO", Altitude: 800},
			Waypoint{Fix: "_JFK_22L", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, r22l)

	rz22r := Approach{
		ShortName: "R2R",
		FullName:  "RNAV Zulu 22 Right",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{[]Waypoint{
			Waypoint{Fix: "RIVRA", Altitude: 3000},
			Waypoint{Fix: "HENEB", Altitude: 1900},
			Waypoint{Fix: "_JFK_22R", Altitude: 50},
		}},
	}
	ac.Approaches = append(ac.Approaches, rz22r)

	i31l := Approach{
		ShortName: "I1L",
		FullName:  "ILS 31 Left",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "CHANT", Altitude: 2000},
				Waypoint{Fix: "ZACHS", Altitude: 2000},
				Waypoint{Fix: "MEALS", Altitude: 1500},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "DPK", Altitude: 2000},
				Waypoint{Fix: "ZACHS", Altitude: 2000},
				Waypoint{Fix: "MEALS", Altitude: 1500},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
		},
	}
	ac.Approaches = append(ac.Approaches, i31l)

	i31r := Approach{
		ShortName: "I1R",
		FullName:  "ILS 31 Right",
		Type:      ILSApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "CATOD", Altitude: 3000},
				Waypoint{Fix: "MALDE", Altitude: 3000},
				Waypoint{Fix: "ZULAB", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
		},
	}
	ac.Approaches = append(ac.Approaches, i31r)

	rz31l := Approach{
		ShortName: "R1L",
		FullName:  "RNAV Zulu 31 Left",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "SESKE"},
				Waypoint{Fix: "ZACHS", Altitude: 2000, Speed: 210},
				Waypoint{Fix: "CUVKU", Altitude: 1800},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "RISSY"},
				Waypoint{Fix: "ZACHS", Altitude: 2000, Speed: 210},
				Waypoint{Fix: "CUVKU", Altitude: 1800},
				Waypoint{Fix: "_JFK_31L", Altitude: 50},
			},
		},
	}
	ac.Approaches = append(ac.Approaches, rz31l)

	rz31r := Approach{
		ShortName: "R1R",
		FullName:  "RNAV Zulu 31 Right",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "PZULU"},
				Waypoint{Fix: "CATOD", Altitude: 3000, Speed: 210},
				Waypoint{Fix: "IGIDE", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "VIDIO"},
				Waypoint{Fix: "CATOD", Altitude: 3000, Speed: 210},
				Waypoint{Fix: "IGIDE", Altitude: 1900},
				Waypoint{Fix: "_JFK_31R", Altitude: 50},
			},
		},
	}
	ac.Approaches = append(ac.Approaches, rz31r)

	camrn4 := ArrivalGroup{
		Name: "CAMRN4",
		Rate: 15,
		Arrivals: []Arrival{
			Arrival{
				Name:              "CAMRN4",
				Waypoints:         mustParseWaypoints("N039.46.43.120,W074.03.15.529 KARRS @ CAMRN #041"),
				Route:             "/. CAMRN4",
				InitialController: "NY_B_CTR",
				InitialAltitude:   15000,
				ClearedAltitude:   11000,
				InitialSpeed:      300,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "WJA", Airport: "KDCA", Fleet: "jfk"},
					ArrivalAirline{ICAO: "WJA", Airport: "KORF", Fleet: "jfk"},
					ArrivalAirline{ICAO: "WJA", Airport: "KJAX", Fleet: "jfk"},
					ArrivalAirline{ICAO: "JBU", Airport: "KDFW"},
					ArrivalAirline{ICAO: "JBU", Airport: "KMCO"},
					ArrivalAirline{ICAO: "JBU", Airport: "KCLT"},
					ArrivalAirline{ICAO: "BWA", Airport: "MKJP"},
					ArrivalAirline{ICAO: "AAL", Airport: "KTUL"},
					ArrivalAirline{ICAO: "AAL", Airport: "KAUS"},
					ArrivalAirline{ICAO: "AAL", Airport: "KDEN"},
					ArrivalAirline{ICAO: "AMX", Airport: "MMMY", Fleet: "long"},
					ArrivalAirline{ICAO: "AMX", Airport: "MMMX", Fleet: "long"},
				},
			},
		},
	}
	ac.ArrivalGroups = append(ac.ArrivalGroups, camrn4)

	owenz := ArrivalGroup{
		Name: "OWENZ",
		Rate: 15,
		Arrivals: []Arrival{
			Arrival{
				Name:              "OWENZ",
				Waypoints:         mustParseWaypoints("N039.56.16.634,W073.30.51.937 N039.57.39.196,W073.37.16.486 @ CAMRN"),
				Route:             "/. OWENZ CAMRN",
				InitialController: "NY_B_CTR",
				InitialAltitude:   11000,
				ClearedAltitude:   9000,
				InitialSpeed:      300,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "AAL", Airport: "MDSD"},
					ArrivalAirline{ICAO: "AAL", Airport: "TXKF"},
					ArrivalAirline{ICAO: "JBU", Airport: "TXKF"},
					ArrivalAirline{ICAO: "JBU", Airport: "TJSJ"},
					ArrivalAirline{ICAO: "AAL", Airport: "TJSJ"},
					ArrivalAirline{ICAO: "AAL", Airport: "SBGR", Fleet: "long"},
					ArrivalAirline{ICAO: "TAM", Airport: "SBGR", Fleet: "long"},
				},
			},
		},
	}
	ac.ArrivalGroups = append(ac.ArrivalGroups, owenz)

	lendyign := ArrivalGroup{
		Name: "LENDY8/IGN1",
		Rate: 15,
		Arrivals: []Arrival{
			Arrival{
				Name:              "LENDY8",
				Waypoints:         mustParseWaypoints("N040.56.09.863,W074.30.33.013 N040.55.09.974,W074.25.19.628 @ LENDY #135"),
				Route:             "/. LENDY8",
				InitialController: "NY_C_CTR",
				InitialAltitude:   20000,
				ClearedAltitude:   19000,
				InitialSpeed:      300,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "ASA", Airport: "KSFO"},
					ArrivalAirline{ICAO: "ASA", Airport: "KPDX"},
					ArrivalAirline{ICAO: "DAL", Airport: "KMSP"},
					ArrivalAirline{ICAO: "DAL", Airport: "KDTW"},
					ArrivalAirline{ICAO: "AAL", Airport: "KORD"},
					ArrivalAirline{ICAO: "UPS", Airport: "KLAS"},
					ArrivalAirline{ICAO: "DAL", Airport: "KSEA"},
					ArrivalAirline{ICAO: "AAL", Airport: "KLAX"},
					ArrivalAirline{ICAO: "UAL", Airport: "KSFO"},
					ArrivalAirline{ICAO: "AAL", Airport: "KSFO"},
					ArrivalAirline{ICAO: "CPA", Airport: "VHHH", Fleet: "cargo"},
					ArrivalAirline{ICAO: "ANA", Airport: "RJAA", Fleet: "long"},
					ArrivalAirline{ICAO: "KAL", Airport: "RKSI", Fleet: "long"},
					ArrivalAirline{ICAO: "WJA", Airport: "KIND", Fleet: "jfk"},
					ArrivalAirline{ICAO: "WJA", Airport: "KCVG", Fleet: "jfk"},
				},
			},
			Arrival{
				Name:              "IGN1",
				Waypoints:         mustParseWaypoints("DOORE N040.58.27.742,W074.16.12.647 @ LENDY #135"),
				Route:             "/. IGN1",
				InitialController: "NY_C_CTR",
				InitialAltitude:   19000,
				ClearedAltitude:   19000,
				InitialSpeed:      300,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "ASA", Airport: "KSFO"},
					ArrivalAirline{ICAO: "ASA", Airport: "KPDX"},
					ArrivalAirline{ICAO: "DAL", Airport: "KMSP"},
					ArrivalAirline{ICAO: "DAL", Airport: "KDTW"},
					ArrivalAirline{ICAO: "AAL", Airport: "KORD"},
					ArrivalAirline{ICAO: "UPS", Airport: "KLAS"},
					ArrivalAirline{ICAO: "DAL", Airport: "KSEA"},
					ArrivalAirline{ICAO: "AAL", Airport: "KLAX"},
					ArrivalAirline{ICAO: "UAL", Airport: "KSFO"},
					ArrivalAirline{ICAO: "AAL", Airport: "KSFO"},
					ArrivalAirline{ICAO: "ACA", Airport: "CYYC"},
					ArrivalAirline{ICAO: "ACA", Airport: "CYUL", Fleet: "short"},
				},
			},
		},
	}
	ac.ArrivalGroups = append(ac.ArrivalGroups, lendyign)

	debug := ArrivalGroup{
		Name: "DEBUG",
		Rate: 20,
		Arrivals: []Arrival{
			Arrival{
				Name:              "DEBUG",
				Waypoints:         mustParseWaypoints("N040.20.22.874,W073.48.09.981 N040.21.34.834,W073.51.11.997 @ #360"),
				Route:             "/. DEBUG",
				InitialController: "NY_F_CTR",
				InitialAltitude:   3000,
				ClearedAltitude:   2000,
				InitialSpeed:      250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "UAL", Airport: "KMSP"},
				},
			},
		},
	}
	if *devmode {
		ac.ArrivalGroups = append(ac.ArrivalGroups, debug)
	}

	parch3 := ArrivalGroup{
		Name: "PARCH3/ROBER2",
		Rate: 15,
		Arrivals: []Arrival{
			Arrival{
				Name:      "PARCH3",
				Waypoints: mustParseWaypoints("N040.58.19.145,W072.40.15.921 N040.56.23.940,W072.45.54.299 @ CCC ROBER #278"),
				RunwayWaypoints: map[string]WaypointArray{
					"22L": mustParseWaypoints("N040.58.19.145,W072.40.15.921 N040.56.23.940,W072.45.54.299 @ CCC ROBER CRAIL #302"),
					"22R": mustParseWaypoints("N040.58.19.145,W072.40.15.921 N040.56.23.940,W072.45.54.299 @ CCC ROBER CRAIL #302"),
				},
				Route:             "/. PARCH3",
				InitialController: "BOS_E_CTR",
				InitialAltitude:   13000,
				ClearedAltitude:   12000,
				InitialSpeed:      275,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "AAL", Airport: "CYYZ", Fleet: "short"},
					ArrivalAirline{ICAO: "AFR", Airport: "LPFG", Fleet: "long"},
					ArrivalAirline{ICAO: "BAW", Airport: "EGLL", Fleet: "long"},
					ArrivalAirline{ICAO: "CLX", Airport: "EGCC"},
					ArrivalAirline{ICAO: "DAL", Airport: "KBOS", Fleet: "short"},
					ArrivalAirline{ICAO: "DLH", Airport: "EDDF", Fleet: "long"},
					ArrivalAirline{ICAO: "GEC", Airport: "EDDF"},
					ArrivalAirline{ICAO: "DLH", Airport: "EDDM", Fleet: "long"},
					ArrivalAirline{ICAO: "GEC", Airport: "EDDM"},
					ArrivalAirline{ICAO: "EIN", Airport: "EIDW", Fleet: "long"},
					ArrivalAirline{ICAO: "ELY", Airport: "LLBG", Fleet: "jfk"},
					ArrivalAirline{ICAO: "FIN", Airport: "EFHK", Fleet: "long"},
					ArrivalAirline{ICAO: "GEC", Airport: "EDDF"},
					ArrivalAirline{ICAO: "IBE", Airport: "LEBL", Fleet: "long"},
					ArrivalAirline{ICAO: "IBE", Airport: "LEMD", Fleet: "long"},
					ArrivalAirline{ICAO: "JBU", Airport: "KBOS"},
					ArrivalAirline{ICAO: "KLM", Airport: "EHAM", Fleet: "long"},
					ArrivalAirline{ICAO: "QXE", Airport: "KBGR", Fleet: "short"},
					ArrivalAirline{ICAO: "QXE", Airport: "KPVD", Fleet: "short"},
					ArrivalAirline{ICAO: "UAE", Airport: "OMDB", Fleet: "loww"},
					ArrivalAirline{ICAO: "UAL", Airport: "CYYZ", Fleet: "short"},
					ArrivalAirline{ICAO: "UAL", Airport: "KBOS", Fleet: "short"},
					ArrivalAirline{ICAO: "UPS", Airport: "KBOS"},
					ArrivalAirline{ICAO: "VIR", Airport: "EGCC", Fleet: "long"},
				},
			},
			Arrival{
				Name:              "ROBER2",
				Waypoints:         mustParseWaypoints("N040.58.19.145,W072.40.15.921 N040.56.23.940,W072.45.54.299 @ CCC ROBER #276"),
				Route:             "/. ROBER2",
				InitialController: "BOS_E_CTR",
				InitialAltitude:   13000,
				ClearedAltitude:   12000,
				InitialSpeed:      275,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "QXE", Airport: "KBGR", Fleet: "short"},
					ArrivalAirline{ICAO: "QXE", Airport: "KPVD", Fleet: "short"},
					ArrivalAirline{ICAO: "FDX", Airport: "KMHT", Fleet: "short"},
					ArrivalAirline{ICAO: "EJA", Airport: "KMHT"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KFMH"},
				},
			},
		},
	}
	ac.ArrivalGroups = append(ac.ArrivalGroups, parch3)

	// TODO? PAWLING2 (turboprop <= 250KT)

	ac.Departures = []Departure{
		// Europe
		// Charles De Gaulle
		Departure{
			Exit:        "HAPIE",
			Route:       "YAHOO WHALE N251A JOOPY NATZ MALOT NATZ GISTI LESLU M142 LND N160 NAKID M25 ANNET UM25 UVSUV UM25 INGOR UM25 LUKIP LUKIP9E",
			Destination: "LPFG",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AFR", Fleet: "long"}},
		},
		// Heathrow
		Departure{
			Exit:        "MERIT",
			Route:       "HFD PUT WITCH ALLEX N379A ALLRY NATU SOVED LUTOV KELLY L10 WAL UY53 NUGRA",
			Destination: "EGLL",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "BAW", Fleet: "long"},
				DepartureAirline{ICAO: "VIR"},
			},
		},
		// Manchester
		Departure{
			Exit:        "BETTE",
			Route:       "ACK KANNI N139A PORTI 4700N/05000W 5000N/04000W 5200N/03000W 5300N/02000W MALOT GISTI PELIG BOFUM Q37 MALUD MALUD1M",
			Destination: "EGCC",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "BAW", Fleet: "long"}},
		},
		// Istanbul
		Departure{
			Exit:        "MERIT",
			Route:       "HFD PUT EBONY ALLRY 5100N/05000W 5300N/04000W 5500N/03000W 5600N/02000W PIKIL SOVED NIBOG BELOX L603 LAMSO PETIK NOMKA OMELO GOLOP KOZLI ROMIS PITOK BADOR RONBU BUVAK RIXEN",
			Destination: "LTFM",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "THY", Fleet: "long"}},
		},
		// Edinburgh
		Departure{
			Exit:        "BETTE",
			Route:       "ACK TUSKY N291A IBERG NATW NEBIN NATW OLGON MOLAK BRUCE L602 CLYDE STIRA",
			Destination: "EGPH",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "VIR"},
				DepartureAirline{ICAO: "FDX", Fleet: "long"},
			},
		},
		// Warsaw
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS MARTN TOPPS N499A RIKAL 5300N/05000W 5500N/04000W 5700N/03000W 5700N/02000W GOMUP GINGA TIR REKNA PETIL BAVTA BAKLI KEKOV GOSOT NASOK N195 SORIX",
			Destination: "EPWA",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "LOT", Fleet: "long"}},
		},
		// Madrid
		Departure{
			Exit:        "BETTE",
			Route:       "ACK VITOL N189A NICSO 4800N/05000W 4900N/04000W 4900N/03000W 4700N/02000W PASAS STG DESAT UN733 ZMR",
			Destination: "LEMD",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "AAL", Fleet: "long"},
				DepartureAirline{ICAO: "IBE", Fleet: "long"},
			},
		},
		// Amsterdam
		Departure{
			Exit:        "BETTE",
			Route:       "ACK KANNI N317A ELSIR NATY DOGAL NATY BEXET BOYNE DIBAL L603 LAMSO",
			Destination: "EHAM",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "KLM", Fleet: "long"}},
		},
		// Frankfurt
		Departure{
			Exit:        "BETTE",
			Route:       "ACK BRADD N255A JOOPY NATX MALOT NATX GISTI UNBEG SHA SLANY UL9 KONAN UL607 SPI T180 TOBOP T180 NIVNU T180 UNOKO UNOK3A",
			Destination: "EDDF",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "DLH", Fleet: "a359"},
				DepartureAirline{ICAO: "GEC"}},
		},
		// Barcelona (lebl)
		Departure{
			Exit:        "HAPIE",
			Route:       "YAHOO DOVEY 4200N/06000W 4200N/05000W 4300N/04000W 4400N/03000W 4400N/02000W MUDOS STG SUSOS UN725 YAKXU UN725 LOBAR",
			Destination: "LEBL",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "IBE", Fleet: "long"}},
		},
		// Rome
		Departure{
			Exit:        "BETTE",
			Route:       "ACK BRADD N255A JOOPY NATX MALOT NATX GISTI SLANY UL9 BIG L15 MOTOX UL15 RANUX UL15 NEBAX LASAT DEVDI ODINA SRN EKPAL Q705 XIBIL XIBIL3A",
			Destination: "LIRF",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AAL", Fleet: "long"}},
		},
		// Helsinki
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS MARTN DANOL N481A SAXAN 5300N/05000W 5600N/04000W 5900N/03000W 6100N/02000W 6200N/01000W IPTON UXADA Y349 AMROT Y362 LAKUT",
			Destination: "EFHK",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "FIN", Fleet: "long"}},
		},
		// Dublin
		Departure{
			Exit:        "MERIT",
			Route:       "HFD PUT BOS TUSKY N321A ELSIR NATV RESNO NATV NETKI OLAPO OLAPO3X",
			Destination: "EIDW",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EIN", Fleet: "long"}},
		},

		// Central/South America (ish)
		// Mexico city
		Departure{
			Exit:        "RBV",
			Route:       "Q430 COPES Q75 GVE LYH COLZI AWYAT IPTAY CHOPZ MGM SJI TBD M575 KENGS M345 AXEXO UM345 PAZ UT154 ENAGA ENAGA2A",
			Destination: "MMMX",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "AAL", Fleet: "long"},
				DepartureAirline{ICAO: "AMX", Fleet: "long"}},
		},
		// Sao Paulo
		Departure{
			Exit:        "SHIPP",
			Route:       "Y492 SQUAD DARUX L459 KEEKA L329 ZPATA UL329 KORTO ETATA UP535 MOMSO UZ24 DOLVI UL452 GELVA UZ6 ISOPI UZ6 NIMKI UZ38 VUNOX VUNOX1A",
			Destination: "SBGR",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AAL", Fleet: "long"}},
		},

		// Caribbean
		// San Juan
		Departure{
			Exit:        "SHIPP",
			Route:       "Y489 RESQU SKPPR L455 KINCH L455 LENNT M423 PLING RTE7 SAALR",
			Destination: "TJSJ",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AAL"}},
		},
		// Bermuda
		Departure{
			Exit:        "SHIPP",
			Route:       "Y487 KINGG KINER L461 BOVIC MOMOM1",
			Destination: "TXKF",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "AAL"},
				DepartureAirline{ICAO: "DAL"},
				DepartureAirline{ICAO: "JBU"},
			},
		},
		// Kingston
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP IDOLS RROOO Y323 CARPX Y307 ENAMO NEFTU UP525 EMABU UA301 IMADI SAVEM",
			Destination: "MKJP",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "JBU"},
				DepartureAirline{ICAO: "BWA", Fleet: "b738"}},
		},
		// St. Thomas
		Departure{
			Exit:        "SHIPP",
			Route:       "Y492 SQUAD DARUX L456 HANCY L456 THANK JETSS",
			Destination: "TIST",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AAL"}},
		},
		// Antigua
		Departure{
			Exit:        "SHIPP",
			Route:       "Y487 KINGG KINER L461 BOVIC PIREX L462 ANU",
			Destination: "TAPA",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "AAL"},
				DepartureAirline{ICAO: "DAL"}},
		},

		// Misc US routes
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 ORF J121 CHS ESENT LUNNI1",
			Destination: "KJAX",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "ATN"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE NOSIK Q812 ZOHAN IDIOM MUSCL3",
			Destination: "KMSP",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "UPS"},
				DepartureAirline{ICAO: "AAL"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "HYPER8",
			Destination: "KIAD",
			Altitude:    22000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "Q430 COPES Q75 GVE LYH CHSLY5",
			Destination: "KCLT",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AAL"}},
		},
		Departure{
			Exit:        "DIXIE",
			Route:       "V276 RBV V249 SBJ",
			Destination: "KTEB",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}},
		},
		Departure{
			Exit:        "DIXIE",
			Route:       "V16 VCN V184 OOD",
			Destination: "KPHL",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}},
		},
		Departure{
			Exit:        "WHITE",
			Route:       "V276 RBV V249 SBJ",
			Destination: "KTEB",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}},
		},
		Departure{
			Exit:        "COATE",
			Route:       "Q436 EMMMA WYNDE2",
			Destination: "KORD",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "AAL"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "J95 CFB TRAAD JACCI FERRL2",
			Destination: "KDTW",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO CHIEZ Y291 MAJIK CUUDA2",
			Destination: "KFLL",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE RUBKI ASP TVC KP87I FSD KP81C BFF KD60S HVE GGAPP CHOWW2",
			Destination: "KLAS",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP Y313 HOAGG BNFSH2",
			Destination: "KMIA",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "AAL"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 WARNN ZJAAY TAQLE1",
			Destination: "KRDU",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "DAL"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 ORF J121 CHS IGARY Q85 LPERD GTOUT1",
			Destination: "KMCO",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "Q430 SAAME J6 HVQ Q68 BWG BLUZZ3",
			Destination: "KMEM",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "FDX", Fleet: "b752"},
				DepartureAirline{ICAO: "FDX", Fleet: "b763"},
			},
		},
		Departure{
			Exit:        "NEION",
			Route:       "J223 CORDS J132 ULW BENEE",
			Destination: "KBUF",
			Altitude:    18000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "WJA", Fleet: "jfk"},
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CCV",
			Destination: "KORF",
			Altitude:    26000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "Q430 SAAME J6 HVQ Q68 YOCKY GROAT PASLY4",
			Destination: "KBNA",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "UPS"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO YLEEE ZILLS Y289 DULEE CLMNT2",
			Destination: "KPBI",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		}, // west palm beach
		Departure{
			Exit:        "BDR",
			Route:       "CARLD V188 GON PVD",
			Destination: "KPVD",
			Altitude:    9000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "COATE",
			Route:       "V116 LVZ V613 FJC",
			Destination: "KABE",
			Altitude:    12000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "WJA", Fleet: "jfk"},
			},
		},
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS CAM ENE",
			Destination: "KBGR",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE SSM YQT VBI YWG LIVBI DUKPO FAREN YDR VLN J500 YYN MEDAK ROPLA YXC GLASR1",
			Destination: "KSEA",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "UPS"},
				DepartureAirline{ICAO: "JBU"},
				DepartureAirline{ICAO: "DAL"},
			},
		},
		Departure{
			Exit:        "RBV",
			Route:       "Q430 BYRDD J48 MOL FLASK REAVS ODF THRSR GRGIA SJI SLIDD2",
			Destination: "KMSY",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "DAL"},
			},
		},
		Departure{
			Exit:        "DEEZZ",
			Route:       "CANDR J60 PSB HAYNZ7",
			Destination: "KPIT",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "DEEZZ",
			Route:       "CANDR J60 PSB UPPRR TRYBE4",
			Destination: "KCLE",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
				DepartureAirline{ICAO: "ATN"},
			},
		},
		Departure{
			Exit:        "NEION",
			Route:       "J223 CORDS CFB V29 SYR",
			Destination: "KSYR",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "WJA"},
			},
		},
		Departure{
			Exit:        "GAYEL",
			Route:       "Q812 ARRKK Q812 SYR TULEG YYB SPALD YTL YGX IKLIX YHY 6100N/13000W JAGIT NCA13 TMSON PTERS3",
			Destination: "PANC",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "CAL", Fleet: "long"},
			},
		},
		Departure{
			Exit:        "HAAYS",
			Route:       "J223 CORDS ULW GIBBE",
			Destination: "KROC",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "WJA"},
			},
		},
		Departure{
			Exit:        "BAYYS",
			Route:       "SEALL V188 GON V374 MINNK",
			Destination: "KPVD",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "EJA"},
			},
		},

		// Canadia
		// Toronto
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE LINNG3",
			Destination: "CYYZ",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "AAL"},
				DepartureAirline{ICAO: "ACA"},
			},
		},
		// Montreal
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS CAM PBERG CARTR4",
			Destination: "CYUL",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "AAL"},
				DepartureAirline{ICAO: "ACA"},
			},
		},
		// Calgary
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE ASP RIMBE WIEDS 4930N/10000W GUDOG PIKLA BIRKO5",
			Destination: "CYYC",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "AAL"},
				DepartureAirline{ICAO: "ACA"},
			},
		},

		// Middle East
		// Doha (qatari air)
		Departure{
			Exit:        "BETTE",
			Route:       "ACK WHALE N193A NICSO NATY ELSOX MAPAG LESLU L180 MERLY M140 DVR UL9 KONAN UL607 KOK MATUG BOMBI DETEV INBED LAMSI STEIN INVED RASUB RIXEN UA17 BAG UL614 EZS UT36 ULTED UT301 BOTAS UT301 DEPSU UT301 DURSI UT301 KAVAM UT301 MIDSI R659 VEDED R659 VELAM Z225 BAYAN",
			Destination: "OTBD",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "QTR", Fleet: "jfk"}},
		},
		// Tel Aviv
		Departure{
			Exit:        "HAPIE",
			Route:       "YAHOO DOVEY 4200N/06000W 4400N/05000W 4700N/04000W 4900N/03000W 5000N/02000W SOMAX ATSUR TAKAS ALUTA KORER UM616 TUPAR DIDRU BEBIX VALKU LERGA TOZOT UT183 OTROT UM728 DOKAR SUNEV LAT SIPRO PAPIZ PINDO LINRO UL52 VAXOS UN134 VANZA PIKOG L609 ZUKKO AMMOS1C",
			Destination: "LLBG",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "ELY", Fleet: "jfk"}},
		},
		// Dubai
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS BAF KB87E MIILS N563A NEEKO 5500N/05000W 6000N/04000W 6300N/03000W 6400N/02000W 6300N/01000W GUNPA RASVI KOSEB PEROM BINKA ROVEK KELEL BADOR ARGES ARTAT UP975 UNVUS UP975 EZS UG8 OTKEP UM688 RATVO UM688 SIDAD P975 SESRU M677 RABAP M677 IVIVI M677 UKNEP M677 DEGSO M677 OBNET M677 LUDAM M677 VUTEB",
			Destination: "OMDB",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "UAE", Fleet: "a388"}},
		},

		// Far East
		// Hong Kong
		Departure{
			Exit:        "GREKI",
			Route:       "JUDDS BAF KJOHN CEFOU YBC KETLA 6700N/05000W 7200N/04000W 7800N/02000W 8000N/00000E 8000N/02000E 8000N/03000E PIREL N611 DOSON R705 OKASA R705 BRT G490 SERNA Y520 POLHO G218 TMR B458 VERUX B458 DADGA W37 OMBEB R473 BEMAG R473 WYN W18 SANIP W18 NLG W23 ZUH R473 SIERA",
			Destination: "VHHH",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "CPA"}},
		},
		// Seoul
		Departure{
			Exit:        "GAYEL",
			Route:       "Q812 MSLIN Q812 SYR RAKAM 4800N/08000W 5400N/09000W 5700N/10000W 5900N/11000W HOGAR GUDEN DEEJA LAIRE KODNE CHUUK R341 HAVAM R341 NATES R220 NUBDA R220 NODAN R217 ASTER Y514 SDE Y512 GTC L512 TENAS Y437 KAE Y697 KARBU",
			Destination: "RKSI",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "KAL", Fleet: "jumbo"}},
		},
		// Tokyo Narita
		Departure{
			Exit:        "GAYEL",
			Route:       "Q818 WOZEE Q917 DUTEL Q917 SSM YRL 5400N/10000W 5800N/11000W 6000N/12000W 6100N/13000W IGSOM OMSUN NCA20 ELLAM TED NODLE R220 NOSHO R220 NIKLL R220 NIPPI R220 NOGAL R220 NANAC Y810 OLDIV Y809 SUPOK",
			Destination: "RJAA",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "ANA", Fleet: "a380"}},
		},

		// India
		// New Delhi
		Departure{
			Exit:        "BETTE",
			Route:       "ACK KANNI N195A NICSO NATY MALOT NATY GISTI EVRIN L607 NUMPO L607 KONAN UL607 MATUG BOMBI TENLO LAMSI STEIN TEGRI BULEN L742 RIXEN UA17 YAVRU UA4 ERZ UN161 YAVUZ UN161 INDUR N161 EDATA M11 RODAR A909 BABUM A477 BUPOR B198 OGNOB G555 USETU G500 BUTRA L181 POMIR G500 FIRUZ P500 PS T400 SULOM A466 IGINO IGIN5A",
			Destination: "VIDP",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AIC", Fleet: "b788"}},
		},
	}

	ac.DepartureRunways = []*DepartureRunway{
		&DepartureRunway{
			Runway:   "31L",
			Rate:     45,
			Altitude: 13,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"SHIPP": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"HAPIE": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"BETTE": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},

				"MERIT": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"GREKI": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"BAYYS": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"BDR":   ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},

				"DIXIE": ExitRoute{InitialRoute: "SKORR5.RNGRR", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR METSS RNGRR #223"), ClearedAltitude: 5000},
				"WHITE": ExitRoute{InitialRoute: "SKORR5.RNGRR", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR METSS RNGRR #223"), ClearedAltitude: 5000},
				"RBV":   ExitRoute{InitialRoute: "SKORR5.RNGRR", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR METSS RNGRR #223"), ClearedAltitude: 5000},
				"ARD":   ExitRoute{InitialRoute: "SKORR5.RNGRR", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR METSS RNGRR #223"), ClearedAltitude: 5000},

				"COATE": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"NEION": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"HAAYS": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"GAYEL": ExitRoute{InitialRoute: "SKORR5.YNKEE", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_JFK_31L _JFK_13R SKORR CESID YNKEE #172"), ClearedAltitude: 5000},
			},
		},
		&DepartureRunway{
			Runway:   "22R",
			Rate:     45,
			Altitude: 13,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"SHIPP": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"HAPIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"BETTE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},

				"MERIT": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"GREKI": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"BAYYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"BDR":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},

				"DIXIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"WHITE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"RBV":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"ARD":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},

				"COATE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"NEION": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"HAAYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"GAYEL": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_JFK_22R _JFK_4L #222"), ClearedAltitude: 5000},
			},
		},
		&DepartureRunway{
			Runway:   "13R",
			Rate:     45,
			Altitude: 13,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #170"), ClearedAltitude: 5000},
				"SHIPP": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #170"), ClearedAltitude: 5000},
				"HAPIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #155"), ClearedAltitude: 5000},
				"BETTE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #155"), ClearedAltitude: 5000},

				"MERIT": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"GREKI": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"BAYYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"BDR":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},

				"DIXIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #185"), ClearedAltitude: 5000},
				"WHITE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #185"), ClearedAltitude: 5000},
				"RBV":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #185"), ClearedAltitude: 5000},
				"ARD":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #185"), ClearedAltitude: 5000},

				"COATE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"NEION": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"HAAYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"GAYEL": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #110"), ClearedAltitude: 5000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_JFK_13R _JFK_31L #109"), ClearedAltitude: 5000},
			},
		},
		&DepartureRunway{
			Runway:   "4L",
			Rate:     45,
			Altitude: 13,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"SHIPP": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"HAPIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"BETTE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},

				"MERIT": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"GREKI": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"BAYYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"BDR":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},

				"DIXIE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"WHITE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"RBV":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"ARD":   ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},

				"COATE": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"NEION": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"HAAYS": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"GAYEL": ExitRoute{InitialRoute: "JFK5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_JFK_4L _JFK_4La #100"), ClearedAltitude: 5000},
			},
		},
	}

	return ac
}

func LGAAirport() *Airport {
	lga := &Airport{
		ICAO:      "KLGA",
		Elevation: 7,
		Location:  Point2LL{-73.8739659, 40.7769271},
	}
	lga.NamedLocations = map[string]Point2LL{
		"_LGA_13":  mustParseLatLong("N040.46.56.029, W073.52.42.359"),
		"_LGA_13a": mustParseLatLong("N040.48.06.479, W073.55.40.914"),
		"_LGA_31":  mustParseLatLong("N040.46.19.788, W073.51.25.949"),
		"_LGA_31a": mustParseLatLong("N040.45.34.950, W073.49.52.922"),
		"_LGA_31b": mustParseLatLong("N040.48.50.809, W073.46.42.200"),
		"_LGA_22":  mustParseLatLong("N040.47.06.864, W073.52.14.811"),
		"_LGA_22a": mustParseLatLong("N040.51.18.890, W073.49.30.483"),
		"_LGA_4":   mustParseLatLong("N040.46.09.447, W073.53.02.574"),
		"_LGA_4a":  mustParseLatLong("N040.44.56.662, W073.51.53.497"),
		"_LGA_4b":  mustParseLatLong("N040.47.59.557, W073.47.11.533"),
	}

	lga.ExitCategories = map[string]string{
		"WAVEY": "Water",
		"SHIPP": "Water",
		"HAPIE": "Water",
		"BETTE": "Water",
		"DIXIE": "Southwest",
		"WHITE": "Southwest",
		"RBV":   "Southwest",
		"ARD":   "Southwest",
	}

	lga.Departures = []Departure{
		// Caribbean
		// San Juan
		Departure{
			Exit:        "SHIPP",
			Route:       "Y489 RESQU SKPPR L455 KINCH L455 LENNT M423 PLING RTE7 SAALR",
			Destination: "TJSJ",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AAL"}},
		},
		// Bermuda
		Departure{
			Exit:        "SHIPP",
			Route:       "Y487 KINGG KINER L461 BOVIC MOMOM1",
			Destination: "TXKF",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "AAL"},
				DepartureAirline{ICAO: "DAL"},
				DepartureAirline{ICAO: "JBU"},
			},
		},
		// Kingston
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP IDOLS RROOO Y323 CARPX Y307 ENAMO NEFTU UP525 EMABU UA301 IMADI SAVEM",
			Destination: "MKJP",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "JBU"},
				DepartureAirline{ICAO: "BWA", Fleet: "b738"}},
		},
		// St. Thomas
		Departure{
			Exit:        "SHIPP",
			Route:       "Y492 SQUAD DARUX L456 HANCY L456 THANK JETSS",
			Destination: "TIST",
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "AAL"}},
		},
		// Antigua
		Departure{
			Exit:        "SHIPP",
			Route:       "Y487 KINGG KINER L461 BOVIC PIREX L462 ANU",
			Destination: "TAPA",
			Airlines: []DepartureAirline{DepartureAirline{ICAO: "AAL"},
				DepartureAirline{ICAO: "DAL"}},
		},

		// Misc US routes
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 ORF J121 CHS ESENT LUNNI1",
			Destination: "KJAX",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "ATN"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO CHIEZ Y291 MAJIK CUUDA2",
			Destination: "KFLL",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP Y313 HOAGG BNFSH2",
			Destination: "KMIA",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "AAL"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 WARNN ZJAAY TAQLE1",
			Destination: "KRDU",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "DAL"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 ORF J121 CHS IGARY Q85 LPERD GTOUT1",
			Destination: "KMCO",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CCV",
			Destination: "KORF",
			Altitude:    26000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "DAL", Fleet: "short"},
			},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO YLEEE ZILLS Y289 DULEE CLMNT2",
			Destination: "KPBI",
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "WHITE",
			Route:       "J209 SBY V1 CCV",
			Destination: "KORF",
			Altitude:    7000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "QXE", Fleet: "short"},
				DepartureAirline{ICAO: "FDX", Fleet: "short"},
				DepartureAirline{ICAO: "WEN"},
			},
		},
		Departure{
			Exit:        "WHITE",
			Route:       "J209 SBY ISO RAPZZ AMYLU3",
			Destination: "KCHS",
			Altitude:    7000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "QXE", Fleet: "short"},
				DepartureAirline{ICAO: "FDX", Fleet: "short"},
				DepartureAirline{ICAO: "WEN"},
			},
		},
	}

	lga.DepartureRunways = []*DepartureRunway{
		&DepartureRunway{
			Runway:   "31",
			Rate:     30,
			Altitude: 20,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 8000},
				"SHIPP": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 8000},
				"HAPIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 8000},
				"BETTE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 8000},

				"DIXIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 6000},
				"WHITE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_31 _LGA_13 _LGA_13a @ JFK"), ClearedAltitude: 6000},
			},
		},
		&DepartureRunway{
			Runway:   "22",
			Rate:     30,
			Altitude: 20,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 8000},
				"SHIPP": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 8000},
				"HAPIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 8000},
				"BETTE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 8000},

				"DIXIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 6000},
				"WHITE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_22 _LGA_4 _LGA_4a _LGA_4b @ JFK"), ClearedAltitude: 6000},
			},
		},
		&DepartureRunway{
			Runway:   "13",
			Rate:     30,
			Altitude: 20,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 8000},
				"SHIPP": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 8000},
				"HAPIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 8000},
				"BETTE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 8000},

				"DIXIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 6000},
				"WHITE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_13 _LGA_31 _LGA_31a _LGA_31b @ JFK"), ClearedAltitude: 6000},
			},
		},
		&DepartureRunway{
			Runway:   "4",
			Rate:     30,
			Altitude: 20,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 8000},
				"SHIPP": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 8000},
				"HAPIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 8000},
				"BETTE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 8000},

				"DIXIE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 6000},
				"WHITE": ExitRoute{InitialRoute: "LGA7", Waypoints: mustParseWaypoints("_LGA_4 _LGA_22 _LGA_22a @ JFK"), ClearedAltitude: 6000},
			},
		},
	}

	/* TODO
	if *config.categoryEnabled["Southwest Props"] {
		// WHITE Props
		rp := proto
		rp.ClearedAltitude = 7000
		rp.Fleet = "short"
		rp.Airlines = []string{"QXE", "BWA", "FDX"}
		routeTemplates = append(routeTemplates, jfkWHITE.GetRouteTemplates(rp, way, "LGA7")...)
	}
	*/

	return lga
}

func ISPAirport() *Airport {
	isp := &Airport{
		ICAO:      "KISP",
		Elevation: 87,
		Location:  Point2LL{-73.1003215, 40.7972401},
	}
	isp.NamedLocations = map[string]Point2LL{
		"_ISP_6":    mustParseLatLong("N040.47.18.743, W073.06.44.022"),
		"_ISP_6a":   mustParseLatLong("N040.50.43.281, W073.02.11.698"),
		"_ISP_6b":   mustParseLatLong("N040.50.28.573, W073.09.10.827"),
		"_ISP_24":   mustParseLatLong("N040.48.06.643, W073.05.39.202"),
		"_ISP_24a":  mustParseLatLong("N040.45.56.414, W073.08.58.879"),
		"_ISP_24b":  mustParseLatLong("N040.47.41.032, W073.06.08.371"),
		"_ISP_24c":  mustParseLatLong("N040.48.48.350, W073.07.30.466"),
		"_ISP_15R":  mustParseLatLong("N040.48.05.462, W073.06.24.356"),
		"_ISP_15Ra": mustParseLatLong("N040.45.33.934, W073.02.36.555"),
		"_ISP_15Rb": mustParseLatLong("N040.49.18.755, W073.03.43.379"),
		"_ISP_15Rc": mustParseLatLong("N040.48.34.288, W073.09.11.211"),
		"_ISP_33L":  mustParseLatLong("N040.47.32.819, W073.05.41.702"),
		"_ISP_33La": mustParseLatLong("N040.49.52.085, W073.08.43.141"),
		"_ISP_33Lb": mustParseLatLong("N040.49.21.515, W073.06.31.250"),
		"_ISP_33Lc": mustParseLatLong("N040.48.20.019, W073.10.31.686"),
	}

	isp.ExitCategories = map[string]string{
		"COATE": "North",
		"NEION": "North",
		"HAAYS": "North",
		"GAYEL": "North",
		"DEEZZ": "North",
	}

	isp.Departures = []Departure{
		Departure{
			Exit:        "NEION",
			Route:       "J223 CORDS J132 ULW BENEE",
			Destination: "KBUF",
			Altitude:    16000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "WJA", Fleet: "jfk"},
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "MERIT",
			Route:       "MERIT ORW ORW7",
			Destination: "KBOS",
			Altitude:    21000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "WJA", Fleet: "jfk"},
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "NEION",
			Route:       "NEION J223 CORDS CFB V29 SYR",
			Destination: "KSYR",
			Altitude:    21000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "WJA", Fleet: "jfk"},
				DepartureAirline{ICAO: "JBU"},
			},
		},
		Departure{
			Exit:        "GREKI",
			Route:       "GREKI JUDDS CAM",
			Destination: "KBTV",
			Altitude:    21000,
			Airlines: []DepartureAirline{
				DepartureAirline{ICAO: "WJA", Fleet: "jfk"},
				DepartureAirline{ICAO: "JBU"},
			},
		},
	}

	isp.DepartureRunways = []*DepartureRunway{
		&DepartureRunway{
			Runway: "6",
			Rate:   30,
			ExitRoutes: map[string]ExitRoute{
				"MERIT": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_6 _ISP_6a _ISP_6b @ #270"), ClearedAltitude: 8000},
				"GREKI": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_6 _ISP_6a _ISP_6b @ #270"), ClearedAltitude: 8000},
				"BAYYS": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_6 _ISP_6a _ISP_6b @ #270"), ClearedAltitude: 8000},
				"BDR":   ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_6 _ISP_6a _ISP_6b @ #270"), ClearedAltitude: 8000},
			},
		},
		&DepartureRunway{
			Runway: "24",
			Rate:   30,
			ExitRoutes: map[string]ExitRoute{
				"MERIT": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_24 _ISP_24a _ISP_24b _ISP_24c @ #275"), ClearedAltitude: 8000},
				"GREKI": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_24 _ISP_24a _ISP_24b _ISP_24c @ #275"), ClearedAltitude: 8000},
				"BAYYS": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_24 _ISP_24a _ISP_24b _ISP_24c @ #275"), ClearedAltitude: 8000},
				"BDR":   ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_24 _ISP_24a _ISP_24b _ISP_24c @ #275"), ClearedAltitude: 8000},
			},
		},
		&DepartureRunway{
			Runway: "15R",
			Rate:   30,
			ExitRoutes: map[string]ExitRoute{
				"MERIT": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_15R _ISP_15Ra _ISP_15Rb _ISP_15Rc @ #275"), ClearedAltitude: 8000},
				"GREKI": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_15R _ISP_15Ra _ISP_15Rb _ISP_15Rc @ #275"), ClearedAltitude: 8000},
				"BAYYS": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_15R _ISP_15Ra _ISP_15Rb _ISP_15Rc @ #275"), ClearedAltitude: 8000},
				"BDR":   ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_15R _ISP_15Ra _ISP_15Rb _ISP_15Rc @ #275"), ClearedAltitude: 8000},
			},
		},
		&DepartureRunway{
			Runway: "33L",
			Rate:   30,
			ExitRoutes: map[string]ExitRoute{
				"MERIT": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_33L _ISP_33La _ISP_33Lb _ISP_33Lc @ #275"), ClearedAltitude: 8000},
				"GREKI": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_33L _ISP_33La _ISP_33Lb _ISP_33Lc @ #275"), ClearedAltitude: 8000},
				"BAYYS": ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_33L _ISP_33La _ISP_33Lb _ISP_33Lc @ #275"), ClearedAltitude: 8000},
				"BDR":   ExitRoute{InitialRoute: "LONGI7", Waypoints: mustParseWaypoints("_ISP_33L _ISP_33La _ISP_33Lb _ISP_33Lc @ #275"), ClearedAltitude: 8000},
			},
		},
	}

	return isp
}

func FRGAirport() *Airport {
	frg := &Airport{
		ICAO:      "KFRG",
		Elevation: 64,
		Location:  Point2LL{-73.418783, 40.724664},
	}
	frg.NamedLocations = map[string]Point2LL{
		"_FRG_1":   mustParseLatLong("N040.43.20.230, W073.24.51.229"),
		"_FRG_1a":  mustParseLatLong("N040.46.52.637, W073.24.58.809"),
		"_FRG_19":  mustParseLatLong("N040.44.10.396, W073.24.50.982"),
		"_FRG_19a": mustParseLatLong("N040.41.03.313, W073.26.45.267"),
		"_FRG_14":  mustParseLatLong("N040.44.02.898, W073.25.17.486"),
		"_FRG_14a": mustParseLatLong("N040.38.37.868, W073.22.41.398"),
		"_FRG_32":  mustParseLatLong("N040.43.20.436, W073.24.13.848"),
		"_FRG_32a": mustParseLatLong("N040.45.28.921, W073.27.08.421"),
	}

	frg.ExitCategories = map[string]string{
		"WAVEY": "Water",
		"SHIPP": "Water",
		"HAPIE": "Water",
		"BETTE": "Water",
		"MERIT": "East",
		"GREKI": "East",
		"BAYYS": "East",
		"BDR":   "East",
		"DIXIE": "Southwest",
		"WHITE": "Southwest",
		"RBV":   "Southwest",
		"ARD":   "Southwest",
		"COATE": "North",
		"NEION": "North",
		"HAAYS": "North",
		"GAYEL": "North",
		"DEEZZ": "North",
	}

	r1 := Approach{
		ShortName: "R1",
		FullName:  "RNAV Runway 1",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "WULUG", Altitude: 2000},
				Waypoint{Fix: "BLAND", Altitude: 1500},
				Waypoint{Fix: "DEUCE", Altitude: 1600},
				Waypoint{Fix: "XAREW", Altitude: 1500},
				Waypoint{Fix: "_FRG_1", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "ZACHS", Altitude: 2000},
				Waypoint{Fix: "BLAND", Altitude: 1500},
				Waypoint{Fix: "DEUCE", Altitude: 1600},
				Waypoint{Fix: "XAREW", Altitude: 1500},
				Waypoint{Fix: "_FRG_1", Altitude: 50},
			},
		},
	}
	frg.Approaches = append(frg.Approaches, r1)

	r14 := Approach{
		ShortName: "R14",
		FullName:  "RNAV Runway 14",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "HOBAM", Altitude: 2000},
				Waypoint{Fix: "LAAZE", Altitude: 2000},
				Waypoint{Fix: "ALABE", Altitude: 1400},
				Waypoint{Fix: "_FRG_14", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "CAMRN", Altitude: 4000},
				Waypoint{Fix: "HEREK", Altitude: 2000},
				Waypoint{Fix: "SEHDO", Altitude: 2000},
				Waypoint{Fix: "WUPMA", Altitude: 1400, Speed: 180},
				Waypoint{Fix: "ALABE", Altitude: 1400},
				Waypoint{Fix: "_FRG_14", Altitude: 50},
			},
		},
	}
	frg.Approaches = append(frg.Approaches, r14)

	i14 := Approach{
		ShortName: "I14",
		FullName:  "ILS Runway 14",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "N040.48.42.061,W073.32.03.431", Altitude: 2000},
				Waypoint{Fix: "FRIKK", Altitude: 1400},
				Waypoint{Fix: "_FRG_14", Altitude: 50},
			},
		},
	}
	frg.Approaches = append(frg.Approaches, i14)

	r19 := Approach{
		ShortName: "R19",
		FullName:  "RNAV Runway 19",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "BLINZ", Altitude: 2000},
				Waypoint{Fix: "DEBYE", Altitude: 2000},
				Waypoint{Fix: "MOIRE", Altitude: 1500},
				Waypoint{Fix: "WULOP", Altitude: 800},
				Waypoint{Fix: "_FRG_19", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "ZOSAB", Altitude: 2000},
				Waypoint{Fix: "DEBYE", Altitude: 2000},
				Waypoint{Fix: "MOIRE", Altitude: 1500},
				Waypoint{Fix: "WULOP", Altitude: 800},
				Waypoint{Fix: "_FRG_19", Altitude: 50},
			},
		},
	}
	frg.Approaches = append(frg.Approaches, r19)

	r32 := Approach{
		ShortName: "R32",
		FullName:  "RNAV Runway 32",
		Type:      RNAVApproach,
		Waypoints: []WaypointArray{
			[]Waypoint{
				Waypoint{Fix: "JUSIN", Altitude: 2000},
				Waypoint{Fix: "TRCCY", Altitude: 2000},
				Waypoint{Fix: "ALFED", Altitude: 1400},
				Waypoint{Fix: "_FRG_32", Altitude: 50},
			},
			[]Waypoint{
				Waypoint{Fix: "SHYNA", Altitude: 2000},
				Waypoint{Fix: "TRCCY", Altitude: 2000},
				Waypoint{Fix: "ALFED", Altitude: 1400},
				Waypoint{Fix: "_FRG_32", Altitude: 50},
			},
		},
	}
	frg.Approaches = append(frg.Approaches, r32)

	camrn4 := ArrivalGroup{
		Name: "CAMRN4",
		Rate: 20,
		Arrivals: []Arrival{
			Arrival{
				Name:              "CAMRN4",
				Waypoints:         mustParseWaypoints("N039.46.43.120,W074.03.15.529 KARRS @ CAMRN #041"),
				Route:             "/. CAMRN4",
				InitialController: "NY_B_CTR",
				InitialAltitude:   15000,
				ClearedAltitude:   11000,
				InitialSpeed:      300,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "EJA", Airport: "KDCA"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KDCA"},
					ArrivalAirline{ICAO: "EJA", Airport: "KJAX"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KJAX"},
					ArrivalAirline{ICAO: "EJA", Airport: "KAUS"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KAUS"},
					ArrivalAirline{ICAO: "EJA", Airport: "KACY"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KACY"},
					ArrivalAirline{ICAO: "EJA", Airport: "KPHL"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KPHL"},
				},
			},
		},
	}
	frg.ArrivalGroups = append(frg.ArrivalGroups, camrn4)

	lendy8 := ArrivalGroup{
		Name: "LENDY8",
		Rate: 20,
		Arrivals: []Arrival{
			Arrival{
				Name:              "LENDY8",
				Waypoints:         mustParseWaypoints("N040.56.09.863,W074.30.33.013 N040.55.09.974,W074.25.19.628 @ LENDY #135"),
				Route:             "/. LENDY8",
				InitialController: "NY_C_CTR",
				InitialAltitude:   20000,
				ClearedAltitude:   19000,
				InitialSpeed:      300,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "EJA", Airport: "KDTW"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KDTW"},
					ArrivalAirline{ICAO: "EJA", Airport: "KORD"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KORD"},
					ArrivalAirline{ICAO: "EJA", Airport: "KASE"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KASE"},
					ArrivalAirline{ICAO: "EJA", Airport: "KGRR"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KGRR"},
				},
			},
		},
	}
	frg.ArrivalGroups = append(frg.ArrivalGroups, lendy8)

	debug := ArrivalGroup{
		Name: "DEBUG",
		Rate: 20,
		Arrivals: []Arrival{
			Arrival{
				Name:              "DEBUG",
				Waypoints:         mustParseWaypoints("N040.47.35.140,W073.18.16.710 N040.47.01.563,W073.20.25.222 @ #270"),
				Route:             "/. DEBUG",
				InitialController: "NY_F_CTR",
				InitialAltitude:   2500,
				ClearedAltitude:   2000,
				InitialSpeed:      250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "EJA", Airport: "KMSP"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KMSP"},
				},
			},
		},
	}
	if *devmode {
		frg.ArrivalGroups = append(frg.ArrivalGroups, debug)
	}

	parch3 := ArrivalGroup{
		Name: "PARCH3",
		Rate: 20,
		Arrivals: []Arrival{
			Arrival{
				Name:              "PARCH3",
				Waypoints:         mustParseWaypoints("N040.58.19.145,W072.40.15.921 N040.56.23.940,W072.45.54.299 @ CCC ROBER #278"),
				Route:             "/. PARCH3",
				InitialController: "BOS_E_CTR",
				InitialAltitude:   13000,
				ClearedAltitude:   12000,
				InitialSpeed:      275,
				SpeedRestriction:  250,
				Airlines: []ArrivalAirline{
					ArrivalAirline{ICAO: "EJA", Airport: "KHYA"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KHYA"},
					ArrivalAirline{ICAO: "EJA", Airport: "KMVY"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KMVY"},
					ArrivalAirline{ICAO: "EJA", Airport: "KACK"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KACK"},
					ArrivalAirline{ICAO: "EJA", Airport: "KBGR"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KBGR"},
					ArrivalAirline{ICAO: "EJA", Airport: "KBTV"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KBTV"},
					ArrivalAirline{ICAO: "EJA", Airport: "KMHT"},
					ArrivalAirline{ICAO: "LXJ", Airport: "KMHT"},
				},
			},
		},
	}
	frg.ArrivalGroups = append(frg.ArrivalGroups, parch3)

	frg.Departures = []Departure{
		Departure{
			Exit:        "DIXIE",
			Route:       "JFK V16 DIXIE V276 RBV V249 SBJ",
			Destination: "KTEB",
			Altitude:    12000,
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}, DepartureAirline{ICAO: "LXJ"}},
		},
		Departure{
			Exit:        "WAVEY",
			Route:       "EMJAY J174 SWL CEBEE WETRO SKARP Y313 HOAGG BNFSH2",
			Destination: "KMIA",
			Altitude:    39000,
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}, DepartureAirline{ICAO: "LXJ"}},
		},
		Departure{
			Exit:        "MERIT",
			Route:       "ROBUC3",
			Destination: "KBOS",
			Altitude:    21000,
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}, DepartureAirline{ICAO: "LXJ"}},
		},
		Departure{
			Exit:        "BDR",
			Route:       "V487 CANAN",
			Destination: "KALB",
			Altitude:    39000,
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}, DepartureAirline{ICAO: "LXJ"}},
		},
		Departure{
			Exit:        "DEEZZ",
			Route:       "CANDR J60 PSB HAYNZ7",
			Destination: "KPIT",
			Altitude:    39000,
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}, DepartureAirline{ICAO: "LXJ"}},
		},
		Departure{
			Exit:        "DIXIE",
			Route:       "JFK DIXIE V16 VCN VCN9",
			Destination: "KILG",
			Altitude:    39000,
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}, DepartureAirline{ICAO: "LXJ"}},
		},
		Departure{
			Exit:        "COATE",
			Route:       "Q436 HERBA JHW WWSHR CBUSS2",
			Destination: "KCMH",
			Altitude:    39000,
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}, DepartureAirline{ICAO: "LXJ"}},
		},
		Departure{
			Exit:        "BDR",
			Route:       "BDR",
			Destination: "KHVN",
			Altitude:    14000,
			Airlines:    []DepartureAirline{DepartureAirline{ICAO: "EJA"}, DepartureAirline{ICAO: "LXJ"}},
		},
	}

	frg.DepartureRunways = []*DepartureRunway{
		&DepartureRunway{
			Runway:   "1",
			Rate:     30,
			Altitude: 81,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"SHIPP": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"HAPIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"BETTE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},

				"MERIT": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"GREKI": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"BAYYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"BDR":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},

				"DIXIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"WHITE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"RBV":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"ARD":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},

				"COATE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"NEION": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"HAAYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"GAYEL": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_FRG_1 _FRG_19 _FRG_1a @ #013"), ClearedAltitude: 3000},
			},
		},
		&DepartureRunway{
			Runway:   "19",
			Rate:     30,
			Altitude: 81,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"SHIPP": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"HAPIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"BETTE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},

				"MERIT": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"GREKI": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"BAYYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"BDR":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},

				"DIXIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"WHITE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"RBV":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"ARD":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},

				"COATE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"NEION": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"HAAYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"GAYEL": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_FRG_19 _FRG_1 _FRG_19a @ #220"), ClearedAltitude: 3000},
			},
		},
		&DepartureRunway{
			Runway:   "14",
			Rate:     30,
			Altitude: 81,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"SHIPP": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"HAPIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"BETTE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},

				"MERIT": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"GREKI": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"BAYYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"BDR":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},

				"DIXIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"WHITE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"RBV":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"ARD":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},

				"COATE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"NEION": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"HAAYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"GAYEL": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_FRG_14 _FRG_32 _FRG_14a @ #220"), ClearedAltitude: 3000},
			},
		},
		&DepartureRunway{
			Runway:   "32",
			Rate:     30,
			Altitude: 81,
			ExitRoutes: map[string]ExitRoute{
				"WAVEY": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"SHIPP": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"HAPIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"BETTE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},

				"MERIT": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"GREKI": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"BAYYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"BDR":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},

				"DIXIE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"WHITE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"RBV":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"ARD":   ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},

				"COATE": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"NEION": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"HAAYS": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"GAYEL": ExitRoute{InitialRoute: "REP1", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
				"DEEZZ": ExitRoute{InitialRoute: "DEEZZ5", Waypoints: mustParseWaypoints("_FRG_32 _FRG_14 _FRG_32a @ #010"), ClearedAltitude: 3000},
			},
		},
	}

	return frg
}
