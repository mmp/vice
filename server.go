// server.go
// Copyright(c) 2023 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"time"
)

var (
	ErrControllerAlreadySignedIn = errors.New("controller with that callsign already signed in")
	ErrInvalidControllerToken    = errors.New("invalid controller token")
)

/*
TODO:
***   maybe Sim is really something like World and is purely read-only data for stars, etc?
 then Server becomes Sim, it handles all changes to the world.
 need to move time management, lots of other stuff to server
 then things like server rate need to come through the api...
  - automatic handoffs, etc should be handled here.

- make sure not using sim.Callsign in this file!!!!
- think about serialization for restart...
- stop holding *Aircraft and assuming callsign->*Aircraft will be
  consistent (mostly an issue in STARSPane); just use callsign?
- drop controller if no messages for some period of time
- is a mutex needed? how is concurrency handled by net/rpc?
- stars contorller list should be updated based on who is signed in
- think through serialize/deserialize of Server
  need to rewriet Handoff times
*/

type Server struct {
	sim         *Sim
	controllers map[string]*ServerController // from token

	Handoffs map[string]time.Time
}

type ServerController struct {
	Callsign string
	EventsId EventSubscriberId
	// *net.Conn?
}

// TODO: func NewServer(ssc NewSimConfiguration) *Server {
func NewServer(sim *Sim) *Server { // STOPGAP shared Sim
	return &Server{
		sim:         sim,
		controllers: make(map[string]*ServerController),

		Handoffs: make(map[string]time.Time),
	}
}

func (s *Server) SignOn(callsign string) (string, error) {
	for _, ctrl := range s.controllers {
		if ctrl.Callsign == callsign {
			return "", ErrControllerAlreadySignedIn
		}
	}

	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return "", err
	}

	token := base64.StdEncoding.EncodeToString(buf[:])
	s.controllers[token] = &ServerController{
		Callsign: callsign,
		EventsId: eventStream.Subscribe(),
	}
	return token, nil
}

func (s *Server) SignOff(token string, _ *struct{}) error {
	delete(s.controllers, token)
	return nil
}

func (s *Server) Update() {
	now := sim.CurrentTime()
	for callsign, t := range s.Handoffs {
		if now.After(t) {
			if ac, ok := s.sim.Aircraft[callsign]; ok {
				ac.TrackingController = ac.OutboundHandoffController
				ac.OutboundHandoffController = ""
				eventStream.Post(&AcceptedHandoffEvent{controller: ac.TrackingController, ac: ac})
				globalConfig.Audio.PlaySound(AudioEventHandoffAccepted)
			}
			delete(s.Handoffs, callsign)
		}
	}
}

type AircraftSpecifier struct {
	ControllerToken string
	Callsign        string
}

type AircraftPropertiesSpecifier struct {
	ControllerToken string
	Callsign        string
	Scratchpad      string
}

func (s *Server) dispatchCommand(token string, callsign string,
	check func(c *Controller, ac *Aircraft) error,
	cmd func(*Controller, *Aircraft) (string, error), response *string) error {
	if sc, ok := s.controllers[token]; !ok {
		return ErrInvalidControllerToken
	} else if ac, ok := s.sim.Aircraft[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		ctrl := sim.GetController(sc.Callsign)
		if ctrl == nil {
			panic("wtf")
		}

		if err := check(ctrl, ac); err != nil {
			return err
		} else {
			resp, err := cmd(ctrl, ac)
			if response != nil {
				*response = resp
			}
			return err
		}
	}
}

// Commands that are allowed by the controlling controller, who may not still have the track;
// e.g., turns after handoffs.
func (s *Server) dispatchControllingCommand(token string, callsign string,
	cmd func(*Controller, *Aircraft) (string, error), response *string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.ControllingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd, response)
}

// Commands that are allowed by tracking controller only.
func (s *Server) dispatchTrackingCommand(token string, callsign string,
	cmd func(*Controller, *Aircraft) (string, error), response *string) error {
	return s.dispatchCommand(token, callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		cmd, response)
}

func (s *Server) SetScratchpad(a *AircraftPropertiesSpecifier, _ *struct{}) error {
	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.Scratchpad = a.Scratchpad
			eventStream.Post(&ModifiedAircraftEvent{ac: ac})
			return "", nil
		}, nil)
}

func (s *Server) InitiateTrack(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(c *Controller, ac *Aircraft) error {
			// Make sure no one has the track already
			if ac.TrackingController != "" {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TrackingController = ctrl.Callsign
			ac.ControllingController = ctrl.Callsign
			eventStream.Post(&ModifiedAircraftEvent{ac: ac})
			eventStream.Post(&InitiatedTrackEvent{ac: ac})
			return "", nil
		}, nil)
}

func (s *Server) DropTrack(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TrackingController = ""
			ac.ControllingController = ""
			eventStream.Post(&ModifiedAircraftEvent{ac: ac})
			eventStream.Post(&DroppedTrackEvent{ac: ac})
			return "", nil
		}, nil)
}

type HandoffSpecifier struct {
	ControllerToken string
	Callsign        string
	Controller      string
}

