// config.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

// Things that apply to all configs
type GlobalConfig struct {
	SectorFile   string
	PositionFile string

	PositionConfigs       map[string]*PositionConfig
	ActivePosition        string
	ColorSchemes          map[string]*ColorScheme
	InitialWindowSize     [2]int
	InitialWindowPosition [2]int
	ImGuiSettings         string
	AudioSettings         AudioSettings
	Notes                 map[NoteID]*Note
}

type PositionConfig struct {
	ColorSchemeName string
	ActiveAirports  map[string]interface{}
	DisplayRoot     *DisplayNode
	SplitLineWidth  int32

	todos  []ToDoReminderItem
	timers []TimerReminderItem

	mit              []*Aircraft
	selectedAircraft *Aircraft

	highlightedLocation        Point2LL
	highlightedLocationEndTime time.Time
}

func (c *GlobalConfig) DrawUI() {
	imgui.Text("Sector file: " + c.SectorFile)
	imgui.SameLine()
	if imgui.Button("New...##sectorfile") {
		ui.openSectorFileDialog.Activate()
	}
	imgui.Text("Position file: " + c.PositionFile)
	imgui.SameLine()
	if imgui.Button("New...##positionfile") {
		ui.openPositionFileDialog.Activate()
	}
	imgui.Separator()
	positionConfig.DrawUI()
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

func (gc *GlobalConfig) MakeConfigActive(name string) {
	if globalConfig.PositionConfigs == nil {
		globalConfig.PositionConfigs = make(map[string]*PositionConfig)
	}
	if len(globalConfig.PositionConfigs) == 0 {
		name = "Default"
		globalConfig.PositionConfigs["Default"] = NewPositionConfig()
	}

	oldConfig := positionConfig

	// NOTE: do not be clever and try to skip this work if
	// ActivePosition==name already; this function used e.g. when the color
	// scheme changes and we need to reset everything derived from that.
	gc.ActivePosition = name
	var ok bool
	if positionConfig, ok = gc.PositionConfigs[name]; !ok {
		lg.Errorf("%s: unknown position config!", name)
		return
	}

	cs := positionConfig.GetColorScheme()

	wmActivateNewConfig(oldConfig, positionConfig, cs)

	if cs.IsDark() {
		imgui.StyleColorsDark()
		style := imgui.CurrentStyle()
		darkGray := imgui.Vec4{.1, .1, .1, 1}
		style.SetColor(imgui.StyleColorWindowBg, darkGray)
		style.SetColor(imgui.StyleColorChildBg, darkGray)
		style.SetColor(imgui.StyleColorPopupBg, darkGray)
	} else {
		imgui.StyleColorsLight()
		style := imgui.CurrentStyle()
		lightGray := imgui.Vec4{.9, .9, .9, 1}
		style.SetColor(imgui.StyleColorWindowBg, lightGray)
		style.SetColor(imgui.StyleColorChildBg, lightGray)
		style.SetColor(imgui.StyleColorPopupBg, lightGray)
	}
	world.SetColorScheme(cs)
}

func (gc *GlobalConfig) PromptToSaveIfChanged(renderer Renderer, platform Platform) bool {
	fn := configFilePath()
	onDisk, err := os.ReadFile(fn)
	if err != nil {
		lg.Errorf("%s: unable to read config file: %v", fn, err)
		return false
	}

	var b strings.Builder
	if err = gc.Encode(&b); err != nil {
		lg.Errorf("%s: unable to encode config: %v", fn, err)
		return false
	}

	if b.String() == string(onDisk) {
		return false
	}

	ui.saveChangedDialog = NewModalDialogBox(&YesOrNoModalClient{
		title: "Save current configuration?",
		query: "Configuration has changed since the last time it was saved to disk.\nSave current configuration?",
		ok: func() {
			err := globalConfig.Save()
			if err != nil {
				ShowErrorDialog("Unable to save configuration file: %v", err)
			}
		}})
	ui.saveChangedDialog.Activate()
	return true
}

type NoteID int64

const InvalidNoteID = 0

func (g *GlobalConfig) NotesSortedByTitle() []*Note {
	var notes []*Note
	for _, n := range g.Notes {
		notes = append(notes, n)
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Title < notes[j].Title })
	return notes
}

type Note struct {
	ID       NoteID
	Title    string
	Contents string
}

func AddNewNote(title string) NoteID {
	id := NoteID(time.Now().UnixNano())
	note := Note{ID: id, Title: title}
	globalConfig.Notes[id] = &note
	return id
}

