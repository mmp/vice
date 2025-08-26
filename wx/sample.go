package wx

import (
	"fmt"
	"maps"
	"slices"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

type Sample struct {
	MB          float32
	UComponent  float32 // eastward
	VComponent  float32 // northward
	Temperature float32 // Kelvin, evidently
	Height      float32 // geopotential height
}

// Lat-longs to stack of levels
type SampleSet map[[2]float32][]Sample

type SampleSetSOA struct {
	Lat, Long []float32
	Levels    []MBLevelSOA
}

const WindHeightOffset = 500

type MBLevelSOA struct {
	MB float32
	// All of the following are delta encoded
	Heading     []uint8 // degrees/2
	Speed       []uint8 // knots
	Temperature []int8  // Temperature in Celsius
	Height      []uint8 // geopotential height + WindHeightOffset in 100s of feet
}

func UVToDirSpeed(u, v float32) (float32, float32) {
	dir := 270 - math.Degrees(math.Atan2(v, u))
	dir = math.NormalizeHeading(dir)

	spd := math.Sqrt(u*u+v*v) * 1.94384 // m/s-> knots

	return float32(dir), float32(spd)
}

func DirSpeedToUV(dir, speed float32) (float32, float32) {
	s := speed * 0.51444 // knots -> m/s
	d := math.Radians(dir)
	return -s * float32(math.Sin(d)), -s * float32(math.Cos(d))
}

func SampleSetToSOA(c SampleSet) (SampleSetSOA, error) {
	soa := SampleSetSOA{}

	keys := slices.Collect(maps.Keys(c))
	slices.SortFunc(keys, func(a, b [2]float32) int {
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

	for _, k := range keys {
		// TODO: check ordering of latlong
		soa.Long = append(soa.Long, k[0])
		soa.Lat = append(soa.Lat, k[1])

		levels := c[k]
		if len(soa.Levels) == 0 {
			soa.Levels = make([]MBLevelSOA, len(levels))
		} else if len(levels) != len(soa.Levels) {
			return SampleSetSOA{}, fmt.Errorf("non-uniform number of levels in different entries")
		}

		for i, level := range levels {
			if soa.Levels[i].MB == 0 {
				soa.Levels[i].MB = level.MB
			} else if soa.Levels[i].MB != level.MB {
				return SampleSetSOA{}, fmt.Errorf("different MB layout in different cells")
			}

			hdg, spd := UVToDirSpeed(level.UComponent, level.VComponent)
			if hdg < 0 || hdg > 360 {
				return SampleSetSOA{}, fmt.Errorf("bad heading: %f not in 0-360", hdg)
			}
			if spd < 0 || spd > 255 {
				return SampleSetSOA{}, fmt.Errorf("bad speed: %f not in 0-255", spd)
			}
			soa.Levels[i].Heading = append(soa.Levels[i].Heading, uint8(math.Round(hdg+1)/2))
			soa.Levels[i].Speed = append(soa.Levels[i].Speed, uint8(math.Round(spd)))

			tc := level.Temperature - 273.15 // K -> C
			tq := int(math.Round(tc))
			if tq < -128 || tq > 127 {
				return SampleSetSOA{}, fmt.Errorf("bad temperature: %d not in -128-127", tq)
			}
			soa.Levels[i].Temperature = append(soa.Levels[i].Temperature, int8(tq))

			h := level.Height + WindHeightOffset // deal with slightly below sea level
			h = (h + 50) / 100                   // 100s of feet
			if h < 0 || h > 255 {
				return SampleSetSOA{}, fmt.Errorf("bad remapped height: %f not in 0-255", h)
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

func SampleSetSOAToAOS(s SampleSetSOA) SampleSet {
	w := make(map[[2]float32][]Sample)

	levels := make([]MBLevelSOA, len(s.Levels))
	for i := range s.Levels {
		levels[i].MB = s.Levels[i].MB
		levels[i].Heading = util.DeltaDecode(s.Levels[i].Heading)
		levels[i].Speed = util.DeltaDecode(s.Levels[i].Speed)
		levels[i].Temperature = util.DeltaDecode(s.Levels[i].Temperature)
		levels[i].Height = util.DeltaDecode(s.Levels[i].Height)
	}

	for i := range s.Lat {
		samplevels := make([]Sample, len(levels))
		for j, level := range levels {
			wl := Sample{
				MB:          level.MB,
				Temperature: float32(level.Temperature[i]) + 273.15, // C -> K
				Height:      float32(level.Height[i])*100 - WindHeightOffset,
			}
			wl.UComponent, wl.VComponent = DirSpeedToUV(float32(level.Heading[i])*2, float32(level.Speed[i]))

			samplevels[j] = wl
		}

		w[[2]float32{s.Long[i], s.Lat[i]}] = samplevels
	}

	return w
}

func CheckSampleSetConversion(cell SampleSet, soa SampleSetSOA) error {
	ckcell := SampleSetSOAToAOS(soa)
	if len(ckcell) != len(cell) {
		return fmt.Errorf("mismatch in number of entries %d - %d", len(cell), len(ckcell))
	}
	for p, levels := range cell {
		cklevels, ok := ckcell[p]
		if !ok {
			return fmt.Errorf("missing point in map %v", p)
		}
		for i := range levels {
			sl, ckl := levels[i], cklevels[i]

			if sl.MB != ckl.MB {
				return fmt.Errorf("MB mismatch round trip %f - %f", sl.MB, ckl.MB)
			}

			d, s := UVToDirSpeed(sl.UComponent, sl.VComponent)
			cd, cs := UVToDirSpeed(ckl.UComponent, ckl.VComponent)
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
