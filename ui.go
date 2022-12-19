// ui.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image/png"
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

		errorText     map[string]func() bool
		menuBarHeight float32

		showAboutDialog   bool
		showRadarSettings bool
		showFKeySettings  bool
		showATISSettings  bool
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

	if iconImage, err := png.Decode(bytes.NewReader([]byte(iconPNG))); err != nil {
		lg.Errorf("Unable to decode icon PNG: %v", err)
	} else {
		ui.iconTextureID = renderer.CreateTextureFromImage(iconImage, false)
	}

	if sadTowerImage, err := png.Decode(bytes.NewReader([]byte(sadTowerPNG))); err != nil {
		lg.Errorf("Unable to decode sad tower PNG: %v", err)
	} else {
		ui.sadTowerTextureID = renderer.CreateTextureFromImage(sadTowerImage, false)
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

// If |b| is true, all following imgui elements will be disabled (and drawn
// accordingly).
func uiStartDisable(b bool) {
	if b {
		imgui.PushItemFlag(imgui.ItemFlagsDisabled, true)
		imgui.PushStyleVarFloat(imgui.StyleVarAlpha, imgui.CurrentStyle().Alpha()*0.5)
	}
}

// Each call to uiStartDisable should have a matching call to uiEndDisable,
// with the same Boolean value passed to it.
func uiEndDisable(b bool) {
	if b {
		imgui.PopItemFlag()
		imgui.PopStyleVar()
	}
}

func (c RGB) imgui() imgui.Vec4 {
	return imgui.Vec4{c.R, c.G, c.B, 1}
}

func (c RGBA) imgui() imgui.Vec4 {
	return imgui.Vec4{c.R, c.G, c.B, c.A}
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
			if imgui.MenuItem("Function Keys...") {
				ui.showFKeySettings = true
			}
			if imgui.MenuItem("Controller ATIS...") {
				ui.showATISSettings = true
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
	ui.menuBarHeight = imgui.CursorPos().Y - 1

	drawActiveDialogBoxes()
	drawActiveSettingsWindows()

	wmDrawUI(platform)

	imgui.PopFont()

	// Finalize and submit the imgui draw lists
	imgui.Render()
	cb := GetCommandBuffer()
	defer ReturnCommandBuffer(cb)
	GenerateImguiCommandBuffer(cb)
	stats.renderUI = renderer.RenderCommandBuffer(cb)
}

func drawActiveDialogBoxes() {
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
}

func drawActiveSettingsWindows() {
	if ui.showRadarSettings {
		imgui.BeginV("Radar Settings", &ui.showRadarSettings, imgui.WindowFlagsAlwaysAutoResize)
		positionConfig.DrawRadarUI()
		imgui.End()
	}

	if ui.showFKeySettings {
		imgui.BeginV("Function Key Settings", &ui.showFKeySettings, imgui.WindowFlagsAlwaysAutoResize)

		commands := SortedMapKeys(allFKeyCommands)

		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg
		if imgui.BeginTableV("SpecialKeys", 4, flags, imgui.Vec2{}, 0.0) {
			imgui.TableSetupColumn("Key")
			imgui.TableSetupColumnV("Command", imgui.TableColumnFlagsWidthFixed, 250, 0)
			imgui.TableSetupColumn("Key##Shift")
			imgui.TableSetupColumnV("Command##Shift", imgui.TableColumnFlagsWidthFixed, 250, 0)

			imgui.TableHeadersRow()
			for i := 1; i <= 12; i++ {
				imgui.TableNextRow()

				key := fmt.Sprintf("F%d", i)
				imgui.TableNextColumn()
				imgui.Text(key)
				imgui.TableNextColumn()
				imgui.SetNextItemWidth(240)
				if imgui.BeginComboV("##"+key, positionConfig.FKeyMappings[i], imgui.ComboFlagsHeightLarge) {
					for _, cmd := range commands {
						if imgui.SelectableV(cmd, cmd == positionConfig.FKeyMappings[i], 0, imgui.Vec2{}) {
							positionConfig.FKeyMappings[i] = cmd
						}
					}
					imgui.EndCombo()
				}
				imgui.TableNextColumn()
				imgui.Text("Shift-" + key)
				imgui.TableNextColumn()
				imgui.SetNextItemWidth(240)
				if imgui.BeginComboV("##Shift-"+key, positionConfig.ShiftFKeyMappings[i], imgui.ComboFlagsHeightLarge) {
					for _, cmd := range commands {
						if imgui.SelectableV(cmd, cmd == positionConfig.ShiftFKeyMappings[i], 0, imgui.Vec2{}) {
							positionConfig.ShiftFKeyMappings[i] = cmd
						}
					}
					imgui.EndCombo()
				}
			}

			imgui.EndTable()
		}
		imgui.End()
	}

	if ui.showATISSettings {
		imgui.BeginV("ATIS Settings", &ui.showATISSettings, imgui.WindowFlagsAlwaysAutoResize)
		imgui.InputTextMultiline("Controller ATIS", &positionConfig.ControllerATIS)
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
	SelectAllColumns bool
	Size             imgui.Vec2
	MaxDisplayed     int
	FixedDisplayed   int
}

func DrawComboBox(state *ComboBoxState, config ComboBoxDisplayConfig,
	firstColumn []string, drawColumn func(s string, col int),
	inputValid func([]*string) bool, add func([]*string), deleteSelection func(map[string]interface{})) {
	id := fmt.Sprintf("%p", state)
	flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg

	sz := config.Size
	if config.FixedDisplayed != 0 {
		flags = flags | imgui.TableFlagsScrollY
		sz.Y = float32(config.FixedDisplayed * (4 + ui.font.size))
	} else if config.MaxDisplayed == 0 || len(firstColumn) < config.MaxDisplayed {
		sz.Y = 0
	} else {
		flags = flags | imgui.TableFlagsScrollY
		sz.Y = float32((1 + config.MaxDisplayed) * (6 + ui.font.size))
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

	uiStartDisable(!valid)
	imgui.SameLine()
	if imgui.Button("+##" + id) {
		add(state.inputValues)
		for _, s := range state.inputValues {
			*s = ""
		}
	}
	uiEndDisable(!valid)

	enableDelete := len(state.selected) > 0
	uiStartDisable(!enableDelete)
	imgui.SameLine()
	if imgui.Button(FontAwesomeIconTrash + "##" + id) {
		deleteSelection(state.selected)
		for k := range state.selected {
			delete(state.selected, k)
		}
	}
	uiEndDisable(!enableDelete)
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
			uiStartDisable(b.disabled)
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
			uiEndDisable(b.disabled)
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

func (v *VATSIMConnectionConfiguration) Initialize() {
	if addr, ok := vatsimServers[globalConfig.LastServer]; ok {
		v.name = globalConfig.LastServer
		v.address = addr
	} else if addr, ok := globalConfig.CustomServers[globalConfig.LastServer]; ok {
		v.name = globalConfig.LastServer
		v.address = addr
	}
}

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

	if imgui.BeginComboV("Rating", globalConfig.VatsimRating.String(), imgui.ComboFlagsHeightLarge) {
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

	if imgui.BeginComboV("Facility", positionConfig.VatsimFacility.String(), imgui.ComboFlagsHeightLarge) {
		for i := FacilityOBS; i <= FacilityUndefined; i++ {
			f := Facility(i)
			s := f.String()
			if imgui.SelectableV(s, f == positionConfig.VatsimFacility, 0, imgui.Vec2{}) {
				positionConfig.VatsimFacility = f
			}
		}
		imgui.EndCombo()
	}

	imgui.InputTextMultiline("Controller ATIS", &positionConfig.ControllerATIS)

	if imgui.BeginComboV("Server", v.name, imgui.ComboFlagsHeightLarge) {
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
	if err == nil {
		globalConfig.LastServer = v.name
	}
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
	if imgui.BeginComboV("Type", c.connectionType.String(), imgui.ComboFlagsHeightLarge) {
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
	if imgui.BeginComboV("Config Name", r.selectedName, imgui.ComboFlagsHeightLarge) {
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
	if imgui.BeginComboV("Config Name", d.name, imgui.ComboFlagsHeightLarge) {
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

				uiStartDisable(!canSelect)
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
				uiEndDisable(!canSelect)
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
		uiStartDisable(disableOk)
		imgui.SameLine()
		if imgui.Button("Ok") || fileSelected {
			imgui.CloseCurrentPopup()
			fs.show = false
			fs.isOpen = false
			fs.callback(path.Join(fs.directory, fs.filename))
			fs.filename = ""
		}
		uiEndDisable(disableOk)

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
		var cb CommandBuffer
		GenerateImguiCommandBuffer(&cb)
		renderer.RenderCommandBuffer(&cb)

		platform.PostRender()
	}
}

///////////////////////////////////////////////////////////////////////////
// ScrollBar

// ScrollBar provides functionality for a basic scrollbar for use in Pane
// implementations.  (Since those are not handled by imgui, we can't use
// imgui's scrollbar there.)
type ScrollBar struct {
	offset            int
	barWidth          int
	nItems, nVisible  int
	accumDrag         float32
	invertY           bool
	mouseClickedInBar bool
}

// NewScrollBar returns a new ScrollBar instance with the given width.
// invertY indicates whether the scrolled items are drawn from the bottom
// of the Pane or the top; invertY should be true if they are being drawn
// from the bottom.
func NewScrollBar(width int, invertY bool) *ScrollBar {
	return &ScrollBar{barWidth: width, invertY: invertY}
}

// Update should be called once per frame, providing the total number of things
// being drawn, the number of them that are visible, and the PaneContext passed
// to the Pane's Draw method (so that mouse events can be handled, if appropriate.
func (sb *ScrollBar) Update(nItems int, nVisible int, ctx *PaneContext) {
	sb.nItems = nItems
	sb.nVisible = nVisible

	if sb.nItems > sb.nVisible {
		sign := float32(1)
		if sb.invertY {
			sign = -1
		}

		if ctx.mouse != nil {
			sb.offset += int(sign * ctx.mouse.Wheel[1])

			if ctx.mouse.Clicked[0] {
				sb.mouseClickedInBar = ctx.mouse.Pos[0] >= ctx.paneExtent.Width()-float32(sb.Width())
				sb.accumDrag = 0
			}

			if ctx.mouse.Dragging[0] && sb.mouseClickedInBar {
				sb.accumDrag += -sign * ctx.mouse.DragDelta[1] * float32(sb.nItems) / ctx.paneExtent.Height()
				if abs(sb.accumDrag) >= 1 {
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

// Offset returns the offset into the items at which drawing should start
// (i.e., the items before the offset are offscreen.)  Note that the scroll
// offset is reported in units of the number of items passed to Update;
// thus, if scrolling text, the number of items might be measured in lines
// of text, or it might be measured in scanlines.  The choice determines
// whether scrolling happens at the granularity of entire lines at a time
// or is continuous.
func (sb *ScrollBar) Offset() int {
	return sb.offset
}

// Visible indicates whether the scrollbar will be drawn (it disappears if
// all of the items can fit onscreen.)
func (sb *ScrollBar) Visible() bool {
	return sb.nItems > sb.nVisible
}

// Draw emits the drawing commands for the scrollbar into the provided
// CommandBuffer.
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

	quad := GetColoredTrianglesDrawBuilder()
	defer ReturnColoredTrianglesDrawBuilder(quad)
	quad.AddQuad([2]float32{pw - float32(sb.barWidth) - float32(edgeSpace), wy0},
		[2]float32{pw - float32(edgeSpace), wy0},
		[2]float32{pw - float32(edgeSpace), wy1},
		[2]float32{pw - float32(sb.barWidth) - float32(edgeSpace), wy1}, ctx.cs.UIControl)
	quad.GenerateCommands(cb)
}

func (sb *ScrollBar) Width() int {
	return sb.barWidth + 4 /* for edge space... */
}

///////////////////////////////////////////////////////////////////////////
// Text editing

const (
	TextEditReturnNone = iota
	TextEditReturnTextChanged
	TextEditReturnEnter
	TextEditReturnNext
	TextEditReturnPrev
)

// uiDrawTextEdit handles the basics of interactive text editing; it takes
// a string and cursor position and then renders them with the specified
// style, processes keyboard inputs and updates the string accordingly.
func uiDrawTextEdit(s *string, cursor *int, keyboard *KeyboardState, pos [2]float32, style,
	cursorStyle TextStyle, cb *CommandBuffer) (exit int, posOut [2]float32) {
	// Make sure we can depend on it being sensible for the following
	*cursor = clamp(*cursor, 0, len(*s))
	originalText := *s

	// Draw the text and the cursor
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	if *cursor == len(*s) {
		// cursor at the end
		posOut = td.AddTextMulti([]string{*s, " "}, pos, []TextStyle{style, cursorStyle})
	} else {
		// cursor in the middle
		sb, sc, se := (*s)[:*cursor], (*s)[*cursor:*cursor+1], (*s)[*cursor+1:]
		styles := []TextStyle{style, cursorStyle, style}
		posOut = td.AddTextMulti([]string{sb, sc, se}, pos, styles)
	}
	td.GenerateCommands(cb)

	// Handle various special keys.
	if keyboard != nil {
		if keyboard.IsPressed(KeyBackspace) && *cursor > 0 {
			*s = (*s)[:*cursor-1] + (*s)[*cursor:]
			*cursor--
		}
		if keyboard.IsPressed(KeyDelete) && *cursor < len(*s)-1 {
			*s = (*s)[:*cursor] + (*s)[*cursor+1:]
		}
		if keyboard.IsPressed(KeyLeftArrow) {
			*cursor = max(*cursor-1, 0)
		}
		if keyboard.IsPressed(KeyRightArrow) {
			*cursor = min(*cursor+1, len(*s))
		}
		if keyboard.IsPressed(KeyEscape) {
			// clear out the string
			*s = ""
			*cursor = 0
		}
		if keyboard.IsPressed(KeyEnter) {
			wmReleaseKeyboardFocus()
			exit = TextEditReturnEnter
		}
		if keyboard.IsPressed(KeyTab) {
			if keyboard.IsPressed(KeyShift) {
				exit = TextEditReturnPrev
			} else {
				exit = TextEditReturnNext
			}
		}

		// And finally insert any regular characters into the appropriate spot
		// in the string.
		if keyboard.Input != "" {
			*s = (*s)[:*cursor] + keyboard.Input + (*s)[*cursor:]
			*cursor += len(keyboard.Input)
		}
	}

	if exit == TextEditReturnNone && *s != originalText {
		exit = TextEditReturnTextChanged
	}

	return
}

///////////////////////////////////////////////////////////////////////////
// ColorScheme

type ColorScheme struct {
	Text          RGB
	TextHighlight RGB
	TextError     RGB
	TextDisabled  RGB

	// UI
	Background          RGB
	AltBackground       RGB
	UITitleBackground   RGB
	UIControl           RGB
	UIControlBackground RGB
	UIControlSeparator  RGB
	UIControlHovered    RGB
	UIInputBackground   RGB
	UIControlActive     RGB

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

	ArrivalStrip   RGB
	DepartureStrip RGB

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
	RangeRing  RGB

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
	edit := func(uiName string, internalName string, color *RGB) {
		// It's slightly grungy to hide this in here but it does simplify
		// the code below...
		imgui.TableNextColumn()
		if color.DrawUI(uiName) {
			handleDefinedColorChange(internalName, *color)
			uiUpdateColorScheme(c)
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
		edit("Text", "Text", &c.Text)
		edit("Track", "Track", &c.Track)
		edit("Airport", "Airport", &c.Airport)
		sfd()

		imgui.TableNextRow()
		edit("Highlighted text", "TextHighlight", &c.TextHighlight)
		edit("Tracked data block", "Tracked Data Block", &c.TrackedDataBlock)
		edit("ARTCC", "ARTCC", &c.ARTCC)
		sfd()

		imgui.TableNextRow()
		edit("Error text", "TextError", &c.TextError)
		edit("Selected data block", "Selected Data Block", &c.SelectedDataBlock)
		edit("Compass", "Compass", &c.Compass)
		sfd()

		imgui.TableNextRow()
		edit("Disabled text", "TextDisabled", &c.TextDisabled)
		edit("Handing-off data block", "HandingOff Data Block", &c.HandingOffDataBlock)
		edit("Fix", "Fix", &c.Fix)
		sfd()

		imgui.TableNextRow()
		edit("Background", "Background", &c.Background)
		edit("Untracked data block", "Untracked Data Block", &c.UntrackedDataBlock)
		edit("Geo", "Geo", &c.Geo)
		sfd()

		imgui.TableNextRow()
		edit("Alternate background", "AltBackground", &c.AltBackground)
		edit("Ghost data block", "Ghost Data Block", &c.GhostDataBlock)
		edit("High airway", "HighAirway", &c.HighAirway)
		sfd()

		imgui.TableNextRow()
		edit("UI title background", "UITitleBackground", &c.UITitleBackground)
		edit("Safe situation", "Safe", &c.Safe)
		edit("Low airway", "LowAirway", &c.LowAirway)
		sfd()

		imgui.TableNextRow()
		edit("UI control", "UIControl", &c.UIControl)
		edit("Caution situation", "Caution", &c.Caution)
		edit("NDB", "NDB", &c.NDB)
		sfd()

		imgui.TableNextRow()
		edit("UI control background", "UIControlBackground", &c.UIControlBackground)
		edit("Error situation", "Error", &c.Error)
		edit("Region", "Region", &c.Region)
		sfd()

		imgui.TableNextRow()
		edit("UI control separator", "UIControlSeparator", &c.UIControlSeparator)
		edit("Departure flight strip", "Departure flight strip", &c.DepartureStrip)
		edit("Runway", "Runway", &c.Runway)
		sfd()

		imgui.TableNextRow()
		edit("UI hovered control", "UIControlHovered", &c.UIControlHovered)
		edit("Arrival flight strip", "Arrival flight strip", &c.ArrivalStrip)
		edit("SID", "SID", &c.SID)
		sfd()

		imgui.TableNextRow()
		edit("UI input background", "UIInputBackground", &c.UIInputBackground)
		imgui.TableNextColumn()
		edit("STAR", "STAR", &c.STAR)
		sfd()

		imgui.TableNextRow()
		edit("UI active control", "UIControlActive", &c.UIControlActive)
		imgui.TableNextColumn()
		edit("VOR", "VOR", &c.VOR)
		sfd()

		imgui.TableNextRow()
		imgui.TableNextColumn()
		imgui.TableNextColumn()
		edit("Range rings", "Range rings", &c.RangeRing)
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
		SelectedDataBlock:   RGB{R: 0.9133574, G: 0.9111314, B: 0.2967587},
		UntrackedDataBlock:  RGBFromHex(0x8f92bc),
		TrackedDataBlock:    RGB{R: 0.44499192, G: 0.9491525, B: 0.2573972},
		HandingOffDataBlock: RGB{R: 0.7689531, G: 0.12214418, B: 0.26224726},
		GhostDataBlock:      RGB{R: 0.5090253, G: 0.5090253, B: 0.5090253},
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
	},
	"Nord (builtin)": &ColorScheme{
		Text:                RGB{R: 0.9254902, G: 0.9372549, B: 0.95686275},
		TextHighlight:       RGB{R: 0.53333336, G: 0.7529412, B: 0.8156863},
		TextError:           RGB{R: 0.7490196, G: 0.38039216, B: 0.41568628},
		TextDisabled:        RGB{R: 0.84705883, G: 0.87058824, B: 0.9137255},
		Background:          RGB{R: 0.09803922, G: 0.09803922, B: 0.12156863},
		AltBackground:       RGB{R: 0.10993608, G: 0.12376564, B: 0.16525424},
		UITitleBackground:   RGB{R: 0.29833382, G: 0.3674482, B: 0.52542377},
		UIControl:           RGB{R: 0.2627451, G: 0.29803923, B: 0.36862746},
		UIControlBackground: RGB{R: 0.10629131, G: 0.1152093, B: 0.13559324},
		UIControlSeparator:  RGB{R: 0.11764706, G: 0.12941177, B: 0.14901961},
		UIControlHovered:    RGB{R: 0.36862746, G: 0.5058824, B: 0.6745098},
		UIInputBackground:   RGB{R: 0.2627451, G: 0.29803923, B: 0.36862746},
		UIControlActive:     RGB{R: 0.53333336, G: 0.627451, B: 0.8156863},
		Safe:                RGB{R: 0.6392157, G: 0.74509805, B: 0.54901963},
		Caution:             RGB{R: 0.92156863, G: 0.79607844, B: 0.54509807},
		Error:               RGB{R: 0.7490196, G: 0.38039216, B: 0.41568628},
		SelectedDataBlock:   RGB{R: 0.56078434, G: 0.7372549, B: 0.73333335},
		UntrackedDataBlock:  RGB{R: 0.5058824, G: 0.6313726, B: 0.75686276},
		TrackedDataBlock:    RGB{R: 0.8980392, G: 0.9137255, B: 0.9411765},
		HandingOffDataBlock: RGBFromHex(0xbf616a),
		GhostDataBlock:      RGB{R: 0.84705883, G: 0.87058824, B: 0.9137255},
		Track:               RGB{R: 0.84705883, G: 0.87058824, B: 0.9137255},
		ArrivalStrip:        RGBFromHex(0x292E3B),
		DepartureStrip:      RGBFromHex(0x1F242C),
		Airport:             RGBFromHex(0x4d7372),
		VOR:                 RGBFromHex(0x4d7372),
		NDB:                 RGBFromHex(0x4d7372),
		Fix:                 RGBFromHex(0x4d7372),
		Runway:              RGB{R: 0.36862746, G: 0.5058824, B: 0.6745098},
		Region:              RGB{R: 0.36862746, G: 0.5058824, B: 0.6745098},
		SID:                 RGB{R: 0.29803923, G: 0.3372549, B: 0.41568628},
		STAR:                RGBFromHex(0x3b475e),
		Geo:                 RGB{R: 0.29803923, G: 0.3372549, B: 0.41568628},
		ARTCC:               RGB{R: 0.29803923, G: 0.3372549, B: 0.41568628},
		LowAirway:           RGB{R: 0.29803923, G: 0.3372549, B: 0.41568628},
		HighAirway:          RGB{R: 0.29803923, G: 0.3372549, B: 0.41568628},
		Compass:             RGB{R: 0.36862746, G: 0.5058824, B: 0.6745098},
		RangeRing:           RGBFromHex(0x313d54),
	},
	"Light (builtin)": &ColorScheme{
		Text:                RGBFromHex(0x092BA8),
		TextHighlight:       RGBFromHex(0x148323),
		TextError:           RGBFromHex(0xc63a3a),
		TextDisabled:        RGB{R: 0, G: 0, B: 0},
		Background:          RGBFromHex(0xfdfaf3),
		AltBackground:       RGBFromHex(0xF5F2EB),
		UITitleBackground:   RGBFromHex(0xC5C3BD),
		UIControl:           RGBFromHex(0xd8d8d8),
		UIControlBackground: RGB{R: 0.937, G: 0.937, B: 0.937},
		UIControlSeparator:  RGB{R: 0.59745765, G: 0.59745765, B: 0.59745765},
		UIControlHovered:    RGB{R: 0.63983047, G: 0.63983047, B: 0.63983047},
		UIInputBackground:   RGBFromHex(0xe8e8e8),
		UIControlActive:     RGB{R: 0.6864407, G: 0.6864407, B: 0.6864407},
		Safe:                RGB{R: 0.5117057, G: 0.5247704, B: 1},
		Caution:             RGB{R: 0.8601695, G: 0.6032181, B: 0.14214665},
		Error:               RGB{R: 1, G: 0, B: 0},
		SelectedDataBlock:   RGBFromHex(0x239438),
		UntrackedDataBlock:  RGB{R: 0.32058924, G: 0.8231047, B: 0.24069126},
		TrackedDataBlock:    RGB{R: 0.15045157, G: 0.21625589, B: 0.80144405},
		HandingOffDataBlock: RGB{R: 0.8267148, G: 0.1790718, B: 0.1790718},
		GhostDataBlock:      RGB{R: 0.44404334, G: 0.44404334, B: 0.44404334},
		Track:               RGB{R: 0.37458193, G: 0.37458193, B: 0.37458193},
		ArrivalStrip:        RGBFromHex(0xe8e8e3),
		DepartureStrip:      RGBFromHex(0xf6f6f1),
		Airport:             RGBFromHex(0x5A78AD),
		VOR:                 RGBFromHex(0x5A78AD),
		NDB:                 RGBFromHex(0x5A78AD),
		Fix:                 RGBFromHex(0x5A78AD),
		Runway:              RGB{R: 0.8, G: 0.8, B: 0.4},
		Region:              RGB{R: 0.691375, G: 0.7966102, B: 0.6177105},
		SID:                 RGB{R: 0.6694915, G: 0.54997474, B: 0.5077923},
		STAR:                RGB{R: 0.4755817, G: 0.65254235, B: 0.48807308},
		Geo:                 RGB{R: 0.38559324, G: 0.38559324, B: 0.38559324},
		ARTCC:               RGB{R: 0.7, G: 0.7, B: 0.7},
		LowAirway:           RGB{R: 0.5, G: 0.5, B: 0.5},
		HighAirway:          RGB{R: 0.5, G: 0.5, B: 0.5},
		Compass:             RGB{R: 0.279661, G: 0.279661, B: 0.279661},
		RangeRing:           RGBFromHex(0xd4d4d4),
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

	if imgui.BeginComboV("Color scheme", displayName(positionConfig.ColorSchemeName), imgui.ComboFlagsHeightLarge) {
		names := SortedMapKeys(builtinColorSchemes)
		names = append(names, SortedMapKeys(globalConfig.ColorSchemes)...)

		for _, name := range names {
			flags := imgui.SelectableFlagsNone
			if imgui.SelectableV(displayName(name), name == positionConfig.ColorSchemeName, flags, imgui.Vec2{}) &&
				name != positionConfig.ColorSchemeName {
				positionConfig.ColorSchemeName = name

				// Update the things that depend on the color scheme.
				cs := positionConfig.GetColorScheme()
				uiUpdateColorScheme(cs)
				database.SetColorScheme(cs)
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
	uiStartDisable(!canEdit)

	cs := positionConfig.GetColorScheme()
	cs.ShowEditor(func(name string, rgb RGB) {
		if positionConfig.GetColorScheme().IsDark() {
			imgui.StyleColorsDark()
		} else {
			imgui.StyleColorsLight()
		}
		database.NamedColorChanged(name, rgb)
	})

	uiEndDisable(!canEdit)
}

func uiUpdateColorScheme(cs *ColorScheme) {
	// As far as I can tell, all imgui elements with "unused" below are not
	// used by vice; if they are, now or in the future, it should be
	// evident from the bright red color. (At which point it will be
	// necessary to figure out which existing UI colors should be used for
	// them or if new ones are needed.)
	unused := RGB{1, 0, 0}.imgui()

	style := imgui.CurrentStyle()
	style.SetColor(imgui.StyleColorText, cs.Text.imgui())
	style.SetColor(imgui.StyleColorTextDisabled, cs.TextDisabled.imgui())
	style.SetColor(imgui.StyleColorWindowBg, cs.Background.imgui())
	style.SetColor(imgui.StyleColorChildBg, cs.UIControlBackground.imgui())
	style.SetColor(imgui.StyleColorPopupBg, cs.Background.imgui())
	style.SetColor(imgui.StyleColorBorder, cs.AltBackground.imgui())
	style.SetColor(imgui.StyleColorBorderShadow, unused)
	style.SetColor(imgui.StyleColorFrameBg, cs.UIInputBackground.imgui())
	style.SetColor(imgui.StyleColorFrameBgHovered, cs.UIControlHovered.imgui())
	style.SetColor(imgui.StyleColorFrameBgActive, cs.UITitleBackground.imgui())
	style.SetColor(imgui.StyleColorTitleBg, cs.UITitleBackground.imgui())
	style.SetColor(imgui.StyleColorTitleBgActive, cs.UITitleBackground.imgui())
	style.SetColor(imgui.StyleColorTitleBgCollapsed, cs.UITitleBackground.imgui())
	style.SetColor(imgui.StyleColorMenuBarBg, cs.Background.imgui())
	style.SetColor(imgui.StyleColorScrollbarBg, cs.AltBackground.imgui())
	style.SetColor(imgui.StyleColorScrollbarGrab, cs.UIControl.imgui())
	style.SetColor(imgui.StyleColorScrollbarGrabHovered, cs.UIControlHovered.imgui())
	style.SetColor(imgui.StyleColorScrollbarGrabActive, cs.UIControlActive.imgui())
	style.SetColor(imgui.StyleColorCheckMark, cs.Text.imgui())
	style.SetColor(imgui.StyleColorSliderGrab, cs.UIControlActive.imgui())
	style.SetColor(imgui.StyleColorSliderGrabActive, cs.UIControlActive.imgui())
	style.SetColor(imgui.StyleColorButton, cs.UIControl.imgui())
	style.SetColor(imgui.StyleColorButtonHovered, cs.UIControlHovered.imgui())
	style.SetColor(imgui.StyleColorButtonActive, cs.UIControlActive.imgui())
	style.SetColor(imgui.StyleColorHeader, cs.UITitleBackground.imgui())
	style.SetColor(imgui.StyleColorHeaderHovered, cs.UIControlHovered.imgui())
	style.SetColor(imgui.StyleColorHeaderActive, cs.UIControlActive.imgui())
	style.SetColor(imgui.StyleColorSeparator, cs.UIControlSeparator.imgui())
	style.SetColor(imgui.StyleColorSeparatorHovered, unused)
	style.SetColor(imgui.StyleColorSeparatorActive, unused)
	style.SetColor(imgui.StyleColorResizeGrip, unused)
	style.SetColor(imgui.StyleColorResizeGripHovered, unused)
	style.SetColor(imgui.StyleColorResizeGripActive, unused)
	style.SetColor(imgui.StyleColorTab, unused)
	style.SetColor(imgui.StyleColorTabHovered, unused)
	style.SetColor(imgui.StyleColorTabActive, unused)
	style.SetColor(imgui.StyleColorTabUnfocused, unused)
	style.SetColor(imgui.StyleColorTabUnfocusedActive, unused)
	style.SetColor(imgui.StyleColorPlotLines, unused)
	style.SetColor(imgui.StyleColorPlotLinesHovered, unused)
	style.SetColor(imgui.StyleColorPlotHistogram, unused)
	style.SetColor(imgui.StyleColorPlotHistogramHovered, unused)
	style.SetColor(imgui.StyleColorTableHeaderBg, cs.UITitleBackground.imgui())
	style.SetColor(imgui.StyleColorTableBorderStrong, cs.UIControlSeparator.imgui())
	style.SetColor(imgui.StyleColorTableBorderLight, cs.UIControlSeparator.imgui())
	style.SetColor(imgui.StyleColorTableRowBg, cs.AltBackground.imgui())
	style.SetColor(imgui.StyleColorTableRowBgAlt, cs.Background.imgui())
	style.SetColor(imgui.StyleColorTextSelectedBg, cs.UIControlHovered.imgui())
	style.SetColor(imgui.StyleColorDragDropTarget, unused)
	style.SetColor(imgui.StyleColorNavHighlight, unused)
	style.SetColor(imgui.StyleColorNavWindowingHighlight, unused)
	style.SetColor(imgui.StyleColorNavWindowingDarkening, RGBA{0.5, 0.5, 0.5, 0.5}.imgui())
	style.SetColor(imgui.StyleColorModalWindowDarkening, RGBA{0.3, 0.3, 0.3, 0.3}.imgui())
}
