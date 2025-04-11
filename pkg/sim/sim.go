// pkg/sim/sim.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"log/slog"
	"maps"
	"slices"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/util"

	"github.com/brunoga/deep"
)

type Sim struct {
	State *State

	mu util.LoggingMutex

	Aircraft map[av.ADSBCallsign]*Aircraft

	SignOnPositions  map[string]*av.Controller
	humanControllers map[string]*EventsSubscription

	STARSComputer *STARSComputer
	ERAMComputer  *ERAMComputer

	eventStream *EventStream
	lg          *log.Logger

	// Airport -> runway -> state
	DepartureState map[string]map[string]*RunwayLaunchState
	// Key is inbound flow group name
	NextInboundSpawn map[string]time.Time

	Handoffs map[av.ADSBCallsign]Handoff
	// a/c callsign -> PointOut
	PointOuts map[av.ADSBCallsign]PointOut

	ReportingPoints []av.ReportingPoint

	FutureControllerContacts []FutureControllerContact
	FutureOnCourse           []FutureOnCourse
	FutureSquawkChanges      []FutureChangeSquawk

	lastSimUpdate  time.Time
	updateTimeSlop time.Duration
	lastUpdateTime time.Time // this is w.r.t. true wallclock time
	lastLogTime    time.Time

	prespawn                 bool
	prespawnUncontrolledOnly bool

	NextPushStart time.Time // both w.r.t. sim time
	PushEnd       time.Time

	Instructors map[string]bool

	// No need to serialize these; they're caches anyway.
	bravoAirspace   *av.AirspaceGrid
	charlieAirspace *av.AirspaceGrid
}

type Aircraft struct {
	av.Aircraft

	STARSFlightPlan *av.STARSFlightPlan

	HoldForRelease   bool
	Released         bool // only used for hold for release
	ReleaseTime      time.Time
	WaitingForLaunch bool // for departures

	GoAroundDistance *float32

	// Departure related state
	DepartureContactAltitude   float32
	DepartureContactController string

	// Who to try to hand off to at a waypoint with /ho
	WaypointHandoffController string

	// The controller who gave approach clearance
	ApproachController string
}

