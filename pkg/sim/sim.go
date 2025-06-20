// pkg/sim/sim.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/speech"
	"github.com/mmp/vice/pkg/util"

	"github.com/davecgh/go-spew/spew"
)

type humanController struct {
	events              *EventsSubscription
	disableTextToSpeech bool
}

type Sim struct {
	State *State

	mu util.LoggingMutex

	Aircraft map[av.ADSBCallsign]*Aircraft

	SignOnPositions  map[string]*av.Controller
	humanControllers map[string]*humanController

	STARSComputer *STARSComputer
	ERAMComputer  *ERAMComputer

	LocalCodePool *av.LocalSquawkCodePool
	CIDAllocator  *CIDAllocator

	GenerationIndex int // for sequencing StateUpdates

	VFRReportingPoints []av.VFRReportingPoint

	eventStream *EventStream
	lg          *log.Logger

	// Airport -> runway -> state
	DepartureState map[string]map[string]*RunwayLaunchState
	// Key is inbound flow group name
	NextInboundSpawn map[string]time.Time
	NextVFFRequest   time.Time

	Handoffs  map[ACID]Handoff
	PointOuts map[ACID]PointOut

	ReportingPoints []av.ReportingPoint

	FutureControllerContacts []FutureControllerContact
	FutureOnCourse           []FutureOnCourse
	FutureSquawkChanges      []FutureChangeSquawk

	lastSimUpdate  time.Time
	updateTimeSlop time.Duration
	lastUpdateTime time.Time // this is w.r.t. true wallclock time

	prespawn                 bool
	prespawnUncontrolledOnly bool

	NextPushStart time.Time // both w.r.t. sim time
	PushEnd       time.Time

	Instructors map[string]bool

	Rand *rand.Rand

	// No need to serialize these; they're caches anyway.
	bravoAirspace   *av.AirspaceGrid
	charlieAirspace *av.AirspaceGrid

	ttsRequests map[string][]ttsRequest
}

type ttsRequest struct {
	callsign av.ADSBCallsign
	ty       speech.RadioTransmissionType
	text     string
	ch       <-chan []byte
}

type AircraftDisplayState struct {
	Spew        string // for debugging
	FlightState string // for display when paused
}

type Track struct {
	av.RadarTrack

	FlightPlan *STARSFlightPlan

	// Sort of hacky to carry these along here but it's convenient...
	DepartureAirport          string
	DepartureAirportElevation float32
	DepartureAirportLocation  math.Point2LL
	ArrivalAirport            string
	ArrivalAirportElevation   float32
	ArrivalAirportLocation    math.Point2LL
	OnExtendedCenterline      bool
	OnApproach                bool
	ATPAVolume                *av.ATPAVolume
	MVAsApply                 bool
	HoldForRelease            bool
	MissingFlightPlan         bool
	Route                     []math.Point2LL
	IsTentative               bool // first 5 seconds after first contact
}

type DepartureRunway struct {
	Airport     string `json:"airport"`
	Runway      string `json:"runway"`
	Category    string `json:"category,omitempty"`
	DefaultRate int    `json:"rate"`

	ExitRoutes map[string]*av.ExitRoute // copied from airport's  departure_routes
}

type ArrivalRunway struct {
	Airport string `json:"airport"`
	Runway  string `json:"runway"`
}

type Handoff struct {
	AutoAcceptTime    time.Time
	ReceivingFacility string // only for auto accept
}

type PointOut struct {
	FromController string
	ToController   string
	AcceptTime     time.Time
}

type PilotSpeech struct {
	Callsign av.ADSBCallsign
	Type     speech.RadioTransmissionType
	Text     string
	MP3      []byte
}

// NewSimConfiguration collects all of the information required to create a new Sim
type NewSimConfiguration struct {
	TRACON      string
	Description string

	Airports           map[string]*av.Airport
	PrimaryAirport     string
	DepartureRunways   []DepartureRunway
	ArrivalRunways     []ArrivalRunway
	InboundFlows       map[string]*av.InboundFlow
	LaunchConfig       LaunchConfig
	Fixes              map[string]math.Point2LL
	VFRReportingPoints []av.VFRReportingPoint

	ControlPositions   map[string]*av.Controller
	PrimaryController  string
	ControllerAirspace map[string][]string
	VirtualControllers []string
	MultiControllers   av.SplitConfiguration
	SignOnPositions    map[string]*av.Controller

	TFRs                    []av.TFR
	LiveWeather             bool
	Wind                    av.Wind
	STARSFacilityAdaptation STARSFacilityAdaptation
	IsLocal                 bool

	ReportingPoints   []av.ReportingPoint
	MagneticVariation float32
	NmPerLongitude    float32
	Center            math.Point2LL
	Range             float32
	DefaultMaps       []string
	Airspace          av.Airspace
}

