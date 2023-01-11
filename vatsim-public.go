// vatsim-public.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Serves up aircraft et al. using the public (updated every 15s) VATSIM
// data-stream.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

///////////////////////////////////////////////////////////////////////////
// VATSIMPublicServer

type VATSIMPublicServer struct {
	aircraft       map[string]*Aircraft
	controllers    map[string]*Controller
	controllerATIS map[string]string
	users          map[string]*User
	airportATIS    map[string][]ATIS
	flightStrips   map[string]*FlightStrip

	metarAirports map[string]interface{}
	metar         map[string]*METAR

	centers [4]Point2LL
	rangeNm int

	ctx    context.Context
	cancel context.CancelFunc

	lastRequest        time.Time
	updateCycle        int
	requestOutstanding bool
	requestsChan       chan VPSUpdateRequest
	responsesChan      chan VPSUpdateResponse
}

func NewVATSIMPublicServer() *VATSIMPublicServer {
	vp := &VATSIMPublicServer{
		aircraft:       make(map[string]*Aircraft),
		controllers:    make(map[string]*Controller),
		controllerATIS: make(map[string]string),
		users:          make(map[string]*User),
		airportATIS:    make(map[string][]ATIS),
		flightStrips:   make(map[string]*FlightStrip),
		metarAirports:  make(map[string]interface{}),
		metar:          make(map[string]*METAR),

		requestsChan:  make(chan VPSUpdateRequest),
		responsesChan: make(chan VPSUpdateResponse),
	}

	vp.ctx, vp.cancel = context.WithCancel(context.Background())

	go vp.fetchVATSIMPublicAsync()

	return vp
}

type VPSUpdateRequest struct {
	metarAirports map[string]interface{}
	centers       [4]Point2LL
	rangeNm       int
}

type VPSUpdateResponse struct {
	aircraft       map[string]*Aircraft
	controllers    map[string]*Controller
	controllerATIS map[string]string
	users          map[string]*User
	airportATIS    map[string][]ATIS
	METAR          map[string]*METAR
}

func NewVPSUpdateResponse() VPSUpdateResponse {
	return VPSUpdateResponse{
		aircraft:       make(map[string]*Aircraft),
		controllers:    make(map[string]*Controller),
		controllerATIS: make(map[string]string),
		users:          make(map[string]*User),
		airportATIS:    make(map[string][]ATIS),
		METAR:          make(map[string]*METAR),
	}
}

func (vp *VATSIMPublicServer) GetAircraft(callsign string) *Aircraft {
	if vp.aircraft == nil {
		return nil
	} else if ac, ok := vp.aircraft[callsign]; ok {
		return ac
	} else {
		return nil
	}
}

func (vp *VATSIMPublicServer) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range vp.aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (vp *VATSIMPublicServer) GetAllAircraft() []*Aircraft {
	_, ac := FlattenMap(vp.aircraft)
	return ac
}

func (vp *VATSIMPublicServer) GetFlightStrip(callsign string) *FlightStrip {
	if _, ok := vp.aircraft[callsign]; !ok {
		return nil
	}
	if _, ok := vp.flightStrips[callsign]; !ok {
		vp.flightStrips[callsign] = &FlightStrip{callsign: callsign}
	}
	return vp.flightStrips[callsign]
}

func (vp *VATSIMPublicServer) GetMETAR(location string) *METAR {
	if metar, ok := vp.metar[location]; ok {
		return metar
	}
	return nil
}

func (vp *VATSIMPublicServer) GetAirportATIS(airport string) []ATIS {
	if atis, ok := vp.airportATIS[airport]; ok {
		return atis
	}
	return nil
}

func (vp *VATSIMPublicServer) RequestControllerATIS(controller string) error {
	if atis, ok := vp.controllerATIS[controller]; ok {
		tm := TextMessage{
			messageType: TextPrivate,
			sender:      controller,
			contents:    "ATIS: " + atis,
		}
		eventStream.Post(&TextMessageEvent{message: &tm})
	}
	return nil
}

func (vp *VATSIMPublicServer) GetUser(callsign string) *User {
	if u, ok := vp.users[callsign]; ok {
		return u
	}
	return nil
}

