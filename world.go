// world.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
)

const initialSimSeconds = 45

var (
	ErrArrivalAirportUnknown        = errors.New("Arrival airport unknown")
	ErrUnknownApproach              = errors.New("Unknown approach")
	ErrClearedForUnexpectedApproach = errors.New("Cleared for unexpected approach")
	ErrNoAircraftForCallsign        = errors.New("No aircraft exists with specified callsign")
	ErrNoFlightPlan                 = errors.New("No flight plan has been filed for aircraft")
	ErrOtherControllerHasTrack      = errors.New("Another controller is already tracking the aircraft")
	ErrNotBeingHandedOffToMe        = errors.New("Aircraft not being handed off to current controller")
	ErrNoController                 = errors.New("No controller with that callsign")
	ErrUnknownAircraftType          = errors.New("Unknown aircraft type")
	ErrUnableCommand                = errors.New("Unable")
	ErrInvalidAltitude              = errors.New("Altitude above aircraft's ceiling")
	ErrInvalidHeading               = errors.New("Invalid heading")
	ErrInvalidApproach              = errors.New("Invalid approach")
	ErrInvalidCommandSyntax         = errors.New("Invalid command syntax")
	ErrFixNotInRoute                = errors.New("Fix not in aircraft's route")
)

///////////////////////////////////////////////////////////////////////////
// World

type World struct {
	token string

	Aircraft    map[string]*Aircraft
	METAR       map[string]*METAR
	Controllers map[string]*Controller

	DepartureAirports map[string]*Airport
	ArrivalAirports   map[string]*Airport

	eventsId EventSubscriberId

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	MagneticVariation             float32
	NmPerLatitude, NmPerLongitude float32
	Airports                      map[string]*Airport
	Fixes                         map[string]Point2LL
	PrimaryAirport                string
	RadarSites                    map[string]*RadarSite
	Center                        Point2LL
	Range                         float32
	DefaultMap                    string
	STARSMaps                     []STARSMap
	Wind                          Wind
	Callsign                      string
	ApproachAirspace              []AirspaceVolume
	DepartureAirspace             []AirspaceVolume
	DepartureRunways              []ScenarioGroupDepartureRunway
	Scratchpads                   map[string]string
	ArrivalGroups                 map[string][]Arrival
}

func (w *World) GetWindVector(p Point2LL, alt float32) Point2LL {
	return sim.GetWindVector(p, alt)
}

func (w *World) GetAirport(icao string) *Airport {
	return w.Airports[icao]
}

func (w *World) Locate(s string) (Point2LL, bool) {
	s = strings.ToUpper(s)
	// ScenarioGroup's definitions take precedence...
	if ap, ok := w.Airports[s]; ok {
		return ap.Location, true
	} else if p, ok := w.Fixes[s]; ok {
		return p, true
	} else if n, ok := database.Navaids[strings.ToUpper(s)]; ok {
		return n.Location, ok
	} else if ap, ok := database.Airports[strings.ToUpper(s)]; ok {
		return ap.Location, ok
	} else if f, ok := database.Fixes[strings.ToUpper(s)]; ok {
		return f.Location, ok
	} else if p, err := ParseLatLong([]byte(s)); err == nil {
		return p, true
	} else {
		return Point2LL{}, false
	}
}

func (w *World) AllAirports() map[string]*Airport {
	all := DuplicateMap(w.DepartureAirports)
	for name, ap := range w.ArrivalAirports {
		all[name] = ap
	}
	return all
}

func (w *World) SetSquawk(callsign string, squawk Squawk) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetSquawkAutomatic(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) SetScratchpad(callsign string, scratchpad string) error {
	return sim.SetScratchpad(&AircraftPropertiesSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
		Scratchpad:      scratchpad,
	}, nil)
}

func (w *World) SetTemporaryAltitude(callsign string, alt int) error {
	return sim.SetTemporaryAltitude(&AltitudeAssignment{
		ControllerToken: w.token,
		Callsign:        callsign,
		Altitude:        alt,
	}, nil)
}

func (w *World) AmendFlightPlan(callsign string, fp FlightPlan) error {
	return nil // UNIMPLEMENTED
}

