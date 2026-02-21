// ui.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image/png"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"

	"github.com/AllenDang/cimgui-go/imgui"
	implogl3 "github.com/AllenDang/cimgui-go/impl/opengl3"
	"github.com/ncruces/zenity"
	"github.com/pkg/browser"
)

var (
	ui struct {
		font           *renderer.Font
		fixedFont      *renderer.Font
		aboutFont      *renderer.Font
		aboutFontSmall *renderer.Font

		eventsSubscription *sim.EventsSubscription

		menuBarHeight float32

		showAboutDialog bool

		iconTextureID uint32

		newReleaseDialogChan chan *NewReleaseModalClient

		launchControlWindow *LaunchControlWindow

		// Scenario routes to draw on the scope
		showSettings      bool
		showScenarioInfo  bool
		showLaunchControl bool
		showMessages      bool
		showFlightStrips  bool

		// STT state
		pttRecording              bool
		pttGarbling               bool      // true if PTT pressed while audio was playing (no recording)
		pttMicFailed              bool      // true if mic open failed this press; cleared on release
		pttCapture                bool      // capturing new PTT key assignment
		pttPressTime              time.Time // for latency logging
		audioCaptureWarningLogged bool      // only log audio capture failure once
	}

	//go:embed icons/tower-256x256.png
	iconPNG string
)

func imguiInit() *imgui.Context {
	context := imgui.CreateContext()
	imgui.CurrentIO().SetIniFilename("")

	// Enable multi-viewport support so imgui windows can float outside the main window.
	io := imgui.CurrentIO()
	io.SetConfigFlags(io.ConfigFlags() | imgui.ConfigFlagsViewportsEnable)

	// Disable the nav windowing popup (Ctrl+Tab/Cmd+Tab window switcher) by
	// clearing the shortcut keys that trigger it.
	context.SetConfigNavWindowingKeyNext(imgui.KeyChord(imgui.KeyNone))
	context.SetConfigNavWindowingKeyPrev(imgui.KeyChord(imgui.KeyNone))

	// Only allow dragging windows by their title bars, not by clicking content.
	io.SetConfigWindowsMoveFromTitleBarOnly(true)

	// General imgui styling
	style := imgui.CurrentStyle()
	style.SetFrameRounding(2.)
	style.SetWindowRounding(4.)
	style.SetPopupRounding(4.)
	style.SetScrollbarSize(6.)
	style.ScaleAllSizes(1.25)

	return context
}

func uiInit(r renderer.Renderer, p platform.Platform, config *Config, es *sim.EventStream, lg *log.Logger) {
	if runtime.GOOS == "windows" {
		imgui.CurrentStyle().ScaleAllSizes(p.DPIScale())
	}

	ui.font = renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoRegular, Size: config.UIFontSize})
	ui.fixedFont = renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoMono, Size: config.UIFontSize + 2 /* better match regular size */})
	ui.aboutFont = renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoRegular, Size: 18})
	ui.aboutFontSmall = renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoRegular, Size: 14})
	ui.eventsSubscription = es.Subscribe()

	if iconImage, err := png.Decode(bytes.NewReader([]byte(iconPNG))); err != nil {
		lg.Errorf("Unable to decode icon PNG: %v", err)
	} else {
		ui.iconTextureID = r.CreateTextureFromImage(iconImage, false)
	}

	dialogsInit(r, lg)

	// Do this asynchronously since it involves network traffic and may
	// take some time (or may even time out, etc.)
	ui.newReleaseDialogChan = make(chan *NewReleaseModalClient)
	go checkForNewRelease(ui.newReleaseDialogChan, config, lg)

	if config.WhatsNewIndex < len(whatsNew) {
		uiShowModalDialog(NewModalDialogBox(&WhatsNewModalClient{config: config}, p), false)
	}

	if !config.AskedDiscordOptIn {
		uiShowDiscordOptInDialog(p, config)
	}
	if !config.NotifiedTargetGenMode {
		uiShowTargetGenCommandModeDialog(p, config)
	}

	// Restore which child windows were open in the previous session.
	ui.showSettings = config.ShowSettings
	ui.showLaunchControl = config.ShowLaunchCtrl
	ui.showScenarioInfo = config.ShowScenarioInfo
	ui.showMessages = config.ShowMessages
	ui.showFlightStrips = config.ShowFlightStrips
	keyboardWindowVisible = config.ShowKeyboardRef
}

