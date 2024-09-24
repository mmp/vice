package main

import (
	"bufio"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
)

type SignificantPoint struct {
	Name         string
	ShortName    string
	Description  string
	Abbreviation string
	Lines        [2]string
	Position     [2]float32
}

func main() {
	f, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)

	pts := make(map[int]*SignificantPoint)

	for s.Scan() {
		l := s.Text()
		if len(l) == 0 || l[0] != '|' {
			continue
		}

		f := strings.Split(l[1:], "|")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}

		n, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}

		if len(f) == 12 {
			// First set
			if f[5] == "Y" {
				// sim only
				continue
			}
			if f[3] == "" || f[7] == "" || f[9] == "" {
				// missing description and/or latlong
				continue
			}

			pfloat := func(idx int) float32 {
				v, err := strconv.Atoi(f[idx])
				if err != nil {
					panic(err)
				}
				deg, min, sec := v/10000, (v%10000)/100, v%100
				if min >= 60 || sec >= 60 {
					panic(v)
				}
				return float32(deg) + float32(min)/60 + float32(sec)/3600
			}
			sp := &SignificantPoint{
				Name:        f[1],
				ShortName:   f[2],
				Description: f[3],
				Position:    [2]float32{pfloat(7), -pfloat(9)},
			}
			pts[n] = sp
		} else if len(f) == 10 {
			// second set of values
			if sp, ok := pts[n]; ok {
				sp.Abbreviation = f[7]
			}
		} else if len(f) == 9 {
			// third set
			if sp, ok := pts[n]; ok {
				sp.Lines[0] = f[2]
				sp.Lines[1] = f[3]
			}
		} else {
			fmt.Fprintf(os.Stderr, "Unexpected %d fields: %+v\n", f)
		}
	}

	keys := slices.Collect(maps.Keys(pts))
	slices.Sort(keys)
	var lines []string
	for _, idx := range keys {
		sp := pts[idx]
		var extras []string
		if sp.ShortName != "" {
			extras = append(extras, fmt.Sprintf(`"short_name": %q`, sp.ShortName))
		}
		if sp.Abbreviation != "" {
			extras = append(extras, fmt.Sprintf(`"abbreviation": %q`, sp.Abbreviation))
		}
		if sp.Description != "" {
			extras = append(extras, fmt.Sprintf(`"description": %q`, sp.Description))
		}
		extras = append(extras, fmt.Sprintf(`"location": "%f,%f"`, sp.Position[0], sp.Position[1]))
		lines = append(lines, fmt.Sprintf("  %q: { ", sp.Name)+fmt.Sprintf(strings.Join(extras, ", ")+"}"))
	}

	fmt.Printf("\"significant_points\": {\n")
	fmt.Println(strings.Join(lines, ",\n"))
	fmt.Printf("}\n")
}