type RadarTrack struct {
	av.RadarTrack

	FlightPlan *av.STARSFlightPlan

	// Sort of hacky to carry these along here but it's convenient...
	DepartureContactController string
	DepartureAirport           string
	DepartureAirportElevation  float32
	DepartureAirportLocation   math.Point2LL
	ArrivalAirport             string
	ArrivalAirportElevation    float32
	ArrivalAirportLocation     math.Point2LL
	IsAirborne                 bool
	OnExtendedCenterline       bool
	OnApproach                 bool
	ATPAVolume                 *av.ATPAVolume
	MVAsApply                  bool
	HoldForRelease             bool
	Route                      []math.Point2LL
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

// NewSimConfiguration collects all of the information required to create a new Sim
type NewSimConfiguration struct {
	TRACON      string
	Description string

	Airports         map[string]*av.Airport
	PrimaryAirport   string
	DepartureRunways []DepartureRunway
	ArrivalRunways   []ArrivalRunway
	InboundFlows     map[string]*av.InboundFlow
	LaunchConfig     LaunchConfig
	Fixes            map[string]math.Point2LL

	ControlPositions   map[string]*av.Controller
	PrimaryController  string
	ControllerAirspace map[string][]string
	VirtualControllers []string
	MultiControllers   av.SplitConfiguration
	SignOnPositions    map[string]*av.Controller

	TFRs                    []av.TFR
	LiveWeather             bool
	Wind                    av.Wind
	STARSFacilityAdaptation av.STARSFacilityAdaptation
	IsLocal                 bool

	ReportingPoints   []av.ReportingPoint
	MagneticVariation float32
	NmPerLongitude    float32
	Center            math.Point2LL
	Range             float32
	DefaultMaps       []string
	Airspace          av.Airspace
}

func NewSim(config NewSimConfiguration, manifest *av.VideoMapManifest, lg *log.Logger) *Sim {
	beaconBank := util.Select(config.STARSFacilityAdaptation.BeaconBank != 0,
		config.STARSFacilityAdaptation.BeaconBank, 3) // don't hand out 00xx codes

	s := &Sim{
		Aircraft: make(map[av.ADSBCallsign]*Aircraft),

		DepartureState:   make(map[string]map[string]*RunwayLaunchState),
		NextInboundSpawn: make(map[string]time.Time),

		SignOnPositions: config.SignOnPositions,

		ERAMComputer:  makeERAMComputer(av.DB.TRACONs[config.TRACON].ARTCC),
		STARSComputer: makeSTARSComputer(config.TRACON, beaconBank),

		humanControllers: make(map[string]*EventsSubscription),

		eventStream: NewEventStream(lg),
		lg:          lg,

		ReportingPoints: config.ReportingPoints,

		lastUpdateTime: time.Now(),

		Handoffs:  make(map[av.ADSBCallsign]Handoff),
		PointOuts: make(map[av.ADSBCallsign]PointOut),

		Instructors: make(map[string]bool),
	}

	s.State = newState(config, manifest, lg)

	s.setInitialSpawnTimes(time.Now()) // FIXME? will be clobbered in prespawn

	return s
}

func (s *Sim) Activate(lg *log.Logger) {
	s.lg = lg

	if s.eventStream == nil {
		s.eventStream = NewEventStream(lg)
	}
	s.humanControllers = make(map[string]*EventsSubscription)
	s.State.HumanControllers = nil

	now := time.Now()
	s.lastUpdateTime = now
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

func (s *Sim) SignOn(tcp string, instructor bool) (*State, error) {
	if err := s.signOn(tcp, instructor); err != nil {
		return nil, err
	}
	return s.State.GetStateForController(tcp), nil
}

func (s *Sim) signOn(tcp string, instructor bool) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

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

	s.humanControllers[tcp] = s.eventStream.Subscribe()
	s.State.Controllers[tcp] = s.SignOnPositions[tcp]
	s.State.HumanControllers = append(s.State.HumanControllers, tcp)

	if tcp == s.State.PrimaryController {
		// The primary controller signed in so the sim will resume.
		// Reset lastUpdateTime so that the next time Update() is
		// called for the sim, we don't try to run a ton of steps.
		s.lastUpdateTime = time.Now()
	}
	if instructor {
		s.Instructors[tcp] = true
	}

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: tcp + " has signed on.",
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

	s.humanControllers[tcp].Unsubscribe()

	delete(s.humanControllers, tcp)
	delete(s.State.Controllers, tcp)
	delete(s.Instructors, tcp)
	s.State.HumanControllers =
		slices.DeleteFunc(s.State.HumanControllers, func(s string) bool { return s == tcp })

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: tcp + " has signed off.",
	})
	s.lg.Infof("%s: controller signing off", tcp)

	return nil
}

