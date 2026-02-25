// sim/sim.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/nav"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/brunoga/deep"
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
	wxProvider wx.Provider
	METAR      map[string][]wx.METAR

	ATISChangedTime map[string]time.Time

	eventStream *EventStream
	lg          *log.Logger

	// Airport -> runway -> state
	DepartureState map[string]map[av.RunwayID]*RunwayLaunchState
	// Key is inbound flow group name
	NextInboundSpawn map[string]time.Time
	NextVFFRequest   time.Time

	Handoffs  map[ACID]Handoff
	PointOuts map[ACID]PointOut

	PrivilegedTCWs map[TCW]bool // TCWs with elevated privileges (can control any aircraft)

	ReportingPoints []av.ReportingPoint

	FDAMSystemInhibited         bool
	DisabledFDAMRegions         map[string]struct{} // keyed by region ID
	EnforceUniqueCallsignSuffix bool

	PendingContacts         map[TCP][]PendingContact
	PendingFrequencyChanges []PendingFrequencyChange
	DeferredContacts        map[av.ADSBCallsign]map[ControlPosition]TCP
	FutureOnCourse          []FutureOnCourse
	FutureSquawkChanges     []FutureChangeSquawk
	FutureEmergencyUpdates  []FutureEmergencyUpdate

	NextEmergencyTime time.Time

	PilotErrorInterval time.Duration
	LastPilotError     time.Time

	lastSimUpdate  time.Time
	updateTimeSlop time.Duration
	lastUpdateTime time.Time // this is w.r.t. true wallclock time

	pausedByServer bool // set by server when no humans are connected

	lastControlCommandTime time.Time

	prespawn                 bool
	prespawnUncontrolledOnly bool

	NextPushStart time.Time // both w.r.t. sim time
	PushEnd       time.Time

	Rand *rand.Rand

	VoiceAssigner *VoiceAssigner

	SquawkWarnedACIDs map[ACID]any // Warn once in CheckLeaks(); don't spam the logs

	// No need to serialize these; they're caches anyway.
	bravoAirspace   *av.AirspaceGrid
	charlieAirspace *av.AirspaceGrid
	mvaGrid         *av.MVAGrid

	// Waypoint commands: commands to execute when aircraft pass specific fixes
	waypointCommands map[TCP]map[string]string // tcp -> fix -> commands

	// LastSTTCommand stores state needed to roll back a misheard STT command.
	// Only the single most recent command is tracked.
	LastSTTCommand *LastSTTCommand

	AvailableStripCIDs []int
}

// LastSTTCommand stores the nav snapshot from before the most recent STT command
// was executed, allowing rollback if the controller says "negative, that was for {other callsign}".
type LastSTTCommand struct {
	Callsign    av.ADSBCallsign
	NavSnapshot nav.NavSnapshot
}

type AircraftDisplayState struct {
	Spew        string // for debugging
	FlightState string // for display when paused
}

type Track struct {
	av.RadarTrack

	FlightPlan          *NASFlightPlan
	ControllerFrequency ControlPosition

	// Sort of hacky to carry these along here but it's convenient...
	DepartureAirport          string
	DepartureAirportElevation float32
	DepartureAirportLocation  math.Point2LL
	ArrivalAirport            string
	ArrivalAirportElevation   float32
	ArrivalAirportLocation    math.Point2LL
	FiledRoute                string
	FiledAltitude             int
	OnExtendedCenterline      bool
	OnApproach                bool
	ClearedForApproach        bool
	Approach                  string   // Full name of assigned approach, if any
	Fixes                     []string // Relevant fix names for STT
	SID                       string
	STAR                      string
	ATPAVolume                *av.ATPAVolume
	MVAsApply                 bool
	HoldForRelease            bool
	MissingFlightPlan         bool
	Route                     []math.Point2LL
	IsTentative               bool   // first 5 seconds after first contact
	CWTCategory               string // True CWT from aircraft performance DB, not from NAS flight plan
}

type DepartureRunway struct {
	Airport     string      `json:"airport"`
	Runway      av.RunwayID `json:"runway"`
	Category    string      `json:"category,omitempty"`
	DefaultRate int         `json:"rate"`
}

// GoAroundProcedure defines go-around parameters for a specific arrival runway.
type GoAroundProcedure struct {
	Heading           int      `json:"heading"` // degrees 1-360; 0 (or unset) means runway heading
	IsRunwayHeading   bool     // true when heading was 0 (runway heading) before resolution
	Altitude          int      `json:"altitude"`           // feet, e.g., 2000, 3000
	HandoffController TCP      `json:"handoff_controller"` // TCP (e.g., "1D")
	HoldDepartures    []string `json:"hold_departures"`    // runways to hold, empty = no holds
}

type ArrivalRunway struct {
	Airport  string             `json:"airport"`
	Runway   av.RunwayID        `json:"runway"`
	GoAround *GoAroundProcedure `json:"go_around,omitempty"`
}

type Handoff struct {
	AutoAcceptTime    time.Time
	ReceivingFacility string // only for auto accept
}

type PointOut struct {
	FromController ControlPosition
	ToController   ControlPosition
	AcceptTime     time.Time
}

type PilotSpeech struct {
	Callsign av.ADSBCallsign
	Type     av.RadioTransmissionType
	Text     string
	SimTime  time.Time // Virtual simulation time when transmission was made
}

// NewSimConfiguration collects all of the information required to create a new Sim
type NewSimConfiguration struct {
	Facility    string
	Description string

	Airports           map[string]*av.Airport
	PrimaryAirport     string
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

	TFRs               []av.TFR
	FacilityAdaptation FacilityAdaptation

	EnforceUniqueCallsignSuffix bool

	ReportingPoints   []av.ReportingPoint
	MagneticVariation float32
	NmPerLongitude    float32
	StartTime         time.Time
	WindSpecifier     *wx.WindSpecifier
	Center            math.Point2LL
	Range             float32
	DefaultMaps       []string
	DefaultMapGroup   string
	Airspace          av.Airspace

	PilotErrorInterval float32

	WXProvider wx.Provider

	Emergencies []Emergency

	HandoffIDs         []HandoffID
	FixPairs           []FixPairDefinition
	FixPairAssignments []FixPairAssignment
}

