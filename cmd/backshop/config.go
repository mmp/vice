// cmd/backshop/config.go
// Copyright(c) 2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/scenario"

	"github.com/AllenDang/cimgui-go/imgui"
)

// Config is what backshop remembers between runs: where its window was,
// what it was last looking at, and imgui's own window layout.
type Config struct {
	platform.Config

	ImGuiSettings string
	UIFontSize    int
	WhatsNewIndex int

	LastFacility string
	LastGroup    string
	LastScenario string

	// TTSVoice is the Kokoro voice the TTS tab speaks with.
	TTSVoice string

	ScenarioFile        string
	VideoMapFile        string
	ScenarioBriefFile   string
	FacilityConfigFiles []string

	// CRCDir is the CRC installation directory the Tools tab reads ARTCC
	// definitions from and MapPackDir is where it writes the video map
	// libraries it converts them to.
	CRCDir     string
	MapPackDir string
}

// overrideFiles returns the files the user has picked to replace or add to
// the ones in the resources directory.
func (c *Config) overrideFiles() scenario.OverrideFiles {
	return scenario.OverrideFiles{
		Scenario:        c.ScenarioFile,
		VideoMap:        c.VideoMapFile,
		ScenarioBrief:   c.ScenarioBriefFile,
		FacilityConfigs: c.FacilityConfigFiles,
	}
}

func configFilePath(lg *log.Logger) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		lg.Errorf("Unable to find user config dir: %v", err)
		dir = "."
	}

	dir = filepath.Join(dir, "Vice")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		lg.Errorf("%s: unable to make directory for config file: %v", dir, err)
	}

	return filepath.Join(dir, "backshop.json")
}

func defaultConfig() *Config {
	return &Config{
		Config: platform.Config{
			InitialWindowSize:     [2]int{1400, 900},
			InitialWindowPosition: [2]int{100, 100},
		},
		UIFontSize: 14,
		// Someone running backshop for the first time doesn't need to be
		// told what changed in past releases.
		WhatsNewIndex: len(whatsNew),
	}
}

// loadConfig reads the saved configuration, falling back to defaults for
// anything it can't read; a corrupt file is not worth failing to start over.
func loadConfig(lg *log.Logger) *Config {
	c := defaultConfig()

	fn := configFilePath(lg)
	contents, err := os.ReadFile(fn)
	if err != nil {
		return c
	}
	if err := json.NewDecoder(bytes.NewReader(contents)).Decode(c); err != nil {
		lg.Warnf("%s: discarding unreadable config: %v", fn, err)
		return defaultConfig()
	}
	if c.UIFontSize == 0 {
		c.UIFontSize = defaultConfig().UIFontSize
	}
	return c
}

func (c *Config) save(p platform.Platform, lg *log.Logger) {
	c.ImGuiSettings = imgui.SaveIniSettingsToMemory()
	c.InitialWindowSize = p.WindowSize()
	c.InitialWindowPosition = p.WindowPosition()

	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "    ")
	if err := enc.Encode(c); err != nil {
		lg.Errorf("unable to encode config: %v", err)
		return
	}

	fn := configFilePath(lg)
	if onDisk, err := os.ReadFile(fn); err == nil && b.String() == string(onDisk) {
		return
	}

	// Write to a temp file in the same directory and rename, so a crash
	// mid-write doesn't leave a corrupt config behind.
	tmp, err := os.CreateTemp(filepath.Dir(fn), "backshop-*.json.tmp")
	if err != nil {
		lg.Errorf("%v", err)
		return
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		lg.Errorf("%v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		lg.Errorf("%v", err)
		return
	}
	if err := os.Rename(tmp.Name(), fn); err != nil {
		lg.Errorf("%v", err)
	}
}
