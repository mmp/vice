// wx/metar_test.go
package wx

import (
	"bytes"
	"math"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"

	"encoding/json"
	"github.com/vmihailenco/msgpack/v5"
)

func TestEffectiveVisualRangeSurface(t *testing.T) {
	// 3 SM surface vis, ground level → ~3 SM in NM.
	m := METAR{Raw: "KJFK 3SM BKN050"}
	got := m.EffectiveVisualRange(0, 0)
	want := float32(3 * 0.8690)
	if math.Abs(float64(got-want)) > 0.1 {
		t.Errorf("EffectiveVisualRange(3SM, 0, 0) = %.3f, want ~%.3f", got, want)
	}
}

func TestEffectiveVisualRangeAltitudeBonus(t *testing.T) {
	// At altitude the slant-path integral expands effective range beyond surface vis.
	m := METAR{Raw: "KJFK 3SM BKN080"}
	surface := m.EffectiveVisualRange(0, 0)
	aloft := m.EffectiveVisualRange(5000, 0)
	if aloft <= surface {
		t.Errorf("expected altitude bonus: surface=%.2f aloft=%.2f", surface, aloft)
	}
}

func TestEffectiveVisualRangeCap(t *testing.T) {
	// 10SM at high altitude should be capped at maxVisualRangeNM (25 NM).
	m := METAR{Raw: "KJFK 10SM CLR"}
	got := m.EffectiveVisualRange(20000, 0)
	if got > maxVisualRangeNM {
		t.Errorf("EffectiveVisualRange exceeded cap: %.2f > %.2f", got, float32(maxVisualRangeNM))
	}
	if got < maxVisualRangeNM-0.1 {
		t.Errorf("expected cap at %.2f NM for 10SM + high altitude, got %.2f", float32(maxVisualRangeNM), got)
	}
}

func TestEffectiveVisualRangeObscurationPenalty(t *testing.T) {
	clear := METAR{Raw: "KJFK 5SM BKN050"}
	haze := METAR{Raw: "KJFK 5SM HZ BKN050"}
	if haze.EffectiveVisualRange(0, 0) >= clear.EffectiveVisualRange(0, 0) {
		t.Errorf("expected obscuration penalty: clear=%.2f haze=%.2f",
			clear.EffectiveVisualRange(0, 0), haze.EffectiveVisualRange(0, 0))
	}
}

func TestEffectiveVisualRangeUnparseable(t *testing.T) {
	// Missing visibility field → conservative cap.
	m := METAR{Raw: "KJFK BKN050"}
	if got := m.EffectiveVisualRange(0, 0); got != maxVisualRangeNM {
		t.Errorf("unparseable visibility: got %.2f, want %.2f", got, float32(maxVisualRangeNM))
	}
}

