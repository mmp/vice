// ui.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"runtime/debug"
	"strings"
	"time"
	"unicode"

	"github.com/mmp/imgui-go/v4"
	"github.com/pkg/browser"
)

var (
	ui struct {
		font      *Font
		aboutFont *Font
		fixedFont *Font

		errorText         map[string]string
		topControlsHeight float32

		showAboutDialog           bool
		showGeneralSettingsWindow bool
		showColorEditor           bool
		showSoundConfig           bool

		iconTextureID     uint32
		sadTowerTextureID uint32

		connectDialog           *ModalDialogBox
		disconnectDialog        *ModalDialogBox
		newConfigDialog         *ModalDialogBox
		newFromCurrentDialog    *ModalDialogBox
		renameDialog            *ModalDialogBox
		deleteDialog            *ModalDialogBox
		saveChangedDialog       *ModalDialogBox
		errorDialog             *ModalDialogBox
		confirmDisconnectDialog *ModalDialogBox
		newReleaseDialog        *ModalDialogBox

		openSectorFileDialog   *FileSelectDialogBox
		openPositionFileDialog *FileSelectDialogBox
		openNotesFileDialog    *FileSelectDialogBox
	}

	//go:embed icons/tower-256x256.png
	iconPNG string
	//go:embed icons/sad-tower-alpha-128x128.png
	sadTowerPNG string
)

func imguiInit() *imgui.Context {
	context := imgui.CreateContext(nil)
	imgui.CurrentIO().SetIniFilename("")

	// General imgui styling
	style := imgui.CurrentStyle()
	style.SetFrameRounding(2.)
	style.SetWindowRounding(4.)
	style.SetPopupRounding(4.)
	style.SetScrollbarSize(6.)
	style.ScaleAllSizes(1.25)

	return context
}

func uiInit(renderer Renderer) {
	ui.font = GetFont(FontIdentifier{Name: "Roboto Regular", Size: 16})
	ui.aboutFont = GetFont(FontIdentifier{Name: "Roboto Regular", Size: 18})
	ui.fixedFont = GetFont(FontIdentifier{Name: "Source Code Pro Regular", Size: 16})

	ui.errorText = make(map[string]string)

	var err error
	ui.iconTextureID, err = renderer.CreateTextureFromPNG(bytes.NewReader([]byte(iconPNG)))
	if err != nil {
		lg.Errorf("Unable to create icon texture: %v", err)
	}
	ui.sadTowerTextureID, err = renderer.CreateTextureFromPNG(bytes.NewReader([]byte(sadTowerPNG)))
	if err != nil {
		lg.Errorf("Unable to create sad tower icon texture: %v", err)
	}

	ui.connectDialog = NewModalDialogBox(&ConnectModalClient{})
	ui.disconnectDialog = NewModalDialogBox(&DisconnectModalClient{})

	ui.newConfigDialog = NewModalDialogBox(&NewModalClient{isBrandNew: true})
	ui.newFromCurrentDialog = NewModalDialogBox(&NewModalClient{isBrandNew: false})
	ui.renameDialog = NewModalDialogBox(&RenameModalClient{})
	ui.deleteDialog = NewModalDialogBox(&DeleteModalClient{})

	if nrc := checkForNewRelease(); nrc != nil {
		ui.newReleaseDialog = NewModalDialogBox(nrc)
		ui.newReleaseDialog.Activate()
	}

	ui.openSectorFileDialog = NewFileSelectDialogBox("Open Sector File...", []string{".sct", ".sct2"},
		globalConfig.SectorFile,
		func(filename string) {
			if err := world.LoadSectorFile(filename); err == nil {
				delete(ui.errorText, "SECTORFILE")
				globalConfig.SectorFile = filename
				world.SetColorScheme(positionConfig.GetColorScheme())

				// This is probably the wrong place to do this, but it's
				// convenient... Walk through the radar scopes and center
				// any that have a (0,0) center according to the position
				// file center. This fixes things up with the default scope
				// on a first run.
				positionConfig.DisplayRoot.VisitPanes(func(p Pane) {
					if rs, ok := p.(*RadarScopePane); ok {
						if rs.Center[0] == 0 && rs.Center[1] == 0 {
							rs.Center = world.defaultCenter
						}
					}
				})
			}
		})
	ui.openPositionFileDialog = NewFileSelectDialogBox("Open Position File...", []string{".pof"},
		globalConfig.PositionFile,
		func(filename string) {
			if err := world.LoadPositionFile(filename); err == nil {
				delete(ui.errorText, "POSITIONFILE")
				globalConfig.PositionFile = filename
			}
		})
	ui.openNotesFileDialog = NewFileSelectDialogBox("Open Notes File...", []string{".txt"},
		globalConfig.NotesFile,
		func(filename string) {
			globalConfig.NotesFile = filename
			globalConfig.LoadNotesFile()
		})
}

