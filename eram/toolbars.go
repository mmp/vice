// eram/toolbars.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/videomaps"
)

// The ERAM toolbars: the buttons they hold, in which menus, and what each
// button does. The engine in toolbarengine.go displays them and routes
// clicks to them, whether a button is in its toolbar or torn off from it.

// Buttons whose functions aren't simulated are inert: they are drawn as
// their kind in its inactive state and do nothing when clicked.
func inertMenu(label string) toolbarButton   { return toolbarButton{label: label, kind: buttonMenu} }
func inertToggle(label string) toolbarButton { return toolbarButton{label: label} }
func inertIncDec(label string) toolbarButton { return toolbarButton{label: label, kind: buttonIncDec} }

// inertToggleOn is an inert toggle drawn on, for a feature that is always
// active.
func inertToggleOn(label string) toolbarButton {
	return toggleButtonFunc(label, func(*Scope) bool { return true }, nil)
}

func menuButton(label string, m *toolbarMenu) toolbarButton {
	return toolbarButton{label: label, kind: buttonMenu, submenu: m}
}

func menuButtonFunc(labelFunc func(ep *Scope) string, m *toolbarMenu) toolbarButton {
	return toolbarButton{labelFunc: labelFunc, kind: buttonMenu, submenu: m}
}

// toggleButton makes a toggle for a boolean setting.
func toggleButton(label string, v func(*Preferences) *bool) toolbarButton {
	return toggleButtonFunc(label,
		func(ep *Scope) bool { return *v(ep.currentPrefs()) },
		func(ep *Scope, _ toolbarClick) error {
			p := v(ep.currentPrefs())
			*p = !*p
			return nil
		})
}

// toggleButtonFunc makes a toggle from its state and what toggles it.
func toggleButtonFunc(label string, active func(ep *Scope) bool, click func(ep *Scope, c toolbarClick) error) toolbarButton {
	return toolbarButton{label: label, active: active, click: click}
}

// incDecButton makes an increment/decrement button for an integer setting
// within [min, max]. Its text is name over the value.
func incDecButton[T ~int](name string, v func(*Preferences) *T, min, max, step int) toolbarButton {
	return incDecButtonFunc(name,
		func(ep *Scope) string { return fmt.Sprint(int(*v(ep.currentPrefs()))) },
		func(ep *Scope, c toolbarClick) error { return adjust(v(ep.currentPrefs()), c, min, max, step) })
}

// incDecButtonFunc makes an increment/decrement button from the text of
// its value and what adjusts it.
func incDecButtonFunc(name string, value func(ep *Scope) string, click func(ep *Scope, c toolbarClick) error) toolbarButton {
	return toolbarButton{
		kind:      buttonIncDec,
		repeat:    true,
		labelFunc: func(ep *Scope) string { return name + "\n" + value(ep) },
		click:     click,
	}
}

func commandButton(label string, active func(ep *Scope) bool, click func(ep *Scope, c toolbarClick) error) toolbarButton {
	return toolbarButton{label: label, kind: buttonCommand, active: active, click: click}
}

// readoutButton makes a button that only shows a value; it is drawn as a
// toggle.
func readoutButton(labelFunc func(ep *Scope) string) toolbarButton {
	return toolbarButton{labelFunc: labelFunc}
}

// panelButton makes a toggle that displays a panel beside itself.
func panelButton(labelFunc func(ep *Scope) string, panel toolbarPanel) toolbarButton {
	return toolbarButton{labelFunc: labelFunc, submenu: &toolbarMenu{panel: panel}}
}

///////////////////////////////////////////////////////////////////////////
// The main toolbar

