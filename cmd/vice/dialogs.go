// dialogs.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image/png"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/pkg/browser"
)

var (
	activeModalDialogs []*ModalDialogBox
	sadTowerTextureID  uint32

	//go:embed icons/sad-tower-alpha-128x128.png
	sadTowerPNG string
)

func hasActiveModalDialogs() bool {
	return len(activeModalDialogs) > 0
}

func dialogsInit(r renderer.Renderer, lg *log.Logger) {
	if sadTowerImage, err := png.Decode(bytes.NewReader([]byte(sadTowerPNG))); err != nil {
		lg.Errorf("Unable to decode sad tower PNG: %v", err)
	} else {
		sadTowerTextureID = r.CreateTextureFromImage(sadTowerImage, false)
	}
}

func uiShowModalDialog(d *ModalDialogBox, atFront bool) {
	if atFront {
		activeModalDialogs = append([]*ModalDialogBox{d}, activeModalDialogs...)
	} else {
		activeModalDialogs = append(activeModalDialogs, d)
	}
}

func uiShowConnectDialog(mgr *client.ConnectionManager, allowCancel bool, config *Config, p platform.Platform, lg *log.Logger) {
	client := &ScenarioSelectionModalClient{
		mgr:         mgr,
		lg:          lg,
		allowCancel: allowCancel,
		platform:    p,
		config:      config,
	}
	uiShowModalDialog(NewModalDialogBox(client, p), false)
}

func uiShowDiscordOptInDialog(p platform.Platform, config *Config) {
	uiShowModalDialog(NewModalDialogBox(&DiscordOptInModalClient{config: config}, p), true)
}

func uiShowTargetGenCommandModeDialog(p platform.Platform, config *Config) {
	client := &NotifyTargetGenModalClient{notifiedNew: &config.NotifiedTargetGenMode}
	uiShowModalDialog(NewModalDialogBox(client, p), true)
}

