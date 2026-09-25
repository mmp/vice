// sim/sessionlog.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"github.com/mmp/vice/simlog"
	"github.com/mmp/vice/util"

	"github.com/vmihailenco/msgpack/v5"
)

// A server records each sim it runs in a session log (see package simlog).
// Replaying the session means starting a sim from the snapshot the log begins
// with and making the requests the log records at the ticks it records them.
// The sim writes the rest of the log itself: the aircraft that enter and
// leave it, the weather it flies in, and where every aircraft is each second.

// Restart returns the snapshot a session log starts with, the sim encoded,
// along with a sim decoded from it to run in s's place. A replay decodes the
// same snapshot, so it starts from exactly the state the session does:
// encoding doesn't carry everything a sim holds (pointers two parts of it
// share come back as separate copies, for one), and the session has to lose
// the same things the replay does.
//
// The text controllers type for each other to read, the general information
// text and flight strip annotations, is left out of the snapshot, as is the
// sim's description, which carries the name its creator gave it; the sim
// returned keeps them.
func Restart(s *Sim) ([]byte, *Sim, error) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	description, giText := s.State.SimDescription, s.State.GIText
	annotations := make(map[ACID][9]string)
	for fp := range s.flightPlans() {
		annotations[fp.ACID] = fp.StripAnnotations
		fp.StripAnnotations = [9]string{}
	}
	s.State.SimDescription, s.State.GIText = "", [10]string{}

	snapshot, err := msgpack.Marshal(s)

	s.State.SimDescription, s.State.GIText = description, giText
	for fp := range s.flightPlans() {
		fp.StripAnnotations = annotations[fp.ACID]
	}
	if err != nil {
		return nil, nil, err
	}

	r, err := DecodeSnapshot(snapshot)
	if err != nil {
		return nil, nil, err
	}
	r.State.SimDescription, r.State.GIText = description, giText
	for fp := range r.flightPlans() {
		fp.StripAnnotations = annotations[fp.ACID]
	}
	// The model isn't part of the snapshot; the grids it has installed go
	// into the log when it is attached.
	r.wxModel = s.wxModel
	r.textRand = s.textRand
	r.lg = s.lg

	return snapshot, r, nil
}

// DecodeSnapshot returns the sim a session log's snapshot holds. Like a sim
// read back from the user's configuration, it has to be activated before it
// runs.
func DecodeSnapshot(b []byte) (*Sim, error) {
	var s Sim
	if err := msgpack.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SetSessionLog sets the log the sim records what happens in it to, or stops
// its recording if w is nil. The log starts with the grids the weather model
// has already installed and the aircraft already in the sim.
func (s *Sim) SetSessionLog(w *simlog.Writer) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.sessionLog = w
	if w != nil {
		for _, u := range s.wxModel.Installed() {
			w.Weather(simlog.Weather{Time: s.State.SimTime.Time(), Update: u})
		}
		for _, ac := range util.SortedMap(s.Aircraft) {
			s.logSpawn(ac)
		}
	}
}

func (s *Sim) logSpawn(ac *Aircraft) {
	if s.sessionLog != nil {
		s.sessionLog.Spawn(simlog.Spawn{
			Time:         s.State.SimTime.Time(),
			Callsign:     string(ac.ADSBCallsign),
			AircraftType: ac.FlightPlan.AircraftType,
			Rules:        ac.FlightPlan.Rules.String(),
			Flight:       ac.TypeOfFlight.String(),
			Departure:    string(ac.FlightPlan.DepartureAirport),
			Arrival:      string(ac.FlightPlan.ArrivalAirport),
			Route:        ac.FlightPlan.Route,
		})
	}
}

func (s *Sim) logDelete(ac *Aircraft, reason DeleteReason) {
	if s.sessionLog != nil {
		s.sessionLog.Delete(simlog.Delete{
			Time:     s.State.SimTime.Time(),
			Callsign: string(ac.ADSBCallsign),
			Reason:   reason.String(),
		})
	}
}

// logTick records where each aircraft the tick moved ended up.
func (s *Sim) logTick() {
	if s.sessionLog == nil {
		return
	}
	var samples []simlog.Sample
	for callsign, ac := range util.SortedMap(s.Aircraft) {
		active := !ac.WaitingForLaunch && (!ac.HoldForRelease || ac.Released)
		if active {
			fs := ac.Nav.FlightState
			samples = append(samples, simlog.Sample{
				Callsign: string(callsign),
				Position: fs.Position,
				Altitude: fs.Altitude,
				IAS:      fs.IAS,
				GS:       fs.GS,
				Heading:  float32(fs.Heading),
			})
		}
	}
	s.sessionLog.Tick(simlog.Tick{Time: s.State.SimTime.Time(), Aircraft: samples})
}