func TestMETARJSONRoundTripWindDir(t *testing.T) {
	dir := 150
	orig := METAR{
		ICAO:       "KBDR",
		WindDir:    &dir,
		WindSpeed:  5,
		Raw:        "KBDR 160052Z 15005KT 10SM CLR 24/21 A3017",
		ReportTime: "2025-08-16 01:00:00",
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got METAR
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.WindDir == nil {
		t.Fatalf("WindDir nil after round-trip; JSON was: %s", string(data))
	}
	if *got.WindDir != 150 {
		t.Errorf("WindDir = %d, want 150", *got.WindDir)
	}
	if got.ICAO != "KBDR" {
		t.Errorf("ICAO = %q, want KBDR", got.ICAO)
	}
}

func TestMETARJSONRoundTripVRB(t *testing.T) {
	orig := METAR{
		ICAO:       "KCDW",
		WindDir:    nil,
		WindSpeed:  4,
		Raw:        "KCDW 241653Z VRB04KT 10SM",
		ReportTime: "2025-08-24 16:53:00",
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got METAR
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.WindDir != nil {
		t.Errorf("WindDir = &%d, want nil", *got.WindDir)
	}
}

func TestMETARObservation(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{"KJFK 061351Z 18017G25KT 10SM", "KJFK 061351Z 18017G25KT 10SM"},
		{"KJFK 061351Z 18017G25KT 10SM RMK AO2 SLP985 T01440111", "KJFK 061351Z 18017G25KT 10SM"},
		{"", ""},
	}
	for _, c := range cases {
		m := METAR{Raw: c.raw}
		if got := m.Observation(); got != c.want {
			t.Errorf("Observation(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestMETARUnmarshalAPIShape(t *testing.T) {
	data := `{"icaoId":"KJFK","reportTime":"2026-05-06T14:00:00.000Z","temp":14.4,"dewp":11.1,"wdir":180,"wspd":17,"wgst":25,"altim":1010.6,"rawOb":"METAR KJFK 061351Z 18017G25KT 10SM"}`

	var m METAR
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m.ICAO != "KJFK" {
		t.Errorf("ICAO = %q, want KJFK", m.ICAO)
	}
	if m.WindDir == nil || *m.WindDir != 180 {
		got := "nil"
		if m.WindDir != nil {
			got = string(rune('0' + *m.WindDir))
		}
		t.Errorf("WindDir = %s, want &180", got)
	}
	if m.WindSpeed != 17 {
		t.Errorf("WindSpeed = %d, want 17", m.WindSpeed)
	}
}

func TestMETARUnmarshalVariableWind(t *testing.T) {
	cases := map[string]string{
		"VRB string": `{"icaoId":"KSFO","wdir":"VRB","wspd":4,"rawOb":"","reportTime":"2025-08-16 01:00:00"}`,
		"null":       `{"icaoId":"KSFO","wdir":null,"wspd":4,"rawOb":"","reportTime":"2025-08-16 01:00:00"}`,
		"missing":    `{"icaoId":"KSFO","wspd":4,"rawOb":"","reportTime":"2025-08-16 01:00:00"}`,
	}
	for name, data := range cases {
		var m METAR
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Errorf("[%s] Unmarshal: %v", name, err)
			continue
		}
		if m.WindDir != nil {
			t.Errorf("[%s] WindDir = &%d, want nil", name, *m.WindDir)
		}
	}
}

func TestMETARSOADecodeWindow(t *testing.T) {
	start := time.Date(2024, time.January, 15, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name    string
		reports []time.Duration
		want    []time.Duration
	}{
		{name: "empty"},
		{
			name:    "all reports precede start",
			reports: []time.Duration{-2 * time.Hour, -time.Hour},
			want:    []time.Duration{-time.Hour},
		},
		{
			name:    "next report at window end",
			reports: []time.Duration{-2 * time.Hour, -time.Hour, time.Hour},
			want:    []time.Duration{-time.Hour},
		},
		{
			name:    "next report after window end",
			reports: []time.Duration{-2 * time.Hour, -time.Hour, 2 * time.Hour},
			want:    []time.Duration{-time.Hour},
		},
		{
			name:    "preceding and in-window reports",
			reports: []time.Duration{-2 * time.Hour, -time.Hour, 30 * time.Minute, time.Hour},
			want:    []time.Duration{-time.Hour, 30 * time.Minute},
		},
		{
			name:    "report at start replaces preceding",
			reports: []time.Duration{-time.Hour, 0, 30 * time.Minute, time.Hour},
			want:    []time.Duration{0, 30 * time.Minute},
		},
		{
			name:    "only reports outside window",
			reports: []time.Duration{time.Hour, 2 * time.Hour},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var reports []METAR
			for _, offset := range tt.reports {
				reportTime := start.Add(offset)
				reports = append(reports, METAR{
					ICAO:       "KJFK",
					Time:       reportTime,
					ReportTime: reportTime.Format(time.RFC3339),
				})
			}
			var soa METARSOA
			if len(reports) > 0 {
				var err error
				soa, err = MakeMETARSOA(reports)
				if err != nil {
					t.Fatal(err)
				}
			}
			got := soa.DecodeWindow("KJFK", start, time.Hour)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d reports, want %d", len(got), len(tt.want))
			}
			for i, offset := range tt.want {
				if want := start.Add(offset); !got[i].Time.Equal(want) {
					t.Errorf("report %d time = %v, want %v", i, got[i].Time, want)
				}
			}
		})
	}
}

func TestCompressedMETARMsgpackSerialization(t *testing.T) {
	// Create test METAR data
	testTime, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
	testMETARs := []METAR{
		{
			ICAO:        "KJFK",
			Time:        testTime,
			Temperature: av.MakeTemperatureFromCelsius(15.5),
			Dewpoint:    av.MakeTemperatureFromCelsius(10.2),
			Altimeter:   1013.2,
			WindSpeed:   10,
			Raw:         "KJFK 151200Z 10010KT RMK AO2 SLP192 T02170172 10233 20189 58004",
			ReportTime:  "2024-01-15T12:00:00Z",
		},
	}

	// Create CompressedMETAR and add data
	cm := NewCompressedMETAR()
	if err := cm.SetAirportMETAR("KJFK", testMETARs); err != nil {
		t.Fatalf("SetAirportMETAR failed: %v", err)
	}
	laxMETARs := []METAR{testMETARs[0]}
	laxMETARs[0].ICAO = "KLAX"
	if err := cm.SetAirportMETAR("KLAX", laxMETARs); err != nil {
		t.Fatalf("SetAirportMETAR failed: %v", err)
	}

	// Marshal to msgpack
	data, err := msgpack.Marshal(cm)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Marshaled data is empty - custom MarshalMsgpack may not be working")
	}

	// Unmarshal back
	var cm2 CompressedMETAR
	if err := msgpack.Unmarshal(data, &cm2); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify data was preserved
	if cm2.Len() != 2 {
		t.Errorf("Expected 2 airports, got %d", cm2.Len())
	}

	// Verify we can retrieve the data
	metars, err := cm2.GetAirportMETAR("KJFK")
	if err != nil {
		t.Fatalf("GetAirportMETAR failed: %v", err)
	}

	if len(metars) != 1 {
		t.Errorf("Expected 1 METAR, got %d", len(metars))
	}

	if metars[0].ICAO != "KJFK" {
		t.Errorf("Expected ICAO 'KJFK', got '%s'", metars[0].ICAO)
	}
	if metars[0].Temperature.Celsius() != 15.5 {
		t.Errorf("Expected Temperature 15.5, got %f", metars[0].Temperature.Celsius())
	}
	if metars[0].Raw != "KJFK 151200Z 10010KT RMK AO2 SLP192 T02170172 10233 20189 58004" {
		t.Errorf("Expected Raw with RMK preserved, got '%s'", metars[0].Raw)
	}
}

