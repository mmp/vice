// simserver.go

package main

/*
TODO:

- think about pause option

- config scenarios via json files (directory of them?)

- checkboxes to select which spawners to use, configure them...
  enabled bool?
  spec w.r.t ADR -- avg departure rate
  have a 50/50 of launching to the same gate (or make configurable)

- fix LGA spawn prop/non prop overlaps

- go faster when higher... (not just 10k faster.)

- callsign formats... @ stuff for letters...

- winds (IAS vs ground speed...)
*/

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

type Simulator interface {
	AssignAltitude(callsign string, altitude int) error
	AssignHeading(callsign string, heading int) error
	DirectFix(callsign string, fix string) error
	PrintInfo(callsign string) error
	DeleteAircraft(callsign string) error
	TogglePause() error
}

func parseLatLong(l string) float32 {
	if l[0] != 'N' && l[0] != 'S' && l[0] != 'E' && l[0] != 'W' {
		panic("bad lat long")
	}

	bytes := []byte(l)
	idx := 1
	parseInt := func() (value int, digits int) {
		for idx < len(bytes) && bytes[idx] != '.' {
			value *= 10
			digit := int(bytes[idx] - '0')
			if digit < 0 || digit > 9 {
				panic("bad lat long")
			}
			value += digit
			idx++
			digits++
		}
		return
	}

	// Get the whole degrees up to the first "."
	value, _ := parseInt()
	ll := float64(value)
	idx++ // skip .
	value, _ = parseInt()
	ll += float64(value) / 60
	idx++ // skip .
	value, _ = parseInt()
	ll += float64(value) / 3600
	idx++ // skip .
	value, digits := parseInt()
	for digits < 3 {
		digits++
		value *= 10
	}
	ll += float64(value) / 3600000

	if l[0] == 'S' || l[0] == 'W' {
		ll = -ll
	}
	return float32(ll)
}

var configPositions map[string]Point2LL = map[string]Point2LL{
	"_JFK_31L": Point2LL{parseLatLong("W073.46.20.227"), parseLatLong("N040.37.41.000")},
	"_JFK_31R": Point2LL{parseLatLong("W073.45.34.963"), parseLatLong("N040.38.36.961")},
	"_JFK_22R": Point2LL{parseLatLong("W073.45.49.053"), parseLatLong("N040.39.00.362")},
	"_JFK_22L": Point2LL{parseLatLong("W073.45.18.511"), parseLatLong("N040.38.41.232")},
	"_JFK_4L":  Point2LL{parseLatLong("W073.47.08.045"), parseLatLong("N040.37.19.370")},
	"_JFK_4La": Point2LL{parseLatLong("W073.45.32.849"), parseLatLong("N040.39.21.332")}, // turn for 4L deps
	"_JFK_4R":  Point2LL{parseLatLong("W073.46.12.894"), parseLatLong("N040.37.31.661")},
	"_JFK_13R": Point2LL{parseLatLong("W073.49.00.188"), parseLatLong("N040.38.53.537")},
	"_JFK_13L": Point2LL{parseLatLong("W073.47.24.277"), parseLatLong("N040.39.26.976")},

	"_KFRG_0":  Point2LL{parseLatLong("W073.24.56.035"), parseLatLong("N040.45.12.277")},
	"_KFRG_0a": Point2LL{parseLatLong("W073.25.03.149"), parseLatLong("N040.46.18.469")},
	"_KFRG_1":  Point2LL{parseLatLong("W073.23.27.925"), parseLatLong("N040.42.51.432")},
	"_KFRG_1a": Point2LL{parseLatLong("W073.22.33.158"), parseLatLong("N040.42.12.417")},
	"_KFRG_2":  Point2LL{parseLatLong("W073.24.46.395"), parseLatLong("N040.42.15.026")},
	"_KFRG_2a": Point2LL{parseLatLong("W073.24.34.584"), parseLatLong("N040.39.58.466")},
	"_KFRG_3":  Point2LL{parseLatLong("W073.26.23.706"), parseLatLong("N040.44.49.233")},
	"_KFRG_3a": Point2LL{parseLatLong("W073.27.58.683"), parseLatLong("N040.45.56.716")},

	"_KISP_CLIMB": Point2LL{parseLatLong("W073.02.06.672"), parseLatLong("N040.49.24.523")},
	"_KISP_HO":    Point2LL{parseLatLong("W073.11.01.019"), parseLatLong("N040.47.39.411")},
	"_KLGA_CLIMB": Point2LL{parseLatLong("W073.46.46.402"), parseLatLong("N040.48.36.210")},
	"_KLGA_HO":    Point2LL{parseLatLong("W073.45.41.940"), parseLatLong("N040.45.07.388")},
}

type SimSpawnerConfig struct {
	adr       int32
	challenge float32
	enabled   bool
	create    func(c *SimSpawnerConfig, now time.Time) *SimSpawner
}

type SimServerConnectionConfiguration struct {
	spawnRate      int32
	spawnRateDelta int32
	simRate        float32
	configs        map[string]*SimSpawnerConfig
}

