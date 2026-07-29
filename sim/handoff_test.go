// sim/handoff_filter_test.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only
package sim

import (
	"io"
	"log/slog"
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/util"
)

// bigCircle returns an AirspaceVolume that contains the test aircraft position
// (~5nm north of the origin) at all reasonable altitudes.
func bigCircle() av.AirspaceVolume {
	return av.AirspaceVolume{
		Type:    av.AirspaceVolumeCircle,
		Floor:   0,
		Ceiling: 100000,
		Center:  math.Point2LL{0, 0},
		Radius:  50,
	}
}

// makeHandoffRow returns r with the shared "owning_tcp" qualifier set.
func makeHandoffRow(owningTCPs string, r HandoffFilterRegion) HandoffFilterRegion {
	r.OwningTCPString = owningTCPs
	return r
}
func TestHandoffFilterValidate(t *testing.T) {
	positions := map[TCP]*av.Controller{
		"1A":  {},
		"1B":  {},
		"EXT": {FacilityIdentifier: "N"},
	}
	valid := func(r HandoffFilterRegion) bool {
		e := &util.ErrorLogger{}
		r.ValidateTCPs(positions, e)
		return !e.HaveErrors()
	}
	// Valid: initiate to a local TCP.
	if !valid(makeHandoffRow("1A", HandoffFilterRegion{HandoffAction: "I", HORcvr: "1B"})) {
		t.Error("valid I row rejected")
	}
	// Valid: transfer.
	if !valid(HandoffFilterRegion{HandoffAction: "T", HORcvr: "1B"}) {
		t.Error("valid T row rejected")
	}
	// Valid: disable, no receiver.
	if !valid(HandoffFilterRegion{HandoffAction: "D"}) {
		t.Error("valid D row rejected")
	}
	// Valid: slave with owning TCP.
	if !valid(makeHandoffRow("1A", HandoffFilterRegion{HandoffAction: "I", HORcvr: "1B", SlaveTCPs: true})) {
		t.Error("valid slave row rejected")
	}
	// The owning TCP list is taken over from the shared qualifiers.
	r0 := makeHandoffRow("1A,1B", HandoffFilterRegion{HandoffAction: "D"})
	e0 := &util.ErrorLogger{}
	r0.ValidateTCPs(positions, e0)
	if len(r0.OwnerTCPs) != 2 || len(r0.FilterQualifiers.OwningTCPs) != 0 {
		t.Errorf("owning_tcp: OwnerTCPs = %v, FilterQualifiers.OwningTCPs = %v",
			r0.OwnerTCPs, r0.FilterQualifiers.OwningTCPs)
	}
	// Invalid cases.
	if valid(HandoffFilterRegion{HandoffAction: "A"}) {
		t.Error(`handoff_action "A" should be rejected (not yet supported)`)
	}
	if valid(HandoffFilterRegion{HandoffAction: "X"}) {
		t.Error(`handoff_action "X" should be rejected`)
	}
	if valid(HandoffFilterRegion{HandoffAction: "I"}) {
		t.Error(`"I" with no ho_receiver should be rejected`)
	}
	if valid(HandoffFilterRegion{HandoffAction: "D", HORcvr: "1B"}) {
		t.Error(`"D" with a ho_receiver should be rejected`)
	}
	if valid(HandoffFilterRegion{HandoffAction: "I", HORcvr: "EXT"}) {
		t.Error(`ho_receiver must be a local position`)
	}
	if valid(HandoffFilterRegion{HandoffAction: "I", HORcvr: "1B", SlaveTCPs: true}) {
		t.Error(`slave without owning_tcp should be rejected`)
	}
	if valid(HandoffFilterRegion{HandoffAction: "D", TerminalSector: "A"}) {
		t.Error(`terminal_sector without config_plan should be rejected`)
	}
	if valid(HandoffFilterRegion{HandoffAction: "D", ConfigPlan: "CP1", TerminalSector: "T"}) {
		t.Error(`terminal_sector "T" should be rejected`)
	}
	// Default action is "I".
	r := HandoffFilterRegion{HORcvr: "1B"}
	e := &util.ErrorLogger{}
	r.ValidateTCPs(positions, e)
	if r.HandoffAction != "I" {
		t.Errorf("default handoff_action = %q, want I", r.HandoffAction)
	}
}
func TestHandoffFilterQualifies(t *testing.T) {
	r := HandoffFilterRegion{
		AirspaceVolume: bigCircle(),
		ConfigPlan:     "CP2",
	}
	r.FlightType = "ARRIVAL"
	ac := MakeTestAircraft("TST100", "22L")
	fp := &NASFlightPlan{ACID: "TST100", TypeOfFlight: av.FlightTypeArrival}
	// Matches when config plan matches and flight type matches.
	if !r.qualifies(ac.Position(), int(ac.Altitude()), fp, "B738", "jet", "CP2", nil) {
		t.Error("should qualify: config plan + flight type match")
	}
	// Wrong active config plan -> no match.
	if r.qualifies(ac.Position(), int(ac.Altitude()), fp, "B738", "jet", "CP1", nil) {
		t.Error("should not qualify: wrong active config plan")
	}
	// Wrong flight type -> no match.
	dep := &NASFlightPlan{ACID: "TST100", TypeOfFlight: av.FlightTypeDeparture}
	if r.qualifies(ac.Position(), int(ac.Altitude()), dep, "B738", "jet", "CP2", nil) {
		t.Error("should not qualify: departure vs arrival filter")
	}
	// A/C type class gate.
	r2 := HandoffFilterRegion{AirspaceVolume: bigCircle(), ACTypeClass: "PROP"}
	if r2.qualifies(ac.Position(), int(ac.Altitude()), fp, "B738", "jet", "", nil) {
		t.Error("jet should not qualify a PROP-class row")
	}
	if !r2.qualifies(ac.Position(), int(ac.Altitude()), fp, "C172", "prop", "", nil) {
		t.Error("prop should qualify a PROP-class row")
	}
}
func newHandoffTestSim() *Sim {
	lg := &log.Logger{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := NewTestSim(lg)
	// IsLocalController requires a Controllers entry with no FacilityIdentifier.
	s.State.Controllers = map[ControlPosition]*av.Controller{
		"125.0": {},
		"1B":    {},
		"N56":   {FacilityIdentifier: "N", Position: "56"},
	}
	s.Handoffs = make(map[ACID]Handoff)
	return s
}

// newHandoffTestTrack returns an associated aircraft owned by 125.0 at the
// TEST TCW, squawking a discrete code so that it is AHOP-eligible.
func newHandoffTestTrack(s *Sim, acid ACID) (*Aircraft, *NASFlightPlan) {
	ac := MakeTestAircraft(av.ADSBCallsign(acid), "22L")
	ac.Squawk = 0o1234
	fp := &NASFlightPlan{ACID: acid, TypeOfFlight: av.FlightTypeArrival,
		TrackingController: "125.0", OwningTCW: "TEST"}
	ac.AssociateFlightPlan(fp)
	s.Aircraft[ac.ADSBCallsign] = ac
	return ac, fp
}
func TestHandoffFilterOwnerMatch(t *testing.T) {
	s := newHandoffTestSim()
	// Consolidate 1B under the TEST TCW (whose primary is 125.0).
	s.State.CurrentConsolidation["TEST"].SecondaryTCPs = []SecondaryTCP{{TCP: "1B"}}
	fp := &NASFlightPlan{TrackingController: "125.0", OwningTCW: "TEST"}
	// Blank owning TCP -> always matches.
	if !s.handoffFilterOwnerMatch(&HandoffFilterRegion{}, fp) {
		t.Error("blank owning_tcp should match")
	}
	// Exact owner match.
	if !s.handoffFilterOwnerMatch(&HandoffFilterRegion{OwnerTCPs: []ControlPosition{"9Z", "125.0"}}, fp) {
		t.Error("exact owning_tcp should match")
	}
	// Different TCP, no slave -> no match.
	if s.handoffFilterOwnerMatch(&HandoffFilterRegion{OwnerTCPs: []ControlPosition{"1B"}}, fp) {
		t.Error("consolidated TCP without slave flag should not match")
	}
	// Different TCP consolidated to owner, with slave -> match.
	if !s.handoffFilterOwnerMatch(&HandoffFilterRegion{OwnerTCPs: []ControlPosition{"1B"}, SlaveTCPs: true}, fp) {
		t.Error("consolidated TCP with slave flag should match")
	}
	// TCP not consolidated to owner, with slave -> no match.
	if s.handoffFilterOwnerMatch(&HandoffFilterRegion{OwnerTCPs: []ControlPosition{"9Z"}, SlaveTCPs: true}, fp) {
		t.Error("unrelated TCP should not match even with slave flag")
	}
}
func TestHandoffFilterActions(t *testing.T) {
	s := newHandoffTestSim()
	// D disables auto-handoff (delta) and locks it against controller input.
	fp := &NASFlightPlan{TrackingController: "125.0", OwningTCW: "TEST"}
	s.applyHandoffFilterAction(&HandoffFilterRegion{HandoffAction: "D"}, fp)
	if !fp.AutoHandoffInhibited || !fp.AutoHandoffInhibitLocked {
		t.Error("D action should set AutoHandoffInhibited and AutoHandoffInhibitLocked")
	}
	// I on an inhibited track is suppressed.
	s.applyHandoffFilterAction(&HandoffFilterRegion{HandoffAction: "I", HORcvr: "1B"}, fp)
	if fp.HandoffController != "" {
		t.Error("I action should be suppressed for an inhibited track")
	}
	// I on an eligible track initiates the handoff and records it as automatic.
	fp2 := &NASFlightPlan{ACID: "TST200", TrackingController: "125.0", OwningTCW: "TEST"}
	s.applyHandoffFilterAction(&HandoffFilterRegion{HandoffAction: "I", HORcvr: "1B"}, fp2)
	if fp2.HandoffController != "1B" {
		t.Errorf("I action: HandoffController = %q, want 1B", fp2.HandoffController)
	}
	if !fp2.HandoffWasAutomatic {
		t.Error("I action should record the handoff as automatic")
	}
	// A row whose receiver already owns the track does nothing.
	fp3 := &NASFlightPlan{ACID: "TST210", TrackingController: "1B", OwningTCW: "1B"}
	s.applyHandoffFilterAction(&HandoffFilterRegion{HandoffAction: "I", HORcvr: "1B"}, fp3)
	if fp3.HandoffController != "" {
		t.Error("I action should not hand a track off to its current owner")
	}
}
func TestHandoffFilterFirstMatchWins(t *testing.T) {
	s := newHandoffTestSim()
	ac, fp := newHandoffTestTrack(s, "TST300")
	// Two overlapping rows; the first should win.
	r1 := HandoffFilterRegion{AirspaceVolume: bigCircle(), HandoffAction: "I", HORcvr: "1B"}
	r1.Id = "R1"
	r2 := HandoffFilterRegion{AirspaceVolume: bigCircle(), HandoffAction: "D"}
	r2.Id = "R2"
	s.State.FacilityAdaptation.Filters.Handoff = HandoffFilterRegions{r1, r2}
	s.processHandoffFilterRegions(ac)
	if fp.HandoffController != "1B" {
		t.Errorf("first-match row (I) should have fired: HandoffController = %q", fp.HandoffController)
	}
	if fp.AutoHandoffInhibited {
		t.Error("second row (D) should not fire when the first row matched")
	}
	// Re-processing while still inside must not re-fire (entry transition only).
	fp.HandoffController = ""
	s.processHandoffFilterRegions(ac)
	if fp.HandoffController != "" {
		t.Error("action should not re-fire while the track stays inside the volume")
	}
}

// Tracks STARS never auto-hands off: ones already in handoff status, ones
// squawking a non-discrete code, suspended ones, and ones another facility
// owns (5.1.7, p. 5-15).
func TestHandoffFilterIneligibleTracks(t *testing.T) {
	r := HandoffFilterRegion{AirspaceVolume: bigCircle(), HandoffAction: "I", HORcvr: "1B"}
	r.Id = "R1"

	for _, tc := range []struct {
		name  string
		setup func(ac *Aircraft, fp *NASFlightPlan)
	}{
		{"in-handoff", func(ac *Aircraft, fp *NASFlightPlan) { fp.HandoffController = "1B" }},
		{"non-discrete-code", func(ac *Aircraft, fp *NASFlightPlan) { ac.Squawk = 0o1200 }},
		{"suspended", func(ac *Aircraft, fp *NASFlightPlan) { fp.Suspended = true }},
		{"external-owner", func(ac *Aircraft, fp *NASFlightPlan) { fp.TrackingController = "N56" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHandoffTestSim()
			s.State.FacilityAdaptation.Filters.Handoff = HandoffFilterRegions{r}
			ac, fp := newHandoffTestTrack(s, ACID(tc.name))
			tc.setup(ac, fp)
			want := fp.HandoffController
			s.processHandoffFilterRegions(ac)
			if fp.HandoffController != want {
				t.Errorf("HandoffController = %q, want %q", fp.HandoffController, want)
			}
		})
	}
}

// Recalling an automatic handoff makes the track ineligible for further
// auto-handoff (5.1.17), even after the auto-accept timer has dropped the
// Sim.Handoffs entry; recalling a manual handoff does not.
func TestAutoHandoffRecall(t *testing.T) {
	s := newHandoffTestSim()
	ac, fp := newHandoffTestTrack(s, "TST350")
	r := HandoffFilterRegion{AirspaceVolume: bigCircle(), HandoffAction: "I", HORcvr: "1B"}
	r.Id = "R1"
	s.State.FacilityAdaptation.Filters.Handoff = HandoffFilterRegions{r}

	s.processHandoffFilterRegions(ac)
	if fp.HandoffController != "1B" {
		t.Fatalf("HandoffController = %q, want 1B", fp.HandoffController)
	}
	// The handoff sits unaccepted past the auto-accept time, dropping the entry.
	delete(s.Handoffs, fp.ACID)
	if err := s.CancelHandoff("TEST", fp.ACID); err != nil {
		t.Fatalf("CancelHandoff: %v", err)
	}
	if fp.HandoffController != "" {
		t.Errorf("recall should clear HandoffController: %q", fp.HandoffController)
	}
	if !fp.AutoHandoffInhibited {
		t.Error("recalling an automatic handoff should inhibit further auto-handoff")
	}
	if fp.HandoffWasAutomatic {
		t.Error("recall should clear HandoffWasAutomatic")
	}

	// A manual handoff's recall leaves the track eligible.
	fp.AutoHandoffInhibited = false
	fp.HandoffController = "1B"
	if err := s.CancelHandoff("TEST", fp.ACID); err != nil {
		t.Fatalf("CancelHandoff: %v", err)
	}
	if fp.AutoHandoffInhibited {
		t.Error("recalling a manual handoff should not inhibit auto-handoff")
	}
}

func TestAutoHandoffConfiguration(t *testing.T) {
	s := newHandoffTestSim()
	ac, fp := newHandoffTestTrack(s, "TST400")
	r := HandoffFilterRegion{AirspaceVolume: bigCircle(), HandoffAction: "I", HORcvr: "1B"}
	r.Id = "R1"
	s.State.FacilityAdaptation.Filters.Handoff = HandoffFilterRegions{r}

	// Inhibit intrafacility AHOP for the entering TCP: nothing is handed off.
	if _, err := s.ConfigureAutoHandoff("TEST", AutoHandoffTCPIntrafacility, false); err != nil {
		t.Fatalf("ConfigureAutoHandoff: %v", err)
	}
	s.processHandoffFilterRegions(ac)
	if fp.HandoffController != "" {
		t.Errorf("inhibited TCP should not auto-hand off: %q", fp.HandoffController)
	}

	// Inhibiting only interfacility AHOP doesn't stop it.
	if _, err := s.ConfigureAutoHandoff("TEST", AutoHandoffTCPIntrafacility, true); err != nil {
		t.Fatalf("ConfigureAutoHandoff: %v", err)
	}
	if _, err := s.ConfigureAutoHandoff("TEST", AutoHandoffTCPInterfacility, false); err != nil {
		t.Fatalf("ConfigureAutoHandoff: %v", err)
	}
	s.processHandoffFilterRegions(ac)
	if fp.HandoffController != "1B" {
		t.Errorf("HandoffController = %q, want 1B", fp.HandoffController)
	}

	// Enabling both categories drops the TCP from the map entirely.
	if _, err := s.ConfigureAutoHandoff("TEST", AutoHandoffTCPBoth, true); err != nil {
		t.Fatalf("ConfigureAutoHandoff: %v", err)
	}
	if len(s.State.AutoHandoffTCPInhibits) != 0 {
		t.Errorf("re-enabling should clear the TCP's entry: %v", s.State.AutoHandoffTCPInhibits)
	}

	// The site-wide inhibit reports NO CHANGE if it's already set and blocks
	// the per-TCP command.
	if _, err := s.ConfigureAutoHandoff("TEST", AutoHandoffSite, false); err != nil {
		t.Fatalf("ConfigureAutoHandoff: %v", err)
	}
	if msg, _ := s.ConfigureAutoHandoff("TEST", AutoHandoffSite, false); msg != "NO CHANGE" {
		t.Errorf("repeated site inhibit: msg = %q, want NO CHANGE", msg)
	}
	if _, err := s.ConfigureAutoHandoff("TEST", AutoHandoffTCPBoth, true); err != ErrIllegalFunction {
		t.Errorf("per-TCP command with site AHOP off: err = %v, want %v", err, ErrIllegalFunction)
	}
}

func TestAutoHandoffToggleChecks(t *testing.T) {
	s := newHandoffTestSim()
	ac, fp := newHandoffTestTrack(s, "TST500")

	if err := s.checkAutoHandoffToggle(fp, ac); err != nil {
		t.Errorf("eligible track: %v", err)
	}
	// A track disabled by a "D" filter row can't be re-enabled by a controller.
	fp.AutoHandoffInhibitLocked = true
	if err := s.checkAutoHandoffToggle(fp, ac); err != ErrIllegalFunction {
		t.Errorf("filter-disabled track: err = %v, want %v", err, ErrIllegalFunction)
	}
	fp.AutoHandoffInhibitLocked = false
	// Nor is the command valid for a track in handoff status.
	fp.HandoffController = "1B"
	if err := s.checkAutoHandoffToggle(fp, ac); err != ErrTrackIsBeingHandedOff {
		t.Errorf("track in handoff: err = %v, want %v", err, ErrTrackIsBeingHandedOff)
	}
	fp.HandoffController = ""
	// Nor when AHOP is inhibited site-wide.
	if _, err := s.ConfigureAutoHandoff("TEST", AutoHandoffSite, false); err != nil {
		t.Fatalf("ConfigureAutoHandoff: %v", err)
	}
	if err := s.checkAutoHandoffToggle(fp, ac); err != ErrIllegalFunction {
		t.Errorf("site AHOP off: err = %v, want %v", err, ErrIllegalFunction)
	}
}
