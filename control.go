// control.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
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

// AircraftController defines the interface that servers must implement to
// allow control actions to be performed--things like assigning squawk
// codes, etc.  Note that this is essentially a one-way interface: the only
// response from these methods is error codes; using AircraftController,
// the controller can't make any queries about aircraft, etc.
//
// Note that:
// 1. All methods start with a verb: "do this thing".
// 2. Aircraft and controllers are identified by their callsigns
// represented by strings. We assume these are all unique!
type AircraftController interface {
	// Clearance delivery (and related)
	// SetSquawk assigns the specified beacon code to the aircraft.
	SetSquawk(callsign string, squawk Squawk) error

	// SetSquawkAutomatic automatically assigns a squawk code to an IFR
	// aircraft using an unused one from the range of valid squawk codes
	// from the sector file. It returns an error for VFR aircraft.
	SetSquawkAutomatic(callsign string) error

	// SetScratchpad sets the aircraft's scratchpad string.  An empty string
	// clears the scratchpad.
	SetScratchpad(callsign string, scratchpad string) error

	// SetTemporaryAltitude assigns the given temporary altitude (specified
	// in feet) to the aircraft.
	SetTemporaryAltitude(callsign string, alt int) error

	SetVoiceType(callsign string, voice VoiceCapability) error
	AmendFlightPlan(callsign string, fp FlightPlan) error
	PushFlightStrip(callsign string, controller string) error

	// Tracking aircraft
	InitiateTrack(callsign string) error
	DropTrack(callsign string) error
	Handoff(callsign string, controller string) error
	AcceptHandoff(callsign string) error
	RejectHandoff(callsign string) error
	PointOut(callsign string, controller string) error

	SendTextMessage(m TextMessage) error

	// SetRadarCenters specifies the primary and up to 3 secondary radar
	// centers for the controller (as well as the radar range).
	SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error

	// Disconnect shuts down the connection with the server and cleans up
	// detritus.
	Disconnect()
}

// ATCServer includes both the AircraftController interface and adds
// additional methods for querying the state of the world, as represented
// by the remote server.  In a sense, it provides the second half of the
// AircraftController interface.
type ATCServer interface {
	AircraftController

	// GetAircraft returns an *Aircraft for the specified callsign (or nil
	// if no such aircraft exists.)  This pointer may be safely stored by
	// the caller; it will remain the same for the aircraft throughout the
	// time it's being tracked by the controller.
	GetAircraft(callsign string) *Aircraft

	// GetFilteredAircraft returns a slice of all aircraft for which the
	// provided filter callback function returns true.
	GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft

	// GetAllAircraft returns a slice of *Aircraft for all of the known
	// aircraft.
	GetAllAircraft() []*Aircraft

	// GetFlightStrip returns the flightstrip for the specified aircraft.
	// Like the *Aircraft returned by GetAircraft, the flightstrip pointer
	// remains valid as long as the aircraft is present on the radar.
	GetFlightStrip(callsign string) *FlightStrip

	// AddAirportForWeather adds the specified airport to the list of airports
	// for which the weather is requested.  (Note that there is currently no
	// method to remove airports from this list.)
	AddAirportForWeather(airport string)

	// GetMETAR returns the most recent weather report for the specified
	// airport.  (For a successful result, the airport must have previously
	// been given to AddAirportForWeather.)
	GetMETAR(location string) *METAR

	// GetATIS returns the most recent ATIS that has been broadcast for the
	// specified airport.  Note that unlike METAR, there's no need to
	// specify the airport ahead of time.
	GetATIS(airport string) []ATIS

	GetUser(callsign string) *User
	GetController(callsign string) *Controller
	GetAllControllers() []*Controller

	// GetTrackingController returns the callsign (e.g., PHL_TWR) of the
	// controller that is tracking the aircraft with the given callsign.
	// An empty string is returned if no controller is tracking it.
	GetTrackingController(callsign string) string

	// InboundHandoffController returns the callsign of the controller
	// (e.g., JFK_APP), who has offered a handoff of the given aircraft to
	// the current controller.  An empty string is returned if a handoff
	// offer has not been made for the aircraft.
	InboundHandoffController(callsign string) string

	// OutboundHandoffController returns the controller to which the
	// current controller has offered a handoff of the specified aircraft.
	OutboundHandoffController(callsign string) string

	SetPrimaryFrequency(f Frequency)

	// GetUpdates causes the server to process inbound network messages to
	// update its representation of the world.  In addition to updating its
	// internal data structures (and thence, things like *Aircraft)
	// returned earlier by methods like GetAircraft, it also updates the
	// global EventStream with information about the changes seen.
	GetUpdates()

	// Connected reports if the server connection is active.  (Note that
	// the connection may unexpectedly be closed by the server for errors
	// like an invalid password or a network timeout.)
	Connected() bool

	// Callsign eturns the callsign the user is signed in under (e.g.,
	// "JFK_TWR")
	Callsign() string

	// CurrentTime returns the ~current time; getting it from the server
	// lets us report the past time when replaying traces, etc.
	CurrentTime() time.Time

	// GetWindowTitle returns a string that summarizes useful details of
	// the server connection for use in the titlebar of the vice window
	GetWindowTitle() string
}

