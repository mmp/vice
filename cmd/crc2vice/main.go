// cmd/crc2vice/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
//
// Reads a CRC ARTCC JSON and emits:
//   - one STARS video map library per STARS-equipped child facility
//     (<ARTCC>-<facility>.mappack), and
//   - a single ERAM video map library (<ARTCC>.mappack),
//     if the ARTCC has any ERAM geomaps.
//
// Run from the CRC working directory (where ARTCCs/ and VideoMaps/ live).

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"golang.org/x/sync/errgroup"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	var inputARTCC, outDir string
	flag.StringVar(&inputARTCC, "artcc", "", "ARTCC to import (e.g. ZNY)")
	flag.StringVar(&outDir, "out", ".", "Directory to write the output .mappack file(s) into")
	flag.Parse()
	if inputARTCC == "" {
		log.Fatal("-artcc is required")
	}

	cwd, _ := os.Getwd()
	artccPath := filepath.Join(cwd, "ARTCCs", inputARTCC+".json")
	log.Printf("reading %s", artccPath)
	data, err := os.ReadFile(artccPath)
	if err != nil {
		log.Fatalf("open ARTCC: %v", err)
	}
	var artcc ARTCC
	if err := json.Unmarshal(data, &artcc); err != nil {
		log.Fatalf("decode ARTCC: %v", err)
	}
	log.Printf("loaded ARTCC %s (%s)", artcc.Facility.Name, artcc.Facility.ID)

	var eg errgroup.Group
	eg.Go(func() error {
		return runSTARS(cwd, outDir, inputARTCC, &artcc, &eg)
	})
	eg.Go(func() error {
		return runERAM(cwd, outDir, inputARTCC, &artcc)
	})
	if err := eg.Wait(); err != nil {
		log.Fatalf("%v", err)
	}
}

// loadedSource is one parsed .geojson with its three sentinel defaults
// extracted and the non-sentinel features retained for downstream
// processing.
type loadedSource struct {
	path           string
	lineDefaults   GeoJSONProperties
	symbolDefaults GeoJSONProperties
	textDefaults   GeoJSONProperties
	features       []GeoJSONFeature
}

// loadGeoJSON reads one .geojson and partitions the three sentinel
// defaults features out of the regular feature list.
func loadGeoJSON(path string) (loadedSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return loadedSource{}, err
	}
	var gj GeoJSON
	if err := json.Unmarshal(data, &gj); err != nil {
		return loadedSource{}, fmt.Errorf("decode: %w", err)
	}
	src := loadedSource{path: path}
	for i := range gj.Features {
		if p := gj.Features[i].Properties; p == nil {
			src.features = append(src.features, gj.Features[i])
		} else {
			switch {
			case p.IsLineDefaults:
				src.lineDefaults = *p
			case p.IsSymbolDefaults:
				src.symbolDefaults = *p
			case p.IsTextDefaults:
				src.textDefaults = *p
			default:
				src.features = append(src.features, gj.Features[i])
			}
		}
	}
	return src, nil
}

// featureSink is the trio of slices that both STARS VideoMap and
// ERAMMap expose; appendFeatures pushes into them via the pointers.
type featureSink struct {
	Lines   *[]av.MapLine
	Symbols *[]av.MapSymbol
	Labels  *[]av.MapLabel
}