func (vp *VATSIMPublicServer) GetController(callsign string) *Controller {
	if vp.controllers == nil {
		return nil
	} else if ctrl, ok := vp.controllers[callsign]; ok {
		return ctrl
	} else {
		return nil
	}

}
func (vp *VATSIMPublicServer) GetAllControllers() []*Controller {
	_, ctrl := FlattenMap(vp.controllers)
	return ctrl
}

func (vp *VATSIMPublicServer) AddAirportForWeather(airport string) {
	vp.metarAirports[airport] = nil
}

func (vp *VATSIMPublicServer) SetPrimaryFrequency(f Frequency) {}

func (vp *VATSIMPublicServer) SetSquawk(callsign string, squawk Squawk) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) SetSquawkAutomatic(callsign string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) SetScratchpad(callsign string, scratchpad string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) SetTemporaryAltitude(callsign string, alt int) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) SetVoiceType(callsign string, cap VoiceCapability) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) PushFlightStrip(callsign string, controller string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) InitiateTrack(callsign string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) Handoff(callsign string, controller string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) AcceptHandoff(callsign string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) RejectHandoff(callsign string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) CancelHandoff(callsign string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) DropTrack(callsign string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) PointOut(callsign string, controller string) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) SendTextMessage(m TextMessage) error {
	return ControlUnsupported
}

func (vp *VATSIMPublicServer) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	vp.centers = [4]Point2LL{primary, secondary[0], secondary[1], secondary[2]}
	vp.rangeNm = rangeNm
	return nil
}

func (vp *VATSIMPublicServer) GetWindowTitle() string { return "VATSIM Public" }
func (vp *VATSIMPublicServer) Connected() bool        { return vp.aircraft != nil }

func (vp *VATSIMPublicServer) Disconnect() {
	vp.cancel()

	for _, ac := range vp.aircraft {
		eventStream.Post(&RemovedAircraftEvent{ac: ac})
	}
	vp.aircraft = nil

	for _, ctrl := range vp.controllers {
		eventStream.Post(&RemovedControllerEvent{Controller: ctrl})
	}
	vp.controllers = nil
}

func (vp *VATSIMPublicServer) Callsign() string       { return "(none)" }
func (vp *VATSIMPublicServer) CurrentTime() time.Time { return time.Now() }

