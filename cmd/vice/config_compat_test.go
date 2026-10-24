// cmd/vice/config_compat_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mmp/vice/nav"
)

// Settings saved when these windows were called panes must still be read.
func TestOldPaneSettingsStillRead(t *testing.T) {
	old := `{"Version":91,
	  "MessagesPane":{"AudioAlertSelection":"Radio Static","ContactTransmissionsAlert":true},
	  "FlightStripPane":{"FontSize":19,"DarkMode":true}}`
	var c Config
	if err := json.Unmarshal([]byte(old), &c); err != nil {
		t.Fatal(err)
	}
	if c.MessagesWindow == nil || c.MessagesWindow.AudioAlertSelection != "Radio Static" ||
		!c.MessagesWindow.ContactTransmissionsAlert {
		t.Errorf("messages window settings not read back: %+v", c.MessagesWindow)
	}
	if c.FlightStripWindow == nil || c.FlightStripWindow.FontSize != 19 || !c.FlightStripWindow.DarkMode {
		t.Errorf("flight strip settings not read back: %+v", c.FlightStripWindow)
	}
	b, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	json.Unmarshal(b, &back)
	if _, ok := back["MessagesPane"]; !ok {
		t.Error("written config no longer uses the MessagesPane name")
	}
	if _, ok := back["FlightStripPane"]; !ok {
		t.Error("written config no longer uses the FlightStripPane name")
	}
}

// Configs saved before custom pilot delays were stored start them at the
// brief ones, while saved delays are read back as they were, zeros included.
func TestCustomPilotDelaysRead(t *testing.T) {
	c, err := decodeConfig([]byte(`{"Version":104}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.PilotDelayChoice != pilotDelaysBrief || c.CustomPilotDelays != nav.BriefPilotDelays {
		t.Errorf("config without pilot delays got choice %d and custom delays %+v",
			c.PilotDelayChoice, c.CustomPilotDelays)
	}

	c.PilotDelayChoice = pilotDelaysCustom
	c.CustomPilotDelays = nav.PilotDelays{OnCourse: nav.DelayRange{Max: 5 * time.Second}}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.PilotDelayChoice != pilotDelaysCustom || back.CustomPilotDelays != c.CustomPilotDelays {
		t.Errorf("saved custom pilot delays %+v read back as choice %d with %+v",
			c.CustomPilotDelays, back.PilotDelayChoice, back.CustomPilotDelays)
	}
}
