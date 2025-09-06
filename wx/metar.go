package wx

import (
	"encoding/json"
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
	WindDir     *int    `json:"-"` // nil for variable winds, otherwise heading 0-360
	WindSpeed   int     `json:"wspd"`
	WindGust    *int    `json:"wgst"`
	Raw         string  `json:"rawOb"`

	// WindDirRaw is used for JSON unmarshaling only
	WindDirRaw any `json:"wdir"` // nil or string "VRB" for variable, else number for heading
}

// UnmarshalJSON handles converting WindDirRaw to WindDir
func (b *BasicMETAR) UnmarshalJSON(data []byte) error {
	type Alias BasicMETAR
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(b),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Convert WindDirRaw to WindDir
	switch v := b.WindDirRaw.(type) {
	case nil:
		b.WindDir = nil
	case string:
		if v == "VRB" {
			b.WindDir = nil
		} else {
			return fmt.Errorf("unexpected wind direction string %q", v)
		}
	case float64:
		if v < 0 || v > 360 {
			return fmt.Errorf("wind direction out of range: %f", v)
		}
		dir := int(v)
		b.WindDir = &dir
	default:
		return fmt.Errorf("unexpected wind direction type %T: %v", b.WindDirRaw, b.WindDirRaw)
	}

	return nil
}
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

		// Convert wind direction: -1 for variable, otherwise the numeric direction
		var dir int16
		if m.WindDir == nil {
			dir = -1
		} else {
			if *m.WindDir < 0 || *m.WindDir > 360 {
				return BasicMETARSOA{}, fmt.Errorf("wind direction out of range: %d", *m.WindDir)
			}
			dir = int16(*m.WindDir)
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
