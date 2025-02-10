package eram

import "github.com/mmp/vice/pkg/math"

type Preferences struct {
	CommonPreferences

	Name string

	Center math.Point2LL
	Range float32

	CurrentCenter math.Point2LL

	AltitudeFilters []float32 // find out the different targets

	// QuickLookPositions []QuickLookPositiosn // find out more about this 

	VideoMapVisible map[int]interface{}

	DisplayToolbar bool 
}

const numSavedPreferenceSets = 10

type PrefrenceSet struct {
	Current Preferences
	Selected *int 
	Saved [numSavedPreferenceSets]*Preferences
}

type CommonPreferences struct {
	ClockPosition []int 
	InputPosition []int
	OutputPosition []int
	CharSize struct{
		Line4 int // Find out what this is
		RDB int 
		LDB int 
		FDB int 
		Toolbar int 
		Outage int // Again, what is this?
		Portal int // Same here...
	}
	Brightness struct{
		Background ERAMBrightness 
		Cursor ERAMBrightness 
		Text ERAMBrightness 
		PRTGT ERAMBrightness 
		UNPTGT ERAMBrightness 
		PRHST ERAMBrightness 
		UNPHST ERAMBrightness
		LDB ERAMBrightness 
		SLDB ERAMBrightness 
		WX ERAMBrightness 
		NEXRAD ERAMBrightness 
		Backlight ERAMBrightness 
		Button ERAMBrightness 
		Border ERAMBrightness 
		Toolbar ERAMBrightness
		TBBRDR ERAMBrightness
		ABBRDR ERAMBrightness
		FDB ERAMBrightness
		Portal ERAMBrightness 
		Satcomm ERAMBrightness 
		ONFREQ ERAMBrightness 
		Line4 ERAMBrightness 
		Dwell ERAMBrightness 
		Fence ERAMBrightness 
		DBFEL ERAMBrightness 
		Outage ERAMBrightness
	}
}

func makeDefaultPreferences() *Preferences {
	var prefs Preferences

	prefs.DisplayToolbar = true
	prefs.Range = 150
	prefs.VideoMapVisible = make(map[int]interface{})

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
	
	return &prefs
}

func (ep *ERAMPane) initPrefsForLoadedSim() *Preferences {
	// TODO: Add saving prefs with different ARTCCS/ sectors
	return makeDefaultPreferences()
}

func (ep *ERAMPane) currentPrefs() *Preferences {
	
	return &ep.prefSet.Current
}

