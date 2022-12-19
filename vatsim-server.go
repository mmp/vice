// vatsim-server.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bufio"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"
)

var (
	// All of the servers we can connect to, maintained as a map from
	// server name to hostname or IP address.
	vatsimServers map[string]string = map[string]string{}
)

func vatsimInit() {
	// Disabled so long as we're not official.
	return

	if resp, err := http.Get("http://data.vatsim.net/vatsim-servers.txt"); err != nil {
		ShowErrorDialog("Error retrieving list of VATSIM servers: " + err.Error())
	} else {
		defer resp.Body.Close()

		inServers := false
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if len(line) == 0 || line[0] == ';' {
				continue
			}

			if line == "!SERVERS:" {
				inServers = true
			} else if line[0] == '!' {
				inServers = false
			} else if inServers {
				fields := strings.Split(line, ":")
				vatsimServers[fields[0]+" ("+fields[2]+")"] = fields[1]
				lg.Printf("added %s -> %s", fields[0]+" ("+fields[2]+")", fields[1])
			}
		}
	}
}

type MalformedMessageError struct {
	Err string
}

func (m MalformedMessageError) Error() string {
	return m.Err
}

var (
	// We maintain these as global variables so that they can be
	// initialized by init() functions when we compile with the secret
	// parts required for full VATSIM support.
	vatsimMessageSpecs           []*VATSIMMessageSpec
	vatsimUpdateCallback         func(v *VATSIMServer)
	makeVatsimAircraftController func(v *VATSIMServer) AircraftController = func(v *VATSIMServer) AircraftController {
		return &InertAircraftController{}
	}
)

///////////////////////////////////////////////////////////////////////////
// VATSIMServer

type VATSIMServer struct {
	callsign string

	connection      VATSIMConnection
	controlDelegate AircraftController

	// Various things we need to keep track of over the course of a
	// session.
	aircraft          map[string]*Aircraft
	flightStrips      map[string]*FlightStrip
	users             map[string]*User
	controllers       map[string]*Controller
	controllerSectors map[string]*Controller
	pilots            map[string]*Pilot
	metar             map[string]METAR
	atis              map[string][]ATIS

	airportsForWeather map[string]interface{}

	atcValid bool
}

func NewVATSIMServer() *VATSIMServer {
	return &VATSIMServer{
		aircraft:           make(map[string]*Aircraft),
		flightStrips:       make(map[string]*FlightStrip),
		controllers:        make(map[string]*Controller),
		controllerSectors:  make(map[string]*Controller),
		metar:              make(map[string]METAR),
		atis:               make(map[string][]ATIS),
		users:              make(map[string]*User),
		pilots:             make(map[string]*Pilot),
		airportsForWeather: make(map[string]interface{}),
	}
}

func NewVATSIMNetworkServer(address string) (*VATSIMServer, error) {
	v := NewVATSIMServer()
	v.callsign = positionConfig.VatsimCallsign

	var err error
	if v.connection, err = NewVATSIMNetConnection(address); err != nil {
		return nil, err
	}
	v.controlDelegate = makeVatsimAircraftController(v)

	loc, _ := database.Locate(positionConfig.PrimaryRadarCenter)

	v.controllers[positionConfig.VatsimCallsign] = &Controller{
		Callsign:   positionConfig.VatsimCallsign,
		Name:       globalConfig.VatsimName,
		CID:        globalConfig.VatsimCID,
		Rating:     globalConfig.VatsimRating,
		Frequency:  positionConfig.primaryFrequency,
		ScopeRange: int(positionConfig.RadarRange),
		Facility:   positionConfig.VatsimFacility,
		Location:   loc}

	return v, nil
}

func NewVATSIMReplayServer(filename string, offsetSeconds int, replayRate float32) (*VATSIMServer, error) {
	v := NewVATSIMServer()
	v.callsign = "(none)"
	v.controlDelegate = &InertAircraftController{}

	var err error
	if v.connection, err = NewVATSIMReplayConnection(filename, offsetSeconds, replayRate); err != nil {
		return nil, err
	} else {
		return v, nil
	}
}

///////////////////////////////////////////////////////////////////////////
// ATCServer method implementations

func (v *VATSIMServer) GetAircraft(callsign string) *Aircraft {
	if ac, ok := v.aircraft[callsign]; ok {
		return ac
	} else {
		return nil
	}
}

func (v *VATSIMServer) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range v.aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (v *VATSIMServer) GetAllAircraft() []*Aircraft {
	_, ac := FlattenMap(v.aircraft)
	return ac
}