var mainToolbar = toolbarMenu{rows: [][]toolbarButton{{
	inertMenu("DRAW"),
	menuButton("ATC\nTOOLS", &atcToolsMenu),
	inertMenu("AB\nSETTING"),
	// RANGE only shows the range, which the scroll wheel sets.
	readoutButton(rangeLabel),
	menuButton("CURSOR", &cursorMenu),
	menuButton("BRIGHT", &brightMenu),
	menuButton("FONT", &fontMenu),
	menuButton("DB\nFIELDS", &dbFieldsMenu),
	incDecButtonFunc("VECTOR", func(ep *Scope) string { return fmt.Sprint(ep.VelocityTime) }, clickVelocityVector),
}, {
	menuButton("VIEWS", &viewsMenu),
	menuButton("CHECK\nLISTS", &checkListsMenu),
	inertMenu("COMMAND\nMENUS"),
	menuButtonFunc(func(ep *Scope) string { return ep.videoMapLabel }, &geoMapMenu),
	panelButton(altitudeLimitsButtonLabel, altitudeLimitsPanel{}),
	menuButton("RADAR\nFILTER", &radarFilterMenu),
	inertToggle("PREFSET"),
	commandButton("DELETE\nTEAROFF",
		func(ep *Scope) bool { return ep.toolbar.deleting },
		func(ep *Scope, _ toolbarClick) error { ep.toggleDeleteTearoffs(); return nil }),
}}}

func rangeLabel(ep *Scope) string {
	r := ep.currentPrefs().Range / 2
	if r >= 2 {
		return fmt.Sprintf("RANGE\n%d", int(r))
	}
	return fmt.Sprintf("RANGE\n%.2f", r)
}

// clickVelocityVector sets the velocity vector length, which doubles from
// one minute up to eight; zero hides the vectors.
func clickVelocityVector(ep *Scope, c toolbarClick) error {
	v, raise := ep.VelocityTime, c.clicked[platform.MouseButtonTertiary]
	switch {
	case raise && v == 0:
		v = 1
	case raise && v < 8:
		v *= 2
	case !raise && v == 1:
		v = 0
	case !raise && v > 1:
		v /= 2
	default:
		return errToolbarLimit
	}
	ep.VelocityTime = v
	return nil
}

///////////////////////////////////////////////////////////////////////////
// ATC TOOLS, WX, and CURSOR

var atcToolsMenu = toolbarMenu{rows: [][]toolbarButton{{
	toggleButton("CRR\nFIX", func(ps *Preferences) *bool { return &ps.CRR.DisplayFixes }),
	inertToggle("SPEED\nADVSRY"),
	menuButton("WX", &wxMenu),
}}}

var wxMenu = toolbarMenu{rows: [][]toolbarButton{{
	inertIncDec("NX 000\n600"),
	incDecButtonFunc("NX LVL", func(ep *Scope) string { return nexradLevelLabel(ep.currentPrefs().NexradLevel) },
		clickNexradLevel),
}, {
	inertToggle("WX1"),
	inertToggle("WX2"),
	inertToggle("WX3"),
}}}

var cursorMenu = toolbarMenu{rows: [][]toolbarButton{{
	inertIncDec("SPEED\n1"),
	incDecButton("SIZE", func(ps *Preferences) *int { return &ps.CursorSize }, 1, 5, 1),
	inertIncDec("VOLUME\n5"),
}}}

///////////////////////////////////////////////////////////////////////////
// BRIGHT and MAP BRIGHT

// brightness makes a BRIGHT menu button for one of the scope brightnesses,
// 0 to 100 in steps of two.
func brightness(name string, v func(*Preferences) *scope.Brightness) toolbarButton {
	return brightnessRange(name, v, 0, 100, 2, showBrightness)
}

// brightnessRange is brightness with the value's range and how it is shown
// given: the brightnesses relative to another show "=" at zero. None of
// the BRIGHT buttons has a tear-off bar.
func brightnessRange(name string, v func(*Preferences) *scope.Brightness, min, max, step int,
	show func(scope.Brightness) string) toolbarButton {
	b := incDecButtonFunc(name,
		func(ep *Scope) string { return show(*v(ep.currentPrefs())) },
		func(ep *Scope, c toolbarClick) error { return adjust(v(ep.currentPrefs()), c, min, max, step) })
	b.noTab = true
	return b
}

// tearoffSpace gives a button without a tear-off bar the space for one, so
// that it lines up with the buttons that have them.
func tearoffSpace(b toolbarButton) toolbarButton {
	b.noTab, b.noTearoff = false, true
	return b
}

func showBrightness(b scope.Brightness) string {
	return fmt.Sprint(int(b))
}

func showRelative(b scope.Brightness) string {
	return util.Select(b > 0, fmt.Sprintf("+%d", int(b)), "=")
}

