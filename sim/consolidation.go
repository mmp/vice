// sim/consolidation.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"slices"

	"github.com/brunoga/deep"
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

// ControlPosition is an alias for av.ControlPosition for convenience.
type ControlPosition = av.ControlPosition

// TCP is an alias for ControlPosition, provided for clarity in STARS-specific code.
// Use TCP when the code is explicitly STARS-related; use ControlPosition for
// code that handles both STARS and ERAM controllers.
type TCP = ControlPosition

// TCW is a Terminal Controller Workstation identifier - a physical display and keyboard.
// A TCW can control zero, one, or more positions. This is STARS-specific; ERAM does
// not have the same consolidation model. For ERAM for now this is the same as the
// position the user is covering.
type TCW string

///////////////////////////////////////////////////////////////////////////
// TCPConsolidation

// TCPConsolidation tracks the consolidation state for a single TCW (terminal
// controller workstation). A TCW may have a primary position and zero or more
// secondary positions consolidated to it. TCW entries persist regardless of
// whether a human is signed in - they represent physical workstations.
// This is STARS-specific functionality.
type TCPConsolidation struct {
	PrimaryTCP    TCP
	SecondaryTCPs []SecondaryTCP
}

type ConsolidationType int

const (
	ConsolidationBasic ConsolidationType = iota
	ConsolidationFull
)

// SecondaryTCP represents a position that is consolidated to another position.
type SecondaryTCP struct {
	TCP  TCP
	Type ConsolidationType
}

// ControlsPosition returns true if this TCW controls the given position (either as
// primary or as one of its secondary positions).
func (tc *TCPConsolidation) ControlsPosition(pos ControlPosition) bool {
	return slices.Contains(tc.OwnedPositions(), pos)
}

// OwnedPositions returns all positions controlled by this TCW (primary + all secondaries).
func (tc *TCPConsolidation) OwnedPositions() []ControlPosition {
	if tc.PrimaryTCP == "" {
		return nil
	}
	return append([]ControlPosition{tc.PrimaryTCP},
		util.MapSlice(tc.SecondaryTCPs, func(sec SecondaryTCP) ControlPosition { return sec.TCP })...)
}

///////////////////////////////////////////////////////////////////////////
// ControllerConfiguration

// ControllerConfiguration defines which facility configuration to use for a scenario.
// The scenario JSON only contains config_id; all other fields are populated at runtime
// from the referenced configuration in the facility config file.
type ControllerConfiguration struct {
	// ConfigId references a configuration in stars_config.configurations in the
	// facility config file. This is the only field from JSON.
	ConfigId string `json:"config_id"`

	// DefaultConsolidation defines the consolidation tree. If set in the
	// scenario JSON, it overrides the facility config's default. If not set,
	// it is populated from the referenced configuration during post-deserialization.
	DefaultConsolidation PositionConsolidation `json:"default_consolidation,omitempty"`

	// InboundAssignments maps inbound flow names to the TCP that handles them.
	// Populated from the referenced configuration during post-deserialization.
	InboundAssignments map[string]TCP `json:"-"`

	// DepartureAssignments maps departure specifiers to the TCP that handles them.
	// Populated from the referenced configuration during post-deserialization.
	DepartureAssignments map[string]TCP `json:"-"`
}

type PositionConsolidation map[TCP][]TCP

// RootPosition returns the root TCP of the consolidation tree, or empty string if not found.
func (cc *ControllerConfiguration) RootPosition() (TCP, error) {
	return cc.DefaultConsolidation.RootPosition()
}

// GetRootPosition returns the root TCP of the consolidation tree (the one
// that has all others transitively consolidated into it).
func (pc PositionConsolidation) RootPosition() (TCP, error) {
	if len(pc) == 0 {
		return "", fmt.Errorf("no positions defined")
	}

	// Find all TCPs that appear as children
	children := make(map[TCP]bool)
	for _, childList := range pc {
		for _, child := range childList {
			children[child] = true
		}
	}

	// The root is a key that is not a child of anyone
	var roots []TCP
	for parent := range pc {
		if !children[parent] {
			roots = append(roots, parent)
		}
	}

	if len(roots) == 0 {
		return "", fmt.Errorf("no root position found (circular dependency?)")
	}
	if len(roots) > 1 {
		return "", fmt.Errorf("multiple root positions found: %v", roots)
	}
	return roots[0], nil
}

// AllPositions returns all TCPs defined in this configuration
func (cc *ControllerConfiguration) AllPositions() []TCP {
	return cc.DefaultConsolidation.AllPositions()
}

