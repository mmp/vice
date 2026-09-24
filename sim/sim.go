// sim/sim.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/enroute"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/goforj/godump"
)

type Sim struct {
	State *CommonState

	mu util.LoggingMutex

	Aircraft map[av.ADSBCallsign]*Aircraft

	ControlPositions     map[TCP]*av.Controller
	VirtualControllers   []TCP
	InboundAssignments   map[string]TCP // Inbound flow name -> TCP responsible
	DepartureAssignments map[string]TCP // Departure specifier -> TCP responsible
	GoAroundAssignments  map[string]TCP // Airport or airport/runway -> go-around controller

	STARSComputer *STARSComputer
	ERAMComputer  *ERAMComputer

	LocalCodePool *av.LocalSquawkCodePool
	CIDAllocator  *CIDAllocator

	TotalIFR, TotalVFR           int
	QuickFlightPlanIndex         int
	ScenarioDefaultConsolidation PositionConsolidation

	VFRReportingPoints []av.VFRReportingPoint

	wxModel    *wx.Model
	wxProvider *wx.Provider
	METAR      map[av.ICAOAirportCode][]wx.METAR

	ATISChangedTime map[av.ICAOAirportCode]Time

	eventStream *EventStream
	lg          *log.Logger

	// Airport -> runway -> state
	DepartureState map[av.ICAOAirportCode]map[av.RunwayID]*RunwayLaunchState
	// LastExitLaunch records when a departure last left each airport over
	// each of its exits. Going out a gate is a property of the airport, not
	// of one runway: two departures over the same fix must be spaced
	// whichever runways they use.
	LastExitLaunch map[av.ICAOAirportCode]map[av.ExitID]Time
	// Airport -> pattern state
	PatternState   map[av.ICAOAirportCode]*PatternState
	NextVFFRequest Time

	Handoffs  map[ACID]Handoff
	PointOuts map[ACID][]PointOut

	PrivilegedTCWs map[TCW]bool // TCWs with elevated privileges (can control any aircraft)

	FDAMSystemInhibited         bool
	DisabledFDAMRegions         map[string]struct{} // keyed by region ID
	EnforceUniqueCallsignSuffix bool

	PendingContacts        map[TCP][]PendingContact
	FutureFrequencyChanges []FutureFrequencyChange
	DeferredContacts       map[av.ADSBCallsign]map[ControlPosition]TCP
	FutureOnCourse         []FutureOnCourse
	// DeferredOnCourse holds departures that were handed off between two
	// virtual controllers before their tracks associated; they are sent on
	// course when association happens.
	DeferredOnCourse       map[ACID]bool
	FutureSquawkChanges    []FutureChangeSquawk
	FutureEmergencyUpdates []FutureEmergencyUpdate
	FutureFieldChecks      map[av.ADSBCallsign]*FutureFieldCheck
	FutureTrafficChecks    map[av.ADSBCallsign]*FutureTrafficCheck

	NextEmergencyTime Time

	PilotErrorInterval time.Duration
	LastPilotError     Time

	lastSimUpdate     Time
	updateTimeSlop    time.Duration
	lastSimUpdateTime time.Time // this is w.r.t. true wallclock time

	pausedByServer bool // set by server when no humans are connected

	lastControlCommandTime time.Time

	prespawn                 bool
	prespawnUncontrolledOnly bool
	prespawnPatternEligible  bool

	Rand *rand.Rand
	// textRand chooses how pilots phrase what they say and which voice
	// they say it in. It is kept apart from Rand so that neither rendering
	// text for clients nor changes to the phrasing change what the aircraft
	// do.
	textRand *rand.Rand

	// User-selected scenario start time before Vice rewinds the clock for prespawn.
	StartTime Time

	// Schedule holds the pregenerated IFR traffic the sim flies, for every
	// traffic source.
	Schedule FlightSchedule

	// When each kind's launches were switched to manual, so that switching
	// back to automatic pushes its schedule later by the time spent there:
	// every flight resumes as far from launch as it was when manual mode
	// began, rather than a backlog spawning at once. Zero when automatic.
	DepartureManualSince  Time
	ArrivalManualSince    Time
	OverflightManualSince Time

	// routed caches the route-database index published departures consult.
	routed *routedPairs

	// discardedArrivals counts the published arrivals dropped at each airport
	// because the scenario lands no traffic there, and discardedClashes the
	// published flights dropped for each callsign something else was already
	// flying; each is reported the first time it turns up.
	discardedArrivals map[av.ICAOAirportCode]int
	discardedClashes  map[string]int

	// The manual launch slots' pending flights, serialized with the sim so a
	// restored one shows the same aircraft. They hold no allocated resources.
	// Keys are airport/runway/category for departures, group/airport for
	// arrivals, the flow group for overflights, and the airport for VFR.
	PendingDepartures  map[string]*ScheduledDeparture
	PendingArrivals    map[string]*ScheduledArrival
	PendingOverflights map[string]*ScheduledOverflight
	PendingVFR         map[av.ICAOAirportCode]*Aircraft

	// nextVFRSample throttles retries after failed VFR route sampling; it is
	// just a retry timer, so a restored sim starting it fresh is fine.
	nextVFRSample map[av.ICAOAirportCode]Time

	// The launch control slots most recently built, cached per publication
	// generation since every client's state update carries them.
	launchSlotsBuilt     bool
	launchSlotGen        uint64
	launchDepartureSlots []DepartureLaunchSlot
	launchInboundSlots   []InboundLaunchSlot

	VoiceAssigner *VoiceAssigner

	SquawkWarnedACIDs map[ACID]any // Warn once in CheckLeaks(); don't spam the logs

	// No need to serialize these; they're caches anyway.
	bravoAirspace   *db.AirspaceGrid
	charlieAirspace *db.AirspaceGrid
	mvaGrid         *db.MVAGrid
	vfrTerminalAlts map[av.ICAOAirportCode]int

	// Waypoint commands: commands to execute when aircraft pass specific fixes
	waypointCommands map[TCP]map[string]string // tcp -> fix -> commands

	// lastSTTCommands stores the state needed to undo the most recent controller
	// transmission, per TCW: multiple users may share a TCW, and so share its radio
	// and its correction history. Only the single most recent one is tracked for each.
	lastSTTCommands map[TCW]*lastSTTCommand

	AvailableStripCIDs []int

	// State publication for server-paced long-poll delivery. pubGen is incremented whenever the
	// visible sim state changes; pubCh is closed to wake parked GetStateUpdate waiters and is
	// replaced with a fresh channel after each publication. simDoneCh is closed in Destroy() so
	// waiters can unblock when the sim goes away. lastPublishTime drives the per-second heartbeat
	// publish in Update().
	pubGen          uint64
	pubCh           chan struct{}
	simDoneCh       chan struct{}
	lastPublishTime time.Time
}

