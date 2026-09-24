// aviation/eram.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"strings"

	"github.com/mmp/vice/util"
)

// ERAMEntries gives the data block entries a controller made before the
// simulation started; aircraft spawn with them already in their flight plans
// so that the data block reflects the handling the flight has had. They
// correspond to the ERAM QZ (assigned altitude), QQ (interim altitude), and
// QS (line 4 heading, speed, and free text) commands and are only displayed:
// none of them changes how the aircraft flies.
type ERAMEntries struct {
	AssignedAltitude int    `json:"assigned_altitude"`
	InterimAltitude  int    `json:"interim_altitude"`
	InterimType      string `json:"interim_type"`
	Heading          int    `json:"heading"`
	FreeText         string `json:"free_text"`
	Speed            int    `json:"speed"`
	Mach             int    `json:"mach"` // hundredths, so 78 is M.78
}

// eramInterimTypes maps the "interim_type" values to the character ERAM shows
// for each of them in the data block.
var eramInterimTypes = map[string]byte{"temp": 'T', "procedure": 'P', "local": 'L'}

// InterimTypeIndicator returns the data block character for the entries'
// interim altitude type.
func (ee ERAMEntries) InterimTypeIndicator() byte {
	if ch, ok := eramInterimTypes[ee.InterimType]; ok {
		return ch
	}
	return 'T'
}

// CheckInbound validates the entries of an arrival or overflight, which also
// specifies an assigned altitude and scratchpads that the data block would
// show in the same places as the entries.
func (ee *ERAMEntries) CheckInbound(assignedAltitude float32, scratchpad, secondaryScratchpad string,
	e *util.ErrorLogger) {
	ee.Check(e)

	if ee.AssignedAltitude != 0 && assignedAltitude != 0 {
		e.ErrorString(`"assigned_altitude" is already shown as the assigned altitude; ` +
			`it must not also be given under "eram"`)
	}
	if (ee.Heading != 0 || ee.FreeText != "") && scratchpad != "" {
		e.ErrorString(`"scratchpad" and the "eram" "heading"/"free_text" use the same data block field`)
	}
	if (ee.Speed != 0 || ee.Mach != 0) && secondaryScratchpad != "" {
		e.ErrorString(`"secondary_scratchpad" and the "eram" "speed"/"mach" use the same data block field`)
	}
}

// Check validates the entries, reporting any problems to e.
func (ee *ERAMEntries) Check(e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	e.Push(`"eram"`)
	defer e.Pop()

	checkAltitude := func(name string, alt int) {
		if alt == 0 {
			return
		}
		if alt < 1000 || alt > 60000 {
			e.ErrorString(`%q: altitude must be given in feet, between 1000 and 60000`, name)
		} else if alt%100 != 0 {
			e.ErrorString(`%q: altitude must be a multiple of 100 feet`, name)
		}
	}
	checkAltitude("assigned_altitude", ee.AssignedAltitude)
	checkAltitude("interim_altitude", ee.InterimAltitude)

	if _, ok := eramInterimTypes[ee.InterimType]; !ok && ee.InterimType != "" {
		e.ErrorString(`%s: unknown "interim_type" value. Options: %s.`, ee.InterimType,
			strings.Join(util.SortedMapKeys(eramInterimTypes), ", "))
	}
	if ee.InterimType != "" && ee.InterimAltitude == 0 {
		e.ErrorString(`"interim_type" given without an "interim_altitude"`)
	}

	if ee.Heading != 0 && ee.FreeText != "" {
		e.ErrorString(`cannot specify both "heading" and "free_text"; ERAM shows one or the other`)
	}
	if ee.Heading < 0 || ee.Heading > 360 {
		e.ErrorString(`"heading": must be between 1 and 360`)
	}
	if len(ee.FreeText) > 8 {
		e.ErrorString(`"free_text": must be at most 8 characters`)
	}
	const freeTextSymbols = "./*+-"
	if strings.ContainsFunc(ee.FreeText, func(ch rune) bool {
		return !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"+freeTextSymbols, ch)
	}) {
		e.ErrorString(`"free_text": must be A-Z, 0-9, or one of %s`, freeTextSymbols)
	}

	if ee.Speed != 0 && ee.Mach != 0 {
		e.ErrorString(`cannot specify both "speed" and "mach"`)
	}
	if ee.Speed != 0 && (ee.Speed < 100 || ee.Speed > 999) {
		e.ErrorString(`"speed": must be between 100 and 999 knots`)
	}
	if ee.Mach != 0 && (ee.Mach < 10 || ee.Mach > 99) {
		e.ErrorString(`"mach": must be given in hundredths, between 10 and 99`)
	}
}