func NewSim(config NewSimConfiguration, manifest *VideoMapManifest, lg *log.Logger) *Sim {
	s := &Sim{
		Aircraft: make(map[av.ADSBCallsign]*Aircraft),

		DepartureState:   make(map[string]map[av.RunwayID]*RunwayLaunchState),
		NextInboundSpawn: make(map[string]time.Time),

		ControlPositions:     config.ControlPositions,
		InboundAssignments:   config.ControllerConfiguration.InboundAssignments,
		DepartureAssignments: config.ControllerConfiguration.DepartureAssignments,
		GoAroundAssignments:  config.ControllerConfiguration.GoAroundAssignments,

		STARSComputer: makeSTARSComputer(config.Facility),

		CIDAllocator: NewCIDAllocator(),

		LocalCodePool: av.MakeLocalSquawkCodePool(config.FacilityAdaptation.SSRCodes),

		VFRReportingPoints: config.VFRReportingPoints,

		wxModel: wx.MakeModel(config.WXProvider, config.Facility, config.PrimaryAirport, config.StartTime.UTC(), lg),
		METAR:   make(map[string][]wx.METAR),

		ATISChangedTime: make(map[string]time.Time),

		eventStream: NewEventStream(lg),
		lg:          lg,

		ReportingPoints: config.ReportingPoints,

		EnforceUniqueCallsignSuffix: config.EnforceUniqueCallsignSuffix,

		PilotErrorInterval: time.Duration(config.PilotErrorInterval * float32(time.Minute)),
		LastPilotError:     time.Now(),

		NextEmergencyTime: util.Select(config.LaunchConfig.EmergencyAircraftRate > 0, config.StartTime, time.Time{}),

		lastUpdateTime: time.Now(),

		Handoffs:  make(map[ACID]Handoff),
		PointOuts: make(map[ACID]PointOut),

		PrivilegedTCWs: make(map[TCW]bool),

		VirtualControllers: config.VirtualControllers,

		Rand: rand.Make(),

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
	}

	s.VoiceAssigner = NewVoiceAssigner(s.Rand)

	// Load METAR data from local resources
	apmetar, err := wx.GetMETAR(slices.Collect(maps.Keys(config.Airports)))
	if err != nil {
		lg.Errorf("%v", err)
	} else {
		for ap, msoa := range apmetar {
			metar := msoa.Decode()
			idx, ok := slices.BinarySearchFunc(metar, config.StartTime, func(m wx.METAR, t time.Time) int {
				return m.Time.Compare(t)
			})
			if !ok && idx > 0 {
				// METAR <= the start time
				idx--
			}
			s.ATISChangedTime[ap] = metar[idx].Time
			for idx < len(metar) && metar[idx].Time.Sub(config.StartTime) < 24*time.Hour {
				s.METAR[ap] = append(s.METAR[ap], metar[idx])
				idx++
			}
		}
	}

	// Automatically add nearby airports and VORs as candidate reporting points
	for _, ap := range av.DB.Airports {
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
					Location:    ap.Location,
				})
		}
	}
	for _, na := range av.DB.Navaids {
		if math.NMDistance2LL(na.Location, config.Center) < 75 {
			s.VFRReportingPoints = append(s.VFRReportingPoints,
				av.VFRReportingPoint{
					Description: util.StopShouting(na.Name) + " VOR",
					Location:    na.Location,
				})
		}
	}

	s.ERAMComputer = makeERAMComputer(av.DB.TRACONs[config.Facility].ARTCC, s.LocalCodePool)

	s.State = newCommonState(config, config.StartTime.UTC(), manifest, s.wxModel, s.METAR, s.Rand, lg)
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
		if _, ok := av.DB.LookupWaypoint(fix); !ok {
			return fmt.Errorf("%s: unknown fix", fix)
		}
		s.waypointCommands[tcp][fix] = cmds // TODO: validate the commands here (somehow)
	}
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
	_, _, err := s.SignOn(tcw, s.AllScenarioPositions())
	if err != nil {
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

func (s *Sim) Activate(lg *log.Logger, provider wx.Provider) {
	s.lg = lg

	if s.eventStream == nil {
		s.eventStream = NewEventStream(lg)
	}

	now := time.Now()
	s.lastUpdateTime = now
	s.lastControlCommandTime = now

	if s.Rand == nil {
		s.Rand = rand.Make()
	}

	s.wxProvider = provider
	if s.wxModel == nil {
		s.wxModel = wx.MakeModel(provider, s.State.Facility, s.State.PrimaryAirport, s.State.SimTime, s.lg)
	}

	// Restore json:"-" fields that are lost during JSON config save/load.
	restoreControllerFields(s.ControlPositions)
	restoreControllerFields(s.State.Controllers)
}

// restoreControllerFields reconstructs the json:"-" fields
// (FacilityIdentifier, ERAMFacility, Area) on controllers from the
// map key and Position. These fields are excluded from JSON
// serialization to prevent them from appearing in facility config
// files, but they need to be restored after loading a saved sim.
func restoreControllerFields(controllers map[TCP]*av.Controller) {
	for tcp, ctrl := range controllers {
		key := string(tcp)

		// FacilityIdentifier is the prefix: key = FacilityIdentifier + Position
		if len(key) > len(ctrl.Position) {
			ctrl.FacilityIdentifier = key[:len(key)-len(ctrl.Position)]
		}

		// ERAMFacility: ARTCC controllers have 2-digit numeric positions.
		if len(ctrl.Position) == 2 && ctrl.Position[0] >= '0' && ctrl.Position[0] <= '9' &&
			ctrl.Position[1] >= '0' && ctrl.Position[1] <= '9' {
			ctrl.ERAMFacility = true
		}

		// Note: Area is not restored here because it has a proper JSON tag
		// and survives serialization. It's auto-derived (for TRACON) or
		// manually specified (for ERAM) in PostDeserialize/rewriteControllers.
	}
}

func (s *Sim) Destroy() {
	s.eventStream.Destroy()
}

// Subscribe creates a new event subscription for this simulation.
// The caller is responsible for calling Unsubscribe when done.
func (s *Sim) Subscribe() *EventsSubscription {
	return s.eventStream.Subscribe()
}

func (s *Sim) GetSerializeSim() Sim {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return *s
}

func (s *Sim) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("state", s.State),
		slog.Any("departure_state", s.DepartureState),
		slog.Any("next_inbound_spawn", s.NextInboundSpawn),
		slog.Any("automatic_handoffs", s.Handoffs),
		slog.Any("automatic_pointouts", s.PointOuts),
		slog.Time("next_push_start", s.NextPushStart),
		slog.Time("push_end", s.PushEnd))
}

func (s *Sim) TogglePause() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.State.Paused = !s.State.Paused
	s.lastUpdateTime = time.Now() // ignore time passage...
	s.lastControlCommandTime = time.Now()
}

// SetPausedByServer allows the server to pause/unpause the sim when
// humans connect or disconnect.
func (s *Sim) SetPausedByServer(paused bool) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.pausedByServer = paused
	if !paused {
		// Reset time so we don't try to catch up
		s.lastUpdateTime = time.Now()
	}
}

func (s *Sim) FastForward() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for range 15 {
		s.State.SimTime = s.State.SimTime.Add(time.Second)
		s.updateState()
	}
	s.updateTimeSlop = 0
	s.lastUpdateTime = time.Now()
	s.lastControlCommandTime = time.Now()
}

func (s *Sim) IdleTime() time.Duration {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return time.Since(s.lastUpdateTime)
}

// SimTime returns the current simulation time.
func (s *Sim) SimTime() time.Time {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.State.SimTime
}

func (s *Sim) SetSimRate(tcw TCW, rate float32) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.State.SimRate = rate
	s.lastControlCommandTime = time.Now()

	s.lg.Infof("sim rate set to %f", s.State.SimRate)
	return nil
}

func (s *Sim) GlobalMessage(tcw TCW, message string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(Event{
		Type:           GlobalMessageEvent,
		WrittenText:    message,
		FromController: s.State.PrimaryPositionForTCW(tcw),
	})
}

func (s *Sim) CreateRestrictionArea(ra av.RestrictionArea) (int, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ra.UpdateTriangles()

	// Look for a free slot from one that was deleted
	for i, ua := range s.State.UserRestrictionAreas {
		if ua.Deleted {
			s.State.UserRestrictionAreas[i] = ra
			return i + 1, nil
		}
	}

	if n := len(s.State.UserRestrictionAreas); n < av.MaxRestrictionAreas {
		s.State.UserRestrictionAreas = append(s.State.UserRestrictionAreas, ra)
		return n + 1, nil
	}

	return 0, ErrTooManyRestrictionAreas
}

func (s *Sim) UpdateRestrictionArea(idx int, ra av.RestrictionArea) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Adjust for one-based indexing in the API call
	idx--

	if idx < 0 || idx >= len(s.State.UserRestrictionAreas) {
		return ErrInvalidRestrictionAreaIndex
	}
	if s.State.UserRestrictionAreas[idx].Deleted {
		return ErrInvalidRestrictionAreaIndex
	}

	// Update the triangulation just in case it's been moved.
	ra.UpdateTriangles()

	s.State.UserRestrictionAreas[idx] = ra
	return nil
}

func (s *Sim) DeleteRestrictionArea(idx int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Adjust for one-based indexing in the API call
	idx--

	if idx < 0 || idx >= len(s.State.UserRestrictionAreas) {
		return ErrInvalidRestrictionAreaIndex
	}
	if s.State.UserRestrictionAreas[idx].Deleted {
		return ErrInvalidRestrictionAreaIndex
	}

	s.State.UserRestrictionAreas[idx] = av.RestrictionArea{Deleted: true}
	return nil
}

type ATPAConfigOp int

const (
	ATPAEnable ATPAConfigOp = iota
	ATPADisable
	ATPAEnableVolume
	ATPADisableVolume
	ATPAEnableReduced25
	ATPADisableReduced25
)