func (c RGB) imgui() imgui.Vec4 {
	return imgui.Vec4{c.R, c.G, c.B, 1}
}

func drawUI(cs *ColorScheme, platform Platform) {
	imgui.PushFont(ui.font.ifont)
	if imgui.BeginMainMenuBar() {
		if imgui.BeginMenu("Connection") {
			if imgui.MenuItemV("Connect...", "", false, !world.Connected()) {
				ui.connectDialog.Activate()
			}
			if imgui.MenuItemV("Disconnect...", "", false, world.Connected()) {
				ui.disconnectDialog.Activate()
			}
			imgui.EndMenu()
		}

		if imgui.BeginMenu("Configs") {
			if imgui.MenuItem("New...") {
				ui.newConfigDialog.Activate()
			}
			if imgui.MenuItem("New from current...") {
				ui.newFromCurrentDialog.Activate()
			}
			if imgui.MenuItem("Rename...") {
				ui.renameDialog.Activate()
			}
			if imgui.MenuItemV("Delete...", "", false, len(globalConfig.PositionConfigs) > 1) {
				ui.deleteDialog.Activate()
			}
			if imgui.MenuItem("Edit layout...") {
				wm.showConfigEditor = true
				// FIXME: this is the wrong place to be doing this...
				wm.editorBackupRoot = positionConfig.DisplayRoot.Duplicate()
			}

			imgui.Separator()

			// Sort them by name
			names := SortedMapKeys(globalConfig.PositionConfigs)
			for _, name := range names {
				if imgui.MenuItemV(name, "", name == globalConfig.ActivePosition, true) &&
					name != globalConfig.ActivePosition {
					globalConfig.MakeConfigActive(name)
				}
			}

			imgui.EndMenu()
		}
		if imgui.BeginMenu("Settings") {
			if imgui.MenuItem("Save") {
				if err := globalConfig.Save(); err != nil {
					ShowErrorDialog("Error saving configuration file: %v", err)
				}
			}
			if imgui.MenuItem("General...") {
				ui.showGeneralSettingsWindow = true
			}
			if imgui.MenuItem("Colors...") {
				ui.showColorEditor = true
			}
			if imgui.MenuItem("Sounds...") {
				ui.showSoundConfig = true
			}
			imgui.EndMenu()
		}

		if imgui.BeginMenu("Subwindows") {
			wmAddPaneMenuSettings()
			imgui.EndMenu()
		}

		if imgui.BeginMenu("Help") {
			if imgui.MenuItem("Documentation...") {
				browser.OpenURL("https://vice.pharr.org/documentation.html")
			}
			if imgui.MenuItem("Report a bug...") {
				browser.OpenURL("https://vice.pharr.org/documentation.html#bugs")
			}
			imgui.Separator()
			if imgui.MenuItem("About vice...") {
				ui.showAboutDialog = true
			}
			imgui.EndMenu()
		}

		t := FontAwesomeIconBug + " Report Bug"
		width, _ := ui.font.BoundText(t, 0)
		imgui.SetCursorPos(imgui.Vec2{platform.DisplaySize()[0] - float32(width+10), 0})
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.CurrentStyle().Color(imgui.StyleColorMenuBarBg))
		if imgui.Button(t) {
			browser.OpenURL("https://vice.pharr.org/documentation.html#bugs")
		}
		imgui.PopStyleColor()

		imgui.EndMainMenuBar()
	}
	ui.topControlsHeight = 1.35 * float32(ui.font.size)

	if len(ui.errorText) > 0 {
		errorTextHeight := 20 + float32(len(ui.errorText))*float32(ui.font.size)

		displaySize := platform.DisplaySize()
		imgui.SetNextWindowPosV(imgui.Vec2{X: 0, Y: ui.topControlsHeight}, imgui.ConditionAlways, imgui.Vec2{})
		imgui.SetNextWindowSize(imgui.Vec2{displaySize[0], errorTextHeight})
		ui.topControlsHeight += errorTextHeight

		var flags imgui.WindowFlags
		flags = imgui.WindowFlagsNoDecoration
		flags |= imgui.WindowFlagsNoSavedSettings
		flags |= imgui.WindowFlagsNoNav
		flags |= imgui.WindowFlagsNoResize

		text := ""
		for _, k := range SortedMapKeys(ui.errorText) {
			text += ui.errorText[k] + "\n"
		}
		text = strings.TrimRight(text, "\n")

		if imgui.BeginV("Errors", nil, flags) {
			imgui.PushStyleColor(imgui.StyleColorText, cs.TextError.imgui())
			imgui.Text(text)
			imgui.PopStyleColor()
			imgui.End()
		}
	}

	ui.connectDialog.Draw()
	ui.disconnectDialog.Draw()
	ui.newConfigDialog.Draw()
	ui.newFromCurrentDialog.Draw()
	ui.renameDialog.Draw()
	ui.deleteDialog.Draw()

	if ui.newReleaseDialog != nil {
		ui.newReleaseDialog.Draw()
	}
	if ui.errorDialog != nil {
		ui.errorDialog.Draw()
	}
	if ui.saveChangedDialog != nil {
		ui.saveChangedDialog.Draw()
	}
	if ui.confirmDisconnectDialog != nil {
		ui.confirmDisconnectDialog.Draw()
	}

	ui.openSectorFileDialog.Draw()
	ui.openPositionFileDialog.Draw()
	ui.openNotesFileDialog.Draw()

	if ui.showAboutDialog {
		showAboutDialog()
	}

	if ui.showGeneralSettingsWindow {
		imgui.BeginV("General Settings", &ui.showGeneralSettingsWindow, imgui.WindowFlagsAlwaysAutoResize)
		globalConfig.DrawUI()
		imgui.End()
	}

	if ui.showColorEditor {
		imgui.BeginV("Color Editor: "+positionConfig.ColorSchemeName, &ui.showColorEditor,
			imgui.WindowFlagsAlwaysAutoResize)
		cs := positionConfig.GetColorScheme()
		cs.ShowEditor(func(name string, rgb RGB) {
			if positionConfig.GetColorScheme().IsDark() {
				imgui.StyleColorsDark()
			} else {
				imgui.StyleColorsLight()
			}
			world.NamedColorChanged(name, rgb)
		})
		imgui.End()
	}

	if ui.showSoundConfig {
		imgui.BeginV("Sound Configuration", &ui.showSoundConfig, imgui.WindowFlagsAlwaysAutoResize)
		globalConfig.AudioSettings.DrawUI()
		imgui.End()
	}

	wmDrawUI(platform)

	imgui.PopFont()
}

