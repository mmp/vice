// dat2vice.go

package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"fmt"
	gomath "math"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/exp/constraints"
)

type Point2LL [2]float32

// Note: this should match STARSMap in stars.go
type STARSMap struct {
	Group int
	Label string
	Name  string
	Id    int
	Lines [][]Point2LL
}

func main() {
	if len(os.Args) == 1 {
		fmt.Printf("usage: dat2vice [dat filenames...]\n")
		os.Exit(1)
	}

	var maps []STARSMap
	for _, fn := range os.Args[1:] {
		maps = append(maps, makeMap(fn))
	}

	// Write the GOB file with everything
	gf, err := os.Create("out-videomaps.gob")
	if err != nil {
		panic(err)
	}
	defer gf.Close()
	if err = gob.NewEncoder(gf).Encode(maps); err != nil {
		panic(err)
	}

	// Write the manifest file (without the lines)
	names := make(map[string]interface{})
	for _, m := range maps {
		names[m.Name] = nil
	}
	mfn := "out-manifest.gob"
	mf, err := os.Create(mfn)
	if err != nil {
		panic(err)
	}
	defer mf.Close()
	if err = gob.NewEncoder(mf).Encode(names); err != nil {
		panic(err)
	}

	fmt.Printf("Available maps:\n")
	for _, name := range SortedMapKeys(names) {
		fmt.Println(name)
	}
}

func makeMap(fn string) STARSMap {
	r, err := os.Open(fn)
	if err != nil {
		panic(err)
	}
	defer r.Close()

	name, _, _ := strings.Cut(fn, ".")
	id, err := strconv.Atoi(fn[3:6])
	if err != nil {
		panic(err)
	}
	sm := STARSMap{
		Label: name,
		Name:  name,
		Id:    id,
	}

	var center Point2LL
	var currentLineStrip []Point2LL
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := []byte(scanner.Text())

		parseInt := func(b []byte) float32 {
			v := 0
			for i, ch := range b {
				v *= 10
				if ch < '0' || ch > '9' {
					panic(fmt.Sprintf("Non-numeric value found at column %d: \"%s\"", i, string(b)))
				}
				v += int(ch - '0')
			}
			return float32(v)
		}
		parseLatLong := func(line []byte) Point2LL {
			lat, latmin, latsec, latsecdec := parseInt(line[:2]), parseInt(line[3:5]), parseInt(line[6:8]), parseInt(line[9:13])
			lon, lonmin, lonsec, lonsecdec := parseInt(line[15:18]), parseInt(line[19:21]), parseInt(line[22:24]), parseInt(line[25:29])
			return Point2LL{
				// Assume West, so negate longitude...
				-(lon + lonmin/60 + lonsec/3600 + lonsecdec/(3600*10000)),
				lat + latmin/60 + latsec/3600 + latsecdec/(3600*10000),
			}
		}

		if len(line) > 8 && line[0] == '!' {
			// Extract center or radius
			if string(line[4:8]) == "9900" {
				center = parseLatLong(line[12:])
			}
			continue
		}

		if bang := bytes.IndexByte(line, '!'); bang == -1 {
			panic(fmt.Sprintf("Unexpected line in DAT file: \"%s\"", line))
		} else {
			line = line[:bang]
		}

		if len(line) == 0 {
			continue
		} else if string(line) == "LINE " {
			// start a new line
			sm.Lines = append(sm.Lines, currentLineStrip)
			currentLineStrip = nil
		} else if len(line) == 34 && string(line[:3]) == "GP " {
			// Assume this format is 100% column based for efficiency...

			// Lines are of the following form. Pull out the values from the columns...
			// GP 42 20 55.0000  071 00 22.0000  !
			pt := parseLatLong(line[3:])
			currentLineStrip = append(currentLineStrip, pt)
		} else {
			panic(fmt.Sprintf("Unexpected line in DAT file: \"%s\"", line))
		}
	}

	if currentLineStrip != nil {
		sm.Lines = append(sm.Lines, currentLineStrip)
	}

	if center[0] == 0 && center[1] == 0 {
		panic("Center not found in DAT file")
	}

	const maxDist = 75
	sm.Lines = FilterSlice(sm.Lines, func(strip []Point2LL) bool {
		for _, p := range strip {
			if nmdistance2ll(p, center) > maxDist {
				return false
			}
		}
		return true
	})

	return sm
}

func FilterSlice[V any](s []V, pred func(V) bool) []V {
	var filtered []V
	for _, item := range s {
		if pred(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func nmdistance2ll(a Point2LL, b Point2LL) float32 {
	// https://www.movable-type.co.uk/scripts/latlong.html
	const R = 6371000 // metres
	rad := func(d float64) float64 { return float64(d) / 180 * gomath.Pi }
	lat1, lon1 := rad(float64(a[1])), rad(float64(a[0]))
	lat2, lon2 := rad(float64(b[1])), rad(float64(b[0]))
	dlat, dlon := lat2-lat1, lon2-lon1

	x := Sqr(gomath.Sin(dlat/2)) + gomath.Cos(lat1)*gomath.Cos(lat2)*Sqr(gomath.Sin(dlon/2))
	c := 2 * gomath.Atan2(gomath.Sqrt(x), gomath.Sqrt(1-x))
	dm := R * c // in metres

	return float32(dm * 0.000539957)
}

func Sqr(x float64) float64 { return x * x }

// SortedMapKeys returns the keys of the given map, sorted from low to high.
func SortedMapKeys[K constraints.Ordered, V any](m map[K]V) []K {
	keys, _ := FlattenMap(m)
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func FlattenMap[K comparable, V any](m map[K]V) ([]K, []V) {
	keys := make([]K, 0, len(m))
	values := make([]V, 0, len(m))
	for k, v := range m {
		keys = append(keys, k)
		values = append(values, v)
	}
	return keys, values
}