func showSigned(b scope.Brightness) string {
	return util.Select(b != 0, fmt.Sprint(int(b)), "=")
}

func showNegated(b scope.Brightness) string {
	return util.Select(b > 0, fmt.Sprint(-int(b)), "=")
}

var brightMenu = toolbarMenu{rows: [][]toolbarButton{{
	menuButton("MAP\nBRIGHT", &mapBrightMenu),
	inertMenu("CPDLC"),
	brightnessRange("BCKGRD", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Background }, 0, 60, 2, showBrightness),
	brightness("CURSOR", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Cursor }),
	brightness("TEXT", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Text }),
	brightness("PR TGT", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.PRTGT }),
	brightness("UNP TGT", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.UNPTGT }),
	brightness("PR HST", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.PRHST }),
	brightness("UNP HST", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.UNPHST }),
	brightness("LDB", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.LDB }),
	brightnessRange("SLDB", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.SLDB }, 0, 20, 1, showRelative),
	brightness("WX", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.WX }),
	brightness("NEXRAD", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.NEXRAD }),
}, {
	tearoffSpace(brightness("BCKLGHT", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Backlight })),
	tearoffSpace(brightness("BUTTON", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Button })),
	brightness("BORDER", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Border }),
	brightness("TOOLBAR", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Toolbar }),
	brightness("TB BRDR", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.TBBRDR }),
	brightness("AB BRDR", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.ABBRDR }),
	brightness("FDB", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.FDB }),
	brightnessRange("PORTAL", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Portal }, -10, 10, 1, showSigned),
	brightness("SATCOMM", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Satcomm }),
	brightness("ON-FREQ", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.ONFREQ }),
	brightnessRange("LINE 4", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Line4 }, 0, 20, 1, showNegated),
	brightnessRange("DWELL", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Dwell }, 0, 20, 1, showRelative),
	brightness("FENCE", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Fence }),
	brightness("DBFEL", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.DBFEL }),
	brightness("OUTAGE", func(ps *Preferences) *scope.Brightness { return &ps.Brightness.Outage }),
}}}

var mapBrightMenu = toolbarMenu{rowsFunc: (*Scope).bcgBrightnessRows}

// bcgBrightnessRows returns the MAP BRIGHT menu: a brightness button for
// each of the GeoMap's brightness control groups, ten to a row.
func (ep *Scope) bcgBrightnessRows() [][]toolbarButton {
	var rows [][]toolbarButton
	n := 0
	for _, name := range ep.bcgNames {
		if name == "" {
			continue
		}
		if n%10 == 0 {
			rows = append(rows, nil)
		}
		rows[n/10] = append(rows[n/10], bcgBrightnessButton(name))
		n++
	}
	return rows
}

func bcgBrightnessButton(name string) toolbarButton {
	b := incDecButtonFunc(name,
		func(ep *Scope) string { return fmt.Sprint(int(ep.currentPrefs().VideoMapBrightness[name])) },
		func(ep *Scope, c toolbarClick) error {
			ps := ep.currentPrefs()
			b := ps.VideoMapBrightness[name]
			err := adjust(&b, c, 0, 100, 2)
			ps.VideoMapBrightness[name] = b
			return err
		})
	b.noTab = true
	return b
}

///////////////////////////////////////////////////////////////////////////
// FONT

func absoluteFontSize(name string, v func(*Preferences) *int, min, max int) toolbarButton {
	return incDecButton(name, v, min, max, 1)
}

// relativeFontSize is for the sizes set relative to another, which show "="
// at zero.
func relativeFontSize(name string, v func(*Preferences) *int, min, max int) toolbarButton {
	return incDecButtonFunc(name,
		func(ep *Scope) string {
			size := *v(ep.currentPrefs())
			return util.Select(size != 0, fmt.Sprint(size), "=")
		},
		func(ep *Scope, c toolbarClick) error { return adjust(v(ep.currentPrefs()), c, min, max, 1) })
}