func (vp *VATSIMPublicServer) GetUpdates() {
	elapsed := time.Since(vp.lastRequest)
	if elapsed > 15*time.Second && !vp.requestOutstanding {
		request := VPSUpdateRequest{
			metarAirports: DuplicateMap(vp.metarAirports),
			centers:       vp.centers,
			rangeNm:       vp.rangeNm,
		}
		vp.requestsChan <- request
		vp.lastRequest = time.Now()
		vp.requestOutstanding = true
		return
	}
	if (elapsed > 5*time.Second && vp.updateCycle == 1) ||
		(elapsed > 10*time.Second && vp.updateCycle == 2) {
		// 		lg.Printf("update %d", vp.updateCycle)
		// Interpolate track positions
		now := time.Now()
		for _, ac := range vp.aircraft {
			heading := ac.Heading()
			v := [2]float32{sin(radians(heading)), cos(radians(heading))}
			nm := nmlength2ll(v)
			// scale so that it's how far the aircraft goes in 5 seconds
			v = scale2ll(v, float32(5*ac.Groundspeed())/(3600*nm))

			//lg.Printf("%s: pos %+v, heading %f, v %+v -> pos %+v", ac.Callsign, ac.Position(), heading, v, add2f(ac.Position(), v))

			ac.AddTrack(RadarTrack{
				Position:    add2f(ac.Position(), v),
				Altitude:    ac.Altitude(),        // TODO: interpolate
				Groundspeed: ac.Groundspeed(),     // TODO: interpolate
				Heading:     ac.Tracks[0].Heading, // so no magnetic correction
				Time:        now})
		}

		vp.updateCycle++
	}

	if !vp.requestOutstanding {
		return
	}

	select {
	case update, ok := <-vp.responsesChan:
		vp.requestOutstanding = false
		vp.updateCycle = 1

		//lg.Printf("got real")
		if !ok {
			// closed?
		}

		for callsign, ac := range update.aircraft {
			if ourac, ok := vp.aircraft[callsign]; !ok {
				eventStream.Post(&AddedAircraftEvent{ac: ac})
				vp.aircraft[callsign] = ac

				actype := ac.FlightPlan.BaseType()
				if _, ok := database.LookupAircraftType(actype); !ok && actype != "" {
					lg.Errorf("%s: unknown ac type %s", ac.Callsign, actype)
				}
			} else {
				eventStream.Post(&ModifiedAircraftEvent{ac: ourac})
				//lg.Printf("%s: proper track %+v", ac.Callsign, ac.Tracks[0])
				ourac.AddTrack(ac.Tracks[0])

				// Fixup the two interpolated ones
				if !ourac.Tracks[3].Position.IsZero() {
					ourac.Tracks[1].Position = lerp2ll(2./3., ourac.Tracks[3].Position, ourac.Tracks[0].Position)
					ourac.Tracks[2].Position = lerp2ll(1./3., ourac.Tracks[3].Position, ourac.Tracks[0].Position)
				}
			}
		}
		for callsign, ac := range vp.aircraft {
			if _, ok := update.aircraft[callsign]; !ok {
				eventStream.Post(&RemovedAircraftEvent{ac: ac})
				delete(vp.aircraft, callsign)
			}
		}

		// Filter down to the ones we have in the position file.
		update.controllers = FilterMap(update.controllers,
			func(callsign string, ctrl *Controller) bool {
				return database.LookupPosition(callsign, ctrl.Frequency) != nil
			})

		for callsign, ctrl := range update.controllers {
			if _, ok := vp.controllers[callsign]; !ok {
				eventStream.Post(&AddedControllerEvent{Controller: ctrl})
			} else {
				eventStream.Post(&ModifiedControllerEvent{Controller: ctrl})
			}
			vp.controllers[callsign] = ctrl
		}
		for callsign, ctrl := range vp.controllers {
			if _, ok := update.controllers[callsign]; !ok {
				eventStream.Post(&RemovedControllerEvent{Controller: ctrl})
				delete(vp.controllers, callsign)
			}
		}

		vp.controllerATIS = FilterMap(update.controllerATIS,
			func(callsign string, atis string) bool {
				if ctrl, ok := vp.controllers[callsign]; !ok {
					return false
				} else {
					return database.LookupPosition(callsign, ctrl.Frequency) != nil
				}
			})

		// Merge users
		for name, user := range update.users {
			vp.users[name] = user
		}
		for name := range vp.users {
			if _, ok := update.users[name]; !ok {
				delete(vp.users, name)
			}
		}

		// TODO: events for updates..
		vp.airportATIS = update.airportATIS

		// Merge like this so we don't clobber when there aren't any METAR
		// updates this time.
		for ap, metar := range update.METAR {
			vp.metar[ap] = metar
		}

	default:
		// nothing on the channel
	}

	// TODO: interpolate?
}