func NewSim(config NewSimConfiguration, manifest *VideoMapManifest, lg *log.Logger) *Sim {
	s := &Sim{
		Aircraft: make(map[av.ADSBCallsign]*Aircraft),

		DepartureState:   make(map[string]map[string]*RunwayLaunchState),
		NextInboundSpawn: make(map[string]time.Time),

		SignOnPositions: config.SignOnPositions,

		STARSComputer: makeSTARSComputer(config.TRACON),

		CIDAllocator: NewCIDAllocator(),

		LocalCodePool: av.MakeLocalSquawkCodePool(config.STARSFacilityAdaptation.SSRCodes),

		VFRReportingPoints: config.VFRReportingPoints,

		humanControllers: make(map[string]*humanController),

		eventStream: NewEventStream(lg),
		lg:          lg,

		ReportingPoints: config.ReportingPoints,

		lastUpdateTime: time.Now(),

		Handoffs:  make(map[ACID]Handoff),
		PointOuts: make(map[ACID]PointOut),

		Instructors: make(map[string]bool),

		Rand: rand.Make(),
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

	s.ERAMComputer = makeERAMComputer(av.DB.TRACONs[config.TRACON].ARTCC, s.LocalCodePool)

	s.State = newState(config, manifest, lg)

	s.setInitialSpawnTimes(time.Now()) // FIXME? will be clobbered in prespawn

	return s
}

func (s *Sim) Activate(lg *log.Logger) {
	s.lg = lg

	if s.eventStream == nil {
		s.eventStream = NewEventStream(lg)
	}
	s.humanControllers = make(map[string]*humanController)
	s.State.HumanControllers = nil

	now := time.Now()
	s.lastUpdateTime = now

	if s.Rand == nil {
		s.Rand = rand.Make()
	}

	s.ttsRequests = make(map[string][]ttsRequest)

}

func (s *Sim) GetSerializeSim() Sim {
	ss := *s

	// Clean up so that the user can sign in when they reload.
	for ctrl := range s.humanControllers {
		delete(ss.State.Controllers, ctrl)
	}

	return ss
}

func (s *Sim) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("state", s.State),
		slog.Any("human_controllers", s.humanControllers),
		slog.Any("departure_state", s.DepartureState),
		slog.Any("next_inbound_spawn", s.NextInboundSpawn),
		slog.Any("automatic_handoffs", s.Handoffs),
		slog.Any("automatic_pointouts", s.PointOuts),
		slog.Time("next_push_start", s.NextPushStart),
		slog.Time("push_end", s.PushEnd))
}

func (s *Sim) SignOn(tcp string, instructor bool, disableTextToSpeech bool) (*State, error) {
	s.mu.Lock(s.lg)
	if err := s.signOn(tcp, instructor, disableTextToSpeech); err != nil {
		s.mu.Unlock(s.lg)
		return nil, err
	}
	s.mu.Unlock(s.lg)

	state := s.State.GetStateForController(tcp)
	var update StateUpdate
	s.GetStateUpdate(tcp, &update)
	update.Apply(state, s.eventStream)
	return state, nil
}

func (s *Sim) signOn(tcp string, instructor bool, disableTextToSpeech bool) error {
	if _, ok := s.humanControllers[tcp]; ok {
		return ErrControllerAlreadySignedIn
	}
	if _, ok := s.State.Controllers[tcp]; ok {
		// Trying to sign in to a virtual position.
		return av.ErrInvalidController
	}
	if _, ok := s.SignOnPositions[tcp]; !ok {
		return av.ErrNoController
	}

	s.humanControllers[tcp] = &humanController{
		events:              s.eventStream.Subscribe(),
		disableTextToSpeech: disableTextToSpeech,
	}
	s.State.Controllers[tcp] = s.SignOnPositions[tcp]
	s.State.HumanControllers = append(s.State.HumanControllers, tcp)

	if tcp == s.State.PrimaryController {
		// The primary controller signed in so the sim will resume.
		// Reset lastUpdateTime so that the next time Update() is
		// called for the sim, we don't try to run a ton of steps.
		s.lastUpdateTime = time.Now()
	}

	s.eventStream.Post(Event{
		Type:        StatusMessageEvent,
		WrittenText: tcp + " has signed on.",
	})
	s.lg.Infof("%s: controller signed on", tcp)

	return nil
}

func (s *Sim) SignOff(tcp string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, ok := s.humanControllers[tcp]; !ok {
		return av.ErrNoController
	}

	// Drop track on controlled aircraft
	for _, ac := range s.Aircraft {
		ac.handleControllerDisconnect(tcp, s.State.PrimaryController)
	}

	if tcp == s.State.LaunchConfig.Controller {
		// give up control of launches so someone else can take it.
		s.State.LaunchConfig.Controller = ""
	}

	s.humanControllers[tcp].events.Unsubscribe()

	delete(s.humanControllers, tcp)
	delete(s.State.Controllers, tcp)
	delete(s.Instructors, tcp)
	s.State.HumanControllers =
		slices.DeleteFunc(s.State.HumanControllers, func(s string) bool { return s == tcp })

	s.eventStream.Post(Event{
		Type:        StatusMessageEvent,
		WrittenText: tcp + " has signed off.",
	})
	s.lg.Infof("%s: controller signing off", tcp)

	return nil
}