// appendFeatures emits src.features into sink. If accept is non-nil,
// only features whose effective properties pass accept(...) are
// emitted (used by ERAM for the filter-index gate). If onAccept is
// non-nil, it's invoked for each accepted feature (used by ERAM for
// BCG promotion). STARS callers pass nil for both.
func appendFeatures(src *loadedSource, sink featureSink, accept func(*GeoJSONProperties) bool, onAccept func(*GeoJSONProperties)) {
	clampPositive := func(v, dflt int) int {
		if v <= 0 {
			return dflt
		} else if v > 255 {
			return 255
		} else {
			return v
		}
	}

	for i := range src.features {
		f := &src.features[i]
		switch f.Geometry.Type {
		case "LineString", "MultiLineString":
			polylines := decodePolylines(f.Geometry.Type, f.Geometry.Coordinates)
			if len(polylines) == 0 {
				continue
			}
			eff := mergeDefaults(f.Properties, &src.lineDefaults)
			if accept != nil && !accept(&eff) {
				continue
			}
			if onAccept != nil {
				onAccept(&eff)
			}
			style := parseLineStyle(eff.Style)
			thickness := uint8(clampPositive(eff.Thickness, 1))
			bcg := uint8(clampPositive(eff.BCG, 0))
			for _, pts := range polylines {
				if len(pts) < 2 {
					continue
				}
				*sink.Lines = append(*sink.Lines, av.MapLine{
					Points:    pts,
					Style:     style,
					Thickness: thickness,
					BCGIndex:  bcg,
				})
			}

		case "Point":
			p, ok := decodePoint(f.Geometry.Coordinates)
			if !ok {
				continue
			}
			if f.Properties != nil && len(f.Properties.Text) > 0 {
				eff := mergeDefaults(f.Properties, &src.textDefaults)
				if accept != nil && !accept(&eff) {
					continue
				}
				if onAccept != nil {
					onAccept(&eff)
				}
				for _, line := range f.Properties.Text {
					*sink.Labels = append(*sink.Labels, av.MapLabel{
						P:         p,
						Text:      line,
						Size:      uint8(clampPositive(eff.Size, 1)),
						XOffset:   int8(eff.XOffset),
						YOffset:   int8(eff.YOffset),
						Underline: eff.Underline,
						Opaque:    eff.Opaque,
						BCGIndex:  uint8(clampPositive(eff.BCG, 0)),
					})
				}
			} else {
				eff := mergeDefaults(f.Properties, &src.symbolDefaults)
				if accept != nil && !accept(&eff) {
					continue
				}
				if onAccept != nil {
					onAccept(&eff)
				}
				style, known := parseSymbolStyle(eff.Style)
				if !known {
					if eff.Style != "" {
						warnUnknownStyle(src.path, eff.Style)
					}
					style = av.SymbolStyleVOR
				}
				*sink.Symbols = append(*sink.Symbols, av.MapSymbol{
					P:        p,
					Style:    style,
					Size:     uint8(clampPositive(eff.Size, 1)),
					BCGIndex: uint8(clampPositive(eff.BCG, 0)),
				})
			}
		}
	}
}

// warnedStyles dedupes "unknown symbol style" log lines across concurrent
// goroutines (the STARS path spawns one per child facility).
var (
	warnedStyles   = map[string]struct{}{}
	warnedStylesMu sync.Mutex
)

func warnUnknownStyle(path, style string) {
	key := path + "\x00" + style
	warnedStylesMu.Lock()
	defer warnedStylesMu.Unlock()
	if _, seen := warnedStyles[key]; seen {
		return
	}
	warnedStyles[key] = struct{}{}
	log.Printf("  WARN: %s: unknown symbol style %q (using VOR)", path, style)
}

func mergeDefaults(feat *GeoJSONProperties, def *GeoJSONProperties) GeoJSONProperties {
	var eff GeoJSONProperties
	if feat != nil {
		eff = *feat
	}
	if eff.BCG == 0 && def.BCG != 0 {
		eff.BCG = def.BCG
	}
	if len(eff.Filters) == 0 && len(def.Filters) > 0 {
		eff.Filters = append([]int(nil), def.Filters...)
	}
	if eff.Style == "" && def.Style != "" {
		eff.Style = def.Style
	}
	if eff.Thickness == 0 && def.Thickness != 0 {
		eff.Thickness = def.Thickness
	}
	if eff.Size == 0 && def.Size != 0 {
		eff.Size = def.Size
	}
	if eff.XOffset == 0 && def.XOffset != 0 {
		eff.XOffset = def.XOffset
	}
	if eff.YOffset == 0 && def.YOffset != 0 {
		eff.YOffset = def.YOffset
	}
	if !eff.Underline && def.Underline {
		eff.Underline = def.Underline
	}
	if !eff.Opaque && def.Opaque {
		eff.Opaque = def.Opaque
	}
	return eff
}