func (ssc *SimServerConnectionConfiguration) Initialize() {
	ssc.spawnRate = 120
	ssc.spawnRateDelta = 15
	ssc.simRate = 1
	ssc.configs = make(map[string]*SimSpawnerConfig)
	ssc.configs["JFK31L"] = &SimSpawnerConfig{adr: 45, challenge: 0.5, create: NewJFK31LSimSpawner, enabled: true}
	ssc.configs["JFK31R"] = &SimSpawnerConfig{adr: 10, challenge: 0.5, create: NewJFK31RSimSpawner}
	ssc.configs["JFK22R"] = &SimSpawnerConfig{adr: 45, challenge: 0.5, create: NewJFK22RSimSpawner}
	ssc.configs["JFK13R"] = &SimSpawnerConfig{adr: 45, challenge: 0.5, create: NewJFK13RSimSpawner}
	ssc.configs["JFK4L"] = &SimSpawnerConfig{adr: 45, challenge: 0.5, create: NewJFK4LSimSpawner}
	ssc.configs["FRG"] = &SimSpawnerConfig{adr: 15, challenge: 0.5, create: NewKFRGSimSpawner, enabled: true}
	ssc.configs["ISP"] = &SimSpawnerConfig{adr: 15, challenge: 0.5, create: NewKISPSimSpawner, enabled: true}
	ssc.configs["LGA"] = &SimSpawnerConfig{adr: 20, challenge: 0.5, create: NewKLGASimSpawner, enabled: true}
	ssc.configs["LGA Prop"] = &SimSpawnerConfig{adr: 5, challenge: 0.5, create: NewKLGAPropSimSpawner}
}

func (ssc *SimServerConnectionConfiguration) DrawUI() bool {
	imgui.InputText("Callsign", &positionConfig.VatsimCallsign)

	imgui.SliderFloatV("Simulation rate", &ssc.simRate, 0.25, 10, "%.1f", 0)

	imgui.Text("Configs:")
	flags := imgui.TableFlagsBordersV | imgui.TableFlagsBordersOuterH | imgui.TableFlagsRowBg | imgui.TableFlagsSizingStretchProp
	if imgui.BeginTableV("configs", 4, flags, imgui.Vec2{600, 0}, 0.) {
		imgui.TableSetupColumn("Enabled")
		imgui.TableSetupColumn("Name")
		imgui.TableSetupColumn("ADR")
		imgui.TableSetupColumn("Challenge level")
		imgui.TableHeadersRow()
		for _, conf := range SortedMapKeys(ssc.configs) {
			imgui.PushID(conf)
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.Checkbox("##check", &ssc.configs[conf].enabled)
			imgui.TableNextColumn()
			imgui.Text(conf)
			imgui.TableNextColumn()
			imgui.InputIntV("##adr", &ssc.configs[conf].adr, 1, 120, 0)
			imgui.TableNextColumn()
			imgui.SliderFloatV("##challenge", &ssc.configs[conf].challenge, 0, 1, "%.01f", 0)
			imgui.PopID()
		}
		imgui.EndTable()
	}

	return false
}

func (*SimServerConnectionConfiguration) Valid() bool { return positionConfig.VatsimCallsign != "" }

func (ssc *SimServerConnectionConfiguration) Connect() error {
	server = NewSimServer(positionConfig.VatsimCallsign, *ssc)
	return nil
}

//go:embed resources/openscope-aircraft.json
var openscopeAircraft string

type SimAircraft struct {
	Name string `json:"name"`
	ICAO string `json:"icao"`
	// engines, weight class, category
	WeightClass string `json:"weightClass"`
	Ceiling     int    `json:"ceiling"`
	Rate        struct {
		Climb      int     `json:"climb"` // ft / minute; reduce by 500 after alt 5000 if this is >=2500
		Descent    int     `json:"descent"`
		Accelerate float32 `json:"accelerate"` // kts / 2 seconds
		Decelerate float32 `json:"decelerate"`
	} `json:"rate"`
	Runway struct {
		Takeoff float32 `json:"takeoff"` // nm
		Landing float32 `json:"landing"` // nm
	} `json:"runway"`
	Speed struct {
		Min     int `json:"min"`
		Landing int `json:"landing"`
		Cruise  int `json:"cruise"`
		Max     int `json:"max"`
	} `json:"speed"`
}

//go:embed resources/openscope-airlines.json
var openscopeAirlines string

type SimAirline struct {
	ICAO string `json:"icao"`
	Name string `json:"name"`
	// TODO: callsignFormats...
	JSONFleets map[string][][2]interface{} `json:"fleets"`
	Fleets     map[string][]FleetAircraft
}

type FleetAircraft struct {
	ICAO  string
	Count int
}

var AllSimAircraft map[string]*SimAircraft
var AllSimAirlines map[string]*SimAirline

type SSAircraft struct {
	AC    *Aircraft
	SimAC *SimAircraft
	Strip FlightStrip
	Route []string

	Position Point2LL
	Heading  float32
	Altitude float32
	Airspeed float32

	AssignedAltitude int

	AssignedHeading *int
	TurnDirection   *int
}

type SimServer struct {
	callsign    string
	aircraft    map[string]*SSAircraft
	handoffs    map[string]time.Time
	controllers map[string]*Controller

	startTime time.Time
	simRate   float32

	lastTrackUpdate time.Time
	lastSimUpdate   time.Time

	spawners []*SimSpawner
}

