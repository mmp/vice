// cmd/stttest runs a single STT test case from a JSON file.
//
// Usage:
//
//	go run ./cmd/stttest path/to/test.json
//
// Exit code 0 on pass, 1 on fail.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stt"
)

// STTTestFile matches the structure in stt/provider_test.go
type STTTestFile struct {
	Transcript  string `json:"transcript"`
	Callsign    string `json:"callsign"`
	Command     string `json:"command"`
	STTAircraft map[string]struct {
		Callsign            string            `json:"Callsign"`
		AircraftType        string            `json:"AircraftType"`
		Fixes               map[string]string `json:"Fixes"`
		CandidateApproaches map[string]string `json:"CandidateApproaches"`
		AssignedApproach    string            `json:"AssignedApproach"`
		SID                 string            `json:"SID"`
		STAR                string            `json:"STAR"`
		Altitude            int               `json:"Altitude"`
		State               string            `json:"State"`
		ControllerFrequency string            `json:"ControllerFrequency"`
		TrackingController  string            `json:"TrackingController"`
		AddressingForm      int               `json:"AddressingForm"`
		LAHSORunways        []string          `json:"LAHSORunways"`
	} `json:"stt_aircraft"`
}

func main() {
	// Initialize the aviation database for aircraft performance lookups
	av.InitDB()

	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <test.json>\n", os.Args[0])
		os.Exit(1)
	}

	file := os.Args[1]
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		os.Exit(1)
	}

	var testFile STTTestFile
	if err := json.Unmarshal(data, &testFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing JSON: %v\n", err)
		os.Exit(1)
	}

	// Convert JSON aircraft to STT Aircraft map.
	// Bake /T into the callsign for type-based addressing entries,
	// mirroring the production context initialization in provider.go.
	aircraft := make(map[string]stt.Aircraft)
	for key, ac := range testFile.STTAircraft {
		callsign := ac.Callsign
		form := sim.CallsignAddressingForm(ac.AddressingForm)
		if form == sim.AddressingFormTypeTrailing3 && !strings.HasSuffix(callsign, "/T") {
			callsign += "/T"
		}
		aircraft[key] = stt.Aircraft{
			Callsign:            callsign,
			AircraftType:        ac.AircraftType,
			Fixes:               ac.Fixes,
			CandidateApproaches: ac.CandidateApproaches,
			AssignedApproach:    ac.AssignedApproach,
			SID:                 ac.SID,
			STAR:                ac.STAR,
			Altitude:            ac.Altitude,
			State:               ac.State,
			ControllerFrequency: ac.ControllerFrequency,
			TrackingController:  ac.TrackingController,
			AddressingForm:      form,
			LAHSORunways:        ac.LAHSORunways,
		}
	}

	// Run the transcript through STT
	provider := stt.NewTranscriber(nil)
	result, err := provider.DecodeTranscript(aircraft, testFile.Transcript, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "DecodeTranscript error: %v\n", err)
		os.Exit(1)
	}

	// Build expected output
	var expected string
	if testFile.Callsign == "" && testFile.Command == "" {
		expected = ""
	} else {
		expected = strings.TrimSpace(testFile.Callsign + " " + testFile.Command)
	}

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
