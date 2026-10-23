// cmd/vice/config.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/eram"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/speech/tts"
	"github.com/mmp/vice/stars"

	"github.com/AllenDang/cimgui-go/imgui"
)

// Config holds the user's settings and the sim that was running when vice
// last exited, if any. It is saved as JSON in config.json.
type Config struct {
	platform.Config

	Version       int
	ImGuiSettings string
	WhatsNewIndex int
	LastServer    string
	LastTRACON    string
	UIFontSize    int

	// Store individual scope and window instances. They give their JSON names
	// explicitly so that settings saved when they were called panes are still
	// read.
	STARSScope        *stars.Scope       `json:"STARSPane"`
	ERAMScope         *eram.Scope        `json:"ERAMPane"`
	MessagesWindow    *MessagesWindow    `json:"MessagesPane"`
	FlightStripWindow *FlightStripWindow `json:"FlightStripPane"`

	// Whether the floating windows are visible
	ShowMessages     bool
	ShowFlightStrips bool

	AskedDiscordOptIn      bool
	InhibitDiscordActivity bool
	NotifiedTargetGenMode  bool
	DisableTextToSpeech    bool
	TTSPlaybackSpeed       float32
	EnableTowerGoArounds   bool

	UserWorkstation    string
	ControllerInitials string

	ScenarioFile        string
	VideoMapFile        string
	ScenarioBriefFile   string
	FacilityConfigFiles []string

	// DisplaySimLogs enables logging of basic information about a Sim's execution (notably, things
	// likely to be useful for facility engineering) in the MessagesWindow.
	DisplaySimLogs bool

	PilotDelays nav.PilotDelays

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

	// Sim is decoded only by savedSim, after the version check on load. A
	// sim saved by another version of vice may no longer decode, and it is
	// discarded without being read.
	Sim json.RawMessage
}

// savedSim returns the simulation stored in the configuration file, or nil
// if there is none.
func (c *Config) savedSim() (*sim.Sim, error) {
	if len(c.Sim) == 0 {
		return nil, nil
	}

	var s *sim.Sim
	if err := json.Unmarshal(c.Sim, &s); err != nil {
		return nil, err
	}
	return s, nil
}

func configFilePath(lg *log.Logger) string {
	dir, err := log.ConfigDir()
	if err != nil {
		lg.Errorf("%v", err)
		dir = "."
	}

	err = os.MkdirAll(dir, 0o700)
	if err != nil {
		lg.Errorf("%s: unable to make directory for config file: %v", dir, err)
	}

	return filepath.Join(dir, "config.json")
}

// configBackupFilePath returns the path where Save keeps the previous
// version of the configuration file fn.
func configBackupFilePath(fn string) string {
	return fn + ".bak"
}

// Save writes the configuration file, keeping the previous one as a backup
// for LoadOrMakeDefaultConfig to fall back on.
func (c *Config) Save(lg *log.Logger) error {
	fn := configFilePath(lg)
	lg.Infof("Saving config to: %s", fn)

	b, err := json.Marshal(c)
	if err != nil {
		return err
	}

	// Only a config.json that loads may replace the backup.
	if old, err := os.ReadFile(fn); err == nil {
		if _, err := decodeConfig(old); err == nil {
			if err := writeFileAtomic(configBackupFilePath(fn), old); err != nil {
				lg.Warnf("%s: unable to back up config: %v", fn, err)
			}
		}
	}

	return writeFileAtomic(fn, b)
}

// writeFileAtomic writes b to fn by way of a temporary file in the same
// directory, so that fn holds either its previous contents or b even if
// vice or the OS crashes partway through.
func writeFileAtomic(fn string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(fn), filepath.Base(fn)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	// Without the sync, a power loss or OS crash soon after the rename can
	// leave an empty file: the rename may reach the disk before the file's
	// contents do.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, fn); err != nil {
		return err
	}

	// The rename also needs to reach disk. Windows does not support syncing
	// directory handles.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(fn))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// SaveAtExit records the window layout, imgui settings, and, if saveSim is
