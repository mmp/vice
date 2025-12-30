package wx

import (
	"math"
	"testing"
)

func TestLevelIndexInverse(t *testing.T) {
	for i := range NumSampleLevels {
		id := IdFromLevelIndex(i)
		idx := LevelIndexFromId([]byte(id))
		if idx != i {
			t.Errorf("Inverse check failed for index %d: got ID %q, converted back to %d", i, id, idx)
		}
	}

	testCases := []struct {
		index int
		id    string
	}{
		{0, "1013.2 mb"},
		{1, "1000 mb"},
		{39, "50 mb"},
	}

	for _, tc := range testCases {
		gotId := IdFromLevelIndex(tc.index)
		if gotId != tc.id {
			t.Errorf("IdFromLevelIndex(%d) = %q, want %q", tc.index, gotId, tc.id)
		}

		gotIndex := LevelIndexFromId([]byte(tc.id))
		if gotIndex != tc.index {
			t.Errorf("LevelIndexFromId(%q) = %d, want %d", tc.id, gotIndex, tc.index)
		}
	}
}

func TestSampleQuantization(t *testing.T) {
	testCases := []struct {
		name        string
		windVec     [2]float32
		temperature float32
		dewpoint    float32
		pressure    float32
		maxWindErr  float32 // max error in nm/s per component
		maxTempErr  float32 // max error in Celsius
		maxPresErr  float32 // max error in mb
	}{
		{
			name:        "typical values",
			windVec:     [2]float32{0.02, -0.01},
			temperature: 15.0,
			dewpoint:    5.0,
			pressure:    1013.2,
			maxWindErr:  0.001,
			maxTempErr:  0.5,
			maxPresErr:  4.0,
		},
		{
			name:        "zero values",
			windVec:     [2]float32{0, 0},
			temperature: 0.0,
			dewpoint:    0.0,
			pressure:    1000.0,
			maxWindErr:  0.001,
			maxTempErr:  0.5,
			maxPresErr:  4.0,
		},
		{
			name:        "extreme cold",
			windVec:     [2]float32{0.05, 0.05},
			temperature: -60.0,
			dewpoint:    -70.0,
			pressure:    200.0,
			maxWindErr:  0.001,
			maxTempErr:  0.5,
			maxPresErr:  4.0,
		},
		{
			name:        "high altitude",
			windVec:     [2]float32{-0.03, 0.04},
			temperature: -50.0,
			dewpoint:    -60.0,
			pressure:    100.0,
			maxWindErr:  0.001,
			maxTempErr:  0.5,
			maxPresErr:  4.0,
		},
		{
			name:        "max pressure",
			windVec:     [2]float32{0.01, 0.01},
			temperature: 20.0,
			dewpoint:    10.0,
			pressure:    1090.0,
			maxWindErr:  0.001,
			maxTempErr:  0.5,
			maxPresErr:  4.5,
		},
		{
			name:        "min pressure",
			windVec:     [2]float32{0.01, 0.01},
			temperature: -40.0,
			dewpoint:    -50.0,
			pressure:    50.0,
			maxWindErr:  0.001,
			maxTempErr:  0.5,
			maxPresErr:  4.0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Quantize
			s := MakeSample(tc.windVec, tc.temperature, tc.dewpoint, tc.pressure)

			// Dequantize and check
			gotWindVec := s.WindVec()
			gotTemp := s.Temperature()
			gotDewpoint := s.Dewpoint()
			gotPressure := s.Pressure()

			// Check wind vector components
			for i := 0; i < 2; i++ {
				err := math.Abs(float64(gotWindVec[i] - tc.windVec[i]))
				if err > float64(tc.maxWindErr) {
					t.Errorf("WindVec[%d]: got %f, want %f (error %f > %f)",
						i, gotWindVec[i], tc.windVec[i], err, tc.maxWindErr)
				}
			}

			// Check temperature
			tempErr := math.Abs(float64(gotTemp - tc.temperature))
			if tempErr > float64(tc.maxTempErr) {
				t.Errorf("Temperature: got %f, want %f (error %f > %f)",
					gotTemp, tc.temperature, tempErr, tc.maxTempErr)
			}

			// Check dewpoint
			dewErr := math.Abs(float64(gotDewpoint - tc.dewpoint))
			if dewErr > float64(tc.maxTempErr) {
				t.Errorf("Dewpoint: got %f, want %f (error %f > %f)",
					gotDewpoint, tc.dewpoint, dewErr, tc.maxTempErr)
			}

			// Check pressure
			presErr := math.Abs(float64(gotPressure - tc.pressure))
			if presErr > float64(tc.maxPresErr) {
				t.Errorf("Pressure: got %f, want %f (error %f > %f)",
					gotPressure, tc.pressure, presErr, tc.maxPresErr)
			}
		})
	}
}

