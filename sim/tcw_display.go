// sim/tcw_display.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

// Track annotation mutations use two code paths:
//
//   - Per-field setters (SetTrackMSAW, SetTrackJRingRadius, ...) are
//     used by server-driven transitions that happen inside the sim
//     update loop (handoff accept, pointout ack, etc.) and by
//     client-side per-frame write paths (MSAW detection,
//     InQLRegion) where touching one field at a time avoids racing
//     against server writes of neighboring fields.
//
//   - SetTrackAnnotations (whole-struct write) is used by human-
//     driven client actions (acknowledge an alert, drop a J-ring,
//     slew a datablock) where the client reads the current
//     annotation, mutates the handful of fields that belong to the
//     action, and pushes the updated struct back in one round-trip.
//     Races with the per-frame paths above are avoided because the
//     per-frame paths do not overlap the fields touched by human
//     actions.
//
// New call sites should prefer the per-field setters when only one
// field changes, and SetTrackAnnotations when multiple fields
// change atomically as part of one user action.

// TCWDisplayState is the set of STARS display state that is shared
// across all relief controllers occupying a single TCW. One exists per
// active TCW on the Sim. It is not persisted to disk -- it lives for
// the sim session only.
type TCWDisplayState struct {
	// Annotations holds per-aircraft STARS track annotations keyed by
	// ADSBCallsign. The callsign is stable across associated and
	// unassociated tracks, so annotations can attach to unassociated
	// VFR tracks as well as associated IFR tracks. Entries are created
	// lazily when a controller first sets a field for a callsign, and
	// pruned when the aircraft leaves the sim.
	Annotations map[av.ADSBCallsign]TrackAnnotations

	// ScopePrefsBlob is an opaque JSON-encoded STARS Preferences
	// snapshot: the whole struct sans a handful of per-user fields
	// (character size, audio, cursor home, dwell mode, DCB position).
	// The server does not inspect the blob -- it is written by the
	// client that last mutated a scope-wide setting and echoed back
	// to all controllers at the TCW so they can apply it to their
	// local prefs. Zero-length means no client has seeded shared
	// state yet.
	ScopePrefsBlob []byte

	// ScopePrefsRev is bumped every time ScopePrefsBlob is written.
	// Clients track the last Rev they applied so they can ignore
	// echoes of their own pushes without having to byte-compare the
	// blob every frame.
	ScopePrefsRev uint64

	// RadioHoldUntil is the sim-time before which the TCW radio is busy
	// or in post-event quiet. All TransmissionManagers at this TCW pause
	// playback while SimTime < RadioHoldUntil. Source-agnostic: pilot
	// transmissions, controller PTTs, and post-event holds all write here.
	RadioHoldUntil Time

	// Monotonic revision, bumped on every mutation. Clients can send
	// last-seen rev to the server for diff detection in future plans.
	Rev uint64

	// Fused is the per-TCW fused/unfused display mode. Toggled via
	// the shared DCB from any client at the TCW; all clients at the
	// TCW observe the same value.
	Fused bool
}

// TrackAnnotations is the subset of stars.TrackState that is shared
// across all relief controllers at a TCW. Each field maps 1:1 to its
// counterpart on TrackState.
type TrackAnnotations struct {
	JRingRadius float32
	ConeLength  float32

	LeaderLineDirection     *math.CardinalOrdinalDirection
	FDAMLeaderLineDirection *math.CardinalOrdinalDirection
	UseGlobalLeaderLine     bool

	DisplayFDB bool
	DisplayPTL bool

	DisplayTPASize       *bool
	DisplayATPAMonitor   *bool
	DisplayATPAWarnAlert *bool

	DisplayRequestedAltitude *bool
	DisplayLDBBeaconCode     bool

	// Display-type overrides
	DatablockAlert            bool
	InhibitACTypeDisplay      *bool
	ForceACTypeDisplayEndTime Time

	// Handoff display state
	AcceptedHandoffSector     string
	AcceptedHandoffDisplayEnd Time
	OutboundHandoffAccepted   bool
	OutboundHandoffFlashEnd   Time
	RDIndicatorEnd            Time

	// Flash timers
	POFlashingEndTime            Time
	UNFlashingEndTime            Time
	IFFlashing                   bool
	SuspendedShowAltitudeEndTime Time
	FullLDBEndTime               Time

	// Alerts + acks
	MSAW                          bool
	MSAWStart                     Time
	InhibitMSAW                   bool
	MSAWAcknowledged              bool
	MSAWSoundEnd                  Time
	SPCAlert                      bool
	SPCAcknowledged               bool
	SPCSoundEnd                   Time
	MissingFlightPlanAcknowledged bool
	DBAcknowledged                av.Squawk

	// Pointouts
	PointOutAcknowledged bool
	ForceQL              bool
	InQLRegion           bool

	// ATPA user toggle
	InhibitDisplayInTrailDist bool

	// Misc per-ACID UX
	IsSelected                 bool
	ReleaseDeleted             bool
	Ghost                      TrackGhost
	ClearedScratchpadAlternate bool
}