func (s *Sim) ConfigureATPA(op ATPAConfigOp, volumeId string) (string, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if len(s.State.ATPAVolumeState) == 0 { // no volumes adapted
		return "", ErrATPADisabled
	}

	// 8.38 Enable/disable ATPA system-wide
	if op == ATPAEnable {
		if s.State.ATPAEnabled {
			return "NO CHANGE", nil
		}

		s.State.ATPAEnabled = true
		// Reset all volume states to defaults
		for _, airportState := range s.State.ATPAVolumeState {
			for _, volState := range airportState {
				volState.Disabled = false
				volState.Reduced25Disabled = false
			}
		}
		return "ATPA ENABLED", nil
	} else if op == ATPADisable {
		if !s.State.ATPAEnabled {
			return "NO CHANGE", nil
		}
		s.State.ATPAEnabled = false
		return "ATPA INHIBITED", nil
	}

	// All other ops need ATPA enabled and a valid volume.
	if !s.State.ATPAEnabled {
		return "", ErrATPADisabled
	}

	airport := s.State.FindAirportForATPAVolume(volumeId)
	if airport == "" {
		return "", ErrInvalidVolumeId
	}
	vol := s.State.Airports[airport].ATPAVolumes[volumeId]
	volState := s.State.ATPAVolumeState[airport][volumeId]

	// 8.39: Enable/disable ATPA approach volume
	if op == ATPAEnableVolume {
		if !volState.Disabled {
			return "NO CHANGE", nil
		}
		volState.Disabled = false
		volState.Reduced25Disabled = false
		return volumeId + " ENABLED", nil
	} else if op == ATPADisableVolume {
		if volState.Disabled {
			return "NO CHANGE", nil
		}
		volState.Disabled = true
		return volumeId + " INHIBITED", nil
	}

	// 8.40: Enable/disable 2.5nm reduced separation for volume
	if !vol.Enable25nmApproach {
		return "", ErrVolumeNot25nm
	}
	if volState.Disabled {
		return "", ErrVolumeDisabled
	}

	if op == ATPAEnableReduced25 {
		if !volState.Reduced25Disabled {
			return "NO CHANGE", nil
		}
		volState.Reduced25Disabled = false
		return volumeId + " 2.5 ENABLED", nil
	} else if op == ATPADisableReduced25 {
		if volState.Reduced25Disabled {
			return "NO CHANGE", nil
		}
		volState.Reduced25Disabled = true
		return volumeId + " 2.5 INHIBITED", nil
	}

	// Should not get here...
	return "", nil
}

type FDAMConfigOp int

const (
	FDAMToggleSystem FDAMConfigOp = iota
	FDAMEnableSystem
	FDAMInhibitSystem
	FDAMToggleRegion
	FDAMEnableRegion
	FDAMInhibitRegion
	FDAMQueryStatus
)

func (s *Sim) ConfigureFDAM(op FDAMConfigOp, regionId string) (string, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if len(s.State.FacilityAdaptation.Filters.FDAM) == 0 {
		return "", ErrFDAMNoRegions
	}

	switch op {
	case FDAMToggleSystem:
		s.FDAMSystemInhibited = !s.FDAMSystemInhibited
		return util.Select(s.FDAMSystemInhibited, "FLIGHT-DATA AUTO-MOD PROC OFF", "FLIGHT-DATA AUTO-MOD PROC ON"), nil
	case FDAMEnableSystem:
		s.FDAMSystemInhibited = false
		return "FLIGHT-DATA AUTO-MOD PROC ON", nil
	case FDAMInhibitSystem:
		s.FDAMSystemInhibited = true
		return "FLIGHT-DATA AUTO-MOD PROC OFF", nil
	}

	if s.FDAMSystemInhibited {
		return "", ErrFDAMProcessingOff
	}

	if op == FDAMQueryStatus {
		return s.fdamStatusString(), nil
	}

	if !s.State.FacilityAdaptation.Filters.FDAM.HaveId(regionId) {
		return "", ErrFDAMIllegalArea
	}

	if s.DisabledFDAMRegions == nil {
		s.DisabledFDAMRegions = make(map[string]struct{})
	}

	switch op {
	case FDAMToggleRegion:
		if _, ok := s.DisabledFDAMRegions[regionId]; ok {
			delete(s.DisabledFDAMRegions, regionId)
			return "REGION " + regionId + " ON", nil
		}
		s.DisabledFDAMRegions[regionId] = struct{}{}
		return "REGION " + regionId + " OFF", nil
	case FDAMEnableRegion:
		delete(s.DisabledFDAMRegions, regionId)
		return "REGION " + regionId + " ON", nil
	case FDAMInhibitRegion:
		s.DisabledFDAMRegions[regionId] = struct{}{}
		return "REGION " + regionId + " OFF", nil
	}
	return "", nil
}

func (s *Sim) fdamStatusString() string {
	var enabled, disabled []string
	for _, f := range s.State.FacilityAdaptation.Filters.FDAM {
		if _, ok := s.DisabledFDAMRegions[f.Id]; ok {
			disabled = append(disabled, f.Id)
		} else {
			enabled = append(enabled, f.Id)
		}
	}

	var output string
	appendRegions := func(regions []string, header string) {
		if len(regions) == 0 {
			return
		}
		if output != "" {
			output += "\n"
		}
		output += header + "\n"
		slices.Sort(regions)
		for i, id := range regions {
			if i > 0 && i%5 == 0 {
				output += "\n"
			} else if i > 0 {
				output += " "
			}
			output += id
		}
	}
	appendRegions(enabled, "ENAB FLIGHT-DATA AUTO-MOD FLTRS")
	appendRegions(disabled, "DISAB FLIGHT-DATA AUTO-MOD FLTRS")
	return output
}

func (s *Sim) PostEvent(e Event) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(e)
}

// GetUserState returns a deep copy of the simulation state for a client.
// Server-only fields (like Airport.Departures) are pruned to reduce bandwidth.
func (s *Sim) GetUserState() *UserState {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	state := UserState{
		CommonState:  *s.State,
		DerivedState: makeDerivedState(s),
	}

	// Make a deep copy so that any state changes after the lock is released aren't included.
	state = deep.MustCopy(state)

	// Prune server-only fields not needed by clients.
	for _, ap := range state.Airports {
		ap.Departures = nil
	}

	return &state
}

// GetControllerVideoMaps returns the video map configuration for the given TCW.
// Returns controller-specific config if available, otherwise facility defaults.
func (s *Sim) GetControllerVideoMaps(tcw TCW) (videoMaps, defaultMaps []string, beaconCodes []av.Squawk) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	tcp := s.State.PrimaryPositionForTCW(tcw)
	if config, ok := s.State.FacilityAdaptation.ControllerConfigs[tcp]; ok && len(config.VideoMapNames) > 0 {
		return config.VideoMapNames, config.DefaultMaps, config.MonitoredBeaconCodeBlocks
	}
	return s.State.FacilityAdaptation.VideoMapNames, s.State.ScenarioDefaultVideoMaps, s.State.FacilityAdaptation.MonitoredBeaconCodeBlocks
}

// GetDepartureController returns the TCP responsible for a departure given the
// airport, runway, and SID. Checks in order: airport/SID, airport/runway, airport only.
func (s *Sim) GetDepartureController(airport, runway, sid string) TCP {
	if sid != "" {
		if tcp, ok := s.DepartureAssignments[airport+"/"+sid]; ok {
			return tcp
		}
	}
	if runway != "" {
		if tcp, ok := s.DepartureAssignments[airport+"/"+runway]; ok {
			return tcp
		}
	}
	if tcp, ok := s.DepartureAssignments[airport]; ok {
		return tcp
	}
	return ""
}

// ScenarioRootPosition returns the root position from the scenario's default consolidation.
func (s *Sim) ScenarioRootPosition() TCP {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.scenarioRootPosition()
}

func (s *Sim) scenarioRootPosition() TCP {
	if root, err := s.ScenarioDefaultConsolidation.RootPosition(); err != nil {
		return ""
	} else {
		return root
	}
}

// AllScenarioPositions returns all positions defined in the scenario's default consolidation.
func (s *Sim) AllScenarioPositions() []TCP {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.ScenarioDefaultConsolidation.AllPositions()
}

// GetTrafficCounts returns the current IFR and VFR traffic counts.
func (s *Sim) GetTrafficCounts() (ifr, vfr int) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.TotalIFR, s.TotalVFR
}