func (w *World) InitiateTrack(callsign string) error {
	return sim.InitiateTrack(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) DropTrack(callsign string) error {
	return sim.DropTrack(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) HandoffTrack(callsign string, controller string) error {
	return sim.HandoffTrack(&HandoffSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
		Controller:      controller,
	}, nil)
}

func (w *World) HandoffControl(callsign string) error {
	return sim.HandoffControl(&HandoffSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) AcceptHandoff(callsign string) error {
	return sim.AcceptHandoff(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) RejectHandoff(callsign string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) CancelHandoff(callsign string) error {
	return sim.CancelHandoff(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        callsign,
	}, nil)
}

func (w *World) PointOut(callsign string, controller string) error {
	return nil // UNIMPLEMENTED
}

func (w *World) Disconnect() {
	if err := sim.SignOff(w.token, nil); err != nil {
		lg.Errorf("Error signing off from sim: %v", err)
	}
	w.Aircraft = nil
	w.Controllers = nil

	if w.eventsId != InvalidEventSubscriberId {
		eventStream.Unsubscribe(w.eventsId)
		w.eventsId = InvalidEventSubscriberId
	}
}

func (w *World) GetAircraft(callsign string) *Aircraft {
	if ac, ok := w.Aircraft[callsign]; ok {
		return ac
	}
	return nil
}

func (w *World) GetFilteredAircraft(filter func(*Aircraft) bool) []*Aircraft {
	var filtered []*Aircraft
	for _, ac := range w.Aircraft {
		if filter(ac) {
			filtered = append(filtered, ac)
		}
	}
	return filtered
}

func (w *World) GetAllAircraft() []*Aircraft {
	return w.GetFilteredAircraft(func(*Aircraft) bool { return true })
}

func (w *World) GetFlightStrip(callsign string) *FlightStrip {
	if ac, ok := w.Aircraft[callsign]; ok {
		return &ac.Strip
	}
	return nil
}

func (w *World) AddAirportForWeather(airport string) {
	// UNIMPLEMENTED
}

func (w *World) GetMETAR(location string) *METAR {
	return w.METAR[location]
}

func (w *World) GetAirportATIS(airport string) []ATIS {
	// UNIMPLEMENTED
	return nil
}

func (w *World) GetController(callsign string) *Controller {
	if ctrl := w.Controllers[callsign]; ctrl != nil {
		return ctrl
	}

	// Look up by id
	for _, ctrl := range w.Controllers {
		if ctrl.SectorId == callsign {
			return ctrl
		}
	}

	return nil
}

func (w *World) GetAllControllers() map[string]*Controller {
	return w.Controllers
}

func (w *World) GetUpdates() {
	if sim != nil {
		sim.Update()
	}
}

func (w *World) Connected() bool {
	return sim != nil
}

func (w *World) CurrentTime() time.Time {
	if sim == nil {
		return time.Time{}
	}
	return sim.CurrentTime()
}

func (w *World) GetWindowTitle() string {
	if sim == nil {
		return "(disconnected)"
	}
	return w.Callsign + ": " + sim.Description()
}

func (w *World) AssignAltitude(ac *Aircraft, altitude int) error {
	return sim.AssignAltitude(&AltitudeAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Altitude:        altitude,
	}, nil)
}

func (w *World) AssignHeading(ac *Aircraft, heading int, turn TurnMethod) error {
	return sim.AssignHeading(&HeadingAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Heading:         heading,
		Turn:            turn,
	}, nil)
}

func (w *World) FlyPresentHeading(ac *Aircraft) error {
	return sim.AssignHeading(&HeadingAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Present:         true,
	}, nil)
}

func (w *World) TurnLeft(ac *Aircraft, deg int) error {
	return sim.AssignHeading(&HeadingAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		LeftDegrees:     deg,
	}, nil)
}

func (w *World) TurnRight(ac *Aircraft, deg int) error {
	return sim.AssignHeading(&HeadingAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		RightDegrees:    deg,
	}, nil)
}

func (w *World) AssignSpeed(ac *Aircraft, speed int) error {
	return sim.AssignSpeed(&SpeedAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Speed:           speed,
	}, nil)
}

func (w *World) DirectFix(ac *Aircraft, fix string) error {
	return sim.DirectFix(&FixSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
	}, nil)
}

func (w *World) DepartFixHeading(ac *Aircraft, fix string, hdg int) error {
	return sim.DepartFixHeading(&FixSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
		Heading:         hdg,
	}, nil)
}

func (w *World) CrossFixAt(ac *Aircraft, fix string, alt int, speed int) error {
	return sim.CrossFixAt(&FixSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Fix:             fix,
		Altitude:        alt,
		Speed:           speed,
	}, nil)
}

func (w *World) ExpectApproach(ac *Aircraft, approach string) error {
	return sim.ExpectApproach(&ApproachAssignment{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
	}, nil)
}

func (w *World) ClearedApproach(ac *Aircraft, approach string) error {
	return sim.ClearedApproach(&ApproachClearance{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
	}, nil)
}

func (w *World) ClearedStraightInApproach(ac *Aircraft, approach string) error {
	return sim.ClearedApproach(&ApproachClearance{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
		Approach:        approach,
		StraightIn:      true,
	}, nil)
}

func (w *World) GoAround(ac *Aircraft) error {
	return sim.GoAround(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
	}, nil)
}

func (w *World) PrintInfo(ac *Aircraft) error {
	lg.Errorf("%s", spew.Sdump(ac))

	s := fmt.Sprintf("%s: current alt %f, heading %f, IAS %.1f, GS %.1f",
		ac.Callsign, ac.Altitude, ac.Heading, ac.IAS, ac.GS)
	if ac.ApproachCleared {
		s += ", cleared approach"
	}
	lg.Errorf("%s", s)
	return nil
}

