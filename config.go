// config.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path"
	"strings"
	"time"

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
// 24: flight plan processing
const CurrentConfigVersion = 24

// Slightly convoluted, but the full GlobalConfig definition is split into
// the part with the Sim and the rest of it.  In this way, we can first
// deserialize the non-Sim part and then only try to deserialize the Sim if
// its version matches CurrentConfigVersion.  This saves us from displaying
// errors about corrupt JSON in cases where fields in the Sim have changed
// (and we're going to throw it away anyway...)
type GlobalConfig struct {
	GlobalConfigNoSim
	GlobalConfigSim
}

type GlobalConfigNoSim struct {
	Version               int
	FullScreenMonitor     int
	InitialWindowSize     [2]int
	InitialWindowPosition [2]int
	ImGuiSettings         string
	WhatsNewIndex         int
	LastServer            string
	LastTRACON            string
	UIFontSize            int

	Audio AudioEngine

	DisplayRoot *DisplayNode

	AskedDiscordOptIn        bool
	InhibitDiscordActivity   AtomicBool
	NotifiedNewCommandSyntax bool
	StartInFullScreen        bool

	Callsign string

	highlightedLocation        Point2LL
	highlightedLocationEndTime time.Time
}

type GlobalConfigSim struct {
	Sim *Sim
}

func configFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		lg.Errorf("Unable to find user config dir: %v", err)
		dir = "."
	}

	dir = path.Join(dir, "Vice")
	err = os.MkdirAll(dir, 0o700)
	if err != nil {
		lg.Errorf("%s: unable to make directory for config file: %v", dir, err)
	}

	return path.Join(dir, "config.json")
}

func (gc *GlobalConfig) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "    ")
	return enc.Encode(gc)
}

func (c *GlobalConfig) Save() error {
	lg.Infof("Saving config to: %s", configFilePath())
	f, err := os.Create(configFilePath())
	if err != nil {
		return err
	}
	defer f.Close()

	return c.Encode(f)
}

func (gc *GlobalConfig) SaveIfChanged(renderer Renderer, platform Platform, w *World, saveSim bool) bool {
	gc.Sim = nil
	gc.Callsign = ""
	if saveSim {
		if sim, err := w.GetSerializeSim(); err != nil {
			lg.Errorf("%v", err)
		} else {
			sim.PreSave()
			gc.Sim = sim
			gc.Callsign = w.Callsign
		}
	}

	// Grab assorted things that may have changed during this session.
	gc.ImGuiSettings = imgui.SaveIniSettingsToMemory()
	gc.InitialWindowSize = platform.WindowSize()
	gc.InitialWindowPosition = platform.WindowPosition()

	fn := configFilePath()
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

	if err := globalConfig.Save(); err != nil {
		ShowErrorDialog("Error saving configuration file: %v", err)
	}

	return true
}

func SetDefaultConfig() {
	globalConfig = &GlobalConfig{}

	globalConfig.Audio.SetDefaults()
	globalConfig.Version = CurrentConfigVersion
	globalConfig.WhatsNewIndex = len(whatsNew)
	globalConfig.InitialWindowPosition = [2]int{100, 100}
	globalConfig.NotifiedNewCommandSyntax = true // don't warn for new installs
}

func LoadOrMakeDefaultConfig() {
	fn := configFilePath()
	lg.Infof("Loading config from: %s", fn)

	SetDefaultConfig()
	if config, err := os.ReadFile(fn); err == nil {
		r := bytes.NewReader(config)
		d := json.NewDecoder(r)

		globalConfig = &GlobalConfig{}
		if err := d.Decode(&globalConfig.GlobalConfigNoSim); err != nil {
			SetDefaultConfig()
			ShowErrorDialog("Configuration file is corrupt: %v", err)
		}

		if globalConfig.Version < 1 {
			// Force upgrade via upcoming Activate() call...
			globalConfig.DisplayRoot = nil
		}
		if globalConfig.Version < 5 {
			globalConfig.Callsign = ""
		}
		if globalConfig.Version < 15 && globalConfig.Audio.AudioEnabled {
			for i := 0; i < AudioNumTypes; i++ {
				globalConfig.Audio.EffectEnabled[i] = true
			}
		}

		if globalConfig.Version < CurrentConfigVersion {
			if globalConfig.DisplayRoot != nil {
				globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
					if up, ok := p.(PaneUpgrader); ok {
						up.Upgrade(globalConfig.Version, CurrentConfigVersion)
					}
				})
			}
		}

		if globalConfig.Version == CurrentConfigVersion {
			// Go ahead and deserialize the Sim
			r.Seek(0, io.SeekStart)
			if err := d.Decode(&globalConfig.GlobalConfigSim); err != nil {
				ShowErrorDialog("Configuration file is corrupt: %v", err)
			}
		}
	}

	if globalConfig.UIFontSize == 0 {
		globalConfig.UIFontSize = 16
	}
	globalConfig.Version = CurrentConfigVersion

	if err := globalConfig.Audio.Activate(); err != nil {
		lg.Errorf("Audio: %v", err)
	}

	imgui.LoadIniSettingsFromMemory(globalConfig.ImGuiSettings)
}

func (gc *GlobalConfig) Activate(w *World, r Renderer, eventStream *EventStream) {
	// Upgrade old ones without a MessagesPane
	if gc.DisplayRoot != nil {
		haveMessages := false
		gc.DisplayRoot.VisitPanes(func(p Pane) {
			if _, ok := p.(*MessagesPane); ok {
				haveMessages = true
			}
		})
		if !haveMessages {
			root := gc.DisplayRoot
			if root.SplitLine.Axis == SplitAxisX && root.Children[0] != nil {
				messages := NewMessagesPane()
				root.Children[0] = &DisplayNode{
					SplitLine: SplitLine{
						Pos:  0.075,
						Axis: SplitAxisY,
					},
					Children: [2]*DisplayNode{
						&DisplayNode{Pane: messages},
						&DisplayNode{Pane: root.Children[0].Pane},
					},
				}
			} else {
				gc.DisplayRoot = nil
			}
		}
	}

	if gc.DisplayRoot == nil {
		stars := NewSTARSPane(w)
		messages := NewMessagesPane()

		fsp := NewFlightStripPane()
		fsp.AutoAddDepartures = true
		fsp.AutoAddTracked = true
		fsp.AutoAddAcceptedHandoffs = true
		fsp.AutoRemoveDropped = true
		fsp.AutoRemoveHandoffs = true

		gc.DisplayRoot = &DisplayNode{
			SplitLine: SplitLine{
				Pos:  0.8,
				Axis: SplitAxisX,
			},
			Children: [2]*DisplayNode{
				&DisplayNode{
					SplitLine: SplitLine{
						Pos:  0.075,
						Axis: SplitAxisY,
					},
					Children: [2]*DisplayNode{
						&DisplayNode{Pane: messages},
						&DisplayNode{Pane: stars},
					},
				},
				&DisplayNode{Pane: fsp},
			},
		}
	}

	gc.DisplayRoot.VisitPanes(func(p Pane) { p.Activate(w, r, eventStream) })
}