var fontMenu = toolbarMenu{rows: [][]toolbarButton{{
	relativeFontSize("LINE4", func(ps *Preferences) *int { return &ps.Line4Size }, -2, 0),
	absoluteFontSize("FDB", func(ps *Preferences) *int { return &ps.FDBSize }, 1, 5),
	relativeFontSize("PORTAL", func(ps *Preferences) *int { return &ps.PoralSize }, -2, 0),
	absoluteFontSize("TOOLBAR", func(ps *Preferences) *int { return &ps.ToolbarSize }, 1, 2),
}, {
	absoluteFontSize("LDB", func(ps *Preferences) *int { return &ps.LDBSize }, 1, 5),
	absoluteFontSize("RDB", func(ps *Preferences) *int { return &ps.RDBSize }, 1, 5),
	absoluteFontSize("OUTAGE", func(ps *Preferences) *int { return &ps.OutageSize }, 1, 3),
}}}

///////////////////////////////////////////////////////////////////////////
// DB FIELDS

// line4Button makes a toggle for showing one of the FDB fourth-line fields;
// only one is shown at a time.
func line4Button(label string, field int) toolbarButton {
	return toggleButtonFunc(label,
		func(ep *Scope) bool { return ep.currentPrefs().Line4Type == field },
		func(ep *Scope, _ toolbarClick) error {
			ps := ep.currentPrefs()
			ps.Line4Type = util.Select(ps.Line4Type == field, Line4None, field)
			return nil
		})
}

// clickFDBLeader adjusts the FDB LDR length, which applies to all FDBs,
// including those whose leader line length was set individually.
func clickFDBLeader(ep *Scope, c toolbarClick) error {
	ps := ep.currentPrefs()
	if err := adjust(&ps.FDBLdrLength, c, 0, 3, 1); err != nil {
		return err
	}
	for _, state := range ep.TrackState {
		state.LeaderLineLength = nil
	}
	return nil
}

var dbFieldsMenu = toolbarMenu{rows: [][]toolbarButton{{
	inertToggle("NON-\nRVSM"),
	inertToggle("VRI"),
	inertToggle("CODE"),
	inertToggle("SPEED"),
	line4Button("DEST", Line4Destination),
	line4Button("TYPE", Line4Type),
	incDecButtonFunc("FDB LDR", func(ep *Scope) string { return fmt.Sprint(ep.currentPrefs().FDBLdrLength) }, clickFDBLeader),
	inertToggleOn("BCAST\nFLID"),
	toggleButton("PORTAL\nFENCE", func(ps *Preferences) *bool { return &ps.PortalFence }),
}, {
	inertToggle("NON-\nADS-B"),
	inertIncDec("NONADSB\n90"),
	inertToggle("SAT\nCOMM"),
	inertToggle("TFM\nREROUTE"),
	inertToggle("CRR\nRDB"),
	inertToggle("STA\nRDB"),
	inertToggle("DELAY\nRDB"),
	inertToggle("DELAY\nFORMAT"),
}}}

///////////////////////////////////////////////////////////////////////////
// VIEWS and CHECK LISTS

// viewButton makes a toggle for a view's visibility. Toggling it also
// closes the view's popup menu, which lives on the view.
func viewButton(label, viewID string, visible func(*Preferences) *bool) toolbarButton {
	b := toggleButton(label, visible)
	toggle := b.click
	b.click = func(ep *Scope, c toolbarClick) error {
		ep.closeViewPopup(viewID)
		return toggle(ep, c)
	}
	return b
}

var viewsMenu = toolbarMenu{rows: [][]toolbarButton{{
	viewButton("ALTIM\nSET", "altim-set", func(ps *Preferences) *bool { return &ps.AltimSet.Visible }),
	inertToggle("AUTO HO\nINHIB"),
	inertToggle("CFR"),
	viewButton("CODE", "beacon", func(ps *Preferences) *bool { return &ps.BeaconCodeView.Visible }),
	inertToggle("CONFLCT\nALERT"),
	inertToggle("CPDLC\nADV"),
	inertToggle("CPDLC\nHIST"),
	inertToggle("CPDLC\nMSGOUT"),
	inertToggle("CPDLC\nTOC SET"),
	toggleButtonFunc("CRR",
		func(ep *Scope) bool { return ep.currentPrefs().CRR.Visible },
		func(ep *Scope, _ toolbarClick) error {
			ep.closeViewPopup("crr")
			ps := ep.currentPrefs()
			ps.CRR.Visible = !ps.CRR.Visible
			if ps.CRR.Visible {
				ps.CRR.ListMode = true
			}
			return nil
		}),
}, {
	inertToggle("DEPT\nLIST"),
	inertToggle("FLIGHT\nEVENT"),
	inertToggle("GROUP\nSUP"),
	inertToggle("HOLD\nLIST"),
	inertToggle("INBND\nLIST"),
	inertToggle("MRP\nLIST"),
	inertToggle("SAA\nFILTER"),
	inertToggle("UA"),
	viewButton("WX\nREPORT", "wx", func(ps *Preferences) *bool { return &ps.WX.Visible }),
}}}

