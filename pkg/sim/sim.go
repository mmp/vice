// pkg/sim/sim.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	crand "crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"

	"github.com/brunoga/deep"
)

const initialSimSeconds = 20 * 60
const initialSimControlledSeconds = 45

type Configuration struct {
	ScenarioConfigs  map[string]*SimScenarioConfiguration
	ControlPositions map[string]*av.Controller
	DefaultScenario  string
}

type SimScenarioConfiguration struct {
	SelectedController  string
	SelectedSplit       string
	SplitConfigurations av.SplitConfigurationSet
	PrimaryAirport      string

	Wind         av.Wind
	LaunchConfig LaunchConfig

	DepartureRunways []ScenarioGroupDepartureRunway
	ArrivalRunways   []ScenarioGroupArrivalRunway
}

const ServerSimCallsign = "__SERVER__"

const (
	LaunchAutomatic = iota
	LaunchManual
)

// LaunchConfig collects settings related to launching aircraft in the sim; it's
// passed back and forth between client and server: server provides them so client
// can draw the UI for what's available, then client returns one back when launching.
type LaunchConfig struct {
	// Controller is the controller in charge of the launch settings; if empty then
	// launch control may be taken by any signed in controller.
	Controller string
	// LaunchManual or LaunchAutomatic
	Mode int

	GoAroundRate float32
	// airport -> runway -> category -> rate
	DepartureRates     map[string]map[string]map[string]float32
	DepartureRateScale float32

	VFRDepartureRateScale float32
	VFRAirports           map[string]*av.Airport

	// inbound flow -> airport / "overflights" -> rate
	InboundFlowRates            map[string]map[string]float32
	InboundFlowRateScale        float32
	ArrivalPushes               bool
	ArrivalPushFrequencyMinutes int
	ArrivalPushLengthMinutes    int
}

func MakeLaunchConfig(dep []ScenarioGroupDepartureRunway, vfrRateScale float32, vfrAirports map[string]*av.Airport,
	inbound map[string]map[string]int) LaunchConfig {
	lc := LaunchConfig{
		GoAroundRate:                0.05,
		DepartureRateScale:          1,
		VFRDepartureRateScale:       vfrRateScale,
		VFRAirports:                 vfrAirports,
		InboundFlowRateScale:        1,
		ArrivalPushFrequencyMinutes: 20,
		ArrivalPushLengthMinutes:    10,
	}

	// Walk the departure runways to create the map for departures.
	lc.DepartureRates = make(map[string]map[string]map[string]float32)
	for _, rwy := range dep {
		if _, ok := lc.DepartureRates[rwy.Airport]; !ok {
			lc.DepartureRates[rwy.Airport] = make(map[string]map[string]float32)
		}
		if _, ok := lc.DepartureRates[rwy.Airport][rwy.Runway]; !ok {
			lc.DepartureRates[rwy.Airport][rwy.Runway] = make(map[string]float32)
		}
		lc.DepartureRates[rwy.Airport][rwy.Runway][rwy.Category] = float32(rwy.DefaultRate)
	}

	// Convert the inbound map from int to float32 rates
	lc.InboundFlowRates = make(map[string]map[string]float32)
	for flow, airportOverflights := range inbound {
		lc.InboundFlowRates[flow] = make(map[string]float32)
		for name, rate := range airportOverflights {
			lc.InboundFlowRates[flow][name] = float32(rate)
		}
	}

	return lc
}

type NewSimConfiguration struct {
	TRACONName      string
	TRACON          map[string]*Configuration
	GroupName       string
	Scenario        *SimScenarioConfiguration
	ScenarioName    string
	NewSimName      string // for create remote only
	RequirePassword bool   // for create remote only
	Password        string // for create remote only
	NewSimType      int
	TFRs            []av.TFR

	LiveWeather               bool
	InstructorAllowed         bool
	Instructor                bool
	SelectedRemoteSim         string
	SelectedRemoteSimPosition string
	RemoteSimPassword         string // for join remote only

	DisplayError error
}

const (
	NewSimCreateLocal = iota
	NewSimCreateRemote
	NewSimJoinRemote
)

func MakeNewSimConfiguration() NewSimConfiguration {
	return NewSimConfiguration{NewSimName: rand.AdjectiveNoun()}
}

type Connection struct {
	SimState State
	SimProxy *proxy
}

///////////////////////////////////////////////////////////////////////////
// Sim

type Sim struct {
	Name string

	mu util.LoggingMutex

	ScenarioGroup string
	Scenario      string

	State *State

	controllers     map[string]*ServerController // from token
	SignOnPositions map[string]*av.Controller

	eventStream *EventStream
	lg          *log.Logger
	mapManifest *av.VideoMapManifest

	LaunchConfig LaunchConfig

	// For each airport, at what time we would like to launch a departure,
	// based on the airport's departure rate. The actual time an aircraft
	// is launched may be later, e.g. if we need longer for wake turbulence
	// separation, etc.
	NextDepartureLaunch map[string]time.Time
	// Map from airport to aircraft that are ready to go. The slice is
	// ordered according to the departure sequence.
	DeparturePool map[string][]DepartureAircraft
	// Index to track departing aircraft; we use this to make sure we don't
	// keep pushing an aircraft to the end of the queue.
	DepartureIndex map[string]int
	// Airport -> runway -> *DepartureAircraft (nil if none launched yet)
	LastDeparture map[string]map[string]*DepartureAircraft

	VFRLaunchAttempts  map[string]int
	VFRLaunchSuccesses map[string]int

	// Key is inbound flow group name
	NextInboundSpawn map[string]time.Time

	Handoffs map[string]Handoff
	// a/c callsign -> PointOut
	PointOuts map[string]PointOut

	TotalDepartures  int
	TotalArrivals    int
	TotalOverflights int

	ReportingPoints []av.ReportingPoint

	FutureControllerContacts []FutureControllerContact
	FutureOnCourse           []FutureOnCourse
	FutureSquawkChanges      []FutureChangeSquawk

	RequirePassword bool
	Password        string

	lastSimUpdate time.Time

	SimTime        time.Time // this is our fake time--accounting for pauses & simRate..
	updateTimeSlop time.Duration

	lastUpdateTime time.Time // this is w.r.t. true wallclock time
	lastLogTime    time.Time
	SimRate        float32
	Paused         bool

	prespawnUncontrolled bool
	prespawnControlled   bool

	NextPushStart time.Time // both w.r.t. sim time
	PushEnd       time.Time

	InstructorAllowed bool
	Instructors       map[string]bool

	// No need to serialize these; they're caches anyway.
	bravoAirspace   *av.AirspaceGrid
	charlieAirspace *av.AirspaceGrid
}

// DepartureAircraft represents a departing aircraft, either still on the
// ground or recently-launched.
type DepartureAircraft struct {
	Callsign         string
	Runway           string
	AddedToList      bool
	ReleaseRequested bool
	ReleaseDelay     time.Duration // minimum wait after release before the takeoff roll
	Index            int
	MinSeparation    time.Duration // How long after takeoff it will be at ~6000' and airborne
	LaunchTime       time.Time
}

type Handoff struct {
	Time              time.Time
	ReceivingFacility string // only for auto accept
}

type PointOut struct {
	FromController string
	ToController   string
	AcceptTime     time.Time
}

type ServerController struct {
	Id                  string
	lastUpdateCall      time.Time
	warnedNoUpdateCalls bool
	events              *EventsSubscription
}

func (sc *ServerController) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", sc.Id),
		slog.Time("last_update", sc.lastUpdateCall),
		slog.Bool("warned_no_update", sc.warnedNoUpdateCalls))
}

func NewSim(ssc NewSimConfiguration, scenarioGroups map[string]map[string]*ScenarioGroup, isLocal bool,
	manifests map[string]*av.VideoMapManifest, lg *log.Logger) *Sim {
	lg = lg.With(slog.String("sim_name", ssc.NewSimName))

	tracon, ok := scenarioGroups[ssc.TRACONName]
	if !ok {
		lg.Errorf("%s: unknown TRACON", ssc.TRACONName)
		return nil
	}
	sg, ok := tracon[ssc.GroupName]
	if !ok {
		lg.Errorf("%s: unknown scenario group", ssc.GroupName)
		return nil
	}
	sc, ok := sg.Scenarios[ssc.ScenarioName]
	if !ok {
		lg.Errorf("%s: unknown scenario", ssc.ScenarioName)
		return nil
	}

	s := &Sim{
		ScenarioGroup: ssc.GroupName,
		Scenario:      ssc.ScenarioName,
		LaunchConfig:  ssc.Scenario.LaunchConfig,

		DeparturePool:      make(map[string][]DepartureAircraft),
		DepartureIndex:     make(map[string]int),
		LastDeparture:      make(map[string]map[string]*DepartureAircraft),
		VFRLaunchAttempts:  make(map[string]int),
		VFRLaunchSuccesses: make(map[string]int),

		controllers: make(map[string]*ServerController),

		eventStream: NewEventStream(lg),
		lg:          lg,
		mapManifest: manifests[sg.STARSFacilityAdaptation.VideoMapFile],

		ReportingPoints: sg.ReportingPoints,

		Password:        ssc.Password,
		RequirePassword: ssc.RequirePassword,

		SimTime:        time.Now(),
		lastUpdateTime: time.Now(),

		SimRate:   1,
		Handoffs:  make(map[string]Handoff),
		PointOuts: make(map[string]PointOut),

		InstructorAllowed: ssc.InstructorAllowed,
		Instructors:       make(map[string]bool),
	}

	if !isLocal {
		s.Name = ssc.NewSimName
	}

	if s.LaunchConfig.ArrivalPushes {
		// Figure out when the next arrival push will start
		m := 1 + rand.Intn(s.LaunchConfig.ArrivalPushFrequencyMinutes)
		s.NextPushStart = time.Now().Add(time.Duration(m) * time.Minute)
	}

	s.SignOnPositions = make(map[string]*av.Controller)
	add := func(callsign string) {
		if ctrl, ok := sg.ControlPositions[callsign]; !ok {
			lg.Errorf("%s: control position unknown??!", callsign)
		} else {
			ctrlCopy := *ctrl
			ctrlCopy.IsHuman = true
			s.SignOnPositions[callsign] = &ctrlCopy
		}
	}
	if !isLocal {
		configs, err := sc.SplitConfigurations.GetConfiguration(ssc.Scenario.SelectedSplit)
		if err != nil {
			lg.Errorf("unable to get configurations for split: %v", err)
		}
		for callsign := range configs {
			add(callsign)
		}
	} else {
		add(sc.SoloController)
	}

	s.State = newState(ssc.Scenario.SelectedSplit, ssc.LiveWeather, isLocal, s, sg, sc, s.mapManifest,
		ssc.TFRs, lg)

	s.setInitialSpawnTimes()

	return s
}

func (s *Sim) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", s.Name),
		slog.String("scenario_group", s.ScenarioGroup),
		slog.String("scenario", s.Scenario),
		slog.Any("controllers", s.State.Controllers),
		slog.Any("launch_config", s.LaunchConfig),
		slog.Any("next_departure_launch", s.NextDepartureLaunch),
		slog.Any("available_departures", s.DeparturePool),
		slog.Any("next_inbound_spawn", s.NextInboundSpawn),
		slog.Any("automatic_handoffs", s.Handoffs),
		slog.Any("automatic_pointouts", s.PointOuts),
		slog.Int("departures", s.TotalDepartures),
		slog.Int("arrivals", s.TotalArrivals),
		slog.Int("overflights", s.TotalOverflights),
		slog.Time("sim_time", s.SimTime),
		slog.Float64("sim_rate", float64(s.SimRate)),
		slog.Bool("paused", s.Paused),
		slog.Time("next_push_start", s.NextPushStart),
		slog.Time("push_end", s.PushEnd),
		slog.Any("aircraft", s.State.Aircraft))
}

func (s *Sim) SignOn(id string, instructor bool) (*State, string, error) {
	if err := s.signOn(id, instructor); err != nil {
		return nil, "", err
	}

	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return nil, "", err
	}
	token := base64.StdEncoding.EncodeToString(buf[:])

	s.controllers[token] = &ServerController{
		Id:             id,
		lastUpdateCall: time.Now(),
		events:         s.eventStream.Subscribe(),
	}

	return s.State.GetStateForController(id), token, nil
}

