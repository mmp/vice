package eram

import (
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/radar"
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

	AltitudeFilters []float32 // find out the different targets

	// QuickLookPositions []QuickLookPositiosn // find out more about this

	VideoMapVisible map[string]interface{}

	DisplayToolbar bool

	altitudeFilter [2]int

	Line4Size   int
	FDBSize     int
	PoralSize   int
	ToolbarSize int
	RDBSize     int // CRR datablocks
	LDBSize     int
	OutageSize  int

	VideoMapBrightness map[string]int

	// CRR view preferences and configuration
	CRR struct {
		Visible       bool
		ListMode      bool // true: list, false: panel
		Opaque        bool
		ShowBorder    bool
		Lines         int
		Font          int
		Bright        int
		SelectedColor CRRColor
		ColorBright   map[CRRColor]int
		Position      [2]float32
		DisplayFixes  bool // ATC TOOLS overlay of CRR fixes
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
	ClockPosition        []int
	commandBigPosition   [2]float32
	commandSmallPosition [2]float32
	CharSize             struct {
		Line4   int // Find out what this is
		RDB     int
		LDB     int
		FDB     int
		Toolbar int
		Outage  int // Again, what is this?
		Portal  int // Same here...
	}
	Brightness struct {
		Background radar.Brightness
		Cursor     radar.Brightness
		Text       radar.Brightness
		PRTGT      radar.Brightness
		UNPTGT     radar.Brightness
		PRHST      radar.Brightness
		UNPHST     radar.Brightness
		LDB        radar.Brightness
		SLDB       radar.Brightness
		WX         radar.Brightness
		NEXRAD     radar.Brightness
		Backlight  radar.Brightness
		Button     radar.Brightness
		Border     radar.Brightness
		Toolbar    radar.Brightness
		TBBRDR     radar.Brightness
		ABBRDR     radar.Brightness
		FDB        radar.Brightness
		Portal     radar.Brightness
		Satcomm    radar.Brightness
		ONFREQ     radar.Brightness
		Line4      radar.Brightness
		Dwell      radar.Brightness
		Fence      radar.Brightness
		DBFEL      radar.Brightness
		Outage     radar.Brightness
	}

	Line4Type int
}

const (
	Line4None = iota
	Line4Destination
	Line4Type
)

func makeDefaultPreferences() *Preferences {
	var prefs Preferences

	prefs.DisplayToolbar = true
	prefs.Range = 150
	prefs.VideoMapVisible = make(map[string]interface{})

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

	prefs.commandBigPosition = [2]float32{2, 80}
	prefs.commandSmallPosition = [2]float32{392, 80}
	prefs.altitudeFilter = [2]int{0, 999}

	prefs.Line4Size = 0
	prefs.FDBSize = 1
	prefs.PoralSize = 0
	prefs.ToolbarSize = 1
	prefs.RDBSize = 1
	prefs.LDBSize = 1
	prefs.OutageSize = 1

	prefs.VideoMapVisible = make(map[string]interface{})
	prefs.VideoMapBrightness = make(map[string]int)

	// CRR defaults
	prefs.CRR.Visible = true
	prefs.CRR.ListMode = true
	prefs.CRR.Opaque = true
	prefs.CRR.ShowBorder = true
	prefs.CRR.Lines = 13
	prefs.CRR.Font = 2
	prefs.CRR.Bright = 90
	prefs.CRR.SelectedColor = CRRGreen
	prefs.CRR.ColorBright = map[CRRColor]int{
		CRRGreen:   90,
		CRRYellow:  90,
		CRRMagenta: 90,
		CRRCyan:    90,
		CRRWhite:   90,
		CRRAmber:   90,
	}
	prefs.CRR.Position = [2]float32{10, 600}
	prefs.CRR.DisplayFixes = false
	return &prefs
}

func (ep *ERAMPane) initPrefsForLoadedSim(ss sim.State) *Preferences {
	// TODO: Add saving prefs with different ARTCCS/ sectors

	p := makeDefaultPreferences()
	p.Center = ss.GetInitialCenter()
	p.CurrentCenter = p.Center
	p.VideoMapGroup = ss.ScenarioDefaultVideoGroup
	p.ARTCC = ss.Facility
	p.Range = ss.Range
	return p
}

func (ep *ERAMPane) currentPrefs() *Preferences {
	return &ep.prefSet.Current
}