// prepareRadioTransmissions adds callsign/controller prefixes to radio transmissions.
// (Multi-command batching is now handled at intent generation time in RunAircraftControlCommands.)
// This is called for both main event subscriptions and TTS event subscriptions.
// Must be called with s.mu held.
func (s *Sim) prepareRadioTransmissions(tcw TCW, events []Event) []Event {
	primaryTCP := s.State.PrimaryPositionForTCW(tcw)
	ctrl := s.State.Controllers[primaryTCP]

	// Add identifying info to radio transmissions destined for this TCW
	for i, e := range events {
		if e.Type != RadioTransmissionEvent || e.DestinationTCW != tcw {
			continue
		}

		ac, ok := s.Aircraft[e.ADSBCallsign]
		if !ok {
			continue
		}

		var heavySuper string
		if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok && !ctrl.ERAMFacility {
			if perf.WeightClass == "H" {
				heavySuper = " heavy"
			} else if perf.WeightClass == "J" {
				heavySuper = " super"
			}
		}

		switch e.RadioTransmissionType {
		case av.RadioTransmissionContact:
			// For emergency aircraft, 50% of the time add "emergency aircraft" after heavy/super.
			// Only on initial contact, not subsequent transmissions.
			if ac.EmergencyState != nil && s.Rand.Bool() {
				heavySuper += " emergency aircraft"
			}
			csArg := av.CallsignArg{
				Callsign:           ac.ADSBCallsign,
				IsEmergency:        ac.EmergencyState != nil,
				AlwaysFullCallsign: true,
			}
			var tr *av.RadioTransmission
			if ac.TypeOfFlight == av.FlightTypeDeparture {
				tr = av.MakeContactTransmission("{dctrl}, {callsign}"+heavySuper+". ", ctrl, csArg)
			} else {
				tr = av.MakeContactTransmission("{actrl}, {callsign}"+heavySuper+". ", ctrl, csArg)
			}
			events[i].WrittenText = tr.Written(s.Rand) + e.WrittenText
			events[i].SpokenText = tr.Spoken(s.Rand) + e.SpokenText
		case av.RadioTransmissionMixUp:
			// No additional formatting for mix-up transmissions; the callsign is already in there.
		case av.RadioTransmissionNoId:
			// No callsign formatting for NoId transmissions (e.g., "blocked").
		default:
			csArg := av.CallsignArg{
				Callsign:    ac.ADSBCallsign,
				IsEmergency: ac.EmergencyState != nil,
			}
			tr := av.MakeReadbackTransmission(", {callsign}"+heavySuper+". ", csArg)
			events[i].WrittenText = e.WrittenText + tr.Written(s.Rand)
			events[i].SpokenText = e.SpokenText + tr.Spoken(s.Rand)
		}
	}

	return events
}

// PrepareRadioTransmissionsForTCW processes events for TTS, adding
// callsign/controller prefixes to radio transmissions. This is the public API
// for the server to process TTS events from a separate subscription.
func (s *Sim) PrepareRadioTransmissionsForTCW(tcw TCW, events []Event) []Event {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.prepareRadioTransmissions(tcw, events)
}

func (s *Sim) GetStateUpdate(tcw TCW) StateUpdate {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.State.GenerationIndex++

	update := StateUpdate{
		DynamicState:     s.State.DynamicState,
		DerivedState:     makeDerivedState(s),
		FlightStripACIDs: s.flightStripACIDsForTCW(tcw),
	}

	if util.SizeOf(update, os.Stderr, false, 1024*1024) > 256*1024*1024 {
		fn := fmt.Sprintf("update_dump%d.txt", time.Now().Unix())
		f, err := os.Create(fn)
		if err != nil {
			s.lg.Errorf("%s: unable to create: %v", fn, err)
		} else {
			util.SizeOf(update, f, true, 1024)
			godump.Fdump(f, update)
		}
		panic("too big")
	}

	// While it seemed that this could be skipped, it's actually necessary
	// to avoid races: although another copy is made as it's marshaled to be
	// returned from RPC call, there may be other updates to the sim state
	// between this function returning and that happening.
	return deep.MustCopy(update)
}

// isVirtualController returns true if the given position is virtual
// (i.e., simulated by the system rather than allocated to humans).
// Virtual controllers auto-accept handoffs and pointouts.
// Human-allocatable positions (from ControllerConfig) do NOT auto-accept,
// regardless of whether a human is currently signed in.
func (s *Sim) isVirtualController(pos ControlPosition) bool {
	// A controller is virtual if it's a valid control position but NOT
	// a human-allocatable position (i.e., not in the consolidation hierarchy).
	if _, ok := s.ControlPositions[TCP(pos)]; !ok {
		return false
	}
	humanPositions := s.ScenarioDefaultConsolidation.AllPositions()
	return !slices.Contains(humanPositions, TCP(pos))
}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (s *Sim) Update() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if !util.DebuggerIsRunning() {
		startUpdate := time.Now()
		defer func() {
			if d := time.Since(startUpdate); d > 200*time.Millisecond {
				s.lg.Warn("unexpectedly long Sim Update() call", slog.Duration("duration", d),
					slog.Any("sim", s))
			}
		}()

		if time.Since(s.lastControlCommandTime) > 15*time.Minute && !s.State.Paused {
			s.eventStream.Post(Event{
				Type:        StatusMessageEvent,
				WrittenText: "Pausing sim due to inactivity.",
			})

			s.State.Paused = true
		}
	}

	if s.State.Paused || s.pausedByServer {
		return
	}

	// Figure out how much time has passed since the last update: wallclock
	// time is scaled by the sim rate, then we add in any time from the
	// last update that wasn't accounted for.
	elapsed := time.Since(s.lastUpdateTime)
	elapsed = time.Duration(s.State.SimRate * float32(elapsed))
	if s.Step(elapsed) {
		// Don't bother with this if we didn't change any aircraft state
		for _, ac := range s.Aircraft {
			ac.Check(s.lg)
		}
	}
	s.lastUpdateTime = time.Now()
}

// Step advances the simulation by the given elapsed time duration.
// This method encapsulates the core simulation stepping logic that was
// previously inline in Update().
func (s *Sim) Step(elapsed time.Duration) bool {
	elapsed += s.updateTimeSlop

	// Run the sim for this many seconds
	ns := int(elapsed.Truncate(time.Second).Seconds())
	if ns > 10 {
		s.lg.Warn("unexpected hitch in update rate", slog.Duration("elapsed", elapsed),
			slog.Int("steps", ns), slog.Duration("slop", s.updateTimeSlop))
	}
	for range ns {
		s.State.SimTime = s.State.SimTime.Add(time.Second)
		s.updateState()
	}

	s.updateTimeSlop = elapsed - elapsed.Truncate(time.Second)

	if ns > 0 {
		// Don't bother with this if we didn't change any aircraft state
		s.CheckLeaks()
	}

	return ns > 0
}

