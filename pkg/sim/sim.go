// pkg/sim/sim.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	crand "crypto/rand"
	"encoding/base64"
	"log/slog"
	"sort"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"

	"github.com/brunoga/deep"
)

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

	Instructors map[string]bool

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

		SimTime:        time.Now(),
		lastUpdateTime: time.Now(),

		SimRate:   1,
		Handoffs:  make(map[string]Handoff),
		PointOuts: make(map[string]PointOut),

		Instructors: make(map[string]bool),
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

func (s *Sim) IdleTime() time.Duration {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)
	return time.Since(s.lastUpdateTime)
}

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

func (s *Sim) GlobalMessage(controller, message string) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.eventStream.Post(Event{
		Type:           GlobalMessageEvent,
		Message:        message,
		FromController: controller,
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

	var controllers []string
	for _, ctrl := range s.controllers {
		controllers = append(controllers, ctrl.Id)
	}
	sort.Strings(controllers)
	return controllers
}

func (s *Sim) GetAvailableCoveredPositions() (map[string]av.Controller, map[string]av.Controller) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	available := make(map[string]av.Controller)
	covered := make(map[string]av.Controller)

	// Figure out which positions are available; start with all of the possible ones,
	// then delete those that are active
	available[s.State.PrimaryController] = *s.SignOnPositions[s.State.PrimaryController]
	for id := range s.State.MultiControllers {
		available[id] = *s.SignOnPositions[id]
	}
	for _, ctrl := range s.controllers {
		delete(available, ctrl.Id)
		if wc, ok := s.State.Controllers[ctrl.Id]; ok && wc.IsHuman {
			covered[ctrl.Id] = *s.SignOnPositions[ctrl.Id]
		}
	}

	return available, covered
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

	UserRestrictionAreas []av.RestrictionArea

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

func (s *Sim) controllerIsSignedIn(id string) bool {
	for _, ctrl := range s.controllers {
		if ctrl.Id == id {
			return true
		}
	}
	return false
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

			passedWaypoint := ac.Update(s.State, nil /* s.lg*/)
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
				s.postRadioEvents(ac.Callsign, []av.RadioTransmission{av.RadioTransmission{
					Controller: ctrl,
					Message:    msg,
					Type:       av.RadioTransmissionContact,
				}})

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
	s.postRadioEvents(ac.Callsign, rt)

	// If it was handed off to tower, hand it back to us
	if ac.TrackingController != "" && ac.TrackingController != ac.ApproachController {
		ac.HandoffTrackController = s.State.DepartureController(ac, s.lg)
		if ac.HandoffTrackController == "" {
			ac.HandoffTrackController = ac.ApproachController
		}
		s.eventStream.Post(Event{
			Type:           OfferedHandoffEvent,
			Callsign:       ac.Callsign,
			FromController: ac.TrackingController,
			ToController:   ac.ApproachController,
		})
	}
}

func (s *Sim) postRadioEvents(from string, transmissions []av.RadioTransmission) {
	for _, rt := range transmissions {
		s.eventStream.Post(Event{
			Type:                  RadioTransmissionEvent,
			Callsign:              from,
			ToController:          rt.Controller,
			Message:               rt.Message,
			RadioTransmissionType: rt.Type,
		})
	}
}
