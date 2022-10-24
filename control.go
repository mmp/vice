// control.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"time"
)

var (
	ErrNoConnection            = errors.New("Not connected to a server")
	ErrNoAircraftForCallsign   = errors.New("No aircraft exists with specified callsign")
	ErrNoFlightPlan            = errors.New("No flight plan has been filed for aircraft")
	ErrScratchpadTooLong       = errors.New("Scratchpad too long: 3 character limit")
	ErrAirportTooLong          = errors.New("Airport name too long: 5 character limit")
	ErrOtherControllerHasTrack = errors.New("Another controller is already tracking the aircraft")
	ErrNotTrackedByMe          = errors.New("Aircraft is not tracked by current controller")
	ErrNotBeingHandedOffToMe   = errors.New("Aircraft not being handed off to current controller")
	ErrNoFlightPlanFiled       = errors.New("No flight plan filed for aircraft")
	ErrNoController            = errors.New("No controller with that callsign")
	ErrNoControllerOrAircraft  = errors.New("No controller or aircraft with that callsign")
	ErrNotController           = errors.New("Not signed in to a controller position")
)

// ATCServer defines the interface that servers must implement; these
// are mostly things where vice is requesting the server to change some
// thing--update the squawk code for an aircraft, etc.  The implementations
// of this interface in vice are FlightRadarServer and VATSIMServer.
//
// Note that:
// 1. All methods start with a verb: "do this thing".
// 2. Aircraft and controllers are identified by their callsigns
// represented by strings. We assume these are all unique!
type AircraftController interface {
	// Clearance delivery (and related)
	SetSquawk(callsign string, squawk Squawk) error
	SetSquawkAutomatic(callsign string) error
	SetScratchpad(callsign string, scratchpad string) error
	SetTemporaryAltitude(callsign string, alt int) error
	SetVoiceType(callsign string, voice string) error
	AmendFlightPlan(callsign string, fp FlightPlan) error

	PushFlightStrip(fs FlightStrip, controller string) error

	// Tracking aircraft
	InitiateTrack(callsign string) error
	DropTrack(callsign string) error
	Handoff(callsign string, controller string) error
	AcceptHandoff(callsign string) error
	RejectHandoff(callsign string) error
	PointOut(callsign string, controller string) error

	SendTextMessage(m TextMessage) error
	SendRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error
}

type ATCServer interface {
	AircraftController

	GetAircraft(callsign string) *Aircraft
	GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft
	GetAllAircraft() []*Aircraft
	GetMETAR(location string) *METAR
	GetATIS(airport string) string
	GetUser(callsign string) *User
	GetController(callsign string) *Controller
	GetAllControllers() []*Controller
	GetTrackingController(callsign string) string
	InboundHandoffController(callsign string) string
	OutboundHandoffController(callsign string) string

	AddAirportForWeather(airport string)

	// Check for updates from the server.
	GetUpdates()

	// Shut down the connection with the server and clean up detritus.
	Disconnect()
	Connected() bool

	// Returns the callsign the user is signed in under (e.g., "JFK_TWR")
	Callsign() string

	// Returns the ~current time; getting it from the server lets us report
	// the past time when replaying traces, etc.
	CurrentTime() time.Time

	GetWindowTitle() string
}

type ControlUpdates struct {
	addedAircraft    map[*Aircraft]interface{}
	modifiedAircraft map[*Aircraft]interface{}
	removedAircraft  map[*Aircraft]interface{}
	pointOuts        map[*Aircraft]string
	offeredHandoffs  map[*Aircraft]string
	acceptedHandoffs map[*Aircraft]string
	rejectedHandoffs map[*Aircraft]string

	messages []TextMessage
}

func NewControlUpdates() *ControlUpdates {
	c := &ControlUpdates{}
	c.addedAircraft = make(map[*Aircraft]interface{})
	c.modifiedAircraft = make(map[*Aircraft]interface{})
	c.removedAircraft = make(map[*Aircraft]interface{})
	c.pointOuts = make(map[*Aircraft]string)
	c.offeredHandoffs = make(map[*Aircraft]string)
	c.acceptedHandoffs = make(map[*Aircraft]string)
	c.rejectedHandoffs = make(map[*Aircraft]string)
	return c
}