// separate so time management can be outside this so we can do the prespawn stuff...
func (s *Sim) updateState() {
	now := s.State.SimTime

	for acid, ho := range s.Handoffs {
		if !now.After(ho.AutoAcceptTime) && !s.prespawn {
			continue
		}

		if fp, ac, _ := s.getFlightPlanForACID(acid); fp != nil {
			if fp.HandoffController != "" && s.isVirtualController(fp.HandoffController) {
				// Automated accept
				s.eventStream.Post(Event{
					Type:           AcceptedHandoffEvent,
					FromController: fp.TrackingController,
					ToController:   fp.HandoffController,
					ACID:           fp.ACID,
				})
				s.lg.Debug("automatic handoff accept", slog.String("acid", string(fp.ACID)),
					slog.String("from", string(fp.TrackingController)),
					slog.String("to", string(fp.HandoffController)))

				previousTrackingController := fp.TrackingController
				newTrackingController := fp.HandoffController

				fp.TrackingController = newTrackingController
				if s.State.IsLocalController(fp.TrackingController) {
					fp.LastLocalController = fp.TrackingController
				}
				fp.OwningTCW = s.tcwForPosition(fp.TrackingController)
				fp.HandoffController = ""

				if ac != nil {
					haveTransferComms := slices.ContainsFunc(ac.Nav.Waypoints,
						func(wp av.Waypoint) bool { return wp.TransferComms() })
					if !haveTransferComms && s.isVirtualController(previousTrackingController) {
						s.virtualControllerTransferComms(ac, TCP(previousTrackingController), TCP(newTrackingController))
					}
				}
			}
		}
		delete(s.Handoffs, acid)
	}

	for acid, po := range s.PointOuts {
		if !now.After(po.AcceptTime) {
			continue
		}

		if fp, _, _ := s.getFlightPlanForACID(acid); fp != nil && s.isVirtualController(po.ToController) {
			// Note that "to" and "from" are swapped in the event,
			// since the ack is coming from the "to" controller of the
			// original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: po.ToController,
				ToController:   po.FromController,
				ACID:           acid,
			})
			s.lg.Debug("automatic pointout accept", slog.String("acid", string(acid)),
				slog.String("by", string(po.ToController)), slog.String("to", string(po.FromController)))

			delete(s.PointOuts, acid)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(s.lastSimUpdate) >= time.Second {
		s.lastSimUpdate = now

		for callsign, ac := range s.Aircraft {
			if ac.HoldForRelease && !ac.Released {
				// nvm...
				continue
			}
			if ac.WaitingForLaunch {
				continue
			}

			passedWaypoint := ac.Update(s.wxModel, s.State.SimTime, s.bravoAirspace, nil /* s.lg*/)

			if ac.Nav.Approach.RequestApproachClearance && ac.IsAssociated() {
				ac.Nav.Approach.RequestApproachClearance = false
				s.enqueuePilotTransmission(callsign, TCP(ac.ControllerFrequency), PendingTransmissionRequestApproachClearance)
			}

			if ac.Nav.Approach.GoAroundNoApproachClearance && ac.IsAssociated() {
				ac.Nav.Approach.GoAroundNoApproachClearance = false
				s.goAround(ac)
			}

			if ac.FirstSeen.IsZero() && s.isRadarVisible(ac) {
				ac.FirstSeen = s.State.SimTime
			}

			if passedWaypoint != nil {
				for tcp, wpCommands := range s.waypointCommands {
					if cmds, ok := wpCommands[passedWaypoint.Fix]; ok {
						// Moderately hacky: the mutex is held when we get here, but then RunAircraftControlCommands
						// will end up calling methods like Sim AssignAltitude that in turn need to acquire the mutex.
						// So... we'll just unlock it for now and grab the lock again before we continue.
						s.mu.Unlock(s.lg)

						// Execute waypoint commands using the waypoint commands controller (typically an instructor)
						nav.NavLog(string(callsign), s.State.SimTime, nav.NavLogCommand, "aircraft=%s fix=%s commands=%s", callsign, passedWaypoint.Fix, cmds)
						s.lg.Infof("Waypoint commands: Aircraft %s passed %s, executing: %s", callsign, passedWaypoint.Fix, cmds)
						result := s.RunAircraftControlCommands(TCW(tcp), callsign, cmds)
						if result.Error != nil {
							nav.NavLog(string(callsign), s.State.SimTime, nav.NavLogCommand, "aircraft=%s error=%v remaining=%s", callsign, result.Error,
								result.RemainingInput)
							s.lg.Errorf("Waypoint command execution failed: %v (remaining: %s)", result.Error, result.RemainingInput)
						} else {
							nav.NavLog(string(callsign), s.State.SimTime, nav.NavLogCommand, "aircraft=%s success", callsign)
						}

						s.mu.Lock(s.lg)
					}
				}

				// Handoffs still happen for "unassociated" (to us) tracks
				// when they're currently tracked by an external facility.
				if passedWaypoint.HumanHandoff() {
					// Handoff from virtual controller to a human controller.
					// During prespawn uncontrolled-only phase, cull aircraft that would be handed off to humans
					// rather than initiating the handoff.
					if s.prespawnUncontrolledOnly {
						s.deleteAircraft(ac)
						continue
					}
					sfp := ac.NASFlightPlan
					if sfp == nil {
						sfp = s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign))
					}
					if sfp != nil {
						s.handoffTrack(sfp, sfp.InboundHandoffController)
					}
				} else if passedWaypoint.HandoffController() != "" {
					// During prespawn uncontrolled-only phase, cull if handoff target is a human controller
					if s.prespawnUncontrolledOnly && !s.isVirtualController(TCP(passedWaypoint.HandoffController())) {
						s.deleteAircraft(ac)
						continue
					}
					sfp := ac.NASFlightPlan
					if sfp == nil {
						sfp = s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign))
					}
					if sfp != nil {
						s.handoffTrack(sfp, TCP(passedWaypoint.HandoffController()))
					}
				}

				if passedWaypoint.ClearApproach() {
					ac.ApproachTCP = TCP(ac.ControllerFrequency)
				}

				if passedWaypoint.GoAroundContactController() != "" {
					tcp := passedWaypoint.GoAroundContactController()
					ac.ControllerFrequency = ControlPosition(tcp)

					// Clear stale pending contacts and frequency changes from before
					// the go-around so the go-around transmission takes priority.
					s.cancelPendingFrequencyChange(ac.ADSBCallsign)
					for t := range s.PendingContacts {
						s.PendingContacts[t] = slices.DeleteFunc(s.PendingContacts[t], func(pc PendingContact) bool {
							return pc.ADSBCallsign == ac.ADSBCallsign &&
								(pc.Type == PendingTransmissionDeparture || pc.Type == PendingTransmissionArrival)
						})
					}

					s.enqueuePilotTransmission(ac.ADSBCallsign, tcp, PendingTransmissionGoAround)

					// Reassociate flight plan if controller dropped it
					sfp := ac.NASFlightPlan
					if sfp == nil {
						if sfp = s.STARSComputer.takeFlightPlanByACID(ACID(ac.ADSBCallsign)); sfp != nil {
							sfp.DeleteTime = time.Time{}
							sfp.OwningTCW = s.tcwForPosition(sfp.TrackingController)
							ac.AssociateFlightPlan(sfp)
							s.eventStream.Post(Event{
								Type: FlightPlanAssociatedEvent,
								ACID: sfp.ACID,
							})
						}
					}
					// Set up handoff from current tracker to go-around controller
					if sfp != nil && sfp.TrackingController != "" && sfp.TrackingController != tcp {
						s.handoffTrack(sfp, tcp)
					}
				}

				if ac.IsAssociated() {
					// Things that only apply to associated aircraft
					sfp := ac.NASFlightPlan

					if passedWaypoint.TransferComms() {
						// This is a departure that hasn't contacted the departure controller yet, do it here
						if ac.IsDeparture() && ac.DepartureContactAltitude == 0 {
							s.contactDeparture(ac, sfp)
						} else {
							// We didn't enqueue this before since we knew an
							// explicit comms handoff was coming so go ahead and
							// send them to the controller's frequency. Note that
							// we use InboundHandoffController and not
							// ac.TrackingController, since the human controller
							// may have already flashed the track to a virtual
							// controller.
							ctrl := s.State.ResolveController(sfp.InboundHandoffController)
							// Make sure they've bought the handoff.
							if ctrl != sfp.HandoffController {
								s.enqueueControllerContact(ac, TCP(ctrl), ac.ControllerFrequency)
							}
						}
					}

					// Update scratchpads if the waypoint has scratchpad commands
					// Only update if aircraft is controlled by a virtual controller
					if s.isVirtualController(ac.ControllerFrequency) {
						if passedWaypoint.PrimaryScratchpad() != "" {
							sfp.Scratchpad = passedWaypoint.PrimaryScratchpad()
						}
						if passedWaypoint.ClearPrimaryScratchpad() {
							sfp.Scratchpad = ""
						}
						if passedWaypoint.SecondaryScratchpad() != "" {
							sfp.SecondaryScratchpad = passedWaypoint.SecondaryScratchpad()
						}
						if passedWaypoint.ClearSecondaryScratchpad() {
							sfp.SecondaryScratchpad = ""
						}
					}

					if passedWaypoint.PointOut() != "" {
						if ctrl, ok := s.State.Controllers[TCP(passedWaypoint.PointOut())]; ok {
							// Only do automatic point outs for virtual controllers
							if s.isVirtualController(ac.ControllerFrequency) {
								fromCtrl := s.State.Controllers[TCP(ac.ControllerFrequency)]
								s.pointOut(sfp.ACID, fromCtrl, ctrl)
								break
							}
						}
					}
				}

				if passedWaypoint.Delete() {
					s.lg.Debug("deleting aircraft at waypoint", slog.Any("waypoint", passedWaypoint))
					s.deleteAircraft(ac)
				}

				if passedWaypoint.Land() {
					// There should be an altitude restriction at the final approach waypoint, but
					// be careful.
					alt := passedWaypoint.AltitudeRestriction()
					// If we're more than 200 feet AGL, go around.
					lowEnough := alt == nil || ac.Altitude() <= alt.TargetAltitude(ac.Altitude())+200
					if lowEnough {
						s.lg.Debug("deleting landing at waypoint", slog.Any("waypoint", passedWaypoint))

						// Record the landing if necessary for scheduling departures.
						if depState, ok := s.DepartureState[ac.FlightPlan.ArrivalAirport]; ok {
							var runway string
							if ac.Nav.Approach.Assigned != nil {
								// IFR aircraft with assigned approach
								runway = ac.Nav.Approach.Assigned.Runway
							} else {
								// VFR aircraft - select best runway based on wind
								ap := av.DB.Airports[ac.FlightPlan.ArrivalAirport]
								as := s.wxModel.Lookup(ap.Location, float32(ap.Elevation), s.State.SimTime)
								if rwy, _ := ap.SelectBestRunway(as.WindDirection(), s.State.MagneticVariation); rwy != nil {
									runway = rwy.Id
								}
							}

							for rwyID, rwyState := range depState {
								if rwyID.Base() == runway {
									rwyState.LastArrivalLandingTime = s.State.SimTime
									rwyState.LastArrivalFlightRules = ac.FlightPlan.Rules
								}
							}
						}

						s.deleteAircraft(ac)
					} else {
						s.goAround(ac)
					}
				}
			}

			// Possibly go around
			if ac.GoAroundDistance != nil {
				if d, err := ac.DistanceToEndOfApproach(); err == nil && d < *ac.GoAroundDistance {
					s.lg.Debug("randomly going around")
					ac.GoAroundDistance = nil // only go around once
					s.goAround(ac)
				}
			}

			// Cull any departures withn ~5NM of their first /tc point
			culled := false
			if s.prespawnUncontrolledOnly && ac.IsDeparture() && ac.DepartureContactAltitude == 0 {
				for _, wp := range ac.Nav.Waypoints {
					if wp.TransferComms() {
						if math.NMDistance2LLFast(ac.Position(), wp.Location, ac.NmPerLongitude()) < 5 {
							s.deleteAircraft(ac)
							culled = true
						}
						break
					}
				}
			}
			if culled {
				continue
			}

			// Possibly contact the departure controller
			if ac.IsDeparture() && ((ac.DepartureContactAltitude > 0 && ac.Nav.FlightState.Altitude >= ac.DepartureContactAltitude) || (ac.DepartureContactAltitude == 0 && ac.EmergencyState != nil)) {
				fp := ac.NASFlightPlan
				if fp == nil {
					fp = s.STARSComputer.lookupFlightPlanBySquawk(ac.Squawk)
				}
				if fp != nil {
					// During prespawn uncontrolled-only phase, cull departures that would
					// contact a human controller rather than initiating the contact.
					if s.prespawnUncontrolledOnly && !s.isVirtualController(fp.InboundHandoffController) {
						s.deleteAircraft(ac)
						continue
					}

					if !s.prespawn {
						// Time to check in
						// Use the original InboundHandoffController position for the radio event,
						// not the resolved position. This ensures TCWControlsPosition checks
						// correctly match when the user has that position consolidated.
						s.contactDeparture(ac, fp)
					}
				}
			}

			// Cull far-away aircraft
			maxDist := util.Select(s.State.FacilityAdaptation.MaxDistance > 0, s.State.FacilityAdaptation.MaxDistance, 125)
			if math.NMDistance2LL(ac.Position(), s.State.Center) > maxDist {
				s.lg.Debug("culled far-away aircraft", slog.String("adsb_callsign", string(callsign)))
				s.deleteAircraft(ac)
			}

			// Check for delayed "traffic in sight" call
			s.checkDelayedTrafficInSight(ac)
		}

		s.possiblyRequestFlightFollowing()

		s.processPendingFrequencySwitches()
		s.processVirtualControllerContacts()

		s.processFutureEvents()

		// Handle emergencies
		s.updateEmergencies()

		// Check for spacing violations on final approach
		s.checkFinalApproachSpacing()

		s.spawnAircraft()

		s.ERAMComputer.Update(s)
		s.STARSComputer.Update(s)

		// Advance METAR: drop old entries when sim time passes the next one's report time
		for ap, metar := range s.METAR {
			for len(metar) > 1 && s.State.SimTime.After(metar[1].Time) {
				metar = metar[1:]
			}
			s.METAR[ap] = metar
			if len(metar) > 0 {
				if s.State.METAR == nil {
					s.State.METAR = make(map[string]wx.METAR)
				}
				old := s.State.METAR[ap]
				if old.Raw != "" && old.Raw != metar[0].Raw {
					if cur, ok := s.State.ATISLetter[ap]; ok {
						s.State.ATISLetter[ap] = string(rune((cur[0]-'A'+1)%26 + 'A'))
						s.ATISChangedTime[ap] = s.State.SimTime
					}
				}
				s.State.METAR[ap] = metar[0]
			}
		}
	}
}

