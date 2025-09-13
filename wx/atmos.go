// wx/atmos.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type Sample struct {
	UComponent  float32 // eastward
	VComponent  float32 // northward
	Temperature float32 // Kelvin
	Height      float32 // geopotential height (meters)
}

// This is fairly specialized to our needs we ingest from: at each
// lat-long, we store a vertical stack of 40 levels with samples from the
// source HRRR files (wind direction, speed, temperature, height). The
// vertical indexing of is low to high where LevelIndexFromId and
// IdFromLevelIndex perform the indexing and its inverse.
const NumSampleLevels = 40

type Atmos struct {
	// Lat-longs to stack of levels
	SampleStacks map[math.Point2LL]*SampleStack
}

type SampleStack struct {
	Levels [NumSampleLevels]Sample
}

// For storage, this information is encoded in structure-of-arrays format,
// which makes it more compressible.
type AtmosSOA struct {
	Lat, Long []float32
	Levels    [NumSampleLevels]LevelsSOA
}

const windHeightOffset = 500

type LevelsSOA struct {
	// All of the following are delta encoded
	Heading     []uint8 // degrees/2
	Speed       []uint8 // knots
	Temperature []int8  // Temperature in Celsius
	Height      []uint8 // geopotential height (MSL) + windHeightOffset in meters
}

func MakeAtmos() Atmos {
	return Atmos{SampleStacks: make(map[math.Point2LL]*SampleStack)}
}

func LevelIndexFromId(b []byte) int {
	// Very simple/limited atoi for just what we need
	atoi := func(b []byte) int {
		i := 0
		for _, d := range b {
			if d == ' ' {
				break
			}
			i *= 10
			i += int(d - '0')
		}
		return i
	}

	if !bytes.HasSuffix(b, []byte(" mb")) {
		return -1 // don't care
	} else if bytes.HasPrefix(b, []byte("1013")) { // 1013.2
		return 0
	} else if mb := atoi(b); mb >= 50 && mb <= 1000 {
		// We should be seeing values ranging from 50-1000 in steps of 25
		if (mb-50)%25 != 0 {
			panic("unexpected mb: " + string(b))
		}
		// First map 50 -> 0, 75 -> 1, ...
		i := (mb - 50) / 25
		// Reverse: 0 -> 38, 1 -> 37, ...
		i = 38 - i
		// Offset to account for 1013.2 at the start
		return 1 + i
	} else {
		panic("unexpected mb " + string(b))
	}
}

func IdFromLevelIndex(i int) string {
	switch {
	case i == 0:
		return "1013.2 mb"
	case i >= 1 && i < NumSampleLevels:
		i -= 1
		i = 38 - i
		return fmt.Sprintf("%d mb", 50+25*i)
	default:
		panic("unexpected level index " + strconv.Itoa(i))
	}
}

func uvToDirSpeed(u, v float32) (float32, float32) {
	dir := 270 - math.Degrees(math.Atan2(v, u))
	dir = math.NormalizeHeading(dir)

	spd := math.Sqrt(u*u+v*v) * 1.94384 // m/s-> knots

	return float32(dir), float32(spd)
}

func dirSpeedToUV(dir, speed float32) (float32, float32) {
	s := speed * 0.51444 // knots -> m/s
	d := math.Radians(dir)
	return -s * float32(math.Sin(d)), -s * float32(math.Cos(d))
}