type VATSIMDataResponse struct {
	Pilots []struct {
		CID         int     `json:"cid"`
		Name        string  `json:"name"`
		Callsign    string  `json:"callsign"`
		Server      string  `json:"server"`
		PilotRating int     `json:"pilot_rating"`
		Latitude    float32 `json:"latitude"`
		Longitude   float32 `json:"longitude"`
		Altitude    int     `json:"altitude"`
		Groundspeed int     `json:"groundspeed"`
		Transponder string  `json:"transponder"`
		Heading     int     `json:"heading"`
		QNH_Hg      float32 `json:"qnh_i_hg"`
		QNH_mb      float32 `json:"qnh_mb"`
		FlightPlan  struct {
			Rules               string    `json:"flight_rules"`
			Aircraft            string    `json:"aircraft"`
			AircraftFAA         string    `json:"aircraft_faa"`
			AircraftShort       string    `json:"aircraft_short"`
			Departure           string    `json:"departure"`
			Arrival             string    `json:"arrival"`
			Alternate           string    `json:"alternate"`
			CruiseTAS           string    `json:"cruise_tas"`
			Altitude            string    `json:"altitude"`
			DepartureTime       string    `json:"deptime"`
			EnrouteTime         string    `json:"enroute_time"`
			FuelTime            string    `json:"fuel_time"`
			Remarks             string    `json:"remarks"`
			Route               string    `json:"route"`
			RevisionID          int       `json:"revision_id"`
			AssignedTransponder string    `json:"assigned_transponder"`
			Logon               time.Time `json:"logon_time"`
			LastUpdate          time.Time `json:"last_updated"`
		} `json:"flight_plan"`
	} `json:"pilots"`

	Controllers []struct {
		CID        int       `json:"cid"`
		Name       string    `json:"name"`
		Callsign   string    `json:"callsign"`
		Frequency  string    `json:"frequency"`
		Facility   int       `json:"facility"`
		Rating     int       `json:"rating"`
		Server     string    `json:"server"`
		Range      int       `json:"visual_range"`
		ATIS       []string  `json:"text_atis"`
		Logon      time.Time `json:"logon_time"`
		LastUpdate time.Time `json:"last_updated"`
	} `json:"controllers"`

	ATIS []struct {
		CID        int       `json:"cid"`
		Name       string    `json:"name"`
		Callsign   string    `json:"callsign"`
		Frequency  string    `json:"frequency"`
		Facility   int       `json:"facility"`
		Rating     int       `json:"rating"`
		Server     string    `json:"server"`
		Range      int       `json:"visual_range"`
		ATISCode   string    `json:"atis_code"`
		ATIS       []string  `json:"text_atis"`
		Logon      time.Time `json:"logon_time"`
		LastUpdate time.Time `json:"last_updated"`
	} `json:"atis"`
}

