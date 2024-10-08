// ui.go
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
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
	"github.com/pkg/browser"
)

var (
	ui struct {
		font           *renderer.Font
		aboutFont      *renderer.Font
		aboutFontSmall *renderer.Font

		eventsSubscription *sim.EventsSubscription

		menuBarHeight float32

		showAboutDialog bool

		iconTextureID     uint32
		sadTowerTextureID uint32

		activeModalDialogs []*ModalDialogBox

		newReleaseDialogChan chan *NewReleaseModalClient

		launchControlWindow  *LaunchControlWindow
		missingPrimaryDialog *ModalDialogBox

		// Scenario routes to draw on the scope
		showSettings     bool
		showScenarioInfo bool
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
		"Clicking the " + renderer.FontAwesomeIconKeyboard + " icon on the menubar gives a summary of vice's keyboard commands",
		"Fixed bug with aircraft descending too early when flying procedure turns",
		"Fixed bug with some departures trying to re-fly their initial departure route",
		"Fixed multiple bugs with the handling of \"at or above\" altitude constraints",
		"Fixed bug with the default DCB brightness being set to 0",
		"Added DCA scenario",
		"There is now a short delay before aircraft start to follow heading assignments",
		"Added \"ID\" command for ident",
		"Aircraft can now also be issued control commands by entering their callsign before the commands",
		"Fixed bugs with endless go-arounds and with departures not obeying altitude restrictions",
		"Fixed a bug that caused vice to sometimes crash after aircraft were given approach clearance",
		"Fixed a bug where descending aircraft would stop descending when given approach clearance",
		"Small fixes to the DCA scenario",
		"Polished up handling of early hand-offs of departures in the STARS scope",
		"Added L30 (Las Vegas) scenarios and a combined N90 (JFK+LGA+EWR) scenario",
		"Important readbacks from pilots are now highlighted in red",
		"Improved STARS *T to allow entering fix names and to show ETA",
		"Added support for charted visual approaches",
		"STARS allows control-shift click to initiate track (CRC style)",
		"Secondary scratchpads are now supported",
		"Fixed various navigation bugs",
		"STARS: fixed a bug where RBL lines for *T that included aircraft weren't drawn",
		"Added an option to hide the flight strips (Settings window, Flight Strips section)",
		"Fixed a bug where inbound handoffs wouldn't send a radio contact message",
		"Sped up loading of video maps so that vice launches more quickly",
		"Added multiple new scenarios: S46, BHM, GSP (Aaron Flett), AUS (Jace Martin), P50 (Mike K)",
		"Multi-controller servers can now be password-protected",
		"Added \"TO\" command for \"contact tower\"",
		"Various bugfixes with handoffs and approach navigation",
		"Match real-world STARS alert sounds",
		"Added new scenarios: D10 (Mike K), CYS, ASE, COS (Jud Lopez)",
		"Fixed multiple bugs with handling of altitude and speed restrictions in departure routes",
		"Increased acceleration and climb rate of departures to be more realistic",
		"Fixed multiple bugs with point outs",
		"Fixed bug in the STARS scope that required secondary scratchpads to be three characters",
		"(Re-)added optional sound effect for accepted handoffs",
		"Airspace warnings are inhibited for aircraft flying approaches",
		"STARS: allow control-left-click in place of the third mouse button to highlight aircraft",
		"STARS: actually use the LDB brightness setting for limited/partial datablocks",
		"STARS: fix incorrect error message after issuing \"at fix, cleared approach\"",
		"Added new scenarios: TPA (Connor Allen), SAN, SCT-BUR (Justin Nguyen), SCT (Eli Thompson), NCT, GJT (Jud Lopez)",
		"Smaller scenario updates: D10 and JAX (Mike K), AUS (Jace Martin), LGA, JFK, COS, CLE, ASE, DCA, F11",
		"Arc routes between fixes can now be accurately specified",
		"STARS: more accurate simulation of STARS weather radar display",
		"Added new syntax for issuing left/right turn in degrees: T10L, T20R, etc.",
		"STARS: allow middle-click highlight of aircraft regardless of having their track",
		"STARS: fixed bug with airport weather list flickering",
		"Added new scenarios: SCT LAX (Jud Lopez), IND (Samuel Valencia), MKE (Yahya Nazimuddin), MIA (Mike K)",
		"Scenario updates/bugfixes: TPA (Connor Allen), SCT ONT/SNA (Eli Thompson), A80, L30 (Michael Trokel)",
		"STARS: added automated terminal proximity alert (ATPA) support",
		"STARS: consolidated wake turbulence (CWT) categories are now shown in datablocks and used for ATPA in-trail requirements",
		"Live weather can now be used in sims",
		"STARS: fixed various small bugs related to when the FDB should be displayed",
		"New scenarios: BDL (MerryArbitrary), D21 (Jackson Verdoorn), M98 (Jace Martin), P80 (Ethan Malimon)",
		"Scenario updates: EWR (aq86_), LGA (Yi Zheng), MIA (Connor Allen), Y90 (MerryArbitrary, Nelson T)",
		`Aircraft control commands (like "C80" for "climb and maintain 8,000") must now start with a comma`,
		`Related: the scratchpad can now be set by entering text and slewing an aircraft`,
		"Redirected handoffs are now supported and inter- and intra-facility handoffs are now handled more accurately",
		`Added support for "force quicklook" to push a quicklook to another controller`,
		`Added support for minimum safe altitude warnings (MSAW) for aircraft that are below the MVA`,
		`CWT category updates and bugfixes`,
		`Added support for global leader lines`,
		`Limited datablocks are now supported (and used when appropriate)`,
		`Handle various cases where the FDB should be displayed by default`,
		`Fixed a bug where go-arounds would sometimes not contact departure`,
		`Fixed a bug where live weather would occasionally cause vice to crash`,
		`Fixed a bug where aircraft TAS would be too high at high altitudes`,
		`Added support for ATC chat (prefix chat messages a '/' in the command prompt)`,
		`Allow entering values for STARS DCB spinner using the keyboard`,
		`Scenario Updates: D01 and COS (Andrew S), Y90 (Merry Arbitrary), C90 (Jud Lopez, Yahya Nazimuddin)`,
		`Added "FC" command to tell aircraft to change to the next controller's frequency`,
		`STARS: Add support for displaying requested altitude in FDB`,
		`Fixed a bug where aircraft callsign numbers could start with 0`,
		`STARS: use realistic fonts for the STARS display`,
		`Improved sequencing of departures`,
		`Added I90 scenario (Jace Martin)`,
		`Added full-screen mode`,
		`Updated command entry so keyboard focus returns to STARS after issuing a control command`,
		`New scenarios: PIT (Gavin V), AVL, AGS, and GSO (Giovanni), ACK, BNA, BOS, CHS, MHT, OKC, RDU (Michael K)`,
		`Updates to BUF, CLE, D21 (Gavin), BHM (Giovanni), JAX and F11 (Michael K), D01 (Jud Lopez, Andrew S)`,
		`Even more scenario updates: C90 (Jud Lopez, Yahya Nazimuddin), A90 (Michael K)`,
		`STARS: more realistic video map handling (per controller maps, map id #s)`,
		`Fixed a bug where vice would crash on launch if it was exited while minimized`,
		`STARS: multiple improvements to drawing aircraft tracks`,
		`Added a short pause before aircraft ident`,
		`Aircraft can now be sent 'direct' to their destination airport`,
		`Fixed a bug where vice would sometimes crash at startup or when MAPS was clicked`,
		`New scenario: SCT (Jud Lopez)`,
		`Scenario updates: AVL (Giovanni), A90 and BOS (Michael K)`,
		`Fixed "ID" flashing when aircraft ident`,
		`New scenarios: MCI (Brody Carty), P31 (Josh Lambert)`,
		`Scenario updates: F11, JAX (Michael Knight), Y90 (Merry Aribrary)`,
		`Added "say altitude" (SA) and "say heading" commands (SH) (Michael Knight)`,
		`Aircraft delete ("X") is now a CLI command, not a STARS command (Michael Knight)`,
		`"Paste" is now supported in the messages pane (Michael Trokel)`,
		`The "\" key can be used in place of END to activate the STARS minimum separation tool`,
		`Fixed a bug where runway-specific routes in STARs would be followed too early`,
		`Fixed a bug where handoffs from virtual controllers would sometimes not be made`,
		`Anti-aliasing is disabled by default (but can be re-enabled via the "settings" menu)`,
		`Multiple fixes to improve accuracy of drawing in STARS`,
		`New scenarios: R90 (Logan S, Jackson Verdoorn), BOI (Jonah Lefkoff)`,
		`Scenario updates (1): M98 (Logan S, Jackson Verdoorn), MCI, N90 (Jud Lopez), D21, CLE (Gavin V)`,
		`Scenario updates (2): SAN (Justin Nguyen), D01 (Andrew S), MHT, ACK, A90 (Michael Knight), AAC`,
		`Added an underlying simulation of the NAS and STARS/ERAM computers`,
		`Multiple improvements to the realism of the STARS display and sounds`,
		`"Beaconator" added to STARS (F1)`,
		`Added "SQ" command to issue a beacon code to an aircraft`,
		`Fixed a crash when setting scratchpads`,
		`Fixed bugs in the "launch control window" that would prevent it from refreshing`,
		`New scenarios: CPR (Andrew S), CID (Tyler Temerowski)`,
		`Scenario updates (1): ASE, COS, CYS, D01 (Andrew S), N90 (Kayden Lambert), P50, A80 (radarcontacto)`,
		`Scenario updates (2): CDW (Mike LeGall), TPA, MIA (Connor Allen), F11 (Michael Knight), Y90 (Merry Arbitrary)`,
		`Fixed a crash on Windows systems with high-DPI displays`,
		`Fixed some cases where a procedure turn would be flown even after aircraft passed a "no pt" fix`,
		`STARS weather radar rendering is much closer to real-world`,
		`STARS: many small fixes to how datablocks and tracks / track ids are drawn`,
		`STARS: fixed some bugs where valid scratchpad entries were rejected`,
		`Fixed a crash when handing off to a different facility`,
		`Fixed a crash with corrupt Sim saves`,
		`Added airspace awareness information to the scenario information window`,
		`STARS: fixed colors for WX buttons in the DCB when WX is available`,
		`STARS: minor improvements to the rendering of tracks and position symbols`,
		`Fixed a crash loading saved scenarios from the last release`,
		`vice's documentation is substantially expanded and now discusses all currently-available functionality`,
		`STARS sign-on list is more realistic. The list of signed-on controllers can now be found in the "scenario info" window`,
		`STARS: multiple improvements to data block and radar track drawing accuracy`,
		`STARS: added support for inverted numpads on keyboards`,
		`STARS: fixed a number of bugs in "quicklook" and in MAPS and PREF management`,
		`Scenario updates: D10 (Austin Jenkins), EWR (Mike LeGall), CID (Tyler T), D01, COS (Andrew S), AAC`,
		`Massive update to the aircraft performance database (EkimWasHere)`,
		`The virtual local controllers sequence departures much better, including handling wake turbulence separation`,
		`Multiple improvements to the aircraft flight model`,
		`STARS: added support for coordination lists (used for "hold for release")`,
		`STARS: improved handling of preference sets: a separate one is stored for each TRACON`,
		`Multiple improvements to the accuracy of flight strips`,
		`New scenario: SGF (Tyler T)`,
	}
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

func uiInit(r renderer.Renderer, p platform.Platform, config *Config, es *sim.EventStream, lg *log.Logger) {
	if runtime.GOOS == "windows" {
		imgui.CurrentStyle().ScaleAllSizes(p.DPIScale())
	}

	ui.font = renderer.GetFont(renderer.FontIdentifier{Name: "Roboto Regular", Size: config.UIFontSize})
	ui.aboutFont = renderer.GetFont(renderer.FontIdentifier{Name: "Roboto Regular", Size: 18})
	ui.aboutFontSmall = renderer.GetFont(renderer.FontIdentifier{Name: "Roboto Regular", Size: 14})
	ui.eventsSubscription = es.Subscribe()

	if iconImage, err := png.Decode(bytes.NewReader([]byte(iconPNG))); err != nil {
		lg.Errorf("Unable to decode icon PNG: %v", err)
	} else {
		ui.iconTextureID = r.CreateTextureFromImage(iconImage, false)
	}

	if sadTowerImage, err := png.Decode(bytes.NewReader([]byte(sadTowerPNG))); err != nil {
		lg.Errorf("Unable to decode sad tower PNG: %v", err)
	} else {
		ui.sadTowerTextureID = r.CreateTextureFromImage(sadTowerImage, false)
	}

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
}

func uiShowModalDialog(d *ModalDialogBox, atFront bool) {
	if atFront {
		ui.activeModalDialogs = append([]*ModalDialogBox{d}, ui.activeModalDialogs...)
	} else {
		ui.activeModalDialogs = append(ui.activeModalDialogs, d)
	}
}

func uiCloseModalDialog(d *ModalDialogBox) {
	ui.activeModalDialogs = util.FilterSlice(ui.activeModalDialogs,
		func(m *ModalDialogBox) bool { return m != d })

}

func uiShowConnectDialog(mgr *sim.ConnectionManager, allowCancel bool, config *Config, p platform.Platform, lg *log.Logger) {

	client := &ConnectModalClient{
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

func uiDraw(mgr *sim.ConnectionManager, config *Config, p platform.Platform, r renderer.Renderer,
	controlClient *sim.ControlClient, eventStream *sim.EventStream, lg *log.Logger) renderer.RendererStats {
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

	imgui.PushFont(ui.font.Ifont)
	if imgui.BeginMainMenuBar() {
		imgui.PushStyleColor(imgui.StyleColorButton, imgui.CurrentStyle().Color(imgui.StyleColorMenuBarBg))

		if controlClient != nil && controlClient.Connected() {
			if controlClient.SimIsPaused {
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
		}

		if imgui.Button(renderer.FontAwesomeIconRedo) {
			uiShowConnectDialog(mgr, true, config, p, lg)
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

		enableLaunch := controlClient != nil &&
			(controlClient.LaunchConfig.Controller == "" || controlClient.LaunchConfig.Controller == controlClient.Callsign)
		uiStartDisable(!enableLaunch)
		if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
			controlClient.TakeOrReturnLaunchControl(eventStream)
		}
		if imgui.IsItemHovered() {
			verb := util.Select(controlClient.LaunchConfig.Controller == "", "Start", "Stop")
			tip := verb + " manually control spawning new aircraft"
			if controlClient.LaunchConfig.Controller != "" {
				tip += "\nCurrent controller: " + controlClient.LaunchConfig.Controller
			}
			imgui.SetTooltip(tip)
		}
		uiEndDisable(!enableLaunch)

		if imgui.Button(renderer.FontAwesomeIconBook) {
			browser.OpenURL("https://pharr.org/vice/index.html")
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip("Display online vice documentation")
		}

		width, _ := ui.font.BoundText(renderer.FontAwesomeIconInfoCircle, 0)
		imgui.SetCursorPos(imgui.Vec2{p.DisplaySize()[0] - float32(6*width+15), 0})
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
		uiDrawSettingsWindow(controlClient, config, p)

		if ui.showScenarioInfo {
			ui.showScenarioInfo = controlClient.DrawScenarioInfoWindow(lg)
		}

		uiDrawMissingPrimaryDialog(mgr, controlClient, p)

		if controlClient.LaunchConfig.Controller == controlClient.Callsign {
			if ui.launchControlWindow == nil {
				ui.launchControlWindow = MakeLaunchControlWindow(controlClient, lg)
			}
			ui.launchControlWindow.Draw(eventStream, p)
		}
	}

	for _, event := range ui.eventsSubscription.Get() {
		if event.Type == sim.ServerBroadcastMessageEvent {
			uiShowModalDialog(NewModalDialogBox(&BroadcastModalDialog{Message: event.Message}, p), false)
		}
	}

	drawActiveDialogBoxes()

	uiDrawKeyboardWindow(controlClient, config)

	imgui.PopFont()

	// Finalize and submit the imgui draw lists
	imgui.Render()
	cb := renderer.GetCommandBuffer()
	defer renderer.ReturnCommandBuffer(cb)
	renderer.GenerateImguiCommandBuffer(cb, p.DisplaySize(), p.FramebufferSize(), lg)
	return r.RenderCommandBuffer(cb)
}

func uiResetControlClient(c *sim.ControlClient) {
	ui.launchControlWindow = nil
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

func NewModalDialogBox(c ModalDialogClient, p platform.Platform) *ModalDialogBox {
	return &ModalDialogBox{client: c, platform: p}
}

func (m *ModalDialogBox) Draw() {
	if m.closed {
		return
	}

	title := fmt.Sprintf("%s##%p", m.client.Title(), m)
	imgui.OpenPopup(title)

	flags := imgui.WindowFlagsNoResize | imgui.WindowFlagsAlwaysAutoResize | imgui.WindowFlagsNoSavedSettings
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{300, 100}, imgui.Vec2{-1, float32(m.platform.WindowSize()[1]) * 19 / 20})
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
	mgr         *sim.ConnectionManager
	lg          *log.Logger
	simConfig   *sim.NewSimConfiguration
	allowCancel bool
	platform    platform.Platform
	config      *Config
}

func (c *ConnectModalClient) Title() string { return "New Simulation" }

func (c *ConnectModalClient) Opening() {
	if c.simConfig == nil {
		c.simConfig = sim.MakeNewSimConfiguration(c.mgr, &c.config.LastTRACON, &c.config.TFRCache, c.lg)
	}
}

func (c *ConnectModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	if c.allowCancel {
		b = append(b, ModalDialogButton{text: "Cancel"})
	}

	next := ModalDialogButton{
		text:     c.simConfig.UIButtonText(),
		disabled: c.simConfig.OkDisabled(),
		action: func() bool {
			if c.simConfig.ShowRatesWindow() {
				client := &RatesModalClient{
					lg:            c.lg,
					connectClient: c,
					platform:      c.platform,
				}
				uiShowModalDialog(NewModalDialogBox(client, c.platform), false)
				return true
			} else {
				c.simConfig.DisplayError = c.simConfig.Start()
				return c.simConfig.DisplayError == nil
			}
		},
	}

	return append(b, next)
}

func (c *ConnectModalClient) Draw() int {
	if enter := c.simConfig.DrawUI(c.platform); enter {
		return 1
	} else {
		return -1
	}
}

type RatesModalClient struct {
	lg *log.Logger
	// Hold on to the connect client both to pick up various parameters
	// from it but also so we can go back to it when "Previous" is pressed.
	connectClient *ConnectModalClient
	platform      platform.Platform
}

func (r *RatesModalClient) Title() string { return "Arrival / Departure Rates" }

func (r *RatesModalClient) Opening() {}

func (r *RatesModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton

	prev := ModalDialogButton{
		text: "Previous",
		action: func() bool {
			uiShowModalDialog(NewModalDialogBox(r.connectClient, r.platform), false)
			return true
		},
	}
	b = append(b, prev)

	if r.connectClient.allowCancel {
		b = append(b, ModalDialogButton{text: "Cancel"})
	}

	ok := ModalDialogButton{
		text:     "Create",
		disabled: r.connectClient.simConfig.OkDisabled(),
		action: func() bool {
			r.connectClient.simConfig.DisplayError = r.connectClient.simConfig.Start()
			return r.connectClient.simConfig.DisplayError == nil
		},
	}

	return append(b, ok)
}

func (r *RatesModalClient) Draw() int {
	if enter := r.connectClient.simConfig.DrawRatesUI(r.platform); enter {
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

	imgui.PushFont(ui.aboutFont.Ifont)
	center("vice")
	center(renderer.FontAwesomeIconCopyright + "2023 Matt Pharr")
	center("Licensed under the GPL, Version 3")
	if imgui.IsItemHovered() && imgui.IsMouseClicked(0) {
		browser.OpenURL("https://www.gnu.org/licenses/gpl-3.0.html")
	}
	center("Current build: " + buildVersion)
	center("Source code: " + renderer.FontAwesomeIconGithub)
	if imgui.IsItemHovered() && imgui.IsMouseClicked(0) {
		browser.OpenURL("https://github.com/mmp/vice")
	}
	imgui.PopFont()

	imgui.Separator()

	imgui.PushFont(ui.aboutFontSmall.Ifont)
	// We would very much like to use imgui.{Push,Pop}TextWrapPos()
	// here, but for unclear reasons that makes the info window
	// vertically maximized. So we hand-wrap the lines for the
	// font we're using...
	credits :=
		`Additional credits:
- Software Development: Artem Dorofeev,
  Dennis Graiani, Neel P, Michael Trokel,
  Samuel Valencia, Xavier Caldwell, and
  Yi Zhang.
- Timely feedback: radarcontacto.
- Facility engineering: Connor Allen, Adam
  Bolek, Brody Carty, Lucas Chan, Aaron
  Flett, Austin Jenkins, Mike K, Josh
  Lambert, Kayden Lambert, Make LeGall,
  Jonah Lefkoff, Jud Lopez, Ethan Malimon,
  Jace Martin, Michael McConnell, Merry,
  Yahya Nazimuddin, Justin Nguyen,
  Giovanni, Andrew S, Logan S, Arya T,
  Nelson T, Tyler Temerowski, Eli Thompson,
  Michael Trokel, Samuel Valencia, Gavin
  Velicevic, and Jackson Verdoorn.
- Video maps: thanks to the ZAU, ZBW, ZDC,
  ZDV, ZHU, ZID, ZJX, ZLA, ZMP, ZNY, ZOB,
  ZSE, and ZTL VATSIM ARTCCs and to the
  FAA, from whence the original maps came.
- Additionally: OpenScope for the aircraft
  performance and airline databases,
  ourairports.com for the airport database,
  and for the FAA for being awesome about
  providing the CIFP, MVA specifications,
  and other useful aviation data digitally.
- One more thing: see the file CREDITS.txt
  in the vice source code distribution for
  third-party software, fonts, sounds, etc.`

	imgui.Text(credits)

	imgui.PopFont()

	imgui.End()
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
	text, _ := util.WrapText(m.message, 80, 0, true)
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
		imgui.Image(imgui.TextureID(ui.sadTowerTextureID), imgui.Vec2{128, 128})

		imgui.TableNextColumn()
		text, _ := util.WrapText(e.message, 80, 0, true)
		imgui.Text("\n\n" + text)

		imgui.EndTable()
	}
	return -1
}

func ShowErrorDialog(p platform.Platform, lg *log.Logger, s string, args ...interface{}) {
	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)}, p)
	uiShowModalDialog(d, true)

	lg.Errorf(s, args...)
}

func ShowFatalErrorDialog(r renderer.Renderer, p platform.Platform, lg *log.Logger, s string, args ...interface{}) {
	lg.Errorf(s, args...)

	d := NewModalDialogBox(&ErrorModalClient{message: fmt.Sprintf(s, args...)}, p)

	for !d.closed {
		p.ProcessEvents()
		p.NewFrame()
		imgui.NewFrame()
		imgui.PushFont(ui.font.Ifont)
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

///////////////////////////////////////////////////////////////////////////

type LaunchControlWindow struct {
	controlClient       *sim.ControlClient
	departures          []*LaunchDeparture
	arrivalsOverflights []*LaunchArrivalOverflight
	lg                  *log.Logger
}

type LaunchDeparture struct {
	Aircraft           av.Aircraft
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

type LaunchArrivalOverflight struct {
	Aircraft           av.Aircraft
	Group              string
	Airport            string
	LastLaunchCallsign string
	LastLaunchTime     time.Time
	TotalLaunches      int
}

func (la *LaunchArrivalOverflight) Reset() {
	la.LastLaunchCallsign = ""
	la.LastLaunchTime = time.Time{}
	la.TotalLaunches = 0
}

func MakeLaunchControlWindow(controlClient *sim.ControlClient, lg *log.Logger) *LaunchControlWindow {
	lc := &LaunchControlWindow{controlClient: controlClient}

	config := &controlClient.LaunchConfig
	for _, airport := range util.SortedMapKeys(config.DepartureRates) {
		runwayRates := config.DepartureRates[airport]
		for _, rwy := range util.SortedMapKeys(runwayRates) {
			for _, category := range util.SortedMapKeys(runwayRates[rwy]) {
				lc.departures = append(lc.departures, &LaunchDeparture{
					Airport:  airport,
					Runway:   rwy,
					Category: category,
				})
			}
		}
	}
	for i := range lc.departures {
		lc.spawnDeparture(lc.departures[i])
	}

	for _, group := range util.SortedMapKeys(config.InboundFlowRates) {
		for ap := range config.InboundFlowRates[group] {
			lc.arrivalsOverflights = append(lc.arrivalsOverflights,
				&LaunchArrivalOverflight{
					Group:   group,
					Airport: ap,
				})
		}
	}
	for i := range lc.arrivalsOverflights {
		lc.spawnArrivalOverflight(lc.arrivalsOverflights[i])
	}

	return lc
}

func (lc *LaunchControlWindow) spawnDeparture(dep *LaunchDeparture) {
	lc.controlClient.CreateDeparture(dep.Airport, dep.Runway, dep.Category, &dep.Aircraft, nil,
		func(err error) { lc.lg.Warnf("CreateDeparture: %v", err) })
}

func (lc *LaunchControlWindow) spawnArrivalOverflight(lac *LaunchArrivalOverflight) {
	if lac.Airport != "overflights" {
		lc.controlClient.CreateArrival(lac.Group, lac.Airport, &lac.Aircraft, nil,
			func(err error) { lc.lg.Warnf("CreateArrival: %v", err) })
	} else {
		lc.controlClient.CreateOverflight(lac.Group, &lac.Aircraft, nil,
			func(err error) { lc.lg.Warnf("CreateOverflight: %v", err) })
	}
}

func (lc *LaunchControlWindow) Draw(eventStream *sim.EventStream, p platform.Platform) {
	showLaunchControls := true
	imgui.SetNextWindowSizeConstraints(imgui.Vec2{300, 100}, imgui.Vec2{-1, float32(p.WindowSize()[1]) * 19 / 20})
	imgui.BeginV("Launch Control", &showLaunchControls, imgui.WindowFlagsAlwaysAutoResize)

	imgui.Text("Mode:")
	imgui.SameLine()
	if imgui.RadioButtonInt("Manual", &lc.controlClient.LaunchConfig.Mode, sim.LaunchManual) {
		lc.controlClient.SetLaunchConfig(lc.controlClient.LaunchConfig)
	}
	imgui.SameLine()
	if imgui.RadioButtonInt("Automatic", &lc.controlClient.LaunchConfig.Mode, sim.LaunchAutomatic) {
		lc.controlClient.SetLaunchConfig(lc.controlClient.LaunchConfig)
	}

	width, _ := ui.font.BoundText(renderer.FontAwesomeIconPlayCircle, 0)
	// Right-justify
	imgui.SameLine()
	//	imgui.SetCursorPos(imgui.Vec2{imgui.CursorPosX() + imgui.ContentRegionAvail().X - float32(3*width+10),
	imgui.SetCursorPos(imgui.Vec2{imgui.WindowWidth() - float32(7*width), imgui.CursorPosY()})
	if lc.controlClient != nil && lc.controlClient.Connected() {
		if lc.controlClient.SimIsPaused {
			if imgui.Button(renderer.FontAwesomeIconPlayCircle) {
				lc.controlClient.ToggleSimPause()
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Resume simulation")
			}
		} else {
			if imgui.Button(renderer.FontAwesomeIconPauseCircle) {
				lc.controlClient.ToggleSimPause()
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Pause simulation")
			}
		}
	}

	imgui.SameLine()
	if imgui.Button(renderer.FontAwesomeIconTrash) {
		uiShowModalDialog(NewModalDialogBox(&YesOrNoModalClient{
			title: "Are you sure?",
			query: "All aircraft will be deleted. Go ahead?",
			ok: func() {
				lc.controlClient.DeleteAllAircraft(nil)
				for _, dep := range lc.departures {
					dep.Reset()
				}
				for _, ac := range lc.arrivalsOverflights {
					ac.Reset()
				}
			},
		}, p), true)
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Delete all aircraft and restart")
	}

	imgui.Separator()

	if lc.controlClient.LaunchConfig.Mode == sim.LaunchManual {
		mitAndTime := func(ac *av.Aircraft, launchPosition math.Point2LL,
			lastLaunchCallsign string, lastLaunchTime time.Time) {
			imgui.TableNextColumn()
			if lastLaunchCallsign != "" {
				if ac := lc.controlClient.Aircraft[lastLaunchCallsign]; ac != nil {
					d := math.NMDistance2LL(ac.Position(), launchPosition)
					imgui.Text(fmt.Sprintf("%.1f", d))
				}
			}

			imgui.TableNextColumn()
			if lastLaunchCallsign != "" {
				d := lc.controlClient.CurrentTime().Sub(lastLaunchTime).Round(time.Second).Seconds()
				m, s := int(d)/60, int(d)%60
				imgui.Text(fmt.Sprintf("%02d:%02d", m, s))
			}
		}

		ndep := util.ReduceSlice(lc.departures, func(dep *LaunchDeparture, n int) int {
			return n + dep.TotalLaunches
		}, 0)

		imgui.Text(fmt.Sprintf("Departures: %d total", ndep))

		flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg |
			imgui.TableFlagsSizingStretchProp
		tableScale := util.Select(runtime.GOOS == "windows", p.DPIScale(), float32(1))
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

				if dep.Aircraft.Callsign != "" {
					imgui.TableNextColumn()
					imgui.Text(dep.Aircraft.Callsign)

					imgui.TableNextColumn()
					imgui.Text(dep.Aircraft.FlightPlan.TypeWithoutSuffix())

					imgui.TableNextColumn()
					imgui.Text(dep.Aircraft.FlightPlan.Exit)

					mitAndTime(&dep.Aircraft, dep.Aircraft.Position(), dep.LastLaunchCallsign,
						dep.LastLaunchTime)

					imgui.TableNextColumn()
					if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
						lc.controlClient.LaunchAircraft(dep.Aircraft)
						dep.LastLaunchCallsign = dep.Aircraft.Callsign
						dep.LastLaunchTime = lc.controlClient.CurrentTime()
						dep.TotalLaunches++

						dep.Aircraft = av.Aircraft{}
						lc.spawnDeparture(dep)
					}

					imgui.TableNextColumn()
					if imgui.Button(renderer.FontAwesomeIconRedo) {
						dep.Aircraft = av.Aircraft{}
						lc.spawnDeparture(dep)
					}
				}

				imgui.PopID()
			}

			imgui.EndTable()
		}

		imgui.Separator()

		narof := util.ReduceSlice(lc.arrivalsOverflights, func(arr *LaunchArrivalOverflight, n int) int {
			return n + arr.TotalLaunches
		}, 0)

		imgui.Text(fmt.Sprintf("Arrivals/Overflights: %d total", narof))

		if imgui.BeginTableV("arrof", 9, flags, imgui.Vec2{tableScale * 600, 0}, 0.0) {
			imgui.TableSetupColumn("Group")
			imgui.TableSetupColumn("Launches")
			imgui.TableSetupColumn("Airport")
			imgui.TableSetupColumn("Callsign")
			imgui.TableSetupColumn("A/C Type")
			imgui.TableSetupColumn("MIT")
			imgui.TableSetupColumn("Time")
			imgui.TableHeadersRow()

			for _, arof := range lc.arrivalsOverflights {
				imgui.PushID(arof.Group + arof.Airport)

				imgui.TableNextRow()

				imgui.TableNextColumn()
				imgui.Text(arof.Group)

				imgui.TableNextColumn()
				imgui.Text(strconv.Itoa(arof.TotalLaunches))

				imgui.TableNextColumn()
				imgui.Text(arof.Airport)

				imgui.TableNextColumn()
				imgui.Text(arof.Aircraft.Callsign)

				if arof.Aircraft.Callsign != "" {
					imgui.TableNextColumn()
					imgui.Text(arof.Aircraft.FlightPlan.TypeWithoutSuffix())

					mitAndTime(&arof.Aircraft, arof.Aircraft.Position(), arof.LastLaunchCallsign,
						arof.LastLaunchTime)

					imgui.TableNextColumn()
					if imgui.Button(renderer.FontAwesomeIconPlaneDeparture) {
						lc.controlClient.LaunchAircraft(arof.Aircraft)
						arof.LastLaunchCallsign = arof.Aircraft.Callsign
						arof.LastLaunchTime = lc.controlClient.CurrentTime()
						arof.TotalLaunches++

						arof.Aircraft = av.Aircraft{}
						lc.spawnArrivalOverflight(arof)
					}

					imgui.TableNextColumn()
					if imgui.Button(renderer.FontAwesomeIconRedo) {
						arof.Aircraft = av.Aircraft{}
						lc.spawnArrivalOverflight(arof)
					}
				}

				imgui.PopID()
			}

			imgui.EndTable()
		}
	} else {
		changed := lc.controlClient.LaunchConfig.DrawDepartureUI(p)
		changed = lc.controlClient.LaunchConfig.DrawArrivalUI(p) || changed
		changed = lc.controlClient.LaunchConfig.DrawOverflightUI(p) || changed

		if changed {
			lc.controlClient.SetLaunchConfig(lc.controlClient.LaunchConfig)
		}
	}

	imgui.End()

	if !showLaunchControls {
		lc.controlClient.TakeOrReturnLaunchControl(eventStream)
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
	[3]string{"*TO*", `"Contact tower"`, "*TO*"},
	[3]string{"*FC*", `"Contact _ctrl_ on _freq_, where _ctrl_ is the controller who has the track and _freq_ is their frequency."`, "*FC*"},
	[3]string{"*X*", "(Deletes the aircraft.)", "*X*"},
}

var secondaryAcCommands = [][3]string{
	[3]string{"*L_hdg", `"Turn left heading _hdg_."`, "*L130*"},
	[3]string{"*T_deg*L", `"Turn _deg_ degrees left."`, "*T10L*"},
	[3]string{"*R_hdg", `"Turn right heading _hdg_".`, "*R210*"},
	[3]string{"*T_deg*R", `"Turn _deg_ degrees right".`, "*T20R*"},
	[3]string{"*D_fix*/H_hdg", `"Depart _fix_ heading _hdg_".`, "*DLENDY/H180*"},
	[3]string{"*C_fix*/A_alt*/S_kts",
		`"Cross _fix_ at _alt_ / _kts_ knots."
Either one or both of *A* and *S* may be specified.`, "*CCAMRN/A110+*"},
	[3]string{"*ED*", `"Expedite descent"`, "*ED*"},
	[3]string{"*EC*", `"Expedite climb"`, "*EC*"},
	[3]string{"*SMIN*", `"Maintain slowest practical speed".`, "*SMIN*"},
	[3]string{"*SMAX*", `"Maintain maximum forward speed".`, "*SMAX*"},
	[3]string{"*SS*", `"Say airspeed".`, "*SS*"},
	[3]string{"*SA*", `"Say altitude".`, "*SA*"},
	[3]string{"*SH*", `"Say heading".`, "*SH*"},
	[3]string{"*SQ_code", `"Squawk _code_."`, "*SQ1200*"},
	[3]string{"*A_fix*/C_appr", `"At _fix_, cleared _appr_ approach."`, "*AROSLY/CI2L*"},
	[3]string{"*CAC*", `"Cancel approach clearance".`, "*CAC*"},
	[3]string{"*CSI_appr", `"Cleared straight-in _appr_ approach.`, "*CSII6*"},
	[3]string{"*I*", `"Intercept the localizer."`, "*I*"},
	[3]string{"*ID*", `"Ident."`, "*ID*"},
	[3]string{"*CVS*", `"Climb via the SID"`, "*CVS*"},
	[3]string{"*DVS*", `"Descend via the STAR"`, "*CVS*"},
	[3]string{"*P*", `Pauses/unpauses the sim`, "*P*"},
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
func uiDrawKeyboardWindow(c *sim.ControlClient, config *Config) {
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
	color := style.Color(imgui.StyleColorText)
	imgui.WindowDrawList().AddLine(imgui.Vec2{min.X, max.Y}, max, imgui.PackedColorFromVec4(color))
	if imgui.IsItemHovered() && imgui.IsMouseClicked(0) {
		browser.OpenURL("https://pharr.org/vice/")
	}
	imgui.PopStyleColor()
	imgui.SameLineV(0, 0)
	imgui.Text(" for full documentation of vice's keyboard commands.")

	imgui.Separator()

	fixedFont := renderer.GetFont(renderer.FontIdentifier{Name: "Roboto Mono", Size: config.UIFontSize})
	italicFont := renderer.GetFont(renderer.FontIdentifier{Name: "Roboto Mono Italic", Size: config.UIFontSize})

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
	imgui.PushFont(regularFont.Ifont)

	// textWidth approximates the width of the given string in pixels; it
	// may slightly over-estimate the width, but that's fine since we use
	// it to decide when to wrap lines of text.
	textWidth := func(s string) float32 {
		s = strings.Trim(s, `_*\`) // remove markup characters
		imgui.PushFont(fixedFont.Ifont)
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
					imgui.PushFont(fixedFont.Ifont)
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
					imgui.PushFont(italicFont.Ifont)
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

type MissingPrimaryModalClient struct {
	mgr           *sim.ConnectionManager
	controlClient *sim.ControlClient
}

func (mp *MissingPrimaryModalClient) Title() string {
	return "Missing Primary Controller"
}

func (mp *MissingPrimaryModalClient) Opening() {}

func (mp *MissingPrimaryModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Sign in to " + mp.controlClient.PrimaryController, action: func() bool {
		err := mp.controlClient.ChangeControlPosition(mp.controlClient.PrimaryController, true)
		return err == nil
	}})
	b = append(b, ModalDialogButton{text: "Disconnect", action: func() bool {
		mp.mgr.Disconnect()
		uiCloseModalDialog(ui.missingPrimaryDialog)
		return true
	}})
	return b
}

func (mp *MissingPrimaryModalClient) Draw() int {
	imgui.Text("The primary controller, " + mp.controlClient.PrimaryController + ", has disconnected from the server or is otherwise unreachable.\nThe simulation will be paused until a primary controller signs in.")
	return -1
}

func uiDrawMissingPrimaryDialog(mgr *sim.ConnectionManager, c *sim.ControlClient, p platform.Platform) {
	if _, ok := c.Controllers[c.PrimaryController]; ok {
		if ui.missingPrimaryDialog != nil {
			uiCloseModalDialog(ui.missingPrimaryDialog)
			ui.missingPrimaryDialog = nil
		}
	} else {
		if ui.missingPrimaryDialog == nil {
			ui.missingPrimaryDialog = NewModalDialogBox(&MissingPrimaryModalClient{
				mgr:           mgr,
				controlClient: c,
			}, p)
			uiShowModalDialog(ui.missingPrimaryDialog, true)
		}
	}
}

func uiDrawSettingsWindow(c *sim.ControlClient, config *Config, p platform.Platform) {
	if !ui.showSettings {
		return
	}

	imgui.BeginV("Settings", &ui.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	if imgui.SliderFloatV("Simulation speed", &c.SimRate, 1, 20, "%.1f", 0) {
		c.SetSimRate(c.SimRate)
	}

	update := !config.InhibitDiscordActivity.Load()
	imgui.Checkbox("Update Discord activity status", &update)
	config.InhibitDiscordActivity.Store(!update)

	if imgui.BeginComboV("UI Font Size", strconv.Itoa(config.UIFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := renderer.AvailableFontSizes("Roboto Regular")
		for _, size := range sizes {
			if imgui.SelectableV(strconv.Itoa(size), size == config.UIFontSize, 0, imgui.Vec2{}) {
				config.UIFontSize = size
				ui.font = renderer.GetFont(renderer.FontIdentifier{Name: "Roboto Regular", Size: config.UIFontSize})
			}
		}
		imgui.EndCombo()
	}

	if imgui.CollapsingHeader("Display") {
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
				if imgui.SelectableV(monitor, monitor == monitorNames[config.FullScreenMonitor], 0, imgui.Vec2{}) {
					config.FullScreenMonitor = index

					p.EnableFullScreen(p.IsFullScreen())
				}
			}

			imgui.EndCombo()
		}
	}

	config.DisplayRoot.VisitPanes(func(pane panes.Pane) {
		if draw, ok := pane.(panes.UIDrawer); ok {
			if imgui.CollapsingHeader(draw.DisplayName()) {
				draw.DrawUI(p, &config.Config)
			}
		}
	})

	imgui.End()
}
