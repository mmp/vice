// extract the B, C, and D airspace from the FAA Class_Airspace.geojson file
// geojson on stdin, files writeen

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/simplify"
)

type AirspaceLoop [][2]float32

type Airspace struct {
	Bottom, Top int
	Loops       []AirspaceLoop
}

func getProp[T any](m map[string]interface{}, name string) (T, bool) {
	p, ok := m[name]
	if !ok {
		var t T
		return t, false
	}

	pv, ok := p.(T)
	if !ok {
		var t T
		return t, false
	}

	return pv, true
}

func main() {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}

	bravo := make(map[string][]Airspace)
	charlie := make(map[string][]Airspace)
	delta := make(map[string][]Airspace)

	fc, err := geojson.UnmarshalFeatureCollection(b)
	if err != nil {
		panic(err)
	}

	for _, f := range fc.Features {
		name, ok := getProp[string](f.Properties, "NAME")

		isBravo := strings.HasSuffix(name, " CLASS B")
		isCharlie := strings.HasSuffix(name, " CLASS C")
		isDelta := strings.HasSuffix(name, " CLASS D")
		if !isBravo && !isCharlie && !isDelta {
			continue
		}

		g := f.Geometry
		poly, ok := g.(orb.Polygon)
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: unexpected type %T\n", name, g)
			continue
		}

		low, ok := getProp[float64](f.Properties, "LOWER_VAL")
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: no lower: %+v\n", name, f.Properties)
			continue
		}
		high, ok := getProp[float64](f.Properties, "UPPER_VAL")
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: no upper: %+v\n", name, f.Properties)
			continue
		}

		airspace := Airspace{Bottom: int(low), Top: int(high)}

		n := 0
		fmt.Printf("%s: ", name)
		for _, ring := range poly {
			var loop AirspaceLoop
			if len(ring) > 100 {
				fmt.Printf("simp %d -> ", len(ring))
				simp := simplify.DouglasPeucker(0.00001).Simplify(ring)
				r2, ok := simp.(orb.Ring)
				if !ok {
					panic("no orb.Ring back from simplify?!")
				}
				ring = r2
			}
			fmt.Printf("%d ", len(ring))

			for _, pt := range ring {
				loop = append(loop, [2]float32{float32(pt[0]), float32(pt[1])})
				n++
			}
			airspace.Loops = append(airspace.Loops, loop)
		}
		fmt.Printf(" = %d verts total\n", n)

		if isBravo {
			name = strings.TrimSuffix(name, " CLASS B")
			bravo[name] = append(bravo[name], airspace)
		} else if isCharlie {
			name = strings.TrimSuffix(name, " CLASS C")
			charlie[name] = append(charlie[name], airspace)
		} else if isDelta {
			name = strings.TrimSuffix(name, " CLASS D")
			delta[name] = append(delta[name], airspace)
		}
	}

	jb, err := json.Marshal(bravo)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("bravo-airspace.json", jb, 0o644); err != nil {
		panic(err)
	}

	jc, err := json.Marshal(charlie)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("charlie-airspace.json", jc, 0o644); err != nil {
		panic(err)
	}

	jd, err := json.Marshal(delta)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("delta-airspace.json", jd, 0o644); err != nil {
		panic(err)
	}
}
