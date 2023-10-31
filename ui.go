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
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mmp/imgui-go/v4"
	"github.com/pkg/browser"
	"golang.org/x/exp/slog"
)

var (
	ui struct {
		font           *Font
		aboutFont      *Font
		aboutFontSmall *Font

		eventsSubscription *EventsSubscription

		menuBarHeight float32

		showAboutDialog bool

		iconTextureID     uint32
		sadTowerTextureID uint32

		jsonSelectDialog *FileSelectDialogBox

		activeModalDialogs []*ModalDialogBox

		newReleaseDialogChan chan *NewReleaseModalClient
	}

	//go:embed icons/tower-256x256.png
	iconPNG string
	//go:embed icons/sad-tower-alpha-128x128.png
	sadTowerPNG string

	whatsNew []string = []string{
		"Added EWR scenarios, including both departure and approach.",
		"Added Liberty departure scenarios.",
		"Improved routing of departures beyond their exit fix.",
		"Fixed a bug where aircraft on RNAV arrivals wouldn't descend.",
		"Each scenario has a default video map, selected automatically.",
		"If an aircraft given approach clearance is later vectored, approach clearance is now canceled.",
		"Improved spawn positions and hand-off locations for JFK arrivals.",
		"Added F11 TRACON scenarios (KMCO, KSFB, KISM, KORL...)",
		"Font sizes for UI elements can now be set in the settings window",
		"Fixed a crash related to handing off aircraft",
		"Added go arounds",
		"Added ABE TRACON scenarios",
		"Added scenarios for KJAX",
		"Updated PHL scenarios for recent arrival changes",
		"Fixed bug with localizer intercept that made aircraft hang in the air",
		"Fixed a few bugs in the KJAX scenario",
		"Added ISP and HVN departures and arrivals to the JFK_APP scenario",
		"Added LGA departure and arrival scenarios",
		"Vice now remembers the active aircraft when you quit and restores the simulation when you launch it again",
		"When vice is paused, hovering the mouse on a radar track shows the directions it has been given",
		"Fixed a bug where the STARS window wouldn't display anything",
		"All new flight modeling engine supports procedure turns and more accurate turns to intercept",
		"Updated approaches to include procedure turns, where appropriate",
		"Fixed very small fonts on Windows systems with high-DPI displays",
		"Added \"depart fix at heading\" and \"cross fix at altitude/speed\" commands",
		"Added \"cancel speed restrictions\" and \"fly present heading\" commands",
		"Handed-off departures don't start to climb until they are clicked post-handoff",
		"Improved wind modeling",
		"Fixed a bug that would cause arrivals to fly faster than the aircraft is capable of",
		"Fixed bugs with arrivals not obeying crossing restrictions",
		"Improved navigation model to better make crossing restrictions at fixes",
		"Fixed *T in the STARS scope: the line is drawn starting with the first click",
		"For facility engineers: an error is issued for any unused items in the scenario JSON files",
		"Added support for multi-controller simulations(!!)",
		"Added manual launch control option",
		"Many new scenarios added, including C90, CLE, and CLT",
		"Replaced the font used in the STARS radar scope",
		"Fixed a few graphics bugs in the STARS radar scope",
		"Fixed a rare crash with incorrect command input to the STARS scope",
		"New scenarios covering the A80 (ATL) and A90 (BOS) TRACONS",
		"Fixed a bug with drawing *P cones",
		"Many improvements to the STARS DCB implementation",
		"STARS now supports quick-look",
		"Fixed a rare crash when manually adjusting launch rates",
		"Numerous minor improvements to the STARS UI and functionality (including adding dwell mode)",
		"Small fixes to the JAX and CLT scenario files",
		"Added support for STARS FUSED mode (choose \"FUSED\" in the \"SITE\" menu in the DCB)",
		"New commands: EC/ED: expedite climb/descent",
		"New command: I: intercept the localizer",
		"New commands: SMIN/SMAX: maintain slowest practical / maximum forward speed",
		"New command: AFIX/CAPP: at FIX cleared APP approach",
		"Altitude crossing restrictions are more flexible: CFIX/A100-, CFIX/A80+, CFIX/A140-160, etc.",
		"Fixed a bug where arrivals would disappear with some scenarios",
		"Various updates to the JAX, C90, and F11 scenarios",
		"Added D01, KSAV, and KSDF scenarios",
		"Allow altitude and speed instructions to be either simultaneous or consecutive",
		"Added a new KAAC/KJKE scenario",
		"Various minor bugfixes and STARS simulation improvements",
		"Many improvements to the accuracy of the KAAC scenario",
		"Fixed a bug where arrivals would sometimes climb after being cleared for the approach",
		"Fixed a bug in the Windows installer that caused new scenarios (AAC, SAV, SDF) to not be installed locally",
		"Added the ability to draw active departure, arrival, and approach routes on the radar scope",
		"Added the D01 (Denver TRACON) scenario to single-user vice (the installer was missing it)",
		"Added support for updating your Discord activity based on your vice activities (thanks, Samuel Valencia!)",
		"Clicking the " + FontAwesomeIconKeyboard + " icon on the menubar gives a summary of vice's keyboard commands",
		"Fixed bug with aircraft descending too early when flying procedure turns",
		"Fixed bug with some departures trying to re-fly their initial departure route",
		"Fixed multiple bugs with the handling of \"at or above\" altitude constraints",
		"Fixed bug with the default DCB brightness being set to 0",
		"Added DCA scenario",
		"There is now a short delay before aircraft start to follow heading assignments",
		"Added \"ID\" command for ident",
		"Aircraft can now also be issued control commands by entering their callsign before the commands",
		"Fixed bugs with endless go-arounds and with departures not obeying altitude restrictions",
	}
)