func setCursorForRightButtons(text []string) {
	style := imgui.CurrentStyle()
	width := float32(0)

	for i, t := range text {
		width += imgui.CalcTextSize(t, false, 100000).X + 2*style.FramePadding().X
		if i > 0 {
			// space between buttons
			width += style.ItemSpacing().X
		}
	}
	offset := imgui.ContentRegionAvail().X - width
	imgui.SetCursorPos(imgui.Vec2{offset, imgui.CursorPosY()})
}

///////////////////////////////////////////////////////////////////////////

func drawAirportSelector(airports map[string]interface{}, title string) map[string]interface{} {
	airportsString := strings.Join(SortedMapKeys(airports), ",")

	if imgui.InputTextV(title, &airportsString, imgui.InputTextFlagsCharsUppercase, nil) {
		ap := strings.FieldsFunc(airportsString, func(ch rune) bool {
			return unicode.IsSpace(ch) || ch == ','
		})

		airports = make(map[string]interface{})
		for _, a := range ap {
			airports[a] = nil
		}
	}

	return airports
}

///////////////////////////////////////////////////////////////////////////

type ModalDialogBox struct {
	show, isOpen bool
	client       ModalDialogClient
}

type ModalDialogButton struct {
	text     string
	disabled bool
	action   func() bool
}

type ModalDialogClient interface {
	Title() string
	Opening()
	Buttons() []ModalDialogButton
	Draw() int /* returns index of equivalently-clicked button; out of range if none */
}

func NewModalDialogBox(c ModalDialogClient) *ModalDialogBox {
	return &ModalDialogBox{client: c}
}

func (m *ModalDialogBox) Activate() {
	m.show = true
	m.isOpen = false
}

func (m *ModalDialogBox) Draw() {
	if !m.show {
		return
	}

	title := fmt.Sprintf("%s##%p", m.client.Title(), m)
	if !m.isOpen {
		imgui.OpenPopup(title)
	}

	flags := imgui.WindowFlagsNoResize | imgui.WindowFlagsAlwaysAutoResize | imgui.WindowFlagsNoSavedSettings
	if imgui.BeginPopupModalV(title, nil, flags) {
		if !m.isOpen {
			imgui.SetKeyboardFocusHere()
			m.client.Opening()
			m.isOpen = true
		}

		selIndex := m.client.Draw()
		imgui.Text("\n") // spacing

		buttons := m.client.Buttons()

		// First, figure out where to start drawing so the buttons end up right-justified.
		// https://github.com/ocornut/imgui/discussions/3862
		var allButtonText []string
		for _, b := range buttons {
			allButtonText = append(allButtonText, b.text)
		}
		setCursorForRightButtons(allButtonText)

		for i, b := range buttons {
			if b.disabled {
				imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
				imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
			}
			if i > 0 {
				imgui.SameLine()
			}
			if (imgui.Button(b.text) || i == selIndex) && !b.disabled {
				if b.action == nil || b.action() {
					imgui.CloseCurrentPopup()
					m.show = false
					m.isOpen = false
				}
			}
			if b.disabled {
				imgui.PopItemFlag()
				imgui.PopStyleVar()
			}
		}

		imgui.EndPopup()
	}
}