// checkListButton makes a toggle for one of the check lists. Only one is
// shown at a time, in a view the two share, so either list's popup menu
// closes.
func checkListButton(label string, list int) toolbarButton {
	return toggleButtonFunc(label,
		func(ep *Scope) bool { return ep.currentPrefs().CheckList.Visible == list },
		func(ep *Scope, _ toolbarClick) error {
			ep.closeViewPopup("pos-check")
			ep.closeViewPopup("emerg-check")
			ps := ep.currentPrefs()
			ps.CheckList.Visible = util.Select(ps.CheckList.Visible == list, checkListHidden, list)
			return nil
		})
}

var checkListsMenu = toolbarMenu{rows: [][]toolbarButton{{
	checkListButton("POS\nCHECK", checkListPos),
	checkListButton("EMERG\nCHECK", checkListEmerg),
}}}

///////////////////////////////////////////////////////////////////////////
// GeoMap and RADAR FILTER

var geoMapMenu = toolbarMenu{rowsFunc: (*Scope).videoMapFilterRows}

// videoMapFilterRows returns the GeoMap menu: a toggle for each of the
// loaded map's forty filter slots, ten to a row.
func (ep *Scope) videoMapFilterRows() [][]toolbarButton {
	rows := make([][]toolbarButton, 4)
	for i := range 40 {
		var vm videomaps.ERAMMap
		if i < len(ep.allVideoMaps) {
			vm = ep.allVideoMaps[i]
		}
		rows[i/10] = append(rows[i/10], videoMapFilterButton(vm))
	}
	return rows
}

// videoMapFilterButton makes the toggle for a GeoMap filter; a map without
// a label is an empty slot in the menu.
func videoMapFilterButton(vm videomaps.ERAMMap) toolbarButton {
	key := vm.Label()
	if key == "" {
		b := inertToggle("")
		b.noTearoff = true
		return b
	}
	return toggleButtonFunc(vm.LabelLine1+"\n"+vm.LabelLine2,
		func(ep *Scope) bool {
			_, visible := ep.currentPrefs().VideoMapVisible[key]
			return visible
		},
		func(ep *Scope, _ toolbarClick) error {
			ps := ep.currentPrefs()
			if _, visible := ps.VideoMapVisible[key]; visible {
				delete(ps.VideoMapVisible, key)
			} else {
				ps.VideoMapVisible[key] = nil
			}
			return nil
		})
}

var radarFilterMenu = toolbarMenu{rows: [][]toolbarButton{{
	inertToggle("ALL\nLDBS"),
	inertToggle("PR\nLDB"),
	inertToggle("UNP\nLDB"),
	inertToggle("ALL\nPRIM"),
	inertToggle("NON\nMODE C"),
}, {
	inertToggle("SELECT\nBEACON"),
	inertToggle("PERM\nECHO"),
	inertToggle("STROBE\nLINES"),
	incDecButton("HISTORY", func(ps *Preferences) *int { return &ps.HistoryLength }, 0, 5, 1),
}}}

///////////////////////////////////////////////////////////////////////////
// The toolbar control button

// toolbarControl is the TOOLBAR button that floats over the scope and opens
// the toolbar control menu. Its tear-off bar moves it.
var toolbarControl = menuButton("TOOLBAR", &toolbarControlMenu)

