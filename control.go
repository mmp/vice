// control.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

// ControlServer defines the interface that servers must implement; these
// are mostly things where vice is requesting the server to change some
// thing--update the squawk code for an aircraft, etc.  The implementations
// of this interface in vice are FlightRadarServer and VATSIMServer.
//
// Note that:
// 1. All methods start with a verb: "do this thing".
// 2. Aircraft and controllers are identified by their callsigns
// represented by strings. We assume these are all unique!
type ControlServer interface {
	// Clearance delivery (and related)
	SetSquawk(callsign string, squawk Squawk)
	SetScratchpad(callsign string, scratchpad string)
	SetRoute(callsign string, route string)
	SetDeparture(callsign string, airport string)
	SetArrival(callsign string, airport string)
	SetAltitude(callsign string, alt int)
	SetTemporaryAltitude(callsign string, alt int)
	SetAircraftType(callsign string, ac string)
	SetFlightRules(callsign string, r FlightRules)

	PushFlightStrip(callsign string, controller string)

	// Tracking aircraft
	InitiateTrack(callsign string)
	DropTrack(callsign string)
	Handoff(callsign string, controller string)
	AcceptHandoff(callsign string)
	RejectHandoff(callsign string)
	PointOut(callsign string, controller string)

	SendTextMessage(m TextMessage)

	// Check for updates from the server, which will in turn call methods
	// of its associated ControlClient to report what has changed.
	GetUpdates()

	// Shut down the connection with the server and clean up detritus.
	Disconnect()

	Description() string
	GetWindowTitle() string
}

// ControlClient defines the interface that ControlServers use to report
// changes to the state of the world back to the rest of the system;
// vice only includes a single implementation of this interface, in World,
// though we still keep this interface around in order to carefully specify
// the way in which the server reports back to vice.
//
// Note: all methods are named to indicate "this thing happened, FYI"
type ControlClient interface {
	METARReceived(m METAR)
	ATISReceived(issuer string, letter byte, contents string)

	UserAdded(callsign string, user User)
	PilotAdded(pilot Pilot)
	PilotRemoved(callsign string)
	ControllerAdded(controller Controller)
	ControllerRemoved(callsign string)

	SquawkAssigned(callsign string, squawk Squawk)
	FlightPlanReceived(fp FlightPlan)
	PositionReceived(callsign string, pos RadarTrack, squawk Squawk, mode TransponderMode)
	AltitudeAssigned(callsign string, altitude int)
	TemporaryAltitudeAssigned(callsign string, altitude int)
	VoiceSet(callsign string, vc VoiceCapability)
	ScratchpadSet(callsign string, contents string)
	FlightStripPushed(from string, to string, fs FlightStrip)

	TrackInitiated(callsign string, controller string)
	TrackDropped(callsign string, controller string)
	HandoffRequested(callsign string, from string, to string)
	HandoffAccepted(callsign string, from string, to string)
	HandoffRejected(callsign string, from string, to string)
	PointOutReceived(callsign string, from string, to string)

	TextMessageReceived(sender string, m TextMessage)
}