type ConnectionConfiguration interface {
	Initialize()
	DrawUI() bool /* hit enter */
	Valid() bool
	Connect(w *World) error
}

type FlightRadarConnectionConfiguration struct{}

func (*FlightRadarConnectionConfiguration) Initialize()  {}
func (*FlightRadarConnectionConfiguration) DrawUI() bool { return false }
func (*FlightRadarConnectionConfiguration) Valid() bool  { return true }

func (*FlightRadarConnectionConfiguration) Connect(w *World) error {
	return w.ConnectFlightRadar()
}

type VATSIMConnectionConfiguration struct {
	address string
	/*
		callsign string
		position *Position

		// FIXME: these aren't connection specific...
		facility Facility
		CID      string
		password string
	*/
}

func (v *VATSIMConnectionConfiguration) Initialize() {
	if v.address == "" {
		v.address = ":6809"
	}
}

func (v *VATSIMConnectionConfiguration) DrawUI() bool {
	flags := imgui.InputTextFlagsEnterReturnsTrue
	enter := imgui.InputTextV("Address", &v.address, flags, nil)
	/*
		enter = imgui.InputTextV("CID", &v.CID, flags, nil) || enter
		enter = imgui.InputTextV("Password", &v.password, flags|imgui.InputTextFlagsPassword, nil) || enter
		enter = imgui.InputTextV("Callsign", &v.callsign, flags, nil) || enter

		freq := "(unset)"
		if v.position != nil {
			freq = fmt.Sprintf("%s: %s", v.position.frequency, v.position.name)
		}
		if imgui.BeginCombo("Frequency", freq) {
			cs := strings.Split(v.callsign, "_")
			callsign := cs[0] + "_" + cs[len(cs)-1] // simplify e.g. JFK_1_TWR, etc.
			for i := range world.positions[callsign] {
				pos := &world.positions[callsign][i]
				name := fmt.Sprintf("%s: %s", pos.frequency, pos.name)
				if imgui.SelectableV(name, pos == v.position, 0, imgui.Vec2{}) {
					v.position = pos
				}
			}
			imgui.EndCombo()
		}
		if imgui.BeginCombo("Facility", v.facility.String()) {
			for i := 0; i < FacilityUndefined; i++ {
				name := Facility(i).String()
				if imgui.SelectableV(name, Facility(i) == v.facility, 0, imgui.Vec2{}) {
					v.facility = Facility(i)
				}
			}
			imgui.EndCombo()
		}
	*/
	return enter
}

func (v *VATSIMConnectionConfiguration) Valid() bool {
	return v.address != "" // && v.callsign != "" && v.position != nil
}

func (v *VATSIMConnectionConfiguration) Connect(w *World) error {
	return w.ConnectVATSIM(v.address)
}

type VATSIMReplayConfiguration struct {
	filename string
	rate     float32
	offset   int32
	dialog   *FileSelectDialogBox
}

func (v *VATSIMReplayConfiguration) Initialize() {
	if v.rate == 0 {
		v.rate = 1
		v.offset = 0
		v.dialog = NewFileSelectDialogBox("Select VATSIM session file", []string{".vsess"}, "",
			func(fn string) { v.filename = fn })
	}
	if v.filename == "" {
		v.dialog.Activate()
	}
}

func (v *VATSIMReplayConfiguration) DrawUI() bool {
	imgui.Text("Filename: " + v.filename)
	imgui.SameLine()
	if imgui.Button("Select...") {
		v.dialog.Activate()
	}
	v.dialog.Draw()

	imgui.SliderFloatV("Playback rate multiplier", &v.rate, 0.1, 300, "%.1f",
		imgui.SliderFlagsLogarithmic)
	imgui.InputIntV("Playback starting offset (seconds)", &v.offset, 0, 3600, 0)

	return false
}

func (v *VATSIMReplayConfiguration) Valid() bool {
	return v.filename != ""
}