func (s *Sim) RequestFlightFollowing() error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.requestRandomFlightFollowing()
}

func (s *Sim) TriggerEmergency(name string) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Find the specified emergency type by name
	if idx := slices.IndexFunc(s.State.Emergencies, func(em Emergency) bool { return em.Name == name }); idx == -1 {
		s.lg.Error("triggerEmergency: emergency not found", "name", name)
	} else {
		s.triggerEmergency(idx)
	}
}

func (s *Sim) requestRandomFlightFollowing() error {
	candidates := make(map[*Aircraft]TCP)

	for _, ac := range s.Aircraft {
		if ac.IsAssociated() || ac.FlightPlan.Rules != av.FlightRulesVFR || ac.RequestedFlightFollowing || !ac.IsAirborne() {
			continue
		}
		if ac.Altitude() < ac.DepartureAirportElevation()+500 &&
			math.NMDistance2LL(ac.Position(), ac.DepartureAirportLocation()) < 1 {
			// Barely off the ground at the departure airport.
			continue
		}
		if math.NMDistance2LL(ac.Position(), ac.ArrivalAirportLocation()) < 15 {
			// It's landing soon, so never mind.
			continue
		}
		if ac.WillDoAirwork() {
			// Aircraft doing airwork won't call in for flight following.
			continue
		}

		for tcpStr, cc := range s.State.FacilityAdaptation.ControllerConfigs {
			tcp := s.State.ResolveController(TCP(tcpStr))
			if s.isVirtualController(tcp) {
				continue
			}
			for _, vol := range cc.FlightFollowingAirspace {
				if vol.Inside(ac.Position(), int(ac.Altitude())) {
					candidates[ac] = tcp // first come, first served
					break
				}
			}
		}
	}

	if len(candidates) == 0 {
		return ErrNoVFRAircraftForFlightFollowing
	}

	ac, _ := rand.SampleSeq(s.Rand, maps.Keys(candidates))

	s.requestFlightFollowing(ac, candidates[ac])

	return nil
}

func (s *Sim) possiblyRequestFlightFollowing() {
	if s.prespawn || s.State.SimTime.Before(s.NextVFFRequest) {
		return
	}

	// Attempt to find an aircraft and make a request
	if err := s.requestRandomFlightFollowing(); err != nil {
		// No candidates; back off a bit before trying again
		s.NextVFFRequest = s.State.SimTime.Add(15 * time.Second)
	} else {
		s.NextVFFRequest = s.State.SimTime.Add(randomWait(float32(s.State.LaunchConfig.VFFRequestRate), false, s.Rand))
	}
}