var UIControlColor RGB = RGB{R: 0.2754237, G: 0.2754237, B: 0.2754237}
var UICautionColor RGB = RGBFromHex(0xB7B513)
var UITextColor RGB = RGB{R: 0.85, G: 0.85, B: 0.85}
var UITextHighlightColor RGB = RGBFromHex(0xB2B338)
var UIErrorColor RGB = RGBFromHex(0xE94242)

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

func uiInit(r Renderer, p Platform, es *EventStream) {
	if runtime.GOOS == "windows" {
		imgui.CurrentStyle().ScaleAllSizes(p.DPIScale())
	}

	ui.font = GetFont(FontIdentifier{Name: "Roboto Regular", Size: globalConfig.UIFontSize})
	ui.aboutFont = GetFont(FontIdentifier{Name: "Roboto Regular", Size: 18})
	ui.aboutFontSmall = GetFont(FontIdentifier{Name: "Roboto Regular", Size: 14})
	ui.eventsSubscription = es.Subscribe()

	if iconImage, err := png.Decode(bytes.NewReader([]byte(iconPNG))); err != nil {
		lg.Errorf("Unable to decode icon PNG: %v", err)
	} else {
		ui.iconTextureID = r.CreateTextureFromImage(iconImage)
	}

	if sadTowerImage, err := png.Decode(bytes.NewReader([]byte(sadTowerPNG))); err != nil {
		lg.Errorf("Unable to decode sad tower PNG: %v", err)
	} else {
		ui.sadTowerTextureID = r.CreateTextureFromImage(sadTowerImage)
	}

	// Do this asynchronously since it involves network traffic and may
	// take some time (or may even time out, etc.)
	ui.newReleaseDialogChan = make(chan *NewReleaseModalClient)
	go checkForNewRelease(ui.newReleaseDialogChan)

	if globalConfig.WhatsNewIndex < len(whatsNew) {
		uiShowModalDialog(NewModalDialogBox(&WhatsNewModalClient{}), false)
	}
}

func uiShowModalDialog(d *ModalDialogBox, atFront bool) {
	if atFront {
		ui.activeModalDialogs = append([]*ModalDialogBox{d}, ui.activeModalDialogs...)
	} else {
		ui.activeModalDialogs = append(ui.activeModalDialogs, d)
	}
}

func uiCloseModalDialog(d *ModalDialogBox) {
	ui.activeModalDialogs = FilterSlice(ui.activeModalDialogs,
		func(m *ModalDialogBox) bool { return m != d })
}

func uiShowConnectDialog(allowCancel bool) {
	uiShowModalDialog(NewModalDialogBox(&ConnectModalClient{allowCancel: allowCancel}), false)
}