func (v *VATSIMServer) GetFlightStrip(callsign string) *FlightStrip {
	if _, ok := v.aircraft[callsign]; !ok {
		return nil
	}
	if _, ok := v.flightStrips[callsign]; !ok {
		v.flightStrips[callsign] = &FlightStrip{callsign: callsign}
	}
	return v.flightStrips[callsign]
}

func (v *VATSIMServer) GetMETAR(location string) *METAR {
	if m, ok := v.metar[location]; ok {
		return &m
	} else {
		return nil
	}
}

func (v *VATSIMServer) GetAirportATIS(airport string) []ATIS {
	if atis, ok := v.atis[airport]; ok {
		return atis
	} else {
		return nil
	}
}

func (v *VATSIMServer) RequestControllerATIS(controller string) error {
	c := v.GetController(controller)
	if c == nil {
		return ErrNoController
	}
	return v.controlDelegate.RequestControllerATIS(c.Callsign)
}

func (v *VATSIMServer) GetUser(callsign string) *User {
	if user, ok := v.users[callsign]; ok {
		return user
	} else {
		return nil
	}
}

func (v *VATSIMServer) GetController(callsign string) *Controller {
	callsign = strings.ToUpper(callsign)
	if controller, ok := v.controllers[callsign]; ok {
		return controller
	} else if controller, ok := v.controllerSectors[callsign]; ok {
		return controller
	} else {
		return nil
	}
}

func (v *VATSIMServer) GetAllControllers() []*Controller {
	_, c := FlattenMap(v.controllers)
	sort.Slice(c, func(i, j int) bool { return c[i].Callsign < c[j].Callsign })
	return c
}

func (v *VATSIMServer) AddAirportForWeather(airport string) {
	v.airportsForWeather[airport] = nil
}

func (v *VATSIMServer) SetPrimaryFrequency(f Frequency) {
	if ctrl, ok := v.controllers[v.callsign]; !ok {
		v.controllers[v.callsign] = &Controller{Frequency: f}
	} else {
		ctrl.Frequency = f
	}
}

func (v *VATSIMServer) SetSquawk(callsign string, code Squawk) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else {
		ac.assignedSquawk = code
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return v.controlDelegate.SetSquawk(callsign, code)
	}
}

func (v *VATSIMServer) SetSquawkAutomatic(callsign string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if ac.flightPlan == nil || ac.flightPlan.Rules != IFR {
		return errors.New("non-IFR squawk codes must be set manually")
	} else {
		if c, ok := v.controllers[v.callsign]; !ok {
			lg.Errorf("%s: no Controller for me?", v.callsign)
			return errors.New("Must be signed in to a control position")
		} else {
			pos := c.GetPosition()
			if pos == nil {
				return errors.New("Radio must be primed to assign squawk codes")
			}

			if pos.LowSquawk == pos.HighSquawk {
				return errors.New("Current position has not been assigned a squawk code range")
			}

			squawkUnused := func(sq Squawk) bool {
				for _, ac := range v.aircraft {
					if ac.assignedSquawk == sq {
						return false
					}
				}
				return true
			}

			// Start at a random point in the range and then go linearly from
			// there.
			n := int(pos.HighSquawk - pos.LowSquawk)
			offset := rand.Int() % n
			for i := 0; i < n; i++ {
				sq := pos.LowSquawk + Squawk((i+offset)%n)
				if squawkUnused(sq) {
					return v.SetSquawk(callsign, sq)
				}
			}
			return fmt.Errorf("No free squawk codes between %s and %s(!)",
				pos.LowSquawk, pos.HighSquawk)
		}
	}
}

func (v *VATSIMServer) SetScratchpad(callsign string, scratchpad string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else if len(scratchpad) > 3 {
		return ErrScratchpadTooLong
	} else {
		ac.scratchpad = scratchpad
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return v.controlDelegate.SetScratchpad(callsign, scratchpad)
	}
}

func (v *VATSIMServer) SetTemporaryAltitude(callsign string, altitude int) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else {
		ac.tempAltitude = altitude
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return v.controlDelegate.SetTemporaryAltitude(callsign, altitude)
	}
}

