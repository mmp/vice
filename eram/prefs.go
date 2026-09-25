// eram/prefs.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"fmt"
	"strconv"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/sim"
)

type Preferences struct {
	CommonPreferences

	Name string

	// ARTCC facility identifier for which this preference set applies (e.g., ZNY)
	ARTCC string

	Center math.Point2LL
	Range  float32

	CurrentCenter math.Point2LL

	VideoMapGroup string // ZNYMAP, AREAA, AREAB, etc

	// QuickLookPositions []QuickLookPositiosn // find out more about this

	VideoMapVisible map[string]any

	DisplayToolbar bool

	// Altitude limits filters, low and high, in hundreds of feet. When
	// Split is false the two ranges are held equal and the toolbar shows a
	// single combined filter; splitting them carries the combined value
	// into both.
	AltitudeLimits struct {
		Split   bool
		Targets [2]int
		LDBs    [2]int
	}

	Line4Size   int
	FDBSize     int
	PoralSize   int
	ToolbarSize int
	RDBSize     int // CRR datablocks
	LDBSize     int
	OutageSize  int
	CursorSize  int

	VideoMapBrightness map[string]scope.Brightness
	HistoryLength      int

	UseRightClick bool

	// CRR view preferences and configuration
	CRR struct {
		Visible       bool
		ListMode      bool // true: list, false: panel
		Opaque        bool
		ShowBorder    bool
		Lines         int
		Font          int
		Bright        scope.Brightness
		SelectedColor CRRColor
		ColorBright   map[CRRColor]scope.Brightness
		Position      [2]float32
		DisplayFixes  bool // ATC TOOLS overlay of CRR fixes
	}

	// ALTIM SET view preferences
	AltimSet struct {
		Visible        bool
		Position       [2]float32
		Opaque         bool
		ShowBorder     bool
		ShowIndicators bool
		Lines          int
		Col            int              // 1-4 columns
		Font           int              // 1-3
		Bright         scope.Brightness // 0-100
	}

	// WX view preferences
	WX struct {
		Visible        bool
		Position       [2]float32
		Opaque         bool
		ShowBorder     bool
		ShowIndicators bool
		Lines          int
		Font           int              // 1-3
		Bright         scope.Brightness // 0-100
	}

	// MCA (Message Composition Area) preferences
	MCA struct {
		Position [2]float32       // top-left of the feedback box
		PALines  int              // max number of preview/feedback area lines
		Width    int              // chars per line
		Font     int              // 1-3
		Bright   scope.Brightness // 0-100
	}

	// RA (Response Area) preferences
	RA struct {
		Position [2]float32
		Width    int              // chars per line
		Font     int              // 1-3
		Bright   scope.Brightness // 0-100
	}

	// TimeView (clock) preferences
	TimeView struct {
		Position   [2]float32
		Opaque     bool
		ShowBorder bool
		Font       int              // 1-3
		Bright     scope.Brightness // 0-100
	}

	BeaconCodeView struct {
		Visible    bool
		Position   [2]float32
		Opaque     bool
		ShowBorder bool
		Lines      int
		Col        int
		Font       int
		Bright     scope.Brightness
		SortManual bool
	}

	// Check list views (POS CHECK / EMERG CHECK). Only one is visible at a
	// time; Visible is an enum (checkListHidden/checkListPos/checkListEmerg)
	// rather than two booleans so the mutual exclusion is structural.
	CheckList struct {
		Visible    int
		Position   [2]float32
		Opaque     bool
		ShowBorder bool
		Lines      int
		Font       int
		Highlight  scope.Brightness
		Text       scope.Brightness
	}
}

const numSavedPreferenceSets = 10

type PrefrenceSet struct {
	Current  Preferences
	Selected *int
	// Saved preference slots (10). Some slots may be nil. Each stored
	// Preferences includes ARTCC and VideoMapGroup so we can filter rows.
	Saved [numSavedPreferenceSets]*Preferences
}