func NewSimServer(callsign string, ssc SimServerConnectionConfiguration) *SimServer {
	rand.Seed(time.Now().UnixNano())

	var acStruct struct {
		Aircraft []SimAircraft `json:"aircraft"`
	}
	if err := json.Unmarshal([]byte(openscopeAircraft), &acStruct); err != nil {
		lg.Errorf("%v", err)
	}

	AllSimAircraft = make(map[string]*SimAircraft)
	for i, ac := range acStruct.Aircraft {
		AllSimAircraft[ac.ICAO] = &acStruct.Aircraft[i]
	}

	var alStruct struct {
		Airlines []SimAirline `json:"airlines"`
	}
	if err := json.Unmarshal([]byte(openscopeAirlines), &alStruct); err != nil {
		lg.Errorf("%v", err)
	}
	// Fix up the fleets...
	AllSimAirlines = make(map[string]*SimAirline)
	for _, al := range alStruct.Airlines {
		fixedAirline := al
		fixedAirline.Fleets = make(map[string][]FleetAircraft)
		for name, aircraft := range fixedAirline.JSONFleets {
			for _, ac := range aircraft {
				fleetAC := FleetAircraft{
					ICAO:  strings.ToUpper(ac[0].(string)),
					Count: int(ac[1].(float64)),
				}
				if _, ok := AllSimAircraft[fleetAC.ICAO]; !ok {
					lg.Errorf("%s: unknown aircraft in airlines database", fleetAC.ICAO)
				}
				fixedAirline.Fleets[name] = append(fixedAirline.Fleets[name], fleetAC)
			}
		}
		fixedAirline.JSONFleets = nil

		AllSimAirlines[strings.ToUpper(al.ICAO)] = &fixedAirline
	}

	ss := &SimServer{
		callsign:    callsign,
		aircraft:    make(map[string]*SSAircraft),
		handoffs:    make(map[string]time.Time),
		controllers: make(map[string]*Controller),
		startTime:   time.Now().Add(-60 * time.Second), // hack, part 1 to prime the sim pump...
		simRate:     ssc.simRate,
	}

	now := ss.startTime
	for _, config := range ssc.configs { // FIXME: "config" overload...
		if config.enabled {
			ss.spawners = append(ss.spawners, config.create(config, now))
		}
	}

	addController := func(cs string, loc string, freq float32) {
		pos, _ := database.Locate(loc)
		ss.controllers[cs] = &Controller{
			Callsign:  cs,
			Location:  pos,
			Frequency: NewFrequency(freq),
		}
	}

	// Us.
	addController("JFK_DEP", "KJFK", 135.9) //  2A

	addController("LGA_DEP", "KLGA", 120.4)     //  1L
	addController("ISP_APP", "KISP", 120.05)    //  3H
	addController("JFK_APP", "KJFK", 132.4)     //  2A
	addController("NY_LE_DEP", "MERIT", 126.8)  //  5E
	addController("NY_LS_DEP", "DIXIE", 124.75) //  5S
	addController("NY_F_CTR", "KEWR", 128.3)    // N66
	addController("BOS_E_CTR", "KBOS", 133.45)  // B17

	for _, spawner := range ss.spawners {
		spawner.MaybeSpawn(ss)
	}
	for i := 0; i < 60; i++ {
		ss.updateSim()
	}

	return ss
}

func (ss *SimServer) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetScratchpad(callsign string, scratchpad string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.Scratchpad = scratchpad
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) SetTemporaryAltitude(callsign string, alt int) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetVoiceType(callsign string, voice VoiceCapability) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) PushFlightStrip(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) InitiateTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != "" {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.TrackingController = ss.callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) DropTrack(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.TrackingController = ""
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) Handoff(callsign string, controller string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else if ctrl := ss.GetController(controller); ctrl == nil {
		return ErrNoController
	} else {
		ac.AC.OutboundHandoffController = ctrl.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		acceptDelay := 2 + rand.Intn(10)
		ss.handoffs[callsign] = ss.CurrentTime().Add(time.Duration(acceptDelay) * time.Second)
		return nil
	}
}

func (ss *SimServer) AcceptHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.InboundHandoffController != ss.callsign {
		return ErrNotBeingHandedOffToMe
	} else {
		ac.AC.InboundHandoffController = ""
		ac.AC.TrackingController = ss.callsign
		eventStream.Post(&AcceptedHandoffEvent{controller: ss.callsign, ac: ac.AC})
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC}) // FIXME...
		return nil
	}
}

func (ss *SimServer) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) CancelHandoff(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else if ac.AC.TrackingController != ss.callsign {
		return ErrOtherControllerHasTrack
	} else {
		ac.AC.OutboundHandoffController = ""
		// TODO: we are inconsistent in other control backends about events
		// when user does things like this; sometimes no event, sometimes
		// modified a/c event...
		eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		return nil
	}
}

func (ss *SimServer) PointOut(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SendTextMessage(m TextMessage) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) RequestControllerATIS(controller string) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	return nil // UNIMPLEMENTED
}

func (ss *SimServer) Disconnect() {
}

func (ss *SimServer) GetAircraft(callsign string) *Aircraft {
	if ac, ok := ss.aircraft[callsign]; ok {
		return ac.AC
	}
	return nil
}

func (ss *SimServer) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range ss.aircraft {
		if filter(ac.AC) {
			filtered = append(filtered, ac.AC)
		}
	}
	return filtered
}

func (ss *SimServer) GetAllAircraft() []*Aircraft {
	return ss.GetFilteredAircraft(func(*Aircraft) bool { return true })
}

func (ss *SimServer) GetFlightStrip(callsign string) *FlightStrip {
	if ac, ok := ss.aircraft[callsign]; ok {
		return &ac.Strip
	}
	return nil
}

func (ss *SimServer) AddAirportForWeather(airport string) {
	// UNIMPLEMENTED
}

func (ss *SimServer) GetMETAR(location string) *METAR {
	// UNIMPLEMENTED
	return nil
}

func (ss *SimServer) GetAirportATIS(airport string) []ATIS {
	// UNIMPLEMENTED
	return nil
}

func (ss *SimServer) GetUser(callsign string) *User {
	// UNIMPLEMENTED
	return nil
}

func (ss *SimServer) GetController(callsign string) *Controller {
	if ctrl, ok := ss.controllers[callsign]; ok {
		return ctrl
	}
	for _, ctrl := range ss.controllers {
		if pos := ctrl.GetPosition(); pos != nil && pos.SectorId == callsign {
			return ctrl
		}
	}
	return nil
}

func (ss *SimServer) GetAllControllers() []*Controller {
	_, ctrl := FlattenMap(ss.controllers)
	return ctrl
}

func (ss *SimServer) SetPrimaryFrequency(f Frequency) {
	// UNIMPLEMENTED
}

