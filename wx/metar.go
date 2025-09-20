package wx

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

// This is as much of the METAR as we need at runtime.
type METAR struct {
	ICAO        string `json:"icaoId"`
	Time        time.Time
	Temperature float32 `json:"temp"`
	Altimeter   float32 `json:"altim"`
	WindDir     *int    `json:"-"` // nil for variable winds, otherwise heading 0-360
	WindSpeed   int     `json:"wspd"`
	WindGust    *int    `json:"wgst"`
	Raw         string  `json:"rawOb"`

	// WindDirRaw and ReportTime are used for JSON unmarshaling only
	WindDirRaw any    `json:"wdir"` // nil or string "VRB" for variable, else number for heading
	ReportTime string `json:"reportTime"`
}

func (m METAR) Altimeter_inHg() float32 {
	// Conversion formula (hectoPascal to Inch of Mercury): 29.92 * (hpa / 1013.2)
	return 0.02953 * m.Altimeter
}

// UnmarshalJSON handles converting WindDirRaw to WindDir
func (m *METAR) UnmarshalJSON(data []byte) error {
	type Alias METAR
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(m),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Convert WindDirRaw to WindDir
	switch v := m.WindDirRaw.(type) {
	case nil:
		m.WindDir = nil
	case string:
		if v == "VRB" {
			m.WindDir = nil
		} else {
			return fmt.Errorf("unexpected wind direction string %q", v)
		}
	case float64:
		if v < 0 || v > 360 {
			return fmt.Errorf("wind direction out of range: %f", v)
		}
		dir := int(v)
		m.WindDir = &dir
	default:
		return fmt.Errorf("unexpected wind direction type %T: %v", m.WindDirRaw, m.WindDirRaw)
	}

	// Parse time
	var err error
	m.Time, err = parseMETARTime(m.ReportTime)

	return err
}

func parseMETARTime(s string) (time.Time, error) {
	t, err := time.Parse(time.DateTime, s)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05.999Z", s)
		if err != nil {
			return time.Time{}, err
		}
	}
	return t.UTC(), nil
}

// IsVMC returns true if Visual Meteorological Conditions apply
// VMC requires >= 3 miles visibility and >= 1000' ceiling AGL
func (m METAR) IsVMC() bool {
	// >= 3 miles visibility
	vis, err := m.Visibility()
	if err != nil || vis < 3 {
		return false
	}

	// >= 1000' ceiling AGL
	ceil, err := m.Ceiling()
	return err == nil && ceil >= 1000
}

// Visibility extracts visibility in statute miles from the raw METAR
func (m METAR) Visibility() (float32, error) {
	for _, f := range strings.Fields(m.Raw) {
		if strings.HasSuffix(f, "SM") {
			f = strings.TrimSuffix(f, "SM")
			f = strings.TrimPrefix(f, "M") // there if 1/4 or less

			// Handle fractional visibility like 1/4SM
			if snum, sdenom, ok := strings.Cut(f, "/"); ok {
				if num, err := strconv.Atoi(snum); err != nil {
					return -1, err
				} else if denom, err := strconv.Atoi(sdenom); err != nil {
					return -1, err
				} else {
					return float32(num) / float32(denom), nil
				}
			} else if vis, err := strconv.Atoi(f); err != nil {
				return -1, err
			} else {
				return float32(vis), nil
			}
		}
	}
	return -1, fmt.Errorf("%s: no visibility found", m.Raw)
}

// Ceiling returns ceiling in feet AGL (above ground level)
func (m METAR) Ceiling() (int, error) {
	for _, f := range strings.Fields(m.Raw) {
		// BKN (broken) or OVC (overcast) constitute a ceiling
		if strings.HasPrefix(f, "BKN") || strings.HasPrefix(f, "OVC") {
			if len(f) < 6 {
				return 0, fmt.Errorf("%s: too short", f)
			}

			// Cloud height is in hundreds of feet
			if alt, err := strconv.Atoi(f[3:6]); err == nil {
				alt *= 100
				return alt, nil
			} else {
				return -1, err
			}
		}
	}
	// No ceiling means unlimited (typically reported as 12000')
	return 12000, nil
}