func uiDraw(mgr *client.ConnectionManager, config *Config, p platform.Platform, r renderer.Renderer,
	controlClient *client.ControlClient, activeRadarPane panes.Pane, eventStream *sim.EventStream, lg *log.Logger) renderer.RendererStats {
	if ui.newReleaseDialogChan != nil {
		select {
		case dialog, ok := <-ui.newReleaseDialogChan:
			if ok {
				uiShowModalDialog(NewModalDialogBox(dialog, p), false)
			} else {
				// channel was closed
				ui.newReleaseDialogChan = nil
			}
		default:
			// don't block on the chan if there's nothing there and it's still open...
		}
	}

	ui.font.ImguiPush()
	if imgui.BeginMainMenuBar() {
		imgui.PushStyleColorVec4(imgui.ColButton, imgui.CurrentStyle().Colors()[imgui.ColMenuBarBg])

		if controlClient != nil && controlClient.Connected() {
			if controlClient.State.Paused {
				if imgui.Button(renderer.FontAwesomeIconPlayCircle) {
					controlClient.ToggleSimPause()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Resume simulation")
				}
			} else {
				if imgui.Button(renderer.FontAwesomeIconPauseCircle) {
					controlClient.ToggleSimPause()
				}
				if imgui.IsItemHovered() {
					imgui.SetTooltip("Pause simulation")
				}
			}

			if controlClient.State.Paused {
				imgui.BeginDisabled()
			}
			if imgui.Button(renderer.FontAwesomeIconFastForward) {
				controlClient.FastForward()
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Advance simulation by 15 seconds")
			}
			if controlClient.State.Paused {
				imgui.EndDisabled()
			}
		}

		if imgui.Button(renderer.FontAwesomeIconRedo) {
			uiShowConnectOrBenchmarkDialog(mgr, true, config, p, lg)
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Start new simulation")
		}

		if controlClient != nil && controlClient.Connected() {
			if imgui.Button(renderer.FontAwesomeIconCog) {
				ui.showSettings = !ui.showSettings
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Open settings window")
			}

			if imgui.Button(renderer.FontAwesomeIconQuestionCircle) {
				ui.showScenarioInfo = !ui.showScenarioInfo
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Show departures, arrivals, approaches, overflights, and airspace awareness")
			}
		}

		if imgui.Button(renderer.FontAwesomeIconKeyboard) {
			uiToggleShowKeyboardWindow()
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Show summary of keyboard commands")
		}

		flashDep := controlClient != nil && !ui.showLaunchControl &&
			len(controlClient.State.GetRegularReleaseDepartures()) > 0 && (time.Now().UnixMilli()/500)&1 == 1
		if flashDep {
			imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{0, .8, 0, 1})
		}
		if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
			ui.showLaunchControl = !ui.showLaunchControl
		}
		if flashDep {
			imgui.PopStyleColor()
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Control spawning new aircraft and grant departure releases")
		}

		if controlClient != nil && controlClient.Connected() {
			if imgui.Button(renderer.FontAwesomeIconComment) {
				ui.showMessages = !ui.showMessages
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Toggle messages window")
			}

			if imgui.Button(renderer.FontAwesomeIconClipboardList) {
				ui.showFlightStrips = !ui.showFlightStrips
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Toggle flight strips window")
			}
		}

		if imgui.Button(renderer.FontAwesomeIconBook) {
			browser.OpenURL("https://pharr.org/vice/index.html")
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Display online vice documentation")
		}

		// Handle PTT key for STT recording
		uiHandlePTTKey(p, controlClient, config, lg)

		// Position for right-side icons (add space for mic icon when recording/garbling)
		width, _ := ui.font.BoundText(renderer.FontAwesomeIconInfoCircle, 0)
		numIcons := 6
		if ui.pttRecording || ui.pttGarbling {
			numIcons = 7
		}
		displaySize := imgui.CurrentIO().DisplaySize()
		imgui.SetCursorPos(imgui.Vec2{X: displaySize.X - float32(numIcons*width+15), Y: 0})

		// Show microphone icon while recording (red) or garbling (yellow)
		if ui.pttRecording {
			imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, 0, 0, 1})
			imgui.TextUnformatted(renderer.FontAwesomeIconMicrophone)
			imgui.PopStyleColor()
			imgui.SameLine()
		} else if ui.pttGarbling {
			imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{1, 1, 0, 1})
			imgui.TextUnformatted(renderer.FontAwesomeIconMicrophone)
			imgui.PopStyleColor()
			imgui.SameLine()
		}

		if imgui.Button(renderer.FontAwesomeIconInfoCircle) {
			ui.showAboutDialog = !ui.showAboutDialog
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Display information about vice")
		}
		if imgui.Button(renderer.FontAwesomeIconDiscord) {
			browser.OpenURL("https://discord.gg/y993vgQxhY")
		}

		if imgui.Button(util.Select(p.IsFullScreen(), renderer.FontAwesomeIconCompressAlt, renderer.FontAwesomeIconExpandAlt)) {
			p.EnableFullScreen(!p.IsFullScreen())
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip(util.Select(p.IsFullScreen(), "Exit", "Enter") + " full-screen mode")
		}

		imgui.PopStyleColor()

		imgui.EndMainMenuBar()
	}
	ui.menuBarHeight = imgui.CursorPos().Y - 1

	if controlClient != nil {
		uiDrawSettingsWindow(controlClient, config, activeRadarPane, p, lg)

		if ui.showScenarioInfo {
			ui.showScenarioInfo = drawScenarioInfoWindow(config, controlClient, activeRadarPane, p, lg)
		}

		if ui.showLaunchControl {
			if ui.launchControlWindow == nil {
				ui.launchControlWindow = MakeLaunchControlWindow(controlClient, lg)
			}
			ui.launchControlWindow.Draw(eventStream, p)
		}

		if ui.showMessages {
			config.MessagesPane.DrawWindow(&ui.showMessages, controlClient, p, lg)
		}
		if ui.showFlightStrips {
			config.FlightStripPane.DrawWindow(&ui.showFlightStrips, controlClient, p, lg)
		}
	}

	for _, event := range ui.eventsSubscription.Get() {
		if event.Type == sim.ServerBroadcastMessageEvent {
			uiShowModalDialog(NewModalDialogBox(&BroadcastModalDialog{Message: event.WrittenText}, p), false)
		}
	}

	drawActiveDialogBoxes()

	uiDrawKeyboardWindow(controlClient, config, p)

	imgui.PopFont()

	// Finalize and submit the imgui draw lists
	imgui.Render()

	// Use the OpenGL 3 backend for all imgui rendering. Both main and
	// secondary viewports use the same code path, eliminating DPI
	// discrepancies between our custom OGL2 renderer and imgui's OGL3 backend.
	implogl3.RenderDrawData(imgui.CurrentDrawData())
	renderer.SyncFontAtlasTexID()

	// Update and render secondary viewport windows (floating OS windows).
	io := imgui.CurrentIO()
	if io.ConfigFlags()&imgui.ConfigFlagsViewportsEnable != 0 {
		imgui.UpdatePlatformWindows()
		imgui.RenderPlatformWindowsDefault()
		p.MakeContextCurrent()
	}

	return renderer.RendererStats{}
}

