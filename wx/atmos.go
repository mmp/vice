// wx/atmos.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"bytes"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// NOTE: PANC (A11) is not included: we only process the conus dataset for
// now and giving that -small_grib with the PANC lat-longs generates a
// ~1.4GB grib file, for reasons unknown.
//
// vice -listscenarios 2>/dev/null | cut -d / -f 1 | grep -v A11 | uniq
var AtmosTRACONs = []string{
	"A80", "A90", "AAC", "ABE", "ABQ", "AGS", "ALB", "ASE", "AUS", "AVL", "BGR",
	"BHM", "BIL", "BNA", "BOI", "BTV", "BUF", "C90", "CHS", "CID", "CLE", "CLT", "COS",
	"CPR", "D01", "D10", "D21", "DAB", "EWR", "F11", "GSO", "GSP", "GTF", "I90", "IND",
	"JAX", "L30", "M98", "MCI", "MDT", "MIA", "MKE", "N90", "NCT", "OKC", "P31", "P50",
	"P80", "PCT", "PHL", "PIT", "PVD", "PWM", "R90", "RDU", "S46", "S56", "SAV", "SBA",
	"SBN", "SCT", "SDF", "SGF", "SYR", "TPA", "Y90",
}

// This is fairly specialized to our needs we ingest from: at each
// lat-long, we store a vertical stack of 40 levels with samples from the
// source HRRR files (wind direction, speed, temperature, height). The
// vertical indexing of is low to high where LevelIndexFromId and
// IdFromLevelIndex perform the indexing and its inverse.
const NumSampleLevels = 40

type Atmos struct {
	// Lat-longs to stack of levels
	SampleStacks map[math.Point2LL]*AtmosSampleStack
}

type AtmosSampleStack struct {
	Levels [NumSampleLevels]AtmosSample
}

// The U-component represents the eastward wind component (positive values
// indicate wind from west to east), and the V-component represents the
// northward wind component (positive values indicate wind from South to
// North); note that a) this is the opposite of than the aviation convention,
// and b) that elsewhere in vice, working with nm-based coordinates, positive y
// points South.
type AtmosSample struct {
	UComponent  float32 // eastward m/s
	VComponent  float32 // northward m/s
	Temperature float32 // Kelvin
	Height      float32 // geopotential height (meters)
}

// For storage, this information is encoded in structure-of-arrays format,
// which makes it more compressible.
type AtmosSOA struct {
	Lat, Long []float32
	Levels    [NumSampleLevels]AtmosLevelsSOA
}

const windHeightOffset = 500

type AtmosLevelsSOA struct {
	// All of the following are delta encoded
	Heading     []uint8 // degrees/2
	Speed       []uint8 // knots
	Temperature []int8  // Temperature in Celsius
	Height      []uint8 // geopotential height (MSL) + windHeightOffset in meters
}

