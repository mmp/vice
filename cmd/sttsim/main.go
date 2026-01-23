// cmd/sttsim provides STT debugging utilities.
//
// Usage:
//
//	# Compare string similarity
//	go run ./cmd/sttsim "localizer" "laser"
//	go run ./cmd/sttsim "heading" "descending"
//
//	# Test transcript normalization
//	go run ./cmd/sttsim -norm "five hundred"
//	go run ./cmd/sttsim -norm "two thousand five hundred"
//
// Similarity mode shows Jaro-Winkler score, Double Metaphone encodings,
// PhoneticMatch result, and FuzzyMatch results at various thresholds.
//
// Normalization mode shows how a transcript is normalized before parsing.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/mmp/vice/stt"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Check for -norm flag
	if os.Args[1] == "-norm" {
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s -norm <transcript>\n", os.Args[0])
			os.Exit(1)
		}
		transcript := strings.Join(os.Args[2:], " ")
		runNormalize(transcript)
		return
	}

	// Default: similarity comparison mode
	if len(os.Args) != 3 {
		printUsage()
		os.Exit(1)
	}

	w1, w2 := os.Args[1], os.Args[2]
	runSimilarity(w1, w2)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s <word1> <word2>       Compare string similarity\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -norm <transcript>   Test transcript normalization\n", os.Args[0])
}

func runNormalize(transcript string) {
	fmt.Printf("Input:  %q\n", transcript)
	result := stt.NormalizeTranscript(transcript)
	fmt.Printf("Output: %v\n", result)
	fmt.Printf("Joined: %q\n", strings.Join(result, " "))
}

func runSimilarity(w1, w2 string) {
	// Jaro-Winkler score
	jw := stt.JaroWinkler(w1, w2)
	fmt.Printf("Jaro-Winkler: %.4f\n", jw)

	// Double Metaphone encodings
	p1, a1 := stt.DoubleMetaphone(w1)
	p2, a2 := stt.DoubleMetaphone(w2)
	fmt.Printf("\nDouble Metaphone:\n")
	fmt.Printf("  %q: primary=%q alternate=%q\n", w1, p1, a1)
	fmt.Printf("  %q: primary=%q alternate=%q\n", w2, p2, a2)

	// PhoneticMatch
	pm := stt.PhoneticMatch(w1, w2)
	fmt.Printf("\nPhoneticMatch: %v\n", pm)

	// FuzzyMatch at various thresholds
	fmt.Printf("\nFuzzyMatch:\n")
	thresholds := []float64{0.70, 0.75, 0.80, 0.85, 0.90}
	for _, t := range thresholds {
		fm := stt.FuzzyMatch(w1, w2, t)
		fmt.Printf("  threshold %.2f: %v\n", t, fm)
	}
}
