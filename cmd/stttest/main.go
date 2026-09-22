// cmd/stttest/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// cmd/stttest runs a single STT test case from a JSON file.
//
// Usage:
//
//	go run ./cmd/stttest path/to/test.json
//
// Exit code 0 on pass, 1 on fail.
package main

import (
	"fmt"
	"os"

	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/speech/stt"
)

func main() {
	// Initialize the aviation database for aircraft performance lookups
	db.InitDB()

	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <test.json>\n", os.Args[0])
		os.Exit(1)
	}

	file := os.Args[1]
	testFile, err := stt.LoadTestFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading test file: %v\n", err)
		os.Exit(1)
	}

	aircraft := testFile.BuildAircraftMap()

	// Run the transcript through STT
	provider := stt.NewTranscriber(nil)
	result, err := provider.DecodeTranscript(aircraft, testFile.Transcript, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "DecodeTranscript error: %v\n", err)
		os.Exit(1)
	}

	expected := testFile.Expected()

	// Output results
	fmt.Printf("File:       %s\n", file)
	fmt.Printf("Transcript: %s\n", testFile.Transcript)
	fmt.Printf("Expected:   %q\n", expected)
	fmt.Printf("Actual:     %q\n", result)

	if stt.CommandsEquivalent(expected, result, aircraft) {
		fmt.Println("\nPASS")
		os.Exit(0)
	} else {
		fmt.Println("\nFAIL")
		os.Exit(1)
	}
}