func drawActiveDialogBoxes() {
	for len(activeModalDialogs) > 0 {
		d := activeModalDialogs[0]
		if !d.closed {
			d.Draw()
			break
		} else {
			activeModalDialogs = activeModalDialogs[1:]
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
		width += imgui.CalcTextSize(t).X + 2*style.FramePadding().X
		if i > 0 {
			// space between buttons
			width += style.ItemSpacing().X
		}
	}
	offset := imgui.ContentRegionAvail().X - width
	imgui.SetCursorPos(imgui.Vec2{offset, imgui.CursorPosY()})
}

///////////////////////////////////////////////////////////////////////////

type ModalDialogBox struct {
	closed, isOpen bool
	client         ModalDialogClient
	platform       platform.Platform
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

// FixedSizeDialogClient is an optional interface that dialog clients can implement
// to specify a fixed window size instead of auto-resizing based on content.
type FixedSizeDialogClient interface {
	FixedSize() [2]float32 // Returns [width, height] in pixels (before DPI scaling)
}

func NewModalDialogBox(c ModalDialogClient, p platform.Platform) *ModalDialogBox {
	return &ModalDialogBox{client: c, platform: p}
}

func (m *ModalDialogBox) Draw() {
	if m.closed {
		return
	}

	title := fmt.Sprintf("%s##%p", m.client.Title(), m)
	imgui.OpenPopupStr(title)

	dpiScale := util.Select(runtime.GOOS == "windows", m.platform.DPIScale(), float32(1))
	windowSize := m.platform.WindowSize()

	// Check if client wants a fixed size window
	var flags imgui.WindowFlags
	if fixedSize, ok := m.client.(FixedSizeDialogClient); ok {
		// Fixed size dialog - don't auto-resize
		flags = imgui.WindowFlagsNoResize | imgui.WindowFlagsNoSavedSettings | imgui.WindowFlagsNoScrollbar
		size := fixedSize.FixedSize()
		imgui.SetNextWindowSize(imgui.Vec2{dpiScale * size[0], dpiScale * size[1]})
	} else {
		// Auto-resize dialog with constraints
		flags = imgui.WindowFlagsNoResize | imgui.WindowFlagsAlwaysAutoResize | imgui.WindowFlagsNoSavedSettings
		maxHeight := float32(windowSize[1]) * 19 / 20
		imgui.SetNextWindowSizeConstraints(imgui.Vec2{dpiScale * 850, dpiScale * 100}, imgui.Vec2{-1, maxHeight})
	}

	// Position the window near the top of the screen to ensure it doesn't extend below the bottom
	// Use a small margin from the top (5% of screen height)
	topMargin := float32(windowSize[1]) * 0.05
	imgui.SetNextWindowPosV(imgui.Vec2{float32(windowSize[0]) / 2, topMargin}, imgui.CondAlways, imgui.Vec2{0.5, 0})

	if imgui.BeginPopupModalV(title, nil, flags) {
		if !m.isOpen {
			imgui.SetKeyboardFocusHere()
			m.client.Opening()
			m.isOpen = true
		}

		selIndex := m.client.Draw()
		imgui.Text("\n") // spacing

		buttons := m.client.Buttons()

		// Only position buttons if we have any
		if len(buttons) > 0 {
			// First, figure out where to start drawing so the buttons end up right-justified.
			// https://github.com/ocornut/imgui/discussions/3862
			var allButtonText []string
			for _, b := range buttons {
				allButtonText = append(allButtonText, b.text)
			}
			setCursorForRightButtons(allButtonText)
		}

		for i, b := range buttons {
			if b.disabled {
				imgui.BeginDisabled()
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
				imgui.EndDisabled()
			}
		}
		imgui.EndPopup()
	}
}

// ScenarioSelectionModalClient handles Screen 1: scenario selection and sim type choice
type ScenarioSelectionModalClient struct {
	mgr         *client.ConnectionManager
	lg          *log.Logger
	simConfig   *NewSimConfiguration
	allowCancel bool
	platform    platform.Platform
	config      *Config
}

func (c *ScenarioSelectionModalClient) Title() string { return "New Simulation" }

func (c *ScenarioSelectionModalClient) Opening() {
	if c.simConfig == nil {
		c.simConfig = MakeNewSimConfiguration(c.mgr, &c.config.LastTRACON, &c.config.TFRCache, c.lg)
	}
}

func (c *ScenarioSelectionModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	if c.allowCancel {
		b = append(b, ModalDialogButton{text: "Cancel"})
	}

	next := ModalDialogButton{
		text:     c.simConfig.UIButtonText(),
		disabled: c.simConfig.ScenarioSelectionDisabled(c.config),
		action: func() bool {
			if c.simConfig.ShowConfigurationWindow() {
				// Go to configuration screen for create flows
				client := &ConfigurationModalClient{
					lg:          c.lg,
					simConfig:   c.simConfig,
					allowCancel: c.allowCancel,
					platform:    c.platform,
					config:      c.config,
					mgr:         c.mgr,
				}
				uiShowModalDialog(NewModalDialogBox(client, c.platform), false)
				return true
			} else {
				// Join flow - start directly
				c.simConfig.displayError = c.simConfig.Start(c.config)
				return c.simConfig.displayError == nil
			}
		},
	}

	return append(b, next)
}

func (c *ScenarioSelectionModalClient) Draw() int {
	if enter := c.simConfig.DrawScenarioSelectionUI(c.platform, c.config); enter {
		return util.Select(c.allowCancel, 1, 0)
	}
	return -1
}

// ConfigurationModalClient handles Screen 2: configuration options and traffic rates
type ConfigurationModalClient struct {
	mgr         *client.ConnectionManager
	lg          *log.Logger
	simConfig   *NewSimConfiguration
	allowCancel bool
	platform    platform.Platform
	config      *Config
}

func (c *ConfigurationModalClient) Title() string {
	return c.simConfig.Facility + " - " + c.simConfig.ScenarioName
}

func (c *ConfigurationModalClient) Opening() {}

func (c *ConfigurationModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton

	// Previous button - go back to scenario selection
	prev := ModalDialogButton{
		text: "Previous",
		action: func() bool {
			client := &ScenarioSelectionModalClient{
				mgr:         c.mgr,
				lg:          c.lg,
				simConfig:   c.simConfig,
				allowCancel: c.allowCancel,
				platform:    c.platform,
				config:      c.config,
			}
			uiShowModalDialog(NewModalDialogBox(client, c.platform), false)
			return true
		},
	}
	b = append(b, prev)

	if c.allowCancel {
		b = append(b, ModalDialogButton{text: "Cancel"})
	}

	// Create button
	create := ModalDialogButton{
		text:     "Create",
		disabled: c.simConfig.ConfigurationDisabled(c.config),
		action: func() bool {
			c.simConfig.displayError = c.simConfig.Start(c.config)
			return c.simConfig.displayError == nil
		},
	}
	b = append(b, create)

	return b
}

func (c *ConfigurationModalClient) Draw() int {
	if enter := c.simConfig.DrawConfigurationUI(c.platform, c.config); enter {
		return util.Select(c.allowCancel, 2, 1)
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

func checkForNewRelease(newReleaseDialogChan chan *NewReleaseModalClient, config *Config, lg *log.Logger) {
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

type WhatsNewModalClient struct {
	config *Config
}

func (wn *WhatsNewModalClient) Title() string {
	return "What's new in this version of vice"
}

func (wn *WhatsNewModalClient) Opening() {}

func (wn *WhatsNewModalClient) Buttons() []ModalDialogButton {
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
				wn.config.WhatsNewIndex = len(whatsNew)
				return true
			},
		},
	}
}

func (wn *WhatsNewModalClient) Draw() int {
	for i := wn.config.WhatsNewIndex; i < len(whatsNew); i++ {
		imgui.Text(renderer.FontAwesomeIconSquare + " " + whatsNew[i])
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

type DiscordOptInModalClient struct {
	config *Config
}

func (d *DiscordOptInModalClient) Title() string {
	return "Discord Activity Updates"
}

func (d *DiscordOptInModalClient) Opening() {}

func (d *DiscordOptInModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		ModalDialogButton{
			text: "Ok",
			action: func() bool {
				d.config.AskedDiscordOptIn = true
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
	imgui.Text("in the settings window " + renderer.FontAwesomeIconCog + " via the menu bar.")

	imgui.PopStyleVar()

	imgui.Text("")

	update := !d.config.InhibitDiscordActivity.Load()
	imgui.Checkbox("Update Discord activity status", &update)
	d.config.InhibitDiscordActivity.Store(!update)

	return -1
}

type NotifyTargetGenModalClient struct {
	notifiedNew *bool
}

func (ns *NotifyTargetGenModalClient) Title() string {
	return "Aircraft Control Command Entry Has Changed"
}

func (ns *NotifyTargetGenModalClient) Opening() {}

func (ns *NotifyTargetGenModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{
		ModalDialogButton{
			text: "Ok",
			action: func() bool {
				*ns.notifiedNew = true
				return true
			},
		},
	}
}

func (ns *NotifyTargetGenModalClient) Draw() int {
	style := imgui.CurrentStyle()
	spc := style.ItemSpacing()
	spc.Y -= 4
	imgui.PushStyleVarVec2(imgui.StyleVarItemSpacing, spc)

	imgui.Text(`Aircraft control commands are now entered in STARS and not in the messages`)
	imgui.Text(`window at the bottom of the screen. Enter a semicolon ";" to enable control`)
	imgui.Text(`command entry mode. Then, either enter a callsign followed by control commands`)
	imgui.Text(`or enter control commands and click on an aircraft's track to issue an instruction.`)

	imgui.PopStyleVar()

	return -1
}

///////////////////////////////////////////////////////////////////////////

type MessageModalClient struct {
	title   string
	message string
}

func (m *MessageModalClient) Title() string { return m.title }
func (m *MessageModalClient) Opening()      {}

func (m *MessageModalClient) Buttons() []ModalDialogButton {
	return []ModalDialogButton{{text: "Ok", action: func() bool { return true }}}
}

func (m *MessageModalClient) Draw() int {
	text, _ := util.TextWrapConfig{
		ColumnLimit: 80,
		WrapAll:     true,
	}.Wrap(m.message)
	imgui.Text("\n\n" + text + "\n\n")
	return -1
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
		imgui.Image(imgui.TextureID(sadTowerTextureID), imgui.Vec2{128, 128})

		imgui.TableNextColumn()
		text, _ := util.TextWrapConfig{
			ColumnLimit: 80,
			WrapAll:     true,
		}.Wrap(e.message)
		imgui.Text("\n\n" + text)

		imgui.EndTable()
	}
	return -1
}

func ShowErrorDialog(p platform.Platform, lg *log.Logger, s string, args ...any) {
	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)}, p)
	uiShowModalDialog(d, true)

	lg.Errorf(s, args...)
}

func ShowFatalErrorDialog(r renderer.Renderer, p platform.Platform, lg *log.Logger, s string, args ...any) {
	lg.Errorf(s, args...)

	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)}, p)

	for !d.closed {
		p.ProcessEvents()
		p.NewFrame()
		imgui.NewFrame()
		imgui.PushFont(&ui.font.Ifont)
		d.Draw()
		imgui.PopFont()

		imgui.Render()
		var cb renderer.CommandBuffer
		renderer.GenerateImguiCommandBuffer(&cb, p.DisplaySize(), p.FramebufferSize(), lg)
		r.RenderCommandBuffer(&cb)

		p.PostRender()
	}
	os.Exit(1)
}