func (s *Sim) ChangeControlPosition(fromTCP, toTCP string, keepTracks bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.lg.Infof("%s: switching to %s", fromTCP, toTCP)

	// Make sure we can successfully sign on before signing off from the
	// current position. Preserve instructor status from the original controller.
	wasInstructor := s.State.Controllers[fromTCP].Instructor
	wasDisableTTS := s.humanControllers[fromTCP].disableTextToSpeech
	if err := s.signOn(toTCP, wasInstructor, wasDisableTTS); err != nil {
		return err
	}

	// Swap the event subscriptions so we don't lose any events pending on the old one.
	s.humanControllers[toTCP].events.Unsubscribe()
	s.humanControllers[toTCP] = s.humanControllers[fromTCP]
	s.State.HumanControllers = append(s.State.HumanControllers, toTCP)

	delete(s.humanControllers, fromTCP)
	delete(s.State.Controllers, fromTCP)
	delete(s.Instructors, fromTCP)
	s.State.HumanControllers = slices.DeleteFunc(s.State.HumanControllers, func(s string) bool { return s == fromTCP })

	s.eventStream.Post(Event{
		Type:        StatusMessageEvent,
		WrittenText: fromTCP + " has signed off.",
	})

	for _, ac := range s.Aircraft {
		if keepTracks {
			ac.transferTracks(fromTCP, toTCP)
		} else {
			ac.handleControllerDisconnect(fromTCP, s.State.PrimaryController)
		}
	}

	return nil
}

func (s *Sim) TogglePause(tcp string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.State.Paused = !s.State.Paused
	s.lg.Infof("paused: %v", s.State.Paused)
	s.lastUpdateTime = time.Now() // ignore time passage...

	s.eventStream.Post(Event{
		Type:        GlobalMessageEvent,
		WrittenText: tcp + " has " + util.Select(s.State.Paused, "paused", "unpaused") + " the sim",
	})
	return nil
}

func (s *Sim) FastForward(tcp string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for i := 0; i < 15; i++ {
		s.State.SimTime = s.State.SimTime.Add(time.Second)
		s.updateState()
	}
	s.updateTimeSlop = 0
	s.lastUpdateTime = time.Now()

	s.eventStream.Post(Event{
		Type:        GlobalMessageEvent,
		WrittenText: tcp + " has fast-forwarded the sim",
	})
	return nil
}

func (s *Sim) IdleTime() time.Duration {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return time.Since(s.lastUpdateTime)
}

func (s *Sim) SetSimRate(tcp string, rate float32) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.State.SimRate = rate
	s.lg.Infof("sim rate set to %f", s.State.SimRate)
	return nil
}

func (s *Sim) GlobalMessage(tcp, message string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(Event{
		Type:           GlobalMessageEvent,
		WrittenText:    message,
		FromController: tcp,
	})

	return nil
}

func (s *Sim) CreateRestrictionArea(ra av.RestrictionArea) (int, error) {
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

func (s *Sim) PostEvent(e Event) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(e)
}

func (s *Sim) ActiveControllers() []string {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return util.SortedMapKeys(s.humanControllers)
}

func (s *Sim) GetAvailableCoveredPositions() (map[string]av.Controller, map[string]av.Controller) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	available := make(map[string]av.Controller)
	covered := make(map[string]av.Controller)

	for tcp, ctrl := range s.SignOnPositions {
		if _, ok := s.humanControllers[tcp]; ok {
			covered[tcp] = *ctrl
		} else {
			available[tcp] = *ctrl
		}
	}

	return available, covered
}

type GlobalMessage struct {
	Message        string
	FromController string
}

type StateUpdate struct {
	GenerationIndex         int
	Tracks                  map[av.ADSBCallsign]*Track
	UnassociatedFlightPlans []*STARSFlightPlan
	ACFlightPlans           map[av.ADSBCallsign]av.FlightPlan
	ReleaseDepartures       []ReleaseDeparture

	Controllers      map[string]*av.Controller
	HumanControllers []string

	Time time.Time

	LaunchConfig LaunchConfig

	UserRestrictionAreas []av.RestrictionArea

	SimIsPaused          bool
	SimRate              float32
	TotalIFR, TotalVFR   int
	Events               []Event
	QuickFlightPlanIndex int
}

