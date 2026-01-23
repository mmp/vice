// cmd/sttsim calculates similarity metrics between two strings.
//
// Usage:
//
//	go run ./cmd/sttsim "localizer" "laser"
//	go run ./cmd/sttsim "heading" "descending"
//
// Shows Jaro-Winkler score, Double Metaphone encodings, PhoneticMatch result,
// and FuzzyMatch results at various thresholds.
package main

import (
	"fmt"
	"os"

	"github.com/mmp/vice/stt"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <word1> <word2>\n", os.Args[0])
		os.Exit(1)
	}

	w1, w2 := os.Args[1], os.Args[2]

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