func (s *Sim) requestFlightFollowing(ac *Aircraft, tcp TCP) {
	ac.RequestedFlightFollowing = true
	ac.ControllerFrequency = ControlPosition(tcp)

	// About 90% of the time, make an abbreviated request and wait for "go ahead"
	if s.Rand.Float32() < 0.9 {
		ac.WaitingForGoAhead = true
		s.enqueuePilotTransmission(ac.ADSBCallsign, tcp, PendingTransmissionFlightFollowingReq)
	} else {
		// Full flight following request
		s.enqueuePilotTransmission(ac.ADSBCallsign, tcp, PendingTransmissionFlightFollowingFull)
	}
}

// generateFlightFollowingMessage creates the full flight following request message.
// This is called on-demand to use current aircraft state.
func (s *Sim) generateFlightFollowingMessage(ac *Aircraft) *av.RadioTransmission {
	closestReportingPoint := func(ac *Aircraft) (string, string, float32, bool) {
		var closest *av.VFRReportingPoint
		dist := float32(1000000)
		var center math.Point2LL
		for _, rp := range s.VFRReportingPoints {
			d := math.NMDistance2LL(ac.Position(), rp.Location)
			if d != 0 && d < dist {
				dist = d
				center = rp.Location
				closest = &rp
			}
		}

		if closest != nil {
			// Possibly override with the departure airport, if we're still
			// close to it.  Note that we don't automatically consider the
			// departure airport as a candidate as it may be well outside
			// the TRACON.
			if d := math.NMDistance2LL(ac.Position(), ac.DepartureAirportLocation()); d < dist {
				hdg := math.Heading2LL(ac.DepartureAirportLocation(), ac.Position(), s.State.NmPerLongitude,
					s.State.MagneticVariation)
				return ac.FlightPlan.DepartureAirport, math.Compass(hdg), d, true
			} else {
				hdg := math.Heading2LL(center, ac.Position(), s.State.NmPerLongitude, s.State.MagneticVariation)
				return closest.Description, math.Compass(hdg), dist, false
			}
		}
		return "", "", 0, false
	}

	rt := av.MakeContactTransmission("[we're a|] {actype}, ", ac.FlightPlan.AircraftType)

	rpdesc, rpdir, dist, isap := closestReportingPoint(ac)
	if math.NMDistance2LL(ac.Position(), ac.DepartureAirportLocation()) < 2 {
		rt.Add("departing {airport}, ", ac.FlightPlan.DepartureAirport)
	} else if dist < 1 {
		if isap {
			rt.Add("overhead {airport}, ", rpdesc)
		} else {
			rt.Add("overhead " + rpdesc + ", ")
		}
	} else {
		nm := int(dist + 0.5)
		var loc string
		if nm == 1 {
			loc = "one mile " + rpdir
		} else {
			loc = strconv.Itoa(int(dist+0.5)) + " miles " + rpdir
		}
		if isap {
			rt.Add(loc+" of {airport}, ", rpdesc)
		} else {
			rt.Add(loc + " of " + rpdesc + ", ")
		}
	}

	var alt *av.RadioTransmission
	// Get the aircraft's target altitude from the navigation system
	targetAlt, _ := ac.Nav.TargetAltitude()
	currentAlt := ac.Altitude()

	// Check if we're in a climb or descent (more than 100 feet difference)
	if currentAlt < targetAlt {
		// Report current altitude and target altitude when climbing or descending
		alt = av.MakeContactTransmission("[at|] {alt} for {alt}, ", currentAlt, targetAlt)
	} else {
		// Just report current altitude if we're level
		alt = av.MakeContactTransmission("at {alt}, ", currentAlt)
	}
	earlyAlt := s.Rand.Bool()
	if earlyAlt {
		rt.Merge(alt)
	}

	if s.Rand.Bool() {
		// Heading only sometimes
		rt.Add(math.Compass(ac.Heading()) + "bound, ")
	}

	rt.Add("[looking for flight-following|request flight-following|request radar advisories|request advisories] to {airport}",
		ac.FlightPlan.ArrivalAirport)

	if !earlyAlt {
		rt.Merge(alt)
	}

	rt.Type = av.RadioTransmissionContact
	return rt
}

func (s *Sim) contactDeparture(ac *Aircraft, fp *NASFlightPlan) {
	tcp := fp.InboundHandoffController
	s.lg.Debug("contacting departure controller", slog.String("tcp", string(tcp)))

	// Mark as already contacted so we only send one contact message
	ac.DepartureContactAltitude = -1

	// Queue the contact (may be delayed due to radio activity)
	s.enqueueDepartureContact(ac, tcp)
}

func (s *Sim) isRadarVisible(ac *Aircraft) bool {
	filters := s.State.FacilityAdaptation.Filters
	return !filters.SurfaceTracking.Inside(ac.Position(), int(ac.Altitude()))
}

func (s *Sim) goAround(ac *Aircraft) {
	// Capture approach info before anything clears it.
	approach := ac.Nav.Approach.Assigned
	if approach == nil {
		s.lg.Warn("goAround called without assigned approach",
			slog.String("callsign", string(ac.ADSBCallsign)))
		return
	}
	airport := ac.FlightPlan.ArrivalAirport
	runway := approach.Runway

	proc := s.getGoAroundProcedureForAircraft(ac)
	if proc.HandoffController == "" {
		proc.HandoffController = s.getGoAroundController(ac)
	}

	ac.WentAround = true
	ac.GotContactTower = false
	ac.SpacingGoAroundDeclined = false
	ac.GoAroundOnRunwayHeading = proc.IsRunwayHeading

	altitude := float32(proc.Altitude)

	// Waypoint at the opposite threshold recording who to contact when it's reached.
	wp := av.Waypoint{
		Location:       approach.OppositeThreshold,
		Flags:          av.WaypointFlagFlyOver | av.WaypointFlagHasAltRestriction,
		Heading:        int16(proc.Heading),
		AltRestriction: av.AltitudeRestriction{Range: [2]float32{altitude, altitude}},
		Extra: &av.WaypointExtra{
			GoAroundContactController: proc.HandoffController,
		},
	}

	ac.Nav.GoAroundWithProcedure(altitude, wp)

	holdRunways := append([]string{runway}, proc.HoldDepartures...)
	s.holdDeparturesForGoAround(airport, holdRunways, proc.HandoffController)
}

// getGoAroundController returns the TCP that should handle a go-around for the given aircraft.
// Lookup priority: go_around_assignments for airport/runway, airport, then departure_assignments for airport.
func (s *Sim) getGoAroundController(ac *Aircraft) TCP {
	airport := ac.FlightPlan.ArrivalAirport
	runway := ""
	if ac.Nav.Approach.Assigned != nil {
		runway = ac.Nav.Approach.Assigned.Runway
	}

	// Check go_around_assignments for specific runway
	if runway != "" {
		if tcp, ok := s.GoAroundAssignments[airport+"/"+runway]; ok {
			return tcp
		}
	}

	// Check go_around_assignments for airport
	if tcp, ok := s.GoAroundAssignments[airport]; ok {
		return tcp
	}

	// Fall back to departure_assignments for airport
	if tcp, ok := s.DepartureAssignments[airport]; ok {
		return tcp
	}

	// We shouldn't get here but just in case--current controller
	return TCP(ac.ControllerFrequency)
}

// holdDeparturesForGoAround sets GoAroundHoldUntil on the specified runways and
// posts a status message to the go-around controller.
func (s *Sim) holdDeparturesForGoAround(airport string, holdRunways []string, goAroundTCP TCP) {
	if len(holdRunways) == 0 {
		return
	}

	depState, ok := s.DepartureState[airport]
	if !ok {
		return
	}

	holdUntil := s.State.SimTime.Add(time.Minute)

	// Set the hold state on matching runways
	for rwy, state := range depState {
		rwyBase := rwy.Base()
		for _, holdRwy := range holdRunways {
			if rwyBase == av.RunwayID(holdRwy).Base() {
				state.GoAroundHoldUntil = holdUntil
				s.lg.Info("holding departures on runway due to go-around",
					slog.String("airport", airport), slog.String("runway", rwyBase))
			}
		}
	}

	s.eventStream.Post(Event{
		Type:         StatusMessageEvent,
		ToController: ControlPosition(goAroundTCP),
		WrittenText:  fmt.Sprintf("%s DEPARTURES HELD FOR 1 MINUTE", airport),
	})
}

