// flightradar.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

///////////////////////////////////////////////////////////////////////////
// FlightRadarServer

// FlightRadarServer is a simple implementation of the ATCServer interface
// that serves aircraft data from flightradar24.com. Almost all of the
// ATCServer method implementations are empty in that we aren't able to
// control real-world aircraft from vice...

type FlightRadarServer struct {
	aircraft    map[string]*Aircraft
	lastRequest time.Time
}

// via https://github.com/Sequal32/vrclivetraffic/blob/master/src/flightradar.rs
type FlightRadarResponse struct {
	mode_s_code  string
	latitude     float32
	longitude    float32
	bearing      int
	altitude     int
	speed        int
	squawkCode   string
	radar        string
	model        string
	registration string
	timestamp    uint64
	origin       string
	destination  string
	flight       string
	onGround     int
	climbRate    int
	callsign     string
	isGlider     int
	airline      string
}

func (fr *FlightRadarServer) GetAircraft(callsign string) *Aircraft {
	if fr.aircraft == nil {
		return nil
	} else if ac, ok := fr.aircraft[callsign]; ok {
		return ac
	} else {
		return nil
	}
}

func (fr *FlightRadarServer) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range fr.aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (fr *FlightRadarServer) GetAllAircraft() []*Aircraft {
	_, ac := FlattenMap(fr.aircraft)
	return ac
}

var ControlUnsupported = errors.New("Controlling is not possible with a FlightRadar connection")

func (fr *FlightRadarServer) GetFlightStrip(callsign string) *FlightStrip   { return nil }
func (fr *FlightRadarServer) GetMETAR(location string) *METAR               { return nil }
func (fr *FlightRadarServer) GetAirportATIS(airport string) []ATIS          { return nil }
func (fr *FlightRadarServer) RequestControllerATIS(controller string) error { return nil }
func (fr *FlightRadarServer) GetUser(callsign string) *User                 { return nil }
func (fr *FlightRadarServer) GetController(callsign string) *Controller     { return nil }
func (fr *FlightRadarServer) GetAllControllers() []*Controller              { return nil }
func (fr *FlightRadarServer) AddAirportForWeather(airport string)           {}
func (fr *FlightRadarServer) SetPrimaryFrequency(f Frequency)               {}

func (fr *FlightRadarServer) SetSquawk(callsign string, squawk Squawk) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) SetSquawkAutomatic(callsign string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) SetScratchpad(callsign string, scratchpad string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) SetTemporaryAltitude(callsign string, alt int) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) SetVoiceType(callsign string, cap VoiceCapability) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) PushFlightStrip(callsign string, controller string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) InitiateTrack(callsign string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) Handoff(callsign string, controller string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) AcceptHandoff(callsign string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) RejectHandoff(callsign string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) CancelHandoff(callsign string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) DropTrack(callsign string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) PointOut(callsign string, controller string) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) SendTextMessage(m TextMessage) error {
	return ControlUnsupported
}

func (fr *FlightRadarServer) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	return nil
}

func (fr *FlightRadarServer) GetWindowTitle() string { return "FlightRadar" }
func (fr *FlightRadarServer) Connected() bool        { return fr.aircraft != nil }