func (s *Sim) signOn(id string, instructor bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if id != "Observer" {
		if s.controllerIsSignedIn(id) {
			return ErrControllerAlreadySignedIn
		}

		ctrl, ok := s.SignOnPositions[id]
		if !ok {
			return av.ErrNoController
		}
		// Make a copy of the *Controller and set the sign on time.
		sctrl := *ctrl
		sctrl.SignOnTime = time.Now()
		s.State.Controllers[id] = &sctrl

		if id == s.State.PrimaryController {
			// The primary controller signed in so the sim will resume.
			// Reset lastUpdateTime so that the next time Update() is
			// called for the sim, we don't try to run a ton of steps.
			s.lastUpdateTime = time.Now()
		}
		if instructor {
			s.Instructors[id] = true
		}
	}

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: id + " has signed on.",
	})
	s.lg.Infof("%s: controller signed on", id)

	return nil
}

func (s *Sim) SignOff(token string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		// Drop track on controlled aircraft
		for _, ac := range s.State.Aircraft {
			ac.HandleControllerDisconnect(ctrl.Id, s.State.PrimaryController)
		}

		if ctrl.Id == s.LaunchConfig.Controller {
			// give up control of launches so someone else can take it.
			s.LaunchConfig.Controller = ""
		}

		ctrl.events.Unsubscribe()
		delete(s.controllers, token)
		delete(s.State.Controllers, ctrl.Id)
		delete(s.Instructors, ctrl.Id)

		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: ctrl.Id + " has signed off.",
		})
		s.lg.Infof("%s: controller signing off", ctrl.Id)
	}
	return nil
}

func (s *Sim) ChangeControlPosition(token string, id string, keepTracks bool) error {
	ctrl, ok := s.controllers[token]
	if !ok {
		return ErrInvalidControllerToken
	}
	oldId := ctrl.Id

	s.lg.Infof("%s: switching to %s", oldId, id)

	// Make sure we can successfully sign on before signing off from the
	// current position.
	if err := s.signOn(id, false); err != nil {
		return err
	}
	ctrl.Id = id

	delete(s.State.Controllers, oldId)

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: oldId + " has signed off.",
	})

	for _, ac := range s.State.Aircraft {
		if keepTracks {
			ac.TransferTracks(oldId, ctrl.Id)
		} else {
			ac.HandleControllerDisconnect(oldId, s.State.PrimaryController)
		}
	}

	return nil
}

func (s *Sim) TogglePause(token string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.Paused = !s.Paused
		s.lg.Infof("paused: %v", s.Paused)
		s.lastUpdateTime = time.Now() // ignore time passage...

		s.eventStream.Post(Event{
			Type:    GlobalMessageEvent,
			Message: ctrl.Id + " has " + util.Select(s.Paused, "paused", "unpaused") + " the sim",
		})
		return nil
	}
}

func (s *Sim) PostEvent(e Event) {
	s.eventStream.Post(e)
}

type GlobalMessage struct {
	Message        string
	FromController string
}

type WorldUpdate struct {
	Aircraft    map[string]*av.Aircraft
	Controllers map[string]*av.Controller
	Time        time.Time

	ERAMComputers *ERAMComputers

	LaunchConfig LaunchConfig

	UserRestrictionAreas []RestrictionArea

	SimIsPaused      bool
	SimRate          float32
	Events           []Event
	TotalDepartures  int
	TotalArrivals    int
	TotalOverflights int
	Instructors      map[string]bool
}

func (s *Sim) GetWorldUpdate(token string, update *WorldUpdate) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		ctrl.lastUpdateCall = time.Now()
		if ctrl.warnedNoUpdateCalls {
			ctrl.warnedNoUpdateCalls = false
			s.lg.Warnf("%s: connection re-established", ctrl.Id)
			s.eventStream.Post(Event{
				Type:    StatusMessageEvent,
				Message: ctrl.Id + " is back online.",
			})
		}

		var err error
		*update, err = deep.Copy(WorldUpdate{
			Aircraft:             s.State.Aircraft,
			Controllers:          s.State.Controllers,
			ERAMComputers:        s.State.ERAMComputers,
			Time:                 s.SimTime,
			LaunchConfig:         s.LaunchConfig,
			SimIsPaused:          s.Paused,
			SimRate:              s.SimRate,
			Events:               ctrl.events.Get(),
			TotalDepartures:      s.TotalDepartures,
			TotalArrivals:        s.TotalArrivals,
			TotalOverflights:     s.TotalOverflights,
			UserRestrictionAreas: s.State.UserRestrictionAreas,
			Instructors:          s.Instructors,
		})

		return err
	}
}

func (s *Sim) Activate(lg *log.Logger) {
	if s.Name == "" {
		s.lg = lg
	} else {
		s.lg = lg.With(slog.String("sim_name", s.Name))
	}

	if s.controllers == nil {
		s.controllers = make(map[string]*ServerController)
	}
	if s.eventStream == nil {
		s.eventStream = NewEventStream(lg)
	}

	now := time.Now()
	s.lastUpdateTime = now

	s.State.Activate(s.lg)
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

	for _, ac := range s.State.Aircraft {
		ac.Check(s.lg)
	}

	if s.Name != "" {
		// Sign off controllers we haven't heard from in 15 seconds so that
		// someone else can take their place. We only make this check for
		// multi-controller sims; we don't want to do this for local sims
		// so that we don't kick people off e.g. when their computer
		// sleeps.
		for token, ctrl := range s.controllers {
			if time.Since(ctrl.lastUpdateCall) > 5*time.Second {
				if !ctrl.warnedNoUpdateCalls {
					ctrl.warnedNoUpdateCalls = true
					s.lg.Warnf("%s: no messages for 5 seconds", ctrl.Id)
					s.eventStream.Post(Event{
						Type:    StatusMessageEvent,
						Message: ctrl.Id + " has not been heard from for 5 seconds. Connection lost?",
					})
				}

				if time.Since(ctrl.lastUpdateCall) > 15*time.Second {
					s.lg.Warnf("%s: signing off idle controller", ctrl.Id)
					s.mu.Unlock(s.lg)
					s.SignOff(token)
					s.mu.Lock(s.lg)
				}
			}
		}
	}

	if s.Paused {
		return
	}

	if !s.controllerIsSignedIn(s.State.PrimaryController) {
		// Pause the sim if the primary controller is gone
		return
	}

	// Figure out how much time has passed since the last update: wallclock
	// time is scaled by the sim rate, then we add in any time from the
	// last update that wasn't accounted for.
	elapsed := time.Since(s.lastUpdateTime)
	elapsed = time.Duration(s.SimRate*float32(elapsed)) + s.updateTimeSlop
	// Run the sim for this many seconds
	ns := int(elapsed.Truncate(time.Second).Seconds())
	if ns > 10 {
		s.lg.Warn("unexpected hitch in update rate", slog.Duration("elapsed", elapsed),
			slog.Int("steps", ns), slog.Duration("slop", s.updateTimeSlop))
	}
	for i := 0; i < ns; i++ {
		s.SimTime = s.SimTime.Add(time.Second)
		s.updateState()
	}
	s.updateTimeSlop = elapsed - elapsed.Truncate(time.Second)
	s.State.SimTime = s.SimTime

	s.lastUpdateTime = time.Now()

	// Log the current state of everything once a minute
	if time.Since(s.lastLogTime) > time.Minute {
		s.lastLogTime = time.Now()
		s.lg.Info("sim", slog.Any("state", s))
	}
}

// separate so time management can be outside this so we can do the prespawn stuff...
func (s *Sim) updateState() {
	now := s.SimTime

	for callsign, ho := range s.Handoffs {
		if !now.After(ho.Time) {
			continue
		}

		if ac, ok := s.State.Aircraft[callsign]; ok && ac.HandoffTrackController != "" &&
			ac.HandoffTrackController != s.State.PrimaryController && // don't accept handoffs during prespawn
			!s.controllerIsSignedIn(ac.HandoffTrackController) {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.TrackingController,
				ToController:   ac.HandoffTrackController,
				Callsign:       ac.Callsign,
			})
			s.lg.Info("automatic handoff accept", slog.String("callsign", ac.Callsign),
				slog.String("from", ac.TrackingController),
				slog.String("to", ac.HandoffTrackController))

			_, receivingSTARS, err := s.State.ERAMComputers.FacilityComputers(ho.ReceivingFacility)
			if err != nil {
				//s.lg.Errorf("%s: FacilityComputers(): %v", ho.ReceivingFacility, err)
			} else if err := s.State.STARSComputer().AutomatedAcceptHandoff(ac, ac.HandoffTrackController,
				receivingSTARS, s.State.Controllers, s.SimTime); err != nil {
				//s.lg.Errorf("AutomatedAcceptHandoff: %v", err)
			}

			ac.TrackingController = ac.HandoffTrackController
			ac.HandoffTrackController = ""
		}
		delete(s.Handoffs, callsign)
	}

	for callsign, po := range s.PointOuts {
		if !now.After(po.AcceptTime) {
			continue
		}

		if ac, ok := s.State.Aircraft[callsign]; ok && !s.controllerIsSignedIn(po.ToController) {
			// Note that "to" and "from" are swapped in the event,
			// since the ack is coming from the "to" controller of the
			// original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: po.ToController,
				ToController:   po.FromController,
				Callsign:       ac.Callsign,
			})
			s.lg.Info("automatic pointout accept", slog.String("callsign", ac.Callsign),
				slog.String("by", po.ToController), slog.String("to", po.FromController))

			delete(s.PointOuts, callsign)
		}
	}

	// Update the simulation state once a second.
	if now.Sub(s.lastSimUpdate) >= time.Second {
		s.lastSimUpdate = now
		for callsign, ac := range s.State.Aircraft {
			if ac.HoldForRelease && !ac.Released {
				// nvm...
				continue
			}
			if ac.WaitingForLaunch {
				continue
			}

			passedWaypoint := ac.Update(s.State, s.lg)
			if passedWaypoint != nil {
				if passedWaypoint.HumanHandoff {
					// Handoff from virtual controller to a human controller.
					s.handoffTrack(ac.TrackingController, s.ResolveController(ac.WaypointHandoffController),
						ac.Callsign)
				} else if passedWaypoint.TCPHandoff != "" {
					s.handoffTrack(ac.TrackingController, passedWaypoint.TCPHandoff, ac.Callsign)
				}

				if passedWaypoint.PointOut != "" {
					for _, ctrl := range s.State.Controllers {
						// Look for a controller with a matching TCP id.
						if ctrl.Id() == passedWaypoint.PointOut {
							// Don't do the point out if a human is
							// controlling the aircraft.
							if fromCtrl, ok := s.State.Controllers[ac.ControllingController]; ok && !fromCtrl.IsHuman {
								s.pointOut(ac.Callsign, fromCtrl, ctrl)
								break
							}
						}
					}
				}

				if passedWaypoint.ClearApproach {
					ac.ClearedApproach(ac.AssignedApproach(), s.lg) // ignore readback
				}

				if passedWaypoint.Delete {
					s.lg.Info("deleting aircraft at waypoint", slog.Any("waypoint", passedWaypoint))
					s.State.DeleteAircraft(ac)
				}

				if passedWaypoint.Land {
					// There should be an altitude restriction at the final approach waypoint, but
					// be careful.
					alt := passedWaypoint.AltitudeRestriction
					// If we're more than 150 feet AGL, go around.
					lowEnough := alt == nil || ac.Altitude() <= alt.TargetAltitude(ac.Altitude())+150
					if lowEnough {
						s.lg.Info("deleting landing at waypoint", slog.Any("waypoint", passedWaypoint))
						s.State.DeleteAircraft(ac)
					} else {
						s.goAround(ac)
					}
				}
			}

			// Possibly go around
			// FIXME: maintain GoAroundDistance, state, in Sim, not Aircraft
			if ac.GoAroundDistance != nil {
				if d, err := ac.DistanceToEndOfApproach(); err == nil && d < *ac.GoAroundDistance {
					s.lg.Info("randomly going around")
					ac.GoAroundDistance = nil // only go around once
					s.goAround(ac)
				}
			}

			// Possibly contact the departure controller
			if ac.DepartureContactAltitude != 0 && ac.Nav.FlightState.Altitude >= ac.DepartureContactAltitude &&
				!s.prespawnUncontrolled && !s.prespawnControlled {
				// Time to check in
				ctrl := s.ResolveController(ac.DepartureContactController)
				s.lg.Info("contacting departure controller", slog.String("callsign", ctrl))

				airportName := ac.FlightPlan.DepartureAirport
				if ap, ok := s.State.Airports[airportName]; ok && ap.Name != "" {
					airportName = ap.Name
				}

				msg := "departing " + airportName + ", " + ac.Nav.DepartureMessage()
				PostRadioEvents(ac.Callsign, []av.RadioTransmission{av.RadioTransmission{
					Controller: ctrl,
					Message:    msg,
					Type:       av.RadioTransmissionContact,
				}}, s)

				// Clear this out so we only send one contact message
				ac.DepartureContactAltitude = 0

				// Only after we're on frequency can the controller start
				// issuing control commands.. (Note that track may have
				// already been handed off to the next controller at this
				// point.)
				ac.ControllingController = ctrl
			}

			// Cull far-away aircraft
			if math.NMDistance2LL(ac.Position(), s.State.Center) > 250 {
				s.lg.Info("culled far-away aircraft", slog.String("callsign", callsign))
				s.State.DeleteAircraft(ac)
			}
		}
	}

	// Handle assorted deferred radio calls.
	s.processEnqueued()

	// Don't spawn automatically if someone is spawning manually.
	if s.LaunchConfig.Mode == LaunchAutomatic {
		s.spawnAircraft()
	}

	s.State.ERAMComputers.Update(s)
}