// lastSTTCommand stores the aircraft state from before the most recent controller
// transmission was executed, so that a following "correction" can undo it.
type lastSTTCommand struct {
	Callsign     av.ADSBCallsign
	NavSnapshot  nav.Snapshot
	ReportedATIS string
}

// NewSimConfiguration collects all of the information required to create a new Sim
type NewSimConfiguration struct {
	Facility    string
	Description string
	Brief       string

	Airports           map[av.ICAOAirportCode]*av.Airport
	DepartureRunways   []DepartureRunway
	ArrivalRunways     []ArrivalRunway
	InboundFlows       map[string]*av.InboundFlow
	LaunchConfig       LaunchConfig
	Fixes              map[string]math.Point2LL
	VFRReportingPoints []av.VFRReportingPoint

	ControlPositions        map[TCP]*av.Controller
	ControllerAirspace      map[TCP][]string
	VirtualControllers      []TCP
	ControllerConfiguration *ControllerConfiguration
	ConfigurationId         string

	TFRs                       []av.TFR
	FacilityAdaptation         FacilityAdaptation
	DisableTFRRestrictionAreas bool

	EnforceUniqueCallsignSuffix bool

	MagneticVariation      float32
	NmPerLongitude         float32
	StartTime              time.Time
	WindSpecifier          *wx.WindSpecifier
	Center                 math.Point2LL
	Range                  float32
	ScenarioCenter         math.Point2LL
	ScenarioRange          float32
	ScenarioAltitudeLimits AltitudeLimits
	DefaultMaps            []string
	DefaultMapGroup        string
	Airspace               av.Airspace

	PilotErrorInterval float32

	WXProvider *wx.Provider

	Emergencies []Emergency

	HandoffIDs []HandoffID
	// ERAMCoordination is the resolved pseudo-ERAM adaptation for this
	// facility's TRACON computer id, or nil if none is adapted.
	ERAMCoordination *enroute.Coordination
}

