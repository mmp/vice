// flightradar.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

///////////////////////////////////////////////////////////////////////////
// FlightRadarServer

// FlightRadarServer is a simple implementation of the ControlServer
// interface that serves aircraft data from flightradar24.com. Almost all
// of the ControlServer method implementations are empty in that we aren't
// able to control real-world aircraft from vice...
type FlightRadarServer struct {
	client      ControlClient
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

func (fr *FlightRadarServer) SetSquawk(callsign string, squawk Squawk)           {}
func (fr *FlightRadarServer) SetScratchpad(callsign string, scratchpad string)   {}
func (fr *FlightRadarServer) SetRoute(callsign string, route string)             {}
func (fr *FlightRadarServer) SetDeparture(callsign string, airport string)       {}
func (fr *FlightRadarServer) SetArrival(callsign string, airport string)         {}
func (fr *FlightRadarServer) SetAltitude(callsign string, alt int)               {}
func (fr *FlightRadarServer) SetTemporaryAltitude(callsign string, alt int)      {}
func (fr *FlightRadarServer) SetAircraftType(callsign string, ac string)         {}
func (fr *FlightRadarServer) SetFlightRules(callsign string, r FlightRules)      {}
func (fr *FlightRadarServer) PushFlightStrip(callsign string, controller string) {}
func (fr *FlightRadarServer) InitiateTrack(callsign string)                      {}
func (fr *FlightRadarServer) Handoff(callsign string, controller string)         {}
func (fr *FlightRadarServer) AcceptHandoff(callsign string)                      {}
func (fr *FlightRadarServer) RejectHandoff(callsign string)                      {}
func (fr *FlightRadarServer) DropTrack(callsign string)                          {}
func (fr *FlightRadarServer) PointOut(callsign string, controller string)        {}
func (fr *FlightRadarServer) SendTextMessage(m TextMessage)                      {}
func (fr *FlightRadarServer) Description() string                                { return "FlightRadar" }
func (fr *FlightRadarServer) GetWindowTitle() string                             { return "FlightRadar" }
func (fr *FlightRadarServer) Disconnect()                                        {}
func (fr *FlightRadarServer) CurrentTime() time.Time                             { return time.Now() }

// GetUpdates is the only ControlServer interface method that
// FlightRadarServer implements. It passes along as much information as it
// has at hand to its ControlClient.
func (fr *FlightRadarServer) GetUpdates() {
	// Don't poke flight rader more frequently than every 5s
	if time.Since(fr.lastRequest) < 5*time.Second {
		return
	}

	// 50nm radius, fixed. (It doesn't seem worth making this
	// configurable...)
	radius := float32(50)

	// default center: JFK, represent
	center := Point2LL{-73.779, 40.64}

	// Try to find a more appropriate center for the flight radar query by
	// taking the center of the first RadarScopePane we come across when
	// walking the window hierarchy.
	positionConfig.DisplayRoot.VisitPanes(func(p Pane) {
		if rs, ok := p.(*RadarScopePane); ok {
			center = rs.Center
		}
	})

	request := fmt.Sprintf("https://data-live.flightradar24.com/zones/fcgi/feed.js?bounds=%.2f,%.2f,%.2f,%.2f&faa=1&satellite=1&vehicles=1&mlat=1&flarm=1&adsb=1&gnd=1&air=1&estimated=1&maxage=30",
		center.Latitude()+radius/world.NmPerLatitude,
		center.Latitude()-radius/world.NmPerLatitude,
		center.Longitude()-radius/world.NmPerLongitude,
		center.Longitude()+radius/world.NmPerLongitude)

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
				pos := RadarTrack{position: Point2LL{f.longitude, f.latitude},
					altitude: f.altitude, groundspeed: f.speed}
				fr.client.PositionReceived(f.callsign, pos, squawk, Charlie)
			}
		}
	}
}

func NewFlightRadarServer(c ControlClient) *FlightRadarServer {
	return &FlightRadarServer{client: c}
}