func (s *Sim) goAround(ac *av.Aircraft) {
	// Update controller before calling GoAround so the
	// transmission goes to the right controller.
	ac.ControllingController = s.State.DepartureController(ac, s.lg)
	rt := ac.GoAround()
	PostRadioEvents(ac.Callsign, rt, s)

	// If it was handed off to tower, hand it back to us
	if ac.TrackingController != "" && ac.TrackingController != ac.ApproachController {
		ac.HandoffTrackController = s.State.DepartureController(ac, s.lg)
		if ac.HandoffTrackController == "" {
			ac.HandoffTrackController = ac.ApproachController
		}
		s.PostEvent(Event{
			Type:           OfferedHandoffEvent,
			Callsign:       ac.Callsign,
			FromController: ac.TrackingController,
			ToController:   ac.ApproachController,
		})
	}
}

func PostRadioEvents(from string, transmissions []av.RadioTransmission, ep EventPoster) {
	for _, rt := range transmissions {
		ep.PostEvent(Event{
			Type:                  RadioTransmissionEvent,
			Callsign:              from,
			ToController:          rt.Controller,
			Message:               rt.Message,
			RadioTransmissionType: rt.Type,
		})
	}
}

func (s *Sim) ResolveController(callsign string) string {
	if s.State.MultiControllers == nil {
		// Single controller
		return s.State.PrimaryController
	} else if len(s.controllers) == 0 {
		// This can happen during the prespawn phase right after launching but
		// before the user has been signed in.
		return s.State.PrimaryController
	} else {
		c, err := s.State.MultiControllers.ResolveController(callsign,
			func(callsign string) bool {
				return s.controllerIsSignedIn(callsign)
			})
		if err != nil {
			s.lg.Errorf("%s: unable to resolve controller: %v", callsign, err)
		}

		if c == "" { // This shouldn't happen...
			return s.State.PrimaryController
		}
		return c
	}
}

func (s *Sim) IdleTime() time.Duration {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return time.Since(s.lastUpdateTime)
}

func (s *Sim) controllerIsSignedIn(id string) bool {
	for _, ctrl := range s.controllers {
		if ctrl.Id == id {
			return true
		}
	}
	return false
}

func (s *Sim) prespawn() {
	s.lg.Info("starting aircraft prespawn")

	// Prime the pump before the user gets involved
	t := time.Now().Add(-(initialSimSeconds + 1) * time.Second)
	s.prespawnUncontrolled = true
	for i := 0; i < initialSimSeconds; i++ {
		// Controlled only at the tail end.
		s.prespawnControlled = i+initialSimControlledSeconds > initialSimSeconds

		s.SimTime = t
		s.lastUpdateTime = t
		t = t.Add(1 * time.Second)

		s.updateState()
	}
	s.prespawnUncontrolled, s.prespawnControlled = false, false

	s.SimTime = time.Now()
	s.State.SimTime = s.SimTime
	s.lastUpdateTime = time.Now()

	s.lg.Info("finished aircraft prespawn")
}

///////////////////////////////////////////////////////////////////////////
// Spawning aircraft

func (s *Sim) setInitialSpawnTimes() {
	// Randomize next spawn time for departures and arrivals; may be before
	// or after the current time.
	randomDelay := func(rate float32) time.Time {
		if rate == 0 {
			return time.Now().Add(365 * 24 * time.Hour)
		}
		avgWait := int(3600 / rate)
		delta := rand.Intn(avgWait) - avgWait/2 - initialSimSeconds
		return time.Now().Add(time.Duration(delta) * time.Second)
	}

	s.NextInboundSpawn = make(map[string]time.Time)
	for group, rates := range s.LaunchConfig.InboundFlowRates {
		var rateSum float32
		for _, rate := range rates {
			rate = scaleRate(rate, s.LaunchConfig.InboundFlowRateScale)
			rateSum += rate
		}
		s.NextInboundSpawn[group] = randomDelay(rateSum)
	}

	s.NextDepartureLaunch = make(map[string]time.Time)
	for name, ap := range s.State.DepartureAirports {
		r := scaleRate(float32(ap.VFRRateSum()), s.LaunchConfig.VFRDepartureRateScale)
		if runwayRates, ok := s.LaunchConfig.DepartureRates[name]; ok {
			r += sumRateMap2(runwayRates, s.LaunchConfig.DepartureRateScale)
		}
		s.NextDepartureLaunch[name] = randomDelay(r)
	}
}

func scaleRate(rate, scale float32) float32 {
	rate *= scale
	if rate <= 0.5 {
		// Since we round to the nearest int when displaying rates in the UI,
		// we don't want to ever launch for ones that have rate 0.
		return 0
	}
	return rate
}

func sumRateMap2(rates map[string]map[string]float32, scale float32) float32 {
	var sum float32
	for _, categoryRates := range rates {
		for _, rate := range categoryRates {
			sum += scaleRate(rate, scale)
		}
	}
	return sum
}

// sampleRateMap randomly samples elements from a map of some type T to a
// rate with probability proportional to the element's rate.
func sampleRateMap[T comparable](rates map[T]float32, scale float32) (T, float32) {
	var rateSum float32
	var result T
	for item, rate := range rates {
		rate = scaleRate(rate, scale)
		rateSum += rate
		// Weighted reservoir sampling...
		if rateSum == 0 || rand.Float32() < rate/rateSum {
			result = item
		}
	}
	return result, rateSum
}
func sampleRateMap2(rates map[string]map[string]float32, scale float32) (string, string, float32) {
	// Choose randomly in proportion to the rates in the map
	var rateSum float32
	var result0, result1 string
	for item0, rateMap := range rates {
		for item1, rate := range rateMap {
			rate = scaleRate(rate, scale)
			if rate == 0 {
				continue
			}
			rateSum += rate
			// Weighted reservoir sampling...
			if rand.Float32() < rate/rateSum {
				result0 = item0
				result1 = item1
			}
		}
	}
	return result0, result1, rateSum
}

func randomWait(rate float32, pushActive bool) time.Duration {
	if rate == 0 {
		return 365 * 24 * time.Hour
	}
	if pushActive {
		rate = rate * 3 / 2
	}

	avgSeconds := 3600 / rate
	seconds := math.Lerp(rand.Float32(), .85*avgSeconds, 1.15*avgSeconds)
	return time.Duration(seconds * float32(time.Second))
}

func (s *Sim) spawnAircraft() {
	s.spawnArrivalsAndOverflights()
	s.spawnDepartures()
}

func (s *Sim) isControlled(ac *av.Aircraft, departure bool) bool {
	if ac.FlightPlan.Rules == av.VFR {
		// No VFR flights are controlled, so it's easy for them.
		return false
	} else {
		// Otherwise we have to dig around a bit and see if a human is initially or will be involved.
		if departure && ac.DepartureContactController != "" {
			return true
		}
		return slices.ContainsFunc(ac.Nav.Waypoints, func(wp av.Waypoint) bool { return wp.HumanHandoff })
	}
}

func (s *Sim) spawnArrivalsAndOverflights() {
	now := s.SimTime

	if !s.NextPushStart.IsZero() && now.After(s.NextPushStart) {
		// party time
		s.PushEnd = now.Add(time.Duration(s.LaunchConfig.ArrivalPushLengthMinutes) * time.Minute)
		s.lg.Info("arrival push starting", slog.Time("end_time", s.PushEnd))
		s.NextPushStart = time.Time{}
	}
	if !s.PushEnd.IsZero() && now.After(s.PushEnd) {
		// end push
		m := -2 + rand.Intn(4) + s.LaunchConfig.ArrivalPushFrequencyMinutes
		s.NextPushStart = now.Add(time.Duration(m) * time.Minute)
		s.lg.Info("arrival push ending", slog.Time("next_start", s.NextPushStart))
		s.PushEnd = time.Time{}
	}

	pushActive := now.Before(s.PushEnd)

	for group, rates := range s.LaunchConfig.InboundFlowRates {
		if now.After(s.NextInboundSpawn[group]) {
			flow, rateSum := sampleRateMap(rates, s.LaunchConfig.InboundFlowRateScale)

			var ac *av.Aircraft
			var err error
			if flow == "overflights" {
				ac, err = s.createOverflightNoLock(group)
			} else {
				ac, err = s.createArrivalNoLock(group, flow)
			}

			if err != nil {
				s.lg.Errorf("create inbound error: %v", err)
			} else if ac != nil {
				if s.prespawnUncontrolled && !s.prespawnControlled && s.isControlled(ac, false) {
					s.lg.Infof("%s: discarding arrival/overflight\n", ac.Callsign)
					s.State.DeleteAircraft(ac)
				} else {
					s.addAircraftNoLock(*ac)
				}
				s.NextInboundSpawn[group] = now.Add(randomWait(rateSum, pushActive))
			}
		}
	}
}

func (s *Sim) spawnDepartures() {
	now := s.SimTime

	for airport, _ := range s.NextDepartureLaunch {
		// Make sure we have a few departing aircraft to work with.
		s.refreshDeparturePool(airport)

		// Add hold for release to the list a bit before departure time and
		// request release a few minutes early as well.
		pool := s.DeparturePool[airport]
		nlist, nrel := 0, 0
		for i, dep := range rand.PermuteSlice(pool, rand.Uint32()) {
			ac := s.State.Aircraft[dep.Callsign]
			if !ac.HoldForRelease {
				continue
			}

			if !dep.AddedToList && nlist < 5 {
				pool[i].AddedToList = true
				s.State.STARSComputer().AddHeldDeparture(ac)
			}
			nlist++

			if !dep.ReleaseRequested && nrel < 3 {
				pool[i].ReleaseRequested = true
				pool[i].ReleaseDelay = time.Duration(20+rand.Intn(100)) * time.Second
			}
			nrel++
		}

		// See if we have anything to launch
		if !now.After(s.NextDepartureLaunch[airport]) || len(pool) == 0 {
			// Don't bother going any further: wait to match the desired
			// overall launch rate.
			continue
		}

		for i, dep := range pool {
			if !s.canLaunch(airport, dep) {
				continue
			}

			ac := s.State.Aircraft[dep.Callsign]
			if i > 0 && ac.FlightPlan.Rules == av.IFR {
				// We can still launch VFRs if we have IFRs waiting for
				// release but don't want to launch a released IFR if an
				// earlier IFR in the sequence hasn't been released yet.
				continue
			}

			if s.prespawnUncontrolled && !s.prespawnControlled && s.isControlled(ac, true) {
				s.lg.Infof("%s: discarding departure\n", ac.Callsign)
				s.State.DeleteAircraft(ac)
			} else {
				// Launch!
				ac.WaitingForLaunch = false

				// Record the launch so we have it when we consider launching the
				// next one.
				dep.LaunchTime = now
				if s.LastDeparture[airport] == nil {
					s.LastDeparture[airport] = make(map[string]*DepartureAircraft)
				}
				s.LastDeparture[airport][dep.Runway] = &dep
			}

			// Remove it from the pool of waiting departures.
			s.DeparturePool[airport] = s.DeparturePool[airport][1:]

			// And figure out when we want to ask for the next departure.
			ap := s.State.DepartureAirports[airport]
			r := scaleRate(float32(ap.VFRRateSum()), s.LaunchConfig.VFRDepartureRateScale)
			if rates, ok := s.LaunchConfig.DepartureRates[airport]; ok {
				r += sumRateMap2(rates, s.LaunchConfig.DepartureRateScale)
			}
			s.NextDepartureLaunch[airport] = now.Add(randomWait(r, false))
		}
	}
}