func uiResetControlClient(c *client.ControlClient, p platform.Platform, lg *log.Logger) {
	ui.launchControlWindow = nil
	clear(acknowledgedATIS)
}

///////////////////////////////////////////////////////////////////////////
// "about" dialog box

func showAboutDialog() {
	flags := imgui.WindowFlagsAlwaysAutoResize | imgui.WindowFlagsNoSavedSettings
	imgui.BeginV("About vice...", &ui.showAboutDialog, flags)

	imgui.Image(*imgui.NewTextureRefTextureID(imgui.TextureID(ui.iconTextureID)), imgui.Vec2{256, 256})

	center := func(s string) {
		// https://stackoverflow.com/a/67855985
		ww := imgui.WindowSize().X
		tw := imgui.CalcTextSize(s).X
		imgui.SetCursorPos(imgui.Vec2{(ww - tw) * 0.5, imgui.CursorPosY()})
		imgui.Text(s)
	}

	ui.aboutFont.ImguiPush()
	center("vice")
	center(renderer.FontAwesomeIconCopyright + "2023-2025 Matt Pharr")
	center("Licensed under the GPL, Version 3")
	if imgui.IsItemHovered() && imgui.IsMouseClickedBool(imgui.MouseButton(0)) {
		browser.OpenURL("https://www.gnu.org/licenses/gpl-3.0.html")
	}
	center("Source code: " + renderer.FontAwesomeIconGithub)
	if imgui.IsItemHovered() && imgui.IsMouseClickedBool(imgui.MouseButton(0)) {
		browser.OpenURL("https://github.com/mmp/vice")
	}
	imgui.PopFont()

	imgui.Separator()

	ui.aboutFontSmall.ImguiPush()
	credits := `Additional credits:
- Software Development: Xavier Caldwell, Artem Dorofeev, Adam E, Dennis Graiani, Michael Knight, Ethan Malimon, Neel P, Makoto Sakaguchi, Michael Trokel, radarcontacto, Rick R, Samuel Valencia, Jordan Williams, and Yi Zhang.
- Timely feedback: radarcontacto.
- Facility engineering: Connor Allen, anguse, Adam Bolek, Brody Carty, Lucas Chan, Aaron Flett, Mike Fries, Ryan G, Gecko, Thomas Halpin, Jason Helkenberg, Trey Hensley, Elijah J, Austin Jenkins, Ketan K, Mike K, Allison L, Josh Lambert, Kayden Lambert, Mike LeGall, Jonah Lefkoff, Jud Lopez, Jake Magee, Ethan Malimon, manaphy, Jace Martin, Michael McConnell, Merry, Yahya Nazimuddin, Justin Nguyen, Giovanni, Andrew S, Logan S, Arya T, Nelson T, Tyler Temerowski, Eli Thompson, Michael Trokel, Samuel Valencia, Gavin Velicevic, and Jackson Verdoorn.
- Video maps: thanks to the ZAU, ZBW, ZDC, ZDV, ZHU, ZID, ZJX, ZLA, ZMP, ZNY, ZOB, ZSE, and ZTL VATSIM ARTCCs and to the FAA, from whence the original maps came.
- Additionally: OpenScope for the aircraft performance and airline databases, ourairports.com for the airport database, and for the FAA for being awesome about providing the CIFP, MVA specifications, and other useful aviation data digitally.
- One more thing: see the file CREDITS.txt in the vice source code distribution for third-party software, fonts, sounds, etc.`

	imgui.PushTextWrapPos()
	imgui.Text(credits)
	imgui.PopTextWrapPos()

	imgui.PopFont()

	imgui.End()
}

