// cmd/vice/config.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/eram"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/stars"
	"github.com/mmp/vice/tts"
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

	// Store individual pane instances
	STARSPane       *stars.STARSPane
	ERAMPane        *eram.ERAMPane
	MessagesPane    *panes.MessagesPane
	FlightStripPane *panes.FlightStripPane

	// Whether the floating windows are visible
	ShowMessages     bool
	ShowFlightStrips bool

	AskedDiscordOptIn      bool
	InhibitDiscordActivity util.AtomicBool
	NotifiedTargetGenMode  bool
	DisableTextToSpeech    bool
	TTSPlaybackSpeed       float32
	EnableTowerGoArounds   bool

	UserWorkstation    string
	ControllerInitials string

	ScenarioFile      string
	VideoMapFile      string
	ScenarioBriefFile string

	NoBriefAtScenarioStart bool

	// Which child windows are open (persisted across sessions)
	ShowSettings     bool
	ShowLaunchCtrl   bool
	ShowScenarioInfo bool
	ShowKeyboardRef  bool

	// Set of child windows that have been unpinned (not always-on-top).
	// Windows absent from the set are pinned by default.
	UnpinnedWindows map[string]struct{}

	UserPTTKey         imgui.Key
	SelectedMicrophone string

	// Cached whisper model selection from benchmarking
	WhisperModelName      string  // Selected model filename (e.g., "ggml-small.en.bin")
	WhisperDeviceID       string  // Device identifier used for benchmarking
	WhisperBenchmarkIndex int     // Benchmark generation; rebenchmark if code's value is higher
	WhisperRealtimeFactor float64 // Ratio of transcription time to audio duration (for quality tuning)
	WhisperGPUDisabled    bool    // Sticky: GPU init failed on a prior run; stay on CPU
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
	fn := configFilePath(lg)
	lg.Infof("Saving config to: %s", fn)

	// Write to a temp file in the same directory, then rename to the
	// target path. This avoids corrupting the config file if we crash
	// mid-write.
	dir := filepath.Dir(fn)
	tmp, err := os.CreateTemp(dir, "config-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if err := c.Encode(tmp); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	return os.Rename(tmpName, fn)
}

func (c *Config) SaveIfChanged(renderer renderer.Renderer, platform platform.Platform,
	client *client.ControlClient, saveSim bool, lg *log.Logger) bool {
	c.Sim = nil
	c.UserWorkstation = ""
	if saveSim {
		if sim, err := client.GetSerializeSim(); err != nil {
			lg.Errorf("%v", err)
		} else {
			c.Sim = sim
			c.UserWorkstation = string(client.State.UserTCW)
		}
	}

	// Grab assorted things that may have changed during this session.
	c.ImGuiSettings = imgui.SaveIniSettingsToMemory()
	c.InitialWindowSize = platform.WindowSize()
	c.InitialWindowPosition = platform.WindowPosition()
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

// ActiveRadarPane returns the STARS or ERAM pane based on the sim type.
func (c *Config) ActiveRadarPane(isSTARSSim bool) panes.Pane {
	if isSTARSSim {
		return c.STARSPane
	}
	return c.ERAMPane
}

func getDefaultConfig() *Config {
	return &Config{
		ConfigNoSim: ConfigNoSim{
			Config: platform.Config{
				InitialWindowPosition: [2]int{100, 100},
			},
			Version:               server.ViceSerializeVersion,
			WhatsNewIndex:         len(whatsNew),
			NotifiedTargetGenMode: true, // don't warn for new installs
			UserPTTKey:            imgui.KeySemicolon,
			STARSPane:             stars.NewSTARSPane(),
			ERAMPane:              eram.NewERAMPane(),
			MessagesPane:          panes.NewMessagesPane(),
			FlightStripPane:       panes.NewFlightStripPane(),
			ShowMessages:          true,
			ShowFlightStrips:      true,
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
			config.Version = 1
		}
		if config.Version < 5 {
			config.UserWorkstation = ""
		}

		// Ensure all pane instances are initialized
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

		if config.Version < server.ViceSerializeVersion {
			// Upgrade panes
			for _, pane := range []any{config.STARSPane, config.ERAMPane} {
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

	if config.UnpinnedWindows == nil {
		config.UnpinnedWindows = make(map[string]struct{})
	}
	if config.UIFontSize == 0 {
		config.UIFontSize = 16
	}
	if config.TTSPlaybackSpeed == 0 {
		config.TTSPlaybackSpeed = tts.DefaultPlaybackSpeed
	}
	tts.SetPlaybackSpeed(config.TTSPlaybackSpeed)
	config.Version = server.ViceSerializeVersion

	imgui.LoadIniSettingsFromMemory(config.ImGuiSettings)

	return
}

func (c *Config) Activate(r renderer.Renderer, p platform.Platform, lg *log.Logger) {
	// Activate all panes
	c.STARSPane.Activate(r, p, lg)
	c.ERAMPane.Activate(r, p, lg)
	c.MessagesPane.Activate(r, p, lg)
	c.FlightStripPane.Activate(r, p, lg)
}