func (ss *SimServer) GetUpdates() {
	// Accept any handoffs whose time has time...
	now := ss.CurrentTime()
	for callsign, t := range ss.handoffs {
		if now.After(t) {
			ac := ss.aircraft[callsign].AC
			ac.TrackingController = ac.OutboundHandoffController
			ac.OutboundHandoffController = ""
			eventStream.Post(&AcceptedHandoffEvent{controller: ac.TrackingController, ac: ac})
			globalConfig.AudioSettings.HandleEvent(AudioEventHandoffAccepted)
			delete(ss.handoffs, callsign)
		}
	}

	if now.Sub(ss.lastSimUpdate) >= time.Second {
		ss.lastSimUpdate = now
		ss.updateSim()
	}

	if now.Sub(ss.lastTrackUpdate) >= 5*time.Second {
		ss.lastTrackUpdate = now

		for _, ac := range ss.aircraft {
			ac.AC.AddTrack(RadarTrack{
				Position:    ac.Position,
				Altitude:    int(ac.Altitude),
				Groundspeed: int(ac.Airspeed),
				Heading:     ac.Heading - database.MagneticVariation,
				Time:        now,
			})

			eventStream.Post(&ModifiedAircraftEvent{ac: ac.AC})
		}
	}

	for _, spawner := range ss.spawners {
		spawner.MaybeSpawn(ss)
	}
}

func (ss *SimServer) updateSim() {
	for _, ac := range ss.aircraft {
		//lg.Printf("%+v", ac)

		// Time for a handoff?
		if len(ac.Route) > 0 && ac.Route[0] == "@" {
			ac.AC.InboundHandoffController = ss.callsign
			eventStream.Post(&OfferedHandoffEvent{controller: ac.AC.TrackingController, ac: ac.AC})
			ac.Route = ac.Route[1:]
		}

		// Update speed; only worry about accelerate for departures (until
		// we have speed assignments at least...)
		targetSpeed := ac.SimAC.Speed.Cruise
		if ac.Altitude < 10000 {
			targetSpeed = min(targetSpeed, 250)
		}
		if ac.Airspeed < float32(targetSpeed) {
			accel := ac.SimAC.Rate.Accelerate / 2 // Accel is given in "per 2 seconds..."
			ac.Airspeed = min(float32(targetSpeed), ac.Airspeed+accel)
		}

		// Don't climb if it isn't going fast enough to fly
		if ac.Airspeed >= 1.1*float32(ac.SimAC.Speed.Min) {
			if ac.Altitude < float32(ac.AssignedAltitude) {
				climb := ac.SimAC.Rate.Climb
				if climb >= 2500 && ac.Altitude > 5000 {
					climb -= 500
				}
				ac.Altitude = min(float32(ac.AssignedAltitude), ac.Altitude+float32(climb)/60)
			} else if ac.Altitude > float32(ac.AssignedAltitude) {
				ac.Altitude = max(float32(ac.AssignedAltitude), ac.Altitude-float32(ac.SimAC.Rate.Descent)/60)
			}
		}

		// Figure out the heading; if the route is empty, just leave it
		// on its current heading...
		targetHeading := ac.Heading
		turn := float32(0)
		if ac.AssignedHeading != nil {
			targetHeading = float32(*ac.AssignedHeading)
			if ac.TurnDirection != nil {
				if *ac.TurnDirection < 0 { // left
					diff := ac.Heading - targetHeading
					if diff < 0 {
						diff += 360
					}
					diff = min(diff, 3)
					turn = -diff
				} else if *ac.TurnDirection > 0 { // right
					diff := targetHeading - ac.Heading
					if diff < 0 {
						diff += 360
					}
					diff = min(diff, 3)
					turn = diff
				}
				//lg.Errorf("From %f to %f, turn %f", ac.Heading, targetHeading, turn)
			}
		} else if len(ac.Route) > 0 {
			if ac.Route[0][0] == '#' {
				hdg, err := strconv.ParseFloat(ac.Route[0][1:], 32)
				if err != nil {
					lg.Errorf("%s: %v", ac.Route[0], err)
				}
				targetHeading = float32(hdg)
			} else {
				var pos Point2LL
				var ok bool
				if pos, ok = database.Locate(ac.Route[0]); !ok {
					if pos, ok = configPositions[ac.Route[0]]; !ok {
						lg.Errorf("%s: unknown route position", ac.Route[0])
						continue
					}
				}
				targetHeading = headingp2ll(ac.Position, pos, database.MagneticVariation)

				//lg.Printf("cur %f target %f", ac.Heading, targetHeading)

				// Have we passed the fix?
				if nmdistance2ll(ac.Position, pos) < .5 {
					//lg.Errorf("%s: CALLING IT THAT WE PASSED IT", ac.Route[0])
					ac.Route = ac.Route[1:]
					//lg.Errorf("New route: %v", ac.Route)
					targetHeading = ac.Heading // keep it sensible until next time through
				}
			}
		}

		if turn == 0 {
			// figure out which way is closest.

			// First find the angle to rotate the target heading by so
			// that it's aligned with 180 degrees. This lets us not
			// worry about the 0/360 wrap around complexities..
			rot := 180 - targetHeading
			if rot < 0 {
				rot += 360
			}
			cur := mod(ac.Heading+rot, 360) // w.r.t. 180 target
			turn = clamp(180-cur, -3, 3)    // max 3 degrees / second
			//lg.Printf("rot %f, rel cur %f -> computed turn %f", rot, cur, turn)
		}

		if ac.Heading != targetHeading {
			ac.Heading += turn
		}

		// Update position given current heading
		hdg := ac.Heading - database.MagneticVariation
		v := [2]float32{sin(radians(hdg)), cos(radians(hdg))}
		ac.Position = nm2ll(add2f(ll2nm(ac.Position), scale2f(v, ac.Airspeed/3600)))
	}
}

