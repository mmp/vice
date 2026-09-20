// videomaps/crc/crc_json.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package crc

import (
	"encoding/json"
	"strconv"
	"strings"
)

// artcc is the trimmed schema needed for both STARS and ERAM video map
// import. Only the fields we actually consume are listed; everything
// else in the CRC ARTCC JSON is ignored.
type artcc struct {
	Facility struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		ChildFacilities []struct {
			ID                 string `json:"id"`
			Type               string `json:"type"`
			Name               string `json:"name"`
			StarsConfiguration struct {
				VideoMapIds []string   `json:"videoMapIds"`
				MapGroups   []mapGroup `json:"mapGroups"`
			} `json:"starsConfiguration"`
		} `json:"childFacilities"`
		ERAMConfiguration struct {
			GeoMaps []artccGeoMap `json:"geoMaps"`
		} `json:"eramConfiguration"`
	} `json:"facility"`
	VideoMaps []artccVideoMap `json:"videoMaps"`
}

// artccGeoMap is one entry under facility.eramConfiguration.geoMaps. The
// FilterMenu drives ERAMMap creation (one per non-empty entry); BCGMenu
// is aligned to filter-menu index and supplies BCG names; VideoMapIds
// lists the source .geojson files contributing features.
type artccGeoMap struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	LabelLine1 string `json:"labelLine1"`
	LabelLine2 string `json:"labelLine2"`
	FilterMenu []struct {
		ID         string `json:"id"`
		LabelLine1 string `json:"labelLine1"`
		LabelLine2 string `json:"labelLine2"`
	} `json:"filterMenu"`
	BCGMenu     []string `json:"bcgMenu"`
	VideoMapIds []string `json:"videoMapIds"`
}

// mapGroup is one DCB button layout in a TRACON's starsConfiguration.
// Tcps lists the position codes (e.g. "1A", "1D") that share this
// layout; MapIds is the per-slot button assignment, with nil entries
// for empty slots.
type mapGroup struct {
	MapIds []*int   `json:"mapIds"`
	Tcps   []string `json:"tcps"`
}

// artccVideoMap is one entry in the top-level videoMaps catalog —
// metadata only; the geometry lives in VideoMaps/<artcc>/<id>.geojson.
type artccVideoMap struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	ShortName               string `json:"shortName"`
	StarsBrightnessCategory string `json:"starsBrightnessCategory"`
	StarsID                 int    `json:"starsId"` // 0 == null in source == no DCB id
}

// geoJSON is the top-level structure of a CRC video-map .geojson.
type geoJSON struct {
	Type     string           `json:"type"`
	Features []geoJSONFeature `json:"features"`
}

type geoJSONFeature struct {
	Type     string `json:"type"`
	Geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
	Properties *geoJSONProperties `json:"properties"`
}

// geoJSONProperties mirrors the per-feature properties used by CRC video
// maps. CRC's encoding is "defaults-sentinel + bare features": each
// .geojson carries up to three sentinel Points marked IsLineDefaults /
// IsSymbolDefaults / IsTextDefaults that supply the file's defaults;
// every other feature inherits fields it doesn't set.
type geoJSONProperties struct {
	IsLineDefaults   bool `json:"isLineDefaults"`
	IsTextDefaults   bool `json:"isTextDefaults"`
	IsSymbolDefaults bool `json:"isSymbolDefaults"`

	BCG     int    `json:"bcg"` // background control group
	Filters []int  `json:"filters"`
	Style   string `json:"style"`

	Thickness int `json:"thickness"`

	Size      int  `json:"size"`
	Underline bool `json:"underline"`
	Opaque    bool `json:"opaque"`
	XOffset   int  `json:"xOffset"`
	YOffset   int  `json:"yOffset"`

	Text []string `json:"text"`
}

// UnmarshalJSON tolerates the few places CRC's JSON gives numeric fields
// as quoted strings (e.g. "bcg": "13").
func (p *geoJSONProperties) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	decodeBool := func(key string, dst *bool) {
		if b, ok := raw[key]; ok {
			_ = json.Unmarshal(b, dst)
		}
	}
	decodeString := func(key string, dst *string) {
		if b, ok := raw[key]; ok {
			_ = json.Unmarshal(b, dst)
		}
	}
	decodeInt := func(key string, dst *int) {
		b, ok := raw[key]
		if !ok {
			return
		}
		var n int
		if err := json.Unmarshal(b, &n); err == nil {
			*dst = n
			return
		}
		var s string
		if err := json.Unmarshal(b, &s); err == nil {
			if i, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				*dst = i
			}
		}
	}
	decodeIntSlice := func(key string, dst *[]int) {
		b, ok := raw[key]
		if !ok {
			return
		}
		// Accept several encodings: [1,2,3], ["1","2","3"], 1, or "1".
		var ints []int
		if err := json.Unmarshal(b, &ints); err == nil {
			*dst = ints
			return
		}
		var strs []string
		if err := json.Unmarshal(b, &strs); err == nil {
			out := make([]int, 0, len(strs))
			for _, s := range strs {
				if i, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
					out = append(out, i)
				}
			}
			*dst = out
			return
		}
		var single int
		if err := json.Unmarshal(b, &single); err == nil {
			*dst = []int{single}
			return
		}
		var singleStr string
		if err := json.Unmarshal(b, &singleStr); err == nil {
			if i, err := strconv.Atoi(strings.TrimSpace(singleStr)); err == nil {
				*dst = []int{i}
			}
		}
	}
	decodeStringSlice := func(key string, dst *[]string) {
		if b, ok := raw[key]; ok {
			_ = json.Unmarshal(b, dst)
		}
	}

	decodeBool("isLineDefaults", &p.IsLineDefaults)
	decodeBool("isTextDefaults", &p.IsTextDefaults)
	decodeBool("isSymbolDefaults", &p.IsSymbolDefaults)
	decodeInt("bcg", &p.BCG)
	decodeIntSlice("filters", &p.Filters)
	decodeString("style", &p.Style)
	decodeInt("thickness", &p.Thickness)
	decodeInt("size", &p.Size)
	decodeBool("underline", &p.Underline)
	decodeBool("opaque", &p.Opaque)
	decodeInt("xOffset", &p.XOffset)
	decodeInt("yOffset", &p.YOffset)
	decodeStringSlice("text", &p.Text)
	return nil
}
