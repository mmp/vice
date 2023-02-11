// config.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

type GlobalConfig struct {
	InitialWindowSize     [2]int
	InitialWindowPosition [2]int
	ImGuiSettings         string

	Audio AudioSettings

	DisplayRoot *DisplayNode `json:"DisplayRoot-v2"` // force ignore earlier ones

	highlightedLocation        Point2LL
	highlightedLocationEndTime time.Time
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
	lg.Printf("Saving config to: %s", configFilePath())
	f, err := os.Create(configFilePath())
	if err != nil {
		return err
	}
	defer f.Close()

	return c.Encode(f)
}

func (gc *GlobalConfig) SaveIfChanged(renderer Renderer, platform Platform) bool {
	fn := configFilePath()
	onDisk, err := os.ReadFile(fn)
	if err != nil {
		lg.Printf("%s: unable to read config file: %v", fn, err)
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

func LoadOrMakeDefaultConfig() {
	fn := configFilePath()
	lg.Printf("Loading config from: %s", fn)

	globalConfig = &GlobalConfig{}
	config, err := os.ReadFile(fn)
	if err != nil {
		globalConfig.InitialWindowSize[0] = 1920
		globalConfig.InitialWindowSize[1] = 1080
		globalConfig.InitialWindowPosition[0] = 100
		globalConfig.InitialWindowPosition[1] = 100

		globalConfig.Audio.SoundEffects[AudioEventConflictAlert] = "Alert 2"
		globalConfig.Audio.SoundEffects[AudioEventInboundHandoff] = "Beep Up"
		globalConfig.Audio.SoundEffects[AudioEventHandoffAccepted] = "Blip"
		globalConfig.Audio.SoundEffects[AudioEventCommandError] = "Beep Negative"
	} else {
		r := bytes.NewReader(config)
		d := json.NewDecoder(r)

		if err := d.Decode(globalConfig); err != nil {
			ShowErrorDialog("Configuration file is corrupt: %v", err)
		}
	}

	imgui.LoadIniSettingsFromMemory(globalConfig.ImGuiSettings)
}

func (gc *GlobalConfig) Activate() {
	if gc.DisplayRoot == nil {
		stars := NewSTARSPane(tracon)

		fsp := NewFlightStripPane()
		fsp.Airports[tracon.PrimaryAirport] = nil
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
				&DisplayNode{Pane: stars},
				&DisplayNode{Pane: fsp},
			},
		}
	}

	gc.DisplayRoot.VisitPanes(func(p Pane) { p.Activate() })
}