func uiShowDiscordOptInDialog() {
	uiShowModalDialog(NewModalDialogBox(&DiscordOptInModalClient{}), true)
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

func drawUI(p Platform, r Renderer, w *World, eventStream *EventStream, stats *Stats) {
	if ui.newReleaseDialogChan != nil {
		select {
		case dialog, ok := <-ui.newReleaseDialogChan:
			if ok {
				uiShowModalDialog(NewModalDialogBox(dialog), false)
			} else {
				// channel was closed
				ui.newReleaseDialogChan = nil
			}
		default:
			// don't block on the chan if there's nothing there and it's still open...
		}
	}

	imgui.PushFont(ui.font.ifont)
	if imgui.BeginMainMenuBar() {
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.CurrentStyle().Color(imgui.StyleColorMenuBarBg))

		if w != nil && w.Connected() {
			if w.SimIsPaused {
				if imgui.Button(FontAwesomeIconPlayCircle) {
					w.ToggleSimPause()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Resume simulation")
				}
			} else {
				if imgui.Button(FontAwesomeIconPauseCircle) {
					w.ToggleSimPause()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Pause simulation")
				}
			}
		}

		if imgui.Button(FontAwesomeIconRedo) {
			uiShowConnectDialog(true)
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Start new simulation")
		}

		if w != nil && w.Connected() {
			if imgui.Button(FontAwesomeIconCog) {
				w.ToggleActivateSettingsWindow()
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Open settings window")
			}

			if imgui.Button(FontAwesomeIconQuestionCircle) {
				w.ToggleShowScenarioInfoWindow()
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Show available departures, arrivals, and approaches")
			}
		}

		if imgui.Button(FontAwesomeIconKeyboard) {
			uiToggleShowKeyboardWindow()
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Show summary of keyboard commands")
		}

		enableLaunch := w != nil &&
			(w.LaunchConfig.Controller == "" || w.LaunchConfig.Controller == w.Callsign)
		uiStartDisable(!enableLaunch)
		if imgui.Button(FontAwesomeIconPlaneDeparture) {
			w.TakeOrReturnLaunchControl(eventStream)
		}
		if imgui.IsItemHovered() {
			verb := Select(w.LaunchConfig.Controller == "", "Start", "Stop")
			tip := verb + " manually control spawning new aircraft"
			if w.LaunchConfig.Controller != "" {
				tip += "\nCurrent controller: " + w.LaunchConfig.Controller
			}
			imgui.SetTooltip(tip)
		}
		uiEndDisable(!enableLaunch)

		if imgui.Button(FontAwesomeIconBook) {
			browser.OpenURL("https://pharr.org/vice/index.html")
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Display online vice documentation")
		}

		width, _ := ui.font.BoundText(FontAwesomeIconInfoCircle, 0)
		imgui.SetCursorPos(imgui.Vec2{p.DisplaySize()[0] - float32(4*width+10), 0})
		if imgui.Button(FontAwesomeIconInfoCircle) {
			ui.showAboutDialog = !ui.showAboutDialog
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Display information about vice")
		}
		if imgui.Button(FontAwesomeIconDiscord) {
			browser.OpenURL("https://discord.gg/y993vgQxhY")
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Join the vice discord")
		}

		imgui.PopStyleColor()

		imgui.EndMainMenuBar()
	}
	ui.menuBarHeight = imgui.CursorPos().Y - 1

	if w != nil {
		w.DrawSettingsWindow()

		w.DrawScenarioInfoWindow()

		w.DrawMissingPrimaryDialog()

		if w.LaunchConfig.Controller == w.Callsign {
			if w.launchControlWindow == nil {
				w.launchControlWindow = MakeLaunchControlWindow(w)
			}
			w.launchControlWindow.Draw(w, eventStream)
		}
	}

	for _, event := range ui.eventsSubscription.Get() {
		if event.Type == ServerBroadcastMessageEvent {
			uiShowModalDialog(NewModalDialogBox(&BroadcastModalDialog{Message: event.Message}), false)
		}
	}

	drawActiveDialogBoxes()

	wmDrawUI(p)

	uiDrawKeyboardWindow(w)

	imgui.PopFont()

	// Finalize and submit the imgui draw lists
	imgui.Render()
	cb := GetCommandBuffer()
	defer ReturnCommandBuffer(cb)
	GenerateImguiCommandBuffer(cb)
	stats.renderUI = r.RenderCommandBuffer(cb)
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

	if ui.showAboutDialog {
		showAboutDialog()
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

func drawAirportSelector(airports map[string]interface{}, title string) (map[string]interface{}, bool) {
	airportsString := strings.Join(SortedMapKeys(airports), ",")

	if imgui.InputTextV(title, &airportsString, imgui.InputTextFlagsCharsUppercase, nil) {
		ap := strings.FieldsFunc(airportsString, func(ch rune) bool {
			return unicode.IsSpace(ch) || ch == ','
		})

		airports = make(map[string]interface{})
		for _, a := range ap {
			airports[a] = nil
		}
		return airports, true
	}

	return airports, false
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

	sz.X *= Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
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
	imgui.OpenPopup(title)

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

type ConnectModalClient struct {
	config      NewSimConfiguration
	allowCancel bool
}

func (c *ConnectModalClient) Title() string { return "New Simulation" }

func (c *ConnectModalClient) Opening() {
	c.config = MakeNewSimConfiguration()
}

func (c *ConnectModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	if c.allowCancel {
		b = append(b, ModalDialogButton{text: "Cancel"})
	}

	ok := ModalDialogButton{
		text:     "Ok",
		disabled: c.config.OkDisabled(),
		action: func() bool {
			c.config.displayError = c.config.Start()
			return c.config.displayError == nil
		},
	}

	return append(b, ok)
}

func (c *ConnectModalClient) Draw() int {
	if enter := c.config.DrawUI(); enter {
		return 1
	} else {
		return -1
	}
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

func checkForNewRelease(newReleaseDialogChan chan *NewReleaseModalClient) {
	defer close(newReleaseDialogChan)

	url := "https://api.github.com/repos/mmp/vice/releases"

	resp, err := http.Get(url)
	if err != nil {
		lg.Warn("new release GET error", slog.String("url", url), slog.Any("error", err))
		return
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
		return
	}
	if len(releases) == 0 {
		return
	}

	var newestRelease *Release
	for i := range releases {
		if strings.HasSuffix(releases[i].TagName, "-beta") {
			continue
		}
		if newestRelease == nil || releases[i].Created.After(newestRelease.Created) {
			newestRelease = &releases[i]
		}
	}
	if newestRelease == nil {
		lg.Warnf("No vice releases found?")
		return
	}

	lg.Infof("newest release found: %v", newestRelease)

	buildTime := ""
	if bi, ok := debug.ReadBuildInfo(); !ok {
		lg.Errorf("unable to read build info")
		return
	} else {
		for _, setting := range bi.Settings {
			if setting.Key == "vcs.time" {
				buildTime = setting.Value
				break
			}
		}

		if buildTime == "" {
			lg.Errorf("build time unavailable in BuildInfo.Settings")
			return
		}
	}

	if bt, err := time.Parse(time.RFC3339, buildTime); err != nil {
		lg.Errorf("error parsing build time \"%s\": %v", buildTime, err)
	} else if newestRelease.Created.UTC().After(bt.UTC()) {
		lg.Infof("build time %s newest release %s -> release is newer",
			bt.UTC().String(), newestRelease.Created.UTC().String())
		newReleaseDialogChan <- &NewReleaseModalClient{
			version: newestRelease.TagName,
			date:    newestRelease.Created}
	} else {
		lg.Infof("build time %s newest release %s -> build is newer",
			bt.UTC().String(), newestRelease.Created.UTC().String())
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
				browser.OpenURL("https://pharr.org/vice/index.html#section-installation")
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

type WhatsNewModalClient struct{}

func (nr *WhatsNewModalClient) Title() string {
	return "What's new in this version of vice"
}

func (nr *WhatsNewModalClient) Opening() {}

func (nr *WhatsNewModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		ModalDialogButton{
			text: "View Release Notes",
			action: func() bool {
				browser.OpenURL("https://pharr.org/vice/index.html#releases")
				return false
			},
		},
		ModalDialogButton{
			text: "Ok",
			action: func() bool {
				globalConfig.WhatsNewIndex = len(whatsNew)
				return true
			},
		},
	}
}

func (nr *WhatsNewModalClient) Draw() int {
	for i := globalConfig.WhatsNewIndex; i < len(whatsNew); i++ {
		imgui.Text(FontAwesomeIconSquare + " " + whatsNew[i])
	}
	return -1
}

type BroadcastModalDialog struct {
	Message string
}

func (b *BroadcastModalDialog) Title() string {
	return "Server Broadcast Message"
}

func (b *BroadcastModalDialog) Opening() {}

func (b *BroadcastModalDialog) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		ModalDialogButton{
			text: "Ok",
			action: func() bool {
				return true
			},
		},
	}
}

func (b *BroadcastModalDialog) Draw() int {
	imgui.Text(b.Message)
	return -1
}

type DiscordOptInModalClient struct{}

func (d *DiscordOptInModalClient) Title() string {
	return "Discord Activity Updates"
}

func (d *DiscordOptInModalClient) Opening() {}

func (d *DiscordOptInModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		ModalDialogButton{
			text: "Ok",
			action: func() bool {
				globalConfig.AskedDiscordOptIn = true
				return true
			},
		},
	}
}

func (d *DiscordOptInModalClient) Draw() int {
	style := imgui.CurrentStyle()
	spc := style.ItemSpacing()
	spc.Y -= 4
	imgui.PushStyleVarVec2(imgui.StyleVarItemSpacing, spc)

	imgui.Text("By default, vice will automatically update your Discord Activity to say")
	imgui.Text("that you are running vice, using information about your current session.")
	imgui.Text("If you do not want it to do this, you can disable this feature using the")
	imgui.Text("checkbox below. You can also change this setting any time in the future")
	imgui.Text("in the settings window " + FontAwesomeIconCog + " via the menu bar.")

	imgui.PopStyleVar()

	imgui.Text("")

	update := !globalConfig.InhibitDiscordActivity.Load()
	imgui.Checkbox("Update Discord activity status", &update)
	globalConfig.InhibitDiscordActivity.Store(!update)

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
	center("vice")
	center(FontAwesomeIconCopyright + "2023 Matt Pharr")
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

	imgui.Separator()

	imgui.PushFont(ui.aboutFontSmall.ifont)
	// We would very much like to use imgui.{Push,Pop}TextWrapPos()
	// here, but for unclear reasons that makes the info window
	// vertically maximized. So we hand-wrap the lines for the
	// font we're using...
	credits :=
		`Additional credits: Thanks to Dennis Graiani and
Samuel Valencia for contributing features to vice
and to Adam Bolek, Mike K, Arya T, and Samuel
Valencia for contributing additional scenarios.
Video maps are thanks to the ZAU, ZBW, ZDV, ZJX,
ZNY, and ZOB VATSIM ARTCCs. Thanks also to
OpenScope for the airline fleet and aircraft
performance databases and to ourairports.com for
the airport database. See the file CREDITS.txt
in the vice source code distribution for third-party
software, fonts, sounds, etc.`

	imgui.Text(credits)

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

	selectDirectory bool
	title           string
	filter          []string
	callback        func(string)
}

type DirEntry struct {
	name  string
	isDir bool
}

func NewFileSelectDialogBox(title string, filter []string, filename string,
	callback func(string)) *FileSelectDialogBox {
	return &FileSelectDialogBox{
		title:     title,
		directory: defaultDirectory(filename),
		filter:    filter,
		callback:  callback}
}

func NewDirectorySelectDialogBox(title string, current string,
	callback func(string)) *FileSelectDialogBox {
	fsd := &FileSelectDialogBox{
		title:           title,
		selectDirectory: true,
		callback:        callback}
	if current != "" {
		fsd.directory = current
	} else {
		fsd.directory = defaultDirectory("")
	}
	return fsd
}

func defaultDirectory(filename string) string {
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
	return path.Clean(dir)
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
		tableScale := Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
		if imgui.BeginTableV("Files##"+fs.directory, 1, flags,
			imgui.Vec2{tableScale * 500, float32(platform.WindowSize()[1] * 3 / 4)}, 0) {
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
					if fs.filter == nil && !fs.selectDirectory {
						canSelect = true
					}
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

		disableOk := !fileSelected && !fs.selectDirectory
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
		text, _ := wrapText(e.message, 80, 0, true)
		imgui.Text("\n\n" + text)

		imgui.EndTable()
	}
	return -1
}

func ShowErrorDialog(s string, args ...interface{}) {
	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)})
	uiShowModalDialog(d, true)

	lg.Errorf(s, args...)
}