func (c *ControlUpdates) Reset() {
	c.addedAircraft = make(map[*Aircraft]interface{})
	c.modifiedAircraft = make(map[*Aircraft]interface{})
	c.removedAircraft = make(map[*Aircraft]interface{})
	c.pointOuts = make(map[*Aircraft]string)
	c.offeredHandoffs = make(map[*Aircraft]string)
	c.acceptedHandoffs = make(map[*Aircraft]string)
	c.rejectedHandoffs = make(map[*Aircraft]string)
	c.messages = c.messages[:0]
}

func (c *ControlUpdates) RemoveAircraft(ac *Aircraft) {
	delete(c.addedAircraft, ac)
	delete(c.modifiedAircraft, ac)
	delete(c.pointOuts, ac)
	delete(c.offeredHandoffs, ac)
	delete(c.acceptedHandoffs, ac)
	delete(c.rejectedHandoffs, ac)

	controlUpdates.removedAircraft[ac] = nil
}

func (c *ControlUpdates) NoUpdates() bool {
	return len(c.addedAircraft) == 0 && len(c.modifiedAircraft) == 0 && len(c.removedAircraft) == 0 &&
		len(c.pointOuts) == 0 && len(c.offeredHandoffs) == 0 && len(c.acceptedHandoffs) == 0 &&
		len(c.rejectedHandoffs) == 0 && len(c.messages) == 0
}

///////////////////////////////////////////////////////////////////////////
// InertAircraftController

type InertAircraftController struct{}

func (*InertAircraftController) SetSquawk(callsign string, squawk Squawk) error          { return nil }
func (*InertAircraftController) SetSquawkAutomatic(callsign string) error                { return nil }
func (*InertAircraftController) SetScratchpad(callsign string, scratchpad string) error  { return nil }
func (*InertAircraftController) SetTemporaryAltitude(callsign string, alt int) error     { return nil }
func (*InertAircraftController) SetVoiceType(callsign string, voice string) error        { return nil }
func (*InertAircraftController) AmendFlightPlan(callsign string, fp FlightPlan) error    { return nil }
func (*InertAircraftController) PushFlightStrip(fs FlightStrip, controller string) error { return nil }
func (*InertAircraftController) InitiateTrack(callsign string) error                     { return nil }
func (*InertAircraftController) DropTrack(callsign string) error                         { return nil }
func (*InertAircraftController) Handoff(callsign string, controller string) error        { return nil }
func (*InertAircraftController) AcceptHandoff(callsign string) error                     { return nil }
func (*InertAircraftController) RejectHandoff(callsign string) error                     { return nil }
func (*InertAircraftController) PointOut(callsign string, controller string) error       { return nil }
func (*InertAircraftController) SendTextMessage(m TextMessage) error                     { return nil }
func (*InertAircraftController) SendRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	return nil
}

///////////////////////////////////////////////////////////////////////////
// DisconnectedATCServer

type DisconnectedATCServer struct {
	InertAircraftController
}

func (d *DisconnectedATCServer) GetAircraft(callsign string) *Aircraft {
	return nil
}

func (d *DisconnectedATCServer) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	return nil
}

func (d *DisconnectedATCServer) GetAllAircraft() []*Aircraft {
	return nil
}

func (d *DisconnectedATCServer) GetMETAR(location string) *METAR {
	return nil
}

func (d *DisconnectedATCServer) GetATIS(airport string) string {
	return ""
}

func (d *DisconnectedATCServer) GetUser(callsign string) *User {
	return nil
}

func (d *DisconnectedATCServer) GetController(callsign string) *Controller {
	return nil
}

func (d *DisconnectedATCServer) GetAllControllers() []*Controller {
	return nil
}

func (d *DisconnectedATCServer) GetTrackingController(callsign string) string {
	return ""
}

func (d *DisconnectedATCServer) InboundHandoffController(callsign string) string {
	return ""
}

func (d *DisconnectedATCServer) OutboundHandoffController(callsign string) string {
	return ""
}

func (d *DisconnectedATCServer) AddAirportForWeather(airport string) {}

func (d *DisconnectedATCServer) GetUpdates() {}

func (d *DisconnectedATCServer) Disconnect() {}

func (d *DisconnectedATCServer) Connected() bool {
	return false
}

func (d *DisconnectedATCServer) Callsign() string {
	return "(none)"
}

func (d *DisconnectedATCServer) CurrentTime() time.Time {
	return time.Now()
}

func (d *DisconnectedATCServer) GetWindowTitle() string {
	return "[Disconnected]"
}