func (ss *SimServer) Connected() bool {
	return true
}

func (ss *SimServer) Callsign() string {
	return ss.callsign
}

func (ss *SimServer) CurrentTime() time.Time {
	elapsed := time.Since(ss.startTime)
	elapsed = time.Duration(float64(elapsed) * float64(ss.simRate))
	return ss.startTime.Add(elapsed)
}

func (ss *SimServer) GetWindowTitle() string {
	return "SimServer: " + ss.callsign
}

func (ss *SimServer) SpawnAircraft(ac *Aircraft, alt int, altAssigned int, speed int) {
	acInfo, ok := AllSimAircraft[ac.FlightPlan.BaseType()]
	if !ok {
		lg.Errorf("%s: ICAO not in db", ac.FlightPlan.BaseType())
		return
	}

	ssa := &SSAircraft{
		AC:    ac,
		SimAC: acInfo,
		Route: strings.Split(ac.FlightPlan.Route, "."),

		Altitude:         float32(alt),
		AssignedAltitude: altAssigned,
		Airspeed:         float32(speed),
	}

	if _, ok := ss.aircraft[ssa.AC.Callsign]; ok {
		lg.Errorf("%s: already have an aircraft with that callsign!", ssa.AC.Callsign)
		return
	}

	var pos0, pos1 Point2LL
	if pos0, ok = database.Locate(ssa.Route[0]); !ok {
		if pos0, ok = configPositions[ssa.Route[0]]; !ok {
			lg.Errorf("%s: unknown initial route position", ssa.Route[0])
			return
		}
	}
	ssa.Position = pos0

	if ssa.Route[1][0] == '#' {
		hdg, err := strconv.ParseFloat(ssa.Route[1][1:], 32)
		if err != nil {
			lg.Errorf("%s: %v", ssa.Route[1], err)
		}
		ssa.Heading = float32(hdg)
	} else {
		if pos1, ok = database.Locate(ssa.Route[1]); !ok {
			if pos1, ok = configPositions[ssa.Route[1]]; !ok {
				lg.Errorf("%s: unknown route position", ssa.Route[1])
				return
			}
		}
		ssa.Heading = headingp2ll(pos0, pos1, database.MagneticVariation)
	}

	// Take off the initial point to maintain the invariant that the first
	// item in the route is what we're following..
	ssa.Route = ssa.Route[1:]

	ss.aircraft[ac.Callsign] = ssa

	eventStream.Post(&AddedAircraftEvent{ac: ssa.AC})
}

func pilotResponse(callsign string, fm string, args ...interface{}) {
	tm := TextMessage{
		sender:      callsign,
		messageType: TextBroadcast,
		contents:    fmt.Sprintf(fm, args...),
	}
	eventStream.Post(&TextMessageEvent{message: &tm})
}

func (ss *SimServer) AssignAltitude(callsign string, altitude int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if float32(altitude) > ac.Altitude {
			pilotResponse(callsign, "climb and maintain %d", altitude)
		} else if float32(altitude) == ac.Altitude {
			pilotResponse(callsign, "maintain %d", altitude)
		} else {
			pilotResponse(callsign, "descend and maintain %d", altitude)
		}

		ac.AssignedAltitude = altitude
		return nil
	}
}

func (ss *SimServer) AssignHeading(callsign string, heading int, turn int) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		if turn > 0 {
			pilotResponse(callsign, "turn right heading %d", heading)
		} else if turn == 0 {
			pilotResponse(callsign, "fly heading %d", heading)
		} else {
			pilotResponse(callsign, "turn left heading %d", heading)
		}

		ac.AssignedHeading = &heading
		if turn != 0 {
			ac.TurnDirection = &turn
		} else {
			ac.TurnDirection = nil
		}
		return nil
	}
}

func (ss *SimServer) DirectFix(callsign string, fix string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		fix = strings.ToUpper(fix)
		for i, f := range ac.Route {
			if f == fix {
				ac.Route = ac.Route[i:]
				ac.AssignedHeading = nil
				ac.TurnDirection = nil
				pilotResponse(callsign, "direct %s", fix)
				return nil
			}
		}
		return fmt.Errorf("%s: fix not found in route", fix)
	}
}

func (ss *SimServer) PrintInfo(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		s := fmt.Sprintf("%s: assigned alt %d", ac.AC.Callsign, ac.AssignedAltitude)
		if ac.AssignedHeading != nil {
			s += fmt.Sprintf(" heading %d", *ac.AssignedHeading)
			if ac.TurnDirection != nil {
				s += fmt.Sprintf(" turn direction %d", *ac.TurnDirection)
			}
		}
		s += fmt.Sprintf(", route %+v", ac.Route)
		lg.Errorf("%s", s)
	}
	return nil
}

func (ss *SimServer) DeleteAircraft(callsign string) error {
	if ac, ok := ss.aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		eventStream.Post(&RemovedAircraftEvent{ac: ac.AC})
		delete(ss.aircraft, callsign)
		return nil
	}
}

func (ss *SimServer) TogglePause() error {
	return nil
}

///////////////////////////////////////////////////////////////////////////

type SimSpawner struct {
	nextSpawn time.Time

	adr       int
	challenge float32

	departureAirport  string
	initialController string

	airlines []string
	fleet    string
	routes   map[string][]*Route

	lastRouteCategory string
	lastRoute         *Route
}

type Route struct {
	Waypoints       string
	Scratchpad      string
	InitialAltitude int
	ClearedAltitude int
	InitialSpeed    int
	Destinations    []string
}

func (s *SimSpawner) AddRoute(name string, r *Route) {
	if s.routes == nil {
		s.routes = make(map[string][]*Route)
	}
	s.routes[name] = append(s.routes[name], r)
}