func MakeAtmos() Atmos {
	return Atmos{SampleStacks: make(map[math.Point2LL]*AtmosSampleStack)}
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

func (at Atmos) ToSOA() (AtmosSOA, error) {
	soa := AtmosSOA{}

	pts := slices.Collect(maps.Keys(at.SampleStacks))
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

		for i, level := range at.SampleStacks[pt].Levels {
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

	var levels [NumSampleLevels]AtmosLevelsSOA
	for i := range atsoa.Levels {
		levels[i].Heading = util.DeltaDecode(atsoa.Levels[i].Heading)
		levels[i].Speed = util.DeltaDecode(atsoa.Levels[i].Speed)
		levels[i].Temperature = util.DeltaDecode(atsoa.Levels[i].Temperature)
		levels[i].Height = util.DeltaDecode(atsoa.Levels[i].Height)
	}

	for i := range atsoa.Lat {
		var stack AtmosSampleStack
		for j, level := range levels {
			s := AtmosSample{
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

func (at Atmos) GetGrid() *AtmosGrid {
	return MakeAtmosGrid(at.SampleStacks)
}

///////////////////////////////////////////////////////////////////////////
// AtmosGrid

type AtmosGrid struct {
	Extent   math.Extent2D
	Res      [3]int
	AltRange [2]float32
	Points   []Sample
}

type WindSample struct {
	// WindVec represents the force acting on an aircraft.
	WindVec [2]float32 // nm / s
}

type Sample struct {
	WindSample
	Temperature float32 // Celsius
}

func (s WindSample) WindDirection() float32 {
	// Because WindVec represents how the wind applies force to the
	// aircraft, we need to spin it around to get its direction.
	return math.OppositeHeading(math.VectorHeading(s.WindVec))
}

func (s WindSample) WindSpeed() float32 { // returns knots
	l := math.Length2f(s.WindVec)
	return l * 3600 /* seconds -> hour */
}

func LerpSample(x float32, s0, s1 Sample) Sample {
	return Sample{
		WindSample:  WindSample{WindVec: math.Lerp2f(x, s0.WindVec, s1.WindVec)},
		Temperature: math.Lerp(x, s0.Temperature, s1.Temperature),
	}
}

func MakeAtmosGrid(sampleStacks map[math.Point2LL]*AtmosSampleStack) *AtmosGrid {
	g := &AtmosGrid{
		Extent:   math.Extent2DFromSeq(maps.Keys(sampleStacks)),
		AltRange: [2]float32{24000, 24000}, // will fix up min below
	}

	const xyDelta = 2 /* roughly 2nm spacing */
	nmPerLongitude := math.NMPerLongitudeAt(math.Point2LL(g.Extent.Center()))
	g.Res[0] = int(max(2, nmPerLongitude*g.Extent.Width()/xyDelta))
	g.Res[1] = int(max(2, 60*g.Extent.Height()/xyDelta))

	const metersToFeet = 3.28084
	for _, stack := range sampleStacks {
		for _, level := range stack.Levels {
			g.AltRange[0] = min(g.AltRange[0], level.Height*metersToFeet)
		}
	}
	// roughly one level per 1000' feet (though nonlinearly distributed)
	g.Res[2] = 1 + int((g.AltRange[1]-g.AltRange[0]+500)/1000)

	g.Points = make([]Sample, g.Res[0]*g.Res[1]*g.Res[2])
	sumWt := make([]float32, g.Res[0]*g.Res[1]*g.Res[2])

	// In xy, we accumulate to the nearest sample in the grid; for
	// z/altitude, we lerp between  along the non-uniform altitude spacing from the
	// original data.
	for p, stack := range sampleStacks {
		pg, _ := g.ptToGrid(p)
		ipg := [2]int{int(math.Round(pg[0])), int(math.Round(pg[1]))}

		for z := range g.Res[2] {
			const feetToMeters = 0.3048
			altm := g.gridToAlt(z) * feetToMeters

			idx, ok := slices.BinarySearchFunc(stack.Levels[:], altm, func(s AtmosSample, alt float32) int {
				if s.Height < alt {
					return -1
				} else if s.Height > alt {
					return 1
				} else {
					return 0
				}
			})
			if !ok && idx > 0 {
				idx--
			}

			s0, s1 := stack.Levels[idx], stack.Levels[idx+1]
			t := (altm - s0.Height) / (s1.Height - s0.Height)

			// Convert wind vector from m/s to nm/s since the nav code all
			// works w.r.t nautical miles.  Further, negate the x component
			// to account for the fact that the grib2 files follow
			// meteorological conventions and give where the wind is coming
			// from, while for sim purposes, we generally want the vector
			// representing force on aircraft.  However, we do *not* negate
			// y: we have one negation for that, but then need a second to
			// account for the fact that in lat-long and the linearized nm
			// coordinate system we do simulation in +y represents moving
			// South, not North as it is in grib2. So those two cancel...
			const meterToNm = 0.0005399568
			v0 := [2]float32{-s0.UComponent * meterToNm, s0.VComponent * meterToNm}
			v1 := [2]float32{-s1.UComponent * meterToNm, s1.VComponent * meterToNm}
			gidx := ipg[0] + ipg[1]*g.Res[0] + z*g.Res[0]*g.Res[1]
			g.Points[gidx].WindVec = math.Add2f(g.Points[gidx].WindVec, math.Lerp2f(t, v0, v1))

			const kelvinToCelsius = -273.15
			g.Points[gidx].Temperature += math.Lerp(t, s0.Temperature, s1.Temperature) + kelvinToCelsius

			sumWt[gidx]++
		}
	}

	for i, wt := range sumWt {
		if wt != 0 {
			g.Points[i].WindVec = math.Scale2f(g.Points[i].WindVec, 1/wt)
			g.Points[i].Temperature /= wt
		}
	}

	return g
}

func (g *AtmosGrid) ptToGrid(p [2]float32) ([2]float32, bool) {
	if !g.Extent.Inside(p) {
		return [2]float32{0, 0}, false
	}
	pg := math.Sub2f(p, g.Extent.P0)
	pg[0] *= float32(g.Res[0]-1) / g.Extent.Width()
	pg[1] *= float32(g.Res[1]-1) / g.Extent.Height()

	pg[0] = math.Clamp(pg[0], 0, float32(g.Res[0])-1)
	pg[1] = math.Clamp(pg[1], 0, float32(g.Res[1])-1)

	return pg, true
}

func (g *AtmosGrid) gridToAlt(z int) float32 {
	t := float32(z) / float32(g.Res[2]-1)
	t = math.Clamp(t, 0, 1)
	t *= t
	return math.Lerp(t, g.AltRange[0], g.AltRange[1])
}

// given an altitude, returns a continuous coordinate in z up to g.Res[2]-1
// where the fractional component represents the offset between the two
// altitudes that bracket [alt].
func (g *AtmosGrid) altToGrid(alt float32) float32 {
	if alt < g.AltRange[0] {
		return 0
	} else if alt > g.AltRange[1] {
		return float32(g.Res[2] - 1)
	} else {
		z := (alt - g.AltRange[0]) / (g.AltRange[1] - g.AltRange[0])
		z = math.Sqrt(z) // more precision near ground
		return z * float32(g.Res[2]-1)
	}
}

func (g *AtmosGrid) Lookup(p math.Point2LL, alt float32) (Sample, bool) {
	pg, ok := g.ptToGrid(p)
	if !ok {
		return Sample{}, false
	}
	zg := g.altToGrid(alt)

	// Closest lookup for now
	ipg := [2]int{int(math.Round(pg[0])), int(math.Round(pg[1]))}
	izg := int(math.Round(zg))
	idx := ipg[0] + ipg[1]*g.Res[0] + izg*g.Res[0]*g.Res[1]

	if g.Points[idx].Temperature == 0 {
		// No samples hit this one during the splat phase
		return Sample{}, false
	}
	return g.Points[idx], true
}

func (g *AtmosGrid) AltitudeForIndex(idx int) float32 {
	return g.gridToAlt(idx)
}

func (g *AtmosGrid) SamplesAtLevel(level, step int) iter.Seq2[math.Point2LL, Sample] {
	return func(yield func(math.Point2LL, Sample) bool) {
		for y := 0; y < g.Res[1]; y += step {
			ty := float32(y) / float32(g.Res[1]-1)
			for x := 0; x < g.Res[0]; x += step {
				tx := float32(x) / float32(g.Res[0]-1)
				idx := x + y*g.Res[0] + level*g.Res[0]*g.Res[1]

				p := math.Point2LL(g.Extent.Lerp([2]float32{tx, ty}))
				if !yield(p, g.Points[idx]) {
					return
				}
			}
		}
	}
}