func (v *VATSIMReplayConfiguration) Connect(w *World) error {
	return w.ConnectVATSIMReplay(v.filename, int(v.offset), v.rate)
}

type ConnectModalClient struct {
	connectionType ConnectionType
	err            string

	// Store all three so that if the user switches back and forth any set
	// values are retained.
	flightRadar  FlightRadarConnectionConfiguration
	vatsim       VATSIMConnectionConfiguration
	vatsimReplay VATSIMReplayConfiguration
}

type ConnectionType int

const (
	ConnectionTypeFlightRadar = iota
	ConnectionTypeVATSIM
	ConnectionTypeVATSIMReplay
	ConnectionTypeCount
)

func (c ConnectionType) String() string {
	return [...]string{"Flight Radar", "VATSIM (not actually)", "VATSIM Replay"}[c]
}

func (c *ConnectModalClient) Title() string { return "New Connection" }

func (c *ConnectModalClient) Opening() {
	c.err = ""
	c.flightRadar.Initialize()
	c.vatsim.Initialize()
	c.vatsimReplay.Initialize()
}

func (c *ConnectModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		var err error
		switch c.connectionType {
		case ConnectionTypeFlightRadar:
			err = c.flightRadar.Connect(world)

		case ConnectionTypeVATSIM:
			err = c.vatsim.Connect(world)

		case ConnectionTypeVATSIMReplay:
			err = c.vatsimReplay.Connect(world)

		default:
			lg.Errorf("Unhandled connection type")
			return true
		}

		if err == nil {
			c.err = ""
		} else {
			c.err = err.Error()
		}
		return err == nil
	}}

	switch c.connectionType {
	case ConnectionTypeFlightRadar:
		ok.disabled = !c.flightRadar.Valid()

	case ConnectionTypeVATSIM:
		ok.disabled = !c.vatsim.Valid()

	case ConnectionTypeVATSIMReplay:
		ok.disabled = !c.vatsimReplay.Valid()

	default:
		lg.Errorf("Unhandled connection type")
	}
	b = append(b, ok)

	return b
}

func (c *ConnectModalClient) Draw() int {
	if imgui.BeginCombo("Type", c.connectionType.String()) {
		for i := 0; i < ConnectionTypeCount; i++ {
			ct := ConnectionType(i)
			if imgui.SelectableV(ct.String(), ct == c.connectionType, 0, imgui.Vec2{}) {
				c.connectionType = ct
			}
		}
		imgui.EndCombo()
	}

	var enter bool
	switch c.connectionType {
	case ConnectionTypeFlightRadar:
		enter = c.flightRadar.DrawUI()

	case ConnectionTypeVATSIM:
		enter = c.vatsim.DrawUI()

	case ConnectionTypeVATSIMReplay:
		enter = c.vatsimReplay.DrawUI()
	}

	if c.err != "" {
		imgui.Text(c.err)
	}

	if enter {
		return 1
	} else {
		return -1
	}
}

type DisconnectModalClient struct {
	err string
}

func (d *DisconnectModalClient) Title() string { return "Confirm Disconnection" }

func (d *DisconnectModalClient) Opening() {
	d.err = ""
}

func (c *DisconnectModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		if err := world.Disconnect(); err != nil {
			lg.Errorf("Disconnect: %v", err)
			c.err = err.Error()
			return false
		}
		return true
	}}
	b = append(b, ok)

	return b
}

func (d *DisconnectModalClient) Draw() int {
	imgui.Text("Are you sure you want to disconnect?")
	if d.err != "" {
		color := positionConfig.GetColorScheme().TextError
		imgui.PushStyleColor(imgui.StyleColorText, color.imgui())
		imgui.Text(d.err)
		imgui.PopStyleColor()
	}
	return -1
}

type NewModalClient struct {
	name       string
	err        string
	isBrandNew bool
}

func (n *NewModalClient) Title() string { return "Create New Config" }

func (n *NewModalClient) Opening() {
	n.name = ""
	n.err = ""
}

func (n *NewModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		if n.isBrandNew {
			globalConfig.PositionConfigs[n.name] = NewPositionConfig()
		} else {
			globalConfig.PositionConfigs[n.name] = positionConfig.Duplicate()
		}
		globalConfig.MakeConfigActive(n.name)
		return true
	}}
	ok.disabled = n.name == ""
	if _, exists := globalConfig.PositionConfigs[n.name]; exists {
		ok.disabled = true
		n.err = "\"" + n.name + "\" already exists"
	} else {
		n.err = ""
	}
	b = append(b, ok)

	return b
}