type CommonPreferences struct {
	CharSize struct {
		Line4   int // Find out what this is
		RDB     int
		LDB     int
		FDB     int
		Toolbar int
		Outage  int // Again, what is this?
		Portal  int // Same here...
	}
	Brightness struct {
		Background scope.Brightness
		Cursor     scope.Brightness
		Text       scope.Brightness
		PRTGT      scope.Brightness
		UNPTGT     scope.Brightness
		PRHST      scope.Brightness
		UNPHST     scope.Brightness
		LDB        scope.Brightness
		SLDB       scope.Brightness
		WX         scope.Brightness
		NEXRAD     scope.Brightness
		Backlight  scope.Brightness
		Button     scope.Brightness
		Border     scope.Brightness
		Toolbar    scope.Brightness
		TBBRDR     scope.Brightness
		ABBRDR     scope.Brightness
		FDB        scope.Brightness
		Portal     scope.Brightness
		Satcomm    scope.Brightness
		ONFREQ     scope.Brightness
		Line4      scope.Brightness
		Dwell      scope.Brightness
		Fence      scope.Brightness
		DBFEL      scope.Brightness
		Outage     scope.Brightness
	}

	Line4Type    int
	FDBLdrLength int // Datablock leader line length: 0=no line (W/E only), 1=normal (default), 2=2x, 3=3x

	// PortalFence controls display of the FDB portal fence, which separates
	// line 0 and column 0 from the rest of the datablock.
	PortalFence bool

	// NexradLevel encodes which NEXRAD precipitation levels are displayed,
	// using the digits the NX LVL toolbar button shows: 0=OFF, 3=Extreme,
	// 23=Heavy+Extreme, 123=Moderate+Heavy+Extreme.
	NexradLevel int

	TornOffButtons        map[string][2]float32 // button name -> screen position
	MasterToolbarPosition [2]float32            // top-left position of the master toolbar button
}

const (
	Line4None = iota
	Line4Destination
	Line4Type
)

// Settings for the NX LVL toolbar button / the NexradLevel preference.
// Non-OFF values match the digits the button displays (e.g., 23 = Heavy and
// Extreme). OFF is intentionally non-zero so the JSON zero value (from
// preference sets that predate this field) is reserved as "uninitialized"
// and gets migrated to the default.
const (
	NexradToolbarOff     = -1
	NexradToolbarExtreme = 3
	NexradToolbarHeavy   = 23
	NexradToolbarAll     = 123
)

// altitudeBlockLength is the number of characters in an altitude block
// entry such as "100B230".
const altitudeBlockLength = 7

// formatAltitudeBlock renders a floor and ceiling in hundreds of feet in the
// <floor>B<ceiling> form that parseAltitudeBlock reads.
func formatAltitudeBlock(block [2]int) string {
	return fmt.Sprintf("%03dB%03d", block[0], block[1])
}

// parseAltitudeBlock parses a <floor>B<ceiling> altitude entry in hundreds
// of feet, such as "100B230", as given to QD for the altitude limits filters
// and QZ for a block altitude. It only checks the format; the entry may still
// fail validAltitudeBlock.
func parseAltitudeBlock(s string) (block [2]int, ok bool) {
	if len(s) != altitudeBlockLength || s[3] != 'B' {
		return [2]int{}, false
	}
	for i := range altitudeBlockLength {
		if i != 3 && !isNum(s[i]) {
			return [2]int{}, false
		}
	}

	low, _ := strconv.Atoi(s[:3])
	high, _ := strconv.Atoi(s[4:])
	return [2]int{low, high}, true
}

// validAltitudeBlock reports whether block spans a range: a floor equal to
// or above the ceiling is rejected.
func validAltitudeBlock(block [2]int) bool {
	return block[0] < block[1]
}

// defaultERAMRange is the initial scope range, in nautical miles of vertical
// extent, when no facility or scenario adapts one.
const defaultERAMRange = 300