func (v *VATSIMServer) SetVoiceType(callsign string, cap VoiceCapability) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else {
		err := v.controlDelegate.SetVoiceType(callsign, cap)

		err2 := amendFlightPlan(callsign, func(fp *FlightPlan) {
			voiceStr := "/" + cap.String() + "/"
			// Is the voice type already in the remarks?
			if strings.Contains(fp.Remarks, voiceStr) {
				return
			}

			// Remove any existing voice type
			fp.Remarks = strings.ReplaceAll(fp.Remarks, "/v/", "")
			fp.Remarks = strings.ReplaceAll(fp.Remarks, "/V/", "")
			fp.Remarks = strings.ReplaceAll(fp.Remarks, "/r/", "")
			fp.Remarks = strings.ReplaceAll(fp.Remarks, "/R/", "")
			fp.Remarks = strings.ReplaceAll(fp.Remarks, "/t/", "")
			fp.Remarks = strings.ReplaceAll(fp.Remarks, "/T/", "")

			// And insert the one that was set
			fp.Remarks += " " + voiceStr
		})

		if err2 != nil {
			if err == nil {
				return err2
			}
			return fmt.Errorf("Multiple errors: %s, %s", err.Error(), err2.Error())
		}
		return err
	}
}

func (v *VATSIMServer) AmendFlightPlan(callsign string, fp FlightPlan) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else if ac.flightPlan == nil {
		return ErrNoFlightPlanFiled
	} else {
		ac.flightPlan = &fp
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return v.controlDelegate.AmendFlightPlan(callsign, fp)
	}
}

func (v *VATSIMServer) PushFlightStrip(callsign string, controller string) error {
	if !v.atcValid {
		return ErrNotController
	} else if c := v.GetController(controller); c == nil {
		return ErrNoController
	} else {
		// Use c.callsign rather controller in case a sector id was
		// specified.
		return v.controlDelegate.PushFlightStrip(callsign, c.Callsign)
	}
}

func (v *VATSIMServer) InitiateTrack(callsign string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else {
		ac.trackingController = v.callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return v.controlDelegate.InitiateTrack(callsign)
	}
}

func (v *VATSIMServer) DropTrack(callsign string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else if ac.trackingController != v.callsign {
		return ErrNotTrackedByMe
	} else {
		ac.trackingController = ""
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return v.controlDelegate.DropTrack(callsign)
	}
}

func (v *VATSIMServer) Handoff(callsign string, controller string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else if c := v.GetController(controller); c == nil {
		return ErrNoController
	} else {
		// Use c.callsign in case we were given a sector id...
		ac.outboundHandoffController = c.Callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return v.controlDelegate.Handoff(callsign, c.Callsign)
	}
}

func (v *VATSIMServer) AcceptHandoff(callsign string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if ac.inboundHandoffController == "" {
		return ErrNotBeingHandedOffToMe
	} else {
		ac.trackingController = v.callsign
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		err := v.controlDelegate.AcceptHandoff(callsign)
		ac.inboundHandoffController = "" // only do this now so delegate can get the controller
		return err
	}
}

func (v *VATSIMServer) RejectHandoff(callsign string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if ac.inboundHandoffController == "" {
		return ErrNotBeingHandedOffToMe
	} else {
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		err := v.controlDelegate.RejectHandoff(callsign)
		ac.inboundHandoffController = "" // only do this now so delegate can get the controller
		return err
	}
}

func (v *VATSIMServer) CancelHandoff(callsign string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if ac.outboundHandoffController == "" {
		return ErrNotHandingOffAircraft
	} else {
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		err := v.controlDelegate.CancelHandoff(callsign)
		ac.outboundHandoffController = "" // only do this now so delegate can get the controller
		return err
	}
}

func (v *VATSIMServer) PointOut(callsign string, controller string) error {
	if !v.atcValid {
		return ErrNotController
	} else if ac := v.GetAircraft(callsign); ac == nil {
		return ErrNoAircraftForCallsign
	} else if v.trackedByAnotherController(callsign) {
		return ErrOtherControllerHasTrack
	} else if c := v.GetController(controller); c == nil {
		return ErrNoController
	} else {
		// Use c.callsign in case we were given a sector id...
		return v.controlDelegate.PointOut(callsign, c.Callsign)
	}
}

func (v *VATSIMServer) SendTextMessage(m TextMessage) error {
	// TODO: can observers send any kind of text message? Maybe private?
	if !v.atcValid {
		return ErrNotController
	}
	if globalConfig.VatsimRating != SupervisorRating &&
		globalConfig.VatsimRating != AdministratorRating &&
		m.messageType == TextBroadcast {
		return errors.New("Broadcast messages cannot be sent by non-supervisors")
	}

	return v.controlDelegate.SendTextMessage(m)
}

