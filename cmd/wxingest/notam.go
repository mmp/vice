// cmd/wxingest/notam.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
)

// faaTimezones maps short timezone names used in FAA TFR XML to IANA
// timezone paths. Add entries here as new short names are encountered.
var faaTimezones = map[string]string{
	"Guam":           "Pacific/Guam",
	"Samoa":          "Pacific/Pago_Pago",
	"Hawaii":         "Pacific/Honolulu",
	"Virgin Islands": "America/Virgin",
}

var tfrTypes = map[string]string{
	"91.137": "HAZARDS",
	"91.138": "HI HAZARDS",
	"91.139": "EMERGENCY",
	"91.141": "VIP",
	"91.143": "SPACE OPS",
	"91.145": "EVENT",
	"99.7":   "SECURITY",
}

// lookupTFRType maps a codeType to a human-readable TFR type using prefix
// matching. This handles sub-clause codes like "91.137(a)(1)" by matching
// the "91.137" prefix.
func lookupTFRType(codeType string) string {
	if t, ok := tfrTypes[codeType]; ok {
		return t
	}
	for prefix, t := range tfrTypes {
		if strings.HasPrefix(codeType, prefix) {
			return t
		}
	}
	return ""
}

// xnotamUpdate was generated 2024-09-23 07:39:34 by
// https://xml-to-go.github.io/, using https://github.com/miku/zek. Then
// manually chopped down to the parts we care about...
type xnotamUpdate struct {
	Group struct {
		Add struct {
			Not struct {
				NotUid struct {
					TxtLocalName string `xml:"txtLocalName"`
					DateIssued   string `xml:"dateIssued"`
				} `xml:"NotUid"`
				DateEffective          string `xml:"dateEffective"`
				DateExpire             string `xml:"dateExpire"`
				CodeTimeZone           string `xml:"codeTimeZone"`
				CodeExpirationTimeZone string `xml:"codeExpirationTimeZone"`
				CodeFacility           string `xml:"codeFacility"`
				TxtDescrPurpose        string `xml:"txtDescrPurpose"`
				AffLocGroup            struct {
					TxtNameCity    string `xml:"txtNameCity"`
					TxtNameUSState string `xml:"txtNameUSState"`
				} `xml:"AffLocGroup"`
				TfrNot struct {
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
						AseTFRArea struct {
							CodeDistVerUpper string `xml:"codeDistVerUpper"`
							ValDistVerUpper  string `xml:"valDistVerUpper"`
							UomDistVerUpper  string `xml:"uomDistVerUpper"`
							CodeDistVerLower string `xml:"codeDistVerLower"`
							ValDistVerLower  string `xml:"valDistVerLower"`
							UomDistVerLower  string `xml:"uomDistVerLower"`
						} `xml:"aseTFRArea"`
					} `xml:"TFRAreaGroup"`
				} `xml:"TfrNot"`
			} `xml:"Not"`
		} `xml:"Add"`
	} `xml:"Group"`
}

