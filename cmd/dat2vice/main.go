// cmd/dat2vice/main.go

package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/klauspost/compress/zstd"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

type ManifestMap struct {
	Filename string  `json:"filename"`
	Group    int     `json:"brightness"`
	Category int     `json:"category"`
	Label    string  `json:"label"`
	Name     string  `json:"title"`
	Id       int     `json:"number"`
	Color    int     `json:"color"`
	Radius   float64 `json:"radius"`
}

func main() {
	maxDist := flag.Float64("radius", 75, "distance in nautical miles beyond which map data is discarded")
	flag.Parse()

	if len(flag.Args()) != 2 {
		fmt.Printf("usage: dat2vice [-radius r] <manifest-filename.json> <result basename>\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	args := flag.Args()
	f, err := os.Open(args[0])
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	manifest, err := io.ReadAll(f)
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}

	var e util.ErrorLogger
	util.CheckJSON[[]ManifestMap](manifest, &e)
	if e.HaveErrors() {
		fmt.Printf("Errors in JSON. Exiting.\n")
		os.Exit(1)
	}

	var manifestMaps []ManifestMap
	if err := util.UnmarshalJSONBytes(manifest, &manifestMaps); err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}

	vmf := sim.VideoMapLibrary{}
	for _, m := range manifestMaps {
		d := m.Radius
		if d == 0 {
			d = *maxDist
		}
		sm, err := makeMap(m, float32(d))
		if err != nil {
			fmt.Printf("%v\n", err)
			os.Exit(1)
		}
		if slices.ContainsFunc(vmf.Maps, func(m sim.VideoMap) bool { return sm.Id == m.Id }) {
			fmt.Printf("Multiple maps have the same id: %d\n", sm.Id)
			os.Exit(1)
		}

		vmf.Maps = append(vmf.Maps, sm)
		fmt.Printf("read %s\n", m.Filename)
	}

	gf, err := os.Create(args[1] + "-videomaps.gob.zst")
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}
	defer gf.Close()

	zw, err := zstd.NewWriter(gf, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}
	defer zw.Close()
	if err = gob.NewEncoder(zw).Encode(vmf); err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}

	names := make(map[string]interface{})
	for _, m := range vmf.Maps {
		names[m.Name] = nil
	}
	mfn := args[1] + "-manifest.gob"
	mf, err := os.Create(mfn)
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}
	defer mf.Close()
	if err = gob.NewEncoder(mf).Encode(names); err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}
}

func makeMap(mm ManifestMap, maxDist float32) (sim.VideoMap, error) {
	sm := sim.VideoMap{
		Group:    mm.Group,
		Label:    mm.Label,
		Name:     mm.Name,
		Id:       mm.Id,
		Category: mm.Category,
		Color:    mm.Color,
	}

	if mm.Group != 0 && mm.Group != 1 {
		return sm, fmt.Errorf("\"brightness\" must be 0 or 1 for map %s", mm.Filename)
	}
	if mm.Color < 0 || mm.Color > 8 {
		return sm, fmt.Errorf("\"color\" must be between 1 and 8 for map %s", mm.Filename)
	}
	if mm.Category < -1 || mm.Category > 9 {
		return sm, fmt.Errorf("\"category\" must be between -1 and 9 for map %s", mm.Filename)
	}

	r, err := os.Open(mm.Filename)
	if err != nil {
		return sm, err
	}
	defer r.Close()

	var center math.Point2LL
	var currentLineStrip []math.Point2LL
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
		parseLatLong := func(line []byte) math.Point2LL {
			lat, latmin, latsec, latsecdec := parseInt(line[:2]), parseInt(line[3:5]), parseInt(line[6:8]), parseInt(line[9:13])
			lon, lonmin, lonsec, lonsecdec := parseInt(line[15:18]), parseInt(line[19:21]), parseInt(line[22:24]), parseInt(line[25:29])
			return math.Point2LL{
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
			return sm, fmt.Errorf("%s: unexpected line in DAT file: \"%s\"", mm.Filename, line)
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
			return sm, fmt.Errorf("%s: unexpected line in DAT file: \"%s\"", mm.Filename, line)
		}
	}

	if currentLineStrip != nil {
		sm.Lines = append(sm.Lines, currentLineStrip)
	}

	if center[0] == 0 && center[1] == 0 {
		return sm, fmt.Errorf("Center not found in DAT file")
	}

	sm.Lines = util.FilterSlice(sm.Lines, func(strip []math.Point2LL) bool {
		for _, p := range strip {
			if math.NMDistance2LL(p, center) > maxDist {
				return false
			}
		}
		return true
	})

	return sm, nil
}
