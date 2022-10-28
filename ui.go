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
	"sort"
	"strings"
	"time"
	"unicode"
	"unsafe"

	"github.com/mmp/imgui-go/v4"
	"github.com/pkg/browser"
)

var (
	ui struct {
		font      *Font
		aboutFont *Font
		fixedFont *Font

		errorText         map[string]func() bool
		topControlsHeight float32

		showAboutDialog   bool
		showRadarSettings bool
		showColorEditor   bool
		showFilesEditor   bool
		showServersEditor bool
		showSoundConfig   bool
		showRadioSettings bool

		iconTextureID     uint32
		sadTowerTextureID uint32

		activeModalDialogs []*ModalDialogBox

		openSectorFileDialog   *FileSelectDialogBox
		openPositionFileDialog *FileSelectDialogBox
		openAliasesFileDialog  *FileSelectDialogBox
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

	if ui.errorText == nil {
		ui.errorText = make(map[string]func() bool)
	}

	var err error
	ui.iconTextureID, err = renderer.CreateTextureFromPNG(bytes.NewReader([]byte(iconPNG)))
	if err != nil {
		lg.Errorf("Unable to create icon texture: %v", err)
	}
	ui.sadTowerTextureID, err = renderer.CreateTextureFromPNG(bytes.NewReader([]byte(sadTowerPNG)))
	if err != nil {
		lg.Errorf("Unable to create sad tower icon texture: %v", err)
	}

	if nrc := checkForNewRelease(); nrc != nil {
		uiShowModalDialog(NewModalDialogBox(nrc), false)
	}

	ui.openSectorFileDialog = NewFileSelectDialogBox("Open Sector File...", []string{".sct", ".sct2"},
		globalConfig.SectorFile,
		func(filename string) {
			if err := database.LoadSectorFile(filename); err == nil {
				delete(ui.errorText, "SECTORFILE")
				globalConfig.SectorFile = filename
				database.SetColorScheme(positionConfig.GetColorScheme())

				// This is probably the wrong place to do this, but it's
				// convenient... Walk through the radar scopes and center
				// any that have a (0,0) center according to the position
				// file center. This fixes things up with the default scope
				// on a first run.
				positionConfig.DisplayRoot.VisitPanes(func(p Pane) {
					if rs, ok := p.(*RadarScopePane); ok {
						if rs.Center[0] == 0 && rs.Center[1] == 0 {
							rs.Center = database.defaultCenter
						}
					}
				})
			}
		})
	ui.openPositionFileDialog = NewFileSelectDialogBox("Open Position File...", []string{".pof"},
		globalConfig.PositionFile,
		func(filename string) {
			if err := database.LoadPositionFile(filename); err == nil {
				delete(ui.errorText, "POSITIONFILE")
				globalConfig.PositionFile = filename
			}
		})
	ui.openAliasesFileDialog = NewFileSelectDialogBox("Open Aliases File...", []string{".txt"},
		globalConfig.AliasesFile,
		func(filename string) {
			globalConfig.AliasesFile = filename
			globalConfig.LoadAliasesFile()
		})
	ui.openNotesFileDialog = NewFileSelectDialogBox("Open Notes File...", []string{".txt"},
		globalConfig.NotesFile,
		func(filename string) {
			globalConfig.NotesFile = filename
			globalConfig.LoadNotesFile()
		})
}

// uiAddError lets the caller specify an error message to be displayed
// until the provided callback function returns true.  If called multiple
// times with the same error text, the error will only be displayed once.
// (These error messages are currently shown under the main menu bar.)
func uiAddError(text string, cleared func() bool) {
	if ui.errorText == nil {
		ui.errorText = make(map[string]func() bool)
	}
	ui.errorText[text] = cleared
}

func uiShowModalDialog(d *ModalDialogBox, atFront bool) {
	if atFront {
		ui.activeModalDialogs = append([]*ModalDialogBox{d}, ui.activeModalDialogs...)
	} else {
		ui.activeModalDialogs = append(ui.activeModalDialogs, d)
	}
}

func (c RGB) imgui() imgui.Vec4 {
	return imgui.Vec4{c.R, c.G, c.B, 1}
}

func drawUI(cs *ColorScheme, platform Platform) {
	imgui.PushFont(ui.font.ifont)
	if imgui.BeginMainMenuBar() {
		if imgui.BeginMenu("Connection") {
			if imgui.MenuItemV("Connect...", "", false, !server.Connected()) {
				uiShowModalDialog(NewModalDialogBox(&ConnectModalClient{}), false)
			}
			if imgui.MenuItemV("Disconnect...", "", false, server.Connected()) {
				uiShowModalDialog(NewModalDialogBox(&DisconnectModalClient{}), false)
			}
			imgui.EndMenu()
		}

		if imgui.BeginMenu("Settings") {
			if imgui.MenuItem("Save") {
				if err := globalConfig.Save(); err != nil {
					ShowErrorDialog("Error saving configuration file: %v", err)
				}
			}
			if imgui.MenuItem("Files...") {
				ui.showFilesEditor = true
			}
			if imgui.MenuItem("Servers...") {
				ui.showServersEditor = true
			}
			if imgui.MenuItem("Radar...") {
				ui.showRadarSettings = true
			}
			if imgui.MenuItemV("Radio...", "", false, server.Connected()) {
				ui.showRadioSettings = true
			}
			if imgui.MenuItem("Colors...") {
				ui.showColorEditor = true
			}
			if imgui.MenuItem("Sounds...") {
				ui.showSoundConfig = true
			}
			imgui.EndMenu()
		}

		if imgui.BeginMenu("Configs") {
			if imgui.MenuItem("New...") {
				uiShowModalDialog(NewModalDialogBox(&NewModalClient{isBrandNew: true}), false)
			}
			if imgui.MenuItem("New from current...") {
				uiShowModalDialog(NewModalDialogBox(&NewModalClient{isBrandNew: false}), false)
			}
			if imgui.MenuItem("Rename...") {
				uiShowModalDialog(NewModalDialogBox(&RenameModalClient{}), false)
			}
			if imgui.MenuItemV("Delete...", "", false, len(globalConfig.PositionConfigs) > 1) {
				uiShowModalDialog(NewModalDialogBox(&DeleteModalClient{}), false)
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

	// See if any errors cleared
	for k, cleared := range ui.errorText {
		if cleared() {
			delete(ui.errorText, k)
		}
	}

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
			text += k + "\n"
		}
		text = strings.TrimRight(text, "\n")

		if imgui.BeginV("Errors", nil, flags) {
			imgui.PushStyleColor(imgui.StyleColorText, cs.TextError.imgui())
			imgui.Text(text)
			imgui.PopStyleColor()
			imgui.End()
		}
	}

	for len(ui.activeModalDialogs) > 0 {
		d := ui.activeModalDialogs[0]
		if !d.closed {
			d.Draw()
			break
		} else {
			ui.activeModalDialogs = ui.activeModalDialogs[1:]
		}
	}

	ui.openSectorFileDialog.Draw()
	ui.openPositionFileDialog.Draw()
	ui.openAliasesFileDialog.Draw()
	ui.openNotesFileDialog.Draw()

	if ui.showAboutDialog {
		showAboutDialog()
	}

	if ui.showRadarSettings {
		imgui.BeginV("Radar Settings", &ui.showRadarSettings, imgui.WindowFlagsAlwaysAutoResize)
		positionConfig.DrawRadarUI()
		imgui.End()
	}

	if ui.showRadioSettings {
		imgui.BeginV("Radio Settings", &ui.showRadioSettings, imgui.WindowFlagsAlwaysAutoResize)
		positionConfig.DrawRadioUI()
		imgui.End()
	}

	if ui.showFilesEditor {
		imgui.BeginV("Files", &ui.showFilesEditor, imgui.WindowFlagsAlwaysAutoResize)
		globalConfig.DrawFilesUI()
		imgui.End()
	}

	if ui.showServersEditor {
		imgui.BeginV("Servers", &ui.showServersEditor, imgui.WindowFlagsAlwaysAutoResize)
		globalConfig.DrawServersUI()
		imgui.End()
	}

	if ui.showColorEditor {
		imgui.BeginV("Color Settings", &ui.showColorEditor, imgui.WindowFlagsAlwaysAutoResize)
		showColorEditor()
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

type ComboBoxState struct {
	inputValues  []*string
	selected     map[string]interface{}
	lastSelected *string
}

func NewComboBoxState(nentry int) *ComboBoxState {
	s := &ComboBoxState{}
	for i := 0; i < nentry; i++ {
		s.inputValues = append(s.inputValues, new(string))
	}
	s.selected = make(map[string]interface{})
	s.lastSelected = new(string)
	return s
}

type ComboBoxDisplayConfig struct {
	ColumnHeaders    []string
	DrawHeaders      bool
	EntryNames       []string
	InputFlags       []imgui.InputTextFlags
	TableFlags       imgui.TableFlags
	SelectAllColumns bool
	Size             imgui.Vec2
}

func DrawComboBox(state *ComboBoxState, config ComboBoxDisplayConfig,
	firstColumn []string, drawColumn func(s string, col int),
	inputValid func([]*string) bool, add func([]*string), deleteSelection func(map[string]interface{})) {
	id := fmt.Sprintf("%p", state)
	flags := config.TableFlags | imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV |
		imgui.TableFlagsRowBg
	sz := config.Size
	if sz.X == 0 && sz.Y == 0 {
		sz = imgui.Vec2{300, 100}
	}
	if imgui.BeginTableV("##"+id, len(config.ColumnHeaders), flags, sz, 0.0) {
		for _, name := range config.ColumnHeaders {
			imgui.TableSetupColumn(name)
		}
		if config.DrawHeaders {
			imgui.TableHeadersRow()
		}

		io := imgui.CurrentIO()
		for _, entry := range firstColumn {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			_, isSelected := state.selected[entry]
			var selFlags imgui.SelectableFlags
			if config.SelectAllColumns {
				selFlags = imgui.SelectableFlagsSpanAllColumns
			}
			if imgui.SelectableV(entry, isSelected, selFlags, imgui.Vec2{}) {
				if io.KeyCtrlPressed() {
					// Toggle selection of this one
					if isSelected {
						delete(state.selected, entry)
					} else {
						state.selected[entry] = nil
						*state.lastSelected = entry
					}
				} else if io.KeyShiftPressed() {
					for _, e := range firstColumn {
						if entry > *state.lastSelected {
							if e > *state.lastSelected && e <= entry {
								state.selected[e] = nil
							}
						} else {
							if e >= entry && e <= *state.lastSelected {
								state.selected[e] = nil
							}
						}
					}
					*state.lastSelected = entry
				} else {
					// Select only this one
					for k := range state.selected {
						delete(state.selected, k)
					}
					state.selected[entry] = nil
					*state.lastSelected = entry
				}
			}
			for i := 1; i < len(config.ColumnHeaders); i++ {
				imgui.TableNextColumn()
				drawColumn(entry, i)
			}
		}
		imgui.EndTable()
	}

	valid := inputValid(state.inputValues)
	for i, entry := range config.EntryNames {
		flags := imgui.InputTextFlagsEnterReturnsTrue
		if config.InputFlags != nil {
			flags |= config.InputFlags[i]
		}
		if imgui.InputTextV(entry+"##"+id, state.inputValues[i], flags, nil) && valid {
			add(state.inputValues)
			for _, s := range state.inputValues {
				*s = ""
			}
			imgui.SetKeyboardFocusHereV(-1)
		}
	}

	if !valid {
		imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
		imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
	}
	imgui.SameLine()
	if imgui.Button("+##" + id) {
		add(state.inputValues)
		for _, s := range state.inputValues {
			*s = ""
		}
	}
	if !valid {
		imgui.PopItemFlag()
		imgui.PopStyleVar()
	}

	enableDelete := len(state.selected) > 0
	if !enableDelete {
		imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
		imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
	}
	imgui.SameLine()
	if imgui.Button(FontAwesomeIconTrash + "##" + id) {
		deleteSelection(state.selected)
		for k := range state.selected {
			delete(state.selected, k)
		}
	}
	if !enableDelete {
		imgui.PopItemFlag()
		imgui.PopStyleVar()
	}
}

///////////////////////////////////////////////////////////////////////////

type ModalDialogBox struct {
	closed, isOpen bool
	client         ModalDialogClient
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

func (m *ModalDialogBox) Draw() {
	if m.closed {
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
					m.closed = true
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

type FlightRadarConnectionConfiguration struct{}

func (*FlightRadarConnectionConfiguration) Initialize()  {}
func (*FlightRadarConnectionConfiguration) DrawUI() bool { return false }
func (*FlightRadarConnectionConfiguration) Valid() bool  { return true }

func (*FlightRadarConnectionConfiguration) Connect() error {
	server = NewFlightRadarServer()
	return nil
}

type VATSIMConnectionConfiguration struct {
	name    string
	address string
}

func (v *VATSIMConnectionConfiguration) Initialize() {}

func (v *VATSIMConnectionConfiguration) DrawUI() bool {
	imgui.InputText("Name", &globalConfig.VatsimName)

	cidFlags := imgui.InputTextFlagsCallbackCharFilter
	imgui.InputTextV("VATSIM CID", &globalConfig.VatsimCID, cidFlags,
		func(cb imgui.InputTextCallbackData) int32 {
			switch cb.EventChar() {
			case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
				return 0
			default:
				return 1
			}
		})

	imgui.InputTextV("VATSIM Password", &globalConfig.VatsimPassword, imgui.InputTextFlagsPassword, nil)

	if imgui.BeginCombo("Rating", globalConfig.VatsimRating.String()) {
		for i := ObserverRating; i <= AdministratorRating; i++ {
			nr := NetworkRating(i)
			s := nr.String()
			if imgui.SelectableV(s, nr == globalConfig.VatsimRating, 0, imgui.Vec2{}) {
				globalConfig.VatsimRating = nr
			}
		}
		imgui.EndCombo()
	}

	imgui.InputText("Callsign", &positionConfig.VatsimCallsign)

	if imgui.BeginCombo("Facility", positionConfig.VatsimFacility.String()) {
		for i := FacilityOBS; i <= FacilityUndefined; i++ {
			f := Facility(i)
			s := f.String()
			if imgui.SelectableV(s, f == positionConfig.VatsimFacility, 0, imgui.Vec2{}) {
				positionConfig.VatsimFacility = f
			}
		}
		imgui.EndCombo()
	}

	if imgui.BeginCombo("Server", v.name) {
		for _, server := range SortedMapKeys(vatsimServers) {
			if imgui.SelectableV(server, server == v.name, 0, imgui.Vec2{}) {
				v.name = server
				v.address = vatsimServers[server]
			}
		}
		for _, server := range SortedMapKeys(globalConfig.CustomServers) {
			if imgui.SelectableV(server, server == v.name, 0, imgui.Vec2{}) {
				v.name = server
				v.address = globalConfig.CustomServers[server]
			}
		}
		imgui.EndCombo()
	}

	return false
}

func (v *VATSIMConnectionConfiguration) Valid() bool { return v.address != "" }

func (v *VATSIMConnectionConfiguration) Connect() error {
	var err error
	server, err = NewVATSIMNetworkServer(v.address)
	return err
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

func (v *VATSIMReplayConfiguration) Connect() error {
	var err error
	server, err = NewVATSIMReplayServer(v.filename, int(v.offset), v.rate)
	return err
}

type ConnectModalClient struct {
	connectionType ConnectionType
	err            string

	// Store all three so that if the user switches back and forth any set
	// values are retained.
	vatsim       VATSIMConnectionConfiguration
	vatsimReplay VATSIMReplayConfiguration
	flightRadar  FlightRadarConnectionConfiguration
}

type ConnectionType int

const (
	ConnectionTypeVATSIM = iota
	ConnectionTypeVATSIMReplay
	ConnectionTypeFlightRadar
	ConnectionTypeCount
)

func (c ConnectionType) String() string {
	return [...]string{"VATSIM Network", "VATSIM Replay", "Flight Radar"}[c]
}

func (c *ConnectModalClient) Title() string { return "New Connection" }

func (c *ConnectModalClient) Opening() {
	c.err = ""
	c.vatsim.Initialize()
	c.vatsimReplay.Initialize()
	c.flightRadar.Initialize()
}

func (c *ConnectModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		var err error
		switch c.connectionType {
		case ConnectionTypeFlightRadar:
			err = c.flightRadar.Connect()

		case ConnectionTypeVATSIM:
			err = c.vatsim.Connect()

		case ConnectionTypeVATSIMReplay:
			err = c.vatsimReplay.Connect()

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

type DisconnectModalClient struct{}

func (d *DisconnectModalClient) Title() string { return "Confirm Disconnection" }

func (d *DisconnectModalClient) Opening() {
}

func (c *DisconnectModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		server.Disconnect()
		server = &DisconnectedATCServer{}
		return true
	}}
	b = append(b, ok)

	return b
}

func (d *DisconnectModalClient) Draw() int {
	imgui.Text("Are you sure you want to disconnect?")
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
		if yn.notok != nil {
			yn.notok()
		}
		return true
	}})
	b = append(b, ModalDialogButton{text: "Yes", action: func() bool {
		if yn.ok != nil {
			yn.ok()
		}
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

	dirEntries            []DirEntry
	dirEntriesLastUpdated time.Time

	title    string
	filter   []string
	callback func(string)
}

type DirEntry struct {
	name  string
	isDir bool
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
			if dirEntries, err := os.ReadDir(fs.directory); err != nil {
				lg.Errorf("%s: unable to read directory: %v", fs.directory, err)
			} else {
				fs.dirEntries = nil
				for _, entry := range dirEntries {
					if entry.Type()&os.ModeSymlink != 0 {
						info, err := os.Stat(path.Join(fs.directory, entry.Name()))
						if err == nil {
							e := DirEntry{name: entry.Name(), isDir: info.IsDir()}
							fs.dirEntries = append(fs.dirEntries, e)
						} else {
							e := DirEntry{name: entry.Name(), isDir: false}
							fs.dirEntries = append(fs.dirEntries, e)
						}
					} else {
						e := DirEntry{name: entry.Name(), isDir: entry.IsDir()}
						fs.dirEntries = append(fs.dirEntries, e)
					}
				}
				sort.Slice(fs.dirEntries, func(i, j int) bool {
					return fs.dirEntries[i].name < fs.dirEntries[j].name
				})
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
				if entry.isDir {
					icon = FontAwesomeIconFolder
				} else {
					icon = FontAwesomeIconFile
				}

				canSelect := entry.isDir
				if !entry.isDir {
					for _, f := range fs.filter {
						if strings.HasSuffix(strings.ToUpper(entry.name), strings.ToUpper(f)) {
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
				if imgui.SelectableV(icon+" "+entry.name, entry.name == fs.filename, selFlags, imgui.Vec2{}) {
					fs.filename = entry.name
				}
				if imgui.IsItemHovered() && imgui.IsMouseDoubleClicked(0) {
					if entry.isDir {
						fs.directory = path.Join(fs.directory, entry.name)
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
	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)})
	uiShowModalDialog(d, true)

	lg.ErrorfUp1(s, args...)
}

func ShowFatalErrorDialog(s string, args ...interface{}) {
	lg.ErrorfUp1(s, args...)

	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)})

	for !d.closed {
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

///////////////////////////////////////////////////////////////////////////
// ScrollBar

type ScrollBar struct {
	offset            int
	barWidth          int
	nItems, nVisible  int
	accumDrag         float32
	invertY           bool
	mouseClickedInBar bool
}

func NewScrollBar(width int, invertY bool) *ScrollBar {
	return &ScrollBar{barWidth: width, invertY: invertY}
}

func (sb *ScrollBar) Update(nItems int, nVisible int, ctx *PaneContext) {
	sb.nItems = nItems
	sb.nVisible = nVisible

	if sb.nItems > sb.nVisible {
		sign := float32(1)
		if sb.invertY {
			sign = -1
		}

		if ctx.mouse != nil {
			sb.offset += int(sign * ctx.mouse.wheel[1])

			if ctx.mouse.clicked[0] {
				sb.mouseClickedInBar = ctx.mouse.pos[0] >= ctx.paneExtent.Width()-float32(sb.Width())
				sb.accumDrag = 0
			}

			if ctx.mouse.dragging[0] && sb.mouseClickedInBar {
				sb.accumDrag += -sign * ctx.mouse.dragDelta[1] * float32(sb.nItems) / ctx.paneExtent.Height()
				if fabs(sb.accumDrag) >= 1 {
					sb.offset += int(sb.accumDrag)
					sb.accumDrag -= float32(int(sb.accumDrag))
				}
			}
		}
		sb.offset = clamp(sb.offset, 0, sb.nItems-sb.nVisible)
	} else {
		sb.offset = 0
	}
}

func (sb *ScrollBar) Offset() int {
	return sb.offset
}

func (sb *ScrollBar) Visible() bool {
	return sb.nItems > sb.nVisible
}

func (sb *ScrollBar) Draw(ctx *PaneContext, cb *CommandBuffer) {
	if !sb.Visible() {
		return
	}

	pw, ph := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	// The visible region is [offset,offset+nVisible].
	// Visible region w.r.t. [0,1]
	y0, y1 := float32(sb.offset)/float32(sb.nItems), float32(sb.offset+sb.nVisible)/float32(sb.nItems)
	if sb.invertY {
		y0, y1 = 1-y0, 1-y1
	}
	// Visible region in window coordinates
	const edgeSpace = 2
	wy0, wy1 := lerp(y0, ph-edgeSpace, edgeSpace), lerp(y1, ph-edgeSpace, edgeSpace)

	quad := TrianglesDrawBuilder{}
	quad.AddQuad([2]float32{pw - float32(sb.barWidth) - float32(edgeSpace), wy0},
		[2]float32{pw - float32(edgeSpace), wy0},
		[2]float32{pw - float32(edgeSpace), wy1},
		[2]float32{pw - float32(sb.barWidth) - float32(edgeSpace), wy1})
	cb.SetRGB(ctx.cs.Scrollbar)
	quad.GenerateCommands(cb)
}

func (sb *ScrollBar) Width() int {
	return sb.barWidth + 4 /* for edge space... */
}

///////////////////////////////////////////////////////////////////////////
// ColorScheme

type ColorScheme struct {
	Background RGB
	SplitLine  RGB
	Scrollbar  RGB

	Text          RGB
	TextHighlight RGB
	TextError     RGB

	Safe    RGB
	Caution RGB
	Error   RGB

	// Datablock colors
	SelectedDataBlock   RGB
	UntrackedDataBlock  RGB
	TrackedDataBlock    RGB
	HandingOffDataBlock RGB
	GhostDataBlock      RGB
	Track               RGB

	FlightStripText RGB
	ArrivalStrip    RGB
	DepartureStrip  RGB

	Airport    RGB
	VOR        RGB
	NDB        RGB
	Fix        RGB
	Runway     RGB
	Region     RGB
	SID        RGB
	STAR       RGB
	Geo        RGB
	ARTCC      RGB
	LowAirway  RGB
	HighAirway RGB
	Compass    RGB

	DefinedColors map[string]*RGB
}

func (c *ColorScheme) IsDark() bool {
	luminance := 0.2126*c.Background.R + 0.7152*c.Background.G + 0.0722*c.Background.B
	return luminance < 0.35 // ad hoc..
}

func (r *RGB) DrawUI(title string) bool {
	ptr := (*[3]float32)(unsafe.Pointer(r))
	flags := imgui.ColorEditFlagsNoAlpha | imgui.ColorEditFlagsNoInputs |
		imgui.ColorEditFlagsRGB | imgui.ColorEditFlagsInputRGB
	return imgui.ColorEdit3V(title, ptr, flags)
}

func (c *ColorScheme) ShowEditor(handleDefinedColorChange func(string, RGB)) {
	edit := func(uiName string, internalName string, c *RGB) {
		// It's slightly grungy to hide this in here but it does simplify
		// the code below...
		imgui.TableNextColumn()
		if c.DrawUI(uiName) {
			handleDefinedColorChange(internalName, *c)
		}
	}

	names := SortedMapKeys(c.DefinedColors)
	sfdIndex := 0
	sfd := func() {
		if len(names) == 0 {
			return
		}
		if sfdIndex < len(names) {
			edit(names[sfdIndex], names[sfdIndex], c.DefinedColors[names[sfdIndex]])
			sfdIndex++
		} else {
			imgui.TableNextColumn()
		}
	}

	nCols := 4
	if len(names) == 0 {
		nCols--
	}
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg
	if imgui.BeginTableV(fmt.Sprintf("ColorEditor##%p", c), nCols, flags, imgui.Vec2{}, 0.0) {
		// TODO: it would be nice to do this in a more data-driven way--to at least just
		// be laying out the table by iterating over slices...
		imgui.TableSetupColumn("General")
		imgui.TableSetupColumn("Aircraft")
		imgui.TableSetupColumn("Radar scope")
		if nCols == 4 {
			imgui.TableSetupColumn("Sector file definitions")
		}
		imgui.TableHeadersRow()

		imgui.TableNextRow()
		edit("Background", "Background", &c.Background)
		edit("Track", "Track", &c.Track)
		edit("Airport", "Airport", &c.Airport)
		sfd()

		imgui.TableNextRow()
		edit("Text", "Text", &c.Text)
		edit("Tracked data block", "Tracked Data Block", &c.TrackedDataBlock)
		edit("ARTCC", "ARTCC", &c.ARTCC)
		sfd()

		imgui.TableNextRow()
		edit("Highlighted text", "TextHighlight", &c.TextHighlight)
		edit("Selected data block", "Selected Data Block", &c.SelectedDataBlock)
		edit("Compass", "Compass", &c.Compass)
		sfd()

		imgui.TableNextRow()
		edit("Error text", "TextError", &c.TextError)
		edit("Handing-off data block", "HandingOff Data Block", &c.HandingOffDataBlock)
		edit("Fix", "Fix", &c.Fix)
		sfd()

		imgui.TableNextRow()
		edit("Split line", "Split Line", &c.SplitLine)
		edit("Untracked data block", "Untracked Data Block", &c.UntrackedDataBlock)
		edit("Geo", "Geo", &c.Geo)
		sfd()

		imgui.TableNextRow()
		edit("Scrollbar", "Scrollbar", &c.Scrollbar)
		edit("Ghost data block", "Ghost Data Block", &c.GhostDataBlock)
		edit("High airway", "HighAirway", &c.HighAirway)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Safe situation", "Safe", &c.Safe)
		edit("Low airway", "LowAirway", &c.LowAirway)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Caution situation", "Caution", &c.Caution)
		edit("NDB", "NDB", &c.NDB)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Error situation", "Error", &c.Error)
		edit("Region", "Region", &c.Region)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Flight strip text", "Flight strip text", &c.FlightStripText)
		edit("Runway", "Runway", &c.Runway)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Departure flight strip", "Departure flight strip", &c.DepartureStrip)
		edit("SID", "SID", &c.SID)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		edit("Arrival flight strip", "Arrival flight strip", &c.ArrivalStrip)
		edit("STAR", "STAR", &c.STAR)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.TableNextColumn()
		edit("VOR", "VOR", &c.VOR)
		sfd()

		for sfdIndex < len(names) {
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			imgui.TableNextColumn()
			sfd()
		}

		imgui.EndTable()
	}
}

var builtinColorSchemes map[string]*ColorScheme = map[string]*ColorScheme{
	"Dark (builtin)": &ColorScheme{
		Background:          RGB{R: 0, G: 0, B: 0},
		SplitLine:           RGB{R: 0.36120403, G: 0.36120403, B: 0.36120403},
		Scrollbar:           RGB{R: 0.65254235, G: 0.65254235, B: 0.65254235},
		Text:                RGB{R: 0.120606266, G: 0.8592058, B: 0.07444384},
		TextHighlight:       RGB{R: 0.8997721, G: 0.90654206, B: 0.18215565},
		TextError:           RGB{R: 0.91525424, G: 0.069807515, B: 0.069807515},
		Safe:                RGB{R: 0.13225771, G: 0.5635748, B: 0.8519856},
		Caution:             RGB{R: 0.8592058, G: 0.85245013, B: 0.062036503},
		Error:               RGB{R: 1, G: 0, B: 0},
		SelectedDataBlock:   RGB{R: 0.9133574, G: 0.9111314, B: 0.2967587},
		UntrackedDataBlock:  RGB{R: 0.21454205, G: 0.94584835, B: 0.11609689},
		TrackedDataBlock:    RGB{R: 0.44499192, G: 0.9491525, B: 0.2573972},
		HandingOffDataBlock: RGB{R: 0.7689531, G: 0.12214418, B: 0.26224726},
		GhostDataBlock:      RGB{R: 0.5090253, G: 0.5090253, B: 0.5090253},
		Track:               RGB{R: 0, G: 1, B: 0.084745646},
		FlightStripText:     RGB{R: 0.9237288, G: 0.9237288, B: 0.9237288},
		ArrivalStrip:        RGB{R: 0.05057959, G: 0.047579724, B: 0.2245763},
		DepartureStrip:      RGB{R: 0.14406782, G: 0.04334244, B: 0.04334244},
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
	},
	"Light (builtin)": &ColorScheme{
		Background:          RGB{R: 1, G: 1, B: 1},
		SplitLine:           RGB{R: 0.8245098, G: 0.8250796, B: 0.90969896},
		Scrollbar:           RGB{R: 0, G: 0, B: 0},
		Text:                RGB{R: 0.107210346, G: 0.035419118, B: 0.65686274},
		TextHighlight:       RGB{R: 0.5127119, G: 0.48472703, B: 0.18249066},
		TextError:           RGB{R: 0.88559324, G: 0.16886309, B: 0.16886309},
		Safe:                RGB{R: 0.5117057, G: 0.5247704, B: 1},
		Caution:             RGB{R: 0.8601695, G: 0.6032181, B: 0.14214665},
		Error:               RGB{R: 1, G: 0, B: 0},
		SelectedDataBlock:   RGB{R: 0.63176894, G: 0.6059726, B: 0.08210714},
		UntrackedDataBlock:  RGB{R: 0.15045157, G: 0.21625589, B: 0.80144405},
		TrackedDataBlock:    RGB{R: 0.32058924, G: 0.8231047, B: 0.24069126},
		HandingOffDataBlock: RGB{R: 0.8267148, G: 0.1790718, B: 0.1790718},
		GhostDataBlock:      RGB{R: 0.44404334, G: 0.44404334, B: 0.44404334},
		Track:               RGB{R: 0.37458193, G: 0.37458193, B: 0.37458193},
		FlightStripText:     RGB{R: 0, G: 0, B: 0},
		ArrivalStrip:        RGB{R: 0.73613906, G: 0.75123316, B: 0.84745765},
		DepartureStrip:      RGB{R: 0.84745765, G: 0.7002298, B: 0.7002298},
		Airport:             RGB{R: 0.627451, G: 0.627451, B: 0.627451},
		VOR:                 RGB{R: 0.627451, G: 0.627451, B: 0.627451},
		NDB:                 RGB{R: 0.627451, G: 0.627451, B: 0.627451},
		Fix:                 RGB{R: 0.627451, G: 0.627451, B: 0.627451},
		Runway:              RGB{R: 0.8, G: 0.8, B: 0.4},
		Region:              RGB{R: 0.691375, G: 0.7966102, B: 0.6177105},
		SID:                 RGB{R: 0.6694915, G: 0.54997474, B: 0.5077923},
		STAR:                RGB{R: 0.4755817, G: 0.65254235, B: 0.48807308},
		Geo:                 RGB{R: 0.38559324, G: 0.38559324, B: 0.38559324},
		ARTCC:               RGB{R: 0.7, G: 0.7, B: 0.7},
		LowAirway:           RGB{R: 0.5, G: 0.5, B: 0.5},
		HighAirway:          RGB{R: 0.5, G: 0.5, B: 0.5},
		Compass:             RGB{R: 0.279661, G: 0.279661, B: 0.279661},
	},
}

func colorSchemeExists(n string) bool {
	_, ok := builtinColorSchemes[n]
	if ok {
		return true
	}
	_, ok = globalConfig.ColorSchemes[n]
	return ok
}

type NewColorSchemeModalClient struct {
	name string
	err  string
}

func (n *NewColorSchemeModalClient) Title() string { return "New Color Scheme" }

func (n *NewColorSchemeModalClient) Opening() {
	n.name = ""
	n.err = ""
}

func (n *NewColorSchemeModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		dupe := *positionConfig.GetColorScheme()
		dupe.DefinedColors = make(map[string]*RGB)
		for name, color := range database.sectorFileColors {
			c := color
			dupe.DefinedColors[name] = &c
		}

		globalConfig.ColorSchemes[n.name] = &dupe
		positionConfig.ColorSchemeName = n.name
		globalConfig.MakeConfigActive(globalConfig.ActivePosition)

		return true
	}}
	ok.disabled = n.name == ""
	if colorSchemeExists(n.name) {
		ok.disabled = true
		n.err = "\"" + n.name + "\" already exists"
	} else {
		n.err = ""
	}
	b = append(b, ok)

	return b
}

func (n *NewColorSchemeModalClient) Draw() int {
	flags := imgui.InputTextFlagsEnterReturnsTrue
	enter := imgui.InputTextV("Color scheme name", &n.name, flags, nil)
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

type RenameColorSchemeModalClient struct {
	newName string
}

func (r *RenameColorSchemeModalClient) Title() string { return "Rename Color Scheme" }

func (r *RenameColorSchemeModalClient) Opening() {
	r.newName = ""
}

func (r *RenameColorSchemeModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Cancel"})

	ok := ModalDialogButton{text: "Ok", action: func() bool {
		oldName := positionConfig.ColorSchemeName
		positionConfig.ColorSchemeName = r.newName
		cs := globalConfig.ColorSchemes[oldName]
		delete(globalConfig.ColorSchemes, oldName)
		globalConfig.ColorSchemes[r.newName] = cs
		return true
	}}

	// Disable "ok" if a new name hasn't been entered or if it's the same
	// as an existing one.
	ok.disabled = r.newName == "" || colorSchemeExists(r.newName)
	b = append(b, ok)

	return b
}

func (r *RenameColorSchemeModalClient) Draw() int {
	flags := imgui.InputTextFlagsEnterReturnsTrue
	enter := imgui.InputTextV("New name", &r.newName, flags, nil)

	if colorSchemeExists(r.newName) {
		color := positionConfig.GetColorScheme().TextError
		imgui.PushStyleColor(imgui.StyleColorText, color.imgui())
		imgui.Text("Color scheme with that name already exits!")
		imgui.PopStyleColor()
	}
	if enter {
		return 1
	} else {
		return -1
	}
}

func showColorEditor() {
	displayName := func(n string) string {
		if _, ok := builtinColorSchemes[n]; ok {
			return FontAwesomeIconLock + " " + n
		}
		return n
	}

	if imgui.BeginCombo("Color scheme", displayName(positionConfig.ColorSchemeName)) {
		names := SortedMapKeys(builtinColorSchemes)
		names = append(names, SortedMapKeys(globalConfig.ColorSchemes)...)

		for _, name := range names {
			flags := imgui.SelectableFlagsNone
			if imgui.SelectableV(displayName(name), name == positionConfig.ColorSchemeName, flags, imgui.Vec2{}) &&
				name != positionConfig.ColorSchemeName {
				positionConfig.ColorSchemeName = name

				// This is slightly wasteful (e.g., resets the DrawList allocations),
				// but ensures that all of the panes get the new colors.
				globalConfig.MakeConfigActive(globalConfig.ActivePosition)
			}
		}
		imgui.EndCombo()
	}

	_, canEdit := globalConfig.ColorSchemes[positionConfig.ColorSchemeName]

	if imgui.Button("Copy...") {
		uiShowModalDialog(NewModalDialogBox(&NewColorSchemeModalClient{}), false)
	}
	if canEdit {
		imgui.SameLine()
		if imgui.Button("Rename...") {
			uiShowModalDialog(NewModalDialogBox(&RenameColorSchemeModalClient{}), false)
		}
		imgui.SameLine()
		if imgui.Button("Delete...") {
			cur := positionConfig.ColorSchemeName
			uiShowModalDialog(NewModalDialogBox(&YesOrNoModalClient{
				title: "Delete current color scheme?",
				query: fmt.Sprintf("Are you sure you want to delete the \"%s\" color scheme?", cur),
				ok: func() {
					delete(globalConfig.ColorSchemes, cur)
					positionConfig.ColorSchemeName = SortedMapKeys(builtinColorSchemes)[0]
					globalConfig.MakeConfigActive(globalConfig.ActivePosition)
				},
			}), false)
		}
	}

	// Disable editing the builtin color schemes
	if !canEdit {
		imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
		imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
	}

	cs := positionConfig.GetColorScheme()
	cs.ShowEditor(func(name string, rgb RGB) {
		if positionConfig.GetColorScheme().IsDark() {
			imgui.StyleColorsDark()
		} else {
			imgui.StyleColorsLight()
		}
		database.NamedColorChanged(name, rgb)
	})

	if !canEdit {
		imgui.PopItemFlag()
		imgui.PopStyleVar()
	}
}