func (v *VATSIMServer) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	if !v.atcValid {
		// No error in this case; it's fine to silently ignore
		return nil
	}
	return v.controlDelegate.SetRadarCenters(primary, secondary, rangeNm)
}

func (v *VATSIMServer) GetUpdates() {
	if v.connection != nil {
		// Receive messages here; this runs in the same thread as the GUI et
		// al., so there's nothing to worry about w.r.t. races.
		messages := v.connection.GetMessages()
		for _, msg := range messages {
			strs := strings.Split(strings.TrimSpace(msg.Contents), ":")
			if len(strs) == 0 {
				lg.Printf("vatsim: empty message received?")
				continue
			}
			if len(strs[0]) == 0 {
				lg.Printf("vatsim: empty first field? \"%s\"", msg.Contents)
				continue
			}

			if *logTraffic {
				lg.Printf("Received: %s", msg.Contents)
			}

			matches := 0
			for _, spec := range vatsimMessageSpecs {
				if sender, ok := spec.Match(strs); ok {
					matches++
					if spec.handler != nil {
						if err := spec.handler(v, sender, strs); err != nil {
							lg.Printf("FSD message error: %T: %s: %s", err, err, msg.Contents)
						}
					}
				}
			}
			if matches == 0 {
				lg.Printf("No rule matched: %s", msg.Contents)
			}
		}

		// Do this after processing the messages.
		if vatsimUpdateCallback != nil {
			vatsimUpdateCallback(v)
		}
	}

	// Clean up anyone who we haven't heard from in 30 minutes
	now := v.CurrentTime()
	for callsign, ac := range v.aircraft {
		if now.Sub(ac.tracks[0].Time).Minutes() > 30. {
			delete(v.aircraft, callsign)
			eventStream.Post(&RemovedAircraftEvent{ac: ac})
		}
	}
}

func (v *VATSIMServer) Disconnect() {
	if v.connection == nil {
		return
	}

	if v.controlDelegate != nil {
		v.controlDelegate.Disconnect()
	}

	v.connection.Close()
	v.connection = nil
	v.controlDelegate = &InertAircraftController{}

	for _, ac := range v.aircraft {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}

	v.aircraft = make(map[string]*Aircraft)
	v.users = make(map[string]*User)
	v.pilots = make(map[string]*Pilot)
	v.controllers = make(map[string]*Controller)
	v.controllerSectors = make(map[string]*Controller)
	v.metar = make(map[string]METAR)
	v.atis = make(map[string][]ATIS)
}

func (v *VATSIMServer) Connected() bool {
	return v.connection != nil && v.connection.Connected()
}

func (v *VATSIMServer) Callsign() string {
	return v.callsign
}

func (v *VATSIMServer) CurrentTime() time.Time {
	if v.connection == nil {
		return time.Time{}
	}
	return v.connection.CurrentTime()
}

func (v *VATSIMServer) GetWindowTitle() string {
	if v.connection == nil {
		return "[Disconnected]"
	} else {
		return v.connection.GetWindowTitle()
	}
}

func (v *VATSIMServer) trackedByAnotherController(callsign string) bool {
	ac := v.GetAircraft(callsign)
	return ac != nil && ac.trackingController != "" && ac.trackingController != v.callsign
}

func (v *VATSIMServer) send(fields ...interface{}) {
	if v.connection != nil {
		v.connection.SendMessage(v.callsign, fields...)
	} else {
		lg.Printf("Tried to send message to closed connection")
	}
}

func (v *VATSIMServer) getOrCreateAircraft(callsign string) *Aircraft {
	if ac, ok := v.aircraft[callsign]; ok {
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		return ac
	} else {
		ac = &Aircraft{callsign: callsign}
		v.aircraft[callsign] = ac
		eventStream.Post(&AddedAircraftEvent{ac: ac})
		return ac
	}
}

func (v *VATSIMServer) trackInitiated(callsign string, controller string) error {
	// Sometimes we get this message for a/c we haven't seen. Don't worry
	// about it when it happens...
	if ac := v.GetAircraft(callsign); ac != nil {
		if ac.trackingController != "" {
			// This seems to happen for far-away aircraft where we don't
			// see all of the messages..
			// lg.Printf("%s: %s is tracking controller but %s initiated track?", callsign, ac.trackingController, controller)
		}
		ac.trackingController = controller
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
	}
	return nil
}