///////////////////////////////////////////////////////////////////////////

var keyboardWindowVisible bool
var selectedCommandTypes string

func uiToggleShowKeyboardWindow() {
	keyboardWindowVisible = !keyboardWindowVisible
}

var primaryAcCommands = [][3]string{
	{"*H_hdg", `"Fly heading _hdg_." If no heading is given, "fly present heading".`,
		"*H050*, *H*"},
	{"*D_fix", `"Proceed direct _fix_".`, "*DWAVEY*"},
	{"*C_alt", `"Climb and maintain _alt_".`, "*C170*"},
	{"*TC_alt", `"After reaching speed _kts_, climb and maintain _alt_", where _kts_ is a previously-assigned speed.`, "*TC170*"},
	{"*D_alt", `"Descend and maintain _alt_".`, "*D20*"},
	{"*TD_alt", `"Descend and maintain _alt_ after reaching _kts_ knots", where _kts_ is a previously-assigned
speed. (*TD* = 'then descend')`, "*TD20*"},
	{"*S_kts", `"Reduce/increase speed to _kts_."
If no speed is given, "cancel speed restrictions".`, "*S210*, *S*"},
	{"*TS_kts", `"After reaching _alt_, reduce/increase speed to _kts_", where _alt_ is a previously-assigned
altitude. (*TS* = 'then speed')`, "*TS210*"},
	{"*E_appr", `"Expect the _appr_ approach."`, "*EI2L*"},
	{"*C_appr", `"Cleared _appr_ approach."`, "*CI2L*"},
	{"*C*", `"Cleared for the approach that was previously assigned."`, "*C*"},
	{"*TO*", `"Contact tower"`, "*TO*"},
	{"*FC*", `"Contact _ctrl_ on _freq_, where _ctrl_ is the controller who has the track and _freq_ is their frequency."`, "*FC*"},
	{"*CT_tcp*", `"Contact the controller identified by TCP _tcp_."`, "*CT2J*"},
	{"*X*", "(Deletes the aircraft.)", "*X*"},
}

var secondaryAcCommands = [][3]string{
	{"*L_hdg", `"Turn left heading _hdg_."`, "*L130*"},
	{"*T_deg*L", `"Turn _deg_ degrees left."`, "*T10L*"},
	{"*R_hdg", `"Turn right heading _hdg_".`, "*R210*"},
	{"*T_deg*R", `"Turn _deg_ degrees right".`, "*T20R*"},
	{"*D_fix*/H_hdg", `"Depart _fix_ heading _hdg_".`, "*DLENDY/H180*"},
	{"*H_fix*", `"Hold at _fix_ (published hold)".`, "*HJIMEE*"},
	{"*H_fix*/[opts]",
		`"Hold at _fix_ (controller-specified)." Options: *L*/*R* (turns), *xxNM*/*xxM* (legs), *Rxxx* (radial, req'd).`, "*HJIMEE/L/5NM/R090*"},
	{"*C_fix*/A_alt*/S_kts*/M_mach*",
		`"Cross _fix_ at _alt_ / _kts_ knots / mach." Any combination of *A*, *S*, and *M* may be specified.`, "*CCAMRN/A110+*"},
	{"*ED*", `"Expedite descent"`, "*ED*"},
	{"*EC*", `"Expedite climb"`, "*EC*"},
	{"*SMIN*", `"Maintain slowest practical speed".`, "*SMIN*"},
	{"*SMAX*", `"Maintain maximum forward speed".`, "*SMAX*"},
	{"*SPRES*", `"Maintain present speed".`, "*SPRES*"},
	{"*S_kts*+", `"Maintain _kts_ knots or greater" (speed floor). Currently treated as standard speed assignment.`, "*S180+*"},
	{"*S_kts*-", `"Do not exceed _kts_ knots" (speed ceiling). Currently treated as standard speed assignment.`, "*S180-*"},
	{"*SS*", `"Say airspeed".`, "*SS*"},
	{"*SA*", `"Say altitude".`, "*SA*"},
	{"*SH*", `"Say heading".`, "*SH*"},
	{"*SQ_code", `"Squawk _code_."`, "*SQ1200*"},
	{"*SQS", `"Squawk standby."`, "*SQS*"},
	{"*SQA", `"Squawk altitude."`, "*SQA*"},
	{"*SQON", `"Squawk on."`, "*SSON*"},
	{"*A_fix*/C[_appr]", `"At _fix_, cleared [_appr_] approach." (approach is optional)`, "*AROSLY/C*"},
	{"*A_fix*/I", `"At _fix_, intercept the localizer."`, "*AROSLY/I*"},
	{"*CAC*", `"Cancel approach clearance".`, "*CAC*"},
	{"*CSI_appr", `"Cleared straight-in _appr_ approach.`, "*CSII6*"},
	{"*I*", `"Intercept the localizer."`, "*I*"},
	{"*ID*", `"Ident."`, "*ID*"},
	{"*CVS*", `"Climb via the SID"`, "*CVS*"},
	{"*DVS*", `"Descend via the STAR"`, "*CVS*"},
	{"*RON*", `"Resume own navigation" (VFR)`, "*RON*"},
	{"*A*", `"Altitude your discretion, maintain VFR" (VFR)`, "*A*"},
	{"*A_alt*", `"Maintain _alt_`, "*A120*"},
	{"*ATIS/_ltr*", `"Advise you have information _ltr_." If the pilot already reported the correct ATIS, no readback.`, "*ATIS/B*"},
	{"*RST*", `"Radar services terminated, squawk VFR, frequency change approved" (VFR)`, "*RST*"},
	{"*GA*", `"Go ahead" (VFR) - respond to abbreviated VFR request`, "*GA*"},
	{"*P*", `Pauses/unpauses the sim`, "*P*"},
	{"*/_message*", `Displays a message to all controllers`, "*/DINNER TIME 2A CLOSED*"},
}

