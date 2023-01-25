// config.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

// Things that apply to all configs
type GlobalConfig struct {
	SectorFile   string
	PositionFile string
	NotesFile    string
	AliasesFile  string

	VatsimName     string
	VatsimCID      string
	VatsimPassword string
	VatsimRating   NetworkRating
	CustomServers  map[string]string
	LastServer     string

	PositionConfigs       map[string]*PositionConfig
	ActivePosition        string
	ColorSchemes          map[string]*ColorScheme
	InitialWindowSize     [2]int
	InitialWindowPosition [2]int
	ImGuiSettings         string
	AudioSettings         AudioSettings

	aliases map[string]string
}

type PositionConfig struct {
	ColorSchemeName string
	DisplayRoot     *DisplayNode

	VatsimCallsign                string
	PrimaryRadarCenter            string
	primaryRadarCenterLocation    Point2LL
	SecondaryRadarCenters         [3]string
	secondaryRadarCentersLocation [3]Point2LL
	RadarRange                    int32
	primaryFrequency              Frequency // We don't save this in the config file
	Frequencies                   map[string]Frequency

	selectedAircraft *Aircraft

	highlightedLocation        Point2LL
	highlightedLocationEndTime time.Time
	drawnRoute                 string
	drawnRouteEndTime          time.Time
	sessionDrawVORs            map[string]interface{}
	sessionDrawNDBs            map[string]interface{}
	sessionDrawFixes           map[string]interface{}
	sessionDrawAirports        map[string]interface{}

	frequenciesComboBoxState     *ComboBoxState
	txFrequencies, rxFrequencies map[Frequency]*bool

	eventsId EventSubscriberId
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

	positionConfig.Activate()
	if oldConfig != nil && oldConfig != positionConfig {
		oldConfig.Deactivate()
	}

	wmActivateNewConfig(oldConfig, positionConfig)

	cs := positionConfig.GetColorScheme()

	uiUpdateColorScheme(cs)
	database.SetColorScheme(cs)
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

	uiShowModalDialog(NewModalDialogBox(&YesOrNoModalClient{
		title: "Save current configuration?",
		query: "Configuration has changed since the last time it was saved to disk.\nSave current configuration?",
		ok: func() {
			err := globalConfig.Save()
			if err != nil {
				ShowErrorDialog("Unable to save configuration file: %v", err)
			}
		}}), false)

	return true
}

func (pc *PositionConfig) Activate() {
	if pc.Frequencies == nil {
		pc.Frequencies = make(map[string]Frequency)
	}
	if pc.eventsId == InvalidEventSubscriberId {
		pc.eventsId = eventStream.Subscribe()
	}
	if pc.sessionDrawVORs == nil {
		pc.sessionDrawVORs = make(map[string]interface{})
	}
	if pc.sessionDrawNDBs == nil {
		pc.sessionDrawNDBs = make(map[string]interface{})
	}
	if pc.sessionDrawFixes == nil {
		pc.sessionDrawFixes = make(map[string]interface{})
	}
	if pc.sessionDrawAirports == nil {
		pc.sessionDrawAirports = make(map[string]interface{})
	}

	pc.CheckRadarCenters()

	pos, _ := database.Locate(pc.PrimaryRadarCenter)
	pc.primaryRadarCenterLocation = pos
	for i, ctr := range pc.SecondaryRadarCenters {
		pos, _ := database.Locate(ctr)
		pc.secondaryRadarCentersLocation[i] = pos
	}
}

func (pc *PositionConfig) Deactivate() {
	eventStream.Unsubscribe(pc.eventsId)
	pc.eventsId = InvalidEventSubscriberId
}

func (pc *PositionConfig) SendUpdates() {
	pc.CheckRadarCenters()

	server.SetRadarCenters(pc.primaryRadarCenterLocation, pc.secondaryRadarCentersLocation,
		int(pc.RadarRange))
}

func (pc *PositionConfig) MonitoredFrequencies(frequencies []Frequency) []Frequency {
	var monitored []Frequency
	for _, f := range frequencies {
		if ptr, ok := pc.rxFrequencies[f]; ok && *ptr {
			monitored = append(monitored, f)
		}
	}
	return monitored
}