func (s *Sim) GetStateUpdate(tcp string, update *StateUpdate) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var events []Event
	if hc, ok := s.humanControllers[tcp]; ok {
		consolidateRadioTransmissions := func(events []Event) []Event {
			canConsolidate := func(a, b Event) bool {
				return a.Type == RadioTransmissionEvent && b.Type == RadioTransmissionEvent &&
					a.ADSBCallsign == b.ADSBCallsign && a.Type == b.Type && a.ToController == b.ToController
			}
			lastRadio := -1
			var c []Event
			for _, e := range events {
				if lastRadio != -1 && canConsolidate(e, c[lastRadio]) {
					c[lastRadio].WrittenText += ", " + e.WrittenText
					c[lastRadio].SpokenText += ", " + e.SpokenText
					if e.RadioTransmissionType == speech.RadioTransmissionUnexpected {
						c[lastRadio].RadioTransmissionType = speech.RadioTransmissionUnexpected
					}
				} else {
					if e.Type == RadioTransmissionEvent {
						lastRadio = len(c)
					}
					c = append(c, e)
				}
			}
			return c
		}

		events = consolidateRadioTransmissions(hc.events.Get())

		ctrl := s.State.Controllers[tcp]

		// Add identifying info
		for i, e := range events {
			if e.Type != RadioTransmissionEvent || e.ToController != tcp {
				continue
			}

			ac, ok := s.Aircraft[e.ADSBCallsign]
			if !ok {
				fmt.Printf("%s: no ac found for radio transmission?", e.ADSBCallsign)
				continue
			}

			var heavySuper string
			if perf, ok := av.DB.AircraftPerformance[ac.FlightPlan.AircraftType]; ok {
				if perf.WeightClass == "H" {
					heavySuper = " heavy"
				} else if perf.WeightClass == "J" {
					heavySuper = " super"
				}
			}

			if e.RadioTransmissionType == speech.RadioTransmissionContact {
				var tr *speech.RadioTransmission
				if ac.TypeOfFlight == av.FlightTypeDeparture {
					tr = speech.MakeContactTransmission("{dctrl}, {callsign}"+heavySuper+". ", ctrl, ac.ADSBCallsign)
				} else {
					tr = speech.MakeContactTransmission("{actrl}, {callsign}"+heavySuper+". ", ctrl, ac.ADSBCallsign)
				}
				events[i].WrittenText = tr.Written(s.Rand) + e.WrittenText
				events[i].SpokenText = tr.Spoken(s.Rand) + e.SpokenText
			} else {
				tr := speech.MakeReadbackTransmission(", {callsign}"+heavySuper+". ", ac.ADSBCallsign)
				events[i].WrittenText = e.WrittenText + tr.Written(s.Rand)
				events[i].SpokenText = e.SpokenText + tr.Spoken(s.Rand)
			}
		}

		// Post TTS requests
		for _, e := range events {
			if e.Type != RadioTransmissionEvent || e.ToController != tcp {
				continue
			}

			// Skip TTS generation if it's disabled for this controller
			if hc, ok := s.humanControllers[tcp]; ok && hc.disableTextToSpeech {
				continue
			}

			ac, ok := s.Aircraft[e.ADSBCallsign]
			if !ok {
				fmt.Printf("%s: no ac found for radio transmission?", e.ADSBCallsign)
				continue
			}

			if ac.Voice == "" {
				var err error
				if ac.Voice, err = speech.GetRandomVoice(); err != nil {
					s.lg.Errorf("TTS GetRandomVoice: %v", err)
				}
			}

			ch, err := speech.RequestTTS(ac.Voice, e.SpokenText, s.lg)
			if err != nil {
				s.lg.Errorf("TTS: %v", err)
			} else {
				s.ttsRequests[tcp] = append(s.ttsRequests[tcp], ttsRequest{
					callsign: ac.ADSBCallsign,
					ty:       e.RadioTransmissionType,
					text:     e.SpokenText,
					ch:       ch,
				})
			}
		}
	}

	*update = StateUpdate{
		UnassociatedFlightPlans: s.STARSComputer.FlightPlans,

		Controllers:      s.State.Controllers,
		HumanControllers: slices.Collect(maps.Keys(s.humanControllers)),

		Time: s.State.SimTime,

		LaunchConfig: s.State.LaunchConfig,

		UserRestrictionAreas: s.State.UserRestrictionAreas,
		SimIsPaused:          s.State.Paused,
		SimRate:              s.State.SimRate,
		TotalIFR:             s.State.TotalIFR,
		TotalVFR:             s.State.TotalVFR,
		Events:               events,
		QuickFlightPlanIndex: s.State.QuickFlightPlanIndex,
	}

	s.GenerationIndex++
	update.GenerationIndex = s.GenerationIndex

	update.ACFlightPlans = make(map[av.ADSBCallsign]av.FlightPlan)
	for cs, ac := range s.Aircraft {
		update.ACFlightPlans[cs] = ac.FlightPlan
	}

	for _, ac := range s.STARSComputer.HoldForRelease {
		fp, _ := s.GetFlightPlanForACID(ACID(ac.ADSBCallsign))
		if fp == nil {
			s.lg.Warnf("%s: no flight plan for hold for release aircraft", string(ac.ADSBCallsign))
			continue
		}
		update.ReleaseDepartures = append(update.ReleaseDepartures,
			ReleaseDeparture{
				ADSBCallsign:        ac.ADSBCallsign,
				DepartureAirport:    "K" + fp.EntryFix,
				DepartureController: fp.InboundHandoffController,
				Released:            ac.Released,
				Squawk:              ac.Squawk,
				ListIndex:           fp.ListIndex,
				AircraftType:        fp.AircraftType,
				Exit:                fp.ExitFix,
			})
	}

	update.Tracks = make(map[av.ADSBCallsign]*Track)
	for _, callsign := range util.SortedMapKeys(s.Aircraft) {
		ac := s.Aircraft[callsign]
		if !s.isRadarVisible(ac) {
			continue
		}

		rt := Track{
			RadarTrack:                ac.GetRadarTrack(s.State.SimTime),
			FlightPlan:                ac.STARSFlightPlan,
			DepartureAirport:          ac.FlightPlan.DepartureAirport,
			DepartureAirportElevation: ac.DepartureAirportElevation(),
			DepartureAirportLocation:  ac.DepartureAirportLocation(),
			ArrivalAirport:            ac.FlightPlan.ArrivalAirport,
			ArrivalAirportElevation:   ac.ArrivalAirportElevation(),
			ArrivalAirportLocation:    ac.ArrivalAirportLocation(),
			OnExtendedCenterline:      ac.OnExtendedCenterline(0.2),
			OnApproach:                ac.OnApproach(false), /* don't check altitude */
			MVAsApply:                 ac.MVAsApply(),
			HoldForRelease:            ac.HoldForRelease,
			MissingFlightPlan:         ac.MissingFlightPlan,
			ATPAVolume:                ac.ATPAVolume(),
			IsTentative:               s.State.SimTime.Sub(ac.FirstSeen) < 5*time.Second,
		}

		for _, wp := range ac.Nav.Waypoints {
			rt.Route = append(rt.Route, wp.Location)
		}

		update.Tracks[callsign] = &rt
	}

	// Make up fake tracks for unsupported datablocks
	for i, fp := range update.UnassociatedFlightPlans {
		if fp.Location.IsZero() {
			continue
		}
		callsign := av.ADSBCallsign("__" + string(fp.ACID))
		update.Tracks[callsign] = &Track{
			RadarTrack: av.RadarTrack{
				ADSBCallsign: callsign,
				Location:     fp.Location,
			},
			FlightPlan: update.UnassociatedFlightPlans[i],
		}
	}

	// While it seemed that this could be skipped, it's actually necessary
	// to avoid races: while another copy is made as it's marshaled to be
	// returned from RPC call, there may be other updates to the sim state
	// between this function returning and that happening.
	*update = deep.MustCopy(*update)
}