func (n *NewModalClient) Draw() int {
	flags := imgui.InputTextFlagsEnterReturnsTrue
	enter := imgui.InputTextV("Configuration name", &n.name, flags, nil)
	if n.err != "" {
		cs := positionConfig.GetColorScheme()
		imgui.PushStyleColor(imgui.StyleColorText, cs.Error.imgui())
		imgui.Text(n.err)
		imgui.PopStyleColor()
	}
	if enter {
		return 1
	} else {
		return -1
	}
}

type RenameModalClient struct {
	selectedName, newName string
}

func (r *RenameModalClient) Title() string { return "Rename Config" }

func (r *RenameModalClient) Opening() {
	r.selectedName = ""
	r.newName = ""
}

func (r *RenameModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		if globalConfig.ActivePosition == r.selectedName {
			globalConfig.ActivePosition = r.newName
		}
		pc := globalConfig.PositionConfigs[r.selectedName]
		delete(globalConfig.PositionConfigs, r.selectedName)
		globalConfig.PositionConfigs[r.newName] = pc
		return true
	}}
	ok.disabled = r.selectedName == ""
	if !ok.disabled {
		_, ok.disabled = globalConfig.PositionConfigs[r.newName]
	}
	b = append(b, ok)

	return b
}

func (r *RenameModalClient) Draw() int {
	if imgui.BeginCombo("Config Name", r.selectedName) {
		names := SortedMapKeys(globalConfig.PositionConfigs)

		for _, name := range names {
			if imgui.SelectableV(name, name == r.selectedName, 0, imgui.Vec2{}) {
				r.selectedName = name
			}
		}
		imgui.EndCombo()
	}

	flags := imgui.InputTextFlagsEnterReturnsTrue
	enter := imgui.InputTextV("New name", &r.newName, flags, nil)

	if _, ok := globalConfig.PositionConfigs[r.newName]; ok {
		color := positionConfig.GetColorScheme().TextError
		imgui.PushStyleColor(imgui.StyleColorText, color.imgui())
		imgui.Text("Config with that name already exits!")
		imgui.PopStyleColor()
	}
	if enter {
		return 1
	} else {
		return -1
	}
}

type DeleteModalClient struct {
	name string
}

func (d *DeleteModalClient) Title() string { return "Delete Config" }

func (d *DeleteModalClient) Opening() { d.name = "" }

func (d *DeleteModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		if globalConfig.ActivePosition == d.name {
			for name := range globalConfig.PositionConfigs {
				if name != d.name {
					globalConfig.MakeConfigActive(name)
					break
				}
			}
		}
		delete(globalConfig.PositionConfigs, d.name)
		return true
	}}
	ok.disabled = d.name == ""
	b = append(b, ok)

	return b
}

func (d *DeleteModalClient) Draw() int {
	if imgui.BeginCombo("Config Name", d.name) {
		names := SortedMapKeys(globalConfig.PositionConfigs)
		for _, name := range names {
			if imgui.SelectableV(name, name == d.name, 0, imgui.Vec2{}) {
				d.name = name
			}
		}
		imgui.EndCombo()
	}
	return -1
}

type YesOrNoModalClient struct {
	title, query string
	ok, notok    func()
}

func (yn *YesOrNoModalClient) Title() string { return yn.title }

func (yn *YesOrNoModalClient) Opening() {}

func (yn *YesOrNoModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "No", action: func() bool {
		yn.notok()
		return true
	}})
	b = append(b, ModalDialogButton{text: "Yes", action: func() bool {
		yn.ok()
		return true
	}})
	return b
}

func (yn *YesOrNoModalClient) Draw() int {
	imgui.Text(yn.query)
	return -1
}

func checkForNewRelease() *NewReleaseModalClient {
	url := "https://api.github.com/repos/mmp/vice/releases"

	resp, err := http.Get(url)
	if err != nil {
		lg.Errorf("%s: get err: %v", url, err)
		return nil
	}
	defer resp.Body.Close()

	type Release struct {
		TagName string    `json:"tag_name"`
		Created time.Time `json:"created_at"`
	}

	decoder := json.NewDecoder(resp.Body)
	var releases []Release
	if err := decoder.Decode(&releases); err != nil {
		lg.Errorf("JSON decode error: %v", err)
		return nil
	}
	if len(releases) == 0 {
		return nil
	}

	newestRelease := releases[0]
	for i := range releases {
		if releases[i].Created.After(newestRelease.Created) {
			newestRelease = releases[i]
		}
	}

	lg.Printf("newest release found: %v", newestRelease)

	buildTime := ""
	if bi, ok := debug.ReadBuildInfo(); !ok {
		lg.Errorf("unable to read build info")
		return nil
	} else {
		for _, setting := range bi.Settings {
			if setting.Key == "vcs.time" {
				buildTime = setting.Value
				break
			}
		}

		if buildTime == "" {
			lg.Errorf("build time unavailable in BuildInfo.Settings")
			return nil
		}
	}

	if bt, err := time.Parse(time.RFC3339, buildTime); err != nil {
		lg.Errorf("error parsing build time \"%s\": %v", buildTime, err)
		return nil
	} else if bt.UTC().After(newestRelease.Created.UTC()) {
		lg.Printf("build time %s newest release %s -> build is newer",
			bt.UTC().String(), newestRelease.Created.UTC().String())
		return nil
	} else {
		lg.Printf("build time %s newest release %s -> release is newer",
			bt.UTC().String(), newestRelease.Created.UTC().String())
		return &NewReleaseModalClient{
			version: newestRelease.TagName,
			date:    newestRelease.Created}
	}
}

