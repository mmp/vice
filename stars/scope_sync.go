// stars/scope_sync.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/mmp/vice/client"
	"github.com/mmp/vice/panes"
)

// Scope-prefs sync routes every scope-wide STARS preference
// (brightness, PTL, range/pan/range-ring, weather levels, leader
// lines, SSA filters, list positions, video map selections, etc.)
// through a single opaque JSON blob shipped with SimStateUpdate.
// Per-user settings -- character size, audio, dwell mode, cursor
// home, DCB visibility -- are excluded and remain local.
//
// Sync is automatic: two controllers at the same TCW are modeled as
// sharing one physical radar scope, so whenever a TCWDisplay snapshot
// is present on the client, the reconciliation loop runs.
//
// The loop runs once per STARSPane Draw:
//   - First tick with the shared blob already seeded: adopt it.
//   - First tick with shared blob empty: push local prefs (first-in
//     seeds the shared state).
//   - Subsequent ticks: if server's Rev advanced, apply the shared
//     blob; else push if local prefs diverged.

// scopeSyncActive reports whether this client participates in the
// TCW's shared scope-prefs sync. Two controllers at the same TCW are
// modeled as sharing one physical radar scope, so sync is always on
// whenever a TCWDisplay snapshot is present on the client.
func scopeSyncActive(c *client.ControlClient) bool {
	return c != nil && c.State.TCWDisplay != nil
}

// scopeLocalOnly snapshots the STARS preference fields that must
// survive any incoming scope-sync apply. These are per-user
// settings the user explicitly wants to keep (character size) or
// that simply don't make sense to share (audio, cursor home, dwell
// mode, DCB position/visibility, per-user restriction-area
// overrides).
type scopeLocalOnly struct {
	charSize                struct{ DCB, Datablocks, Lists, Tools, PositionSymbols int }
	audioVolume             int
	audioEffectEnabled      []bool
	dwellMode               DwellMode
	autoCursorHome          bool
	cursorHome              [2]float32
	displayDCB              bool
	dcbPosition             int
	restrictionAreaSettings map[int]*RestrictionAreaSettings
}

func captureScopeLocalOnly(p *Preferences) scopeLocalOnly {
	return scopeLocalOnly{
		charSize:                p.CharSize,
		audioVolume:             p.AudioVolume,
		audioEffectEnabled:      append([]bool(nil), p.AudioEffectEnabled...),
		dwellMode:               p.DwellMode,
		autoCursorHome:          p.AutoCursorHome,
		cursorHome:              p.CursorHome,
		displayDCB:              p.DisplayDCB,
		dcbPosition:             p.DCBPosition,
		restrictionAreaSettings: p.RestrictionAreaSettings,
	}
}

func restoreScopeLocalOnly(p *Preferences, s scopeLocalOnly) {
	p.CharSize = s.charSize
	p.AudioVolume = s.audioVolume
	p.AudioEffectEnabled = s.audioEffectEnabled
	p.DwellMode = s.dwellMode
	p.AutoCursorHome = s.autoCursorHome
	p.CursorHome = s.cursorHome
	p.DisplayDCB = s.displayDCB
	p.DCBPosition = s.dcbPosition
	p.RestrictionAreaSettings = s.restrictionAreaSettings
}

// encodeScopePrefs JSON-encodes a Preferences snapshot suitable for
// shared-state transport. Local-only fields are zeroed so they don't
// clobber the receiver's values on apply.
func encodeScopePrefs(p *Preferences) ([]byte, error) {
	snap := *p
	// Zero local-only fields on the copy so they round-trip as
	// empty; restoreScopeLocalOnly on apply then reinstates the
	// receiver's own values.
	snap.CharSize = struct{ DCB, Datablocks, Lists, Tools, PositionSymbols int }{}
	snap.AudioVolume = 0
	snap.AudioEffectEnabled = nil
	snap.DwellMode = DwellMode(0)
	snap.AutoCursorHome = false
	snap.CursorHome = [2]float32{}
	snap.DisplayDCB = false
	snap.DCBPosition = 0
	snap.RestrictionAreaSettings = nil
	return json.Marshal(&snap)
}

// applyScopePrefsBlob decodes blob and copies the result over the
// receiver's Preferences, then restores the local-only fields so
// character size / audio / cursor / etc. remain per-user.
func applyScopePrefsBlob(p *Preferences, blob []byte) error {
	local := captureScopeLocalOnly(p)

	var incoming Preferences
	if err := json.Unmarshal(blob, &incoming); err != nil {
		return err
	}
	*p = incoming

	restoreScopeLocalOnly(p, local)
	return nil
}

// syncScopePrefs is called once per STARSPane.Draw. It reconciles
// the local prefs with the TCW's shared blob: adopt shared state
// when we fall behind, push local state when we diverge. Does
// nothing when sync is not active.
func (sp *STARSPane) syncScopePrefs(ctx *panes.Context) {
	c := ctx.Client
	if !scopeSyncActive(c) {
		sp.scopePrefsBaseline = nil
		sp.scopePrefsBaselineRev = 0
		return
	}
	d := c.State.TCWDisplay
	ps := sp.currentPrefs()

	// First tick after sync enabled on this client.
	if sp.scopePrefsBaseline == nil {
		if d.ScopePrefsRev > 0 && len(d.ScopePrefsBlob) > 0 {
			// Shared already seeded by someone else -- adopt it.
			if err := applyScopePrefsBlob(ps, d.ScopePrefsBlob); err != nil {
				return
			}
			sp.scopePrefsBaseline = append([]byte(nil), d.ScopePrefsBlob...)
			sp.scopePrefsBaselineRev = d.ScopePrefsRev
			return
		}
		// Shared empty -- first client in seeds the shared blob with
		// its own local prefs.
		blob, err := encodeScopePrefs(ps)
		if err != nil {
			return
		}
		c.SetScopePrefs(blob, func(err error) { sp.displayError(err, ctx, "") })
		sp.scopePrefsBaseline = blob
		// Rev will catch up on the next poll when we see the
		// server's echo; leave it at 0 so the next tick treats the
		// server echo as "remote advanced" and does not re-push.
		return
	}

	// If the server's Rev advanced past ours, pull the shared blob
	// into local prefs. This path handles echoes of our own pushes
	// too -- applying our own blob back onto ourselves is a no-op.
	if d.ScopePrefsRev > sp.scopePrefsBaselineRev && len(d.ScopePrefsBlob) > 0 {
		if err := applyScopePrefsBlob(ps, d.ScopePrefsBlob); err != nil {
			return
		}
		sp.scopePrefsBaseline = append([]byte(nil), d.ScopePrefsBlob...)
		sp.scopePrefsBaselineRev = d.ScopePrefsRev
		return
	}

	// Local-only path: detect divergence vs the last known shared
	// blob and push if different. Rate-limited so a user actively
	// dragging pan/range/etc. at frame rate doesn't flood the server.
	if time.Since(sp.lastScopePrefsPush) < scopePrefsPushInterval {
		return
	}
	blob, err := encodeScopePrefs(ps)
	if err != nil {
		return
	}
	if !bytes.Equal(blob, sp.scopePrefsBaseline) {
		c.SetScopePrefs(blob, func(err error) { sp.displayError(err, ctx, "") })
		sp.scopePrefsBaseline = blob
		sp.lastScopePrefsPush = time.Now()
	}
}

// scopePrefsPushInterval caps how often a client pushes its local
// scope prefs to the server. The observer's state-update poll is
// capped at ~100ms, so pushing faster than that is wasted server
// and network work with no visible improvement.
const scopePrefsPushInterval = 100 * time.Millisecond