func TestSampleAccessors(t *testing.T) {
	windVec := [2]float32{0.03, -0.02}
	temp := 18.5
	dewpoint := 8.3
	pressure := 1013.2

	s := MakeSample(windVec, float32(temp), float32(dewpoint), float32(pressure))

	// Test that accessors work
	_ = s.Temperature()
	_ = s.Dewpoint()
	_ = s.Pressure()
	_ = s.WindDirection()
	_ = s.WindSpeed()
	_ = s.RelativeHumidity()
}

func TestLerpSample(t *testing.T) {
	s0 := MakeSample([2]float32{0.01, 0.02}, 10.0, 0.0, 1000.0)
	s1 := MakeSample([2]float32{0.03, 0.04}, 20.0, 10.0, 900.0)

	// Interpolate at midpoint
	sMid := LerpSample(0.5, s0, s1)

	// Check values are approximately at midpoint
	wv := sMid.WindVec()
	if math.Abs(float64(wv[0]-0.02)) > 0.002 {
		t.Errorf("Interpolated WindVec[0]: got %f, want ~0.02", wv[0])
	}
	if math.Abs(float64(wv[1]-0.03)) > 0.002 {
		t.Errorf("Interpolated WindVec[1]: got %f, want ~0.03", wv[1])
	}

	temp := sMid.Temperature()
	if math.Abs(float64(temp-15.0)) > 1.0 {
		t.Errorf("Interpolated Temperature: got %f, want ~15.0", temp)
	}

	dewpoint := sMid.Dewpoint()
	if math.Abs(float64(dewpoint-5.0)) > 1.0 {
		t.Errorf("Interpolated Dewpoint: got %f, want ~5.0", dewpoint)
	}

	pressure := sMid.Pressure()
	if math.Abs(float64(pressure-950.0)) > 5.0 {
		t.Errorf("Interpolated Pressure: got %f, want ~950.0", pressure)
	}
}

func TestMakeStandardSampleForAltitude(t *testing.T) {
	testCases := []struct {
		altitude float32
		name     string
	}{
		{-1412, "below sea level (Dead Sea)"},
		{0, "sea level"},
		{10000, "10000 feet"},
		{24000, "24000 feet"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := MakeStandardSampleForAltitude(tc.altitude)

			// Check that we can access all fields
			wv := s.WindVec()
			if wv[0] != 0 || wv[1] != 0 {
				t.Errorf("Standard atmosphere should have calm winds, got %v", wv)
			}

			temp := s.Temperature()
			pressure := s.Pressure()
			dewpoint := s.Dewpoint()

			// Temperature should decrease with altitude (increase below sea level)
			if tc.altitude > 0 && temp >= 15.0 {
				t.Errorf("Temperature at %f ft should be < 15°C, got %f", tc.altitude, temp)
			}
			if tc.altitude < 0 && temp <= 15.0 {
				t.Errorf("Temperature at %f ft should be > 15°C, got %f", tc.altitude, temp)
			}

			// Pressure should decrease with altitude (increase below sea level)
			if tc.altitude > 0 && pressure >= 1013.0 {
				t.Errorf("Pressure at %f ft should be < 1013 mb, got %f", tc.altitude, pressure)
			}
			if tc.altitude < 0 && pressure <= 1013.0 {
				t.Errorf("Pressure at %f ft should be > 1013 mb, got %f", tc.altitude, pressure)
			}

			// Dewpoint should be well below temperature
			if dewpoint >= temp {
				t.Errorf("Dewpoint (%f) should be below temperature (%f)", dewpoint, temp)
			}
		})
	}
}

func TestBelowSeaLevelPressure(t *testing.T) {
	// Test that below-sea-level altitudes produce pressures > 1013 mb
	deadSea := MakeStandardSampleForAltitude(-1412) // Dead Sea altitude
	pressure := deadSea.Pressure()

	if pressure <= 1013.0 {
		t.Errorf("Dead Sea pressure should be > 1013 mb, got %f mb", pressure)
	}
	if pressure > 1090.0 {
		t.Errorf("Dead Sea pressure exceeded max quantization range: %f mb", pressure)
	}

	// Verify temperature is also higher below sea level
	temp := deadSea.Temperature()
	if temp <= 15.0 {
		t.Errorf("Dead Sea temperature should be > 15°C, got %f°C", temp)
	}

	t.Logf("Dead Sea (-1412 ft): pressure=%.1f mb, temp=%.1f°C", pressure, temp)
}