func METARForTime(metar []METAR, t time.Time) METAR {
	if idx, _ := slices.BinarySearchFunc(metar, t, func(m METAR, t time.Time) int {
		return m.Time.Compare(t)
	}); idx < len(metar) {
		return metar[idx]
	}
	return METAR{}
}

// Given an average headings (e.g. runway directions) and a slice of valid time intervals,
// randomly sample a METAR entry with wind that is compatible with the headings.
func SampleMETAR(metar []METAR, intervals []util.TimeInterval, avgHdg float32) *METAR {
	var m *METAR
	r := rand.Make()
	n := float32(0)

	for _, iv := range intervals {
		idx, _ := slices.BinarySearchFunc(metar, iv[0], func(m METAR, t time.Time) int {
			return m.Time.Compare(t)
		})

		for idx < len(metar) && metar[idx].Time.Before(iv[1]) {
			if metar[idx].WindDir != nil && math.HeadingDifference(avgHdg, float32(*metar[idx].WindDir)) < 30 {
				n++
				if r.Float32() < 1/n { // reservoir sampling
					m = &metar[idx]
				}
			}
			idx++
		}
	}
	return m
}

///////////////////////////////////////////////////////////////////////////

// Structure-of-arrays representation of an array of METAR objects
// for better compressability.
type METARSOA struct {
	// These are all delta coded
	ReportTime  [][]byte
	Temperature []int16 // fixed point, one decimal digit
	Altimeter   []int16 // fixed point, one decimal digit
	WindDir     []int16
	WindSpeed   []int16
	WindGust    []int16

	// This is not; it's not worth delta encoding the raw METAR reports
	// since there's generally not much character alignment between
	// successive reports.
	Raw []string
}

func MakeMETARSOA(recs []METAR) (METARSOA, error) {
	if len(recs) == 0 {
		return METARSOA{}, fmt.Errorf("No METAR records provided")
	}

	soa := METARSOA{}
	for _, m := range recs {
		soa.ReportTime = append(soa.ReportTime, []byte(m.ReportTime))

		toFixedS14_1 := func(v float32) (int16, error) {
			vf := math.Round(v * 10)
			if vf < -32768 || vf > 32767 {
				return 0, fmt.Errorf("%f out of range for fixed s14.1 representation", v)
			}
			return int16(vf), nil
		}

		temp, err := toFixedS14_1(m.Temperature)
		if err != nil {
			return METARSOA{}, err
		}
		soa.Temperature = append(soa.Temperature, temp)

		alt, err := toFixedS14_1(m.Altimeter)
		if err != nil {
			return METARSOA{}, err
		}
		soa.Altimeter = append(soa.Altimeter, alt)

		// Convert wind direction: -1 for variable, otherwise the numeric direction
		var dir int16
		if m.WindDir == nil {
			dir = -1
		} else {
			if *m.WindDir < 0 || *m.WindDir > 360 {
				return METARSOA{}, fmt.Errorf("wind direction out of range: %d", *m.WindDir)
			}
			dir = int16(*m.WindDir)
		}
		soa.WindDir = append(soa.WindDir, dir)

		if m.WindSpeed < 0 || m.WindSpeed > 32767 {
			return METARSOA{}, fmt.Errorf("Unexpected wind speed %d", m.WindSpeed)
		}
		soa.WindSpeed = append(soa.WindSpeed, int16(m.WindSpeed))

		if m.WindGust != nil {
			if *m.WindGust < 0 || *m.WindGust > 32767 {
				return METARSOA{}, fmt.Errorf("Unexpected wind gust %d", *m.WindGust)
			}
			soa.WindGust = append(soa.WindGust, int16(*m.WindGust))
		} else {
			soa.WindGust = append(soa.WindGust, 0)
		}

		soa.Raw = append(soa.Raw, m.Raw)
	}

	soa.ReportTime = util.DeltaEncodeBytesSlice(soa.ReportTime)
	soa.Temperature = util.DeltaEncode(soa.Temperature)
	soa.Altimeter = util.DeltaEncode(soa.Altimeter)
	soa.WindDir = util.DeltaEncode(soa.WindDir)
	soa.WindSpeed = util.DeltaEncode(soa.WindSpeed)
	soa.WindGust = util.DeltaEncode(soa.WindGust)

	return soa, nil
}