// canLaunch checks whether we can go ahead and launch dep.
func (s *Sim) canLaunch(airport string, dep DepartureAircraft) bool {
	ac := s.State.Aircraft[dep.Callsign]

	// If it's hold for release make sure both that it has been released
	// and that a sufficient delay has passed since the release was issued.
	if ac.HoldForRelease {
		if !ac.Released {
			return false
		} else if s.State.SimTime.Sub(ac.ReleaseTime) < dep.ReleaseDelay {
			return false
		}
	}

	prevDep := s.LastDeparture[airport][dep.Runway]
	if prevDep == nil {
		// No previous departure on this runway, so there's nothing
		// stopping us.
		return true
	}

	// Make sure enough time has passed since the last departure.
	elapsed := s.SimTime.Sub(prevDep.LaunchTime)
	return elapsed > s.launchInterval(*prevDep, dep)
}

// launchInterval returns the amount of time we must wait before launching
// cur, if prev was the last aircraft launched.
func (s *Sim) launchInterval(prev, cur DepartureAircraft) time.Duration {
	cac := s.State.Aircraft[cur.Callsign]
	pac, ok := s.State.Aircraft[prev.Callsign]
	if !ok {
		// Previous was presumably deleted
		return 0
	}

	// FIXME: for now we assume we can launch on different runways
	// independently.
	if prev.Runway != cur.Runway {
		return 0
	}

	// Check for wake turbulence separation.
	wtDist := av.CWTDirectlyBehindSeparation(pac.CWT(), cac.CWT())
	if wtDist != 0 {
		// Assume '1 gives you 3.5'
		return time.Duration(wtDist / 3.5 * float32(time.Minute))
	}

	// Assume this will be less than wake turbulence
	return prev.MinSeparation
}

func (s *Sim) refreshDeparturePool(airport string) {
	pool := s.DeparturePool[airport]
	// Keep a pool of 2-5 around.
	if len(pool) >= 2 {
		return
	}

	for range 3 {
		// Figure out which category to generate.
		ap := s.State.DepartureAirports[airport]
		vfrRate := scaleRate(float32(ap.VFRRateSum()), s.LaunchConfig.VFRDepartureRateScale)
		ifrRate := float32(0)
		rates, ok := s.LaunchConfig.DepartureRates[airport]
		if ok {
			ifrRate = sumRateMap2(rates, s.LaunchConfig.DepartureRateScale)
		}
		if ifrRate == 0 && vfrRate == 0 {
			// The airport currently has a 0 departure rate.
			return
		}

		var ac *av.Aircraft
		var err error
		var runway string
		if vfrRate > 0 && rand.Float32() < vfrRate/(vfrRate+ifrRate) {
			// Don't waste time trying to find a valid launch if it's been
			// near-impossible to find valid routes.
			if s.VFRLaunchAttempts[airport] < 400 ||
				(s.VFRLaunchSuccesses[airport] > 0 &&
					s.VFRLaunchAttempts[airport]/s.VFRLaunchSuccesses[airport] < 200) {
				// Add a VFR
				ac, runway, err = s.createVFRDeparture(airport)
			}
		} else if ifrRate > 0 {
			// Add an IFR
			var category string
			var rateSum float32
			runway, category, rateSum = sampleRateMap2(rates, s.LaunchConfig.DepartureRateScale)
			if rateSum > 0 {
				ac, err = s.createDepartureNoLock(airport, runway, category)
			}
		}

		if err == nil && ac != nil {
			ac.WaitingForLaunch = true
			s.addAircraftNoLock(*ac)

			pool = append(pool, makeDepartureAircraft(ac, runway, s.DepartureIndex[airport],
				s.State, s.lg))
			s.DepartureIndex[airport]++
		}
	}

	// We've updated the pool; resequence them.
	s.DeparturePool[airport] = s.sequenceDepartures(s.LastDeparture[airport], pool,
		s.DepartureIndex[airport])
}

func makeDepartureAircraft(ac *av.Aircraft, runway string, idx int, wind av.WindModel, lg *log.Logger) DepartureAircraft {
	d := DepartureAircraft{
		Callsign: ac.Callsign,
		Runway:   runway,
		Index:    idx,
	}

	// Simulate out the takeoff roll and initial climb to figure out when
	// we'll have sufficient separation to launch the next aircraft.
	simAc := *ac
	start := ac.Position()
	d.MinSeparation = 120 * time.Second // just in case
	for i := range 120 {
		simAc.Update(wind, lg)
		// We need 6,000' and airborne, but we'll add a bit of slop
		if simAc.IsAirborne() && math.NMDistance2LL(start, simAc.Position()) > 7500*math.FeetToNauticalMiles {
			d.MinSeparation = time.Duration(i) * time.Second
			break
		}
	}

	return d
}

func (s *Sim) sequenceDepartures(prev map[string]*DepartureAircraft, dep []DepartureAircraft, seq int) []DepartureAircraft {
	// If the oldest one has been hanging around and not launched,
	// eventually force it; this way we don't keep kicking the can down the
	// road on a super indefinitely...
	minIdx := 1000000
	minIdxCallsign := ""
	for _, d := range dep {
		if d.Index < minIdx && seq-d.Index >= 7 {
			minIdx = d.Index
			minIdxCallsign = d.Callsign
		}
	}

	var bestOrder []DepartureAircraft
	bestDuration := 24 * time.Hour

	for depPerm := range util.AllPermutations(dep) {
		// Manifest the permutation into a slice so we can keep the best one.
		var perm []DepartureAircraft
		for _, dep := range depPerm {
			perm = append(perm, dep)
		}

		// If we have decided that an aircraft that has been waiting is
		// going to go first, make sure it is so in this permutation. (We
		// could do this more elegantly...)
		if minIdxCallsign != "" && perm[0].Callsign != minIdxCallsign {
			continue
		}

		// Figure out how long it would take to launch them in this order.
		var d time.Duration
		p := prev[perm[0].Runway]
		for i := range perm {
			c := &perm[i]
			if p != nil {
				d += s.launchInterval(*p, *c)
			}
			p = c
		}

		if d < bestDuration {
			bestDuration = d
			bestOrder = perm
		}
	}
	return bestOrder
}

func (s *Sim) createVFRDeparture(depart string) (*av.Aircraft, string, error) {
	ap := s.State.DepartureAirports[depart]

	// Sample among the randoms and the routes
	rateSum := 0
	var sampledRandoms *av.VFRRandomsSpec
	var sampledRoute *av.VFRRouteSpec
	if ap.VFR.Randoms.Rate > 0 {
		rateSum = ap.VFR.Randoms.Rate
		sampledRandoms = &ap.VFR.Randoms
	}
	for _, route := range ap.VFR.Routes {
		if route.Rate > 0 {
			rateSum += route.Rate
			p := float32(route.Rate) / float32(rateSum)
			if rand.Float32() < p {
				sampledRandoms = nil
				sampledRoute = &route
			}
		}
	}

	for range 5 {
		s.VFRLaunchAttempts[depart]++

		var ac *av.Aircraft
		var runway string
		var err error
		if sampledRandoms != nil {
			// Sample destination airport: may be where we started from.
			arrive, ok := rand.SampleWeightedSeq(maps.Keys(s.State.DepartureAirports),
				func(ap string) int { return s.State.DepartureAirports[ap].VFRRateSum() })
			if !ok {
				fmt.Printf("%s: unable to sample destination airport???\n", depart)
				continue
			}
			ac, runway, err = s.createUncontrolledVFRDeparture(depart, arrive, sampledRandoms.Fleet, nil)
		} else if sampledRoute != nil {
			ac, runway, err = s.createUncontrolledVFRDeparture(depart, sampledRoute.Destination, sampledRoute.Fleet,
				sampledRoute.Waypoints)
		}

		if err == nil && ac != nil {
			s.VFRLaunchSuccesses[depart]++
			return ac, runway, nil
		}
	}

	return nil, "", ErrViolatedAirspace
}

