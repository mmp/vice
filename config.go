// config.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/panes/stars"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
)

// Version history 0-7 not explicitly recorded
// 8: STARSPane DCB improvements, added DCB font size control
// 9: correct STARSColors, so update brightness settings to compensate
// 10: stop being clever about JSON encoding Waypoint arrays to strings
// 11: expedite, intercept localizer, fix airspace serialization
// 12: set 0 DCB brightness to 50 (WAR not setting a default for it)
// 13: update departure handling for multi-controllers (and rename some members)
// 14: Aircraft ArrivalHandoffController -> WaypointHandoffController
// 15: audio engine rewrite
// 16: cleared/assigned alt for departures, minor nav changes
// 17: weather intensity default bool
// 18: STARS ATPA
// 19: runway waypoints now per-airport
// 20: "stars_config" and various scenario fields moved there, plus STARSFacilityAdaptation
// 21: STARS DCB drawing changes, so system list positions changed
// 22: draw points using triangles, remove some CommandBuffer commands
// 23: video map format update
// 24: packages, audio to platform, flight plan processing
// 25: remove ArrivalGroup/Index from Aircraft
// 26: make allow_long_scratchpad a single bool
// 27: rework prefs, videomaps
// 28: new departure flow
// 29: TFR cache
// 30: video map improvements
const CurrentConfigVersion = 30

// Slightly convoluted, but the full Config definition is split into
// the part with the Sim and the rest of it.  In this way, we can first
// deserialize the non-Sim part and then only try to deserialize the Sim if
// its version matches CurrentConfigVersion.  This saves us from displaying
// errors about corrupt JSON in cases where fields in the Sim have changed
// (and we're going to throw it away anyway...)
type Config struct {
	ConfigNoSim
	ConfigSim
}

type ConfigNoSim struct {
	platform.Config

	Version       int
	ImGuiSettings string
	WhatsNewIndex int
	LastServer    string
	LastTRACON    string
	UIFontSize    int

	DisplayRoot *panes.DisplayNode

	TFRCache av.TFRCache

	AskedDiscordOptIn      bool
	InhibitDiscordActivity util.AtomicBool
	NotifiedTargetGenMode  bool

	Callsign string
}

type ConfigSim struct {
	Sim *sim.Sim
}

func configFilePath(lg *log.Logger) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		lg.Errorf("Unable to find user config dir: %v", err)
		dir = "."
	}

	dir = filepath.Join(dir, "Vice")
	err = os.MkdirAll(dir, 0o700)
	if err != nil {
		lg.Errorf("%s: unable to make directory for config file: %v", dir, err)
	}

	return filepath.Join(dir, "config.json")
}

func (gc *Config) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "    ")
	return enc.Encode(gc)
}

func (c *Config) Save(lg *log.Logger) error {
	lg.Infof("Saving config to: %s", configFilePath(lg))
	f, err := os.Create(configFilePath(lg))
	if err != nil {
		return err
	}
	defer f.Close()

	return c.Encode(f)
}

func (gc *Config) SaveIfChanged(renderer renderer.Renderer, platform platform.Platform,
	c *sim.ControlClient, saveSim bool, lg *log.Logger) bool {
	gc.Sim = nil
	gc.Callsign = ""
	if saveSim {
		if sim, err := c.GetSerializeSim(); err != nil {
			lg.Errorf("%v", err)
		} else {
			gc.Sim = sim
			gc.Callsign = c.Callsign
		}
	}

	// Grab assorted things that may have changed during this session.
	gc.ImGuiSettings = imgui.SaveIniSettingsToMemory()
	gc.InitialWindowSize = platform.WindowSize()
	gc.InitialWindowPosition = platform.WindowPosition()
	gc.TFRCache.Sync(100*time.Millisecond, lg)

	fn := configFilePath(lg)
	onDisk, err := os.ReadFile(fn)
	if err != nil {
		lg.Warnf("%s: unable to read config file: %v", fn, err)
	}

	var b strings.Builder
	if err = gc.Encode(&b); err != nil {
		lg.Errorf("%s: unable to encode config: %v", fn, err)
		return false
	}

	if b.String() == string(onDisk) {
		return false
	}

	if err := gc.Save(lg); err != nil {
		ShowErrorDialog(platform, lg, "Error saving configuration file: %v", err)
	}

	return true
}

func getDefaultConfig() *Config {
	return &Config{
		ConfigNoSim: ConfigNoSim{
			Config: platform.Config{
				AudioEnabled:          true,
				InitialWindowPosition: [2]int{100, 100},
			},
			TFRCache:              av.MakeTFRCache(),
			Version:               CurrentConfigVersion,
			WhatsNewIndex:         len(whatsNew),
			NotifiedTargetGenMode: true, // don't warn for new installs
		},
	}
}

func LoadOrMakeDefaultConfig(lg *log.Logger) (config *Config, configErr error) {
	fn := configFilePath(lg)
	lg.Infof("Loading config from: %s", fn)

	config = getDefaultConfig()

	if contents, err := os.ReadFile(fn); err == nil {
		r := bytes.NewReader(contents)
		d := json.NewDecoder(r)

		config = &Config{}
		if err := d.Decode(&config.ConfigNoSim); err != nil {
			configErr = err
			config = getDefaultConfig()
		}

		if config.Version < 1 {
			// Force upgrade via upcoming Activate() call...
			config.DisplayRoot = nil
		}
		if config.Version < 5 {
			config.Callsign = ""
		}
		if config.Version < 24 {
			config.AudioEnabled = true
		}
		if config.Version < 29 {
			config.TFRCache = av.MakeTFRCache()
		}

		if config.Version < CurrentConfigVersion {
			if config.DisplayRoot != nil {
				config.DisplayRoot.VisitPanes(func(p panes.Pane) {
					if up, ok := p.(panes.PaneUpgrader); ok {
						up.Upgrade(config.Version, CurrentConfigVersion)
					}
				})
			}
		}

		if config.Version == CurrentConfigVersion {
			// Go ahead and deserialize the Sim
			r.Seek(0, io.SeekStart)
			if err := d.Decode(&config.ConfigSim); err != nil {
				configErr = err
			}
		}
	}

	if config.UIFontSize == 0 {
		config.UIFontSize = 16
	}
	config.Version = CurrentConfigVersion

	config.TFRCache.UpdateAsync(lg)

	imgui.LoadIniSettingsFromMemory(config.ImGuiSettings)

	return
}

func (gc *Config) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
	if gc.DisplayRoot == nil {
		gc.DisplayRoot = panes.NewDisplayPanes(stars.NewSTARSPane(), panes.NewMessagesPane(),
			panes.NewFlightStripPane())
	}

	panes.Activate(gc.DisplayRoot, r, p, eventStream, lg)
}
