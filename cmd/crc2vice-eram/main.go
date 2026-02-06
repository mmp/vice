// cmd/crc2vice-eram/main.go

package main

import (
	"encoding/gob"
	"encoding/json"
	"flag"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	log.Println("=== CRC ERAM Map Processor Starting ===")

	var inputARTCC string
	flag.StringVar(&inputARTCC, "artcc", "", "ARTCC to get files for")
	flag.Parse()

	if inputARTCC == "" {
		log.Fatal("Error: ARTCC parameter is required. Use -artcc flag to specify ARTCC (e.g., ZNY)")
	}

	log.Printf("Processing ARTCC: %s", inputARTCC)

	// Assume were in the CRC directory.
	currentDir, _ := os.Getwd()
	log.Printf("Current working directory: %s", currentDir)

	artccDir := filepath.Join(currentDir, "ARTCCs", inputARTCC+".json")

	log.Printf("ARTCC file path: %s", artccDir)
	file, err := os.Open(artccDir)
	if err != nil {
		log.Fatalf("Error opening ARTCC file: %v", err)
	}
	defer file.Close()

	log.Println("Reading and parsing ARTCC file...")
	artcc := ARTCC{}
	err = json.NewDecoder(file).Decode(&artcc)
	if err != nil {
		log.Fatalf("Error parsing ARTCC JSON file: %v", err)
	}

	log.Printf("Successfully loaded ARTCC: %s (ID: %s)", artcc.Facility.Name, artcc.Facility.ID)

	output := ERAMMapGroups{}

	log.Printf("Found %d geomaps in ERAM configuration", len(artcc.Facility.EramConfiguration.GeoMaps))

	for i, geoMap := range artcc.Facility.EramConfiguration.GeoMaps {
		log.Printf("Processing geomap %d/%d: %s (ID: %s)", i+1, len(artcc.Facility.EramConfiguration.GeoMaps), geoMap.Name, geoMap.ID)
		log.Printf("  - Label: %s / %s", geoMap.LabelLine1, geoMap.LabelLine2)
		log.Printf("  - Video map count: %d", len(geoMap.VideoMapIds))
		log.Printf("  - BCG menu items: %d", len(geoMap.BcgMenu))
		group := ERAMMapGroup{}
		for j, filterMenu := range geoMap.FilterMenu {

			log.Printf("  Processing filter menu %d/%d: %s %s", j+1, len(geoMap.FilterMenu), filterMenu.LabelLine1, filterMenu.LabelLine2)

			// Skip unnamed/blank filters
			if filterMenu.LabelLine1 == "" && filterMenu.LabelLine2 == "" {
				continue
			}

			// Aggregate lines across all video maps for this filter
			var aggregatedLines [][]Point2LL
			bcg := ""
			// Prefer BCG label aligned with the filter index; treat empty string as no BCG
			if j >= 0 && j < len(geoMap.BcgMenu) && geoMap.BcgMenu[j] != "" {
				bcg = geoMap.BcgMenu[j]
			}

			for _, videoMapID := range geoMap.VideoMapIds {
				// log.Printf("  Processing video map %d/%d: %s", i+1, len(geoMap.VideoMapIds), videoMapID)
				file, err := os.Open(currentDir + "/VideoMaps/" + inputARTCC + "/" + videoMapID + ".geojson")
				if err != nil {
					log.Fatalf("Error opening video map file %s: %v", videoMapID, err)
				}

				var gj GeoJSON
				err = json.NewDecoder(file).Decode(&gj)
				file.Close()
				if err != nil {
					log.Fatalf("Error decoding video map file %s: %v", videoMapID, err)
				}

				// Collect per-file defaults from special features
				lineDefaults := GeoJSONProperties{}
				for _, f := range gj.Features {
					if f.Properties == nil {
						continue
					}
					if f.Properties.IsLineDefaults {
						lineDefaults = *f.Properties
					}
					// Note: text/symbol defaults are not needed for line extraction
				}

				// Process features with fallback to defaults
				for _, feature := range gj.Features {
					if feature.Type != "Feature" {
						continue
					}
					// Skip defaults features themselves
					if feature.Properties != nil && (feature.Properties.IsLineDefaults || feature.Properties.IsTextDefaults || feature.Properties.IsSymbolDefaults) {
						continue
					}

					// Only extract lines for output
					if feature.Geometry.Type != "LineString" {
						// log.Printf("    Skipping non-LineString feature: %s. Current %v. Len %v", feature.Geometry.Type, k, len(gj.Features))
						continue
					}

					// Determine effective properties by applying defaults
					eff := GeoJSONProperties{}
					if feature.Properties != nil {
						eff = *feature.Properties
					}
					// Apply defaults where missing
					if eff.Bcg == 0 && lineDefaults.Bcg != 0 {
						eff.Bcg = lineDefaults.Bcg
					}
					if len(eff.Filters) == 0 && len(lineDefaults.Filters) != 0 {
						eff.Filters = append([]int(nil), lineDefaults.Filters...)
					}
					if eff.Style == "" && lineDefaults.Style != "" {
						eff.Style = lineDefaults.Style
					}
					if eff.Thickness == 0 && lineDefaults.Thickness != 0 {
						eff.Thickness = lineDefaults.Thickness
					}

					// Filter membership: CRC filters are 1-based; adjust for zero-based j
					if !slices.Contains(eff.Filters, j+1) {
						continue
					}

					// Append line(s), splitting into dash segments when style indicates dashed
					switch normalizeStyle(eff.Style) {
					case "shortdashed", "shortdash", "dashed":
						segments := buildDashedSegments(feature.Geometry.Coordinates, 1.0/60.0, 1.0/60.0)
						aggregatedLines = append(aggregatedLines, segments...)
					case "longdashed", "longdash":
						segments := buildDashedSegments(feature.Geometry.Coordinates, 2.0/60.0, 2.0/60.0)
						aggregatedLines = append(aggregatedLines, segments...)
					default:
						aggregatedLines = append(aggregatedLines, feature.Geometry.Coordinates)
					}

					if eff.Bcg-1 >= 0 && eff.Bcg-1 < len(geoMap.BcgMenu) {
						// Only use element BCG if no filter-index BCG was set
						if bcg == "" && geoMap.BcgMenu[eff.Bcg-1] != "" {
							bcg = geoMap.BcgMenu[eff.Bcg-1]
						}
					}
				}
			}

			// Only append a map entry if we found any lines for this filter
			if len(aggregatedLines) > 0 {
				group.Maps = append(group.Maps, ERAMMap{
					BcgName:    bcg,
					LabelLine1: filterMenu.LabelLine1,
					LabelLine2: filterMenu.LabelLine2,
					Name:       geoMap.Name,
					Lines:      aggregatedLines,
				})
			}

		}
		group.LabelLine1 = geoMap.LabelLine1
		group.LabelLine2 = geoMap.LabelLine2
		output[geoMap.Name] = group
	}

	// Write the output to a file
	log.Println("Preparing to write output file...")
	log.Printf("Output contains %d geomap groups", len(output))

	// Calculate some statistics
	totalMaps := 0
	totalLines := 0
	for groupName, group := range output {
		log.Printf("  %s: %d maps", groupName, len(group.Maps))
		totalMaps += len(group.Maps)
		for _, mapItem := range group.Maps {
			totalLines += len(mapItem.Lines)
		}
	}
	log.Printf("Total maps processed: %d", totalMaps)
	log.Printf("Total LineString features extracted: %d", totalLines)

	outputFile, err := os.Create(inputARTCC + "-eram-videomaps.json")
	if err != nil {
		log.Fatalf("Error creating output file: %v", err)
	}
	defer outputFile.Close()

	log.Println("Writing output to JSON file...")
	err = json.NewEncoder(outputFile).Encode(output)
	if err != nil {
		log.Fatalf("Error writing output file: %v", err)
	}

	log.Printf("✓ Output successfully written to %s", outputFile.Name())

	// Also write gob+zstd file of the JSON output
	fn := inputARTCC + "-eram-videomaps.gob"
	log.Printf("Writing compressed output to %s...", fn)
	f, err := os.Create(fn)
	if err != nil {
		log.Fatalf("Error creating file: %v", err)
	}
	defer f.Close()

	ge := gob.NewEncoder(f)
	if err := ge.Encode(output); err != nil {
		log.Fatalf("Error writing gob payload: %v", err)
	}

	fn = strings.Replace(fn, "videomaps", "manifest", 1)
	log.Printf("Writing manifest to %s...", fn)

	f, err = os.Create(fn)
	if err != nil {
		log.Fatalf("Error creating file: %v", err)
	}
	defer f.Close()

	combine := func(x, y string) string {
		x = strings.TrimSpace(x)
		y = strings.TrimSpace(y)

		if x == "" {
			return y
		}
		if y == "" {
			return x
		}

		// add space unless x already ends with space OR y already starts with space
		if strings.HasSuffix(x, " ") || strings.HasPrefix(y, " ") {
			return x + y
		}
		return x + " " + y
	}

	// Create output with only map names
	manifest := make(map[string]any) // MapGroup -> []MapNames
	for groupName, group := range output {
		for _, mapItem := range group.Maps {
			if _, ok := manifest[groupName]; !ok {
				manifest[groupName] = []string{}
			}
			manifest[groupName] = append(manifest[groupName].([]string), combine(mapItem.LabelLine1, mapItem.LabelLine2))
		}
	}

	fn = strings.Replace(fn, "gob", "json", 1)
	jf, err := os.Create(fn)
	if err != nil {
		log.Fatalf("Error creating file: %v", err)
	}
	defer jf.Close()

	je := json.NewEncoder(jf)
	if err := je.Encode(manifest); err != nil {
		log.Fatalf("Error writing json payload: %v", err)
	}

	ge = gob.NewEncoder(f)
	if err := ge.Encode(manifest); err != nil {
		log.Fatalf("Error writing gob payload: %v", err)
	}

	log.Println("=== CRC ERAM Map Processor Complete ===")
}