func NewSim(config NewSimConfiguration, lg *log.Logger) *Sim {
	s := &Sim{
		Aircraft: make(map[av.ADSBCallsign]*Aircraft),

		DepartureState: make(map[av.ICAOAirportCode]map[av.RunwayID]*RunwayLaunchState),
		LastExitLaunch: make(map[av.ICAOAirportCode]map[av.ExitID]Time),
		PatternState:   make(map[av.ICAOAirportCode]*PatternState),

		ControlPositions:     config.ControlPositions,
		InboundAssignments:   config.ControllerConfiguration.InboundAssignments,
		DepartureAssignments: config.ControllerConfiguration.DepartureAssignments,
		GoAroundAssignments:  config.ControllerConfiguration.GoAroundAssignments,

		STARSComputer: makeSTARSComputer(config.Facility),

		CIDAllocator: NewCIDAllocator(),

		LocalCodePool: av.MakeLocalSquawkCodePool(config.FacilityAdaptation.SSRCodes),

		VFRReportingPoints: config.VFRReportingPoints,

		wxModel: wx.MakeModel(config.WXProvider, config.Facility, string(config.FacilityAdaptation.WeatherStation),
			config.StartTime.UTC(), lg),
		METAR: make(map[av.ICAOAirportCode][]wx.METAR),

		ATISChangedTime: make(map[av.ICAOAirportCode]Time),

		eventStream: NewEventStream(lg),
		lg:          lg,

		EnforceUniqueCallsignSuffix: config.EnforceUniqueCallsignSuffix,

		PilotErrorInterval: time.Duration(config.PilotErrorInterval * float32(time.Minute)),
		LastPilotError:     NewSimTime(config.StartTime),

		FutureFieldChecks:   make(map[av.ADSBCallsign]*FutureFieldCheck),
		FutureTrafficChecks: make(map[av.ADSBCallsign]*FutureTrafficCheck),

		NextEmergencyTime: util.Select(config.LaunchConfig.EmergencyAircraftRate > 0, NewSimTime(config.StartTime), Time{}),

		lastSimUpdateTime: time.Now(),

		Handoffs:  make(map[ACID]Handoff),
		PointOuts: make(map[ACID][]PointOut),

		PrivilegedTCWs: make(map[TCW]bool),

		VirtualControllers: config.VirtualControllers,

		Rand:     rand.Make(),
		textRand: rand.Make(),

		StartTime: NewSimTime(config.StartTime.UTC()),

		SquawkWarnedACIDs: make(map[ACID]any),

		wxProvider: config.WXProvider,

		AvailableStripCIDs: func() []int {
			cids := make([]int, 1000)
			for i := range cids {
				cids[i] = i
			}
			rand.ShuffleSlice(cids, rand.Make())
			return cids
		}(),

		pubCh:     make(chan struct{}),
		simDoneCh: make(chan struct{}),
	}

	s.VoiceAssigner = NewVoiceAssigner(s.textRand)

	// Load METAR data from local resources
	apmetar, err := wx.GetMETAR(slices.Collect(maps.Keys(config.Airports)))
	if err != nil {
		lg.Errorf("%v", err)
	} else {
		for ap, msoa := range apmetar {
			s.loadMETARWindow(ap, msoa, config.StartTime)
		}
	}

	// Automatically add nearby airports and VORs as candidate reporting points
	for _, ap := range db.DB.Airports {
		if ap.Name == "" {
			continue
		}

		if len(ap.Runways) == 0 {
			// Only include airports from the FAA CIFP, not the mass of
			// them from the our airports database (which don't have
			// runways).
			continue
		}

		if slices.ContainsFunc([]string{"Airstrip", "Airpark", "Balloonport", "Base", "Field", "Heliport", "Helistop", "Helipad", "Strip"},
			func(s string) bool { return strings.HasSuffix(ap.Name, s) }) {
			continue
		}

		if math.NMDistance2LL(ap.Location, config.Center) < 75 {
			s.VFRReportingPoints = append(s.VFRReportingPoints,
				av.VFRReportingPoint{
					Description: ap.Name,
					Location:    av.ScenarioPoint2LL{Point2LL: ap.Location},
				})
		}
	}
	for _, na := range db.DB.Navaids {
		if math.NMDistance2LL(na.Location, config.Center) < 75 {
			s.VFRReportingPoints = append(s.VFRReportingPoints,
				av.VFRReportingPoint{
					Description: util.StopShouting(na.Name) + " VOR",
					Location:    av.ScenarioPoint2LL{Point2LL: na.Location},
				})
		}
	}

	s.ERAMComputer = makeERAMComputer(db.DB.ARTCCForFacility(config.Facility), s.LocalCodePool)

	s.State = newCommonState(config, config.StartTime.UTC(), s.wxModel, s.METAR, s.Rand, lg)
	s.ScenarioDefaultConsolidation = config.ControllerConfiguration.DefaultConsolidation

	return s
}

