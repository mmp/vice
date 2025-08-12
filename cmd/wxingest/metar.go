package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// This is as much of the METAR as we need at runtime.
type CommonMETAR struct {
	ICAO        string  `json:"icaoId"`
	ReportTime  string  `json:"reportTime"`
	Temperature float32 `json:"temp"`
	Altimeter   float32 `json:"altim"`
	WindDir     any     `json:"wdir"` // nil or string "VRB" for variable, else number for heading
	WindSpeed   int     `json:"wspd"`
	WindGust    *int    `json:"wgst"`
	Raw         string  `json:"rawOb"`

	parsedTime time.Time // This is convenient to cache in the struct so we can sort the slice of them by time.
}

// Structure-of-arrays representation of an array of CommonMETAR objects.
type METAR_SOA struct {
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

// METAR from a single file
type FileMETAR struct {
	ICAO  string
	METAR []CommonMETAR
}

// parseWindDirection converts the wind direction from CommonMETAR format
// to int16. Returns -1 for nil or "VRB" (variable), otherwise the numeric direction.
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

func MakeMETAR_SOA(recs []CommonMETAR) (METAR_SOA, error) {
	if len(recs) == 0 {
		return METAR_SOA{}, fmt.Errorf("No METAR records provided")
	}

	soa := METAR_SOA{}
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
			return METAR_SOA{}, err
		}
		soa.Temperature = append(soa.Temperature, temp)

		alt, err := toFixedS14_1(m.Altimeter)
		if err != nil {
			return METAR_SOA{}, err
		}
		soa.Altimeter = append(soa.Altimeter, alt)

		dir, err := parseWindDirection(m.WindDir)
		if err != nil {
			return METAR_SOA{}, err
		}
		soa.WindDir = append(soa.WindDir, dir)

		if m.WindSpeed < 0 || m.WindSpeed > 32767 {
			return METAR_SOA{}, fmt.Errorf("Unexpected wind speed %d", m.WindSpeed)
		}
		soa.WindSpeed = append(soa.WindSpeed, int16(m.WindSpeed))

		if m.WindGust != nil {
			if *m.WindGust < 0 || *m.WindGust > 32767 {
				return METAR_SOA{}, fmt.Errorf("Unexpected wind gust %d", *m.WindGust)
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

func DecodeMETAR_SOA(soa METAR_SOA) []CommonMETAR {
	var m []CommonMETAR

	reportTime := util.DeltaDecodeBytesSlice(soa.ReportTime)
	temp := util.DeltaDecode(soa.Temperature)
	alt := util.DeltaDecode(soa.Altimeter)
	dir := util.DeltaDecode(soa.WindDir)
	speed := util.DeltaDecode(soa.WindSpeed)
	gust := util.DeltaDecode(soa.WindGust)

	for i := range soa.ReportTime {
		cm := CommonMETAR{
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

		if gust[i] != 0 {
			g := int(gust[i])
			cm.WindGust = &g
		}

		m = append(m, cm)
	}

	return m
}

func CheckMETAR_SOA(soa METAR_SOA, orig []CommonMETAR) error {
	check := DecodeMETAR_SOA(soa)

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

		if mo.WindGust != nil && mc.WindGust == nil {
			return fmt.Errorf("WindGust mismatch: one nil, the other not")
		} else if mo.WindGust == nil && mc.WindGust != nil {
			return fmt.Errorf("WindGust mismatch: one nil, the other not")
		} else if mo.WindGust != nil && mc.WindGust != nil && *mo.WindGust != *mc.WindGust {
			return fmt.Errorf("WindGust mismatch: %d - %d", *mo.WindGust, *mc.WindGust)
		}

		if mo.Raw != mc.Raw {
			return fmt.Errorf("Raw mismatch: %s - %s", mo.Raw, mc.Raw)
		}
	}

	return nil
}

func ingestMETAR(st StorageBackend) {
	airportMETAR := loadScrapedMETAR()
	storeMETAR(st, airportMETAR)
}

func loadScrapedMETAR() map[string][]FileMETAR {
	airportMETAR := make(map[string][]FileMETAR)

	fmCh := make(chan FileMETAR)
	var pwg sync.WaitGroup
	pwg.Add(1)
	go func() {
		for fm := range fmCh {
			if len(fm.METAR) > 0 { // skip ones for empty files; they don't have ICAO set in any case
				airportMETAR[fm.ICAO] = append(airportMETAR[fm.ICAO], fm)
			}
		}
		pwg.Done()
	}()

	fileCh := make(chan string)
	var wwg sync.WaitGroup // worker wait group
	for range *nWorkers {
		wwg.Add(1)
		go func() {
			for path := range fileCh {
				if fm, err := loadMETARFile(path); err != nil {
					LogError("%s: %v", path, err)
				} else {
					fmCh <- fm
				}
			}
			wwg.Done()
		}()
	}

	EnqueueFiles("metar", fileCh)

	wwg.Wait()
	close(fmCh)
	pwg.Wait()

	return airportMETAR
}

func loadMETARFile(path string) (FileMETAR, error) {
	r, err := os.Open(path)
	if err != nil {
		return FileMETAR{}, err
	}
	defer r.Close()

	zr, err := zstd.NewReader(r)
	if err != nil {
		return FileMETAR{}, err
	}
	defer zr.Close()

	var metar []CommonMETAR
	if err := json.NewDecoder(zr).Decode(&metar); err != nil {
		return FileMETAR{}, err
	}
	if len(metar) == 0 {
		return FileMETAR{}, nil
	}

	for i := range metar {
		if metar[i].parsedTime, err = time.Parse(time.DateTime, metar[i].ReportTime); err != nil {
			return FileMETAR{}, err
		} else {
			metar[i].parsedTime = metar[i].parsedTime.UTC()
		}
	}

	return FileMETAR{ICAO: metar[0].ICAO, METAR: metar}, nil
}

func storeMETAR(st StorageBackend, airportMETAR map[string][]FileMETAR) {
	ch := make(chan string)

	var wg sync.WaitGroup
	var totalBytes int64
	for range *nWorkers {
		wg.Add(1)
		go func() {
			for ap := range ch {
				if n, err := storeAirportMETAR(st, ap, airportMETAR[ap]); err != nil {
					LogError("%s: %v", ap, err)
				} else {
					atomic.AddInt64(&totalBytes, n)
				}
			}
			wg.Done()
		}()
	}

	for ap := range airportMETAR {
		ch <- ap
	}
	close(ch)
	wg.Wait()

	LogInfo("Stored %s in %d objects for METAR", ByteCount(totalBytes), len(airportMETAR))
}

func storeAirportMETAR(st StorageBackend, ap string, fm []FileMETAR) (int64, error) {
	// Flatten all of the METAR
	var recs []CommonMETAR
	for _, m := range fm {
		recs = append(recs, m.METAR...)
	}

	// Sort by date
	slices.SortFunc(recs, func(a, b CommonMETAR) int { return a.parsedTime.Compare(b.parsedTime) })

	// Eliminate duplicates (may happen since the scraper grabs 24-hour chunks every 16 hours.
	recs = slices.CompactFunc(recs, func(a, b CommonMETAR) bool { return a.parsedTime.Equal(b.parsedTime) })

	soa, err := MakeMETAR_SOA(recs)
	if err != nil {
		return 0, err
	}

	if err := CheckMETAR_SOA(soa, recs); err != nil {
		return 0, err
	}

	path := fmt.Sprintf("metar/%s.msgpack.zstd", ap)
	return st.Store(path, soa)
}