func NewPositionConfig() *PositionConfig {
	c := &PositionConfig{}
	if database != nil && database.defaultAirport != "" {
		c.PrimaryRadarCenter = database.defaultAirport
	}
	c.RadarRange = 20
	c.Frequencies = make(map[string]Frequency)

	// Give the user a semi-useful default configuration.
	c.DisplayRoot = &DisplayNode{
		SplitLine: SplitLine{Pos: 0.15, Axis: SplitAxisY},
		Children: [2]*DisplayNode{
			&DisplayNode{Pane: NewSTARSPane("Scope")},
			&DisplayNode{Pane: NewFlightStripPane()},
		},
	}
	c.DisplayRoot.VisitPanes(func(p Pane) { p.Activate() })

	c.ColorSchemeName = SortedMapKeys(builtinColorSchemes)[0]

	return c
}

func (c *PositionConfig) GetColorScheme() *ColorScheme {
	if cs, ok := builtinColorSchemes[c.ColorSchemeName]; ok {
		return cs
	} else if cs, ok := globalConfig.ColorSchemes[c.ColorSchemeName]; !ok {
		lg.Printf("%s: color scheme unknown; returning default", c.ColorSchemeName)
		c.ColorSchemeName = SortedMapKeys(builtinColorSchemes)[0]
		return builtinColorSchemes[c.ColorSchemeName]
	} else {
		return cs
	}
}

func (c *PositionConfig) CheckRadarCenters() {
	// Only show error text in the main window if the radio settings window
	// isn't open.
	if ui.showRadarSettings || !server.Connected() {
		return
	}

	if c.PrimaryRadarCenter == "" {
		uiAddError("Primary radar center is unset. Set it via Settings/Radar...",
			func() bool { return positionConfig != c || c.PrimaryRadarCenter != "" })
	} else if c.primaryRadarCenterLocation.IsZero() {
		msg := fmt.Sprintf("Primary radar center \"%s\" is invalid. Set it via Settings/Radar...", c.PrimaryRadarCenter)
		ctr := c.PrimaryRadarCenter
		uiAddError(msg, func() bool {
			return positionConfig != c || c.PrimaryRadarCenter != ctr ||
				!c.primaryRadarCenterLocation.IsZero()
		})
	}
	for i, ctr := range c.SecondaryRadarCenters {
		if ctr != "" && c.secondaryRadarCentersLocation[i].IsZero() {
			ctr := ctr
			i := i
			uiAddError(fmt.Sprintf("Secondary radar center \"%s\" is invalid. Set it via Settings/Radar...", ctr),
				func() bool {
					return positionConfig != c || c.SecondaryRadarCenters[i] != ctr ||
						!c.secondaryRadarCentersLocation[i].IsZero()
				})
		}
	}
}

func (c *PositionConfig) DrawRadarUI() {
	imgui.InputIntV("Radar range", &c.RadarRange, 5, 25, 0 /* flags */)
	primaryNotOk := ""
	var ok bool
	if c.primaryRadarCenterLocation, ok = database.Locate(c.PrimaryRadarCenter); !ok {
		primaryNotOk = FontAwesomeIconExclamationTriangle + " "
	}
	flags := imgui.InputTextFlagsCharsNoBlank | imgui.InputTextFlagsCharsUppercase
	imgui.InputTextV(primaryNotOk+"Primary center###PrimaryCenter", &c.PrimaryRadarCenter, flags, nil)

	for i, name := range c.SecondaryRadarCenters {
		notOk := ""
		if c.secondaryRadarCentersLocation[i], ok = database.Locate(name); name != "" && !ok {
			notOk = FontAwesomeIconExclamationTriangle + " "
		}
		imgui.InputTextV(fmt.Sprintf(notOk+"Secondary center #%d###SecondaryCenter-%d", i+1, i+1),
			&c.SecondaryRadarCenters[i], flags, nil)
	}
}