func (s *Sim) SetWaypointCommands(tcw TCW, commands string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	tcp := s.State.PrimaryPositionForTCW(tcw)
	if s.waypointCommands == nil {
		s.waypointCommands = make(map[TCP]map[string]string)
	}
	delete(s.waypointCommands, tcp)
	s.waypointCommands[tcp] = make(map[string]string)
	if len(commands) != 0 {
		s.PrivilegedTCWs[tcw] = true
	}

	for cmd := range strings.SplitSeq(commands, ",") {
		cmd = strings.TrimSpace(cmd)
		fix, cmds, ok := strings.Cut(cmd, ":")
		if !ok {
			return fmt.Errorf("missing ':' in waypoint command specifier %q", cmd)
		}
		if _, ok := db.DB.LookupWaypoint(fix); !ok {
			return fmt.Errorf("%s: unknown fix", fix)
		}
		s.waypointCommands[tcp][fix] = cmds // TODO: validate the commands here (somehow)
	}
	s.publish()
	return nil
}

func (s *Sim) ReplayScenario(waypointCommands string, durationSpec string, lg *log.Logger) error {
	// Parse replay duration
	var maxUpdates int
	var untilCallsign av.ADSBCallsign
	if after, ok := strings.CutPrefix(durationSpec, "until:"); ok {
		untilCallsign = av.ADSBCallsign(after)
		maxUpdates = 7200 // 2 hours max
		fmt.Printf("Running until aircraft %s completes (max %d seconds)\n", untilCallsign, maxUpdates)
	} else {
		var err error
		maxUpdates, err = strconv.Atoi(durationSpec)
		if err != nil {
			return fmt.Errorf("invalid replay duration: %s", durationSpec)
		}
		fmt.Printf("Running for %d seconds\n", maxUpdates)
	}

	// Activate the sim to initialize eventStream and other runtime state
	s.Activate(lg, nil)

	// Sign on as root controller + instructor with all positions
	tcw := TCW(s.ScenarioRootPosition())
	if err := s.SignOn(tcw, s.AllScenarioPositions()); err != nil {
		return fmt.Errorf("failed to sign on as controller %s: %w", tcw, err)
	}
	s.SetPrivilegedTCW(tcw, true) // Replay runs as instructor

	s.SetWaypointCommands(tcw, waypointCommands)

	fmt.Printf("Signed on as instructor: %s\n", tcw)
	fmt.Printf("Starting simulation with %d aircraft\n", len(s.Aircraft))

	// Run simulation
	startTime := time.Now()

	for i := range maxUpdates {
		s.Step(time.Second)

		// Check if target aircraft completed
		if untilCallsign != "" {
			if _, exists := s.Aircraft[untilCallsign]; !exists {
				fmt.Printf("Aircraft %s completed at %d seconds\n", untilCallsign, i+1)
				break
			}
		}
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\nSimulation complete:\n")
	fmt.Printf("  Duration: %d seconds simulated in %.2f seconds (%.1fx real-time)\n",
		min(maxUpdates, len(s.Aircraft)), elapsed.Seconds(), float64(min(maxUpdates, len(s.Aircraft)))/elapsed.Seconds())
	fmt.Printf("  Final aircraft count: %d\n", len(s.Aircraft))

	return nil
}

// AddMETARAirport loads METAR data for icao from bundled resources so
// that future state updates carry it in DynamicState.METAR. Returns
// av.ErrUnknownAirport if the ICAO is not in the aviation database;
// silently no-ops when the airport is known but has no bundled METAR
// data or when METAR has already been loaded for it.
func (s *Sim) AddMETARAirport(icao av.ICAOAirportCode) error {
	if _, ok := db.DB.LookupICAOAirport(icao); !ok {
		return av.ErrUnknownAirport
	}

	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, ok := s.METAR[icao]; ok {
		return nil
	}

	apmetar, err := wx.GetMETAR([]av.ICAOAirportCode{icao})
	if err != nil {
		return err
	}
	msoa, ok := apmetar[icao]
	if !ok {
		return nil
	}

	first, ok := s.loadMETARWindow(icao, msoa, s.State.SimTime.Time())
	if !ok {
		return nil
	}

	s.State.METAR[icao] = first

	s.publish()
	return nil
}

// loadMETARWindow decodes msoa for icao, appends the 24-hour window of
// entries starting at-or-before startTime into s.METAR[icao], and sets
// s.ATISChangedTime[icao] to the first entry's observation time. Returns
// the first entry of the window (used to seed s.State.METAR) and whether
// any entries were loaded. Caller is responsible for synchronization.
func (s *Sim) loadMETARWindow(icao av.ICAOAirportCode, msoa wx.METARSOA, startTime time.Time) (wx.METAR, bool) {
	metar := msoa.DecodeWindow(string(icao), startTime, 24*time.Hour)
	if len(metar) == 0 {
		return wx.METAR{}, false
	}
	s.ATISChangedTime[icao] = NewSimTime(metar[0].Time)
	s.METAR[icao] = append(s.METAR[icao], metar...)
	return metar[0], true
}

func (s *Sim) CallsignForACID(acid ACID) (av.ADSBCallsign, bool) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.callsignForACID(acid)
}