func DecodeMETARSOA(soa METARSOA) []METAR {
	var m []METAR

	reportTime := util.DeltaDecodeBytesSlice(soa.ReportTime)
	temp := util.DeltaDecode(soa.Temperature)
	alt := util.DeltaDecode(soa.Altimeter)
	dir := util.DeltaDecode(soa.WindDir)
	speed := util.DeltaDecode(soa.WindSpeed)
	gust := util.DeltaDecode(soa.WindGust)

	for i := range soa.ReportTime {
		cm := METAR{
			ReportTime:  string(reportTime[i]),
			Temperature: float32(temp[i]) / 10,
			Altimeter:   float32(alt[i]) / 10,
			WindSpeed:   int(speed[i]),
			Raw:         soa.Raw[i],
		}

		var err error
		cm.Time, err = parseMETARTime(cm.ReportTime)
		if err != nil {
			panic(err)
		}

		if dir[i] == -1 {
			cm.WindDir = nil
		} else {
			d := int(dir[i])
			cm.WindDir = &d
		}

		// Always set gust on decode, even if it was nil in the original.
		g := int(gust[i])
		cm.WindGust = &g

		m = append(m, cm)
	}

	return m
}

func CheckMETARSOA(soa METARSOA, orig []METAR) error {
	check := DecodeMETARSOA(soa)

	if len(orig) != len(check) {
		return fmt.Errorf("Record count mismatch: %d - %d", len(orig), len(check))
	}

	for i := range len(orig) {
		mo, mc := orig[i], check[i]

		if mo.ReportTime != mc.ReportTime {
			return fmt.Errorf("ReportTime mismatch: %s - %s", mo.ReportTime, mc.ReportTime)
		}
		if math.Abs(mo.Temperature-mc.Temperature) > 0.001 {
			return fmt.Errorf("Temperature mismatch: %.8g - %.8g", mo.Temperature, mc.Temperature)
		}
		if math.Abs(mo.Altimeter-mc.Altimeter) > 0.001 {
			return fmt.Errorf("Altimeter mismatch: %.8g - %.8g", mo.Altimeter, mc.Altimeter)
		}
		// Check WindDir - both should be nil for variable winds
		if mo.WindDir == nil {
			if mc.WindDir != nil {
				return fmt.Errorf("WindDir mismatch: orig nil - %d", *mc.WindDir)
			}
		} else if mc.WindDir == nil {
			return fmt.Errorf("WindDir mismatch: orig %d - nil", *mo.WindDir)
		} else if *mo.WindDir != *mc.WindDir {
			return fmt.Errorf("WindDir mismatch: %d - %d", *mo.WindDir, *mc.WindDir)
		}

		if mo.WindGust == nil {
			if *mc.WindGust != 0 { // check always has gust non-nil, so check 0
				return fmt.Errorf("WindGust mismatch: orig nil - %d", *mc.WindGust)
			}
		} else if *mo.WindGust != *mc.WindGust {
			return fmt.Errorf("WindGust mismatch: %d - %d", *mo.WindGust, *mc.WindGust)
		}

		if mo.Raw != mc.Raw {
			return fmt.Errorf("Raw mismatch: %s - %s", mo.Raw, mc.Raw)
		}
	}

	return nil
}
