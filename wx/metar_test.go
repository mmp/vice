// wx/metar_test.go
package wx

import (
	"encoding/json"
	"math"
	"testing"
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