func (s *Sim) callsignForACID(acid ACID) (av.ADSBCallsign, bool) {
	for cs, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.NASFlightPlan.ACID == acid {
			return cs, true
		}
	}
	return av.ADSBCallsign(""), false
}

func (s *Sim) GetAircraftDisplayState(callsign av.ADSBCallsign) (AircraftDisplayState, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ac, ok := s.Aircraft[callsign]; !ok {
		return AircraftDisplayState{}, ErrNoMatchingFlight
	} else {
		return AircraftDisplayState{
			Spew:        godump.DumpStr(ac),
			FlightState: ac.NavSummary(s.wxModel, s.State.SimTime, s.textRand, s.lg),
		}, nil
	}
}

// *Aircraft may be nil. bool indicates whether the flight plan is active.
func (s *Sim) GetFlightPlanForACID(acid ACID) (*NASFlightPlan, *Aircraft, bool) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.getFlightPlanForACID(acid)
}

func (s *Sim) getFlightPlanForACID(acid ACID) (*NASFlightPlan, *Aircraft, bool) {
	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.NASFlightPlan.ACID == acid {
			return ac.NASFlightPlan, ac, true
		}
	}
	for i, fp := range s.STARSComputer.FlightPlans {
		if fp.ACID == acid {
			return s.STARSComputer.FlightPlans[i], nil, !fp.Location.IsZero()
		}
	}
	return nil, nil, false
}