// TextMessage is used to represent all the types of text message that may be sent
// and received.
type TextMessage struct {
	sender      string
	messageType TextMessageType
	contents    string
	frequencies []Frequency // only used for messageType == TextFrequency
	recipient   string      // only used for TextPrivate
}

func (t *TextMessage) String() string {
	if t.messageType == TextFrequency {
		return fmt.Sprintf("TextMessage: sender: %s, freq: %v, contents: %s", t.sender,
			t.frequencies, t.contents)
	} else if t.messageType == TextPrivate {
		return fmt.Sprintf("TextMessage: sender: %s, recipient: %s, contents: %s", t.sender,
			t.recipient, t.contents)
	} else {
		return fmt.Sprintf("TextMessage: sender: %s, type: %s, contents: %s", t.sender,
			t.messageType.String(), t.contents)
	}
}

// TextMessageType is an enumerant that indicates the type of text message.
type TextMessageType int

const (
	// Broadcast (only from supervisors / admins)
	TextBroadcast = iota
	// Wallop
	TextWallop
	// Inter-ATC messaging
	TextATC
	// Message sent on one or more radio frequencies
	TextFrequency
	// Private message to another controller or aircraft
	TextPrivate
)

func (t TextMessageType) String() string {
	return [...]string{"Broadcast", "Wallop", "ATC", "Frequency", "Private"}[t]
}

///////////////////////////////////////////////////////////////////////////
// InertAircraftController

// InertAircraftController implements the AircraftController interface but does nothing,
// returning ErrNoConnection for all method calls.  With it, vice can assume that there
// is always some AircraftController available; this is used when vice first starts up,
// before the user has initiated a connection, for example.
type InertAircraftController struct{}

func (*InertAircraftController) SetSquawk(callsign string, squawk Squawk) error {
	return ErrNoConnection
}
func (*InertAircraftController) SetSquawkAutomatic(callsign string) error {
	return ErrNoConnection
}
func (*InertAircraftController) SetScratchpad(callsign string, scratchpad string) error {
	return ErrNoConnection
}
func (*InertAircraftController) SetTemporaryAltitude(callsign string, alt int) error {
	return ErrNoConnection
}
func (*InertAircraftController) SetVoiceType(callsign string, cap VoiceCapability) error {
	return ErrNoConnection
}
func (*InertAircraftController) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return ErrNoConnection
}
func (*InertAircraftController) PushFlightStrip(callsign string, controller string) error {
	return ErrNoConnection
}
func (*InertAircraftController) InitiateTrack(callsign string) error {
	return ErrNoConnection
}
func (*InertAircraftController) DropTrack(callsign string) error {
	return ErrNoConnection
}
func (*InertAircraftController) Handoff(callsign string, controller string) error {
	return ErrNoConnection
}
func (*InertAircraftController) AcceptHandoff(callsign string) error {
	return ErrNoConnection
}
func (*InertAircraftController) RejectHandoff(callsign string) error {
	return ErrNoConnection
}
func (*InertAircraftController) PointOut(callsign string, controller string) error {
	return ErrNoConnection
}
func (*InertAircraftController) SendTextMessage(m TextMessage) error {
	return ErrNoConnection
}
func (*InertAircraftController) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	return ErrNoConnection
}
func (*InertAircraftController) Disconnect() {}

///////////////////////////////////////////////////////////////////////////
// DisconnectedATCServer

// DisconnectedATCServer continues the InertAircraftController theme and
// implements the ATCServer interface, more or less without doing anything.
// It is the initial server after vice starts up and is used as the server
// after the user disconnects from an actual VATSIM server.
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

func (d *DisconnectedATCServer) GetFlightStrip(callsign string) *FlightStrip {
	return nil
}

func (d *DisconnectedATCServer) GetMETAR(location string) *METAR {
	return nil
}

func (d *DisconnectedATCServer) GetATIS(airport string) []ATIS {
	return nil
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

func (d *DisconnectedATCServer) SetPrimaryFrequency(f Frequency) {}

func (d *DisconnectedATCServer) GetUpdates() {}

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