func (s *Sim) createUncontrolledVFRDeparture(depart, arrive, fleet string, routeWps []av.Waypoint) (*av.Aircraft, string, error) {
	depap, arrap := av.DB.Airports[depart], av.DB.Airports[arrive]
	rwy, opp := depap.SelectBestRunway(s.State /* wind */, s.State.MagneticVariation)

	ac, acType := s.State.sampleAircraft(av.AirlineSpecifier{ICAO: "N", Fleet: fleet}, s.lg)
	if ac == nil {
		return nil, "", fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.VFR
	ac.Squawk = 0o1200
	if r := rand.Float32(); r < .02 {
		ac.Mode = av.On // mode-A
	} else if r < .03 {
		ac.Mode = av.Standby // flat out off
	}
	ac.FlightPlan = ac.NewFlightPlan(rules, acType, depart, arrive)

	dist := math.NMDistance2LL(depap.Location, arrap.Location)

	base := math.Max(depap.Elevation, arrap.Elevation)
	base = 1000 + 1000*(base/1000) // round to 1000s.
	var alt int
	randalt := func(n int) int { return base + (1+rand.Intn(n))*1000 }
	if dist == 0 {
		// returning to same airport
		alt = randalt(4)
	} else if dist < 25 {
		// short hop
		alt = randalt(4)
	} else if dist < 50 {
		alt = randalt(8)
	} else {
		alt = randalt(16)
	}
	alt = math.Min(alt, 17000)
	alt = math.Min(alt, int(av.DB.AircraftPerformance[acType].Ceiling))
	alt += 500

	mid := math.Mid2f(depap.Location, arrap.Location)
	if arrive == depart {
		dist := float32(10 + rand.Intn(20))
		hdg := float32(1 + rand.Intn(360))
		v := [2]float32{dist * math.Sin(math.Radians(hdg)), dist * math.Cos(math.Radians(hdg))}
		dnm := math.LL2NM(depap.Location, s.State.NmPerLongitude)
		midnm := math.Add2f(dnm, v)
		mid = math.NM2LL(midnm, s.State.NmPerLongitude)
	}

	var wps []av.Waypoint
	wps = append(wps, av.Waypoint{Fix: "_dep_threshold", Location: rwy.Threshold})
	wps = append(wps, av.Waypoint{Fix: "_opp", Location: opp.Threshold})

	rg := av.MakeRouteGenerator(rwy.Threshold, opp.Threshold, s.State.NmPerLongitude)
	wp0 := rg.Waypoint("_dep_climb", 3, 0)
	wp0.FlyOver = true
	wps = append(wps, wp0)

	// Fly a downwind if needed
	var hdg float32
	if len(routeWps) > 0 {
		hdg = math.Heading2LL(opp.Threshold, routeWps[0].Location, s.State.NmPerLongitude, s.State.MagneticVariation)
	} else {
		hdg = math.Heading2LL(opp.Threshold, mid, s.State.NmPerLongitude, s.State.MagneticVariation)
	}
	turn := math.HeadingSignedTurn(rwy.Heading, hdg)
	if turn < -120 {
		// left downwind
		wps = append(wps, rg.Waypoint("_dep_downwind1", 1, 1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind2", 0, 1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind3", -2, 1.5))
	} else if turn > 120 {
		// right downwind
		wps = append(wps, rg.Waypoint("_dep_downwind1", 1, -1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind2", 0, -1.5))
		wps = append(wps, rg.Waypoint("_dep_downwind3", -2, -1.5))
	}

	var randomizeAltitudeRange bool
	if len(routeWps) > 0 {
		wps = append(wps, routeWps...)
		randomizeAltitudeRange = true
	} else {
		randomizeAltitudeRange = false
		depEnd := wps[len(wps)-1].Location

		radius := .15 * dist
		airwork := func() bool {
			if depart == arrive {
				return rand.Intn(3) == 0
			}
			return rand.Intn(10) == 0
		}()

		const nsteps = 10
		for i := 1; i < nsteps; i++ { // skip first one
			t := (float32(i) + 0.5) / nsteps
			pt := func() math.Point2LL {
				if i <= nsteps/2 {
					return math.Lerp2f(2*t, depEnd, mid)
				} else {
					return math.Lerp2f(2*t-1, mid, arrap.Location)
				}
			}()

			// At or below so that they descend for the last one
			ar := &av.AltitudeRestriction{Range: [2]float32{float32(alt), float32(alt)}}
			if i == nsteps-1 {
				ar = &av.AltitudeRestriction{
					Range: [2]float32{float32(arrap.Elevation) + 1500, float32(arrap.Elevation) + 2000}}
			} else if i > nsteps/2 {
				ar.Range[0] = 0 // at or below
			}

			wps = append(wps, av.Waypoint{
				Fix:                 "_route" + strconv.Itoa(i),
				Location:            pt,
				AltitudeRestriction: ar,
				Radius:              util.Select(i <= 1, 0.2*radius, radius),
			})

			if airwork && i == nsteps/2 {
				wps[len(wps)-1].AirworkRadius = 4 + rand.Intn(4)
				wps[len(wps)-1].AirworkMinutes = 5 + rand.Intn(15)
				wps[len(wps)-1].AltitudeRestriction.Range[0] -= 500
				wps[len(wps)-1].AltitudeRestriction.Range[1] += 2000
			}
		}
	}

	wps[len(wps)-1].Land = true

	if err := ac.InitializeVFRDeparture(s.State.Airports[depart], wps, alt, randomizeAltitudeRange,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.State /* wind */, s.lg); err != nil {
		return nil, "", err
	}

	if s.bravoAirspace == nil || s.charlieAirspace == nil {
		s.initializeAirspaceGrids()
	}

	// Check airspace violations
	simac := deep.MustCopy(*ac)
	for range 3 * 60 * 60 { // limit to 3 hours of sim time, just in case
		if wp := simac.Update(s.State /* wind */, nil); wp != nil && wp.Delete {
			return ac, rwy.Id, nil
		}
		if s.bravoAirspace.Inside(simac.Position(), int(simac.Altitude())) ||
			s.charlieAirspace.Inside(simac.Position(), int(simac.Altitude())) {
			return nil, "", ErrViolatedAirspace
		}
	}

	s.lg.Infof("%s: %s/%s aircraft not finished after 3 hours of sim time",
		ac.Callsign, depart, arrive)
	return nil, "", ErrVFRSimTookTooLong
}

func (s *Sim) initializeAirspaceGrids() {
	initAirspace := func(a map[string][]av.AirspaceVolume) *av.AirspaceGrid {
		var vols []*av.AirspaceVolume
		for volslice := range maps.Values(a) {
			for _, v := range volslice {
				vols = append(vols, &v)
			}
		}
		return av.MakeAirspaceGrid(vols)
	}
	s.bravoAirspace = initAirspace(av.DB.BravoAirspace)
	s.charlieAirspace = initAirspace(av.DB.CharlieAirspace)
}

///////////////////////////////////////////////////////////////////////////
// Commands from the user

func (s *Sim) SetSimRate(token string, rate float32) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		s.SimRate = rate
		s.lg.Infof("sim rate set to %f", s.SimRate)
		return nil
	}
}

func (s *Sim) SetLaunchConfig(token string, lc LaunchConfig) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else {
		// Update the next spawn time for any rates that changed.
		for ap, rwyRates := range lc.DepartureRates {
			var newSum, oldSum float32
			for rwy, categoryRates := range rwyRates {
				for category, rate := range categoryRates {
					newSum += rate
					oldSum += s.LaunchConfig.DepartureRates[ap][rwy][category]
				}
			}
			newSum = scaleRate(newSum, lc.DepartureRateScale) +
				scaleRate(float32(s.State.Airports[ap].VFRRateSum()), lc.VFRDepartureRateScale)
			oldSum = scaleRate(oldSum, s.LaunchConfig.DepartureRateScale) +
				scaleRate(float32(s.State.Airports[ap].VFRRateSum()), s.LaunchConfig.VFRDepartureRateScale)

			if newSum != oldSum {
				s.lg.Infof("%s: departure rate changed %f -> %f", ap, oldSum, newSum)
				s.NextDepartureLaunch[ap] = s.SimTime.Add(randomWait(newSum, false))
			}
		}
		if lc.VFRDepartureRateScale != s.LaunchConfig.VFRDepartureRateScale {
			for name, ap := range lc.VFRAirports {
				r := scaleRate(float32(ap.VFRRateSum()), lc.VFRDepartureRateScale)
				s.NextDepartureLaunch[name] = s.SimTime.Add(randomWait(r, false))
			}
		}
		for group, groupRates := range lc.InboundFlowRates {
			var newSum, oldSum float32
			for ap, rate := range groupRates {
				newSum += rate
				oldSum += s.LaunchConfig.InboundFlowRates[group][ap]
			}
			newSum *= lc.InboundFlowRateScale
			oldSum *= s.LaunchConfig.InboundFlowRateScale

			if newSum != oldSum {
				pushActive := s.SimTime.Before(s.PushEnd)
				s.lg.Infof("%s: inbound flow rate changed %f -> %f", group, oldSum, newSum)
				s.NextInboundSpawn[group] = s.SimTime.Add(randomWait(newSum, pushActive))
			}
		}

		s.LaunchConfig = lc
		return nil
	}
}

func (s *Sim) TakeOrReturnLaunchControl(token string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if ctrl, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if lctrl := s.LaunchConfig.Controller; lctrl != "" && ctrl.Id != lctrl {
		return ErrNotLaunchController
	} else if lctrl == "" {
		s.LaunchConfig.Controller = ctrl.Id
		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: ctrl.Id + " is now controlling aircraft launches.",
		})
		s.lg.Infof("%s: now controlling launches", ctrl.Id)
		return nil
	} else {
		s.eventStream.Post(Event{
			Type:    StatusMessageEvent,
			Message: s.LaunchConfig.Controller + " is no longer controlling aircraft launches.",
		})
		s.lg.Infof("%s: no longer controlling launches", ctrl.Id)
		s.LaunchConfig.Controller = ""
		return nil
	}
}

func (s *Sim) LaunchAircraft(ac av.Aircraft) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// Ignore hold for release; this should only be called for manual launches.
	ac.HoldForRelease = false
	s.addAircraftNoLock(ac)
}

// Assumes the lock is already held (as is the case e.g. for automatic spawning...)
func (s *Sim) addAircraftNoLock(ac av.Aircraft) {
	if _, ok := s.State.Aircraft[ac.Callsign]; ok {
		s.lg.Warn("already have an aircraft with that callsign!", slog.String("callsign", ac.Callsign))
		return
	}

	s.State.Aircraft[ac.Callsign] = &ac

	ac.Nav.Check(s.lg)

	if s.State.IsIntraFacility(&ac) {
		s.TotalDepartures++
		s.TotalArrivals++
		s.lg.Info("launched intrafacility", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	} else if s.State.IsDeparture(&ac) {
		s.TotalDepartures++
		s.lg.Info("launched departure", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	} else if s.State.IsArrival(&ac) {
		s.TotalArrivals++
		s.lg.Info("launched arrival", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	} else {
		s.TotalOverflights++
		s.lg.Info("launched overflight", slog.String("callsign", ac.Callsign), slog.Any("aircraft", ac))
	}
}

func (s *Sim) dispatchCommand(token string, callsign string,
	check func(c *av.Controller, ac *av.Aircraft) error,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	if sc, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ac, ok := s.State.Aircraft[callsign]; !ok {
		return av.ErrNoAircraftForCallsign
	} else {
		// TODO(mtrokel): this needs to be updated for the STARS tracking stuff
		if sc.Id == "Observer" {
			return av.ErrOtherControllerHasTrack
		}

		ctrl := s.State.Controllers[sc.Id]
		if ctrl == nil {
			s.lg.Error("controller unknown", slog.String("controller", sc.Id),
				slog.Any("world_controllers", s.State.Controllers))
			return av.ErrNoController
		}

		if err := check(ctrl, ac); err != nil {
			return err
		} else {
			preAc := *ac
			radioTransmissions := cmd(ctrl, ac)

			s.lg.Info("dispatch_command", slog.String("callsign", ac.Callsign),
				slog.Any("prepost_aircraft", []av.Aircraft{preAc, *ac}),
				slog.Any("radio_transmissions", radioTransmissions))
			PostRadioEvents(ac.Callsign, radioTransmissions, s)
			return nil
		}
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Sim) dispatchControllingCommand(token string, callsign string,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			// TODO(mtrokel): this needs to be updated for the STARS tracking stuff
			if ac.ControllingController != ctrl.Id() && !s.Instructors[ctrl.Id()] {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd)
}

// Commands that are allowed by tracking controller only.
func (s *Sim) dispatchTrackingCommand(token string, callsign string,
	cmd func(*av.Controller, *av.Aircraft) []av.RadioTransmission) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Id() && !s.Instructors[ctrl.Id()] {
				return av.ErrOtherControllerHasTrack
			}

			/*
				trk := s.State.STARSComputer().TrackInformation[ac.Callsign]
				if trk != nil || trk.TrackOwner != ctrl.Callsign {
					return av.ErrOtherControllerHasTrack
				}
			*/

			return nil
		},
		cmd)
}

func (s *Sim) GlobalMessage(global GlobalMessageArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(Event{
		Type:           GlobalMessageEvent,
		Message:        global.Message,
		FromController: global.FromController,
	})

	return nil
}

func (s *Sim) SetScratchpad(token, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// FIXME: both for now
			ac.Scratchpad = scratchpad

			err := s.State.ERAMComputers.SetScratchpad(ac.Callsign, ctrl.Facility, scratchpad)
			if err != nil {
				//s.lg.Errorf("%s/%s: SetScratchPad %s: %v", ac.Callsign, ctrl.Facility, scratchpad, err)
			}
			return nil
		})
}

func (s *Sim) SetSecondaryScratchpad(token, callsign, scratchpad string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// FIXME: both for now
			ac.SecondaryScratchpad = scratchpad

			err := s.State.ERAMComputers.SetSecondaryScratchpad(ac.Callsign, ctrl.Facility, scratchpad)
			if err != nil {
				//s.lg.Errorf("%s/%s: SetSecondaryScratchPad %s: %v", ac.Callsign, ctrl.Facility, scratchpad, err)
			}
			return nil
		})
}

func (s *Sim) ChangeSquawk(token, callsign string, sq av.Squawk) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.enqueueTransponderChange(ac.Callsign, sq, ac.Mode)

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: ctrl.Id(),
				Message:    "squawk " + sq.String(),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) ChangeTransponderMode(token, callsign string, mode av.TransponderMode) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.enqueueTransponderChange(ac.Callsign, ac.Squawk, mode)

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: ctrl.Id(),
				Message:    "squawk " + strings.ToLower(mode.String()),
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) Ident(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:     IdentEvent,
				Callsign: ac.Callsign,
			})

			return []av.RadioTransmission{av.RadioTransmission{
				Controller: ctrl.Id(),
				Message:    "ident",
				Type:       av.RadioTransmissionReadback,
			}}
		})
}

