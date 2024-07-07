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

	"github.com/mmp/imgui-go/v4"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
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
// 24: packages, audio to platform
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
	platform.Config

	Version       int
	ImGuiSettings string
	WhatsNewIndex int
	LastServer    string
	LastTRACON    string
	UIFontSize    int

	DisplayRoot *DisplayNode

	AskedDiscordOptIn        bool
	InhibitDiscordActivity   util.AtomicBool
	NotifiedNewCommandSyntax bool

	Callsign string
}

type GlobalConfigSim struct {
	Sim *sim.Sim
}

func configFilePath(lg *log.Logger) string {
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

func (c *GlobalConfig) Save(lg *log.Logger) error {
	lg.Infof("Saving config to: %s", configFilePath(lg))
	f, err := os.Create(configFilePath(lg))
	if err != nil {
		return err
	}
	defer f.Close()

	return c.Encode(f)
}

func (gc *GlobalConfig) SaveIfChanged(renderer renderer.Renderer, platform platform.Platform,
	c *sim.ControlClient, saveSim bool, lg *log.Logger) bool {
	gc.Sim = nil
	gc.Callsign = ""
	if saveSim {
		if sim, err := c.GetSerializeSim(); err != nil {
			lg.Errorf("%v", err)
		} else {
			sim.PreSave()
			gc.Sim = sim
			gc.Callsign = c.Callsign
		}
	}

	// Grab assorted things that may have changed during this session.
	gc.ImGuiSettings = imgui.SaveIniSettingsToMemory()
	gc.InitialWindowSize = platform.WindowSize()
	gc.InitialWindowPosition = platform.WindowPosition()

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

	if err := globalConfig.Save(lg); err != nil {
		ShowErrorDialog(platform, lg, "Error saving configuration file: %v", err)
	}

	return true
}

func SetDefaultConfig() {
	globalConfig = &GlobalConfig{}

	globalConfig.AudioEnabled = true
	globalConfig.Version = CurrentConfigVersion
	globalConfig.WhatsNewIndex = len(whatsNew)
	globalConfig.InitialWindowPosition = [2]int{100, 100}
	globalConfig.NotifiedNewCommandSyntax = true // don't warn for new installs
}

func LoadOrMakeDefaultConfig(p platform.Platform, lg *log.Logger) {
	fn := configFilePath(lg)
	lg.Infof("Loading config from: %s", fn)

	SetDefaultConfig()
	if config, err := os.ReadFile(fn); err == nil {
		r := bytes.NewReader(config)
		d := json.NewDecoder(r)

		globalConfig = &GlobalConfig{}
		if err := d.Decode(&globalConfig.GlobalConfigNoSim); err != nil {
			SetDefaultConfig()
			ShowErrorDialog(p, lg, "Configuration file is corrupt: %v", err)
		}

		if globalConfig.Version < 1 {
			// Force upgrade via upcoming Activate() call...
			globalConfig.DisplayRoot = nil
		}
		if globalConfig.Version < 5 {
			globalConfig.Callsign = ""
		}
		if globalConfig.Version < 24 {
			globalConfig.AudioEnabled = true
		}

		if globalConfig.Version < CurrentConfigVersion {
			if globalConfig.DisplayRoot != nil {
				globalConfig.DisplayRoot.VisitPanes(func(p panes.Pane) {
					if up, ok := p.(panes.PaneUpgrader); ok {
						up.Upgrade(globalConfig.Version, CurrentConfigVersion)
					}
				})
			}
		}

		if globalConfig.Version == CurrentConfigVersion {
			// Go ahead and deserialize the Sim
			r.Seek(0, io.SeekStart)
			if err := d.Decode(&globalConfig.GlobalConfigSim); err != nil {
				ShowErrorDialog(p, lg, "Configuration file is corrupt: %v", err)
			}
		}
	}

	if globalConfig.UIFontSize == 0 {
		globalConfig.UIFontSize = 16
	}
	globalConfig.Version = CurrentConfigVersion

	imgui.LoadIniSettingsFromMemory(globalConfig.ImGuiSettings)
}

func (gc *GlobalConfig) Activate(c *sim.ControlClient, r renderer.Renderer, p platform.Platform,
	eventStream *sim.EventStream, lg *log.Logger) {
	// Upgrade old ones without a MessagesPane
	if gc.DisplayRoot != nil {
		haveMessages := false
		gc.DisplayRoot.VisitPanes(func(p panes.Pane) {
			if _, ok := p.(*panes.MessagesPane); ok {
				haveMessages = true
			}
		})
		if !haveMessages {
			root := gc.DisplayRoot
			if root.SplitLine.Axis == SplitAxisX && root.Children[0] != nil {
				messages := panes.NewMessagesPane()
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
		stars := panes.NewSTARSPane(c.State)
		messages := panes.NewMessagesPane()

		fsp := panes.NewFlightStripPane()
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

	gc.DisplayRoot.VisitPanes(func(pane panes.Pane) {
		if c != nil {
			pane.Activate(&c.State, r, p, eventStream, lg)
		} else {
			pane.Activate(nil, r, p, eventStream, lg)
		}
	})
}