// TrackGhost mirrors stars.TrackState.Ghost (PartialDatablock + GhostState).
type TrackGhost struct {
	PartialDatablock bool
	State            GhostState
}

// GetTCWDisplay returns the shared state for the given TCW or nil if
// it has not been created yet. Caller must hold s.mu.
func (s *Sim) GetTCWDisplay(tcw TCW) *TCWDisplayState {
	return s.TCWDisplay[tcw]
}

// EnsureTCWDisplay returns the existing shared state for the TCW or
// lazily creates one if none exists. Caller must hold s.mu.
func (s *Sim) EnsureTCWDisplay(tcw TCW) *TCWDisplayState {
	if s.TCWDisplay == nil {
		s.TCWDisplay = make(map[TCW]*TCWDisplayState)
	}
	if d, ok := s.TCWDisplay[tcw]; ok {
		return d
	}
	d := &TCWDisplayState{
		Annotations: make(map[av.ADSBCallsign]TrackAnnotations),
	}
	s.TCWDisplay[tcw] = d
	return d
}

// mutateTrackAnnotation acquires the sim lock, ensures a TCWDisplay +
// per-callsign entry exist, applies `f` to the entry in place, writes
// it back, and bumps Rev. All SetTrack* helpers below share this shape.
func (s *Sim) mutateTrackAnnotation(tcw TCW, callsign av.ADSBCallsign, f func(*TrackAnnotations)) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	s.mutateTrackAnnotationLocked(tcw, callsign, f)
}

// mutateTrackAnnotationLocked is the lock-free variant used by
// server-side detection paths (handoff accept, pointout ack, etc.)
// that already hold s.mu. Caller must hold s.mu.
func (s *Sim) mutateTrackAnnotationLocked(tcw TCW, callsign av.ADSBCallsign, f func(*TrackAnnotations)) {
	d := s.EnsureTCWDisplay(tcw)
	entry := d.Annotations[callsign]
	f(&entry)
	d.Annotations[callsign] = entry
	d.Rev++
}

// SetTrackAnnotations overwrites the per-callsign annotation entry
// wholesale and bumps Rev. Used by the unified client -> server RPC
// (one round-trip per logical change). Server-driven transitions
// still use the per-field mutators above; this is the client-initiated
// write path. Last-write-wins on simultaneous mutations from multiple
// clients (acceptable at human-click cadence).
func (s *Sim) SetTrackAnnotations(tcw TCW, callsign av.ADSBCallsign, annot TrackAnnotations) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	d := s.EnsureTCWDisplay(tcw)
	if d.Annotations == nil {
		d.Annotations = make(map[av.ADSBCallsign]TrackAnnotations)
	}
	d.Annotations[callsign] = annot
	d.Rev++
}

func (s *Sim) SetTrackJRingRadius(tcw TCW, callsign av.ADSBCallsign, v float32) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.JRingRadius = v })
}

func (s *Sim) SetTrackConeLength(tcw TCW, callsign av.ADSBCallsign, v float32) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.ConeLength = v })
}

func (s *Sim) SetTrackLeaderLineDirection(tcw TCW, callsign av.ADSBCallsign, v *math.CardinalOrdinalDirection) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.LeaderLineDirection = v })
}

func (s *Sim) SetTrackFDAMLeaderLineDirection(tcw TCW, callsign av.ADSBCallsign, v *math.CardinalOrdinalDirection) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.FDAMLeaderLineDirection = v })
}

func (s *Sim) SetTrackUseGlobalLeaderLine(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.UseGlobalLeaderLine = v })
}

func (s *Sim) SetTrackDisplayFDB(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DisplayFDB = v })
}

func (s *Sim) SetTrackDisplayPTL(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DisplayPTL = v })
}

func (s *Sim) SetTrackDisplayTPASize(tcw TCW, callsign av.ADSBCallsign, v *bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DisplayTPASize = v })
}

func (s *Sim) SetTrackDisplayATPAMonitor(tcw TCW, callsign av.ADSBCallsign, v *bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DisplayATPAMonitor = v })
}

func (s *Sim) SetTrackDisplayATPAWarnAlert(tcw TCW, callsign av.ADSBCallsign, v *bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DisplayATPAWarnAlert = v })
}

func (s *Sim) SetTrackDisplayRequestedAltitude(tcw TCW, callsign av.ADSBCallsign, v *bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DisplayRequestedAltitude = v })
}

func (s *Sim) SetTrackDisplayLDBBeaconCode(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DisplayLDBBeaconCode = v })
}

// --- Display-type overrides ---
func (s *Sim) SetTrackDatablockAlert(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DatablockAlert = v })
}
func (s *Sim) SetTrackInhibitACTypeDisplay(tcw TCW, callsign av.ADSBCallsign, v *bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.InhibitACTypeDisplay = v })
}
func (s *Sim) SetTrackForceACTypeDisplayEndTime(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.ForceACTypeDisplayEndTime = v })
}