func ShowFatalErrorDialog(r Renderer, p Platform, s string, args ...interface{}) {
	lg.Errorf(s, args...)

	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)})

	for !d.closed {
		p.ProcessEvents()
		p.NewFrame()
		imgui.NewFrame()
		imgui.PushFont(ui.font.ifont)
		d.Draw()
		imgui.PopFont()

		imgui.Render()
		var cb CommandBuffer
		GenerateImguiCommandBuffer(&cb)
		r.RenderCommandBuffer(&cb)

		p.PostRender()
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
		[2]float32{pw - float32(sb.barWidth) - float32(edgeSpace), wy1}, UIControlColor)
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

type LaunchControlWindow struct {
	w          *World
	departures []*LaunchDeparture
	arrivals   []*LaunchArrival
}

type LaunchDeparture struct {
	Aircraft           *Aircraft
	Airport            string
	Runway             string
	Category           string
	LastLaunchCallsign string
	LastLaunchTime     time.Time
	TotalLaunches      int
}

func (ld *LaunchDeparture) Reset() {
	ld.LastLaunchCallsign = ""
	ld.LastLaunchTime = time.Time{}
	ld.TotalLaunches = 0
}

type LaunchArrival struct {
	Aircraft           *Aircraft
	Airport            string
	Group              string
	LastLaunchCallsign string
	LastLaunchTime     time.Time
	TotalLaunches      int
}