func (su *StateUpdate) Apply(state *State, eventStream *EventStream) {
	// Make sure the generation index is above the current index so that if
	// updates are returned out of order we ignore stale ones.
	if state.GenerationIndex < su.GenerationIndex {
		state.Tracks = su.Tracks
		if su.Controllers != nil {
			state.Controllers = su.Controllers
		}
		state.HumanControllers = su.HumanControllers
		state.ACFlightPlans = su.ACFlightPlans
		state.UnassociatedFlightPlans = su.UnassociatedFlightPlans
		state.ReleaseDepartures = su.ReleaseDepartures
		state.LaunchConfig = su.LaunchConfig

		state.UserRestrictionAreas = su.UserRestrictionAreas

		state.SimTime = su.Time
		state.Paused = su.SimIsPaused
		state.SimRate = su.SimRate
		state.TotalIFR = su.TotalIFR
		state.TotalVFR = su.TotalVFR
		state.QuickFlightPlanIndex = su.QuickFlightPlanIndex

		state.GenerationIndex = su.GenerationIndex
	}

	// Important: do this after updating aircraft, controllers, etc.,
	// so that they reflect any changes the events are flagging.
	for _, e := range su.Events {
		eventStream.Post(e)
	}
}

func (s *Sim) GetControllerSpeech(tcp string) []PilotSpeech {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var speech []PilotSpeech

	s.ttsRequests[tcp] = util.FilterSliceInPlace(s.ttsRequests[tcp], func(req ttsRequest) bool {
		select {
		case mp3, ok := <-req.ch:
			if ok { // not closed
				speech = append(speech, PilotSpeech{
					Callsign: req.callsign,
					Type:     req.ty,
					Text:     req.text,
					MP3:      mp3,
				})
			}
			return false // remove it from the slice
		default:
			return true
		}
	})

	return speech
}

func (s *Sim) isActiveHumanController(tcp string) bool {
	_, ok := s.humanControllers[tcp]
	return ok
}

///////////////////////////////////////////////////////////////////////////
// Simulation

func (s *Sim) Update() {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	startUpdate := time.Now()
	defer func() {
		if d := time.Since(startUpdate); d > 200*time.Millisecond {
			s.lg.Warn("unexpectedly long Sim Update() call", slog.Duration("duration", d),
				slog.Any("sim", s))
		}
	}()

	for _, ac := range s.Aircraft {
		ac.Check(s.lg)
	}

	if s.State.Paused {
		return
	}

	if !s.isActiveHumanController(s.State.PrimaryController) {
		// Pause the sim if the primary controller is gone
		return
	}

	// Figure out how much time has passed since the last update: wallclock
	// time is scaled by the sim rate, then we add in any time from the
	// last update that wasn't accounted for.
	elapsed := time.Since(s.lastUpdateTime)
	elapsed = time.Duration(s.State.SimRate*float32(elapsed)) + s.updateTimeSlop
	// Run the sim for this many seconds
	ns := int(elapsed.Truncate(time.Second).Seconds())
	if ns > 10 {
		s.lg.Warn("unexpected hitch in update rate", slog.Duration("elapsed", elapsed),
			slog.Int("steps", ns), slog.Duration("slop", s.updateTimeSlop))
	}
	for i := 0; i < ns; i++ {
		s.State.SimTime = s.State.SimTime.Add(time.Second)
		s.updateState()
	}
	s.updateTimeSlop = elapsed - elapsed.Truncate(time.Second)

	s.lastUpdateTime = time.Now()
}