type NewReleaseModalClient struct {
	version string
	date    time.Time
}

func (nr *NewReleaseModalClient) Title() string {
	return "A new vice release is available"
}
func (nr *NewReleaseModalClient) Opening() {}

func (nr *NewReleaseModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		ModalDialogButton{
			text: "Quit and update",
			action: func() bool {
				browser.OpenURL("https://vice.pharr.org/documentation#section-installation")
				os.Exit(0)
				return true
			},
		},
		ModalDialogButton{text: "Update later"}}
}

func (nr *NewReleaseModalClient) Draw() int {
	imgui.Text(fmt.Sprintf("vice version %s is the latest version", nr.version))
	imgui.Text("Would you like to quit and open the vice downloads page?")
	return -1
}

///////////////////////////////////////////////////////////////////////////
// "about" dialog box

func showAboutDialog() {
	flags := imgui.WindowFlagsNoResize | imgui.WindowFlagsNoSavedSettings
	imgui.BeginV("About vice...", &ui.showAboutDialog, flags)

	imgui.Image(imgui.TextureID(ui.iconTextureID), imgui.Vec2{256, 256})

	center := func(s string) {
		// https://stackoverflow.com/a/67855985
		ww := imgui.WindowSize().X
		tw := imgui.CalcTextSize(s, false, 0).X
		imgui.SetCursorPos(imgui.Vec2{(ww - tw) * 0.5, imgui.CursorPosY()})
		imgui.Text(s)
	}

	imgui.PushFont(ui.aboutFont.ifont)
	center("vice: a client for VATSIM")
	center(FontAwesomeIconCopyright + "2022 Matt Pharr")
	center("Licensed under the GPL, Version 3")
	if imgui.IsItemHovered() && imgui.IsMouseClicked(0) {
		browser.OpenURL("https://www.gnu.org/licenses/gpl-3.0.html")
	}
	center("Current build: " + buildVersion)
	center("Source code: " + FontAwesomeIconGithub)
	if imgui.IsItemHovered() && imgui.IsMouseClicked(0) {
		browser.OpenURL("https://github.com/mmp/vice")
	}

	imgui.PopFont()

	imgui.End()
}

///////////////////////////////////////////////////////////////////////////
// FileSelectDialogBox

type FileSelectDialogBox struct {
	show, isOpen bool
	filename     string
	directory    string

	dirEntries            []os.DirEntry
	dirEntriesLastUpdated time.Time

	title    string
	filter   []string
	callback func(string)
}

func NewFileSelectDialogBox(title string, filter []string, filename string,
	callback func(string)) *FileSelectDialogBox {
	var dir string
	if filename != "" {
		dir = path.Dir(filename)
	} else {
		var err error
		if dir, err = os.UserHomeDir(); err != nil {
			lg.Errorf("Unable to get user home directory: %v", err)
			dir = "."
		}
	}
	dir = path.Clean(dir)

	return &FileSelectDialogBox{
		title:     title,
		directory: dir,
		filter:    filter,
		callback:  callback}
}

func (fs *FileSelectDialogBox) Activate() {
	fs.show = true
	fs.isOpen = false
}

