// world.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"log/slog"
	gomath "math"
	"slices"
	"strconv"
	"strings"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/util"

	"github.com/mmp/imgui-go/v4"
)

const initialSimSeconds = 45

///////////////////////////////////////////////////////////////////////////
// World

type World struct {
	// Used on the client side only
	simProxy *SimProxy

	lastUpdateRequest time.Time
	lastReturnedTime  time.Time
	updateCall        *util.PendingCall

	pendingCalls []*util.PendingCall

	sameGateDepartures int
	sameDepartureCap   int

	client ClientState

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	SimState
}

type SimState struct {
	Aircraft    map[string]*av.Aircraft
	METAR       map[string]*av.METAR
	Controllers map[string]*av.Controller

	DepartureAirports map[string]*av.Airport
	ArrivalAirports   map[string]*av.Airport

	TRACON                   string
	LaunchConfig             LaunchConfig
	PrimaryController        string
	MultiControllers         av.SplitConfiguration
	SimIsPaused              bool
	SimRate                  float32
	SimName                  string
	SimDescription           string
	SimTime                  time.Time
	MagneticVariation        float32
	NmPerLongitude           float32
	Airports                 map[string]*av.Airport
	Fixes                    map[string]math.Point2LL
	PrimaryAirport           string
	RadarSites               map[string]*av.RadarSite
	Center                   math.Point2LL
	Range                    float32
	Wind                     av.Wind
	Callsign                 string
	ScenarioDefaultVideoMaps []string
	ApproachAirspace         []ControllerAirspaceVolume
	DepartureAirspace        []ControllerAirspaceVolume
	DepartureRunways         []ScenarioGroupDepartureRunway
	ArrivalRunways           []ScenarioGroupArrivalRunway
	Scratchpads              map[string]string
	ArrivalGroups            map[string][]av.Arrival
	TotalDepartures          int
	TotalArrivals            int
	STARSFacilityAdaptation  STARSFacilityAdaptation
}

func NewWorldFromSimState(ss SimState, controllerToken string, client *util.RPCClient) *World {
	return &World{
		SimState: ss,
		simProxy: &SimProxy{
			ControllerToken: controllerToken,
			Client:          client,
		},
	}
}

func NewWorld() *World {
	return &World{
		SimState: SimState{
			Aircraft:    make(map[string]*av.Aircraft),
			METAR:       make(map[string]*av.METAR),
			Controllers: make(map[string]*av.Controller),
		},
	}
}

func (w *World) GetWindVector(p math.Point2LL, alt float32) math.Point2LL {
	// Sinusoidal wind speed variation from the base speed up to base +
	// gust and then back...
	base := time.UnixMicro(0)
	sec := w.SimTime.Sub(base).Seconds()
	windSpeed := float32(w.Wind.Speed) +
		float32(w.Wind.Gust-w.Wind.Speed)*float32(1+gomath.Cos(sec/4))/2

	// Wind.Direction is where it's coming from, so +180 to get the vector
	// that affects the aircraft's course.
	d := math.OppositeHeading(float32(w.Wind.Direction))
	vWind := [2]float32{math.Sin(math.Radians(d)), math.Cos(math.Radians(d))}
	vWind = math.Scale2f(vWind, windSpeed/3600)
	return vWind
}

func (w *World) AverageWindVector() [2]float32 {
	d := math.OppositeHeading(float32(w.Wind.Direction))
	v := [2]float32{math.Sin(math.Radians(d)), math.Cos(math.Radians(d))}
	return math.Scale2f(v, float32(w.Wind.Speed))
}

func (w *World) GetAirport(icao string) *av.Airport {
	return w.Airports[icao]
}

func (ss *SimState) Locate(s string) (math.Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if ap, ok := ss.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := ss.Fixes[s]; ok {
		return p, true
	} else if n, ok := av.DB.Navaids[strings.ToUpper(s)]; ok {
		return n.Location, ok
	} else if ap, ok := av.DB.Airports[strings.ToUpper(s)]; ok {
		return ap.Location, ok
	} else if f, ok := av.DB.Fixes[strings.ToUpper(s)]; ok {
		return f.Location, ok
	} else if p, err := math.ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else {
		return math.Point2LL{}, false
	}
}