func (s *SimSpawner) MaybeSpawn(ss *SimServer) {
	if ss.CurrentTime().Before(s.nextSpawn) {
		return
	}

	// Pick an airline; go randomizes iteration, so there ya go...
	al := s.airlines[rand.Intn(len(s.airlines))]
	airline, ok := AllSimAirlines[al]
	if !ok {
		lg.Errorf("%s: unknown airline!", al)
		return
	}
	// lg.Printf("AIRLINE %+v", airline)

	callsign := strings.ToUpper(airline.ICAO) + fmt.Sprintf("%d", rand.Intn(1999))

	// Pick an aircraft.
	var aircraft FleetAircraft
	count := 0

	fleet, ok := airline.Fleets[s.fleet]
	if !ok {
		lg.Errorf("%s: didn't find fleet %s -- %+v", airline.ICAO, s.fleet, airline)
		for _, fl := range airline.Fleets {
			fleet = fl
			break
		}
	}

	for _, ac := range fleet {
		// Reservoir sampling...
		count += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(count) {
			aircraft = ac
		}
	}
	//lg.Printf("AC %+v", aircraft)

	if _, ok := AllSimAircraft[aircraft.ICAO]; !ok {
		lg.Errorf("%s: chose aircraft but not in DB!", aircraft.ICAO)
		return // try again next time...
	}

	// pick a route...
	// equal chance of reuse last route, reuse last exit, brand new route
	var route *Route
	u := rand.Float32()
	if u < s.challenge/2 {
		route = s.lastRoute // note: may be nil the first time...
	} else if u < s.challenge {
		if routes, ok := s.routes[s.lastRouteCategory]; ok {
			route = routes[rand.Intn(len(routes))]
		}
	}

	// Either the challenge cases didn't hit or they did and it's the first
	// time through...
	if route == nil {
		for category, routes := range s.routes {
			// yaay for randomized map iteration
			route = routes[rand.Intn(len(routes))]
			s.lastRouteCategory = category
			break
		}
	}
	s.lastRoute = route

	// Pick a destination airport
	destination := route.Destinations[rand.Intn(len(route.Destinations))]

	squawk := Squawk(rand.Intn(0o7000))
	ac := &Aircraft{
		Callsign:           callsign,
		Scratchpad:         route.Scratchpad,
		AssignedSquawk:     squawk,
		Squawk:             squawk,
		Mode:               Charlie,
		VoiceCapability:    VoiceFull,
		TrackingController: s.initialController,
		FlightPlan: &FlightPlan{
			Rules:            IFR,
			AircraftType:     aircraft.ICAO,
			DepartureAirport: s.departureAirport,
			ArrivalAirport:   destination,
			Route:            route.Waypoints,
		},
	}

	acInfo, ok := AllSimAircraft[aircraft.ICAO]
	if !ok {
		lg.Errorf("%s: ICAO not in db", aircraft.ICAO)
		return
	}
	if acInfo.WeightClass == "H" {
		ac.FlightPlan.AircraftType = "H/" + ac.FlightPlan.AircraftType
	}
	if acInfo.WeightClass == "J" {
		ac.FlightPlan.AircraftType = "J/" + ac.FlightPlan.AircraftType
	}

	ss.SpawnAircraft(ac, route.InitialAltitude, route.ClearedAltitude, route.InitialSpeed)

	seconds := 3600/s.adr - 10 + rand.Intn(21)
	s.nextSpawn = ss.CurrentTime().Add(time.Duration(seconds) * time.Second)
}

var jfkWater = [][2]string{
	[2]string{"WAVEY", "WAV"},
	[2]string{"SHIPP", "SHI"},
	[2]string{"HAPIE", "HAP"},
	[2]string{"BETTE", "BET"},
}

var jfkEast = [][2]string{
	[2]string{"MERIT", "MER"},
	[2]string{"GREKI", "GRE"},
	[2]string{"BAYYS", "BAY"},
	[2]string{"BDR", "BDR"},
}

var jfkSouthWest = [][2]string{
	[2]string{"DIXIE", "DIX"},
	[2]string{"WHITE", "WHI"},
	[2]string{"RBV", "RBV"},
	[2]string{"ARD", "ARD"},
}

var jfkNorth = [][2]string{
	//[2]string{"SAX", "SAX"},
	[2]string{"COATE", "COA"},
	[2]string{"NEION", "NEI"},
	[2]string{"HAAYS", "HAY"},
	[2]string{"GAYEL", "GAY"},
	[2]string{"DEEZZ", "DEZ"},
}

func NewJFK31LSimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:        now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport: "KJFK",
		adr:              int(c.adr),
		challenge:        c.challenge,
		fleet:            "default",
		airlines: []string{
			"AAL", "AFR", "AIC", "AMX", "ANA", "ASA", "BAW", "BWA", "CCA", "CLX", "CPA", "DAL", "DLH", "EDV", "EIN",
			"ELY", "FDX", "FFT", "GEC", "IBE", "JBU", "KAL", "KLM", "LXJ", "NKS", "QXE", "SAS", "UAE", "UAL", "UPS"},
	}

	for _, exit := range jfkWater {
		ss.AddRoute("water", &Route{
			Waypoints:       "_JFK_31L._JFK_13R.CRI.#176." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"TAPA", "TXKF", "KMCO", "KFLL", "KSAV", "KATL", "EGLL", "EDDF", "LFPG", "EINN",
			},
		})
	}
	for _, exit := range jfkEast {
		ss.AddRoute("east", &Route{
			Waypoints:       "_JFK_31L._JFK_13R.CRI.#176." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KBOS", "KPVD", "KACK", "KBDL", "KPWM", "KSYR",
			},
		})
	}
	for _, exit := range jfkNorth {
		ss.AddRoute("north", &Route{
			Waypoints:       "_JFK_31L._JFK_13R.CRI.#176." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW",
			},
		})
	}
	for _, exit := range jfkSouthWest {
		ss.AddRoute("sw", &Route{
			Waypoints:       "_JFK_31L._JFK_13R.CRI.#223." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL",
			},
		})
	}

	return ss
}