func (la *LaunchArrival) Reset() {
	la.LastLaunchCallsign = ""
	la.LastLaunchTime = time.Time{}
	la.TotalLaunches = 0
}

func MakeLaunchControlWindow(w *World) *LaunchControlWindow {
	lc := &LaunchControlWindow{w: w}

	config := &w.LaunchConfig
	for _, airport := range SortedMapKeys(config.DepartureRates) {
		runwayRates := config.DepartureRates[airport]
		for _, rwy := range SortedMapKeys(runwayRates) {
			for _, category := range SortedMapKeys(runwayRates[rwy]) {
				lc.departures = append(lc.departures, &LaunchDeparture{
					Aircraft: lc.spawnDeparture(airport, rwy, category),
					Airport:  airport,
					Runway:   rwy,
					Category: category,
				})
			}
		}
	}

	for _, group := range SortedMapKeys(config.ArrivalGroupRates) {
		for _, airport := range SortedMapKeys(config.ArrivalGroupRates[group]) {
			lc.arrivals = append(lc.arrivals, &LaunchArrival{
				Aircraft: lc.spawnArrival(group, airport),
				Airport:  airport,
				Group:    group,
			})
		}
	}

	return lc
}

func (lc *LaunchControlWindow) spawnDeparture(airport, rwy, category string) *Aircraft {
	for i := 0; i < 100; i++ {
		if ac, _, err := lc.w.CreateDeparture(airport, rwy, category, 0, nil); err == nil {
			return ac
		}
	}
	panic("unable to spawn a departure")
}

func (lc *LaunchControlWindow) spawnArrival(group, airport string) *Aircraft {
	for i := 0; i < 100; i++ {
		goAround := rand.Float32() < lc.w.LaunchConfig.GoAroundRate

		if ac, err := lc.w.CreateArrival(group, airport, goAround); err == nil {
			return ac
		}
	}
	panic("unable to spawn a departure")
}

