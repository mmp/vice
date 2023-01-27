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
	SectorFile   string
	PositionFile string

	InitialWindowSize     [2]int
	InitialWindowPosition [2]int
	ImGuiSettings         string

	AudioSettings AudioSettings

	DisplayRoot *DisplayNode

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

func (c *GlobalConfig) GetColorScheme() *ColorScheme {
	return &ColorScheme{
		Text:                RGB{R: 0.85, G: 0.85, B: 0.85},
		TextHighlight:       RGBFromHex(0xB2B338),
		TextError:           RGBFromHex(0xE94242),
		TextDisabled:        RGB{R: 0, G: 0.25, B: 0.01483053},
		Background:          RGB{R: 0, G: 0, B: 0},
		AltBackground:       RGB{R: 0.09322035, G: 0.09322035, B: 0.09322035},
		UITitleBackground:   RGBFromHex(0x242435),
		UIControl:           RGB{R: 0.2754237, G: 0.2754237, B: 0.2754237},
		UIControlBackground: RGB{R: 0.063559294, G: 0.063559294, B: 0.063559294},
		UIControlSeparator:  RGB{R: 0, G: 0, B: 0},
		UIControlHovered:    RGB{R: 0.44915253, G: 0.44915253, B: 0.44915253},
		UIInputBackground:   RGB{R: 0.2881356, G: 0.2881356, B: 0.2881356},
		UIControlActive:     RGB{R: 0.5677966, G: 0.56539065, B: 0.56539065},
		Safe:                RGB{R: 0.13225771, G: 0.5635748, B: 0.8519856},
		Caution:             RGBFromHex(0xB7B513),
		Error:               RGBFromHex(0xE94242),
		SelectedDatablock:   RGB{R: 0.9133574, G: 0.9111314, B: 0.2967587},
		UntrackedDatablock:  RGBFromHex(0x8f92bc),
		TrackedDatablock:    RGB{R: 0.44499192, G: 0.9491525, B: 0.2573972},
		HandingOffDatablock: RGB{R: 0.7689531, G: 0.12214418, B: 0.26224726},
		GhostDatablock:      RGB{R: 0.5090253, G: 0.5090253, B: 0.5090253},
		Track:               RGB{R: 0, G: 1, B: 0.084745646},
		ArrivalStrip:        RGBFromHex(0x080724),
		DepartureStrip:      RGBFromHex(0x150707),
		Airport:             RGB{R: 0.46153843, G: 0.46153843, B: 0.46153843},
		VOR:                 RGB{R: 0.45819396, G: 0.45819396, B: 0.45819396},
		NDB:                 RGB{R: 0.44481605, G: 0.44481605, B: 0.44481605},
		Fix:                 RGB{R: 0.45819396, G: 0.45819396, B: 0.45819396},
		Runway:              RGB{R: 0.1864407, G: 0.3381213, B: 1},
		Region:              RGB{R: 0.63983047, G: 0.63983047, B: 0.63983047},
		SID:                 RGB{R: 0.29765886, G: 0.29765886, B: 0.29765886},
		STAR:                RGB{R: 0.26835144, G: 0.29237288, B: 0.18335249},
		Geo:                 RGB{R: 0.7923729, G: 0.7923729, B: 0.7923729},
		ARTCC:               RGB{R: 0.7, G: 0.7, B: 0.7},
		LowAirway:           RGB{R: 0.5, G: 0.5, B: 0.5},
		HighAirway:          RGB{R: 0.5, G: 0.5, B: 0.5},
		Compass:             RGB{R: 0.5270758, G: 0.5270758, B: 0.5270758},
		RangeRing:           RGBFromHex(0x282b1b),
	}
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

		globalConfig.AudioSettings.SoundEffects[AudioEventConflictAlert] = "Alert 2"
		globalConfig.AudioSettings.SoundEffects[AudioEventPointOut] = "Hint"
		globalConfig.AudioSettings.SoundEffects[AudioEventHandoffRequest] = "Beep Up"
		globalConfig.AudioSettings.SoundEffects[AudioEventHandoffRejected] = "Beep Negative"
		globalConfig.AudioSettings.SoundEffects[AudioEventHandoffAccepted] = "Blip"
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
		stars := NewSTARSPane()
		// hardcoded facility engineering here...
		stars.Facility.Center = Point2LL{-73.7765, 40.6401}
		stars.Facility.Airports = append(stars.Facility.Airports,
			STARSAirport{ICAOCode: "KJFK", Range: 50, IncludeInSSA: true, TowerListIndex: 1},
			STARSAirport{ICAOCode: "KFRG", Range: 30, IncludeInSSA: true, TowerListIndex: 2})

		addMap := func(name string, group int, sid string, star string) {
			m := MakeSTARSMap()
			m.Name = name
			m.Group = group
			if sid != "" {
				m.Draw.SIDDrawSet[sid] = nil
			}
			if star != "" {
				m.Draw.STARDrawSet[star] = nil
			}

			stars.Facility.Maps = append(stars.Facility.Maps, m)
			stars.currentPreferenceSet.MapVisible = append(stars.currentPreferenceSet.MapVisible, false)
		}
		addMap("JFK4", 0, "N90 JFK - 4s", "")
		addMap("JFK13", 0, "N90 JFK - 13s", "")
		addMap("JFK22", 0, "N90 JFK - 22s", "")
		addMap("JFK31", 0, "N90 JFK - 31s", "")
		addMap("NY B", 0, "", "New York Class B")
		addMap("MVA", 0, "", "N90 - MVA")

		addMap("JFK22 ILS", 0, "N90 JFK - ILS 22s", "")
		addMap("JFK31 NTZ", 0, "N90 JFK - 31s NTZ", "")

		addMap("LGA", 0, "N90 LGA - Video Map", "")
		addMap("LIB D", 0, "N90 LIB - Departure", "")
		addMap("LIB C", 0, "N90 LIB - Catskill", "")
		addMap("EWR", 0, "N90 EWR - Video Map", "")
		addMap("EWR SAT", 0, "N90 EWR - Satellite", "")
		addMap("EWR CRDA", 0, "N90 EWR - CRDA", "")
		addMap("ISP", 0, "N90 ISP - Video Map", "")

		stars.Facility.RadarSites = append(stars.Facility.RadarSites,
			STARSRadarSite{Char: "E",
				Id:             "EWR",
				Position:       "KEWR",
				Elevation:      136,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			})
		stars.Facility.RadarSites = append(stars.Facility.RadarSites,
			STARSRadarSite{Char: "J",
				Id:             "JFK",
				Position:       "KJFK",
				Elevation:      143,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			})
		stars.Facility.RadarSites = append(stars.Facility.RadarSites,
			STARSRadarSite{Char: "I",
				Id:             "ISP",
				Position:       "KISP",
				Elevation:      185,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			})
		stars.Facility.RadarSites = append(stars.Facility.RadarSites,
			STARSRadarSite{Char: "H",
				Id:             "HPN",
				Position:       "KHPN",
				Elevation:      708,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			})
		stars.Facility.RadarSites = append(stars.Facility.RadarSites,
			STARSRadarSite{Char: "S",
				Id:             "SWF",
				Position:       "KSWF",
				Elevation:      972,
				SlopeAngle:     0.175,
				PrimaryRange:   60,
				SecondaryRange: 120,
				SilenceAngle:   30,
			})

		fsp := NewFlightStripPane()
		fsp.Airports["KJFK"] = nil
		fsp.AutoAddDepartures = true
		fsp.AutoAddTracked = true
		fsp.AutoAddAcceptedHandoffs = true
		fsp.AutoRemoveDropped = true
		fsp.AutoRemoveHandoffs = true

		gc.DisplayRoot = &DisplayNode{
			SplitLine: SplitLine{
				Pos:  0.75,
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
