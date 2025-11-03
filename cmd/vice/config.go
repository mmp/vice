// cmd/vice/config.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/client"
	"github.com/mmp/vice/eram"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stars"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
)

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

	// Store individual pane instances instead of the entire display hierarchy
	STARSPane       *stars.STARSPane
	ERAMPane        *eram.ERAMPane
	MessagesPane    *panes.MessagesPane
	FlightStripPane *panes.FlightStripPane

	// Store split line positions for user adjustments
	SplitLinePositions [2]float32 // [0] = X split (0.8), [1] = Y split (0.075)

	// Keep DisplayRoot for backward compatibility during migration
	DisplayRoot *panes.DisplayNode

	TFRCache av.TFRCache

	AskedDiscordOptIn      bool
	InhibitDiscordActivity util.AtomicBool
	NotifiedTargetGenMode  bool
	DisableTextToSpeech    bool

	UserTCP string

	ScenarioFile string
	VideoMapFile string
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

func (c *Config) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "    ")
	return enc.Encode(c)
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

func (c *Config) SaveIfChanged(renderer renderer.Renderer, platform platform.Platform,
	client *client.ControlClient, saveSim bool, lg *log.Logger) bool {
	c.Sim = nil
	c.UserTCP = ""
	if saveSim {
		if sim, err := client.GetSerializeSim(); err != nil {
			lg.Errorf("%v", err)
		} else {
			c.Sim = sim
			c.UserTCP = client.State.UserTCP
		}
	}

	// Grab assorted things that may have changed during this session.
	c.ImGuiSettings = imgui.SaveIniSettingsToMemory()
	c.InitialWindowSize = platform.WindowSize()
	c.InitialWindowPosition = platform.WindowPosition()
	c.TFRCache.Sync(100*time.Millisecond, lg)

	// Capture current split line positions from the display hierarchy
	if c.DisplayRoot != nil {
		if c.DisplayRoot.SplitLine.Axis == panes.SplitAxisX {
			c.SplitLinePositions[0] = c.DisplayRoot.SplitLine.Pos
		}
		if c.DisplayRoot.Children[0] != nil && c.DisplayRoot.Children[0].SplitLine.Axis == panes.SplitAxisY {
			c.SplitLinePositions[1] = c.DisplayRoot.Children[0].SplitLine.Pos
		}
	}

	fn := configFilePath(lg)
	onDisk, err := os.ReadFile(fn)
	if err != nil {
		lg.Warnf("%s: unable to read config file: %v", fn, err)
	}

	var b strings.Builder
	if err = c.Encode(&b); err != nil {
		lg.Errorf("%s: unable to encode config: %v", fn, err)
		return false
	}

	if b.String() == string(onDisk) {
		return false
	}

	if err := c.Save(lg); err != nil {
		ShowErrorDialog(platform, lg, "Error saving configuration file: %v", err)
	}

	return true
}

func (c *Config) AllPanes() iter.Seq[panes.Pane] {
	p := []panes.Pane{c.FlightStripPane, c.MessagesPane, c.STARSPane, c.ERAMPane}
	return slices.Values(p)
}

func getDefaultConfig() *Config {
	return &Config{
		ConfigNoSim: ConfigNoSim{
			Config: platform.Config{
				InitialWindowPosition: [2]int{100, 100},
			},
			TFRCache:              av.MakeTFRCache(),
			Version:               server.ViceSerializeVersion,
			WhatsNewIndex:         len(whatsNew),
			NotifiedTargetGenMode: true, // don't warn for new installs
			STARSPane:             stars.NewSTARSPane(),
			ERAMPane:              eram.NewERAMPane(),
			MessagesPane:          panes.NewMessagesPane(),
			FlightStripPane:       panes.NewFlightStripPane(),
			SplitLinePositions:    [2]float32{0.8, 0.075}, // Default split positions
		},
	}
}

func LoadOrMakeDefaultConfig(lg *log.Logger) (config *Config, configErr error) {
	fn := configFilePath(lg)
	lg.Infof("Loading config from: %s", fn)

	config = getDefaultConfig()

	defer func() {
		if err := recover(); err != nil {
			configErr = fmt.Errorf("%v", err)
			lg.ReportCrash(err)
		}
	}()

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
			config.UserTCP = ""
		}
		if config.Version < 29 {
			config.TFRCache = av.MakeTFRCache()
		}

		// Migration: Extract pane instances and split positions from old DisplayRoot
		if config.Version < 44 && config.DisplayRoot != nil {
			config.migrateFromDisplayRoot(lg)
		}

		// Ensure all pane instances are initialized, even if migration didn't run
		if config.STARSPane == nil {
			config.STARSPane = stars.NewSTARSPane()
		}
		if config.ERAMPane == nil {
			config.ERAMPane = eram.NewERAMPane()
		}
		if config.MessagesPane == nil {
			config.MessagesPane = panes.NewMessagesPane()
		}
		if config.FlightStripPane == nil {
			config.FlightStripPane = panes.NewFlightStripPane()
		}
		// Initialize split positions if not set
		if config.SplitLinePositions[0] == 0 {
			config.SplitLinePositions[0] = 0.8
		}
		if config.SplitLinePositions[1] == 0 {
			config.SplitLinePositions[1] = 0.075
		}

		if config.Version < server.ViceSerializeVersion {
			if config.DisplayRoot != nil {
				config.DisplayRoot.VisitPanes(func(p panes.Pane) {
					if up, ok := p.(panes.PaneUpgrader); ok {
						up.Upgrade(config.Version, server.ViceSerializeVersion)
					}
				})
			}
			// Also upgrade the individual panes
			for pane := range config.AllPanes() {
				if up, ok := pane.(panes.PaneUpgrader); ok && pane != nil {
					up.Upgrade(config.Version, server.ViceSerializeVersion)
				}
			}
		}

		if config.Version == server.ViceSerializeVersion {
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
	config.Version = server.ViceSerializeVersion

	config.TFRCache.UpdateAsync(lg)

	imgui.LoadIniSettingsFromMemory(config.ImGuiSettings)

	return
}