func TestWindVecEdgeCases(t *testing.T) {
	// Test maximum wind speed (255 knots ≈ 0.0708 nm/s)
	maxWindPerComponent := float32(0.0705)
	s := MakeSample([2]float32{maxWindPerComponent, maxWindPerComponent}, 15.0, 5.0, 1013.2)
	wv := s.WindVec()

	// Should be able to represent this wind speed
	if math.Abs(float64(wv[0]-maxWindPerComponent)) > 0.001 {
		t.Errorf("Max wind component[0]: got %f, want %f", wv[0], maxWindPerComponent)
	}

	// Test negative wind components
	s = MakeSample([2]float32{-maxWindPerComponent, -maxWindPerComponent}, 15.0, 5.0, 1013.2)
	wv = s.WindVec()
	if math.Abs(float64(wv[0]+maxWindPerComponent)) > 0.001 {
		t.Errorf("Negative wind component[0]: got %f, want %f", wv[0], -maxWindPerComponent)
	}
}

func TestMakeSampleClamping(t *testing.T) {
	testCases := []struct {
		name           string
		windVec        [2]float32
		temperature    float32
		dewpoint       float32
		pressure       float32
		expectWindVec  [2]float32
		expectTemp     float32
		expectDewpoint float32
		expectPressure float32
		tolerance      float32
	}{
		{
			name:           "extreme wind clamped",
			windVec:        [2]float32{1.0, -1.0}, // way beyond ±0.0705 nm/s
			temperature:    15.0,
			dewpoint:       5.0,
			pressure:       1000.0,
			expectWindVec:  [2]float32{0.0705, -0.0711}, // clamped to max/min
			expectTemp:     15.0,
			expectDewpoint: 5.0,
			expectPressure: 1000.0,
			tolerance:      0.002,
		},
		{
			name:           "extreme temperature clamped",
			windVec:        [2]float32{0.01, 0.01},
			temperature:    200.0,  // way too hot
			dewpoint:       -200.0, // way too cold
			pressure:       1000.0,
			expectWindVec:  [2]float32{0.01, 0.01},
			expectTemp:     127.0,  // clamped to max
			expectDewpoint: -128.0, // clamped to min
			expectPressure: 1000.0,
			tolerance:      0.002,
		},
		{
			name:           "extreme pressure clamped high",
			windVec:        [2]float32{0.01, 0.01},
			temperature:    15.0,
			dewpoint:       5.0,
			pressure:       2000.0, // way too high
			expectWindVec:  [2]float32{0.01, 0.01},
			expectTemp:     15.0,
			expectDewpoint: 5.0,
			expectPressure: 1090.0, // clamped to max
			tolerance:      0.002,
		},
		{
			name:           "extreme pressure clamped low",
			windVec:        [2]float32{0.01, 0.01},
			temperature:    15.0,
			dewpoint:       5.0,
			pressure:       10.0, // way too low
			expectWindVec:  [2]float32{0.01, 0.01},
			expectTemp:     15.0,
			expectDewpoint: 5.0,
			expectPressure: 50.0, // clamped to min
			tolerance:      0.002,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := MakeSample(tc.windVec, tc.temperature, tc.dewpoint, tc.pressure)

			wv := s.WindVec()
			if math.Abs(float64(wv[0]-tc.expectWindVec[0])) > float64(tc.tolerance) {
				t.Errorf("WindVec[0]: got %f, expected %f", wv[0], tc.expectWindVec[0])
			}
			if math.Abs(float64(wv[1]-tc.expectWindVec[1])) > float64(tc.tolerance) {
				t.Errorf("WindVec[1]: got %f, expected %f", wv[1], tc.expectWindVec[1])
			}

			temp := s.Temperature()
			if math.Abs(float64(temp-tc.expectTemp)) > float64(tc.tolerance) {
				t.Errorf("Temperature: got %f, expected %f", temp, tc.expectTemp)
			}

			dewpoint := s.Dewpoint()
			if math.Abs(float64(dewpoint-tc.expectDewpoint)) > float64(tc.tolerance) {
				t.Errorf("Dewpoint: got %f, expected %f", dewpoint, tc.expectDewpoint)
			}

			pressure := s.Pressure()
			if math.Abs(float64(pressure-tc.expectPressure)) > 5.0 {
				t.Errorf("Pressure: got %f, expected %f", pressure, tc.expectPressure)
			}
		})
	}
}