func (pc *PositionConfig) NotifyAircraftSelected(ac *Aircraft) {
	pc.DisplayRoot.VisitPanes(func(pane Pane) {
		if cli, ok := pane.(*CLIPane); ok {
			if !cli.ConsumeAircraftSelection(ac) {
				pc.selectedAircraft = ac
			}
		}
	})
}

func NewPositionConfig() *PositionConfig {
	c := &PositionConfig{}
	c.ActiveAirports = make(map[string]interface{})
	if world != nil && world.defaultAirport != "" {
		c.ActiveAirports[world.defaultAirport] = nil
	}
	c.DisplayRoot = &DisplayNode{Pane: NewRadarScopePane("Main Scope")}
	c.SplitLineWidth = 4
	c.ColorSchemeName = "Dark"
	return c
}

func (c *PositionConfig) IsActiveAirport(id string) bool {
	if c.ActiveAirports == nil {
		return false
	}

	_, ok := c.ActiveAirports[id]
	return ok
}

func (c *PositionConfig) GetColorScheme() *ColorScheme {
	if cs, ok := globalConfig.ColorSchemes[c.ColorSchemeName]; !ok {
		lg.Printf("%s: color scheme unknown", c.ColorSchemeName)
		cs = NewColorScheme()
		if globalConfig.ColorSchemes == nil {
			globalConfig.ColorSchemes = make(map[string]*ColorScheme)
		}
		globalConfig.ColorSchemes[c.ColorSchemeName] = cs
		return cs
	} else {
		return cs
	}
}

func (c *PositionConfig) DrawUI() {
	c.ActiveAirports = drawAirportSelector(c.ActiveAirports, "Active Airports")

	imgui.SliderInt("Split line width", &c.SplitLineWidth, 1, 10)
	if imgui.BeginCombo("Color scheme", c.ColorSchemeName) {
		names := SortedMapKeys(globalConfig.ColorSchemes)

		for _, name := range names {
			flags := imgui.SelectableFlagsNone
			if imgui.SelectableV(name, name == c.ColorSchemeName, flags, imgui.Vec2{}) &&
				name != c.ColorSchemeName {
				c.ColorSchemeName = name

				// This is slightly wasteful (e.g., resets the DrawList allocations),
				// but ensures that all of the panes get the new colors.
				globalConfig.MakeConfigActive(globalConfig.ActivePosition)
			}
		}
		imgui.EndCombo()
	}
}

func (c *PositionConfig) Duplicate() *PositionConfig {
	nc := &PositionConfig{}
	*nc = *c
	nc.DisplayRoot = c.DisplayRoot.Duplicate()
	nc.ActiveAirports = make(map[string]interface{})
	for ap := range c.ActiveAirports {
		nc.ActiveAirports[ap] = nil
	}
	// don't copy the todos or timers
	return nc
}

var (
	//go:embed resources/default-config.json
	defaultConfig string
)

func LoadOrMakeDefaultConfig() {
	fn := configFilePath()
	lg.Printf("Loading config from: %s", fn)

	config, err := os.ReadFile(fn)
	if err != nil {
		config = []byte(defaultConfig)
		if errors.Is(err, os.ErrNotExist) {
			lg.Printf("%s: config file doesn't exist", fn)
			_ = os.WriteFile(fn, config, 0o600)
		} else {
			lg.Printf("%s: unable to read config file: %v", fn, err)
			ShowErrorDialog("%s: unable to read config file: %v\nUsing default configuration.",
				fn, err)
			fn = "default.config"
		}
	}

	lg.Printf("Using config:\n%s", string(config))

	r := bytes.NewReader(config)
	d := json.NewDecoder(r)

	globalConfig = &GlobalConfig{}
	if err := d.Decode(globalConfig); err != nil {
		ShowErrorDialog("%s: configuration file is corrupt: %v", fn, err)
	}

	imgui.LoadIniSettingsFromMemory(globalConfig.ImGuiSettings)
}

func (pc *PositionConfig) Update(*WorldUpdates) {
	i := 0
	for i < len(pc.mit) {
		ac := pc.mit[i]
		if ac == nil {
			//lg.Printf("%s: lost a/c for mit. removing it.", pc.mit[i].Callsign())
			pc.mit = append(pc.mit[:i], pc.mit[i+1:]...)
		} else if ac.OnGround() || ac.Position().IsZero() {
			pc.mit = append(pc.mit[:i], pc.mit[i+1:]...)
		} else {
			// Only increment i if the aircraft survived
			i++
		}
	}
}