func (s *Sim) SetGlobalLeaderLine(token, callsign string, dir *math.CardinalOrdinalDirection) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *av.Controller, ac *av.Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != c.Id() {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.GlobalLeaderLineDirection = dir
			s.eventStream.Post(Event{
				Type:                SetGlobalLeaderLineEvent,
				Callsign:            ac.Callsign,
				FromController:      callsign,
				LeaderLineDirection: dir,
			})
			return nil
		})
}

func (s *Sim) AutoAssociateFP(token, callsign string, fp *STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			_, _, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			return err
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			_, stars, _ := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			stars.AutoAssociateFP(ac, fp)
			return nil
		})

}

func (s *Sim) CreateUnsupportedTrack(token, callsign string, ut *UnsupportedTrack) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			_, _, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			return err
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			_, stars, _ := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
			stars.AddUnsupportedTrack(*ut)
			return nil
		})
}

func (s *Sim) UploadFlightPlan(token string, Type int, plan *STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	ctrl := s.State.Controllers[s.controllers[token].Id]
	if ctrl == nil {
		s.lg.Errorf("%s: controller unknown", s.controllers[token].Id)
		return ErrUnknownController
	}
	eram, stars, err := s.State.ERAMComputers.FacilityComputers(ctrl.Facility)
	if err != nil {
		return err

	}

	switch Type {
	case LocalNonEnroute:
		stars.AddFlightPlan(plan)
	case LocalEnroute, RemoteEnroute:
		eram.AddFlightPlan(plan)
	}

	return nil
}

func (s *Sim) InitiateTrack(token, callsign string, fp *STARSFlightPlan) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(c *av.Controller, ac *av.Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != "" {
				return av.ErrOtherControllerHasTrack
			}
			if ac.Squawk == 0o1200 {
				return av.ErrNoFlightPlan
			}
			/*
				if s.State.STARSComputer().TrackInformation[ac.Callsign] != nil {
					return av.ErrOtherControllerHasTrack
				}
			*/
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// If they have already contacted departure, then initiating
			// track gives control as well; otherwise ControllingController
			// is left unset until contact.
			haveControl := ac.DepartureContactAltitude == 0

			ac.TrackingController = ctrl.Id()
			if haveControl {
				ac.ControllingController = ctrl.Id()
			}

			if err := s.State.STARSComputer().InitiateTrack(callsign, ctrl.Id(), fp, haveControl); err != nil {
				//s.lg.Errorf("InitiateTrack: %v", err)
			}
			if err := s.State.ERAMComputer().InitiateTrack(callsign, ctrl.Id(), fp); err != nil {
				//s.lg.Errorf("InitiateTrack: %v", err)
			}

			s.eventStream.Post(Event{
				Type:         InitiatedTrackEvent,
				Callsign:     ac.Callsign,
				ToController: ctrl.Id(),
			})

			return nil
		})
}

func (s *Sim) DropTrack(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.TrackingController = ""
			ac.ControllingController = ""

			if err := s.State.STARSComputer().DropTrack(ac); err != nil {
				//s.lg.Errorf("STARS DropTrack: %v", err)
			}
			if err := s.State.ERAMComputer().DropTrack(ac); err != nil {
				//s.lg.Errorf("ERAM DropTrack: %v", err)
			}

			s.eventStream.Post(Event{
				Type:           DroppedTrackEvent,
				Callsign:       ac.Callsign,
				FromController: ctrl.Id(),
			})
			return nil
		})
}

func (s *Sim) HandoffTrack(token, callsign, controller string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Id() {
				return av.ErrOtherControllerHasTrack
			} else if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
				/*
					} else if trk := s.State.STARSComputer().TrackInformation[ac.Callsign]; trk == nil {
						// no one is tracking it
						return av.ErrOtherControllerHasTrack
					} else if trk.TrackOwner != ctrl.Callsign {
						return av.ErrOtherControllerHasTrack
				*/
			} else if octrl.Id() == ctrl.Id() {
				// Can't handoff to ourself
				return av.ErrInvalidController
			} else {
				// Disallow handoff if there's a beacon code mismatch.
				squawkingSPC, _ := ac.Squawk.IsSPC()
				if trk := s.State.STARSComputer().TrackInformation[ac.Callsign]; trk != nil && trk.FlightPlan != nil {
					if ac.Squawk != trk.FlightPlan.AssignedSquawk && !squawkingSPC {
						return ErrBeaconMismatch
					}
				} else if ac.Squawk != ac.FlightPlan.AssignedSquawk && !squawkingSPC { // workaround pending NAS fixes
					return ErrBeaconMismatch
				}
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.handoffTrack(ctrl.Id(), octrl.Id(), ac.Callsign)
			return nil
		})
}

func (s *Sim) handoffTrack(fromTCP, toTCP string, callsign string) {
	s.eventStream.Post(Event{
		Type:           OfferedHandoffEvent,
		FromController: fromTCP,
		ToController:   toTCP,
		Callsign:       callsign,
	})

	s.State.Aircraft[callsign].HandoffTrackController = toTCP

	if from, fok := s.State.Controllers[fromTCP]; !fok {
		s.lg.Errorf("Unable to handoff %s: from controller %q not found", callsign, fromTCP)
	} else if to, tok := s.State.Controllers[toTCP]; !tok {
		s.lg.Errorf("Unable to handoff %s: to controller %q not found", callsign, toTCP)
	} else if err := s.State.STARSComputer().HandoffTrack(callsign, from, to, s.SimTime); err != nil {
		//s.lg.Errorf("HandoffTrack: %v", err)
	}

	// Add them to the auto-accept map even if the target is
	// covered; this way, if they sign off in the interim, we still
	// end up accepting it automatically.
	acceptDelay := 4 + rand.Intn(10)
	s.Handoffs[callsign] = Handoff{
		Time: s.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) HandoffControl(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.ControllingController != ctrl.Id() {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			var radioTransmissions []av.RadioTransmission
			// Immediately respond to the current controller that we're
			// changing frequency.
			if octrl, ok := s.State.Controllers[ac.TrackingController]; ok {
				if octrl.Frequency == ctrl.Frequency {
					radioTransmissions = append(radioTransmissions, av.RadioTransmission{
						Controller: ac.ControllingController,
						Message:    "Unable, we are already on " + octrl.Frequency.String(),
						Type:       av.RadioTransmissionReadback,
					})
					return radioTransmissions
				}
				bye := rand.Sample("good day", "seeya")
				contact := rand.Sample("contact ", "over to ", "")
				goodbye := contact + octrl.RadioName + " on " + octrl.Frequency.String() + ", " + bye
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: ac.ControllingController,
					Message:    goodbye,
					Type:       av.RadioTransmissionReadback,
				})
			} else {
				radioTransmissions = append(radioTransmissions, av.RadioTransmission{
					Controller: ac.ControllingController,
					Message:    "goodbye",
					Type:       av.RadioTransmissionReadback,
				})
			}

			s.eventStream.Post(Event{
				Type:           HandoffControlEvent,
				FromController: ac.ControllingController,
				ToController:   ac.TrackingController,
				Callsign:       ac.Callsign,
			})

			if err := s.State.STARSComputer().HandoffControl(callsign, ac.TrackingController); err != nil {
				//s.lg.Errorf("HandoffControl: %v", err)
			}

			// Take away the current controller's ability to issue control
			// commands.
			ac.ControllingController = ""

			// In 5-10 seconds, have the aircraft contact the new controller
			// (and give them control only then).
			s.enqueueControllerContact(ac.Callsign, ac.TrackingController)

			return radioTransmissions
		})
}

func (s *Sim) AcceptHandoff(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			/*
				trk := s.State.STARSComputer().TrackInformation[ac.Callsign]
				if trk == nil || trk.HandoffController != ctrl.Callsign {
					return av.ErrNotBeingHandedOffToMe
				}
			*/

			if ac.HandoffTrackController == ctrl.Id() {
				return nil
			}
			if po, ok := s.PointOuts[ac.Callsign]; ok && po.ToController == ctrl.Id() {
				// Point out where the recipient decided to take it as a handoff instead.
				return nil
			}
			return av.ErrNotBeingHandedOffToMe

		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           AcceptedHandoffEvent,
				FromController: ac.ControllingController,
				ToController:   ctrl.Id(),
				Callsign:       ac.Callsign,
			})

			ac.HandoffTrackController = ""
			ac.TrackingController = ctrl.Id()

			// Clean up if a point out was accepted as a handoff
			delete(s.PointOuts, ac.Callsign)

			if err := s.State.STARSComputer().AcceptHandoff(ac, ctrl, s.State.Controllers,
				s.State.STARSFacilityAdaptation, s.SimTime); err != nil {
				//s.lg.Errorf("AcceptHandoff: %v", err)
			}

			if !s.controllerIsSignedIn(ac.ControllingController) {
				// Don't wait for a frequency change instruction for
				// handoffs from virtual, but wait a bit before the
				// aircraft calls in at which point we have control.
				s.enqueueControllerContact(ac.Callsign, ctrl.Id())
			}

			return nil
		})
}

func (s *Sim) CancelHandoff(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			delete(s.Handoffs, ac.Callsign)
			ac.HandoffTrackController = ""
			ac.RedirectedHandoff = av.RedirectedHandoff{}

			err := s.State.STARSComputer().CancelHandoff(ac, ctrl, s.State.Controllers, s.SimTime)
			if err != nil {
				//s.lg.Errorf("CancelHandoff: %v", err)
			}

			return nil
		})
}

func (s *Sim) RedirectHandoff(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			} else if octrl.Id() == ctrl.Id() || octrl.Id() == ac.TrackingController {
				// Can't redirect to ourself and the controller who initiated the handoff
				return av.ErrInvalidController
			} else if octrl.FacilityIdentifier != ctrl.FacilityIdentifier {
				// Can't redirect to an interfacility position
				return av.ErrInvalidFacility
				/*
					} else if trk := s.State.STARSComputer().TrackInformation[callsign]; trk != nil && octrl.Callsign == trk.TrackOwner {
						return av.ErrInvalidController
				*/
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			ac.RedirectedHandoff.OriginalOwner = ac.TrackingController
			if ac.RedirectedHandoff.ShouldFallbackToHandoff(ctrl.Id(), octrl.Id()) {
				ac.HandoffTrackController = ac.RedirectedHandoff.Redirector[0]
				ac.RedirectedHandoff = av.RedirectedHandoff{}
				return nil
			}
			ac.RedirectedHandoff.AddRedirector(ctrl)
			ac.RedirectedHandoff.RedirectedTo = octrl.Id()

			s.State.STARSComputer().RedirectHandoff(ac, ctrl, octrl)

			return nil
		})
}

func (s *Sim) AcceptRedirectedHandoff(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			// TODO(mtrokel): need checks here that we do have an inbound
			// redirected handoff or that we have an outbound one to
			// recall.
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if ac.RedirectedHandoff.RedirectedTo == ctrl.Id() { // Accept
				s.eventStream.Post(Event{
					Type:           AcceptedRedirectedHandoffEvent,
					FromController: ac.RedirectedHandoff.OriginalOwner,
					ToController:   ctrl.Id(),
					Callsign:       ac.Callsign,
				})
				ac.ControllingController = ctrl.Id()
				ac.HandoffTrackController = ""
				ac.TrackingController = ac.RedirectedHandoff.RedirectedTo
				ac.RedirectedHandoff = av.RedirectedHandoff{}
			} else if ac.RedirectedHandoff.GetLastRedirector() == ctrl.Id() { // Recall (only the last redirector is able to recall)
				if len(ac.RedirectedHandoff.Redirector) > 1 { // Multiple redirected handoff, recall & still show "RD"
					ac.RedirectedHandoff.RedirectedTo = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-1]
				} else { // One redirect took place, clear the RD and show it as a normal handoff
					ac.HandoffTrackController = ac.RedirectedHandoff.Redirector[len(ac.RedirectedHandoff.Redirector)-1]
					ac.RedirectedHandoff = av.RedirectedHandoff{}
				}
			}

			err := s.State.STARSComputer().AcceptRedirectedHandoff(ac, ctrl)
			if err != nil {
				//s.lg.Errorf("AcceptRedirectedHandoff: %v", err)
			}

			return nil
		})
}

