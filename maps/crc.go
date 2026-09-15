// maps/crc.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package maps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
	"golang.org/x/sync/errgroup"
)

// The layout of a CRC working directory: ARTCC definitions in one
// subdirectory and the video map geometry they refer to in another.
const (
	artccSubdir    = "ARTCCs"
	videoMapSubdir = "VideoMaps"
)

// Report receives a conversion's progress and warning lines. ConvertCRC runs
// the STARS and ERAM halves of a conversion concurrently, so a Report may be
// called from several goroutines at once and must be safe for concurrent use.
type Report func(line string)

// DefaultCRCDirectory returns where CRC keeps its working files, or "" if that
// can't be determined. CRC has no Linux build, so there the result is just a
// plausible starting point for the user to correct.
func DefaultCRCDirectory() string {
	// On Windows CRC stores its files under %LOCALAPPDATA%, which is what
	// UserCacheDir returns there; UserConfigDir would give the roaming
	// %APPDATA% instead. Elsewhere UserConfigDir gives macOS's
	// ~/Library/Application Support.
	dir, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		dir, err = os.UserCacheDir()
	}
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "CRC")
}

// ListCRCARTCCs returns the sorted ids of the ARTCCs available in a CRC
// directory: the names of the JSON files it has ARTCC definitions in.
func ListCRCARTCCs(crcDir string) ([]string, error) {
	dir := filepath.Join(crcDir, artccSubdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	defs := util.FilterSlice(entries, func(e os.DirEntry) bool {
		return !e.IsDir() && filepath.Ext(e.Name()) == ".json"
	})
	ids := util.MapSlice(defs, func(e os.DirEntry) string {
		return strings.TrimSuffix(e.Name(), ".json")
	})
	slices.Sort(ids)
	return ids, nil
}

// ConvertCRC reads the given ARTCC from a CRC directory and writes the video
// map libraries for it into outDir. report may be nil.
func ConvertCRC(crcDir, artccID, outDir string, report Report) error {
	c := &converter{
		crcDir:       crcDir,
		artccID:      artccID,
		outDir:       outDir,
		report:       report,
		warnedStyles: make(map[string]struct{}),
	}

	path := filepath.Join(crcDir, artccSubdir, artccID+".json")
	c.reportf("reading %s", path)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var a artcc
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("decode ARTCC: %w", err)
	}
	c.reportf("loaded ARTCC %s (%s)", a.Facility.Name, a.Facility.ID)

	var eg errgroup.Group
	eg.Go(func() error {
		return c.convertSTARS(&a, &eg)
	})
	eg.Go(func() error {
		return c.convertERAM(&a)
	})
	return eg.Wait()
}

// converter holds the settings and the reporting for the conversion of a
// single ARTCC.
type converter struct {
	crcDir  string
	artccID string
	outDir  string
	report  Report

	// warnedStyles dedupes "unknown symbol style" warnings across the
	// goroutines the conversion runs in.
	warnedStyles   map[string]struct{}
	warnedStylesMu sync.Mutex
}

func (c *converter) reportf(format string, args ...any) {
	if c.report != nil {
		c.report(fmt.Sprintf(format, args...))
	}
}

// videoMapPath is where the geometry of one of the ARTCC's video maps lives.
func (c *converter) videoMapPath(id string) string {
	return filepath.Join(c.crcDir, videoMapSubdir, c.artccID, id+".geojson")
}

// loadedSource is one parsed .geojson with its three sentinel defaults
// extracted and the non-sentinel features retained for downstream
// processing.
type loadedSource struct {
	path           string
	lineDefaults   geoJSONProperties
	symbolDefaults geoJSONProperties
	textDefaults   geoJSONProperties
	features       []geoJSONFeature
}

// loadGeoJSON reads one .geojson and partitions the three sentinel
// defaults features out of the regular feature list.
func loadGeoJSON(path string) (loadedSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return loadedSource{}, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM borkage
	var gj geoJSON
	if err := json.Unmarshal(data, &gj); err != nil {
		return loadedSource{}, fmt.Errorf("decode %s: %w", path, err)
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

// appendFeatures emits src.features into sink.
func (c *converter) appendFeatures(src *loadedSource, sink featureSink) {
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
				// Join multi-line labels into a single MapLabel.
				*sink.Labels = append(*sink.Labels, av.MapLabel{
					P:         p,
					Text:      strings.Join(f.Properties.Text, "\n"),
					Size:      uint8(clampPositive(eff.Size, 1)),
					XOffset:   int8(eff.XOffset),
					YOffset:   int8(eff.YOffset),
					Underline: eff.Underline,
					Opaque:    eff.Opaque,
					BCGIndex:  uint8(clampPositive(eff.BCG, 0)),
				})
			} else {
				eff := mergeDefaults(f.Properties, &src.symbolDefaults)
				style, known := parseSymbolStyle(eff.Style)
				if !known {
					if eff.Style != "" {
						c.warnUnknownStyle(src.path, eff.Style)
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

func (c *converter) warnUnknownStyle(path, style string) {
	key := path + "\x00" + style
	c.warnedStylesMu.Lock()
	defer c.warnedStylesMu.Unlock()
	if _, ok := c.warnedStyles[key]; ok {
		return
	}
	c.warnedStyles[key] = struct{}{}
	c.reportf("  WARN: %s: unknown symbol style %q (using VOR)", path, style)
}

func mergeDefaults(feat *geoJSONProperties, def *geoJSONProperties) geoJSONProperties {
	var eff geoJSONProperties
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