func (pc PositionConsolidation) AllPositions() []TCP {
	positions := make(map[TCP]bool)
	for parent, children := range pc {
		positions[parent] = true
		for _, child := range children {
			positions[child] = true
		}
	}
	return util.SortedMapKeys(positions)
}

// Validate checks the configuration for errors
func (cc *ControllerConfiguration) Validate(controlPositions map[TCP]*av.Controller, e *util.ErrorLogger) {
	e.Push("\"configuration\"")
	defer e.Pop()

	// Check that all positions are valid control positions
	for _, tcp := range cc.AllPositions() {
		if _, ok := controlPositions[tcp]; !ok {
			e.ErrorString("position %q not found in \"control_positions\"", tcp)
		}
	}

	// Check for exactly one root
	root, err := cc.RootPosition()
	if err != nil {
		e.Error(err)
	} else {
		// Check that root is in default_consolidation map (as a key with possibly empty children)
		if _, ok := cc.DefaultConsolidation[root]; !ok {
			e.ErrorString("root position %q must be a key in \"default_consolidation\"", root)
		}
	}

	// Check for cycles (a position can't be its own ancestor)
	// Inline the GetConsolidatedInto logic here
	getConsolidatedInto := func(tcp TCP) TCP {
		for parent, children := range cc.DefaultConsolidation {
			if slices.Contains(children, tcp) {
				return parent
			}
		}
		return ""
	}

	for tcp := range cc.DefaultConsolidation {
		visited := make(map[TCP]bool)
		current := tcp
		for current != "" {
			if visited[current] {
				e.ErrorString("cycle detected in consolidation hierarchy involving %q", tcp)
				break
			}
			visited[current] = true
			current = getConsolidatedInto(current)
		}
	}

	// Check that no position appears as a child of multiple parents
	childParent := make(map[TCP]TCP)
	for parent, children := range cc.DefaultConsolidation {
		for _, child := range children {
			if existingParent, ok := childParent[child]; ok {
				e.ErrorString("position %q appears as a child of both %q and %q in \"default_consolidation\"",
					child, existingParent, parent)
			} else {
				childParent[child] = parent
			}
		}
	}

	// Check inbound assignments refer to valid positions
	for flow, tcp := range cc.InboundAssignments {
		if !slices.Contains(cc.AllPositions(), tcp) {
			e.ErrorString("inbound_assignments: %q assigns to %q which is not in positions", flow, tcp)
		}
	}

	// Check departure assignments refer to valid positions
	for airport, tcp := range cc.DepartureAssignments {
		if !slices.Contains(cc.AllPositions(), tcp) {
			e.ErrorString("departure_assignments: %q assigns to %q which is not in positions", airport, tcp)
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Sim consolidation methods

// SignOn returns state and event subscription for a controller at the given TCW
// and consolidates the provided TCPs.
func (s *Sim) SignOn(tcw TCW, tcps []TCP) (*UserState, *EventsSubscription, error) {
	s.mu.Lock(s.lg)

	if _, ok := s.State.CurrentConsolidation[tcw]; !ok {
		s.mu.Unlock(s.lg)
		return nil, nil, av.ErrNoController
	}

	s.mu.Unlock(s.lg)

	for _, tcp := range tcps {
		if err := s.ConsolidateTCP(tcw, tcp, ConsolidationFull); err != nil {
			s.lg.Errorf("%s->%s: %v", tcp, tcw, err)
		}
	}

	return s.GetUserState(), s.Subscribe(), nil
}

// consolidationContext describes where a TCP currently lives in the consolidation structure.
type consolidationContext struct {
	isPrimary      bool // sendingTCP is a primary at some TCW
	isSecondary    bool // sendingTCP is a secondary at some TCW
	currentTCW     TCW  // which TCW owns this TCP (as primary or secondary)
	secondaryIdx   int  // index in SecondaryTCPs slice (only valid if isSecondary)
	atReceivingTCW bool // already at the target TCW
}

// classifyConsolidation determines where sendingTCP currently lives in the consolidation
// structure relative to receivingTCW.
func (s *Sim) classifyConsolidation(sendingTCP TCP, receivingTCW TCW) *consolidationContext {
	for tcw, cons := range s.State.CurrentConsolidation {
		if cons.PrimaryTCP == sendingTCP {
			return &consolidationContext{
				isPrimary:      true,
				currentTCW:     tcw,
				atReceivingTCW: (tcw == receivingTCW),
			}
		}

		for i, sec := range cons.SecondaryTCPs {
			if sec.TCP == sendingTCP {
				return &consolidationContext{
					isSecondary:    true,
					currentTCW:     tcw,
					secondaryIdx:   i,
					atReceivingTCW: (tcw == receivingTCW),
				}
			}
		}
	}

	s.lg.Infof("Attempted invalid consolidation of TCP %s to TCW %s", sendingTCP, receivingTCW)
	return nil
}

// findBasicConsolidation checks if tcp is a basic/limited consolidated secondary somewhere.
// Returns the TCW where it's consolidated and true, or empty string and false if not found.
func (s *Sim) findBasicConsolidation(tcp TCP) (TCW, bool) {
	for tcw, cons := range s.State.CurrentConsolidation {
		for _, sec := range cons.SecondaryTCPs {
			if sec.TCP == tcp && sec.Type == ConsolidationBasic {
				return tcw, true
			}
		}
	}
	return "", false
}

// ConsolidateTCP consolidates sendingTCP to the receivingTCW's keyboard. (3.11.1 and 3.11.3)
// ConsolidationFull transfers active tracks and allows moving already-consolidated positions.
// ConsolidationBasic only consolidates inactive/future and rejects already-consolidated positions.
func (s *Sim) ConsolidateTCP(receivingTCW TCW, sendingTCP TCP, consType ConsolidationType) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if _, ok := s.State.CurrentConsolidation[receivingTCW]; !ok {
		return ErrTCWNotFound
	}

	ctx := s.classifyConsolidation(sendingTCP, receivingTCW)
	if ctx == nil {
		return ErrTCWNotFound
	}

	receivingCons := s.State.CurrentConsolidation[receivingTCW]

	// Receiver must not be a basic/limited consolidated position (3.11.1, 3.11.3)
	if _, isBasic := s.findBasicConsolidation(TCP(receivingTCW)); isBasic {
		return ErrTCWIsConsolidated
	}

	if consType == ConsolidationBasic {
		// Basic consolidation requires receiver to have a primary (3.11.1)
		if receivingCons.PrimaryTCP == "" {
			return ErrTCWNotFound
		}
		// Basic consolidation cannot move already-consolidated positions (3.11.1)
		if ctx.isSecondary {
			return ErrTCPAlreadyConsolidated
		}
	} else if consType == ConsolidationFull {
		// If sending TCP is a basic-consolidated secondary, receiver must be the same TCW (3.11.3)
		if ctx.isSecondary {
			if originalTCW, isBasic := s.findBasicConsolidation(sendingTCP); isBasic && originalTCW != receivingTCW {
				return ErrTCPAlreadyConsolidated
			}
		}
	}

	var transferredTCPs []TCP // TCPs whose track ownership should transfer

	switch {
	case ctx.isSecondary && ctx.atReceivingTCW:
		// Already a secondary at this TCW - upgrade type if needed
		receivingCons.SecondaryTCPs[ctx.secondaryIdx].Type = consType
		transferredTCPs = []TCP{sendingTCP}

	case ctx.isSecondary && !ctx.atReceivingTCW:
		// Secondary at different TCW - move to receiving TCW (full consolidation only)
		oldCons := s.State.CurrentConsolidation[ctx.currentTCW]
		oldCons.SecondaryTCPs = append(oldCons.SecondaryTCPs[:ctx.secondaryIdx],
			oldCons.SecondaryTCPs[ctx.secondaryIdx+1:]...)

		if receivingCons.PrimaryTCP == "" {
			// Vacant position - sending becomes primary
			receivingCons.PrimaryTCP = sendingTCP
		} else {
			// Already has primary - sending becomes secondary
			receivingCons.SecondaryTCPs = append(receivingCons.SecondaryTCPs,
				SecondaryTCP{TCP: sendingTCP, Type: consType})
		}
		transferredTCPs = []TCP{sendingTCP}

	case ctx.isPrimary && ctx.atReceivingTCW:
		// Primary consolidating to own TCW
		transferredTCPs = []TCP{sendingTCP}

	case ctx.isPrimary && !ctx.atReceivingTCW:
		// Primary at different TCW - transfer secondaries, clear old TCW
		oldCons := s.State.CurrentConsolidation[ctx.currentTCW]
		// Collect all TCPs being transferred before modifying slices
		transferredTCPs = []TCP{sendingTCP}
		for _, sec := range oldCons.SecondaryTCPs {
			transferredTCPs = append(transferredTCPs, sec.TCP)
			receivingCons.SecondaryTCPs = append(receivingCons.SecondaryTCPs, sec)
			s.lg.Infof("transferred secondary %s from %s to %s", sec.TCP, ctx.currentTCW, receivingTCW)
		}
		oldCons.SecondaryTCPs = nil
		oldCons.PrimaryTCP = ""

		if receivingCons.PrimaryTCP == "" {
			// Vacant position - sending becomes primary
			receivingCons.PrimaryTCP = sendingTCP
			s.lg.Infof("consolidated primary %s from %s to vacant %s as primary", sendingTCP, ctx.currentTCW, receivingTCW)
		} else {
			// Already has primary - sending becomes secondary
			receivingCons.SecondaryTCPs = append(receivingCons.SecondaryTCPs,
				SecondaryTCP{TCP: sendingTCP, Type: consType})
			s.lg.Infof("consolidated primary %s from %s to %s as secondary", sendingTCP, ctx.currentTCW, receivingTCW)
		}
	}

	// For full consolidation, transfer track ownership
	if consType == ConsolidationFull && len(transferredTCPs) > 0 {
		for _, ac := range s.Aircraft {
			if ac.NASFlightPlan != nil && slices.Contains(transferredTCPs, ac.NASFlightPlan.TrackingController) {
				ac.NASFlightPlan.OwningTCW = receivingTCW
				if ac.NASFlightPlan.HandoffController != "" && s.State.TCWControlsPosition(receivingTCW, ac.NASFlightPlan.HandoffController) {
					// It's being flashed to us but was then consolidated; the handoff is now irrelevant...
					ac.NASFlightPlan.HandoffController = ""
				}
				s.lg.Infof("transferred track %s ownership to %s", ac.NASFlightPlan.ACID, receivingTCW)
			}
		}
	}

	return nil
}

// DeconsolidateTCP returns a secondary TCP to its default keyboard.
// If tcp is empty, it means the user wants to deconsolidate their own TCW's TCP
// back to themselves (e.g., user at TCW "1A" wants TCP "1A" returned to them).
// If tcp is specified, that TCP is deconsolidated back to its default TCW.
func (s *Sim) DeconsolidateTCP(tcw TCW, tcp TCP) error {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	// If no TCP specified, the user wants their own TCW's TCP back
	if tcp == "" {
		tcp = TCP(tcw)
	}

	// Search all TCWs to find where this TCP is consolidated as a secondary
	owningCons, owningTCW := func() (*TCPConsolidation, TCW) {
		for tcw, cons := range s.State.CurrentConsolidation {
			for _, sec := range cons.SecondaryTCPs {
				if sec.TCP == tcp {
					return cons, tcw
				}
			}
		}
		return nil, ""
	}()
	if owningCons == nil {
		return ErrTCPNotConsolidated
	}

	// Validate receiving TCW is vacant before making changes (3.11.4)
	destCons, ok := s.State.CurrentConsolidation[TCW(tcp)]
	if !ok {
		s.lg.Warnf("%s: no TCW for deconsolidated TCP?", tcp)
		return ErrTCWNotFound
	}
	if destCons.PrimaryTCP != "" {
		return ErrTCWNotVacant
	}

	// Remove the secondary from the owner
	owningCons.SecondaryTCPs = slices.DeleteFunc(owningCons.SecondaryTCPs, func(sec SecondaryTCP) bool {
		return sec.TCP == tcp
	})

	// Restore the TCP to its original TCW
	destCons.PrimaryTCP = tcp

	// Transfer ownership of tracks back to the original TCW
	for _, ac := range s.Aircraft {
		if ac.NASFlightPlan != nil && ac.NASFlightPlan.TrackingController == TCP(tcp) {
			ac.NASFlightPlan.OwningTCW = TCW(tcp)
			s.lg.Infof("transferred track %s ownership to %s (deconsolidation)", ac.NASFlightPlan.ACID, tcp)
		}
	}

	s.lg.Infof("deconsolidated %s from %s", tcp, owningTCW)
	return nil
}

// TCWControlsPosition returns true if the given TCW controls the specified position.
// Thread-safe wrapper around State.TCWControlsPosition.
func (s *Sim) TCWControlsPosition(tcw TCW, pos ControlPosition) bool {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.State.TCWControlsPosition(tcw, pos)
}

// SetPrivilegedTCW sets or clears privileged (instructor) status for a TCW.
// Privileged TCWs can control any aircraft regardless of which position owns it.
func (s *Sim) SetPrivilegedTCW(tcw TCW, privileged bool) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	if privileged {
		s.PrivilegedTCWs[tcw] = true
	} else {
		delete(s.PrivilegedTCWs, tcw)
	}
}

// TCWIsPrivileged returns whether the given TCW has elevated privileges.
func (s *Sim) TCWIsPrivileged(tcw TCW) bool {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.PrivilegedTCWs[tcw]
}

// GetConsolidatedPositions returns all positions controlled by a TCW
func (s *Sim) GetPositionsForTCW(tcw TCW) []ControlPosition {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return s.State.GetPositionsForTCW(tcw)
}

func (s *Sim) GetCurrentConsolidation() map[TCW]*TCPConsolidation {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	return deep.MustCopy(s.State.CurrentConsolidation)
}
