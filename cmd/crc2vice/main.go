// cmd/crc2vice/main.go

package main

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path"

	"github.com/mmp/vice/math"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
)

type ARTCC struct {
	VideoMaps []VideoMapSpec `json:"videoMaps"`
}

type VideoMapSpec struct {
	Id        string `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"shortName"`
	Category  string `json:"starsBrightnessCategory"`
	STARSId   int    `json:"starsId"`
}

type GeoJSON struct {
	Type     string           `json:"type"`
	Features []GeoJSONFeature `json:"features"`
}

type GeoJSONFeature struct {
	Type     string `json:"type"`
	Geometry struct {
		Type        string             `json:"type"`
		Coordinates GeoJSONCoordinates `json:"coordinates"`
	} `json:"geometry"`
}

type GeoJSONCoordinates []math.Point2LL

func (c *GeoJSONCoordinates) UnmarshalJSON(d []byte) error {
	*c = nil

	var coords []math.Point2LL
	if err := json.Unmarshal(d, &coords); err == nil {
		*c = coords
	}
	// Don't report any errors but assume that it's a point, polygon, ...
	return nil
}

func errorExit(msg string, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", msg, err)
	os.Exit(1)
}

func write(maps []sim.VideoMap, fn string) {
	gfn := fn + "-videomaps.gob"
	fmt.Printf("Writing %s... ", gfn)
	gf, err := os.Create(gfn)
	errorExit("creating file", err)
	defer gf.Close()

	vmf := sim.VideoMapLibrary{Maps: maps}
	err = gob.NewEncoder(gf).Encode(vmf)
	errorExit("GOB error", err)

	names := make(map[string]interface{})
	for _, m := range maps {
		names[m.Name] = nil
	}
	mfn := fn + "-manifest.gob"
	fmt.Printf("Writing %s... ", mfn)
	mf, err := os.Create(mfn)
	errorExit("creating file", err)
	defer mf.Close()
	err = gob.NewEncoder(mf).Encode(names)
	errorExit("GOB error", err)

	fmt.Printf("Done.\n")
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "crc2vice: expected ARTCC name as program argument (e.g., ZNY)\n")
		os.Exit(1)
	}
	base := os.Args[1]

	fn := "ARTCCs/" + base + ".json"
	artccFile, err := os.ReadFile(fn)
	errorExit(fmt.Sprintf("%s: unable to read ARTCC definition", fn), err)

	artcc := ARTCC{}
	err = json.Unmarshal(artccFile, &artcc)
	errorExit(fmt.Sprintf("%s: JSON error", artccFile), err)
	fmt.Printf("Read ARTCC definition: %s\n", fn)

	var maps []sim.VideoMap
	for _, m := range artcc.VideoMaps {
		group := 1
		if m.Category == "A" {
			group = 0
		}
		sm := sim.VideoMap{
			Group: group,
			Label: m.ShortName,
			Name:  m.Name,
			Id:    m.STARSId,
		}

		fn := path.Join("VideoMaps", base, m.Id) + ".geojson"
		file, err := os.ReadFile(fn)
		errorExit(fmt.Sprintf("%s: unable to read file", fn), err)

		var gj GeoJSON
		err = util.UnmarshalJSONBytes(file, &gj)
		if err != nil {
			fmt.Printf("\r%s: warning: %v\n", fn, err)
		}

		for _, f := range gj.Features {
			if f.Type != "Feature" {
				continue
			}

			if f.Geometry.Type != "LineString" {
				continue
			}

			sm.Lines = append(sm.Lines, f.Geometry.Coordinates)
		}

		maps = append(maps, sm)
	}
	fmt.Printf("\rRead video maps                                               \n")

	write(maps, base)
}
