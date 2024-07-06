package sim

import (
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/math"
)

type ClientState struct {
	scopeDraw struct {
		arrivals   map[string]map[int]bool               // group->index
		approaches map[string]map[string]bool            // airport->approach
		departures map[string]map[string]map[string]bool // airport->runway->exit
	}
}

func (c ClientState) ScopeDrawArrivals() map[string]map[int]bool {
	return c.scopeDraw.arrivals
}

func (c ClientState) ScopeDrawApproaches() map[string]map[string]bool {
	return c.scopeDraw.approaches
}

func (c ClientState) ScopeDrawDepartures() map[string]map[string]map[string]bool {
	return c.scopeDraw.departures
}

type AircraftController interface {
	// TODO: Callsign() string

	Connected() bool

	// FIXME: this doesn't really belong here
	CurrentTime() time.Time

	SetSquawkAutomatic(callsign string) error
	SetSquawk(callsign string, squawk av.Squawk) error
	SetScratchpad(callsign string, scratchpad string, success func(any), err func(error))
	SetSecondaryScratchpad(callsign string, scratchpad string, success func(any), err func(error))
	SetTemporaryAltitude(callsign string, alt int, success func(any), err func(error))
	SetGlobalLeaderLine(callsign string, dir *math.CardinalOrdinalDirection, success func(any),
		err func(error))
	InitiateTrack(callsign string, success func(any), err func(error))
	DropTrack(callsign string, success func(any), err func(error))
	HandoffTrack(callsign string, controller string, success func(any), err func(error))
	AcceptHandoff(callsign string, success func(any), err func(error))
	RedirectHandoff(callsign, controller string, success func(any), err func(error))
	AcceptRedirectedHandoff(callsign string, success func(any), err func(error))
	CancelHandoff(callsign string, success func(any), err func(error))
	ForceQL(callsign, controller string, success func(any), err func(error))
	RemoveForceQL(callsign, controller string, success func(any), err func(error))
	PointOut(callsign string, controller string, success func(any), err func(error))
	AcknowledgePointOut(callsign string, success func(any), err func(error))
	RejectPointOut(callsign string, success func(any), err func(error))
	ToggleSPCOverride(callsign string, spc string, success func(any), err func(error))
	AmendFlightPlan(callsign string, fp av.FlightPlan) error

	SendGlobalMessage(global GlobalMessage)

	RunAircraftCommands(callsign string, cmds string,
		handleResult func(message string, remainingInput string))
}