// masterRaiseButton makes the toggle that raises the main toolbar over the
// torn-off buttons and the views; it reads MASTER LOWER while it is raised.
func masterRaiseButton() toolbarButton {
	raised := func(ps *Preferences) *bool { return &ps.MasterToolbarRaised }
	b := toggleButton("", raised)
	b.labelFunc = func(ep *Scope) string {
		return util.Select(*raised(ep.currentPrefs()), "MASTER\nLOWER", "MASTER\nRAISE")
	}
	return b
}

var toolbarControlMenu = toolbarMenu{rows: [][]toolbarButton{{
	toggleButton("MASTER\nTOOLBAR", func(ps *Preferences) *bool { return &ps.DisplayToolbar }),
	inertToggle("MCA\nTOOLBAR"),
	inertToggle("HORIZ\nTOOLBAR"),
	inertToggle("LEFT\nTOOLBAR"),
	inertToggle("RIGHT\nTOOLBAR"),
}, {
	masterRaiseButton(),
	inertToggle("MCA\nRAISE"),
	inertToggle("HORIZ\nRAISE"),
	inertToggle("LEFT\nRAISE"),
	inertToggle("RIGHT\nRAISE"),
}}}

///////////////////////////////////////////////////////////////////////////
// Altitude limits filters

// altitudeLimitsPanel is the sub-entry box the ALT LIM button displays
// beside itself, for entering the altitude limits filters.
type altitudeLimitsPanel struct{}

// altitudeLimitsField identifies which of the sub-entry box's text boxes has
// been clicked and so is taking keyboard input.
type altitudeLimitsField int

const (
	altitudeLimitsNone altitudeLimitsField = iota
	altitudeLimitsCombined
	altitudeLimitsTargets
	altitudeLimitsLDBs
)

// altitudeLimitsEntry is the state of keyboard entry into the sub-entry
// box: which of its text boxes has been clicked and the text entered there
// so far.
type altitudeLimitsEntry struct {
	field   altitudeLimitsField
	buf     string
	invalid bool // the entry was rejected by [Enter]; INVALID is displayed
}

const altitudeLimitsInvalid = "INVALID"

// altitudeLimitsPad separates the edges of the sub-entry box, its labels, and
// its text boxes.
const altitudeLimitsPad = float32(4)

// altitudeLimitsRow is one label and text box pair in the sub-entry box.
type altitudeLimitsRow struct {
	field       altitudeLimitsField
	label       string
	limits      [2]int
	labelExtent math.Extent2D
	valueExtent math.Extent2D
	textY       float32 // shared by the label and the text box so they share a baseline
}

// altitudeLimitsLayout is the geometry of the sub-entry box: its outer extent
// and the pick areas of each of its rows.
type altitudeLimitsLayout struct {
	extent math.Extent2D
	rows   []altitudeLimitsRow
}

// altitudeLimitsButtonLabel returns the text of the ALT LIM toolbar button,
// which shows the filter when both halves agree and a row of Xs when they
// have been split to different ranges.
func altitudeLimitsButtonLabel(ep *Scope) string {
	ps := ep.currentPrefs()
	if ps.AltitudeLimits.Targets != ps.AltitudeLimits.LDBs {
		return "ALT LIM\n" + strings.Repeat("X", altitudeBlockLength)
	}
	return "ALT LIM\n" + formatAltitudeBlock(ps.AltitudeLimits.Targets)
}

// altitudeLimitsInkBounds returns the ink bounds of a full filter entry. The
// labels and the entries are all capitals and digits, which share this band,
// so it serves as a fixed reference that doesn't shift as a filter is typed.
func (ep *Scope) altitudeLimitsInkBounds() math.Extent2D {
	return ep.ERAMToolbarFont().InkBounds(formatAltitudeBlock(sim.UnrestrictedAltitudeLimits), 0)
}