func (s *Sim) ForceQL(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if _, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.eventStream.Post(Event{
				Type:           ForceQLEvent,
				FromController: ctrl.Id(),
				ToController:   octrl.Id(),
				Callsign:       ac.Callsign,
			})

			return nil
		})
}

func (s *Sim) PointOut(token, callsign, controller string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if ac.TrackingController != ctrl.Id() {
				return av.ErrOtherControllerHasTrack
			} else if octrl, ok := s.State.Controllers[controller]; !ok {
				return av.ErrNoController
			} else if octrl.Facility != ctrl.Facility {
				// Can't point out to another STARS facility.
				return av.ErrInvalidController
			} else if octrl.Id() == ctrl.Id() {
				// Can't point out to ourself
				return av.ErrInvalidController
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			octrl := s.State.Controllers[controller]
			s.pointOut(ac.Callsign, ctrl, octrl)
			return nil
		})
}

func (s *Sim) pointOut(callsign string, from *av.Controller, to *av.Controller) {
	s.eventStream.Post(Event{
		Type:           PointOutEvent,
		FromController: from.Id(),
		ToController:   to.Id(),
		Callsign:       callsign,
	})

	if err := s.State.STARSComputer().PointOut(callsign, to.Id()); err != nil {
		//s.lg.Errorf("PointOut: %v", err)
	}

	acceptDelay := 4 + rand.Intn(10)
	s.PointOuts[callsign] = PointOut{
		FromController: from.Id(),
		ToController:   to.Id(),
		AcceptTime:     s.SimTime.Add(time.Duration(acceptDelay) * time.Second),
	}
}

func (s *Sim) AcknowledgePointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.ToController != ctrl.Id() {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: ctrl.Id(),
				ToController:   s.PointOuts[callsign].FromController,
				Callsign:       ac.Callsign,
			})
			if len(ac.PointOutHistory) < 20 {
				ac.PointOutHistory = append([]string{ctrl.Id()}, ac.PointOutHistory...)
			} else {
				ac.PointOutHistory = ac.PointOutHistory[:19]
				ac.PointOutHistory = append([]string{ctrl.Id()}, ac.PointOutHistory...)
			}

			delete(s.PointOuts, callsign)

			err := s.State.STARSComputer().AcknowledgePointOut(ac.Callsign, ctrl.Id())
			if err != nil {
				//s.lg.Errorf("AcknowledgePointOut: %v", err)
			}

			return nil
		})
}

func (s *Sim) RecallPointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.FromController != ctrl.Id() {
				return av.ErrNotPointedOutByMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			s.eventStream.Post(Event{
				Type:           RecalledPointOutEvent,
				FromController: ctrl.Id(),
				ToController:   s.PointOuts[callsign].ToController,
				Callsign:       ac.Callsign,
			})

			delete(s.PointOuts, callsign)

			err := s.State.STARSComputer().RecallPointOut(ac.Callsign, ctrl.Id())
			if err != nil {
				//s.lg.Errorf("RecallPointOut: %v", err)
			}

			return nil
		})
}

func (s *Sim) RejectPointOut(token, callsign string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if po, ok := s.PointOuts[callsign]; !ok || po.ToController != ctrl.Id() {
				return av.ErrNotPointedOutToMe
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// As with auto accepts, "to" and "from" are swapped in the
			// event since they are w.r.t. the original point out.
			s.eventStream.Post(Event{
				Type:           RejectedPointOutEvent,
				FromController: ctrl.Id(),
				ToController:   s.PointOuts[callsign].FromController,
				Callsign:       ac.Callsign,
			})

			delete(s.PointOuts, callsign)

			err := s.State.STARSComputer().RejectPointOut(ac.Callsign, ctrl.Id())
			if err != nil {
				//s.lg.Errorf("RejectPointOut: %v", err)
			}

			return nil
		})
}

func (s *Sim) ToggleSPCOverride(token, callsign, spc string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.ToggleSPCOverride(spc)
			return nil
		})
}

func (s *Sim) ReleaseDeparture(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	sc, ok := s.controllers[token]
	if !ok {
		return ErrInvalidControllerToken
	}

	ac, ok := s.State.Aircraft[callsign]
	if !ok {
		return av.ErrNoAircraftForCallsign
	}
	if s.State.DepartureController(ac, s.lg) != sc.Id {
		return ErrInvalidDepartureController
	}

	stars := s.State.STARSComputer()
	if err := stars.ReleaseDeparture(callsign); err == nil {
		ac.Released = true
		ac.ReleaseTime = s.State.SimTime
		return nil
	} else {
		return err
	}
}

func (s *Sim) AssignAltitude(token, callsign string, altitude int, afterSpeed bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignAltitude(altitude, afterSpeed)
		})
}

func (s *Sim) SetTemporaryAltitude(token, callsign string, altitude int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.TempAltitude = altitude
			return nil
		})
}

func (s *Sim) SetPilotReportedAltitude(token, callsign string, altitude int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			// Must own the track
			if ac.TrackingController != ctrl.Id() && !s.Instructors[ctrl.Id()] {
				return av.ErrOtherControllerHasTrack
			}
			if ac.Mode == av.Altitude && !ac.InhibitModeCAltitudeDisplay {
				// 5-166: must inhibit mode C display if we are getting altitude from the aircraft
				return ErrIllegalFunction
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			ac.PilotReportedAltitude = altitude
			return nil
		})
}

func (s *Sim) ToggleDisplayModeCAltitude(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchTrackingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			// 5-167
			ac.InhibitModeCAltitudeDisplay = !ac.InhibitModeCAltitudeDisplay

			if !ac.InhibitModeCAltitudeDisplay && ac.Mode == av.Altitude {
				// Clear pilot reported if toggled on and we have mode-C altitude
				ac.PilotReportedAltitude = 0
			}
			return nil
		})
}

type HeadingArgs struct {
	ControllerToken string
	Callsign        string
	Heading         int
	Present         bool
	LeftDegrees     int
	RightDegrees    int
	Turn            av.TurnMethod
}

func (s *Sim) AssignHeading(hdg *HeadingArgs) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(hdg.ControllerToken, hdg.Callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if hdg.Present {
				return ac.FlyPresentHeading()
			} else if hdg.LeftDegrees != 0 {
				return ac.TurnLeft(hdg.LeftDegrees)
			} else if hdg.RightDegrees != 0 {
				return ac.TurnRight(hdg.RightDegrees)
			} else {
				return ac.AssignHeading(hdg.Heading, hdg.Turn)
			}
		})
}

func (s *Sim) AssignSpeed(token, callsign string, speed int, afterAltitude bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AssignSpeed(speed, afterAltitude)
		})
}

func (s *Sim) MaintainSlowestPractical(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.MaintainSlowestPractical()
		})
}

func (s *Sim) MaintainMaximumForward(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.MaintainMaximumForward()
		})
}

func (s *Sim) SaySpeed(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SaySpeed()
		})
}

func (s *Sim) SayAltitude(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SayAltitude()
		})
}

func (s *Sim) SayHeading(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.SayHeading()
		})
}

func (s *Sim) ExpediteDescent(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpediteDescent()
		})
}

func (s *Sim) ExpediteClimb(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpediteClimb()
		})
}

func (s *Sim) DirectFix(token, callsign, fix string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DirectFix(fix)
		})
}

func (s *Sim) DepartFixDirect(token, callsign, fixa string, fixb string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DepartFixDirect(fixa, fixb)
		})
}

func (s *Sim) DepartFixHeading(token, callsign, fix string, heading int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DepartFixHeading(fix, heading)
		})
}

func (s *Sim) CrossFixAt(token, callsign, fix string, ar *av.AltitudeRestriction, speed int) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.CrossFixAt(fix, ar, speed)
		})
}

func (s *Sim) AtFixCleared(token, callsign, fix, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.AtFixCleared(fix, approach)
		})
}

func (s *Sim) ExpectApproach(token, callsign, approach string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var ap *av.Airport
	if ac, ok := s.State.Aircraft[callsign]; ok {
		ap = s.State.Airports[ac.FlightPlan.ArrivalAirport]
		if ap == nil {
			return av.ErrUnknownAirport
		}
	}

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ExpectApproach(approach, ap, s.lg)
		})
}

func (s *Sim) ClearedApproach(token, callsign, approach string, straightIn bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if straightIn {
				return ac.ClearedStraightInApproach(approach)
			} else {
				return ac.ClearedApproach(approach, s.lg)
			}
		})
}

func (s *Sim) InterceptLocalizer(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.InterceptApproach()
		})
}

func (s *Sim) CancelApproachClearance(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.CancelApproachClearance()
		})
}

func (s *Sim) ClimbViaSID(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ClimbViaSID()
		})
}

func (s *Sim) DescendViaSTAR(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.DescendViaSTAR()
		})
}

func (s *Sim) GoAround(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			resp := ac.GoAround()
			for i := range resp {
				resp[i].Type = av.RadioTransmissionUnexpected
			}
			return resp
		})
}

func (s *Sim) ContactTower(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchControllingCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			return ac.ContactTower(s.lg)
		})
}

func (s *Sim) DeleteAircraft(token, callsign string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.dispatchCommand(token, callsign,
		func(ctrl *av.Controller, ac *av.Aircraft) error {
			if lctrl := s.LaunchConfig.Controller; lctrl != "" && lctrl != ctrl.Id() {
				return av.ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *av.Controller, ac *av.Aircraft) []av.RadioTransmission {
			if s.State.IsIntraFacility(ac) {
				s.TotalDepartures--
				s.TotalArrivals--
			} else if s.State.IsDeparture(ac) {
				s.TotalDepartures--
			} else if s.State.IsArrival(ac) {
				s.TotalArrivals--
			} else {
				s.TotalOverflights--
			}

			s.eventStream.Post(Event{
				Type:    StatusMessageEvent,
				Message: fmt.Sprintf("%s deleted %s", ctrl.Id(), ac.Callsign),
			})

			s.lg.Info("deleted aircraft", slog.String("callsign", ac.Callsign),
				slog.String("controller", ctrl.Id()))

			s.State.DeleteAircraft(ac)

			return nil
		})
}

func (s *Sim) DeleteAllAircraft(token string) error {
	for cs := range s.State.Aircraft {
		if err := s.DeleteAircraft(token, cs); err != nil {
			return err
		}
	}
	for ap := range s.DeparturePool {
		s.DeparturePool[ap] = nil
	}
	for _, rwys := range s.LastDeparture {
		clear(rwys)
	}

	return nil
}

var badCallsigns map[string]interface{} = map[string]interface{}{
	// 9/11
	"AAL11":  nil,
	"UAL175": nil,
	"AAL77":  nil,
	"UAL93":  nil,

	// Pilot suicide
	"MAS17":   nil,
	"MAS370":  nil,
	"GWI18G":  nil,
	"GWI9525": nil,
	"MSR990":  nil,

	// Hijackings
	"FDX705":  nil,
	"AFR8969": nil,

	// Selected major crashes (leaning toward callsigns vice uses or is
	// likely to use in the future, via
	// https://en.wikipedia.org/wiki/List_of_deadliest_aircraft_accidents_and_incidents
	"PAA1736": nil,
	"KLM4805": nil,
	"JAL123":  nil,
	"AIC182":  nil,
	"AAL191":  nil,
	"PAA103":  nil,
	"KAL007":  nil,
	"AAL587":  nil,
	"CAL140":  nil,
	"TWA800":  nil,
	"SWR111":  nil,
	"KAL801":  nil,
	"AFR447":  nil,
	"CAL611":  nil,
	"LOT5055": nil,
	"ICE001":  nil,
}

func (ss *State) sampleAircraft(al av.AirlineSpecifier, lg *log.Logger) (*av.Aircraft, string) {
	dbAirline, ok := av.DB.Airlines[al.ICAO]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Airline %s, not found in database", al.ICAO)
		return nil, ""
	}

	// Sample according to fleet count
	var aircraft string
	acCount := 0
	for _, ac := range al.Aircraft() {
		// Reservoir sampling...
		acCount += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(acCount) {
			aircraft = ac.ICAO
		}
	}

	perf, ok := av.DB.AircraftPerformance[aircraft]
	if !ok {
		// TODO: validation stage...
		lg.Errorf("Aircraft %s not found in performance database from airline %+v",
			aircraft, al)
		return nil, ""
	}

	// random callsign
	callsign := strings.ToUpper(dbAirline.ICAO)
	for {
		format := "####"
		if len(dbAirline.Callsign.CallsignFormats) > 0 {
			f, ok := rand.SampleWeighted(dbAirline.Callsign.CallsignFormats,
				func(f string) int {
					if _, wt, ok := strings.Cut(f, "x"); ok { // we have a weight
						if v, err := strconv.Atoi(wt); err == nil {
							return v
						}
					}
					return 1
				})
			if ok {
				format = f
			}
		}

		id := ""
	loop:
		for i, ch := range format {
			switch ch {
			case '#':
				if i == 0 {
					// Don't start with a 0.
					id += strconv.Itoa(1 + rand.Intn(9))
				} else {
					id += strconv.Itoa(rand.Intn(10))
				}
			case '@':
				id += string(rune('A' + rand.Intn(26)))
			case 'x':
				break loop
			}
		}
		if _, ok := ss.Aircraft[callsign+id]; ok {
			continue // it already exits
		} else if _, ok := badCallsigns[callsign+id]; ok {
			continue // nope
		} else {
			callsign += id
			break
		}
	}

	acType := aircraft
	if perf.WeightClass == "H" {
		acType = "H/" + acType
	}
	if perf.WeightClass == "J" {
		acType = "J/" + acType
	}

	return &av.Aircraft{
		Callsign: callsign,
		Mode:     av.Altitude,
	}, acType
}

