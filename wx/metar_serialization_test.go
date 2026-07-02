// wx/metar_serialization_test.go
package wx

import (
	"bytes"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/vmihailenco/msgpack/v5"
)

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