// set, the running sim, then saves the configuration. Vice exits right
// after, so a failure is shown in a dialog that the user dismisses before
// this returns.
func (c *Config) SaveAtExit(platform platform.Platform, client *client.ControlClient, saveSim bool,
	lg *log.Logger) {
	c.Sim = nil
	c.UserWorkstation = ""
	if saveSim {
		if simJSON, err := client.GetSerializeSimJSON(); err != nil {
			lg.Errorf("%v", err)
		} else {
			c.Sim = simJSON
			c.UserWorkstation = string(client.State.UserTCW)
		}
	}

	// Grab assorted things that may have changed during this session.
	c.ImGuiSettings = imgui.SaveIniSettingsToMemory()
	c.InitialWindowSize = platform.WindowSize()
	c.InitialWindowPosition = platform.WindowPosition()

	if err := c.Save(lg); err != nil {
		ShowBlockingErrorDialog(platform, lg, "Error saving configuration file: %v", err)
	}
}

// hasFacilityEngineeringFiles reports whether any override files are
// selected in the "Facility Engineering" section of the settings window.
func (c *Config) hasFacilityEngineeringFiles() bool {
	return c.ScenarioFile != "" || c.VideoMapFile != "" || c.ScenarioBriefFile != "" ||
		len(c.FacilityConfigFiles) > 0
}

// clearFacilityEngineeringFiles removes all of the override file selections.
func (c *Config) clearFacilityEngineeringFiles() {
	c.ScenarioFile = ""
	c.VideoMapFile = ""
	c.ScenarioBriefFile = ""
	c.FacilityConfigFiles = nil
}

// ActiveRadarScope returns the STARS or ERAM scope based on the sim type.
func (c *Config) ActiveRadarScope(isSTARSSim bool) scope.Scope {
	if isSTARSSim {
		return c.STARSScope
	}
	return c.ERAMScope
}

func getDefaultConfig() *Config {
	return &Config{
		Config: platform.Config{
			InitialWindowPosition: [2]int{100, 100},
		},
		Version:               server.ViceSerializeVersion,
		WhatsNewIndex:         len(whatsNew),
		NotifiedTargetGenMode: true, // don't warn for new installs
		UserPTTKey:            imgui.KeySemicolon,
		STARSScope:            stars.NewScope(),
		ERAMScope:             eram.NewScope(),
		MessagesWindow:        NewMessagesWindow(),
		FlightStripWindow:     NewFlightStripWindow(),
		ShowMessages:          true,
		ShowFlightStrips:      true,
		PilotDelays:           nav.DefaultPilotDelays,
	}
}

// readConfig reads and decodes the configuration file fn.
func readConfig(fn string) (*Config, error) {
	b, err := os.ReadFile(fn)
	if err != nil {
		return nil, err
	}
	return decodeConfig(b)
}

// decodeConfig decodes the contents of a configuration file.
func decodeConfig(b []byte) (*Config, error) {
	// A config saved before the pilot delays were stored gets the defaults.
	// Zero delays are valid, so they can't be filled in after decoding the
	// way zero values of other settings are.
	c := &Config{PilotDelays: nav.DefaultPilotDelays}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	return c, nil
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

	if c, err := readConfig(fn); err == nil {
		config = c
	} else if !errors.Is(err, fs.ErrNotExist) {
		bak := configBackupFilePath(fn)
		if c, bakErr := readConfig(bak); bakErr == nil {
			config = c
			configErr = fmt.Errorf("%w; restored the configuration from the previous save", err)
		} else {
			lg.Warnf("%s: %v", bak, bakErr)
			configErr = fmt.Errorf("%w; using the default configuration", err)
		}
	}

	if config.Version < 1 {
		config.Version = 1
	}
	if config.Version < 5 {
		config.UserWorkstation = ""
	}

	// Ensure all scope and window instances are initialized
	if config.STARSScope == nil {
		config.STARSScope = stars.NewScope()
	}
	if config.ERAMScope == nil {
		config.ERAMScope = eram.NewScope()
	}
	if config.MessagesWindow == nil {
		config.MessagesWindow = NewMessagesWindow()
	}
	if config.FlightStripWindow == nil {
		config.FlightStripWindow = NewFlightStripWindow()
	}

	if config.Version < server.ViceSerializeVersion {
		// Upgrade scopes
		for _, sc := range []any{config.STARSScope, config.ERAMScope} {
			if up, ok := sc.(scope.Upgrader); ok && sc != nil {
				up.Upgrade(config.Version, server.ViceSerializeVersion)
			}
		}
	}

	if config.Version != server.ViceSerializeVersion {
		config.Sim = nil
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
	// Activate all scopes and windows
	c.STARSScope.Activate(r, p, lg)
	c.ERAMScope.Activate(r, p, lg)
	c.MessagesWindow.Activate(r, p, lg)
	c.FlightStripWindow.Activate(r, p, lg)
}