// altitudeLimitsLayoutAt lays out the sub-entry box with its top-left corner
// at anchor.
func (ep *Scope) altitudeLimitsLayoutAt(anchor [2]float32) altitudeLimitsLayout {
	ps := ep.currentPrefs()
	font := ep.ERAMToolbarFont()

	rows := []altitudeLimitsRow{{field: altitudeLimitsCombined, label: "ALTITUDE LIMITS",
		limits: ps.AltitudeLimits.Targets}}
	if ps.AltitudeLimits.Split {
		rows = []altitudeLimitsRow{
			{field: altitudeLimitsTargets, label: "TARGETS", limits: ps.AltitudeLimits.Targets},
			{field: altitudeLimitsLDBs, label: "LDBS", limits: ps.AltitudeLimits.LDBs},
		}
	}

	var labelWidth float32
	for _, row := range rows {
		labelWidth = max(labelWidth, font.LayoutBounds(row.label, 0).Width())
	}
	valueWidth := font.LayoutBounds(formatAltitudeBlock(sim.UnrestrictedAltitudeLimits), 0).Width() + 2*altitudeLimitsPad

	width := 3*altitudeLimitsPad + labelWidth + valueWidth
	if ep.altLimits.invalid {
		width += altitudeLimitsPad + font.LayoutBounds(altitudeLimitsInvalid, 0).Width()
	}

	rowHeight := font.LayoutBounds(altitudeLimitsInvalid, 0).Height() + 2*altitudeLimitsPad
	rowsHeight := float32(len(rows)) * rowHeight
	// The box is at least as tall as the button it hangs off of; any extra
	// rows grow it downwards past the bottom of the toolbar.
	height := max(toolbarFaceHeight, rowsHeight+2*altitudeLimitsPad)

	// Text is placed by its ink rather than its layout box: the latter has
	// empty descender space below the capitals and digits these rows are made
	// of, which would sit the text low in the row and in its text box.
	inkCenter := ep.altitudeLimitsInkBounds().Center()[1]

	labelX := anchor[0] + altitudeLimitsPad
	valueX := labelX + labelWidth + altitudeLimitsPad
	top := anchor[1] - (height-rowsHeight)/2
	for i := range rows {
		rowTop := top - float32(i)*rowHeight
		rows[i].textY = rowTop - rowHeight/2 - inkCenter
		rows[i].labelExtent = math.Extent2D{P0: [2]float32{labelX, rowTop - rowHeight},
			P1: [2]float32{labelX + labelWidth, rowTop}}
		rows[i].valueExtent = math.Extent2D{P0: [2]float32{valueX, rowTop - rowHeight + altitudeLimitsPad/2},
			P1: [2]float32{valueX + valueWidth, rowTop - altitudeLimitsPad/2}}
	}

	return altitudeLimitsLayout{
		extent: math.Extent2D{P0: [2]float32{anchor[0], anchor[1] - height},
			P1: [2]float32{anchor[0] + width, anchor[1]}},
		rows: rows,
	}
}

func (altitudeLimitsPanel) extent(ep *Scope, anchor [2]float32) math.Extent2D {
	return ep.altitudeLimitsLayoutAt(anchor).extent
}

func (altitudeLimitsPanel) draw(ep *Scope, ctx *scope.Context, cb *renderer.CommandBuffer, anchor [2]float32) {
	ps := ep.currentPrefs()
	font := ep.ERAMToolbarFont()
	layout := ep.altitudeLimitsLayoutAt(anchor)

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	e := layout.extent
	p0, p1 := [2]float32{e.P0[0], e.P1[1]}, e.P1
	p2, p3 := [2]float32{e.P1[0], e.P0[1]}, e.P0
	trid.AddQuad(p0, p1, p2, p3, ps.Brightness.Toolbar.ScaleRGB(colors.toolbar.submenuBackground))

	mouse := ctx.Mouse
	style := renderer.TextStyle{Font: font, Color: ps.Brightness.Text.ScaleRGB(colors.toolbar.text)}

	for _, row := range layout.rows {
		label, value := row.labelExtent, row.valueExtent
		td.AddText(row.label, [2]float32{label.P0[0], row.textY}, style)

		hovered := mouse != nil && value.Inside(mouse.Pos)
		outline := util.Select(hovered, colors.toolbar.hoveredOutline, colors.menu.outerBorder)
		ld.AddLineLoop(ps.Brightness.Border.ScaleRGB(outline), [][2]float32{
			value.P0, {value.P1[0], value.P0[1]}, value.P1, {value.P0[0], value.P1[1]}})

		text := formatAltitudeBlock(row.limits)
		if row.field == ep.altLimits.field {
			text = ep.altLimits.buf
		}
		td.AddText(text, [2]float32{value.P0[0] + altitudeLimitsPad, row.textY}, style)

		if row.field == ep.altLimits.field && ep.altLimits.invalid {
			td.AddText(altitudeLimitsInvalid, [2]float32{value.P1[0] + altitudeLimitsPad, row.textY},
				renderer.TextStyle{Font: font, Color: ps.Brightness.Text.ScaleRGB(colors.yellow)})
		}
	}

	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
	ep.drawToolbarMenuOutline(ctx, cb, e)
}