func normalizeStyle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "_", "")
	return s
}

func buildDashedSegments(coords []Point2LL, dashLenDeg float64, gapLenDeg float64) [][]Point2LL {
	if len(coords) < 2 || dashLenDeg <= 0 || gapLenDeg < 0 {
		return [][]Point2LL{coords}
	}
	var segments [][]Point2LL
	// State
	onDash := true
	remaining := dashLenDeg
	if !onDash {
		remaining = gapLenDeg
	}
	// Current segment points when in dash phase
	var cur []Point2LL

	// Helper to emit and reset the current dash segment
	emit := func() {
		if len(cur) >= 2 {
			// Copy to avoid aliasing
			seg := make([]Point2LL, len(cur))
			copy(seg, cur)
			segments = append(segments, seg)
		}
		cur = cur[:0]
	}

	// Iterate over each segment of the input polyline
	for i := 0; i < len(coords)-1; i++ {
		x1 := float64(coords[i][1])
		y1 := float64(coords[i][0])
		x2 := float64(coords[i+1][1])
		y2 := float64(coords[i+1][0])
		dx := x2 - x1
		dy := y2 - y1
		segLen := math.Hypot(dx, dy)
		if segLen == 0 {
			continue
		}
		// Unit direction
		ux := dx / segLen
		uy := dy / segLen

		// Current position along this segment
		cx := x1
		cy := y1

		// For dash segments, start with the starting point
		if onDash && len(cur) == 0 {
			cur = append(cur, Point2LL{float32(y1), float32(x1)})
		}

		remainingInThis := remaining
		traveled := 0.0
		for traveled < segLen {
			step := math.Min(remainingInThis, segLen-traveled)
			// Advance by step
			cx += ux * step
			cy += uy * step
			traveled += step

			if onDash {
				// Record the point in the dash
				cur = append(cur, Point2LL{float32(cy), float32(cx)})
			}

			remainingInThis -= step
			if remainingInThis <= 1e-9 {
				// Toggle phase and reset remaining for next phase
				onDash = !onDash
				if onDash {
					remainingInThis = dashLenDeg
					// Start a new dash from current point
					cur = append(cur, Point2LL{float32(cy), float32(cx)})
				} else {
					// Emit completed dash
					emit()
					remainingInThis = gapLenDeg
				}
			}
		}

		// Carry remaining into next input segment
		remaining = remainingInThis
		if onDash && len(cur) == 0 {
			// Ensure continuity of dash across vertices
			cur = append(cur, Point2LL{float32(cy), float32(cx)})
		}
	}

	// If we ended while on a dash, emit it
	if onDash {
		emit()
	}
	if len(segments) == 0 {
		return [][]Point2LL{coords}
	}
	return segments
}
