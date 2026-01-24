// cmd/updatesay/main.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/util"
)

var (
	dryRun = flag.Bool("dry-run", false, "Show what would be done and estimate cost, don't query Claude")
	sample = flag.Bool("sample", false, "Query Claude for only one chunk of each category to evaluate quality")
)

func main() {
	flag.Parse()

	apiKey := os.Getenv("VICE_ANTHROPIC_KEY")
	if apiKey == "" && !*dryRun {
		fmt.Fprintf(os.Stderr, "VICE_ANTHROPIC_KEY environment variable is required\n")
		os.Exit(1)
	}

	av.InitDB()

	var e util.ErrorLogger
	lg := log.New(false, "warn", "")
	scenarioGroups, _, _, _ := server.LoadScenarioGroups("", "", true /* skipVideoMaps */, &e, lg)
	if e.HaveErrors() {
		e.PrintErrors(lg)
		os.Exit(1)
	}

	// Extract all fixes/SIDs/STARs from loaded data structures
	fixes := make(map[string]struct{})
	sids := make(map[string]*ProcedureInfo)
	stars := make(map[string]*ProcedureInfo)

	// Extract from scenario groups
	for _, scenarios := range scenarioGroups {
		for _, sg := range scenarios {
			ExtractFromAirports(sg.Airports, fixes, sids)
			ExtractFromInboundFlows(sg.InboundFlows, fixes, stars)
			ExtractFromReportingPoints(sg.ReportingPoints, fixes)
			AddFixesFromMap(sg.Fixes, fixes)
		}
	}

	// Extract from aviation database
	ExtractFromDB(fixes, stars)

	// Load existing say files
	existingFixes, err := LoadSayFile("resources/sayfix.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading sayfix.json: %v\n", err)
		os.Exit(1)
	}

	existingSIDs, err := LoadSayFile("resources/saysid.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading saysid.json: %v\n", err)
		os.Exit(1)
	}

	existingSTARs, err := LoadSayFile("resources/saystar.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading saystar.json: %v\n", err)
		os.Exit(1)
	}

	// Split fixes: 3-letter VORs get names from database, 5-letter go to Claude
	vors, fiveLetterFixes := SplitFixesByLength(fixes)
	vorPronunciations := GetVORPronunciations(vors, existingFixes)
	missingFiveLetterFixes := FindMissing(fiveLetterFixes, existingFixes)

	// Split SIDs/STARs: 3-letter ones go to Opus with web search, longer ones go to Sonnet
	threeLetterSIDs, longerSIDs := SplitByLength(sids)
	threeLetterSTARs, longerSTARs := SplitByLength(stars)
	missingSIDs := FindMissingProcedures(longerSIDs, existingSIDs)
	missingSTARs := FindMissingProcedures(longerSTARs, existingSTARs)
	missingThreeLetterSIDs := FilterMissingProcedures(threeLetterSIDs, existingSIDs)
	missingThreeLetterSTARs := FilterMissingProcedures(threeLetterSTARs, existingSTARs)

	// Summary
	fmt.Printf("Found %d fixes (%d VORs, %d 5-letter), %d SIDs, %d STARs in scenarios\n",
		len(fixes), len(vors), len(fiveLetterFixes), len(sids), len(stars))
	fmt.Printf("VORs with names from database: %d\n", len(vorPronunciations))
	fmt.Printf("Missing 5-letter fixes for Sonnet: %d\n", len(missingFiveLetterFixes))
	fmt.Printf("Missing SIDs for Sonnet: %d\n", len(missingSIDs))
	fmt.Printf("Missing STARs for Sonnet: %d\n", len(missingSTARs))
	fmt.Printf("Missing 3-letter SIDs for Opus (web search): %d\n", len(missingThreeLetterSIDs))
	fmt.Printf("Missing 3-letter STARs for Opus (web search): %d\n", len(missingThreeLetterSTARs))

	if *dryRun {
		EstimateCost(missingFiveLetterFixes, missingSIDs, missingSTARs)
		// Show 3-letter items that will be queried via Opus
		printThreeLetterItems("3-letter SIDs for Opus", missingThreeLetterSIDs)
		printThreeLetterItems("3-letter STARs for Opus", missingThreeLetterSTARs)
		if len(missingFiveLetterFixes) > 0 {
			fmt.Printf("\nSample missing 5-letter fixes (up to 20): %v\n", sampleItems(missingFiveLetterFixes, 20))
		}
		if len(missingSIDs) > 0 {
			fmt.Printf("Sample missing SIDs (up to 20): %v\n", sampleItems(missingSIDs, 20))
		}
		if len(missingSTARs) > 0 {
			fmt.Printf("Sample missing STARs (up to 20): %v\n", sampleItems(missingSTARs, 20))
		}
		return
	}

	if *sample {
		// Sample mode: query one chunk of each and print results (don't save)
		runSample(apiKey, missingFiveLetterFixes, missingSIDs, missingSTARs, missingThreeLetterSIDs, missingThreeLetterSTARs)
		return
	}

	// Full run: add VOR names from database and query Claude for the rest

	// Add VOR pronunciations from database (save immediately)
	if len(vorPronunciations) > 0 {
		merged, count := MergeSayData(existingFixes, vorPronunciations)
		existingFixes = merged
		if err := SaveSayFile("resources/sayfix.json", existingFixes); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving sayfix.json: %v\n", err)
		} else {
			fmt.Printf("Added %d VOR pronunciations from database\n", count)
		}
	}

	// Query Sonnet for 5-letter fixes (saves after each chunk)
	if len(missingFiveLetterFixes) > 0 {
		fmt.Printf("Querying Sonnet for %d fix pronunciations...\n", len(missingFiveLetterFixes))
		QueryAndSave(apiKey, missingFiveLetterFixes, "fixes", existingFixes, "resources/sayfix.json")
	}

	// Query Sonnet for longer SIDs (saves after each chunk)
	if len(missingSIDs) > 0 {
		fmt.Printf("Querying Sonnet for %d SID pronunciations...\n", len(missingSIDs))
		existingSIDs, _ = QueryAndSave(apiKey, missingSIDs, "SIDs (Standard Instrument Departures)", existingSIDs, "resources/saysid.json")
	}

	// Query Opus for 3-letter SIDs with web search (saves after each chunk)
	if len(missingThreeLetterSIDs) > 0 {
		fmt.Printf("Querying Opus for %d 3-letter SID names (with web search)...\n", len(missingThreeLetterSIDs))
		QueryOpusAndSave(apiKey, missingThreeLetterSIDs, "SID", existingSIDs, "resources/saysid.json")
	}

	// Query Sonnet for longer STARs (saves after each chunk)
	if len(missingSTARs) > 0 {
		fmt.Printf("Querying Sonnet for %d STAR pronunciations...\n", len(missingSTARs))
		existingSTARs, _ = QueryAndSave(apiKey, missingSTARs, "STARs (Standard Terminal Arrival Routes)", existingSTARs, "resources/saystar.json")
	}

	// Query Opus for 3-letter STARs with web search (saves after each chunk)
	if len(missingThreeLetterSTARs) > 0 {
		fmt.Printf("Querying Opus for %d 3-letter STAR names (with web search)...\n", len(missingThreeLetterSTARs))
		QueryOpusAndSave(apiKey, missingThreeLetterSTARs, "STAR", existingSTARs, "resources/saystar.json")
	}

	fmt.Println("\nDone. Run 'prettier --parser json --print-width 120 --write resources/say*.json' to format output.")
}