func (s *Sim) TCWForPosition(pos ControlPosition) TCW {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.tcwForPosition(pos)
}

func (s *Sim) tcwForPosition(pos ControlPosition) TCW {
	return s.State.TCWForPosition(pos)
}

// Make sure we're not leaking beacon codes or list indices.
func (s *Sim) CheckLeaks() {
	var usedIndices [100]bool // 1-99 are handed out
	nUsedIndices := 0
	seenSquawks := make(map[av.Squawk]any)

	check := func(fp *NASFlightPlan) {
		if fp.ListIndex != UnsetSTARSListIndex {
			if usedIndices[fp.ListIndex] {
				s.lg.Errorf("List index %d used more than once", fp.ListIndex)
			} else {
				usedIndices[fp.ListIndex] = true
				nUsedIndices++
			}
		}

		_, warned := s.SquawkWarnedACIDs[fp.ACID]

		if _, ok := seenSquawks[fp.AssignedSquawk]; ok && !warned {
			s.lg.Warnf("%s: squawk code %q assigned to multiple aircraft", fp.ACID, fp.AssignedSquawk)
			s.SquawkWarnedACIDs[fp.ACID] = nil
		}
		seenSquawks[fp.AssignedSquawk] = nil

		if s.ERAMComputer.SquawkCodePool.InInitialPool(fp.AssignedSquawk) {
			if !s.ERAMComputer.SquawkCodePool.IsAssigned(fp.AssignedSquawk) && !warned {
				s.lg.Warnf("%s: squawking unassigned ERAM code %q", fp.ACID, fp.AssignedSquawk)
				s.SquawkWarnedACIDs[fp.ACID] = nil
			}
		} else if s.LocalCodePool.InInitialPool(fp.AssignedSquawk) {
			if !s.LocalCodePool.IsAssigned(fp.AssignedSquawk) && !warned {
				s.lg.Warnf("%s: squawking unassigned local code %q", fp.ACID, fp.AssignedSquawk)
				s.SquawkWarnedACIDs[fp.ACID] = nil
			}
		} else if !warned {
			// It may be controller-assigned to something arbitrary.
			s.lg.Warnf("%s: squawk code %q not in any pool", fp.ACID, fp.AssignedSquawk)
			s.SquawkWarnedACIDs[fp.ACID] = nil
		}
	}

	nAircraftFPs := 0
	for _, ac := range s.Aircraft {
		if ac.IsAssociated() {
			check(ac.NASFlightPlan)
			nAircraftFPs++
		}
	}
	nUnassociatedFPs := 0
	for _, fp := range s.STARSComputer.FlightPlans {
		check(fp)
		nUnassociatedFPs++
	}

	if len(s.STARSComputer.AvailableIndices) != 99-nUsedIndices {
		// Build the set of available indices for comparison
		availableSet := make(map[int]bool)
		for _, idx := range s.STARSComputer.AvailableIndices {
			availableSet[idx] = true
		}

		// Find leaked indices (not used and not available)
		var leaked []int
		for i := 1; i <= 99; i++ {
			if !usedIndices[i] && !availableSet[i] {
				leaked = append(leaked, i)
			}
		}

		s.lg.Errorf("%d available list indices but %d used so should be %d (aircraft FPs: %d, unassociated FPs: %d, leaked indices: %v)",
			len(s.STARSComputer.AvailableIndices), nUsedIndices, 99-nUsedIndices,
			nAircraftFPs, nUnassociatedFPs, leaked)
	}
}

func IsValidACID(acid string) bool {
	if len(acid) < 2 {
		return false
	}
	if acid[0] < 'A' || acid[0] > 'Z' {
		// Must start with a letter
		return false
	}
	for _, ch := range acid {
		if !((ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			// ACID must be alphanumeric
			return false
		}
	}
	return true
}