func (fs *FileSelectDialogBox) Draw() {
	if !fs.show {
		return
	}

	if !fs.isOpen {
		imgui.OpenPopup(fs.title)
	}

	flags := imgui.WindowFlagsNoResize | imgui.WindowFlagsAlwaysAutoResize | imgui.WindowFlagsNoSavedSettings
	if imgui.BeginPopupModalV(fs.title, nil, flags) {
		if !fs.isOpen {
			imgui.SetKeyboardFocusHere()
			fs.isOpen = true
		}

		if imgui.Button(FontAwesomeIconHome) {
			var err error
			fs.directory, err = os.UserHomeDir()
			if err != nil {
				lg.Errorf("Unable to get user home dir: %v", err)
				fs.directory = "."
			}
		}
		imgui.SameLine()
		if imgui.Button(FontAwesomeIconLevelUpAlt) {
			fs.directory, _ = path.Split(fs.directory)
			fs.directory = path.Clean(fs.directory) // get rid of trailing slash
			fs.dirEntriesLastUpdated = time.Time{}
			fs.filename = ""
		}

		imgui.SameLine()
		imgui.Text(fs.directory)

		// Only rescan the directory contents once a second.
		if time.Since(fs.dirEntriesLastUpdated) > 1*time.Second {
			var err error
			fs.dirEntries, err = os.ReadDir(fs.directory)
			if err != nil {
				lg.Errorf("%s: unable to read directory: %v", fs.directory, err)
			}
			fs.dirEntriesLastUpdated = time.Now()
		}

		flags := imgui.TableFlagsScrollY | imgui.TableFlagsRowBg
		fileSelected := false
		// unique per-directory id maintains the scroll position in each
		// directory (and starts newly visited ones at the top!)
		if imgui.BeginTableV("Files##"+fs.directory, 1, flags,
			imgui.Vec2{500, float32(platform.WindowSize()[1] * 3 / 4)}, 0) {
			imgui.TableSetupColumn("Filename")
			for _, entry := range fs.dirEntries {
				icon := ""
				if entry.IsDir() {
					icon = FontAwesomeIconFolder
				} else {
					icon = FontAwesomeIconFile
				}

				canSelect := entry.IsDir()
				if !entry.IsDir() {
					for _, f := range fs.filter {
						if strings.HasSuffix(strings.ToUpper(entry.Name()), strings.ToUpper(f)) {
							canSelect = true
							break
						}
					}
				}

				if !canSelect {
					imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
					imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
				}
				imgui.TableNextRow()
				imgui.TableNextColumn()
				selFlags := imgui.SelectableFlagsSpanAllColumns
				if imgui.SelectableV(icon+" "+entry.Name(), entry.Name() == fs.filename, selFlags, imgui.Vec2{}) {
					fs.filename = entry.Name()
				}
				if imgui.IsItemHovered() && imgui.IsMouseDoubleClicked(0) {
					if entry.IsDir() {
						fs.directory = path.Join(fs.directory, entry.Name())
						fs.filename = ""
						fs.dirEntriesLastUpdated = time.Time{}
					} else {
						fileSelected = true
					}
				}
				if !canSelect {
					imgui.PopItemFlag()
					imgui.PopStyleVar()
				}
			}
			imgui.EndTable()
		}

		if imgui.Button("Cancel") {
			imgui.CloseCurrentPopup()
			fs.show = false
			fs.isOpen = false
			fs.filename = ""
		}

		disableOk := fs.filename == ""
		if disableOk {
			imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
			imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
		}
		imgui.SameLine()
		if imgui.Button("Ok") || fileSelected {
			imgui.CloseCurrentPopup()
			fs.show = false
			fs.isOpen = false
			fs.callback(path.Join(fs.directory, fs.filename))
			fs.filename = ""
		}
		if disableOk {
			imgui.PopItemFlag()
			imgui.PopStyleVar()
		}

		imgui.EndPopup()
	}
}

type ErrorModalClient struct {
	message string
}

func (e *ErrorModalClient) Title() string { return "Vice Error" }
func (e *ErrorModalClient) Opening()      {}

func (e *ErrorModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Ok", action: func() bool {
		return true
	}})
	return b
}

func (e *ErrorModalClient) Draw() int {
	if imgui.BeginTableV("Error", 2, 0, imgui.Vec2{}, 0) {
		imgui.TableSetupColumn("icon")
		imgui.TableSetupColumn("text")

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.Image(imgui.TextureID(ui.sadTowerTextureID), imgui.Vec2{128, 128})

		imgui.TableNextColumn()
		imgui.Text("\n\n" + e.message)

		imgui.EndTable()
	}
	return -1
}

func ShowErrorDialog(s string, args ...interface{}) {
	ui.errorDialog = NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)})
	ui.errorDialog.Activate()

	lg.ErrorfUp1(s, args...)
}

func ShowFatalErrorDialog(s string, args ...interface{}) {
	lg.ErrorfUp1(s, args...)

	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)})
	d.Activate()
	for d.show {
		platform.ProcessEvents()
		platform.NewFrame()
		imgui.NewFrame()
		imgui.PushFont(ui.font.ifont)
		d.Draw()
		imgui.PopFont()
		imgui.Render()
		renderer.RenderImgui(platform.DisplaySize(), platform.FramebufferSize(), imgui.RenderedDrawData())
		platform.PostRender()
	}
}