// click handles a click in the sub-entry box: on a text box to start
// entering a new filter, or on a label to split the combined filter into
// separate ones or to rejoin them.
func (altitudeLimitsPanel) click(ep *Scope, anchor, p [2]float32) {
	ps := ep.currentPrefs()
	for _, row := range ep.altitudeLimitsLayoutAt(anchor).rows {
		if row.valueExtent.Inside(p) {
			// Any entry under way is finished first, so that moving from a
			// completed one to the other text box keeps it.
			ep.finishAltitudeLimitsEntry()
			ep.altLimits.field = row.field
			return
		}
		if row.labelExtent.Inside(p) {
			ep.finishAltitudeLimitsEntry()
			// Rejoining the two filters is only possible when they agree.
			if !ps.AltitudeLimits.Split {
				ps.AltitudeLimits.Split = true
			} else if ps.AltitudeLimits.Targets == ps.AltitudeLimits.LDBs {
				ps.AltitudeLimits.Split = false
			}
			return
		}
	}
	ep.finishAltitudeLimitsEntry()
}

// clickOutside finishes an entry under way; the click itself is left alone
// so that it still does what it otherwise would.
func (altitudeLimitsPanel) clickOutside(ep *Scope) {
	ep.finishAltitudeLimitsEntry()
}

// finishAltitudeLimitsEntry ends keyboard entry into the sub-entry box,
// keeping a complete and valid entry and silently discarding anything else.
func (ep *Scope) finishAltitudeLimitsEntry() {
	if limits, ok := parseAltitudeBlock(ep.altLimits.buf); ok && validAltitudeBlock(limits) {
		ep.setAltitudeLimits(limits)
	}
	ep.altLimits.field = altitudeLimitsNone
	ep.altLimits.buf = ""
	ep.altLimits.invalid = false
}

// setAltitudeLimits stores limits in the filter currently being entered; the
// combined filter sets both halves.
func (ep *Scope) setAltitudeLimits(limits [2]int) {
	ps := ep.currentPrefs()
	switch ep.altLimits.field {
	case altitudeLimitsTargets:
		ps.AltitudeLimits.Targets = limits
	case altitudeLimitsLDBs:
		ps.AltitudeLimits.LDBs = limits
	default:
		ps.AltitudeLimits.Targets = limits
		ps.AltitudeLimits.LDBs = limits
	}
}

// handleAltitudeLimitsKeyboard routes keystrokes to the sub-entry box while
// one of its text boxes has been clicked, returning true if it took them.
// Characters past a full entry are ignored rather than rejected.
func (ep *Scope) handleAltitudeLimitsKeyboard(ctx *scope.Context) bool {
	if ep.altLimits.field == altitudeLimitsNone {
		return false
	}

	for _, r := range strings.ToUpper(ctx.Keyboard.Input) {
		if r <= ' ' || r > '~' || len(ep.altLimits.buf) >= altitudeBlockLength {
			continue
		}
		ep.altLimits.buf += string(r)
		ep.altLimits.invalid = false
	}

	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if n := len(ep.altLimits.buf); n > 0 {
				ep.altLimits.buf = ep.altLimits.buf[:n-1]
				ep.altLimits.invalid = false
			}
		case imgui.KeyEnter:
			limits, ok := parseAltitudeBlock(ep.altLimits.buf)
			ok = ok && validAltitudeBlock(limits)
			if ok {
				ep.setAltitudeLimits(limits)
			}
			ep.altLimits.invalid = !ok
		case imgui.KeyEscape:
			ep.altLimits.field = altitudeLimitsNone
			ep.altLimits.buf = ""
			ep.altLimits.invalid = false
		}
	}
	return true
}