func makeDefaultPreferences() *Preferences {
	var prefs Preferences

	prefs.DisplayToolbar = true
	prefs.Range = defaultERAMRange
	prefs.VideoMapVisible = make(map[string]any)

	prefs.CharSize.Line4 = 0
	prefs.CharSize.RDB = 1
	prefs.CharSize.LDB = 1
	prefs.CharSize.FDB = 1
	prefs.CharSize.Toolbar = 1
	prefs.CharSize.Outage = 1
	prefs.CharSize.Portal = 0

	prefs.Brightness.Background = 26
	prefs.Brightness.Cursor = 100
	prefs.Brightness.Text = 90
	prefs.Brightness.PRTGT = 92
	prefs.Brightness.UNPTGT = 92
	prefs.Brightness.PRHST = 16
	prefs.Brightness.UNPHST = 16
	prefs.Brightness.LDB = 60
	prefs.Brightness.SLDB = 5
	prefs.Brightness.WX = 50
	prefs.Brightness.NEXRAD = 50
	prefs.Brightness.Backlight = 90
	prefs.Brightness.Button = 80
	prefs.Brightness.Border = 56
	prefs.Brightness.Toolbar = 40
	prefs.Brightness.TBBRDR = 50
	prefs.Brightness.ABBRDR = 56
	prefs.Brightness.FDB = 90
	prefs.Brightness.Portal = 0
	prefs.Brightness.Satcomm = 90
	prefs.Brightness.ONFREQ = 90
	prefs.Brightness.Line4 = 0
	prefs.Brightness.Dwell = 20
	prefs.Brightness.Fence = 90
	prefs.Brightness.DBFEL = 80
	prefs.Brightness.Outage = 80

	prefs.AltitudeLimits.Targets = sim.UnrestrictedAltitudeLimits
	prefs.AltitudeLimits.LDBs = sim.UnrestrictedAltitudeLimits
	prefs.TornOffButtons = make(map[string][2]float32)

	prefs.NexradLevel = NexradToolbarAll

	prefs.Line4Size = 0
	prefs.FDBSize = 1
	prefs.PoralSize = 0
	prefs.ToolbarSize = 1
	prefs.RDBSize = 1
	prefs.LDBSize = 1
	prefs.OutageSize = 1
	prefs.CursorSize = 1

	prefs.FDBLdrLength = 1 // Default to normal mode
	prefs.PortalFence = true

	prefs.VideoMapVisible = make(map[string]any)
	prefs.VideoMapBrightness = make(map[string]scope.Brightness)

	// CRR defaults
	prefs.CRR.Visible = true
	prefs.CRR.ListMode = true
	prefs.CRR.Opaque = false
	prefs.CRR.ShowBorder = true
	prefs.CRR.Lines = 13
	prefs.CRR.Font = 2
	prefs.CRR.Bright = 90
	prefs.CRR.SelectedColor = CRRGreen
	prefs.CRR.ColorBright = map[CRRColor]scope.Brightness{
		CRRGreen:   90,
		CRRYellow:  90,
		CRRMagenta: 90,
		CRRCyan:    90,
		CRRWhite:   90,
		CRRAmber:   90,
	}
	prefs.CRR.Position = [2]float32{100, 900}
	prefs.CRR.DisplayFixes = false

	prefs.HistoryLength = 5

	prefs.AltimSet.Visible = false
	prefs.AltimSet.Position = [2]float32{100, 900}
	prefs.AltimSet.Opaque = false
	prefs.AltimSet.ShowBorder = true
	prefs.AltimSet.ShowIndicators = true
	prefs.AltimSet.Lines = 5
	prefs.AltimSet.Col = 1
	prefs.AltimSet.Font = 2
	prefs.AltimSet.Bright = 80

	prefs.WX.Visible = false
	prefs.WX.Position = [2]float32{100, 900}
	prefs.WX.Opaque = false
	prefs.WX.ShowBorder = true
	prefs.WX.ShowIndicators = true
	prefs.WX.Lines = 5
	prefs.WX.Font = 2
	prefs.WX.Bright = 80

	prefs.BeaconCodeView.Visible = false
	prefs.BeaconCodeView.Position = [2]float32{100, 900}
	prefs.BeaconCodeView.Opaque = false
	prefs.BeaconCodeView.ShowBorder = true
	prefs.BeaconCodeView.Lines = 11
	prefs.BeaconCodeView.Col = 5
	prefs.BeaconCodeView.Font = 2
	prefs.BeaconCodeView.Bright = 100

	prefs.CheckList.Visible = checkListHidden
	prefs.CheckList.Position = [2]float32{100, 900}
	prefs.CheckList.Opaque = false
	prefs.CheckList.ShowBorder = true
	prefs.CheckList.Lines = 17
	prefs.CheckList.Font = 2
	prefs.CheckList.Highlight = 40
	prefs.CheckList.Text = 76

	prefs.MCA.Position = [2]float32{2, 80}
	prefs.MCA.PALines = 6
	prefs.MCA.Width = 32
	prefs.MCA.Font = 2
	prefs.MCA.Bright = 100

	prefs.RA.Position = [2]float32{392, 80}
	prefs.RA.Width = 27
	prefs.RA.Font = 2
	prefs.RA.Bright = 100

	prefs.TimeView.Opaque = false
	prefs.TimeView.ShowBorder = true
	prefs.TimeView.Font = 3
	prefs.TimeView.Bright = 100

	return &prefs
}

func (p *PrefrenceSet) Upgrade(from, to int) {
	p.Current.Upgrade(from, to)
	for _, sp := range p.Saved {
		if sp != nil {
			sp.Upgrade(from, to)
		}
	}
}