func (lc *LaunchControlWindow) Draw(w *World, eventStream *EventStream) {
	showLaunchControls := true
	imgui.BeginV("Launch Control", &showLaunchControls, imgui.WindowFlagsAlwaysAutoResize)

	imgui.Text("Mode:")
	imgui.SameLine()
	if imgui.RadioButtonInt("Manual", &lc.w.LaunchConfig.Mode, LaunchManual) {
		w.SetLaunchConfig(lc.w.LaunchConfig)
	}
	imgui.SameLine()
	if imgui.RadioButtonInt("Automatic", &lc.w.LaunchConfig.Mode, LaunchAutomatic) {
		w.SetLaunchConfig(lc.w.LaunchConfig)
	}

	width, _ := ui.font.BoundText(FontAwesomeIconPlayCircle, 0)
	// Right-justify
	imgui.SameLine()
	//	imgui.SetCursorPos(imgui.Vec2{imgui.CursorPosX() + imgui.ContentRegionAvail().X - float32(3*width+10),
	imgui.SetCursorPos(imgui.Vec2{imgui.WindowWidth() - float32(5*width), imgui.CursorPosY()})
	if lc.w != nil && lc.w.Connected() {
		if lc.w.SimIsPaused {
			if imgui.Button(FontAwesomeIconPlayCircle) {
				lc.w.ToggleSimPause()
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Resume simulation")
			}
		} else {
			if imgui.Button(FontAwesomeIconPauseCircle) {
				lc.w.ToggleSimPause()
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Pause simulation")
			}
		}
	}

	imgui.SameLine()
	if imgui.Button(FontAwesomeIconTrash) {
		uiShowModalDialog(NewModalDialogBox(&YesOrNoModalClient{
			title: "Are you sure?",
			query: "All aircraft will be deleted. Go ahead?",
			ok: func() {
				for _, ac := range lc.w.Aircraft {
					lc.w.DeleteAircraft(ac, nil)
					for _, dep := range lc.departures {
						dep.Reset()
					}
					for _, arr := range lc.arrivals {
						arr.Reset()
					}
				}
			},
		}), true)
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Delete all aircraft and restart")
	}

	imgui.Separator()

	if lc.w.LaunchConfig.Mode == LaunchManual {
		mitAndTime := func(ac *Aircraft, launchPosition Point2LL,
			lastLaunchCallsign string, lastLaunchTime time.Time) {
			imgui.TableNextColumn()
			if lastLaunchCallsign != "" {
				if ac := lc.w.Aircraft[lastLaunchCallsign]; ac != nil {
					d := nmdistance2ll(ac.Position(), launchPosition)
					imgui.Text(fmt.Sprintf("%.1f", d))
				}
			}

			imgui.TableNextColumn()
			if lastLaunchCallsign != "" {
				d := lc.w.CurrentTime().Sub(lastLaunchTime).Round(time.Second).Seconds()
				m, s := int(d)/60, int(d)%60
				imgui.Text(fmt.Sprintf("%02d:%02d", m, s))
			}
		}

		ndep := ReduceSlice(lc.departures, func(dep *LaunchDeparture, n int) int {
			return n + dep.TotalLaunches
		}, 0)
		imgui.Text(fmt.Sprintf("Departures: %d total", ndep))

		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		tableScale := Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
		if imgui.BeginTableV("dep", 9, flags, imgui.Vec2{tableScale * 600, 0}, 0.0) {
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Launches")
			imgui.TableSetupColumn("Callsign")
			imgui.TableSetupColumn("A/C Type")
			imgui.TableSetupColumn("Exit")
			imgui.TableSetupColumn("MIT")
			imgui.TableSetupColumn("Time")
			imgui.TableHeadersRow()

			for _, dep := range lc.departures {
				imgui.PushID(dep.Airport + " " + dep.Runway + " " + dep.Category)

				imgui.TableNextRow()

				imgui.TableNextColumn()
				imgui.Text(dep.Airport + " " + dep.Runway + " " + dep.Category)

				imgui.TableNextColumn()
				imgui.Text(strconv.Itoa(dep.TotalLaunches))

				imgui.TableNextColumn()
				imgui.Text(dep.Aircraft.Callsign)

				imgui.TableNextColumn()
				imgui.Text(dep.Aircraft.FlightPlan.TypeWithoutSuffix())

				imgui.TableNextColumn()
				imgui.Text(dep.Aircraft.Scratchpad)

				mitAndTime(dep.Aircraft, dep.Aircraft.Position(), dep.LastLaunchCallsign,
					dep.LastLaunchTime)

				imgui.TableNextColumn()
				if imgui.Button(FontAwesomeIconPlaneDeparture) {
					lc.w.LaunchAircraft(*dep.Aircraft)
					dep.LastLaunchCallsign = dep.Aircraft.Callsign
					dep.LastLaunchTime = lc.w.CurrentTime()
					dep.TotalLaunches++

					dep.Aircraft = lc.spawnDeparture(dep.Airport, dep.Runway, dep.Category)
				}

				imgui.TableNextColumn()
				if imgui.Button(FontAwesomeIconRedo) {
					dep.Aircraft = lc.spawnDeparture(dep.Airport, dep.Runway, dep.Category)
				}

				imgui.PopID()
			}

			imgui.EndTable()
		}

		imgui.Separator()

		narr := ReduceSlice(lc.arrivals, func(arr *LaunchArrival, n int) int {
			return n + arr.TotalLaunches
		}, 0)
		imgui.Text(fmt.Sprintf("Arrivals: %d total", narr))

		if imgui.BeginTableV("arr", 9, flags, imgui.Vec2{tableScale * 600, 0}, 0.0) {
			imgui.TableSetupColumn("Group")
			imgui.TableSetupColumn("Launches")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Callsign")
			imgui.TableSetupColumn("A/C Type")
			imgui.TableSetupColumn("MIT")
			imgui.TableSetupColumn("Time")
			imgui.TableHeadersRow()

			for _, arr := range lc.arrivals {
				imgui.PushID(arr.Group + arr.Airport)

				imgui.TableNextRow()

				imgui.TableNextColumn()
				imgui.Text(arr.Group)

				imgui.TableNextColumn()
				imgui.Text(strconv.Itoa(arr.TotalLaunches))

				imgui.TableNextColumn()
				imgui.Text(arr.Airport)

				imgui.TableNextColumn()
				imgui.Text(arr.Aircraft.Callsign)

				imgui.TableNextColumn()
				imgui.Text(arr.Aircraft.FlightPlan.TypeWithoutSuffix())

				mitAndTime(arr.Aircraft, arr.Aircraft.Position(), arr.LastLaunchCallsign,
					arr.LastLaunchTime)

				imgui.TableNextColumn()
				if imgui.Button(FontAwesomeIconPlaneDeparture) {
					lc.w.LaunchAircraft(*arr.Aircraft)
					arr.LastLaunchCallsign = arr.Aircraft.Callsign
					arr.LastLaunchTime = lc.w.CurrentTime()
					arr.TotalLaunches++

					arr.Aircraft = lc.spawnArrival(arr.Group, arr.Airport)
				}

				imgui.TableNextColumn()
				if imgui.Button(FontAwesomeIconRedo) {
					arr.Aircraft = lc.spawnArrival(arr.Group, arr.Airport)
				}

				imgui.PopID()
			}

			imgui.EndTable()
		}
	} else {
		// Slightly messy, but DrawActiveDepartureRunways expects a table context...
		tableScale := Select(runtime.GOOS == "windows", platform.DPIScale(), float32(1))
		if imgui.BeginTableV("runways", 2, 0, imgui.Vec2{tableScale * 500, 0}, 0.) {
			lc.w.LaunchConfig.DrawActiveDepartureRunways()
			imgui.EndTable()
		}
		changed := lc.w.LaunchConfig.DrawDepartureUI()
		changed = lc.w.LaunchConfig.DrawArrivalUI() || changed

		if changed {
			lc.w.SetLaunchConfig(lc.w.LaunchConfig)
		}
	}

	imgui.End()

	if !showLaunchControls {
		lc.w.TakeOrReturnLaunchControl(eventStream)
	}
}

///////////////////////////////////////////////////////////////////////////

var keyboardWindowVisible bool
var selectedCommandTypes string

func uiToggleShowKeyboardWindow() {
	keyboardWindowVisible = !keyboardWindowVisible
}

var primaryAcCommands = [][3]string{
	[3]string{"*H_hdg", `"Fly heading _hdg_." If no heading is given, "fly present heading".`,
		"*H050*, *H*"},
	[3]string{"*D_fix", `"Proceed direct _fix_".`, "*DWAVEY*"},
	[3]string{"*C_alt", `"Climb and maintain _alt_".`, "*C170*"},
	[3]string{"*TC_alt", `"After reaching speed _kts_, climb and maintain _alt_", where _kts_ is a previously-assigned speed.`, "*TC170*"},
	[3]string{"*D_alt", `"Descend and maintain _alt_".`, "*D20*"},
	[3]string{"*TD_alt", `"Descend and maintain _alt_ after reaching _kts_ knots", where _kts_ is a previously-assigned
speed. (*TD* = 'then descend')`, "*TD20*"},
	[3]string{"*S_kts", `"Reduce/increase speed to _kts_."
If no speed is given, "cancel speed restrictions".`, "*S210*, *S*"},
	[3]string{"*TS_kts", `"After reaching _alt_, reduce/increase speed to _kts_", where _alt_ is a previously-assigned
altitude. (*TS* = 'then speed')`, "*TS210*"},
	[3]string{"*E_appr", `"Expect the _appr_ approach."`, "*EI2L*"},
	[3]string{"*C_appr", `"Cleared _appr_ approach."`, "*CI2L*"},
	[3]string{"*X*", "(Deletes the aircraft.)", "*X*"},
}