func (w *World) AllAirports() map[string]*av.Airport {
	all := util.DuplicateMap(w.DepartureAirports)
	for name, ap := range w.ArrivalAirports {
		all[name] = ap
	}
	return all
}

func (w *World) SetSquawk(callsign string, squawk av.Squawk) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) TakeOrReturnLaunchControl(eventStream *EventStream) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.TakeOrReturnLaunchControl(),
			IssueTime: time.Now(),
			OnErr: func(e error) {
				eventStream.Post(Event{
					Type:    StatusMessageEvent,
					Message: e.Error(),
				})
			},
		})
}

func (w *World) LaunchAircraft(ac av.Aircraft) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.LaunchAircraft(ac),
			IssueTime: time.Now(),
		})
}

func (w *World) SendGlobalMessage(global GlobalMessage) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.GlobalMessage(global),
			IssueTime: time.Now(),
		})
}

func (w *World) SetScratchpad(callsign string, scratchpad string, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == w.Callsign {
		ac.Scratchpad = scratchpad
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.SetScratchpad(callsign, scratchpad),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) SetSecondaryScratchpad(callsign string, scratchpad string, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == w.Callsign {
		ac.SecondaryScratchpad = scratchpad
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.SetSecondaryScratchpad(callsign, scratchpad),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) SetTemporaryAltitude(callsign string, alt int, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == w.Callsign {
		ac.TempAltitude = alt
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.SetTemporaryAltitude(callsign, alt),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AmendFlightPlan(callsign string, fp av.FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetGlobalLeaderLine(callsign string, dir *math.CardinalOrdinalDirection, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.SetGlobalLeaderLine(callsign, dir),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) InitiateTrack(callsign string, success func(any), err func(error)) {
	// Modifying locally is not canonical but improves perceived latency in
	// the common case; the RPC may fail, though that's fine; the next
	// world update will roll back these changes anyway.
	//
	// As in sim.go, only check for an unset TrackingController; we may already
	// have ControllingController due to a pilot checkin on a departure.
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == "" {
		ac.TrackingController = w.Callsign
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.InitiateTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) DropTrack(callsign string, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == w.Callsign {
		ac.TrackingController = ""
		ac.ControllingController = ""
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.DropTrack(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) HandoffTrack(callsign string, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.HandoffTrack(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AcceptHandoff(callsign string, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.HandoffTrackController == w.Callsign {
		ac.HandoffTrackController = ""
		ac.TrackingController = w.Callsign
		ac.ControllingController = w.Callsign
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.AcceptHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RedirectHandoff(callsign, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.RedirectHandoff(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AcceptRedirectedHandoff(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.AcceptRedirectedHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) CancelHandoff(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.CancelHandoff(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) ForceQL(callsign, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.ForceQL(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RemoveForceQL(callsign, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.RemoveForceQL(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) PointOut(callsign string, controller string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.PointOut(callsign, controller),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) AcknowledgePointOut(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.AcknowledgePointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) RejectPointOut(callsign string, success func(any), err func(error)) {
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.RejectPointOut(callsign),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) ToggleSPCOverride(callsign string, spc string, success func(any), err func(error)) {
	if ac := w.Aircraft[callsign]; ac != nil && ac.TrackingController == w.Callsign {
		ac.ToggleSPCOverride(spc)
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.ToggleSPCOverride(callsign, spc),
			IssueTime: time.Now(),
			OnSuccess: success,
			OnErr:     err,
		})
}

func (w *World) ChangeControlPosition(callsign string, keepTracks bool) error {
	err := w.simProxy.ChangeControlPosition(callsign, keepTracks)
	if err == nil {
		w.Callsign = callsign
	}
	return err
}

func (w *World) Disconnect() {
	if err := w.simProxy.SignOff(nil, nil); err != nil {
		lg.Errorf("Error signing off from sim: %v", err)
	}
	w.Aircraft = nil
	w.Controllers = nil
}

func (ss *SimState) AircraftFromPartialCallsign(c string) *av.Aircraft {
	if ac, ok := ss.Aircraft[c]; ok {
		return ac
	}

	var final []*av.Aircraft
	for callsign, ac := range ss.Aircraft {
		if ac.ControllingController == ss.Callsign && strings.Contains(callsign, c) {
			final = append(final, ac)
		}
	}
	if len(final) == 1 {
		return final[0]
	} else {
		return nil
	}
}

func (ss *SimState) DepartureController(ac *av.Aircraft) string {
	if len(ss.MultiControllers) > 0 {
		callsign, err := ss.MultiControllers.ResolveController(ac.DepartureContactController,
			func(callsign string) bool {
				ctrl, ok := ss.Controllers[callsign]
				return ok && ctrl.IsHuman
			})
		if err != nil {
			lg.Error("Unable to resolve departure controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}
		return util.Select(callsign != "", callsign, ss.PrimaryController)
	} else {
		return ss.PrimaryController
	}
}

func (w *World) GetUpdates(eventStream *EventStream, onErr func(error)) {
	if w.simProxy == nil {
		return
	}

	if w.updateCall != nil && w.updateCall.CheckFinished() {
		w.updateCall = nil
		return
	}

	w.checkPendingRPCs(eventStream)

	// Wait in seconds between update fetches; no less than 50ms
	rate := math.Clamp(1/w.SimRate, 0.05, 1)
	if d := time.Since(w.lastUpdateRequest); d > time.Duration(rate*float32(time.Second)) {
		if w.updateCall != nil {
			lg.Warnf("GetUpdates still waiting for %s on last update call", d)
			return
		}
		w.lastUpdateRequest = time.Now()

		wu := &SimWorldUpdate{}
		w.updateCall = &util.PendingCall{
			Call:      w.simProxy.GetWorldUpdate(wu),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				d := time.Since(w.updateCall.IssueTime)
				if d > 250*time.Millisecond {
					lg.Warnf("Slow world update response %s", d)
				} else {
					lg.Debugf("World update response time %s", d)
				}
				wu.UpdateWorld(w, eventStream)
			},
			OnErr: onErr,
		}
	}
}

func (w *World) checkPendingRPCs(eventStream *EventStream) {
	w.pendingCalls = util.FilterSlice(w.pendingCalls,
		func(call *util.PendingCall) bool { return !call.CheckFinished() })
}

func (w *World) Connected() bool {
	return w.simProxy != nil
}

func (w *World) GetSerializeSim() (*Sim, error) {
	return w.simProxy.GetSerializeSim()
}

func (w *World) PreSave() {
	w.STARSFacilityAdaptation.PreSave()
}

func (w *World) PostLoad(ml *av.VideoMapLibrary) error {
	return w.STARSFacilityAdaptation.PostLoad(ml)
}

func (w *World) ToggleSimPause() {
	w.pendingCalls = append(w.pendingCalls, &util.PendingCall{
		Call:      w.simProxy.TogglePause(),
		IssueTime: time.Now(),
	})
}

func (w *World) GetSimRate() float32 {
	if w.SimRate == 0 {
		return 1
	}
	return w.SimRate
}

func (w *World) SetSimRate(r float32) {
	w.pendingCalls = append(w.pendingCalls, &util.PendingCall{
		Call:      w.simProxy.SetSimRate(r),
		IssueTime: time.Now(),
	})
	w.SimRate = r // so the UI is well-behaved...
}

func (w *World) SetLaunchConfig(lc LaunchConfig) {
	w.pendingCalls = append(w.pendingCalls, &util.PendingCall{
		Call:      w.simProxy.SetLaunchConfig(lc),
		IssueTime: time.Now(),
	})
	w.LaunchConfig = lc // for the UI's benefit...
}

// CurrentTime returns an extrapolated value that models the current Sim's time.
// (Because the Sim may be running remotely, we have to make some approximations,
// though they shouldn't cause much trouble since we get an update from the Sim
// at least once a second...)
func (w *World) CurrentTime() time.Time {
	t := w.SimTime

	if !w.SimIsPaused && !w.lastUpdateRequest.IsZero() {
		d := time.Since(w.lastUpdateRequest)

		// Roughly account for RPC overhead; more for a remote server (where
		// SimName will be set.)
		if w.SimName == "" {
			d -= 10 * time.Millisecond
		} else {
			d -= 50 * time.Millisecond
		}
		d = math.Max(0, d)

		// Account for sim rate
		d = time.Duration(float64(d) * float64(w.SimRate))

		t = t.Add(d)
	}

	// Make sure we don't ever go backward; this can happen due to
	// approximations in the above when an updated current time comes in
	// with a Sim update.
	if t.After(w.lastReturnedTime) {
		w.lastReturnedTime = t
	}
	return w.lastReturnedTime
}

func (w *World) GetWindowTitle() string {
	if w.SimDescription == "" {
		return "(disconnected)"
	} else {
		deparr := fmt.Sprintf(" [ %d departures %d arrivals ]", w.TotalDepartures, w.TotalArrivals)
		if w.SimName == "" {
			return w.Callsign + ": " + w.SimDescription + deparr
		} else {
			return w.Callsign + "@" + w.SimName + ": " + w.SimDescription + deparr
		}
	}
}

func (ss *SimState) GetVideoMaps() ([]av.VideoMap, []string) {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.Callsign]; ok {
		return config.VideoMaps, config.DefaultMaps
	}
	return ss.STARSFacilityAdaptation.VideoMaps, ss.ScenarioDefaultVideoMaps
}

func (ss *SimState) GetInitialRange() float32 {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.Callsign]; ok && config.Range != 0 {
		return config.Range
	}
	return ss.Range
}

func (ss *SimState) GetInitialCenter() math.Point2LL {
	if config, ok := ss.STARSFacilityAdaptation.ControllerConfigs[ss.Callsign]; ok && !config.Center.IsZero() {
		return config.Center
	}
	return ss.Center
}

func (ss *SimState) InhibitCAVolumes() []av.AirspaceVolume {
	return ss.STARSFacilityAdaptation.InhibitCAVolumes
}

func (w *World) DeleteAllAircraft(onErr func(err error)) {
	if lctrl := w.LaunchConfig.Controller; lctrl == "" || lctrl == w.Callsign {
		w.Aircraft = nil
	}

	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.DeleteAllAircraft(),
			IssueTime: time.Now(),
			OnErr:     onErr,
		})
}

func (w *World) RunAircraftCommands(callsign string, cmds string, handleResult func(message string, remainingInput string)) {
	var result AircraftCommandsResult
	w.pendingCalls = append(w.pendingCalls,
		&util.PendingCall{
			Call:      w.simProxy.RunAircraftCommands(callsign, cmds, &result),
			IssueTime: time.Now(),
			OnSuccess: func(any) {
				handleResult(result.ErrorMessage, result.RemainingInput)
			},
			OnErr: func(err error) {
				lg.Errorf("%s: %v", callsign, err)
			},
		})
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

func (w *World) sampleAircraft(icao, fleet string) (*av.Aircraft, string) {
	al, ok := av.DB.Airlines[icao]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Chose airline %s, not found in database", icao)
		return nil, ""
	}

	if fleet == "" {
		fleet = "default"
	}

	fl, ok := al.Fleets[fleet]
	if !ok {
		// TODO: this also should be caught at validation time...
		lg.Errorf("Airline %s doesn't have a \"%s\" fleet!", icao, fleet)
		return nil, ""
	}

	// Sample according to fleet count
	var aircraft string
	acCount := 0
	for _, ac := range fl {
		// Reservoir sampling...
		acCount += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(acCount) {
			aircraft = ac.ICAO
		}
	}

	perf, ok := av.DB.AircraftPerformance[aircraft]
	if !ok {
		// TODO: validation stage...
		lg.Errorf("Aircraft %s not found in performance database from fleet %+v, airline %s",
			aircraft, fleet, icao)
		return nil, ""
	}

	// random callsign
	callsign := strings.ToUpper(icao)
	for {
		format := "####"
		if len(al.Callsign.CallsignFormats) > 0 {
			format = rand.SampleSlice(al.Callsign.CallsignFormats)
		}

		id := ""
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
			}
		}
		if _, ok := w.Aircraft[callsign+id]; ok {
			continue // it already exits
		} else if _, ok := badCallsigns[callsign+id]; ok {
			continue // nope
		} else {
			callsign += id
			break
		}
	}

	squawk := av.Squawk(rand.Intn(0o7000))

	acType := aircraft
	if perf.WeightClass == "H" {
		acType = "H/" + acType
	}
	if perf.WeightClass == "J" {
		acType = "J/" + acType
	}

	return &av.Aircraft{
		Callsign:       callsign,
		AssignedSquawk: squawk,
		Squawk:         squawk,
		Mode:           av.Charlie,
	}, acType
}

func (w *World) CreateArrival(arrivalGroup string, arrivalAirport string, goAround bool) (*av.Aircraft, error) {
	arrivals := w.ArrivalGroups[arrivalGroup]
	// Randomly sample from the arrivals that have a route to this airport.
	idx := rand.SampleFiltered(arrivals, func(ar av.Arrival) bool {
		_, ok := ar.Airlines[arrivalAirport]
		return ok
	})

	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			arrivalGroup, arrivalAirport)
	}
	arr := arrivals[idx]

	airline := rand.SampleSlice(arr.Airlines[arrivalAirport])
	ac, acType := w.sampleAircraft(airline.ICAO, airline.Fleet)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightPlan = av.NewFlightPlan(av.IFR, acType, airline.Airport, arrivalAirport)

	// Figure out which controller will (for starters) get the arrival
	// handoff. For single-user, it's easy.  Otherwise, figure out which
	// control position is initially responsible for the arrival. Note that
	// the actual handoff controller will be resolved later when the
	// handoff happens, so that it can reflect which controllers are
	// actually signed in at that point.
	arrivalController := w.PrimaryController
	if len(w.MultiControllers) > 0 {
		var err error
		arrivalController, err = w.MultiControllers.GetArrivalController(arrivalGroup)
		if err != nil {
			lg.Error("Unable to resolve arrival controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}

		if arrivalController == "" {
			arrivalController = w.PrimaryController
		}
	}

	if err := ac.InitializeArrival(w.Airports[arrivalAirport], w.ArrivalGroups,
		arrivalGroup, idx, arrivalController, goAround, w.NmPerLongitude, w.MagneticVariation, lg); err != nil {
		return nil, err
	}

	return ac, nil
}

func (w *World) CreateDeparture(departureAirport, runway, category string, challenge float32,
	lastDeparture *av.Departure) (*av.Aircraft, *av.Departure, error) {
	ap := w.Airports[departureAirport]
	if ap == nil {
		return nil, nil, ErrUnknownAirport
	}

	idx := slices.IndexFunc(w.DepartureRunways,
		func(r ScenarioGroupDepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, nil, ErrUnknownRunway
	}
	rwy := &w.DepartureRunways[idx]

	var dep *av.Departure
	if w.sameDepartureCap == 0 {
		w.sameDepartureCap = rand.Intn(3) + 1 // Set the initial max same departure cap (1-3)
	}
	if rand.Float32() < challenge && lastDeparture != nil && w.sameGateDepartures < w.sameDepartureCap {
		// 50/50 split between the exact same departure and a departure to
		// the same gate as the last departure.
		pred := util.Select(rand.Float32() < .5,
			func(d av.Departure) bool { return d.Exit == lastDeparture.Exit },
			func(d av.Departure) bool {
				_, ok := rwy.ExitRoutes[d.Exit] // make sure the runway handles the exit
				return ok && ap.ExitCategories[d.Exit] == ap.ExitCategories[lastDeparture.Exit]
			})

		if idx := rand.SampleFiltered(ap.Departures, pred); idx == -1 {
			// This should never happen...
			lg.Errorf("%s/%s/%s: unable to sample departure", departureAirport, runway, category)
		} else {
			dep = &ap.Departures[idx]
		}

	}

	if dep == nil {
		// Sample uniformly, minding the category, if specified
		idx := rand.SampleFiltered(ap.Departures,
			func(d av.Departure) bool {
				_, ok := rwy.ExitRoutes[d.Exit] // make sure the runway handles the exit
				return ok && (rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit])
			})

		if idx == -1 {
			// This shouldn't ever happen...
			return nil, nil, fmt.Errorf("%s/%s: unable to find a valid departure",
				departureAirport, rwy.Runway)
		}
		dep = &ap.Departures[idx]
	}

	if lastDeparture != nil && (dep.Exit == lastDeparture.Exit && w.sameGateDepartures >= w.sameDepartureCap) {
		return nil, nil, fmt.Errorf("couldn't make a departure")
	}

	// Same gate buffer is a random int between 3-4 that gives a period after a few same gate departures.
	// For example, WHITE, WHITE, WHITE, DIXIE, NEWEL, GAYEL, MERIT, DIXIE, DIXIE
	// Another same-gate departure will not be happen untill after MERIT (in this example) because of the buffer.
	sameGateBuffer := rand.Intn(2) + 3

	if w.sameGateDepartures >= w.sameDepartureCap+sameGateBuffer || (lastDeparture != nil && dep.Exit != lastDeparture.Exit) { // reset back to zero if its at 7 or if there is a new gate
		w.sameDepartureCap = rand.Intn(3) + 1
		w.sameGateDepartures = 0
	}

	airline := rand.SampleSlice(dep.Airlines)
	ac, acType := w.sampleAircraft(airline.ICAO, airline.Fleet)
	if ac == nil {
		return nil, nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightPlan = av.NewFlightPlan(av.IFR, acType, departureAirport, dep.Destination)
	exitRoute := rwy.ExitRoutes[dep.Exit]
	if err := ac.InitializeDeparture(ap, departureAirport, dep, runway, exitRoute,
		w.NmPerLongitude, w.MagneticVariation, w.Scratchpads,
		w.PrimaryController, w.MultiControllers, lg); err != nil {
		return nil, nil, err
	}

	/* Keep adding to World sameGateDepartures number until the departure cap + the buffer so that no more
	same-gate departures are launched, then reset it to zero. Once the buffer is reached, it will reset World sameGateDepartures to zero*/
	w.sameGateDepartures += 1

	return ac, dep, nil
}

///////////////////////////////////////////////////////////////////////////
// Settings

func (w *World) ToggleActivateSettingsWindow() {
	w.client.showSettings = !w.client.showSettings
}

func (w *World) ToggleShowScenarioInfoWindow() {
	w.client.showScenarioInfo = !w.client.showScenarioInfo
}

type MissingPrimaryModalClient struct {
	world *World
}

func (mp *MissingPrimaryModalClient) Title() string {
	return "Missing Primary Controller"
}

func (mp *MissingPrimaryModalClient) Opening() {}

func (mp *MissingPrimaryModalClient) Buttons() []ModalDialogButton {
	var b []ModalDialogButton
	b = append(b, ModalDialogButton{text: "Sign in to " + mp.world.PrimaryController, action: func() bool {
		err := mp.world.ChangeControlPosition(mp.world.PrimaryController, true)
		return err == nil
	}})
	b = append(b, ModalDialogButton{text: "Disconnect", action: func() bool {
		newSimConnectionChan <- nil // This will lead to a World Disconnect() call in main.go
		uiCloseModalDialog(mp.world.client.missingPrimaryDialog)
		return true
	}})
	return b
}

func (mp *MissingPrimaryModalClient) Draw() int {
	imgui.Text("The primary controller, " + mp.world.PrimaryController + ", has disconnected from the server or is otherwise unreachable.\nThe simulation will be paused until a primary controller signs in.")
	return -1
}

func (w *World) DrawMissingPrimaryDialog(p platform.Platform) {
	if _, ok := w.Controllers[w.PrimaryController]; ok {
		if w.client.missingPrimaryDialog != nil {
			uiCloseModalDialog(w.client.missingPrimaryDialog)
			w.client.missingPrimaryDialog = nil
		}
	} else {
		if w.client.missingPrimaryDialog == nil {
			w.client.missingPrimaryDialog = NewModalDialogBox(&MissingPrimaryModalClient{world: w}, p)
			uiShowModalDialog(w.client.missingPrimaryDialog, true)
		}
	}
}

func (w *World) DrawSettingsWindow(p platform.Platform) {
	if !w.client.showSettings {
		return
	}

	imgui.BeginV("Settings", &w.client.showSettings, imgui.WindowFlagsAlwaysAutoResize)

	if imgui.SliderFloatV("Simulation speed", &w.SimRate, 1, 20, "%.1f", 0) {
		w.SetSimRate(w.SimRate)
	}

	update := !globalConfig.InhibitDiscordActivity.Load()
	imgui.Checkbox("Update Discord activity status", &update)
	globalConfig.InhibitDiscordActivity.Store(!update)

	if imgui.BeginComboV("UI Font Size", strconv.Itoa(globalConfig.UIFontSize), imgui.ComboFlagsHeightLarge) {
		sizes := make(map[int]interface{})
		for fontid := range fonts {
			if fontid.Name == "Roboto Regular" {
				sizes[fontid.Size] = nil
			}
		}
		for _, size := range util.SortedMapKeys(sizes) {
			if imgui.SelectableV(strconv.Itoa(size), size == globalConfig.UIFontSize, 0, imgui.Vec2{}) {
				globalConfig.UIFontSize = size
				ui.font = GetFont(renderer.FontIdentifier{Name: "Roboto Regular", Size: globalConfig.UIFontSize})
			}
		}
		imgui.EndCombo()
	}

	var fsp *FlightStripPane
	var messages *MessagesPane
	var stars *STARSPane
	globalConfig.DisplayRoot.VisitPanes(func(p Pane) {
		switch pane := p.(type) {
		case *FlightStripPane:
			fsp = pane
		case *STARSPane:
			stars = pane
		case *MessagesPane:
			messages = pane
		}
	})

	if imgui.CollapsingHeader("STARS") {
		stars.DrawUI(p)
	}

	if imgui.CollapsingHeader("Display") {
		if imgui.Checkbox("Enable anti-aliasing", &globalConfig.EnableMSAA) {
			uiShowModalDialog(NewModalDialogBox(
				&MessageModalClient{
					title: "Alert",
					message: "You must restart vice for changes to the anti-aliasing " +
						"mode to take effect.",
				}, p), true)
		}

		imgui.Checkbox("Start in full-screen", &globalConfig.StartInFullScreen)

		monitorNames := p.GetAllMonitorNames()
		if imgui.BeginComboV("Monitor", monitorNames[globalConfig.FullScreenMonitor], imgui.ComboFlagsHeightLarge) {
			for index, monitor := range monitorNames {
				if imgui.SelectableV(monitor, monitor == monitorNames[globalConfig.FullScreenMonitor], 0, imgui.Vec2{}) {
					globalConfig.FullScreenMonitor = index

					p.EnableFullScreen(p.IsFullScreen())
				}
			}

			imgui.EndCombo()
		}
	}
	if fsp != nil && imgui.CollapsingHeader("Flight Strips") {
		fsp.DrawUI()
	}
	if messages != nil && imgui.CollapsingHeader("Messages") {
		messages.DrawUI()
	}

	imgui.End()
}