func (fr *FlightRadarServer) Disconnect() {
	for _, ac := range fr.aircraft {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	fr.aircraft = nil
}

func (fr *FlightRadarServer) Callsign() string       { return "(none)" }
func (fr *FlightRadarServer) CurrentTime() time.Time { return time.Now() }

func (fr *FlightRadarServer) GetUpdates() {
	// Don't poke flight rader more frequently than every 5s
	if fr.aircraft == nil || time.Since(fr.lastRequest) < 5*time.Second {
		return
	}

	// 50nm radius, fixed. (It doesn't seem worth making this
	// configurable...)
	radius := float32(50)

	center, ok := database.Locate(positionConfig.PrimaryRadarCenter)
	if !ok {
		// Try to find a center for the flight radar query by taking the
		// center of the last RadarScopePane we come across when walking
		// the window hierarchy.
		positionConfig.DisplayRoot.VisitPanes(func(p Pane) {
			if rs, ok := p.(*RadarScopePane); ok {
				center = rs.Center
			}
		})
	}

	request := fmt.Sprintf("https://data-live.flightradar24.com/zones/fcgi/feed.js?bounds=%.2f,%.2f,%.2f,%.2f&faa=1&satellite=1&vehicles=1&mlat=1&flarm=1&adsb=1&gnd=1&air=1&estimated=1&maxage=30",
		center.Latitude()+radius/database.NmPerLatitude,
		center.Latitude()-radius/database.NmPerLatitude,
		center.Longitude()-radius/database.NmPerLongitude,
		center.Longitude()+radius/database.NmPerLongitude)

	fr.lastRequest = time.Now()
	response, err := http.Get(request)
	if err != nil {
		lg.Errorf("Error with flightradar GET: %v", err)
		return
	}
	defer response.Body.Close()

	var text []byte
	if text, err = io.ReadAll(response.Body); err != nil {
		lg.Errorf("Error reading flightradar response: %v", err)
		return
	}

	// Slurp up the response as a general set of key/value pairs without
	// trying to impose any more structure just yet.
	var entries map[string]interface{}
	if err := json.Unmarshal(text, &entries); err != nil {
		lg.Errorf("Error unmarshaling flightradar response: %v", err)
	}

	for _, v := range entries {
		// Is it an array of things? If so, it's an aircraft position
		// update. (There's sundry other housekeeping in the response that
		// we're not interested in and will ignore.)
		if array, ok := v.([]interface{}); ok {
			i := 0

			// A few helper functions to decode elements of the array, with
			// the assumption that they have various types. Note that they
			// capture the local variable "i" and increment it after
			// consuming a value.
			getstring := func() string {
				var s string
				var ok bool
				if s, ok = array[i].(string); !ok {
					lg.Errorf("%d expected string, got %T: %+v", i, array[i], array[i])
				}
				i++
				return s
			}
			getfloat64 := func() float64 {
				var v float64
				var ok bool
				if v, ok = array[i].(float64); !ok {
					lg.Errorf("%d expected float64, got %T: %+v", i, array[i], array[i])
				}
				i++
				return v
			}
			getint := func() int { return int(getfloat64()) }
			getfloat32 := func() float32 { return float32(getfloat64()) }
			getuint64 := func() uint64 { return uint64(getfloat64()) }

			// #yolo to fill in a FlightRadarResponse from the array of entries.
			f := FlightRadarResponse{getstring(), getfloat32(), getfloat32(),
				getint(), getint(), getint(), getstring(), getstring(), getstring(),
				getstring(), getuint64(), getstring(), getstring(), getstring(),
				getint(), getint(), getstring(), getint(), getstring()}

			// Convert the FlightRadarResponse into a RadarTrack that we
			// can pass along to the client.
			if f.callsign != "" {
				squawk, err := ParseSquawk(f.squawkCode)
				if err != nil {
					lg.Errorf("Error parsing squawk \"%s\": %v", f.squawkCode, err)
				}
				pos := RadarTrack{
					Position:    Point2LL{f.longitude, f.latitude},
					Altitude:    f.altitude,
					Groundspeed: f.speed,
					Time:        time.Now()}

				var ac *Aircraft
				var ok bool
				if ac, ok = fr.aircraft[f.callsign]; !ok {
					ac = &Aircraft{callsign: f.callsign}
					ac.flightPlan = &FlightPlan{}
					ac.flightPlan.depart = f.origin
					ac.flightPlan.arrive = f.destination
					ac.flightPlan.actype = f.model
					fr.aircraft[f.callsign] = ac
					ac.mode = Charlie
					eventStream.Post(&AddedAircraftEvent{ac: ac})
				} else {
					eventStream.Post(&ModifiedAircraftEvent{ac: ac})
				}
				ac.squawk = squawk
				ac.AddTrack(pos)
			}
		}
	}
}

func NewFlightRadarServer() *FlightRadarServer {
	return &FlightRadarServer{aircraft: make(map[string]*Aircraft)}
}