// getGoAroundProcedureForAircraft returns the go-around procedure defined for the
// aircraft's arrival airport/runway, if one exists in the scenario's arrival_runways.
func (s *Sim) getGoAroundProcedureForAircraft(ac *Aircraft) *GoAroundProcedure {
	airport := ac.FlightPlan.ArrivalAirport
	runway := ac.Nav.Approach.Assigned.Runway

	// Find matching arrival runway with a go-around procedure
	for _, ar := range s.State.ArrivalRunways {
		if ar.Airport == airport && ar.Runway.Base() == runway && ar.GoAround != nil {
			return ar.GoAround
		}
	}

	approach := ac.Nav.Approach.Assigned
	return &GoAroundProcedure{
		Heading:           int(approach.RunwayHeading(s.State.NmPerLongitude, s.State.MagneticVariation) + 0.5),
		IsRunwayHeading:   true,
		Altitude:          1000 * int((ac.Nav.FlightState.ArrivalAirportElevation+2500)/1000),
		HandoffController: s.getGoAroundController(ac),
	}
}

// checkFinalApproachSpacing checks for spacing violations between IFR aircraft
// on the same final approach and triggers go-arounds when separation is insufficient.
func (s *Sim) checkFinalApproachSpacing() {
	type runwayKey struct{ airport, runway string }
	aircraftByRunway := make(map[runwayKey][]*Aircraft)

	// Group IFR aircraft with assigned approaches by airport+runway
	for _, ac := range s.Aircraft {
		// Only tower sends aircraft around; don't include ones that have already been sent around
		// since presumably we'll have vertical separation soon if not already.
		if ac.Nav.Approach.Assigned != nil && ac.GotContactTower && !ac.SentAroundForSpacing {
			key := runwayKey{ac.FlightPlan.ArrivalAirport, ac.Nav.Approach.Assigned.Runway}
			aircraftByRunway[key] = append(aircraftByRunway[key], ac)
		}
	}

	for _, aircraft := range aircraftByRunway {
		// Sort by distance to threshold (closest first)
		threshold := aircraft[0].Nav.Approach.Assigned.Threshold
		slices.SortFunc(aircraft, func(a, b *Aircraft) int {
			return cmp.Compare(math.NMDistance2LL(a.Position(), threshold),
				math.NMDistance2LL(b.Position(), threshold))
		})

		// Check each adjacent pair
		for i := 1; i < len(aircraft); i++ {
			front, trailing := aircraft[i-1], aircraft[i]

			// Get required separation
			vol := trailing.ATPAVolume()
			eligible25nm := vol != nil && vol.Enable25nmApproach &&
				s.State.IsATPAVolume25nmEnabled(vol.Id) &&
				trailing.OnExtendedCenterline(0.2) && front.OnExtendedCenterline(0.2)
			reqSep := av.CWTRequiredApproachSeparation(front.CWT(), trailing.CWT(), eligible25nm)

			actualSep := math.NMDistance2LL(front.Position(), trailing.Position())

			majorBust := actualSep < reqSep*0.8
			minorBust := actualSep < reqSep*0.9

			// >20% violation: always go around
			// >10% but <=20% violation: 50% chance (one-time roll); skip check if already declined
			issueGoAround := majorBust || (minorBust && !trailing.SpacingGoAroundDeclined && s.Rand.Float32() < 0.5)
			if issueGoAround {
				s.goAroundForSpacing(trailing)
			} else if minorBust {
				trailing.SpacingGoAroundDeclined = true
			}
		}
	}
}

// goAroundForSpacing initiates a tower-commanded go-around for spacing violations.
func (s *Sim) goAroundForSpacing(ac *Aircraft) {
	ac.SentAroundForSpacing = true
	s.goAround(ac)
}

// postReadbackTransmission posts a radio event for a pilot responding to a command.
// DestinationTCW is the specific TCW that issued the command.
// Use this for readbacks, where the response must go to the issuing controller
// regardless of any consolidation changes.
func (s *Sim) postReadbackTransmission(from av.ADSBCallsign, tr av.RadioTransmission, tcw TCW) {
	tr.Validate(s.lg)

	if ac, ok := s.Aircraft[from]; ok {
		ac.LastRadioTransmission = s.State.SimTime
	}

	tcp := s.State.PrimaryPositionForTCW(tcw)
	s.eventStream.Post(Event{
		Type:                  RadioTransmissionEvent,
		ADSBCallsign:          from,
		ToController:          tcp,
		DestinationTCW:        tcw,
		WrittenText:           tr.Written(s.Rand),
		SpokenText:            tr.Spoken(s.Rand),
		RadioTransmissionType: tr.Type,
	})
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
			FlightState: ac.NavSummary(s.wxModel, s.State.SimTime, s.lg),
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

func (t *Track) IsAssociated() bool {
	return t.FlightPlan != nil
}

func (t *Track) IsUnassociated() bool {
	return t.FlightPlan == nil
}

func (t *Track) IsUnsupportedDB() bool {
	return t.FlightPlan != nil && !t.FlightPlan.Location.IsZero()
}

func (t *Track) IsDeparture() bool {
	return t.IsAssociated() && t.FlightPlan.TypeOfFlight == av.FlightTypeDeparture
}

func (t *Track) IsArrival() bool {
	return t.IsAssociated() && t.FlightPlan.TypeOfFlight == av.FlightTypeArrival
}

func (t *Track) IsOverflight() bool {
	return t.IsAssociated() && t.FlightPlan.TypeOfFlight == av.FlightTypeOverflight
}

///////////////////////////////////////////////////////////////////////////
// Flight Strips

// allocateStripCID picks a random free CID in 000-999 and marks it used.
func (s *Sim) allocateStripCID() int {
	if len(s.AvailableStripCIDs) > 0 {
		cid := s.AvailableStripCIDs[0]
		s.AvailableStripCIDs = s.AvailableStripCIDs[1:]
		return cid
	}
	return s.Rand.Intn(1000)
}

// freeStripCID releases a CID back to the pool.
func (s *Sim) freeStripCID(cid int) {
	s.AvailableStripCIDs = append(s.AvailableStripCIDs, cid)
}

// initFlightStrip assigns a strip CID and owner on the flight plan.
// No-op if the flight plan already has a strip.
func (s *Sim) initFlightStrip(fp *NASFlightPlan, owner ControlPosition) {
	if fp.StripOwner != "" {
		return
	}
	fp.StripCID = s.allocateStripCID()
	fp.StripOwner = owner
	s.lg.Debug("created flight strip", slog.String("acid", string(fp.ACID)), slog.String("owner", string(owner)))
}

func shouldCreateFlightStrip(fp *NASFlightPlan) bool {
	return fp.Rules == av.FlightRulesIFR || (fp.PlanType != LocalNonEnroute && fp.TypeOfFlight == av.FlightTypeDeparture)
}

// flightStripACIDsForTCW returns the ACIDs of all flight plans with strips
// owned by TCPs controlled by the given TCW. Caller must hold the mutex.
func (s *Sim) flightStripACIDsForTCW(tcw TCW) []ACID {
	var result []ACID
	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && s.State.TCWControlsPosition(tcw, ac.NASFlightPlan.StripOwner) {
			result = append(result, ac.NASFlightPlan.ACID)
		}
	}
	for _, fp := range s.STARSComputer.FlightPlans {
		if s.State.TCWControlsPosition(tcw, fp.StripOwner) {
			result = append(result, fp.ACID)
		}
	}
	return result
}

// PushFlightStrip moves a flight strip to the given TCP.
func (s *Sim) PushFlightStrip(tcw TCW, acid ACID, toTCP TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp, _, _ := s.getFlightPlanForACID(acid)
	if fp == nil || !s.State.TCWControlsPosition(tcw, fp.StripOwner) {
		return ErrNoMatchingFlight
	}

	fp.StripOwner = ControlPosition(toTCP)
	return nil
}

// AnnotateFlightStrip updates the annotations on a flight strip.
func (s *Sim) AnnotateFlightStrip(tcw TCW, acid ACID, annotations [9]string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	fp, _, _ := s.getFlightPlanForACID(acid)
	if fp == nil || !s.State.TCWControlsPosition(tcw, fp.StripOwner) {
		return ErrNoMatchingFlight
	}

	fp.StripAnnotations = annotations
	return nil
}
