// server/stt.go
// Copyright(c) 2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	av "github.com/mmp/vice/aviation"
)

// STTAircraftContext maps spoken telephony names to aircraft information
// for STT processing.
type STTAircraftContext map[string]STTAircraft

// STTAircraft contains information about an aircraft for STT processing.
type STTAircraft struct {
	Callsign            av.ADSBCallsign   `json:"callsign"`
	Fixes               map[string]string `json:"fixes"`
	CandidateApproaches map[string]string `json:"candidate_approaches,omitempty"`
	AssignedApproach    string            `json:"assigned_approach"`
	Altitude            int               `json:"altitude"`
	State               string            `json:"state"`
}