var secondaryAcCommands = [][3]string{
	[3]string{"*L_hdg", `"Turn left heading _hdg_."`, "*L130*"},
	[3]string{"*L_deg*D", `"Turn _deg_ degrees left."`, "*L10D*"},
	[3]string{"*R_hdg", `"Turn right heading _hdg_".`, "*R210*"},
	[3]string{"*R_deg*D", `"Turn _deg_ degrees right".`, "*R20D*"},
	[3]string{"*D_fix*/H_hdg", `"Depart _fix_ heading _hdg_".`, "*DLENDY/H180*"},
	[3]string{"*C_fix*/A_alt*/S_kts",
		`"Cross _fix_ at _alt_ / _kts_ knots."
Either one or both of *A* and *S* may be specified.`, "*CCAMRN/A110+*"},
	[3]string{"*ED*", `"Expedite descent"`, "*ED*"},
	[3]string{"*EC*", `"Expedite climb"`, "*EC*"},
	[3]string{"*SMIN*", `"Maintain slowest practical speed".`, "*SMIN*"},
	[3]string{"*SMAX*", `"Maintain maximum forward speed".`, "*SMAX*"},
	[3]string{"*A_fix*/C_appr", `"At _fix_, cleared _appr_ approach."`, "*AROSLY/CI2L*"},
	[3]string{"*CAC*", `"Cancel approach clearance".`, "*CAC*"},
	[3]string{"*CSI_appr", `"Cleared straight-in _appr_ approach.`, "*CSII6*"},
	[3]string{"*I*", `"Intercept the localizer."`, "*I*"},
	[3]string{"*ID*", `"Ident."`, "*ID*"},
}

var starsCommands = [][2]string{
	[2]string{"@", `If the aircraft is an inbound handoff, accept the handoff.
If the aircraft has been handed off to another controller who has accepted
the handoff, transfer control to the other controller.`},
	[2]string{"*[F3] @", `Initiate track of an untracked aircraft.`},
	[2]string{"_id_ @", `Handoff aircraft to the controller identified by _id_.`},
	[2]string{". @", `Clear aircraft's scratchpad.`},
	[2]string{"*[F7]Y_scr_ @", `Set aircraft's scratchpad to _scr_ (3 character limit).`},
	[2]string{"+_alt_ @", `Set the temporary altitude in the aircraft's datablock to _alt_,
which must be 3 digits (e.g., *040*).`},
	[2]string{"_id_\\* @", `Point out the aircraft to the controller identified by _id_.`},
}