func NewJFK31RSimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:        now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport: "KJFK",
		adr:              int(c.adr),
		challenge:        c.challenge,
		airlines:         []string{"N"},
		fleet:            "lightGA",
	}

	for _, exit := range jfkWater {
		ss.AddRoute("water", &Route{
			Waypoints:       "_JFK_31R._JFK_13L.#090." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 2000,
			InitialSpeed:    0,
			Destinations: []string{
				"TAPA", "TXKF", "KMCO", "KFLL", "KSAV", "KATL", "EGLL", "EDDF", "LFPG", "EINN",
			},
		})
	}
	for _, exit := range jfkEast {
		ss.AddRoute("east", &Route{
			Waypoints:       "_JFK_31R._JFK_13L.#090." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 2000,
			InitialSpeed:    0,
			Destinations: []string{
				"KBOS", "KPVD", "KACK", "KBDL", "KPWM", "KSYR",
			},
		})
	}
	for _, exit := range jfkNorth {
		ss.AddRoute("north", &Route{
			Waypoints:       "_JFK_31R._JFK_13L.#090." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 2000,
			InitialSpeed:    0,
			Destinations: []string{
				"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW",
			},
		})
	}

	// Southwest gates go from 31L, SKORR4...

	return ss
}

func NewJFK22RSimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:        now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport: "KJFK",
		adr:              int(c.adr),
		challenge:        c.challenge,
		fleet:            "default",
		airlines: []string{
			"AAL", "AFR", "AIC", "AMX", "ANA", "ASA", "BAW", "BWA", "CCA", "CLX", "CPA", "DAL", "DLH", "EDV", "EIN",
			"ELY", "FDX", "FFT", "GEC", "IBE", "JBU", "KAL", "KLM", "LXJ", "NKS", "QXE", "SAS", "UAE", "UAL", "UPS"},
	}

	for _, exit := range jfkWater {
		ss.AddRoute("water", &Route{
			Waypoints:       "_JFK_22R._JFK_4L.#222." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"TAPA", "TXKF", "KMCO", "KFLL", "KSAV", "KATL", "EGLL", "EDDF", "LFPG", "EINN",
			},
		})
	}
	for _, exit := range jfkEast {
		ss.AddRoute("east", &Route{
			Waypoints:       "_JFK_22R._JFK_4L.#222." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KBOS", "KPVD", "KACK", "KBDL", "KPWM", "KSYR",
			},
		})
	}
	for _, exit := range jfkNorth {
		ss.AddRoute("north", &Route{
			Waypoints:       "_JFK_22R._JFK_4L.#222." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW",
			},
		})
	}
	for _, exit := range jfkSouthWest {
		ss.AddRoute("sw", &Route{
			Waypoints:       "_JFK_22R._JFK_4L.#222." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL",
			},
		})
	}

	return ss
}

func NewJFK13RSimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:        now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport: "KJFK",
		adr:              int(c.adr),
		challenge:        c.challenge,
		fleet:            "default",
		airlines: []string{
			"AAL", "AFR", "AIC", "AMX", "ANA", "ASA", "BAW", "BWA", "CCA", "CLX", "CPA", "DAL", "DLH", "EDV", "EIN",
			"ELY", "FDX", "FFT", "GEC", "IBE", "JBU", "KAL", "KLM", "LXJ", "NKS", "QXE", "SAS", "UAE", "UAL", "UPS"},
	}

	for _, exit := range jfkWater {
		ss.AddRoute("water", &Route{
			Waypoints:       "_JFK_13R._JFK_31L.#109." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"TAPA", "TXKF", "KMCO", "KFLL", "KSAV", "KATL", "EGLL", "EDDF", "LFPG", "EINN",
			},
		})
	}
	for _, exit := range jfkEast {
		ss.AddRoute("east", &Route{
			Waypoints:       "_JFK_13R._JFK_31L.#109." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KBOS", "KPVD", "KACK", "KBDL", "KPWM", "KSYR",
			},
		})
	}
	for _, exit := range jfkNorth {
		ss.AddRoute("north", &Route{
			Waypoints:       "_JFK_13R._JFK_31L.#109." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW",
			},
		})
	}
	for _, exit := range jfkSouthWest {
		ss.AddRoute("sw", &Route{
			Waypoints:       "_JFK_13R._JFK_31L.#109." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL",
			},
		})
	}

	return ss
}

func NewJFK4LSimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:        now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport: "KJFK",
		adr:              int(c.adr),
		challenge:        c.challenge,
		fleet:            "default",
		airlines: []string{
			"AAL", "AFR", "AIC", "AMX", "ANA", "ASA", "BAW", "BWA", "CCA", "CLX", "CPA", "DAL", "DLH", "EDV", "EIN",
			"ELY", "FDX", "FFT", "GEC", "IBE", "JBU", "KAL", "KLM", "LXJ", "NKS", "QXE", "SAS", "UAE", "UAL", "UPS"},
	}

	for _, exit := range jfkWater {
		ss.AddRoute("water", &Route{
			Waypoints:       "_JFK_4L._JFK_4La.#099." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"TAPA", "TXKF", "KMCO", "KFLL", "KSAV", "KATL", "EGLL", "EDDF", "LFPG", "EINN",
			},
		})
	}
	for _, exit := range jfkEast {
		ss.AddRoute("east", &Route{
			Waypoints:       "_JFK_4L._JFK_4La.#099." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KBOS", "KPVD", "KACK", "KBDL", "KPWM", "KSYR",
			},
		})
	}
	for _, exit := range jfkNorth {
		ss.AddRoute("north", &Route{
			Waypoints:       "_JFK_4L._JFK_4La.#099." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW",
			},
		})
	}
	for _, exit := range jfkSouthWest {
		ss.AddRoute("sw", &Route{
			Waypoints:       "_JFK_4L._JFK_4La.#099." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 13,
			ClearedAltitude: 5000,
			InitialSpeed:    0,
			Destinations: []string{
				"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL",
			},
		})
	}

	return ss
}

func NewKFRGSimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:         now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport:  "KFRG",
		adr:               int(c.adr),
		fleet:             "default",
		initialController: "JFK_APP",
		airlines:          []string{"AAL", "ASA", "DAL", "EDV", "FDX", "FFT", "JBU", "NKS", "QXE", "UAL", "UPS"},
	}

	for _, ho := range []string{"_KFRG_0", "_KFRG_1", "_KFRG_2", "_KFRG_3"} {
		for _, exit := range jfkWater {
			ss.AddRoute("water", &Route{
				Waypoints:       "KFRG." + ho + ".@." + exit[0],
				Scratchpad:      exit[1],
				InitialAltitude: 70,
				ClearedAltitude: 5000,
				InitialSpeed:    0,
				Destinations: []string{
					"TAPA", "TXKF", "KMCO", "KFLL", "KSAV", "KATL", "EGLL", "EDDF", "LFPG", "EINN",
				},
			})
		}
		for _, exit := range jfkEast {
			ss.AddRoute("east", &Route{
				Waypoints:       "KFRG." + ho + ".@." + exit[0],
				Scratchpad:      exit[1],
				InitialAltitude: 70,
				ClearedAltitude: 5000,
				InitialSpeed:    0,
				Destinations: []string{
					"KBOS", "KPVD", "KACK", "KBDL", "KPWM", "KSYR",
				},
			})
		}
		for _, exit := range jfkNorth {
			ss.AddRoute("north", &Route{
				Waypoints:       "KFRG." + ho + ".@." + exit[0],
				Scratchpad:      exit[1],
				InitialAltitude: 70,
				ClearedAltitude: 5000,
				InitialSpeed:    0,
				Destinations: []string{
					"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW",
				},
			})
		}
		for _, exit := range jfkSouthWest {
			ss.AddRoute("sw", &Route{
				Waypoints:       "KFRG." + ho + ".@." + ho + "a." + exit[0],
				Scratchpad:      exit[1],
				InitialAltitude: 70,
				ClearedAltitude: 5000,
				InitialSpeed:    0,
				Destinations: []string{
					"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL",
				},
			})
		}
	}

	return ss
}

func NewKISPSimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:         now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport:  "KISP",
		adr:               int(c.adr),
		fleet:             "default",
		initialController: "ISP_APP",
		airlines:          []string{"AAL", "ASA", "DAL", "EDV", "FDX", "FFT", "JBU", "NKS", "QXE", "UAL", "UPS"},
	}

	for _, exit := range jfkNorth {
		ss.AddRoute("north", &Route{
			Waypoints:       "KISP._KISP_CLIMB._KISP_HO.@.#275." + exit[0],
			Scratchpad:      exit[1],
			InitialAltitude: 70,
			ClearedAltitude: 8000,
			InitialSpeed:    0,
			Destinations: []string{
				"KSAN", "KLAX", "KSFO", "KSEA", "KYYZ", "KORD", "KDEN", "KLAS", "KPHX", "KDTW",
			},
		})
	}

	return ss
}

func NewKLGASimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:         now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport:  "KLGA",
		adr:               int(c.adr),
		fleet:             "default",
		initialController: "LGA_DEP",
		airlines:          []string{"AAL", "ASA", "DAL", "EDV", "FDX", "FFT", "JBU", "NKS", "QXE", "UAL", "UPS"},
	}

	ss.AddRoute("dix", &Route{
		Waypoints:       "KLGA._KLGA_CLIMB._KLGA_HO.@.JFK.DIXIE",
		Scratchpad:      "DIX",
		InitialAltitude: 70,
		ClearedAltitude: 6000,
		InitialSpeed:    0,
		Destinations: []string{
			"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL",
		},
	})
	for i, water := range []string{"SHIPP", "WAVEY", "BETTE"} {
		sp := []string{"SHI", "WAV", "BET"}
		ss.AddRoute("water", &Route{
			Waypoints:       "KLGA._KLGA_CLIMB._KLGA_HO.@.JFK." + water,
			Scratchpad:      sp[i],
			InitialAltitude: 70,
			ClearedAltitude: 8000,
			InitialSpeed:    0,
			Destinations: []string{
				"TAPA", "TXKF", "KMCO", "KFLL", "KSAV", "KATL", "EGLL", "EDDF", "LFPG", "EINN",
			},
		})
	}

	return ss
}

func NewKLGAPropSimSpawner(c *SimSpawnerConfig, now time.Time) *SimSpawner {
	ss := &SimSpawner{
		nextSpawn:         now.Add(time.Duration(rand.Intn(int(3600/c.adr))) * time.Second),
		departureAirport:  "KLGA",
		initialController: "LGA_DEP",
		airlines:          []string{"N"},
		adr:               int(c.adr),
		fleet:             "lightGA",
	}

	ss.AddRoute("white", &Route{
		Waypoints:       "KLGA._KLGA_CLIMB._KLGA_HO.@.JFK.WHITE",
		Scratchpad:      "WHI",
		InitialAltitude: 70,
		ClearedAltitude: 7000,
		InitialSpeed:    0,
		Destinations: []string{
			"KAUS", "KMSY", "KDFW", "KACY", "KDCA", "KIAH", "KIAD", "KBWI", "KCLT", "KPHL",
		},
	})

	return ss
}
