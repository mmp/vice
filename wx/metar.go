package wx

import (
	"fmt"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// This is as much of the METAR as we need at runtime.
type BasicMETAR struct {
	ICAO        string  `json:"icaoId"`
	ReportTime  string  `json:"reportTime"`
	Temperature float32 `json:"temp"`
	Altimeter   float32 `json:"altim"`
	WindDir     any     `json:"wdir"` // nil or string "VRB" for variable, else number for heading
	WindSpeed   int     `json:"wspd"`
	WindGust    *int    `json:"wgst"`
	Raw         string  `json:"rawOb"`
}

// Structure-of-arrays representation of an array of BasicMETAR objects
// for better compressability.
type BasicMETARSOA struct {
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

// parseWindDirection converts the wind direction from BasicMETAR format to
// int16. Returns -1 for nil or "VRB" (variable), otherwise the numeric
// direction.
func parseWindDirection(windDir any) (int16, error) {
	switch v := windDir.(type) {
	case nil:
		return -1, nil
	case string:
		if v == "VRB" {
			return -1, nil
		}
		return 0, fmt.Errorf("unexpected wind direction string %q", v)
	case float64:
		if v < 0 || v > 360 {
			return 0, fmt.Errorf("wind direction out of range: %f", v)
		}
		return int16(v), nil
	default:
		return 0, fmt.Errorf("unexpected wind direction type %T: %v", windDir, windDir)
	}
}

func MakeBasicMETARSOA(recs []BasicMETAR) (BasicMETARSOA, error) {
	if len(recs) == 0 {
		return BasicMETARSOA{}, fmt.Errorf("No METAR records provided")
	}

	soa := BasicMETARSOA{}
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
			return BasicMETARSOA{}, err
		}
		soa.Temperature = append(soa.Temperature, temp)

		alt, err := toFixedS14_1(m.Altimeter)
		if err != nil {
			return BasicMETARSOA{}, err
		}
		soa.Altimeter = append(soa.Altimeter, alt)

		dir, err := parseWindDirection(m.WindDir)
		if err != nil {
			return BasicMETARSOA{}, err
		}
		soa.WindDir = append(soa.WindDir, dir)

		if m.WindSpeed < 0 || m.WindSpeed > 32767 {
			return BasicMETARSOA{}, fmt.Errorf("Unexpected wind speed %d", m.WindSpeed)
		}
		soa.WindSpeed = append(soa.WindSpeed, int16(m.WindSpeed))

		if m.WindGust != nil {
			if *m.WindGust < 0 || *m.WindGust > 32767 {
				return BasicMETARSOA{}, fmt.Errorf("Unexpected wind gust %d", *m.WindGust)
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

func DecodeBasicMETARSOA(soa BasicMETARSOA) []BasicMETAR {
	var m []BasicMETAR

	reportTime := util.DeltaDecodeBytesSlice(soa.ReportTime)
	temp := util.DeltaDecode(soa.Temperature)
	alt := util.DeltaDecode(soa.Altimeter)
	dir := util.DeltaDecode(soa.WindDir)
	speed := util.DeltaDecode(soa.WindSpeed)
	gust := util.DeltaDecode(soa.WindGust)

	for i := range soa.ReportTime {
		cm := BasicMETAR{
			ReportTime:  string(reportTime[i]),
			Temperature: float32(temp[i]) / 10,
			Altimeter:   float32(alt[i]) / 10,
			WindSpeed:   int(speed[i]),
			Raw:         soa.Raw[i],
		}

		if dir[i] == -1 {
			cm.WindDir = "VRB"
		} else {
			cm.WindDir = float64(dir[i])
		}

		// Always set gust on decode, even if it was nil in the original.
		g := int(gust[i])
		cm.WindGust = &g

		m = append(m, cm)
	}

	return m
}

func CheckBasicMETARSOA(soa BasicMETARSOA, orig []BasicMETAR) error {
	check := DecodeBasicMETARSOA(soa)

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
		if _, ok := mo.WindDir.(string); ok || mo.WindDir == nil {
			if sc, ok := mc.WindDir.(string); !ok || sc != "VRB" {
				return fmt.Errorf("WindDir mismatch: %v - %v", mo.WindDir, mc.WindDir)
			}
		} else if d, ok := mo.WindDir.(float64); ok {
			if dc, ok := mc.WindDir.(float64); !ok || d != dc {
				return fmt.Errorf("WindDir mismatch: %v - %v", mo.WindDir, mc.WindDir)
			}
		} else {
			return fmt.Errorf("WindDir mismatch: %v - %v", mo.WindDir, mc.WindDir)
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