func (s *Sim) CreateArrival(arrivalGroup string, arrivalAirport string) (*av.Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createArrivalNoLock(arrivalGroup, arrivalAirport)
}

func (s *Sim) createArrivalNoLock(group string, arrivalAirport string) (*av.Aircraft, error) {
	goAround := rand.Float32() < s.LaunchConfig.GoAroundRate

	arrivals := s.State.InboundFlows[group].Arrivals
	// Randomly sample from the arrivals that have a route to this airport.
	idx := rand.SampleFiltered(arrivals, func(ar av.Arrival) bool {
		_, ok := ar.Airlines[arrivalAirport]
		return ok
	})

	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			group, arrivalAirport)
	}
	arr := arrivals[idx]

	airline := rand.SampleSlice(arr.Airlines[arrivalAirport])
	ac, acType := s.State.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	sq, err := s.State.ERAMComputer().CreateSquawk()
	if err != nil {
		return nil, err
	}
	ac.Squawk = sq
	ac.FlightPlan = ac.NewFlightPlan(av.IFR, acType, airline.Airport, arrivalAirport)

	// Figure out which controller will (for starters) get the arrival
	// handoff. For single-user, it's easy.  Otherwise, figure out which
	// control position is initially responsible for the arrival. Note that
	// the actual handoff controller will be resolved later when the
	// handoff happens, so that it can reflect which controllers are
	// actually signed in at that point.
	arrivalController := s.State.PrimaryController
	if len(s.State.MultiControllers) > 0 {
		var err error
		arrivalController, err = s.State.MultiControllers.GetInboundController(group)
		if err != nil {
			s.lg.Error("Unable to resolve arrival controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}

		if arrivalController == "" {
			arrivalController = s.State.PrimaryController
		}
	}

	if err := ac.InitializeArrival(s.State.Airports[arrivalAirport], &arr, arrivalController,
		goAround, s.State.NmPerLongitude, s.State.MagneticVariation, s.State /* wind */, s.lg); err != nil {
		return nil, err
	}

	facility, ok := s.State.FacilityFromController(ac.TrackingController)
	if !ok {
		return nil, ErrUnknownControllerFacility
	}
	s.State.ERAMComputers.AddArrival(ac, facility, s.State.STARSFacilityAdaptation, s.SimTime)

	return ac, nil
}

func (s *Sim) CreateDeparture(departureAirport, runway, category string) (*av.Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createDepartureNoLock(departureAirport, runway, category)
}

func (s *Sim) createDepartureNoLock(departureAirport, runway, category string) (*av.Aircraft, error) {
	ap := s.State.Airports[departureAirport]
	if ap == nil {
		return nil, av.ErrUnknownAirport
	}

	idx := slices.IndexFunc(s.State.DepartureRunways,
		func(r ScenarioGroupDepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, av.ErrUnknownRunway
	}
	rwy := &s.State.DepartureRunways[idx]

	// Sample uniformly, minding the category, if specified
	idx = rand.SampleFiltered(ap.Departures,
		func(d av.Departure) bool {
			_, ok := rwy.ExitRoutes[d.Exit] // make sure the runway handles the exit
			return ok && (rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit])
		})
	if idx == -1 {
		// This shouldn't ever happen...
		return nil, fmt.Errorf("%s/%s: unable to find a valid departure",
			departureAirport, rwy.Runway)
	}
	dep := &ap.Departures[idx]

	airline := rand.SampleSlice(dep.Airlines)
	ac, acType := s.State.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.IFR
	if dep.Unassociated {
		ac.Squawk = 0o1200
		rules = av.VFR
		if r := rand.Float32(); r < .02 {
			ac.Mode = av.On // mode-A
		} else if r < .03 {
			ac.Mode = av.Standby // flat out off
		}
	} else {
		sq, err := s.State.ERAMComputer().CreateSquawk()
		if err != nil {
			return nil, err
		}
		ac.Squawk = sq
	}
	ac.FlightPlan = ac.NewFlightPlan(rules, acType, departureAirport, dep.Destination)

	exitRoute := rwy.ExitRoutes[dep.Exit]
	if err := ac.InitializeDeparture(ap, departureAirport, dep, runway, *exitRoute,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.State.Scratchpads,
		s.State.PrimaryController, s.State.MultiControllers, s.State /* wind */, s.lg); err != nil {
		return nil, err
	}

	if rules == av.IFR {
		eram := s.State.ERAMComputer()
		eram.AddDeparture(ac.FlightPlan, s.State.TRACON, s.SimTime)
	}

	return ac, nil
}

func (s *Sim) CreateOverflight(group string) (*av.Aircraft, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return s.createOverflightNoLock(group)
}

func (s *Sim) createOverflightNoLock(group string) (*av.Aircraft, error) {
	overflights := s.State.InboundFlows[group].Overflights
	// Randomly sample an overflight
	of := rand.SampleSlice(overflights)

	airline := rand.SampleSlice(of.Airlines)
	ac, acType := s.State.sampleAircraft(airline.AirlineSpecifier, s.lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	rules := av.IFR
	if of.Unassociated {
		ac.Squawk = 0o1200
		rules = av.VFR
		if r := rand.Float32(); r < .02 {
			ac.Mode = av.On // mode-A
		} else if r < .03 {
			ac.Mode = av.Standby // flat out off
		}
	} else {
		sq, err := s.State.ERAMComputer().CreateSquawk()
		if err != nil {
			return nil, err
		}
		ac.Squawk = sq
	}
	ac.FlightPlan = ac.NewFlightPlan(rules, acType, airline.DepartureAirport,
		airline.ArrivalAirport)

	// Figure out which controller will (for starters) get the handoff. For
	// single-user, it's easy.  Otherwise, figure out which control
	// position is initially responsible for the arrival. Note that the
	// actual handoff controller will be resolved later when the handoff
	// happens, so that it can reflect which controllers are actually
	// signed in at that point.
	controller := s.State.PrimaryController
	if len(s.State.MultiControllers) > 0 {
		var err error
		controller, err = s.State.MultiControllers.GetInboundController(group)
		if err != nil {
			s.lg.Error("Unable to resolve overflight controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}
		if controller == "" {
			controller = s.State.PrimaryController
		}
	}

	if err := ac.InitializeOverflight(&of, controller, s.State.NmPerLongitude, s.State.MagneticVariation,
		s.State /* wind */, s.lg); err != nil {
		return nil, err
	}

	return ac, nil
}

func (s *Sim) CreateRestrictionArea(ra RestrictionArea) (int, error) {
	ra.UpdateTriangles()

	// Look for a free slot from one that was deleted
	for i, ua := range s.State.UserRestrictionAreas {
		if ua.Deleted {
			s.State.UserRestrictionAreas[i] = ra
			return i + 1, nil
		}
	}

	if n := len(s.State.UserRestrictionAreas); n < MaxRestrictionAreas {
		s.State.UserRestrictionAreas = append(s.State.UserRestrictionAreas, ra)
		return n + 1, nil
	}

	return 0, ErrTooManyRestrictionAreas
}

func (s *Sim) UpdateRestrictionArea(idx int, ra RestrictionArea) error {
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

	s.State.UserRestrictionAreas[idx] = RestrictionArea{Deleted: true}
	return nil
}

func (s *Sim) GetVideoMapLibrary(filename string) (*av.VideoMapLibrary, error) {
	return av.LoadVideoMapLibrary(filename)
}

// Deferred operations

type FutureControllerContact struct {
	Callsign string
	TCP      string
	Time     time.Time
}

func (s *Sim) enqueueControllerContact(callsign, tcp string) {
	wait := time.Duration(5+rand.Intn(10)) * time.Second
	s.FutureControllerContacts = append(s.FutureControllerContacts,
		FutureControllerContact{Callsign: callsign, TCP: tcp, Time: s.SimTime.Add(wait)})
}

type FutureOnCourse struct {
	Callsign string
	Time     time.Time
}

func (s *Sim) enqueueDepartOnCourse(callsign string) {
	wait := time.Duration(10+rand.Intn(15)) * time.Second
	s.FutureOnCourse = append(s.FutureOnCourse,
		FutureOnCourse{Callsign: callsign, Time: s.SimTime.Add(wait)})
}

type FutureChangeSquawk struct {
	Callsign string
	Code     av.Squawk
	Mode     av.TransponderMode
	Time     time.Time
}

func (s *Sim) enqueueTransponderChange(callsign string, code av.Squawk, mode av.TransponderMode) {
	wait := time.Duration(5+rand.Intn(5)) * time.Second
	s.FutureSquawkChanges = append(s.FutureSquawkChanges,
		FutureChangeSquawk{Callsign: callsign, Code: code, Mode: mode, Time: s.SimTime.Add(wait)})
}

func (s *Sim) processEnqueued() {
	s.FutureControllerContacts = util.FilterSlice(s.FutureControllerContacts,
		func(c FutureControllerContact) bool {
			if s.SimTime.After(c.Time) {
				if ac, ok := s.State.Aircraft[c.Callsign]; ok {
					ac.ControllingController = c.TCP
					r := []av.RadioTransmission{av.RadioTransmission{
						Controller: c.TCP,
						Message:    ac.ContactMessage(s.ReportingPoints),
						Type:       av.RadioTransmissionContact,
					}}
					PostRadioEvents(c.Callsign, r, s)

					// For departures handed off to virtual controllers,
					// enqueue climbing them to cruise sending them direct
					// to their first fix if they aren't already.
					ctrl := s.State.Controllers[ac.ControllingController]
					if s.State.IsDeparture(ac) && ctrl != nil && !ctrl.IsHuman {
						s.enqueueDepartOnCourse(ac.Callsign)
					}
				}
				return false // remove it from the slice
			}
			return true // keep it in the slice
		})

	s.FutureOnCourse = util.FilterSlice(s.FutureOnCourse,
		func(oc FutureOnCourse) bool {
			if s.SimTime.After(oc.Time) {
				if ac, ok := s.State.Aircraft[oc.Callsign]; ok {
					s.lg.Info("departing on course", slog.String("callsign", ac.Callsign),
						slog.Int("final_altitude", ac.FlightPlan.Altitude))
					ac.DepartOnCourse(s.lg)
				}
				return false
			}
			return true
		})

	s.FutureSquawkChanges = util.FilterSlice(s.FutureSquawkChanges,
		func(fcs FutureChangeSquawk) bool {
			if s.SimTime.After(fcs.Time) {
				if ac, ok := s.State.Aircraft[fcs.Callsign]; ok {
					ac.Squawk = fcs.Code
					ac.Mode = fcs.Mode
				}
				return false
			}
			return true
		})
}