// The implementation below has multiple assumptions that an update request
// is sent every 15 seconds or so.
func (vp *VATSIMPublicServer) fetchVATSIMPublicAsync() {
	// Counter so that we only fetch METAR every 6th request
	metarCycle := 0
	var prevMETARAirports []string

	for {
		select {
		case <-vp.ctx.Done():
			// canceled
			return

		case req := <-vp.requestsChan:
			resp := NewVPSUpdateResponse()

			if len(req.metarAirports) > 0 {
				// Fetch METAR every 90 seconds or if the airports have changed
				changed := !SliceEqual(prevMETARAirports, SortedMapKeys(req.metarAirports))
				if changed || metarCycle%6 == 0 {
					metarCycle = 0
					prevMETARAirports = SortedMapKeys(req.metarAirports)

					ap, _ := FlattenMap(req.metarAirports)
					url := "https://metar.vatsim.net/metar.php?id=" + strings.Join(ap, ",")
					if metarText, err := FetchURL(url); err == nil {
						for _, line := range strings.Split(string(metarText), "\n") {
							if metar, err := ParseMETAR(line); err == nil {
								resp.METAR[metar.AirportICAO] = metar
							} else {
								lg.Errorf("%s: %v", line, err)
							}
						}
					} else {
						lg.Errorf("%s: %v", url, err)
					}
				}
				metarCycle++
			}
			text, err := FetchURL("https://data.vatsim.net/v3/vatsim-data.json")

			var vsd VATSIMDataResponse
			if err := json.Unmarshal(text, &vsd); err != nil {
				lg.Errorf("Error unmarshaling vatsim response: %v", err)
				return
			}

			for _, p := range vsd.Pilots {
				// FIXME: could check all centers?
				if nmdistance2ll(req.centers[0], Point2LL{p.Longitude, p.Latitude}) > float32(req.rangeNm) {
					continue
				}

				resp.users[p.Callsign] = &User{Name: p.Name, Rating: NetworkRating(p.PilotRating)}

				fp := &FlightPlan{
					AircraftType: p.FlightPlan.AircraftFAA,
					//CruiseSpeed:      p.FlightPlan.CruiseTAS,
					DepartureAirport: p.FlightPlan.Departure,
					// DepartureTime -> 				DepartTimeActual       int
					ArrivalAirport: p.FlightPlan.Arrival,
					// EnrouteTime -> Hours, Minutes         int
					// FuelTime -> FuelHours, FuelMinutes int
					AlternateAirport: p.FlightPlan.Alternate,
					Route:            p.FlightPlan.Route,
					Remarks:          p.FlightPlan.Remarks,
				}

				fp.Altitude, err = ParseAltitude(p.FlightPlan.Altitude)
				if p.FlightPlan.Altitude != "" && err != nil {
					lg.Errorf("%s: bogus altitude %s: %v", p.Callsign, p.FlightPlan.Altitude, err)
				}

				if p.FlightPlan.Rules == "I" {
					fp.Rules = IFR
				} else {
					fp.Rules = VFR
				}

				ac := &Aircraft{
					Callsign:   p.Callsign,
					FlightPlan: fp,
				}

				ac.Squawk, err = ParseSquawk(p.Transponder)
				if err != nil {
					lg.Errorf("%s: bogus squawk %s: %v", p.Callsign, p.Transponder, err)
				}
				ac.AssignedSquawk, err = ParseSquawk(p.FlightPlan.AssignedTransponder)
				if err != nil {
					lg.Errorf("%s: bogus squawk %s: %v", p.Callsign, p.FlightPlan.AssignedTransponder, err)
				}

				if strings.Contains(p.FlightPlan.Remarks, "/V/") || strings.Contains(p.FlightPlan.Remarks, "/v/") {
					ac.VoiceCapability = VoiceFull
				} else if strings.Contains(p.FlightPlan.Remarks, "/R/") || strings.Contains(p.FlightPlan.Remarks, "/r/") {
					ac.VoiceCapability = VoiceReceive
				} else if strings.Contains(p.FlightPlan.Remarks, "/T/") || strings.Contains(p.FlightPlan.Remarks, "/t/") {
					ac.VoiceCapability = VoiceText
				}

				ac.Tracks[0] = RadarTrack{
					Position:    Point2LL{p.Longitude, p.Latitude},
					Altitude:    p.Altitude,
					Groundspeed: p.Groundspeed,
					Heading:     float32(p.Heading),
					Time:        time.Now(),
				}

				resp.aircraft[p.Callsign] = ac
			}

			for _, c := range vsd.Controllers {
				resp.users[c.Callsign] = &User{Name: c.Name, Rating: NetworkRating(c.Rating)}

				ctrl := &Controller{
					CID:        fmt.Sprintf("%d", c.CID),
					Callsign:   c.Callsign,
					Name:       c.Name,
					ScopeRange: c.Range,
					Rating:     NetworkRating(c.Rating),
					Facility:   Facility(c.Facility),
				}

				if fr, err := strconv.ParseFloat(c.Frequency, 32); err == nil {
					ctrl.Frequency = NewFrequency(float32(fr))
				}

				// Location is not provided, unfortunately, so try to set one
				// using their airport location.
				if airport, _, ok := strings.Cut(c.Callsign, "_"); ok {
					ctrl.Location, _ = database.Locate(airport)
				}
				resp.controllers[c.Callsign] = ctrl
				resp.controllerATIS[c.Callsign] = strings.Join(c.ATIS, "\n")
			}

			for _, a := range vsd.ATIS {
				apFields := strings.Split(strings.TrimSuffix(a.Callsign, "_ATIS"), "_")
				var atis ATIS
				switch len(apFields) {
				case 1:
					atis = ATIS{Airport: apFields[0], Code: a.ATISCode, Contents: strings.Join(a.ATIS, "\n")}
					resp.airportATIS[atis.Airport] = append(resp.airportATIS[atis.Airport], atis)

				case 2:
					atis = ATIS{Airport: apFields[0], AppDep: apFields[1], Code: a.ATISCode, Contents: strings.Join(a.ATIS, "\n")}
					resp.airportATIS[atis.Airport] = append(resp.airportATIS[atis.Airport], atis)

				default:
					lg.Errorf("%s: unexpected ATIS airport", a.Callsign)
				}
			}

			vp.responsesChan <- resp
		}
	}
}