// migrateFromDisplayRoot extracts pane instances and split positions from the old DisplayRoot structure
// and stores them in the new individual pane fields. This is used for backward compatibility.
func (c *Config) migrateFromDisplayRoot(lg *log.Logger) {
	if c.DisplayRoot == nil {
		return
	}

	// Extract split positions from the DisplayRoot hierarchy
	if c.DisplayRoot.SplitLine.Axis == panes.SplitAxisX {
		c.SplitLinePositions[0] = c.DisplayRoot.SplitLine.Pos
	}

	if c.DisplayRoot.Children[0] != nil && c.DisplayRoot.Children[0].SplitLine.Axis == panes.SplitAxisY {
		c.SplitLinePositions[1] = c.DisplayRoot.Children[0].SplitLine.Pos
	}

	// Extract pane instances from the DisplayRoot hierarchy
	c.DisplayRoot.VisitPanes(func(p panes.Pane) {
		switch pane := p.(type) {
		case *stars.STARSPane:
			if c.STARSPane == nil {
				c.STARSPane = pane
			}
		case *eram.ERAMPane:
			if c.ERAMPane == nil {
				c.ERAMPane = pane
			}
		case *panes.MessagesPane:
			if c.MessagesPane == nil {
				c.MessagesPane = pane
			}
		case *panes.FlightStripPane:
			if c.FlightStripPane == nil {
				c.FlightStripPane = pane
			}
		}
	})

	// Ensure we have all required panes
	if c.STARSPane == nil {
		c.STARSPane = stars.NewSTARSPane()
		lg.Infof("Created new STARSPane during migration")
	}
	if c.ERAMPane == nil {
		c.ERAMPane = eram.NewERAMPane()
		lg.Infof("Created new ERAMPane during migration")
	}
	if c.MessagesPane == nil {
		c.MessagesPane = panes.NewMessagesPane()
		lg.Infof("Created new MessagesPane during migration")
	}
	if c.FlightStripPane == nil {
		c.FlightStripPane = panes.NewFlightStripPane()
		lg.Infof("Created new FlightStripPane during migration")
	}

	c.DisplayRoot = nil

	lg.Infof("Migrated pane instances from DisplayRoot structure")
}

// buildDisplayRoot creates a new DisplayNode hierarchy from the stored pane instances
// and split line positions. This replaces the old approach of storing the entire hierarchy.
func (c *ConfigNoSim) buildDisplayRoot(radarPane panes.Pane) *panes.DisplayNode {
	return &panes.DisplayNode{
		SplitLine: panes.SplitLine{
			Pos:  c.SplitLinePositions[0], // X split
			Axis: panes.SplitAxisX,
		},
		Children: [2]*panes.DisplayNode{
			&panes.DisplayNode{
				SplitLine: panes.SplitLine{
					Pos:  c.SplitLinePositions[1], // Y split
					Axis: panes.SplitAxisY,
				},
				Children: [2]*panes.DisplayNode{
					&panes.DisplayNode{Pane: c.MessagesPane},
					&panes.DisplayNode{Pane: radarPane},
				},
			},
			&panes.DisplayNode{Pane: c.FlightStripPane},
		},
	}
}

func (c *Config) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
	// Prefer a robust signal of scenario type: STARS scenarios define
	// a PrimaryAirport in the sim state; ERAM scenarios do not.
	isSTARSSim := false
	switch {
	case c.Sim != nil:
		isSTARSSim = c.Sim.State.PrimaryAirport != ""
	case c.LastTRACON != "":
		_, isSTARSSim = av.DB.TRACONs[c.LastTRACON]
	default:
		isSTARSSim = true
	}

	// Use stored pane instances instead of creating new ones
	var radarPane panes.Pane
	if isSTARSSim {
		radarPane = c.STARSPane
	} else {
		radarPane = c.ERAMPane
	}

	// Build the display hierarchy from stored panes and split positions
	c.DisplayRoot = c.buildDisplayRoot(radarPane)

	panes.Activate(c.DisplayRoot, r, p, eventStream, lg)
}

// RebuildDisplayRootForSim rebuilds the display hierarchy with the appropriate radar pane
// based on the sim type (STARS vs ERAM). This is used when switching between scenarios.
func (c *Config) RebuildDisplayRootForSim(isSTARSSim bool) {
	var radarPane panes.Pane
	if isSTARSSim {
		radarPane = c.STARSPane
	} else {
		radarPane = c.ERAMPane
	}
	c.DisplayRoot = c.buildDisplayRoot(radarPane)
}