func (s *Server) Handoff(h *HandoffSpecifier, _ *struct{}) error {
	return s.dispatchCommand(h.ControllerToken, h.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.TrackingController != ctrl.Callsign {
				return ErrOtherControllerHasTrack
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if octrl := sim.GetController(h.Controller); octrl == nil {
				return "", ErrNoController
			} else {
				ac.OutboundHandoffController = octrl.Callsign
				eventStream.Post(&ModifiedAircraftEvent{ac: ac})
				acceptDelay := 4 + rand.Intn(10)
				s.Handoffs[ac.Callsign] = sim.CurrentTime().Add(time.Duration(acceptDelay) * time.Second)
				return "", nil
			}
		}, nil)
}

func (s *Server) AcceptHandoff(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) error {
			if ac.InboundHandoffController != ctrl.Callsign {
				return ErrNotBeingHandedOffToMe
			}
			return nil
		},
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.InboundHandoffController = ""
			ac.TrackingController = ctrl.Callsign
			ac.ControllingController = ctrl.Callsign
			eventStream.Post(&AcceptedHandoffEvent{controller: ctrl.Callsign, ac: ac})
			eventStream.Post(&ModifiedAircraftEvent{ac: ac}) // FIXME...
			return "", nil
		}, nil)
}

func (s *Server) CancelHandoff(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchTrackingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			delete(s.Handoffs, ac.Callsign)

			ac.OutboundHandoffController = ""
			// TODO: we are inconsistent in other control backends about events
			// when user does things like this; sometimes no event, sometimes
			// modified a/c event...
			eventStream.Post(&ModifiedAircraftEvent{ac: ac})
			return "", nil
		}, nil)
}

type AltitudeAssignment struct {
	ControllerToken string
	Callsign        string
	Altitude        int
}

func (s *Server) AssignAltitude(alt *AltitudeAssignment, response *string) error {
	return s.dispatchControllingCommand(alt.ControllerToken, alt.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.AssignAltitude(alt.Altitude) },
		response)
}

func (s *Server) SetTemporaryAltitude(alt *AltitudeAssignment, response *string) error {
	return s.dispatchTrackingCommand(alt.ControllerToken, alt.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			ac.TempAltitude = alt.Altitude
			return "", nil
		}, response)
}

type HeadingAssignment struct {
	ControllerToken string
	Callsign        string
	Heading         int
	Present         bool
	LeftDegrees     int
	RightDegrees    int
	Turn            TurnMethod
}

func (s *Server) AssignHeading(hdg *HeadingAssignment, response *string) error {
	return s.dispatchControllingCommand(hdg.ControllerToken, hdg.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if hdg.Present {
				if _, err := ac.AssignHeading(int(ac.Heading), TurnClosest); err == nil {
					return "fly present heading", nil
				} else {
					return "", err
				}
			} else if hdg.LeftDegrees != 0 {
				return ac.TurnLeft(hdg.LeftDegrees)
			} else if hdg.RightDegrees != 0 {
				return ac.TurnRight(hdg.RightDegrees)
			} else {
				return ac.AssignHeading(hdg.Heading, hdg.Turn)
			}
		}, response)
}

type SpeedAssignment struct {
	ControllerToken string
	Callsign        string
	Speed           int
}

func (s *Server) AssignSpeed(sa *SpeedAssignment, response *string) error {
	return s.dispatchControllingCommand(sa.ControllerToken, sa.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.AssignSpeed(sa.Speed) },
		response)
}

type FixSpecifier struct {
	ControllerToken string
	Callsign        string
	Fix             string
	Heading         int
	Altitude        int
	Speed           int
}

func (s *Server) DirectFix(f *FixSpecifier, response *string) error {
	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.DirectFix(f.Fix) },
		response)
}

func (s *Server) DepartFixHeading(f *FixSpecifier, response *string) error {
	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.DepartFixHeading(f.Fix, f.Heading) },
		response)
}

func (s *Server) CrossFixAt(f *FixSpecifier, response *string) error {
	return s.dispatchControllingCommand(f.ControllerToken, f.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.CrossFixAt(f.Fix, f.Altitude, f.Speed) },
		response)
}

type ApproachAssignment struct {
	ControllerToken string
	Callsign        string
	Approach        string
}

func (s *Server) ExpectApproach(a *ApproachAssignment, response *string) error {
	return s.dispatchControllingCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) { return ac.ExpectApproach(a.Approach) },
		response)
}

type ApproachClearance struct {
	ControllerToken string
	Callsign        string
	Approach        string
	StraightIn      bool
}

func (s *Server) ClearedApproach(c *ApproachClearance, response *string) error {
	return s.dispatchControllingCommand(c.ControllerToken, c.Callsign,
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			if c.StraightIn {
				return ac.ClearedStraightInApproach(c.Approach)
			} else {
				return ac.ClearedApproach(c.Approach)
			}
		}, response)
}

func (s *Server) DeleteAircraft(a *AircraftSpecifier, _ *struct{}) error {
	return s.dispatchCommand(a.ControllerToken, a.Callsign,
		func(ctrl *Controller, ac *Aircraft) error { return nil },
		func(ctrl *Controller, ac *Aircraft) (string, error) {
			eventStream.Post(&RemovedAircraftEvent{ac: ac})
			delete(s.sim.Aircraft, ac.Callsign)
			return "", nil
		}, nil)
}

type ServerUpdates struct {
	// events
	Events   []interface{} // GACK: no go for gob encoding...
	Aircraft map[string]*Aircraft
}

func (s *Server) GetUpdates(token string, u *ServerUpdates) error {
	return nil
}