func decodePoint(raw json.RawMessage) (math.Point2LL, bool) {
	var coords [2]float32
	if err := json.Unmarshal(raw, &coords); err != nil {
		return math.Point2LL{}, false
	}
	return math.Point2LL{coords[0], coords[1]}, true
}

func decodePolylines(geomType string, raw json.RawMessage) [][]math.Point2LL {
	switch geomType {
	case "LineString":
		var pts [][2]float32
		if err := json.Unmarshal(raw, &pts); err != nil {
			return nil
		}
		line := util.MapSlice(pts, func(p [2]float32) math.Point2LL { return p })
		return [][]math.Point2LL{line}
	case "MultiLineString":
		var lines [][][2]float32
		if err := json.Unmarshal(raw, &lines); err != nil {
			return nil
		}
		out := make([][]math.Point2LL, 0, len(lines))
		for _, pts := range lines {
			line := util.MapSlice(pts, func(p [2]float32) math.Point2LL { return p })
			out = append(out, line)
		}
		return out
	}
	return nil
}

// parseLineStyle accepts CRC's case-inconsistent line-style strings
// ("Solid", "solid", "ShortDashed", "LongDashShortDash", …). Unknown
// strings fall back to LineStyleSolid.
func parseLineStyle(s string) av.LineStyle {
	switch strings.ToLower(strings.ReplaceAll(s, "_", "")) {
	case "shortdashed", "shortdash", "dashed":
		return av.LineStyleShortDashed
	case "longdashed", "longdash":
		return av.LineStyleLongDashed
	case "longdashshortdash", "longshortdash":
		return av.LineStyleLongDashShortDash
	default:
		return av.LineStyleSolid
	}
}

// parseSymbolStyle accepts CRC's case-inconsistent symbol-style strings
// ("Vor", "vor", "OtherWaypoints", "Ndb", …). Returns ok=false for
// unrecognized styles; callers may log and fall back to a default.
func parseSymbolStyle(s string) (av.SymbolStyle, bool) {
	switch strings.ToLower(strings.ReplaceAll(s, "_", "")) {
	case "vor":
		return av.SymbolStyleVOR, true
	case "ndb":
		return av.SymbolStyleNDB, true
	case "tacan":
		return av.SymbolStyleTACAN, true
	case "vortacan":
		return av.SymbolStyleVOR_TACAN, true
	case "dme":
		return av.SymbolStyleDME, true
	case "rnav":
		return av.SymbolStyleRNAV, true
	case "rnavonlywaypoint", "rnavonlywp":
		return av.SymbolStyleRNAVOnlyWaypoint, true
	case "airport":
		return av.SymbolStyleAirport, true
	case "satelliteairport", "satelliteaiport": // CRC has a misspelled variant in the wild
		return av.SymbolStyleSatelliteAirport, true
	case "emergencyairport":
		return av.SymbolStyleEmergencyAirport, true
	case "heliport":
		return av.SymbolStyleHeliport, true
	case "otherwaypoints", "otherwaypoint", "waypoint":
		return av.SymbolStyleOtherWaypoints, true
	case "airwayintersections", "airwayintersection":
		return av.SymbolStyleAirwayIntersections, true
	case "iaf":
		return av.SymbolStyleIAF, true
	case "obstruction1", "obstruction":
		return av.SymbolStyleObstruction1, true
	case "obstruction2":
		return av.SymbolStyleObstruction2, true
	case "nuclear":
		return av.SymbolStyleNuclear, true
	case "radar":
		return av.SymbolStyleRadar, true
	}
	return 0, false
}