func (p *Preferences) Upgrade(from, to int) {
	if from < 72 {
		// ERAM Range now represents the vertical extent of the scope in NM
		// (matching the real-ERAM RANGE label), not the half-width passed to
		// the scope transform. Double the stored value so the on-screen extent
		// is unchanged.
		p.Range *= 2
	}
	if from < 74 {
		// MCA, RA, and TimeView preference structs were added; older saves
		// have zero values that would render as invisible/broken widgets.
		// These values match makeDefaultPreferences().
		p.MCA.PALines = 6
		p.MCA.Width = 32
		p.MCA.Font = 2
		p.MCA.Bright = 100
		p.RA.Width = 27
		p.RA.Font = 2
		p.RA.Bright = 100
		p.TimeView.ShowBorder = true
		p.TimeView.Font = 3
		p.TimeView.Bright = 100
		p.BeaconCodeView.ShowBorder = true
		p.BeaconCodeView.Lines = 11
		p.BeaconCodeView.Col = 5
		p.BeaconCodeView.Font = 2
		p.BeaconCodeView.Bright = 100
	}
	if from < 75 {
		// MCA/RA/TimeView positions moved from unexported CommonPreferences
		// fields (which never serialized) to exported fields on each view's
		// own struct. Initialize them to the defaults from
		// makeDefaultPreferences(). TimeView.Position stays zero; drawClock
		// fills it in once it knows the pane size.
		p.MCA.Position = [2]float32{2, 80}
		p.RA.Position = [2]float32{392, 80}

		// Fixed to only allow these widths; patch up if another width was set.
		if p.MCA.Width != 30 && p.MCA.Width != 50 {
			p.MCA.Width = 30
		}
		if p.RA.Width != 25 && p.RA.Width != 50 {
			p.RA.Width = 25
		}
	}
	if from < 76 {
		// BeaconCodeView and CheckList were each added without bumping the
		// serialize version, so saves from 74/75 can have these structs zero-
		// valued — and VisibleRows=0 then divides by zero in applyRowSource.
		// Backfill defaults only when Lines is the uninitialized zero so we
		// don't stomp on anyone who configured these views before the bump.
		if p.BeaconCodeView.Lines == 0 {
			p.BeaconCodeView.ShowBorder = true
			p.BeaconCodeView.Lines = 11
			p.BeaconCodeView.Col = 5
			p.BeaconCodeView.Font = 2
			p.BeaconCodeView.Bright = 100
		}
		if p.CheckList.Lines == 0 {
			p.CheckList.Visible = checkListHidden
			p.CheckList.Position = [2]float32{100, 900}
			p.CheckList.Opaque = false
			p.CheckList.ShowBorder = true
			p.CheckList.Lines = 17
			p.CheckList.Font = 2
			p.CheckList.Highlight = 40
			p.CheckList.Text = 76
		}
	}
	if from < 79 {
		// makeDefaultPreferences() never initialized BeaconCodeView, so prefs
		// created at versions 76-78 still have it zero-valued even though the
		// version-76 upgrade above backfills older saves.
		if p.BeaconCodeView.Lines == 0 {
			p.BeaconCodeView.ShowBorder = true
			p.BeaconCodeView.Lines = 11
			p.BeaconCodeView.Col = 5
			p.BeaconCodeView.Font = 2
			p.BeaconCodeView.Bright = 100
		}
		// Position was never set by any earlier upgrade either; a dragged view
		// never lands exactly on the origin, so zero means uninitialized.
		if p.BeaconCodeView.Position == [2]float32{} {
			p.BeaconCodeView.Position = [2]float32{100, 900}
		}
	}
	if from < 93 {
		// The altitude limits filters moved from an unexported field that
		// never serialized to AltitudeLimits, so every older save has them
		// zero-valued, which would filter out all but the ground.
		p.AltitudeLimits.Targets = sim.UnrestrictedAltitudeLimits
		p.AltitudeLimits.LDBs = sim.UnrestrictedAltitudeLimits
	}
}

// initialERAMRange is the scope range a sim starts at: the adapted one if
// there is any, otherwise the system default.
func initialERAMRange(ss client.SimState) float32 {
	if r := ss.GetInitialRange(); r != 0 {
		return r
	}
	return defaultERAMRange
}

// Reset applies the sim's initial settings to the preferences; it is called
// when a new sim is started so that they follow the scenario rather than
// carrying over from the last one flown at the same ARTCC.
func (p *Preferences) Reset(ss client.SimState) {
	p.Center = ss.GetInitialCenter()
	p.CurrentCenter = p.Center
	p.Range = initialERAMRange(ss)
	p.VideoMapGroup = ss.ScenarioDefaultVideoGroup
	p.VideoMapVisible = make(map[string]any)

	p.AltitudeLimits.Targets, p.AltitudeLimits.LDBs = ss.GetInitialAltitudeLimits()
	// Adapting the target and LDB filters separately brings the sub-entry box
	// up split, showing the two of them.
	p.AltitudeLimits.Split = p.AltitudeLimits.Targets != p.AltitudeLimits.LDBs
}

func (ep *Scope) initPrefsForLoadedSim(ss client.SimState) *Preferences {
	// TODO: Add saving prefs with different ARTCCS/ sectors

	p := makeDefaultPreferences()
	p.ARTCC = ss.Facility
	p.Reset(ss)
	return p
}

// resetPrefsForNewSim is called when a new Sim is started from scratch.
func (ep *Scope) resetPrefsForNewSim(ss client.SimState) {
	ep.ensurePrefSetForSim(ss)
	ep.prefSet.Current.Reset(ss)
}

func (ep *Scope) currentPrefs() *Preferences {
	return &ep.prefSet.Current
}