// separate so time management can be outside this so we can do the prespawn stuff...
func (s *Sim) updateState() {
	now := s.State.SimTime

	for acid, ho := range s.Handoffs {
		if !now.After(ho.AutoAcceptTime) && !s.prespawn {
			continue
		}

		if fp, _ := s.GetFlightPlanForACID(acid); fp != nil {
			if fp.HandoffTrackController != "" && !s.isActiveHumanController(fp.HandoffTrackController) {
				// Automated accept
				s.eventStream.Post(Event{
					Type:           AcceptedHandoffEvent,
					FromController: fp.TrackingController,
					ToController:   fp.HandoffTrackController,
					ACID:           fp.ACID,
				})
				s.lg.Info("automatic handoff accept", slog.String("acid", string(fp.ACID)),
					slog.String("from", fp.TrackingController),
					slog.String("to", fp.HandoffTrackController))

				fp.TrackingController = fp.HandoffTrackController
				if s.State.IsLocalController(fp.TrackingController) {
					fp.LastLocalController = fp.TrackingController
				}
				fp.HandoffTrackController = ""
			}
		}
		delete(s.Handoffs, acid)
	}

	for acid, po := range s.PointOuts {
		if !now.After(po.AcceptTime) {
			continue
		}

		if fp, _ := s.GetFlightPlanForACID(acid); fp != nil && !s.isActiveHumanController(po.ToController) {
			// Note that "to" and "from" are swapped in the event,
			// since the ack is coming from the "to" controller of the
			// original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: po.ToController,
				ToController:   po.FromController,
				ACID:           acid,
			})
			s.lg.Info("automatic pointout accept", slog.String("acid", string(acid)),
				slog.String("by", po.ToController), slog.String("to", po.FromController))

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

			passedWaypoint := ac.Update(s.State, nil /* s.lg*/)

			if ac.FirstSeen.IsZero() && s.isRadarVisible(ac) {
				ac.FirstSeen = s.State.SimTime
			}

			if passedWaypoint != nil {
				// Handoffs still happen for "unassociated" (to us) tracks
				// when they're currently tracked by an external facility.
				if passedWaypoint.HumanHandoff {
					// Handoff from virtual controller to a human controller.
					sfp := ac.STARSFlightPlan
					if sfp == nil {
						sfp = s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign))
					}
					if sfp != nil {
						s.handoffTrack(sfp, s.State.ResolveController(sfp.InboundHandoffController))
					}
				} else if passedWaypoint.TCPHandoff != "" {
					sfp := ac.STARSFlightPlan
					if sfp == nil {
						sfp = s.STARSComputer.lookupFlightPlanByACID(ACID(ac.ADSBCallsign))
					}
					if sfp != nil {
						s.handoffTrack(sfp, passedWaypoint.TCPHandoff)
					}
				}

				if ac.IsAssociated() {
					// Things that only apply to associated aircraft
					sfp := ac.STARSFlightPlan

					if passedWaypoint.ClearApproach {
						ac.ApproachController = sfp.ControllingController
					}

					if passedWaypoint.TransferComms {
						// We didn't enqueue this before since we knew an
						// explicit comms handoff was coming so go ahead and
						// send them to the controller's frequency. Note that
						// we use InboundHandoffController and not
						// ac.TrackingController, since the human controller
						// may have already flashed the track to a virtual
						// controller.
						ctrl := s.State.ResolveController(sfp.InboundHandoffController)
						// Make sure they've bought the handoff.
						if ctrl != sfp.HandoffTrackController {
							s.enqueueControllerContact(ac.ADSBCallsign, ctrl, 0 /* no delay */)
						}
					}

					// Update scratchpads if the waypoint has scratchpad commands
					// Only update if aircraft is not controlled by a human
					if !s.isActiveHumanController(sfp.ControllingController) {
						if passedWaypoint.PrimaryScratchpad != "" {
							sfp.Scratchpad = passedWaypoint.PrimaryScratchpad
						}
						if passedWaypoint.ClearPrimaryScratchpad {
							sfp.Scratchpad = ""
						}
						if passedWaypoint.SecondaryScratchpad != "" {
							sfp.SecondaryScratchpad = passedWaypoint.SecondaryScratchpad
						}
						if passedWaypoint.ClearSecondaryScratchpad {
							sfp.SecondaryScratchpad = ""
						}
					}

					if passedWaypoint.PointOut != "" {
						if ctrl, ok := s.State.Controllers[passedWaypoint.PointOut]; ok {
							// Don't do the point out if a human is controlling the aircraft.
							if !s.isActiveHumanController(sfp.ControllingController) {
								fromCtrl := s.State.Controllers[sfp.ControllingController]
								s.pointOut(sfp.ACID, fromCtrl, ctrl)
								break
							}
						}
					}
				}

				if passedWaypoint.Delete {
					s.lg.Info("deleting aircraft at waypoint", slog.Any("waypoint", passedWaypoint))
					s.deleteAircraft(ac)
				}

				if passedWaypoint.Land {
					// There should be an altitude restriction at the final approach waypoint, but
					// be careful.
					alt := passedWaypoint.AltitudeRestriction
					// If we're more than 200 feet AGL, go around.
					lowEnough := alt == nil || ac.Altitude() <= alt.TargetAltitude(ac.Altitude())+200
					if lowEnough {
						s.lg.Info("deleting landing at waypoint", slog.Any("waypoint", passedWaypoint))

						// Record the landing if necessary for scheduling departures.
						if depState, ok := s.DepartureState[ac.FlightPlan.ArrivalAirport]; ok {
							var runway string
							if ac.Nav.Approach.Assigned != nil {
								// IFR aircraft with assigned approach
								runway = ac.Nav.Approach.Assigned.Runway
							} else {
								// VFR aircraft - select best runway based on wind
								ap := av.DB.Airports[ac.FlightPlan.ArrivalAirport]
								if rwy, _ := ap.SelectBestRunway(s.State /* wind */, s.State.MagneticVariation); rwy != nil {
									runway = rwy.Id
								}
							}

							if rwyState, ok := depState[runway]; ok {
								rwyState.LastArrivalLandingTime = s.State.SimTime
								rwyState.LastArrivalFlightRules = ac.FlightPlan.Rules
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
					s.lg.Info("randomly going around")
					ac.GoAroundDistance = nil // only go around once
					s.goAround(ac)
				}
			}

			// Possibly contact the departure controller
			if ac.DepartureContactAltitude != 0 && ac.Nav.FlightState.Altitude >= ac.DepartureContactAltitude &&
				!s.prespawn {
				// Time to check in
				fp := ac.STARSFlightPlan
				if fp == nil {
					fp = s.STARSComputer.lookupFlightPlanBySquawk(ac.Squawk)
				}
				if fp != nil {
					tcp := s.State.ResolveController(fp.InboundHandoffController)
					s.lg.Info("contacting departure controller", slog.String("tcp", tcp))

					rt := ac.Nav.DepartureMessage()
					s.postRadioEvent(ac.ADSBCallsign, tcp, *rt)

					// Clear this out so we only send one contact message
					ac.DepartureContactAltitude = 0

					// Only after we're on frequency can the controller start
					// issuing control commands.. (Note that track may have
					// already been handed off to the next controller at this
					// point.)
					fp.ControllingController = tcp
				}
			}

			// Cull far-away aircraft
			if math.NMDistance2LL(ac.Position(), s.State.Center) > 125 {
				s.lg.Info("culled far-away aircraft", slog.String("adsb_callsign", string(callsign)))
				s.deleteAircraft(ac)
			}
		}

		s.possiblyRequestFlightFollowing()

		// Handle assorted deferred radio calls.
		s.processEnqueued()

		s.spawnAircraft()

		s.ERAMComputer.Update(s)
		s.STARSComputer.Update(s)
	}
}