func (w *World) DeleteAircraft(ac *Aircraft) error {
	return sim.DeleteAircraft(&AircraftSpecifier{
		ControllerToken: w.token,
		Callsign:        ac.Callsign,
	}, nil)
}

func (w *World) RunAircraftCommands(ac *Aircraft, cmds string) ([]string, error) {
	commands := strings.Fields(cmds)
	for i, command := range commands {
		switch command[0] {
		case 'D':
			if components := strings.Split(command, "/"); len(components) > 1 {
				// Depart <fix> at heading <hdg>
				fix := components[0][1:]

				if components[1][0] != 'H' {
					return commands[i:], ErrInvalidCommandSyntax
				}
				if hdg, err := strconv.Atoi(components[1][1:]); err != nil {
					return commands[i:], err
				} else if err := w.DepartFixHeading(ac, fix, hdg); err != nil {
					return commands[i:], err
				}
			} else if len(command) > 1 && command[1] >= '0' && command[1] <= '9' {
				// Looks like an altitude.
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := w.AssignAltitude(ac, 100*alt); err != nil {
					return commands[i:], err
				}
			} else if _, ok := w.Locate(string(command[1:])); ok {
				if err := w.DirectFix(ac, command[1:]); err != nil {
					return commands[i:], err
				}
			} else {
				return commands[i:], ErrInvalidCommandSyntax
			}

		case 'H':
			if len(command) == 1 {
				if err := w.FlyPresentHeading(ac); err != nil {
					return commands[i:], err
				}
			} else if hdg, err := strconv.Atoi(command[1:]); err != nil {
				return commands[i:], err
			} else if err := w.AssignHeading(ac, hdg, TurnClosest); err != nil {
				return commands[i:], err
			}

		case 'L':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn left x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					return commands[i:], err
				} else if err := w.TurnLeft(ac, deg); err != nil {
					return commands[i:], err
				}
			} else {
				// turn left heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := w.AssignHeading(ac, hdg, TurnLeft); err != nil {
					return commands[i:], err
				}
			}

		case 'R':
			if l := len(command); l > 2 && command[l-1] == 'D' {
				// turn right x degrees
				if deg, err := strconv.Atoi(command[1 : l-1]); err != nil {
					return commands[i:], err
				} else if err := w.TurnRight(ac, deg); err != nil {
					return commands[i:], err
				}
			} else {
				// turn right heading...
				if hdg, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := w.AssignHeading(ac, hdg, TurnRight); err != nil {
					return commands[i:], err
				}
			}

		case 'C', 'A':
			if len(command) > 4 && command[:3] == "CSI" && !isAllNumbers(command[3:]) {
				// Cleared straight in approach.
				if err := w.ClearedStraightInApproach(ac, command[3:]); err != nil {
					return commands[i:], err
				}
			} else if command[0] == 'C' && len(command) > 2 && !isAllNumbers(command[1:]) {
				if components := strings.Split(command, "/"); len(components) > 1 {
					// Cross fix [at altitude] [at speed]
					fix := components[0][1:]
					alt, speed := 0, 0

					for _, cmd := range components[1:] {
						if len(cmd) == 0 {
							return commands[i:], ErrInvalidCommandSyntax
						}

						var err error
						if cmd[0] == 'A' {
							if alt, err = strconv.Atoi(cmd[1:]); err != nil {
								return commands[i:], err
							}
						} else if cmd[0] == 'S' {
							if speed, err = strconv.Atoi(cmd[1:]); err != nil {
								return commands[i:], err
							}
						} else {
							return commands[i:], ErrInvalidCommandSyntax
						}
					}

					if err := w.CrossFixAt(ac, fix, 100*alt, speed); err != nil {
						return commands[i:], err
					}
				} else if err := w.ClearedApproach(ac, command[1:]); err != nil {
					return commands[i:], err
				}
			} else {
				// Otherwise look for an altitude
				if alt, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := w.AssignAltitude(ac, 100*alt); err != nil {
					return commands[i:], err
				}
			}

		case 'S':
			if len(command) == 1 {
				// Cancel speed restrictions
				if err := w.AssignSpeed(ac, 0); err != nil {
					return commands[i:], err
				}
			} else {
				if kts, err := strconv.Atoi(command[1:]); err != nil {
					return commands[i:], err
				} else if err := w.AssignSpeed(ac, kts); err != nil {
					return commands[i:], err
				}
			}

		case 'E':
			// Expect approach.
			if len(command) > 1 {
				if err := w.ExpectApproach(ac, command[1:]); err != nil {
					return commands[i:], err
				}
			} else {
				return commands[i:], ErrInvalidCommandSyntax
			}

		case '?':
			if err := w.PrintInfo(ac); err != nil {
				return commands[i:], err
			}

		case 'X':
			if err := w.DeleteAircraft(ac); err != nil {
				return commands[i:], err
			}

		default:
			return commands[i:], ErrInvalidCommandSyntax
		}
	}
	return nil, nil
}
