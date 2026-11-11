// cmd/vice/config_compat_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/server"

	"github.com/klauspost/compress/zstd"
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

// A config saved when it held the running sim still loads.
func TestConfigWithSimStillRead(t *testing.T) {
	c, err := decodeConfig([]byte(`{"Version":107,"ControllerInitials":"XX","Sim":{"TotalIFR":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.ControllerInitials != "XX" {
		t.Errorf("got initials %q, want XX", c.ControllerInitials)
	}
}

func TestSavedSimRoundTrip(t *testing.T) {
	fn := filepath.Join(t.TempDir(), "sim.json.zst")

	if s, err := readSavedSim(fn); s != nil || err != nil {
		t.Fatalf("missing file read as %v, %v", s, err)
	}

	if err := writeSavedSim(fn, []byte(`{"TotalIFR":3}`)); err != nil {
		t.Fatal(err)
	}
	s, err := readSavedSim(fn)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.TotalIFR != 3 {
		t.Errorf("saved sim read back as %+v", s)
	}

	if err := writeSavedSim(fn, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fn); !os.IsNotExist(err) {
		t.Errorf("writing no sim left the file: %v", err)
	}
}

func TestSavedSimFromOtherVersionDiscarded(t *testing.T) {
	fn := filepath.Join(t.TempDir(), "sim.json.zst")
	b, err := json.Marshal(savedSim{Version: server.ViceSerializeVersion - 1, Sim: []byte(`{"TotalIFR":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer zw.Close()
	if err := os.WriteFile(fn, zw.EncodeAll(b, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := readSavedSim(fn); s != nil || err != nil {
		t.Errorf("sim from another version read as %+v, %v", s, err)
	}
}