func (s *Sim) RequestFlightFollowing() error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.requestRandomFlightFollowing()
}

func (s *Sim) requestRandomFlightFollowing() error {
	candidates := make(map[*Aircraft]string)

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

		for tcp, cc := range s.State.STARSFacilityAdaptation.ControllerConfigs {
			tcp = s.State.ResolveController(tcp)
			if !s.isActiveHumanController(tcp) {
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

func (s *Sim) requestFlightFollowing(ac *Aircraft, tcp string) {
	ac.RequestedFlightFollowing = true

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

	rt := speech.MakeContactTransmission("[we're a|] {actype}", ac.FlightPlan.AircraftType)

	rpdesc, rpdir, dist, isap := closestReportingPoint(ac)
	if math.NMDistance2LL(ac.Position(), ac.DepartureAirportLocation()) < 2 {
		rt.Add("departing {airport}", ac.FlightPlan.DepartureAirport)
	} else if dist < 1 {
		if isap {
			rt.Add("overhead {airport}", rpdesc)
		} else {
			rt.Add("overhead " + rpdesc)
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
			rt.Add(loc+" of {airport}", rpdesc)
		} else {
			rt.Add(loc + " of " + rpdesc)
		}
	}

	var alt *speech.RadioTransmission
	// Get the aircraft's target altitude from the navigation system
	targetAlt, _ := ac.Nav.TargetAltitude(nil)
	currentAlt := ac.Altitude()

	// Check if we're in a climb or descent (more than 100 feet difference)
	if currentAlt < targetAlt {
		// Report current altitude and target altitude when climbing or descending
		alt = speech.MakeContactTransmission("[at|] {alt} for {alt}", currentAlt, targetAlt)
	} else {
		// Just report current altitude if we're level
		alt = speech.MakeContactTransmission("at {alt}", currentAlt)
	}
	earlyAlt := s.Rand.Bool()
	if earlyAlt {
		rt.Merge(alt)
	}

	if s.Rand.Bool() {
		// Heading only sometimes
		rt.Add(math.Compass(ac.Heading()) + "bound")
	}

	rt.Add("[looking for flight-following|request flight-following|request radar advisories|request advisories] to {airport}",
		ac.FlightPlan.ArrivalAirport)

	if !earlyAlt {
		rt.Merge(alt)
	}

	rt.Type = speech.RadioTransmissionContact

	s.postRadioEvent(ac.ADSBCallsign, tcp, *rt)
}

func (s *Sim) isRadarVisible(ac *Aircraft) bool {
	filters := s.State.STARSFacilityAdaptation.Filters
	return !filters.SurfaceTracking.Inside(ac.Position(), int(ac.Altitude()))
}

func (s *Sim) goAround(ac *Aircraft) {
	if ac.IsUnassociated() { // this shouldn't happen...
		return
	}
	sfp := ac.STARSFlightPlan

	// Update controller before calling GoAround so the
	// transmission goes to the right controller.
	// FIXME: we going to the approach controller is often the wrong thing;
	// we need some more functionality for specifying go around procedures
	// in general.

	towerHadTrack := sfp.TrackingController != "" && sfp.TrackingController != ac.ApproachController

	sfp.ControllingController = s.State.ResolveController(ac.ApproachController)

	rt := ac.GoAround()
	rt.Type = speech.RadioTransmissionUnexpected
	s.postRadioEvent(ac.ADSBCallsign, ac.ApproachController /* FIXME: issue #540 */, *rt)

	// If it was handed off to tower, hand it back to us
	if towerHadTrack {
		sfp.HandoffTrackController = sfp.ControllingController

		s.eventStream.Post(Event{
			Type:           OfferedHandoffEvent,
			ADSBCallsign:   ac.ADSBCallsign,
			FromController: sfp.TrackingController,
			ToController:   ac.ApproachController,
		})
	}
}

func (s *Sim) postRadioEvent(from av.ADSBCallsign, defaultTCP string, tr speech.RadioTransmission) {
	tr.Validate(s.lg)

	if tr.Controller == "" {
		tr.Controller = defaultTCP
	}

	s.eventStream.Post(Event{
		Type:                  RadioTransmissionEvent,
		ADSBCallsign:          from,
		ToController:          tr.Controller,
		WrittenText:           tr.Written(s.Rand),
		SpokenText:            tr.Spoken(s.Rand),
		RadioTransmissionType: tr.Type,
	})
}

func (s *Sim) CallsignForACID(acid ACID) (av.ADSBCallsign, bool) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for cs, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.STARSFlightPlan.ACID == acid {
			return cs, true
		}
	}
	return av.ADSBCallsign(""), false
}

func (s *Sim) GetAircraftDisplayState(callsign av.ADSBCallsign) (AircraftDisplayState, error) {
	if ac, ok := s.Aircraft[callsign]; !ok {
		return AircraftDisplayState{}, ErrNoMatchingFlight
	} else {
		return AircraftDisplayState{
			Spew:        spew.Sdump(ac),
			FlightState: ac.NavSummary(s.lg),
		}, nil
	}
}

// bool indicates whether it's active
func (s *Sim) GetFlightPlanForACID(acid ACID) (*STARSFlightPlan, bool) {
	for _, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.STARSFlightPlan.ACID == acid {
			return ac.STARSFlightPlan, true
		}
	}
	for i, fp := range s.STARSComputer.FlightPlans {
		if fp.ACID == acid {
			return s.STARSComputer.FlightPlans[i], !fp.Location.IsZero()
		}
	}
	return nil, false
}