// --- Handoff display state ---
func (s *Sim) SetTrackAcceptedHandoffSector(tcw TCW, callsign av.ADSBCallsign, v string) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.AcceptedHandoffSector = v })
}
func (s *Sim) SetTrackAcceptedHandoffDisplayEnd(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.AcceptedHandoffDisplayEnd = v })
}
func (s *Sim) SetTrackOutboundHandoffAccepted(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.OutboundHandoffAccepted = v })
}
func (s *Sim) SetTrackOutboundHandoffFlashEnd(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.OutboundHandoffFlashEnd = v })
}
func (s *Sim) SetTrackRDIndicatorEnd(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.RDIndicatorEnd = v })
}

// --- Flash timers ---
func (s *Sim) SetTrackPOFlashingEndTime(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.POFlashingEndTime = v })
}
func (s *Sim) SetTrackUNFlashingEndTime(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.UNFlashingEndTime = v })
}
func (s *Sim) SetTrackIFFlashing(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.IFFlashing = v })
}
func (s *Sim) SetTrackSuspendedShowAltitudeEndTime(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.SuspendedShowAltitudeEndTime = v })
}
func (s *Sim) SetTrackFullLDBEndTime(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.FullLDBEndTime = v })
}

// --- Alerts + acks ---
func (s *Sim) SetTrackMSAW(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.MSAW = v })
}
func (s *Sim) SetTrackMSAWStart(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.MSAWStart = v })
}
func (s *Sim) SetTrackInhibitMSAW(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.InhibitMSAW = v })
}
func (s *Sim) SetTrackMSAWAcknowledged(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.MSAWAcknowledged = v })
}
func (s *Sim) SetTrackMSAWSoundEnd(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.MSAWSoundEnd = v })
}
func (s *Sim) SetTrackSPCAlert(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.SPCAlert = v })
}
func (s *Sim) SetTrackSPCAcknowledged(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.SPCAcknowledged = v })
}
func (s *Sim) SetTrackSPCSoundEnd(tcw TCW, callsign av.ADSBCallsign, v Time) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.SPCSoundEnd = v })
}
func (s *Sim) SetTrackMissingFlightPlanAcknowledged(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.MissingFlightPlanAcknowledged = v })
}
func (s *Sim) SetTrackDBAcknowledged(tcw TCW, callsign av.ADSBCallsign, v av.Squawk) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.DBAcknowledged = v })
}

// --- Pointouts ---
func (s *Sim) SetTrackPointOutAcknowledged(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.PointOutAcknowledged = v })
}
func (s *Sim) SetTrackForceQL(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.ForceQL = v })
}
func (s *Sim) SetTrackInQLRegion(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.InQLRegion = v })
}

// --- ATPA user toggle ---
func (s *Sim) SetTrackInhibitDisplayInTrailDist(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.InhibitDisplayInTrailDist = v })
}

// --- Misc ---
func (s *Sim) SetTrackIsSelected(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.IsSelected = v })
}
func (s *Sim) SetTrackReleaseDeleted(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.ReleaseDeleted = v })
}
func (s *Sim) SetTrackGhost(tcw TCW, callsign av.ADSBCallsign, pdb bool, state GhostState) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) {
		a.Ghost = TrackGhost{PartialDatablock: pdb, State: state}
	})
}
func (s *Sim) SetTrackClearedScratchpadAlternate(tcw TCW, callsign av.ADSBCallsign, v bool) {
	s.mutateTrackAnnotation(tcw, callsign, func(a *TrackAnnotations) { a.ClearedScratchpadAlternate = v })
}

// SetScopePrefsBlob replaces the TCW-wide scope prefs blob with the
// caller's payload and bumps both ScopePrefsRev and Rev. The server
// does not interpret the bytes -- it just fans them out to everyone
// polling the TCW's state.
func (s *Sim) SetScopePrefsBlob(tcw TCW, blob []byte) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	d := s.EnsureTCWDisplay(tcw)
	d.ScopePrefsBlob = blob
	d.ScopePrefsRev++
	d.Rev++
}

// SetFused flips the TCW-wide Fused flag and bumps Rev.
func (s *Sim) SetFused(tcw TCW, v bool) {
	s.mu.Lock(s.lg)
	defer s.mu.Unlock(s.lg)

	d := s.EnsureTCWDisplay(tcw)
	d.Fused = v
	d.Rev++
}

// pruneTCWDisplayAnnotations removes per-callsign annotation entries
// whose ADSBCallsign is no longer present in s.Aircraft. Called from
// the tick loop. Caller must hold s.mu.
func (s *Sim) pruneTCWDisplayAnnotations() {
	if len(s.TCWDisplay) == 0 {
		return
	}
	for _, d := range s.TCWDisplay {
		for callsign := range d.Annotations {
			if _, ok := s.Aircraft[callsign]; !ok {
				delete(d.Annotations, callsign)
				d.Rev++
			}
		}
	}
}