func sampleItems(items []string, max int) []string {
	if len(items) <= max {
		return items
	}
	return items[:max]
}

func runSample(apiKey string, missingFixes, missingSIDs, missingSTARs []string, missingThreeLetterSIDs, missingThreeLetterSTARs map[string]*ProcedureInfo) {
	fmt.Println("\n=== SAMPLE MODE: Querying one chunk of each category ===")

	if len(missingFixes) > 0 {
		chunk := sampleItems(missingFixes, chunkSize)
		fmt.Printf("\nQuerying %d sample 5-letter fixes (Sonnet)...\n", len(chunk))
		results, err := QuerySampleChunk(apiKey, chunk, "fixes")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Println("\nFix pronunciations:")
			printResults(results)
		}
	}

	if len(missingSIDs) > 0 {
		chunk := sampleItems(missingSIDs, chunkSize)
		fmt.Printf("\nQuerying %d sample SIDs (Sonnet)...\n", len(chunk))
		results, err := QuerySampleChunk(apiKey, chunk, "SIDs (Standard Instrument Departures)")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Println("\nSID pronunciations:")
			printResults(results)
		}
	}

	if len(missingSTARs) > 0 {
		chunk := sampleItems(missingSTARs, chunkSize)
		fmt.Printf("\nQuerying %d sample STARs (Sonnet)...\n", len(chunk))
		results, err := QuerySampleChunk(apiKey, chunk, "STARs (Standard Terminal Arrival Routes)")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Println("\nSTAR pronunciations:")
			printResults(results)
		}
	}

	// Sample 3-letter SIDs with Opus
	if len(missingThreeLetterSIDs) > 0 {
		fmt.Printf("\nQuerying sample 3-letter SIDs (Opus with web search)...\n")
		results, err := QueryOpusSample(apiKey, missingThreeLetterSIDs, "SID")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Println("\n3-letter SID names:")
			printResults(results)
		}
	}

	// Sample 3-letter STARs with Opus
	if len(missingThreeLetterSTARs) > 0 {
		fmt.Printf("\nQuerying sample 3-letter STARs (Opus with web search)...\n")
		results, err := QueryOpusSample(apiKey, missingThreeLetterSTARs, "STAR")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		} else {
			fmt.Println("\n3-letter STAR names:")
			printResults(results)
		}
	}

	fmt.Println("\n=== Sample complete. Review results above before running full update. ===")
}

func printResults(results map[string]string) {
	keys := slices.Sorted(maps.Keys(results))
	for _, k := range keys {
		fmt.Printf("  %s -> %s\n", k, results[k])
	}
}

func printThreeLetterItems(label string, items map[string]*ProcedureInfo) {
	if len(items) == 0 {
		return
	}

	names := slices.Sorted(maps.Keys(items))
	fmt.Printf("\n%s:\n", label)
	for _, name := range names {
		info := items[name]
		airportList := slices.Sorted(maps.Keys(info.Airports))
		fullNames := slices.Sorted(maps.Keys(info.FullNames))
		fmt.Printf("  %s: airports=%s, variants=%s\n", name, strings.Join(airportList, ","), strings.Join(fullNames, ","))
	}
}

// FindMissingProcedures returns missing base names from procedures.
func FindMissingProcedures(items map[string]*ProcedureInfo, existing map[string]string) []string {
	var missing []string
	for name := range items {
		if _, exists := existing[name]; !exists {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// FilterMissingProcedures returns only the procedures that are missing from existing.
func FilterMissingProcedures(items map[string]*ProcedureInfo, existing map[string]string) map[string]*ProcedureInfo {
	result := make(map[string]*ProcedureInfo)
	for name, info := range items {
		if _, exists := existing[name]; !exists {
			result[name] = info
		}
	}
	return result
}