// draw the windows that shows the available keyboard commands
func uiDrawKeyboardWindow(w *World) {
	if !keyboardWindowVisible {
		return
	}

	imgui.BeginV("Keyboard Command Reference", &keyboardWindowVisible, 0)

	style := imgui.CurrentStyle()

	// Initial line with a link to the website
	imgui.Text("See the ")
	imgui.SameLineV(0, 0)
	imgui.PushStyleColor(imgui.StyleColorText, imgui.Vec4{0.4, 0.6, 1, 1})
	imgui.Text("vice website")
	// Underline the link
	min, max := imgui.ItemRectMin(), imgui.ItemRectMax()
	c := style.Color(imgui.StyleColorText)
	imgui.WindowDrawList().AddLine(imgui.Vec2{min.X, max.Y}, max, imgui.PackedColorFromVec4(c))
	if imgui.IsItemHovered() && imgui.IsMouseClicked(0) {
		browser.OpenURL("https://pharr.org/vice/")
	}
	imgui.PopStyleColor()
	imgui.SameLineV(0, 0)
	imgui.Text(" for full documentation of vice's keyboard commands.")

	imgui.Separator()

	fixedFont := GetFont(FontIdentifier{Name: "Roboto Mono", Size: globalConfig.UIFontSize})
	italicFont := GetFont(FontIdentifier{Name: "Roboto Mono Italic", Size: globalConfig.UIFontSize})

	// Tighten up the line spacing
	spc := style.ItemSpacing()
	spc.Y -= 4
	imgui.PushStyleVarVec2(imgui.StyleVarItemSpacing, spc)

	// Handling of the three types of commands that may be drawn is fairly
	// hard-coded in the following; there are few enough of them that any
	// further abstraction doesn't seem worth the trouble.
	const ACControlPrimary = "Aircraft Control (Primary)"
	const ACControlSecondary = "Aircraft Control (Secondary)"
	const STARS = "STARS (Most frequently used)"

	if selectedCommandTypes == "" {
		selectedCommandTypes = ACControlPrimary
	}
	if imgui.BeginComboV("Command Group", selectedCommandTypes, imgui.ComboFlagsHeightLarge) {
		if imgui.SelectableV(ACControlPrimary, selectedCommandTypes == ACControlPrimary, 0, imgui.Vec2{}) {
			selectedCommandTypes = ACControlPrimary
		}
		if imgui.SelectableV(ACControlSecondary, selectedCommandTypes == ACControlSecondary, 0, imgui.Vec2{}) {
			selectedCommandTypes = ACControlSecondary
		}
		if imgui.SelectableV(STARS, selectedCommandTypes == STARS, 0, imgui.Vec2{}) {
			selectedCommandTypes = STARS
		}
		imgui.EndCombo()
	}

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg |
		imgui.TableFlagsSizingStretchProp
	if selectedCommandTypes == ACControlPrimary || selectedCommandTypes == ACControlSecondary {
		imgui.Text("\n")
		uiDrawMarkedupText(ui.font, fixedFont, italicFont, `
To issue a command to an aircraft, enter one the following commands and then click on an
aircraft to issue the command. Alternatively, enter the aircraft's callsign with a
space after it and then enter a command. Multiple commands may be given separated by spaces.
`)
		imgui.Text("\n\n")
		uiDrawMarkedupText(ui.font, fixedFont, italicFont, `
Note that all altitudes should be specified in hundreds of feet and speed/altitude changes happen
simultaneously unless the *TC*, *TD*, or *TS* commands are used to specify the change to be done
after the first.`)
		imgui.Text("\n\n")

		if w != nil {
			var apprNames []string
			for _, rwy := range w.ArrivalRunways {
				ap := w.Airports[rwy.Airport]
				for _, name := range SortedMapKeys(ap.Approaches) {
					appr := ap.Approaches[name]
					if appr.Runway == rwy.Runway {
						apprNames = append(apprNames, name+" ("+rwy.Airport+")")
					}
				}
			}
			if len(apprNames) > 0 {
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, `Active approaches: `+strings.Join(apprNames, ", "))
				imgui.Text("\n\n")
			}
		}

		if imgui.BeginTableV("control", 3, flags, imgui.Vec2{}, 0.) {
			imgui.TableSetupColumn("Command")
			imgui.TableSetupColumn("Instruction")
			imgui.TableSetupColumn("Example")
			imgui.TableHeadersRow()

			cmds := Select(selectedCommandTypes == ACControlPrimary, primaryAcCommands, secondaryAcCommands)
			for _, cmd := range cmds {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, cmd[0])
				imgui.TableNextColumn()
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, cmd[1])
				imgui.TableNextColumn()
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, cmd[2])
			}
		}
		imgui.EndTable()
	} else {
		imgui.Text("\n")
		uiDrawMarkedupText(ui.font, fixedFont, italicFont, `
In the following, the mouse icon @ indicates clicking on the radar track of an aircraft on the scope.
Keyboard function keys are shown in brackets: *[F3]*.
The _id_s used to identify other controllers are 2-3 characters and are listed to the left of the
control positions in the controller list on the upper right side of the scope (unless moved).`)
		imgui.Text("\n\n")

		if imgui.BeginTableV("stars", 2, flags, imgui.Vec2{}, 0.) {
			imgui.TableSetupColumn("Command")
			imgui.TableSetupColumn("Description")
			imgui.TableHeadersRow()

			for _, cmd := range starsCommands {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, cmd[0])
				imgui.TableNextColumn()
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, cmd[1])
			}
			imgui.EndTable()
		}
	}

	imgui.PopStyleVar()

	imgui.End()
}

// uiDrawMarkedupText uses imgui to draw the given string, which may
// include some rudimentary markup:
//
// - @ -> draw a computer mouse icon
// - _ -> start/stop italic font
// - * -> start/stop fixed-width font
// - \ -> escape character: interpret the next character literally
//
// If italic text follows fixed-width font text (or vice versa), it is not
// necessary to denote the end of the old formatting. Thus, one may write
// "*D_alt" to have "D" fixed-width and "alt" in italics; it is not
// necessary to write "*D*_alt_".
func uiDrawMarkedupText(regularFont *Font, fixedFont *Font, italicFont *Font, str string) {
	// regularFont is the default and starting point
	imgui.PushFont(regularFont.ifont)

	// textWidth approximates the width of the given string in pixels; it
	// may slightly over-estimate the width, but that's fine since we use
	// it to decide when to wrap lines of text.
	textWidth := func(s string) float32 {
		s = strings.Trim(s, `_*\`) // remove markup characters
		imgui.PushFont(fixedFont.ifont)
		sz := imgui.CalcTextSize(s, false, 0)
		imgui.PopFont()
		return sz.X
	}

	fixed, italic := false, false
	// Split the string into words. Note that this doesn't preserve extra
	// spacing from multiple spaces or respect embedded newlines.
	for _, word := range strings.Fields(str) {
		if textWidth(word) > imgui.ContentRegionAvail().X {
			// start a new line
			imgui.Text("\n")
		}

		// Rather than calling imgui.Text() for each word, we'll accumulate
		// text into s and then display it when needed (font change, new
		// line, etc..)
		var s string
		flush := func() {
			imgui.Text(s)
			imgui.SameLineV(0, 0) // prevent extra spacing after the text.
			s = ""
		}

		nextLiteral := false // should the next character be treated literally?
		for _, ch := range word {
			if nextLiteral {
				s += string(ch)
				nextLiteral = false
				continue
			}

			switch ch {
			case '@':
				s += FontAwesomeIconMouse

			case '\\':
				nextLiteral = true

			case '*':
				flush() // font change
				if fixed {
					// end of fixed-width
					fixed = false
					imgui.PopFont()
				} else {
					if italic {
						// end italic
						imgui.PopFont()
					}
					fixed, italic = true, false
					imgui.PushFont(fixedFont.ifont)
				}

			case '_':
				flush() // font change
				if italic {
					// end of italics
					italic = false
					imgui.PopFont()
				} else {
					if fixed {
						// end of fixed-width
						imgui.PopFont()
					}
					fixed, italic = false, true
					imgui.PushFont(italicFont.ifont)
				}

			default:
				s += string(ch)
			}
		}
		s += " "
		flush()
	}

	if fixed || italic {
		imgui.PopFont()
	}

	imgui.PopFont() // regular font
}