func (c *PositionConfig) DrawRadioUI() {
	if c.frequenciesComboBoxState == nil {
		c.frequenciesComboBoxState = NewComboBoxState(2)
	}
	if c.txFrequencies == nil {
		c.txFrequencies = make(map[Frequency]*bool)
	}
	if c.rxFrequencies == nil {
		c.rxFrequencies = make(map[Frequency]*bool)
	}

	if imgui.RadioButtonInt("Unprime radio", (*int)(&c.primaryFrequency), 0) {
		server.SetPrimaryFrequency(c.primaryFrequency)
	}
	config := ComboBoxDisplayConfig{
		ColumnHeaders:    []string{"Position", "Frequency", "Primed", "TX", "RX"},
		DrawHeaders:      true,
		SelectAllColumns: false,
		EntryNames:       []string{"Position", "Frequency"},
		InputFlags:       []imgui.InputTextFlags{imgui.InputTextFlagsCharsUppercase, imgui.InputTextFlagsCharsDecimal},
	}
	DrawComboBox(c.frequenciesComboBoxState, config, SortedMapKeys(c.Frequencies),
		/* draw col */ func(s string, col int) {
			freq := c.Frequencies[s]
			switch col {
			case 1:
				imgui.Text(freq.String())
			case 2:
				if imgui.RadioButtonInt("##prime-"+s, (*int)(&c.primaryFrequency), int(freq)) {
					server.SetPrimaryFrequency(c.primaryFrequency)
				}
			case 3:
				if _, ok := c.txFrequencies[freq]; !ok {
					c.txFrequencies[freq] = new(bool)
				}
				if freq == c.primaryFrequency {
					*c.txFrequencies[freq] = true
				}
				uiStartDisable(freq == c.primaryFrequency)
				imgui.Checkbox("##tx-"+s, c.txFrequencies[freq])
				uiEndDisable(freq == c.primaryFrequency)
			case 4:
				if _, ok := c.rxFrequencies[freq]; !ok {
					c.rxFrequencies[freq] = new(bool)
				}
				if freq == c.primaryFrequency {
					*c.rxFrequencies[freq] = true
				}
				uiStartDisable(freq == c.primaryFrequency)
				imgui.Checkbox("##rx-"+s, c.rxFrequencies[freq])
				uiEndDisable(freq == c.primaryFrequency)
			default:
				lg.Errorf("%d: unexpected column from DrawComboBox", col)
			}
		},
		/* valid */
		func(entries []*string) bool {
			_, ok := c.Frequencies[*entries[0]]
			if ok {
				return false
			}
			f, err := strconv.ParseFloat(*entries[1], 32)
			// TODO: what range should we accept?
			return *entries[0] != "" && err == nil && f >= 100 && f <= 150
		},
		/* add */ func(entries []*string) {
			// Assume that valid has passed for this input
			f, _ := strconv.ParseFloat(*entries[1], 32)
			c.Frequencies[*entries[0]] = NewFrequency(float32(f))
		},
		/* delete */ func(selected map[string]interface{}) {
			for k := range selected {
				delete(c.Frequencies, k)
			}
		})
}

func (c *PositionConfig) Duplicate() *PositionConfig {
	nc := &PositionConfig{}
	*nc = *c
	nc.DisplayRoot = c.DisplayRoot.Duplicate()
	nc.Frequencies = DuplicateMap(c.Frequencies)

	nc.eventsId = InvalidEventSubscriberId
	nc.frequenciesComboBoxState = nil
	nc.txFrequencies = nil
	nc.rxFrequencies = nil

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
			ShowErrorDialog("Unable to read config file: %v\nUsing default configuration.", err)
			fn = "default.config"
		}
	}

	r := bytes.NewReader(config)
	d := json.NewDecoder(r)

	globalConfig = &GlobalConfig{}
	if err := d.Decode(globalConfig); err != nil {
		ShowErrorDialog("Configuration file is corrupt: %v", err)
	}
	if globalConfig.CustomServers == nil {
		globalConfig.CustomServers = make(map[string]string)
	}

	imgui.LoadIniSettingsFromMemory(globalConfig.ImGuiSettings)
}

func (pc *PositionConfig) Update() {
	for _, event := range eventStream.Get(pc.eventsId) {
		if sel, ok := event.(*SelectedAircraftEvent); ok {
			pc.selectedAircraft = sel.ac
		}
	}
}