// decodeTFRXML takes an XML-formatted TFR and converts it to our struct.
func decodeTFRXML(url string, r io.Reader, lg *log.Logger) (av.TFR, error) {
	var tfr av.TFR
	var xmlTFR xnotamUpdate
	dec := xml.NewDecoder(r)
	if err := dec.Decode(&xmlTFR); err != nil {
		return tfr, err
	}

	notam := xmlTFR.Group.Add.Not
	tfr.ARTCC = notam.CodeFacility
	tfr.Type = lookupTFRType(notam.TfrNot.CodeType)
	tfr.LocalName = notam.NotUid.TxtLocalName
	tfr.Regulation = notam.TfrNot.CodeType
	tfr.City = notam.AffLocGroup.TxtNameCity
	tfr.State = notam.AffLocGroup.TxtNameUSState
	tfr.Purpose = notam.TxtDescrPurpose

	// FAA TFR timezone names: usually standard abbreviations (EST, UTC)
	// but occasionally short names that need mapping to IANA paths.
	loadTZ := func(zone string) (*time.Location, error) {
		if zone == "" {
			return time.UTC, nil
		}
		// Try as IANA location (handles "America/New_York", etc.)
		if loc, err := time.LoadLocation(zone); err == nil {
			return loc, nil
		}
		// FAA sometimes uses short names not in the IANA database.
		if full, ok := faaTimezones[zone]; ok {
			if loc, err := time.LoadLocation(full); err == nil {
				return loc, nil
			}
		}
		// Try as fixed abbreviation by parsing a reference time.
		if t, err := time.Parse("MST", zone); err == nil {
			_, offset := t.Zone()
			return time.FixedZone(zone, offset), nil
		}
		return nil, fmt.Errorf("unknown timezone %q", zone)
	}

	parseTime := func(date, zone string) (time.Time, error) {
		if date == "" {
			return time.Time{}, fmt.Errorf("empty date")
		}
		loc, err := loadTZ(zone)
		if err != nil {
			return time.Time{}, err
		}
		return time.ParseInLocation("2006-01-02T15:04:05", date, loc)
	}

	// Some TFRs lack dateEffective; fall back to dateIssued.
	effectiveDate := notam.DateEffective
	if effectiveDate == "" {
		effectiveDate = notam.NotUid.DateIssued
	}
	var err error
	tfr.Effective, err = parseTime(effectiveDate, notam.CodeTimeZone)
	if err != nil {
		return tfr, fmt.Errorf("%s: effective time: %w", url, err)
	}

	// Some TFRs lack dateExpire; use expiration timezone falling back
	// to the effective timezone.
	expireZone := notam.CodeExpirationTimeZone
	if expireZone == "" {
		expireZone = notam.CodeTimeZone
	}
	tfr.Expire, err = parseTime(notam.DateExpire, expireZone)
	if err != nil {
		return tfr, fmt.Errorf("%s: expire time: %w", url, err)
	}

	// The extent is given as one or more line loops. Also gather
	// altitude limits across all area groups for the description.
	type altLimit struct {
		code string // "AGL" or "MSL"
		val  float64
	}
	var minLower, maxUpper *altLimit
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

				if v < -180 || v > 360 {
					return 0, fmt.Errorf("invalid lat/long coordinate %q -> %f", s, v)
				}
				return float32(v), nil
			}

			var p math.Point2LL
			p[0], err = pf(pt.GeoLong)
			if err != nil {
				lg.Warnf("%s: %v", url, err)
				continue
			}
			p[1], err = pf(pt.GeoLat)
			if err != nil {
				lg.Warnf("%s: %v", url, err)
				continue
			}
			pts = append(pts, p)
		}
		if len(pts) > 0 {
			tfr.Points = append(tfr.Points, pts)
		}

		a := group.AseTFRArea
		parseAlt := func(val, uom string) *altLimit {
			if val == "" {
				return nil
			}
			v, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return nil
			}
			return &altLimit{code: uom, val: v}
		}
		if lo := parseAlt(a.ValDistVerLower, a.CodeDistVerLower); lo != nil {
			if minLower == nil || lo.val < minLower.val {
				minLower = lo
			}
		}
		if hi := parseAlt(a.ValDistVerUpper, a.CodeDistVerUpper); hi != nil {
			if maxUpper == nil || hi.val > maxUpper.val {
				maxUpper = hi
			}
		}
	}

	fmtAlt := func(a *altLimit) string {
		if a == nil {
			return ""
		}
		// Map FAA altitude codes to standard abbreviations.
		code := a.code
		switch code {
		case "HEI":
			code = "AGL"
		case "ALT":
			code = "MSL"
		}
		if a.val == 0 && code == "AGL" {
			return "SFC"
		}
		return fmt.Sprintf("%.0f ft %s", a.val, code)
	}
	if lo, hi := fmtAlt(minLower), fmtAlt(maxUpper); lo != "" && hi != "" {
		tfr.AltDescr = lo + " - " + hi
	} else if lo != "" {
		tfr.AltDescr = lo
	} else if hi != "" {
		tfr.AltDescr = hi
	}

	return tfr, nil
}