func TestCompressedMETARSaveLoad(t *testing.T) {
	// Create test METAR data
	testTime, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
	testMETARs := []METAR{
		{
			ICAO:        "KORD",
			Time:        testTime,
			Temperature: av.MakeTemperatureFromCelsius(5.0),
			Dewpoint:    av.MakeTemperatureFromCelsius(2.0),
			Altimeter:   1015.0,
			WindSpeed:   15,
			Raw:         "KORD 151200Z 15015KT",
			ReportTime:  "2024-01-15T12:00:00Z",
		},
	}

	// Create and populate CompressedMETAR
	cm := NewCompressedMETAR()
	if err := cm.SetAirportMETAR("KORD", testMETARs); err != nil {
		t.Fatalf("SetAirportMETAR failed: %v", err)
	}
	bosMETARs := []METAR{testMETARs[0]}
	bosMETARs[0].ICAO = "KBOS"
	if err := cm.SetAirportMETAR("KBOS", bosMETARs); err != nil {
		t.Fatalf("SetAirportMETAR failed: %v", err)
	}

	// Save to buffer
	var buf bytes.Buffer
	if err := cm.Save(&buf); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if buf.Len() == 0 {
		t.Fatal("Saved data is empty")
	}

	// Load from buffer
	cm2, err := LoadCompressedMETAR(&buf)
	if err != nil {
		t.Fatalf("LoadCompressedMETAR failed: %v", err)
	}

	// Verify data was preserved
	if cm2.Len() != 2 {
		t.Errorf("Expected 2 airports after load, got %d", cm2.Len())
	}

	// Verify we can retrieve the data
	metars, err := cm2.GetAirportMETAR("KORD")
	if err != nil {
		t.Fatalf("GetAirportMETAR failed: %v", err)
	}

	if len(metars) != 1 {
		t.Errorf("Expected 1 METAR, got %d", len(metars))
	}

	if metars[0].ICAO != "KORD" {
		t.Errorf("Expected ICAO 'KORD', got '%s'", metars[0].ICAO)
	}
	if metars[0].Temperature.Celsius() != 5.0 {
		t.Errorf("Expected Temperature 5.0, got %f", metars[0].Temperature.Celsius())
	}
	if metars[0].Raw != "KORD 151200Z 15015KT" {
		t.Errorf("Expected Raw 'KORD 151200Z 15015KT', got '%s'", metars[0].Raw)
	}
}