var starsCommands = [][2]string{
	{"@", `If the aircraft is an inbound handoff, accept the handoff.
If the aircraft has been handed off to another controller who has accepted
the handoff, transfer control to the other controller.`},
	{"*[F3] @", `Initiate track of an untracked aircraft.`},
	{"_id_ @", `Handoff aircraft to the controller identified by _id_.`},
	{". @", `Clear aircraft's scratchpad.`},
	{"*[F7]Y_scr_ @", `Set aircraft's scratchpad to _scr_ (3 character limit).`},
	{"+_alt_ @", `Set the temporary altitude in the aircraft's datablock to _alt_,
which must be 3 digits (e.g., *040*).`},
	{"_id_\\* @", `Point out the aircraft to the controller identified by _id_.`},
}

// draw the windows that shows the available keyboard commands
func uiDrawKeyboardWindow(c *client.ControlClient, config *Config, platform platform.Platform) {
	if !keyboardWindowVisible {
		return
	}

	imgui.SetNextWindowSizeConstraints(imgui.Vec2{300, 300}, imgui.Vec2{-1, float32(platform.WindowSize()[1]) * 19 / 20})
	imgui.BeginV("Keyboard Command Reference", &keyboardWindowVisible, imgui.WindowFlagsAlwaysAutoResize)

	style := imgui.CurrentStyle()

	// Initial line with a link to the website
	imgui.Text("See the ")
	imgui.SameLineV(0, 0)
	imgui.PushStyleColorVec4(imgui.ColText, imgui.Vec4{0.4, 0.6, 1, 1})
	imgui.Text("vice website")
	// Underline the link
	min, max := imgui.ItemRectMin(), imgui.ItemRectMax()
	color := imgui.CurrentStyle().Colors()[imgui.ColText]
	imgui.WindowDrawList().AddLine(imgui.Vec2{min.X, max.Y}, max, imgui.ColorU32Vec4(color))
	if imgui.IsItemHovered() && imgui.IsMouseClickedBool(imgui.MouseButton(0)) {
		browser.OpenURL("https://pharr.org/vice/")
	}
	imgui.PopStyleColor()
	imgui.SameLineV(0, 0)
	imgui.Text(" for full documentation of vice's keyboard commands.")

	imgui.Separator()

	fixedFont := renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoMono, Size: config.UIFontSize})
	italicFont := renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoMonoItalic, Size: config.UIFontSize})

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
		if imgui.SelectableBoolV(ACControlPrimary, selectedCommandTypes == ACControlPrimary, 0, imgui.Vec2{}) {
			selectedCommandTypes = ACControlPrimary
		}
		if imgui.SelectableBoolV(ACControlSecondary, selectedCommandTypes == ACControlSecondary, 0, imgui.Vec2{}) {
			selectedCommandTypes = ACControlSecondary
		}
		if imgui.SelectableBoolV(STARS, selectedCommandTypes == STARS, 0, imgui.Vec2{}) {
			selectedCommandTypes = STARS
		}
		imgui.EndCombo()
	}

	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg |
		imgui.TableFlagsSizingStretchProp
	if selectedCommandTypes == ACControlPrimary || selectedCommandTypes == ACControlSecondary {
		imgui.Text("\n")
		uiDrawMarkedupText(ui.font, fixedFont, italicFont, `
To issue a command to an aircraft, first type *;* to enter "TGT GEN" mode; *TG* will
appear in the preview area in the STARS window. Then enter one or more commands and click
on an aircraft to issue the commands to it. Alternatively, first enter the aircraft's
callsign with a space after it and then enter a command. Multiple commands may be given
separated by spaces.
`)
		imgui.Text("\n\n")
		uiDrawMarkedupText(ui.font, fixedFont, italicFont, `
Note that all altitudes should be specified in hundreds of feet and speed/altitude changes happen
simultaneously unless the *TC*, *TD*, or *TS* commands are used to specify the change to be done
after the first.`)
		imgui.Text("\n\n")

		if c != nil {
			var apprNames []string
			for _, rwy := range c.State.ArrivalRunways {
				ap := c.State.Airports[rwy.Airport]
				for _, name := range util.SortedMapKeys(ap.Approaches) {
					appr := ap.Approaches[name]
					if appr.Runway == rwy.Runway.Base() {
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

			cmds := util.Select(selectedCommandTypes == ACControlPrimary, primaryAcCommands, secondaryAcCommands)
			for _, cmd := range cmds {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, cmd[0])
				imgui.TableNextColumn()
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, cmd[1])
				imgui.TableNextColumn()
				uiDrawMarkedupText(ui.font, fixedFont, italicFont, cmd[2])
			}
			imgui.EndTable()
		}
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
func uiDrawMarkedupText(regularFont *renderer.Font, fixedFont *renderer.Font, italicFont *renderer.Font, str string) {
	// regularFont is the default and starting point
	regularFont.ImguiPush()

	// textWidth approximates the width of the given string in pixels; it
	// may slightly over-estimate the width, but that's fine since we use
	// it to decide when to wrap lines of text.
	textWidth := func(s string) float32 {
		s = strings.Trim(s, `_*\`) // remove markup characters
		fixedFont.ImguiPush()
		sz := imgui.CalcTextSize(s)
		imgui.PopFont()
		return sz.X
	}

	fixed, italic := false, false
	// Split the string into words. Note that this doesn't preserve extra
	// spacing from multiple spaces or respect embedded newlines.
	for word := range strings.FieldsSeq(str) {
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
				s += renderer.FontAwesomeIconMouse

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
					fixedFont.ImguiPush()
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
					italicFont.ImguiPush()
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

func uiDrawSettingsWindow(c *client.ControlClient, config *Config, activeRadarPane panes.Pane, p platform.Platform, lg *log.Logger) {
	if !ui.showSettings {
		return
	}

	imgui.BeginV("Settings", &ui.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	if imgui.SliderFloatV("Simulation speed", &c.State.SimRate, 1, 20, "%.1f", 0) {
		c.SetSimRate(c.State.SimRate)
	}

	update := !config.InhibitDiscordActivity.Load()
	imgui.Checkbox("Update Discord activity status", &update)
	config.InhibitDiscordActivity.Store(!update)

	if c != nil {
		imgui.Checkbox("Disable text-to-speech", &config.DisableTextToSpeech)
	}

	imgui.Separator()

	if imgui.BeginComboV("UI Font Size", strconv.Itoa(config.UIFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := renderer.AvailableFontSizes(renderer.RobotoRegular)
		for _, size := range sizes {
			if imgui.SelectableBoolV(strconv.Itoa(size), size == config.UIFontSize, 0, imgui.Vec2{}) {
				config.UIFontSize = size
				ui.font = renderer.GetFont(renderer.FontIdentifier{Name: renderer.RobotoRegular, Size: config.UIFontSize})
			}
		}
		imgui.EndCombo()
	}

	imgui.Separator()

	if imgui.CollapsingHeaderBoolPtr("Display", nil) {
		if imgui.Checkbox("Enable anti-aliasing", &config.EnableMSAA) {
			uiShowModalDialog(NewModalDialogBox(
				&MessageModalClient{
					title: "Alert",
					message: "You must restart vice for changes to the anti-aliasing " +
						"mode to take effect.",
				}, p), true)
		}

		imgui.Checkbox("Start in full-screen", &config.StartInFullScreen)

		monitorNames := p.GetAllMonitorNames()
		if imgui.BeginComboV("Monitor", monitorNames[config.FullScreenMonitor], imgui.ComboFlagsHeightLarge) {
			for index, monitor := range monitorNames {
				label := fmt.Sprintf("%s##monitor%d", monitor, index)
				if imgui.SelectableBoolV(label, monitor == monitorNames[config.FullScreenMonitor], 0, imgui.Vec2{}) {
					config.FullScreenMonitor = index

					p.EnableFullScreen(p.IsFullScreen())
				}
			}

			imgui.EndCombo()
		}
	}

	if imgui.CollapsingHeaderBoolPtr("Speech to Text", nil) {
		// Push-to-talk key
		if config.UserPTTKey == imgui.KeyNone {
			config.UserPTTKey = imgui.KeySemicolon
		}
		keyName := platform.GetImGuiKeyName(config.UserPTTKey)

		imgui.Text("Push-to-Talk Key: ")
		imgui.SameLine()
		imgui.TextColored(imgui.Vec4{0, 1, 1, 1}, keyName)

		if ui.pttCapture {
			imgui.TextColored(imgui.Vec4{1, 1, 0, 1}, "Press any key for Push-to-Talk...")
			if kb := p.GetKeyboard(); kb != nil {
				for key := range kb.Pressed {
					config.UserPTTKey = key
					ui.pttCapture = false
					break
				}
			}
		} else {
			imgui.SameLine()
			if imgui.Button("Change Key") {
				ui.pttCapture = true
			}
			imgui.SameLine()
			if imgui.Button("Clear") {
				config.UserPTTKey = imgui.KeyNone
			}
		}

		// Microphone selection
		imgui.Text("Microphone:")
		imgui.SameLine()
		micName := config.SelectedMicrophone
		cleanMic := func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ' {
				return r
			}
			return -1
		}
		if micName == "" {
			micName = "Default"
		}
		micName = strings.Map(cleanMic, micName)
		if imgui.BeginComboV("##microphone", micName, 0) {
			if imgui.SelectableBoolV("Default", config.SelectedMicrophone == "", 0, imgui.Vec2{}) {
				config.SelectedMicrophone = ""
			}
			mics := p.GetAudioInputDevices()
			for i, mic := range mics {
				micFormatted := fmt.Sprintf("%s##mic%d", strings.Map(cleanMic, mic), i)
				if imgui.SelectableBoolV(micFormatted, mic == config.SelectedMicrophone, 0, imgui.Vec2{}) {
					config.SelectedMicrophone = mic
				}
			}
			imgui.EndCombo()
		}

		// Whisper model selection dropdown
		if modelName := client.GetWhisperModelName(); modelName != "" {
			imgui.Text("Model:")
			imgui.SameLine()
			// Format display name (remove ggml- prefix and .bin suffix for readability)
			displayName := modelName
			displayName = strings.TrimPrefix(displayName, "ggml-")
			displayName = strings.TrimSuffix(displayName, ".bin")
			if imgui.BeginComboV("##whispermodel", displayName, 0) {
				// Auto option runs benchmark
				if imgui.SelectableBoolV("Auto (Benchmark)", false, 0, imgui.Vec2{}) {
					client.ForceWhisperRebenchmark(lg, func(modelName, deviceID string, benchmarkIndex int, realtimeFactor float64) {
						config.WhisperModelName = modelName
						config.WhisperDeviceID = deviceID
						config.WhisperBenchmarkIndex = benchmarkIndex
						config.WhisperRealtimeFactor = realtimeFactor
					})
					benchClient := &rebenchmarkModalClient{config: config, lg: lg}
					uiShowModalDialog(NewModalDialogBox(benchClient, p), false)
				}
				imgui.Separator()
				// Individual model options
				for _, model := range client.GetWhisperModelTiers() {
					modelDisplay := strings.TrimPrefix(model, "ggml-")
					modelDisplay = strings.TrimSuffix(modelDisplay, ".bin")
					isSelected := model == modelName
					if imgui.SelectableBoolV(modelDisplay, isSelected, 0, imgui.Vec2{}) {
						if model != modelName {
							client.SelectWhisperModel(lg, model, func(modelName, deviceID string, benchmarkIndex int, realtimeFactor float64) {
								config.WhisperModelName = modelName
								config.WhisperDeviceID = deviceID
								config.WhisperBenchmarkIndex = benchmarkIndex
								config.WhisperRealtimeFactor = realtimeFactor
							})
						}
					}
				}
				imgui.EndCombo()
			}
		}

		if p.IsAudioRecording() {
			imgui.TextColored(imgui.Vec4{1, 0, 0, 1}, "Recording...")
		} else {
			if transcription := c.GetLastTranscription(); transcription != "" {
				durationMs := c.GetLastWhisperDurationMs()
				imgui.Text(fmt.Sprintf("Last transcription (%dms):", durationMs))
				imgui.TextWrapped(transcription)
			}
			if lastCmd := c.GetLastCommand(); lastCmd != "" {
				imgui.Text("Last command:")
				imgui.TextWrapped(lastCmd)
			}
		}
	}

	if imgui.CollapsingHeaderBoolPtr("Scenario Files", nil) {
		imgui.BeginGroup()
		imgui.Text("For testing new scenarios, an additional scenario and/or video map file can be specified.")
		imgui.Text("Note that vice must be restarted to reload scenarios after they are changed.")
		imgui.Separator()
		imgui.Text(fmt.Sprintf("Scenario: %s", util.Select(config.ScenarioFile != "", config.ScenarioFile, "None Selected")))
		imgui.SameLine()
		if imgui.Button("Select##scenario") {
			path, err := zenity.SelectFile(
				zenity.Title("Select Scenario JSON File"),
				zenity.FileFilters{
					{
						Name:     "JSON Files",
						Patterns: []string{"*.json"},
					},
				},
			)
			if err != nil {
				fmt.Printf("Error selecting scenario file: %v\n", err)
			} else {
				config.ScenarioFile = path
			}
		}
		imgui.SameLine()
		if imgui.Button("Clear##scenario") {
			config.ScenarioFile = ""
		}
		imgui.EndGroup()

		imgui.BeginGroup()
		imgui.Text(fmt.Sprintf("Video Map: %s", util.Select(config.VideoMapFile != "", config.VideoMapFile, "None Selected")))
		imgui.SameLine()
		if imgui.Button("Select##videoMap") {
			path, err := zenity.SelectFile(
				zenity.Title("Select Video Map JSON File"),
				zenity.FileFilters{
					{
						Name:     "Video Map JSON Files",
						Patterns: []string{"*.json"},
					},
				},
			)
			if err != nil {
				fmt.Printf("Error selecting video map file: %v\n", err)
			} else {
				config.VideoMapFile = path
			}
		}
		imgui.SameLine()
		if imgui.Button("Clear##videoMap") {
			config.VideoMapFile = ""
		}
		imgui.EndGroup()
	}

	if imgui.CollapsingHeaderBoolPtr(config.MessagesPane.DisplayName(), nil) {
		config.MessagesPane.DrawUI(p, &config.Config)
	}
	if imgui.CollapsingHeaderBoolPtr(config.FlightStripPane.DisplayName(), nil) {
		config.FlightStripPane.DrawUI(p, &config.Config)
	}
	if draw, ok := activeRadarPane.(panes.UIDrawer); ok {
		if imgui.CollapsingHeaderBoolPtr(draw.DisplayName(), nil) {
			draw.DrawUI(p, &config.Config)
		}
	}

	imgui.End()
}

// uiHandlePTTKey handles push-to-talk key input for STT recording.
func uiHandlePTTKey(p platform.Platform, controlClient *client.ControlClient, config *Config, lg *log.Logger) {
	pttKey := config.UserPTTKey
	if pttKey == imgui.KeyNone {
		return
	}

	// Ensure background capture is running for preroll buffer.
	// This captures audio continuously so we don't lose the start of transmissions.
	if !p.IsAudioCapturing() {
		if err := p.StartAudioCaptureWithDevice(config.SelectedMicrophone); err != nil {
			// Log but don't block - recording will still work, just without preroll
			if !ui.audioCaptureWarningLogged {
				lg.Warnf("Failed to start background audio capture: %v", err)
				ui.audioCaptureWarningLogged = true
			}
		}
	}

	// Start on initial press (ignore repeats by checking our own flags)
	if imgui.IsKeyDown(pttKey) && !ui.pttRecording && !ui.pttGarbling && !ui.pttMicFailed {
		if p.IsPlayingSpeech() {
			// Audio is playing - garble it instead of recording
			p.SetSpeechGarbled(true)
			ui.pttGarbling = true
			lg.Infof("Push-to-talk: Garbling audio (pressed during playback)")
		} else {
			ui.pttPressTime = time.Now()

			// Get preroll samples before starting recording (if capture is active)
			preroll := p.GetAudioPreroll()

			// No audio playing - start recording
			if err := p.StartAudioRecordingWithDevice(config.SelectedMicrophone); err != nil {
				var hint string
				switch runtime.GOOS {
				case "darwin":
					hint = "Please check System Settings -> Privacy & Security -> Microphone and ensure vice has permission."
				case "windows":
					hint = `Please check Settings -> Privacy & Security -> Microphone and ensure "Let desktop apps access your microphone" is enabled.`
				default:
					hint = "Please check your system's audio settings and ensure microphone access is permitted."
				}
				ShowErrorDialog(p, lg, "Unable to access microphone: %v\n\n%s", err, hint)
				ui.pttMicFailed = true
			} else {
				ui.pttRecording = true
				if controlClient != nil {
					// Start streaming transcription
					if err := controlClient.StartStreamingSTT(lg); err != nil {
						lg.Errorf("Failed to start streaming STT: %v", err)
					} else {
						// Feed preroll samples to transcriber first (audio from before PTT press)
						if len(preroll) > 0 {
							controlClient.FeedAudioToStreaming(preroll)
							lg.Debugf("Fed %d preroll samples to transcriber", len(preroll))
						}
						// Set up audio streaming callback to feed new samples to transcriber
						p.SetAudioStreamCallback(func(samples []int16) {
							controlClient.FeedAudioToStreaming(samples)
						})
					}
				}
				lg.Infof("Push-to-talk: Started recording (streaming)")
			}
		}
	}

	// Detect release
	if !imgui.IsKeyDown(pttKey) {
		ui.pttMicFailed = false
		if ui.pttGarbling {
			// Was garbling - stop garbling
			p.SetSpeechGarbled(false)
			ui.pttGarbling = false
			lg.Infof("Push-to-talk: Stopped garbling")
		}
		if ui.pttRecording {
			// Clear streaming callback first
			p.SetAudioStreamCallback(nil)

			// Stop SDL audio device
			if p.IsAudioRecording() {
				p.StopAudioRecording()
			}

			// Stop streaming and process final result (synchronous to avoid race
			// if user quickly presses PTT again)
			if controlClient != nil {
				controlClient.StopStreamingSTT(lg)
			}

			ui.pttRecording = false
			lg.Infof("Push-to-talk: Stopped recording, processing streaming result...")
		}
	}
}