func (sf Atmos) ToSOA() (AtmosSOA, error) {
	soa := AtmosSOA{}

	pts := slices.Collect(maps.Keys(sf.SampleStacks))
	slices.SortFunc(pts, func(a, b math.Point2LL) int {
		if a[0] < b[0] {
			return -1
		} else if a[0] > b[0] {
			return 1
		} else if a[1] < b[1] {
			return -1
		} else if a[1] > b[1] {
			return 1
		}
		return 0
	})

	for _, pt := range pts {
		soa.Long = append(soa.Long, pt[0])
		soa.Lat = append(soa.Lat, pt[1])

		for i, level := range sf.SampleStacks[pt].Levels {
			hdg, spd := uvToDirSpeed(level.UComponent, level.VComponent)
			if hdg < 0 || hdg > 360 {
				return AtmosSOA{}, fmt.Errorf("bad heading: %f not in 0-360", hdg)
			}
			if spd < 0 || spd > 255 {
				return AtmosSOA{}, fmt.Errorf("bad speed: %f not in 0-255", spd)
			}
			soa.Levels[i].Heading = append(soa.Levels[i].Heading, uint8(math.Round(hdg+1)/2))
			soa.Levels[i].Speed = append(soa.Levels[i].Speed, uint8(math.Round(spd)))

			tc := level.Temperature - 273.15 // K -> C
			tq := int(math.Round(tc))
			if tq < -128 || tq > 127 {
				return AtmosSOA{}, fmt.Errorf("bad temperature: %d not in -128-127", tq)
			}
			soa.Levels[i].Temperature = append(soa.Levels[i].Temperature, int8(tq))

			h := level.Height + windHeightOffset // deal with slightly below sea level
			h = (h + 50) / 100                   // 100s of meters
			if h < 0 || h > 255 {
				return AtmosSOA{}, fmt.Errorf("bad remapped height: %f not in 0-255", h)
			}
			soa.Levels[i].Height = append(soa.Levels[i].Height, uint8(h))
		}
	}

	for i := range soa.Levels {
		soa.Levels[i].Heading = util.DeltaEncode(soa.Levels[i].Heading)
		soa.Levels[i].Speed = util.DeltaEncode(soa.Levels[i].Speed)
		soa.Levels[i].Temperature = util.DeltaEncode(soa.Levels[i].Temperature)
		soa.Levels[i].Height = util.DeltaEncode(soa.Levels[i].Height)
	}

	return soa, nil
}

func (atsoa AtmosSOA) ToAOS() Atmos {
	at := MakeAtmos()

	var levels [NumSampleLevels]LevelsSOA
	for i := range atsoa.Levels {
		levels[i].Heading = util.DeltaDecode(atsoa.Levels[i].Heading)
		levels[i].Speed = util.DeltaDecode(atsoa.Levels[i].Speed)
		levels[i].Temperature = util.DeltaDecode(atsoa.Levels[i].Temperature)
		levels[i].Height = util.DeltaDecode(atsoa.Levels[i].Height)
	}

	for i := range atsoa.Lat {
		var stack SampleStack
		for j, level := range levels {
			s := Sample{
				Temperature: float32(level.Temperature[i]) + 273.15, // C -> K
				Height:      float32(level.Height[i])*100 - windHeightOffset,
			}
			s.UComponent, s.VComponent = dirSpeedToUV(float32(level.Heading[i])*2, float32(level.Speed[i]))

			stack.Levels[j] = s
		}

		at.SampleStacks[math.Point2LL{atsoa.Long[i], atsoa.Lat[i]}] = &stack
	}

	return at
}

func CheckAtmosConversion(at Atmos, soa AtmosSOA) error {
	ckat := soa.ToAOS()
	if len(ckat.SampleStacks) != len(at.SampleStacks) {
		return fmt.Errorf("mismatch in number of entries %d - %d", len(at.SampleStacks), len(ckat.SampleStacks))
	}
	for p, stack := range at.SampleStacks {
		ckstack, ok := ckat.SampleStacks[p]
		if !ok {
			return fmt.Errorf("missing point in SampleStacks map %v", p)
		}
		for i := range stack.Levels {
			sl, ckl := stack.Levels[i], ckstack.Levels[i]

			d, s := uvToDirSpeed(sl.UComponent, sl.VComponent)
			cd, cs := uvToDirSpeed(ckl.UComponent, ckl.VComponent)
			if s >= 1 { // don't worry about direction for idle winds
				if math.HeadingDifference(d, cd) > 2 {
					fmt.Printf("SL: %+v\n", sl)
					fmt.Printf("Dir %f spd %f\n", d, s)

					fmt.Printf("CK: %+v\n", ckl)
					fmt.Printf("Dir %f spd %f\n", cd, cs)

					return fmt.Errorf("Direction mismatch round trip %f - %f", d, cd)
				}
			}

			if math.Abs(s-cs) > 1 {
				fmt.Printf("SL: %+v\n", sl)
				fmt.Printf("Dir %f spd %f\n", d, s)

				fmt.Printf("CK: %+v\n", ckl)
				fmt.Printf("Dir %f spd %f\n", cd, cs)

				return fmt.Errorf("Speed mismatch round trip %f - %f", s, cs)
			}

			if math.Abs(sl.Temperature-ckl.Temperature) > 0.51 {
				return fmt.Errorf("Temperature mismatch round trip %f - %f", sl.Temperature, ckl.Temperature)
			}
			if math.Abs(sl.Height-ckl.Height) > 51 {
				return fmt.Errorf("Height mismatch round trip %f - %f", sl.Height, ckl.Height)
			}
		}
	}
	return nil
}