func (s *Sim) ChangeControlPosition(fromTCP, toTCP string, keepTracks bool) error {
	s.lg.Infof("%s: switching to %s", fromTCP, toTCP)

	// Make sure we can successfully sign on before signing off from the
	// current position.
	if err := s.signOn(toTCP, s.Instructors[fromTCP]); err != nil {
		return err
	}

	// Swap the event subscriptions so we don't lose any events pending on the old one.
	s.humanControllers[toTCP].Unsubscribe()
	s.humanControllers[toTCP] = s.humanControllers[fromTCP]
	s.State.HumanControllers = append(s.State.HumanControllers, toTCP)

	delete(s.humanControllers, fromTCP)
	delete(s.State.Controllers, fromTCP)
	delete(s.Instructors, fromTCP)
	slices.DeleteFunc(s.State.HumanControllers, func(s string) bool { return s == fromTCP })

	s.eventStream.Post(Event{
		Type:    StatusMessageEvent,
		Message: fromTCP + " has signed off.",
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
		Type:    GlobalMessageEvent,
		Message: tcp + " has " + util.Select(s.State.Paused, "paused", "unpaused") + " the sim",
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
		Message:        message,
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

	// Figure out which positions are available; start with all of the possible ones,
	// then delete those that are active
	available[s.State.PrimaryController] = *s.SignOnPositions[s.State.PrimaryController]
	for id := range s.State.MultiControllers {
		available[id] = *s.SignOnPositions[id]
	}
	for tcp := range s.humanControllers {
		delete(available, tcp)
		covered[tcp] = *s.SignOnPositions[tcp]
	}

	return available, covered
}

type GlobalMessage struct {
	Message        string
	FromController string
}

type WorldUpdate struct {
	RadarTracks             []RadarTrack
	UnsupportedTracks       []RadarTrack
	Controllers             map[string]*av.Controller
	HumanControllers        []string
	FlightPlans             map[av.ADSBCallsign]av.FlightPlan
	UnassociatedFlightPlans []av.STARSFlightPlan
	ReleaseDepartures       []ReleaseDeparture

	Time time.Time

	LaunchConfig LaunchConfig

	UserRestrictionAreas []av.RestrictionArea

	SimIsPaused          bool
	SimRate              float32
	TotalIFR, TotalVFR   int
	Events               []Event
	Instructors          map[string]bool
	QuickFlightPlanIndex int
}

func (s *Sim) GetWorldUpdate(tcp string, update *WorldUpdate) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	var events []Event
	if sub, ok := s.humanControllers[tcp]; ok {
		events = sub.Get()
	}

	var err error
	*update, err = deep.Copy(WorldUpdate{
		UnsupportedTracks:       s.State.UnsupportedTracks,
		Controllers:             s.State.Controllers,
		HumanControllers:        slices.Collect(maps.Keys(s.humanControllers)),
		UnassociatedFlightPlans: s.STARSComputer.FlightPlans,
		Time:                    s.State.SimTime,
		LaunchConfig:            s.State.LaunchConfig,
		SimIsPaused:             s.State.Paused,
		SimRate:                 s.State.SimRate,
		TotalIFR:                s.State.TotalIFR,
		TotalVFR:                s.State.TotalVFR,
		Events:                  events,
		UserRestrictionAreas:    s.State.UserRestrictionAreas,
		Instructors:             s.Instructors,
		QuickFlightPlanIndex:    s.State.QuickFlightPlanIndex,
	})

	update.FlightPlans = make(map[av.ADSBCallsign]av.FlightPlan)
	for cs, ac := range s.Aircraft {
		update.FlightPlans[cs] = ac.FlightPlan
	}

	for _, ac := range s.STARSComputer.HoldForRelease {
		update.ReleaseDepartures = append(update.ReleaseDepartures,
			ReleaseDeparture{
				ADSBCallsign:               ac.ADSBCallsign,
				DepartureAirport:           ac.FlightPlan.DepartureAirport, // TODO: STARS fp entry fix?
				Released:                   ac.Released,
				Squawk:                     ac.Squawk,
				ListIndex:                  ac.STARSFlightPlan.ListIndex,
				AircraftType:               ac.STARSFlightPlan.AircraftType,
				Exit:                       ac.STARSFlightPlan.ExitFix,
				departureContactController: ac.DepartureContactController,
			})
	}

	for _, callsign := range util.SortedMapKeys(s.Aircraft) {
		ac := s.Aircraft[callsign]
		rt := RadarTrack{
			RadarTrack:                 ac.GetRadarTrack(),
			FlightPlan:                 ac.STARSFlightPlan,
			DepartureContactController: ac.DepartureContactController,
			DepartureAirport:           ac.FlightPlan.DepartureAirport,
			DepartureAirportElevation:  ac.DepartureAirportElevation(),
			DepartureAirportLocation:   ac.DepartureAirportLocation(),
			ArrivalAirport:             ac.FlightPlan.ArrivalAirport,
			ArrivalAirportElevation:    ac.ArrivalAirportElevation(),
			ArrivalAirportLocation:     ac.ArrivalAirportLocation(),
			IsAirborne:                 ac.IsAirborne(),
			OnExtendedCenterline:       ac.OnExtendedCenterline(0.2),
			OnApproach:                 ac.OnApproach(false), /* don't check altitude */
			MVAsApply:                  ac.MVAsApply(),
			HoldForRelease:             ac.HoldForRelease,
			ATPAVolume:                 ac.ATPAVolume(),
		}
		for _, wp := range ac.Nav.Waypoints {
			rt.Route = append(rt.Route, wp.Location)
		}

		update.RadarTracks = append(update.RadarTracks, rt)
	}

	return err
}

func (wu *WorldUpdate) UpdateState(state *State, eventStream *EventStream) {
	state.RadarTracks = wu.RadarTracks
	if wu.Controllers != nil {
		state.Controllers = wu.Controllers
	}
	state.HumanControllers = wu.HumanControllers
	state.FlightPlans = wu.FlightPlans
	state.UnassociatedFlightPlans = wu.UnassociatedFlightPlans
	state.ReleaseDepartures = wu.ReleaseDepartures
	state.LaunchConfig = wu.LaunchConfig

	state.UserRestrictionAreas = wu.UserRestrictionAreas

	state.SimTime = wu.Time
	state.Paused = wu.SimIsPaused
	state.SimRate = wu.SimRate
	state.TotalIFR = wu.TotalIFR
	state.TotalVFR = wu.TotalVFR
	state.Instructors = wu.Instructors
	state.QuickFlightPlanIndex = wu.QuickFlightPlanIndex

	// Important: do this after updating aircraft, controllers, etc.,
	// so that they reflect any changes the events are flagging.
	for _, e := range wu.Events {
		eventStream.Post(e)
	}
}

func (s *Sim) ResolveController(tcp string) string {
	if s.State.MultiControllers == nil {
		// Single controller
		return s.State.PrimaryController
	} else {
		c, err := s.State.MultiControllers.ResolveController(tcp,
			func(callsign string) bool {
				return s.isActiveHumanController(callsign)
			})
		if err != nil {
			s.lg.Errorf("%s: unable to resolve controller: %v", tcp, err)
		}

		if c == "" { // This shouldn't happen...
			return s.State.PrimaryController
		}
		return c
	}
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
	s.State.SimTime = s.State.SimTime

	s.lastUpdateTime = time.Now()

	// Log the current state of everything once a minute
	if time.Since(s.lastLogTime) > time.Minute {
		s.lastLogTime = time.Now()
		s.lg.Info("sim", slog.Any("state", s))
	}
}

// separate so time management can be outside this so we can do the prespawn stuff...
func (s *Sim) updateState() {
	now := s.State.SimTime

	for callsign, ho := range s.Handoffs {
		if !now.After(ho.AutoAcceptTime) && !s.prespawn {
			continue
		}

		if ac, ok := s.Aircraft[callsign]; ok && ac.IsAssociated() {
			sfp := ac.STARSFlightPlan
			if sfp.HandoffTrackController != "" && !s.isActiveHumanController(sfp.HandoffTrackController) {
				// Automated accept
				s.eventStream.Post(Event{
					Type:           AcceptedHandoffEvent,
					FromController: sfp.TrackingController,
					ToController:   sfp.HandoffTrackController,
					ADSBCallsign:   ac.ADSBCallsign,
				})
				s.lg.Info("automatic handoff accept", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
					slog.String("from", sfp.TrackingController),
					slog.String("to", sfp.HandoffTrackController))

				sfp.TrackingController = sfp.HandoffTrackController
				sfp.HandoffTrackController = ""
			}
		}
		delete(s.Handoffs, callsign)
	}

	for callsign, po := range s.PointOuts {
		if !now.After(po.AcceptTime) {
			continue
		}

		if ac, ok := s.Aircraft[callsign]; ok && !s.isActiveHumanController(po.ToController) {
			// Note that "to" and "from" are swapped in the event,
			// since the ack is coming from the "to" controller of the
			// original point out.
			s.eventStream.Post(Event{
				Type:           AcknowledgedPointOutEvent,
				FromController: po.ToController,
				ToController:   po.FromController,
				ADSBCallsign:   ac.ADSBCallsign,
			})
			s.lg.Info("automatic pointout accept", slog.String("adsb_callsign", string(ac.ADSBCallsign)),
				slog.String("by", po.ToController), slog.String("to", po.FromController))

			delete(s.PointOuts, callsign)
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
			if passedWaypoint != nil {
				if ac.IsAssociated() {
					// Things that only apply to associated aircraft
					sfp := ac.STARSFlightPlan
					if passedWaypoint.HumanHandoff {
						// Handoff from virtual controller to a human controller.
						s.handoffTrack(sfp.TrackingController, s.ResolveController(ac.WaypointHandoffController), ac)
					} else if passedWaypoint.TCPHandoff != "" {
						s.handoffTrack(sfp.TrackingController, passedWaypoint.TCPHandoff, ac)
					}

					if passedWaypoint.ClearApproach {
						ac.ApproachController = ac.STARSFlightPlan.ControllingController
					}

					if passedWaypoint.TransferComms {
						// We didn't enqueue this before since we knew an
						// explicit comms handoff was coming so go ahead and
						// send them to the controller's frequency. Note that
						// we use WaypointHandoffController and not
						// ac.TrackingController, since the human controller
						// may have already flashed the track to a virtual
						// controller.
						ctrl := s.ResolveController(ac.WaypointHandoffController)
						s.enqueueControllerContact(ac.ADSBCallsign, ctrl, 0 /* no delay */)
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
								s.pointOut(ac.ADSBCallsign, fromCtrl, ctrl)
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
					// If we're more than 150 feet AGL, go around.
					lowEnough := alt == nil || ac.Altitude() <= alt.TargetAltitude(ac.Altitude())+150
					if lowEnough {
						s.lg.Info("deleting landing at waypoint", slog.Any("waypoint", passedWaypoint))
						s.deleteAircraft(ac)
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
				!s.prespawn {
				// Time to check in
				ctrl := s.ResolveController(ac.DepartureContactController)
				s.lg.Info("contacting departure controller", slog.String("callsign", ctrl))

				airportName := ac.FlightPlan.DepartureAirport
				if ap, ok := s.State.Airports[airportName]; ok && ap.Name != "" {
					airportName = ap.Name
				}

				msg := "departing " + airportName + ", " + ac.Nav.DepartureMessage()
				s.postRadioEvents(ac.ADSBCallsign, []av.RadioTransmission{av.RadioTransmission{
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
				ac.STARSFlightPlan.ControllingController = ctrl
			}

			// Cull far-away aircraft
			if math.NMDistance2LL(ac.Position(), s.State.Center) > 250 {
				s.lg.Info("culled far-away aircraft", slog.String("adsb_callsign", string(callsign)))
				s.deleteAircraft(ac)
			}
		}

		// Handle assorted deferred radio calls.
		s.processEnqueued()

		s.spawnAircraft()

		s.ERAMComputer.Update(s)
		s.STARSComputer.Update(s)
	}
}

func (s *Sim) goAround(ac *Aircraft) {
	if ac.IsUnassociated() { // this shouldn't happen...
		return
	}
	sfp := ac.STARSFlightPlan

	// Update controller before calling GoAround so the
	// transmission goes to the right controller.
	var ok bool
	sfp.ControllingController, ok = s.State.DepartureController(ac.DepartureContactController, s.lg)
	if !ok {
		sfp.ControllingController = s.State.PrimaryController
	}

	rt := ac.GoAround()
	s.postRadioEvents(ac.ADSBCallsign, rt)

	// If it was handed off to tower, hand it back to us
	if sfp.TrackingController != "" && sfp.TrackingController != ac.ApproachController {
		sfp.HandoffTrackController, ok = s.State.DepartureController(ac.DepartureContactController, s.lg)
		if !ok {
			sfp.HandoffTrackController = ac.ApproachController
		}
		s.eventStream.Post(Event{
			Type:           OfferedHandoffEvent,
			ADSBCallsign:   ac.ADSBCallsign,
			FromController: sfp.TrackingController,
			ToController:   ac.ApproachController,
		})
	}
}

func (s *Sim) postRadioEvents(from av.ADSBCallsign, transmissions []av.RadioTransmission) {
	for _, rt := range transmissions {
		s.eventStream.Post(Event{
			Type:                  RadioTransmissionEvent,
			ADSBCallsign:          from,
			ToController:          rt.Controller,
			Message:               rt.Message,
			RadioTransmissionType: rt.Type,
		})
	}
}

func (s *Sim) CallsignForACID(acid av.ACID) (av.ADSBCallsign, bool) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	for cs, ac := range s.Aircraft {
		if ac.IsAssociated() && ac.STARSFlightPlan.ACID == acid {
			return cs, true
		}
	}
	return av.ADSBCallsign(""), false
}

func (t *RadarTrack) IsAssociated() bool {
	return t.FlightPlan != nil
}

func (t *RadarTrack) IsUnassociated() bool {
	return t.FlightPlan == nil
}

func (t *RadarTrack) IsDeparture() bool {
	return t.IsAssociated() && t.FlightPlan.TypeOfFlight == av.FlightTypeDeparture
}

func (t *RadarTrack) IsArrival() bool {
	return t.IsAssociated() && t.FlightPlan.TypeOfFlight == av.FlightTypeArrival
}

func (t *RadarTrack) IsOverflight() bool {
	return t.IsAssociated() && t.FlightPlan.TypeOfFlight == av.FlightTypeOverflight
}

func (t *RadarTrack) HandingOffTo(tcp string) bool {
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

func (ac *Aircraft) AssociateFlightPlan(fp *av.STARSFlightPlan) {
	ac.STARSFlightPlan = fp
}

func (ac *Aircraft) UpdateFlightPlan(spec av.STARSFlightPlanSpecifier) av.STARSFlightPlan {
	if ac.STARSFlightPlan != nil {
		if spec.InitialController.IsSet {
			ac.STARSFlightPlan.TrackingController = spec.InitialController.Get()
		}

		ac.STARSFlightPlan.Update(spec)
		return *ac.STARSFlightPlan
	}
	return spec.GetFlightPlan()
}