func IsValidACID(acid string) bool {
	if len(acid) < 3 {
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

func (t *Track) HandingOffTo(tcp string) bool {
	if t.IsUnassociated() {
		return false
	}

	sfp := t.FlightPlan
	return sfp.HandoffTrackController == tcp &&
		(!slices.Contains(sfp.RedirectedHandoff.Redirector, tcp) || // not a redirector
			sfp.RedirectedHandoff.RedirectedTo == tcp) // redirected to
}

func (ac *Aircraft) transferTracks(from, to string) {
	if ac.ApproachController == from {
		ac.ApproachController = to
	}
	if ac.PreArrivalDropController == from {
		ac.PreArrivalDropController = to
	}

	if ac.IsUnassociated() {
		return
	}

	sfp := ac.STARSFlightPlan
	if sfp.HandoffTrackController == from {
		sfp.HandoffTrackController = to
	}
	if sfp.TrackingController == from {
		sfp.TrackingController = to
	}
	if sfp.ControllingController == from {
		sfp.ControllingController = to
	}
}

func (ac *Aircraft) handleControllerDisconnect(callsign string, primaryController string) {
	if callsign == primaryController {
		// Don't change anything; the sim will pause without the primary
		// controller, so we might as well have all of the tracks and
		// inbound handoffs waiting for them when they return.
		return
	}
	if ac.IsUnassociated() {
		return
	}

	sfp := ac.STARSFlightPlan
	if sfp.HandoffTrackController == callsign {
		// Otherwise redirect handoffs to the primary controller. This is
		// not a perfect solution; for an arrival, for example, we should
		// re-resolve it based on the signed-in controllers, as is done in
		// Sim updateState() for arrivals when they are first handed
		// off. We don't have all of that information here, though...
		sfp.HandoffTrackController = primaryController
	}

	if sfp.ControllingController == callsign {
		if sfp.TrackingController == callsign {
			// Drop track of aircraft that we control
			sfp.TrackingController = ""
			sfp.ControllingController = ""
		} else {
			// Another controller has the track but not yet control;
			// just give them control
			sfp.ControllingController = sfp.TrackingController
		}
	}
}

func (ac *Aircraft) IsUnassociated() bool {
	return ac.STARSFlightPlan == nil
}

func (ac *Aircraft) IsAssociated() bool {
	return ac.STARSFlightPlan != nil
}

func (ac *Aircraft) AssociateFlightPlan(fp *STARSFlightPlan) {
	fp.Location = math.Point2LL{} // clear location in case it was an unsupported DB
	ac.STARSFlightPlan = fp
	ac.PreArrivalDropController = ""
}

func (ac *Aircraft) DisassociateFlightPlan() *STARSFlightPlan {
	fp := ac.STARSFlightPlan
	ac.STARSFlightPlan = nil
	return fp
}
