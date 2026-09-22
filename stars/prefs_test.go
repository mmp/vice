// stars/prefs_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stars

import (
	"testing"

	av "github.com/mmp/vice/aviation"
)

func TestMakeCRDARunwayPairState(t *testing.T) {
	pairs := []CRDAPair{
		{Airport: "JFK", CRDAPair: av.CRDAPair{SourceRegion: "31L", GhostRegion: "31R"}},
		{Airport: "JFK", CRDAPair: av.CRDAPair{SourceRegion: "4L", GhostRegion: "4R"}},
	}

	state := makeCRDARunwayPairState(pairs)
	if len(state) != len(pairs) {
		t.Fatalf("got %d runway pair states, expected %d", len(state), len(pairs))
	}

	for i, pair := range pairs {
		s := state[i]
		if !s.SourceState.Enabled {
			t.Errorf("pair %d: SourceState.Enabled should default to true", i)
		}
		if s.GhostState.Enabled {
			t.Errorf("pair %d: GhostState.Enabled should default to false", i)
		}
		if s.SourceState.Airport != pair.Airport || s.GhostState.Airport != pair.Airport {
			t.Errorf("pair %d: airport not carried over from CRDAPair", i)
		}
		if s.SourceState.Region != pair.SourceRegion {
			t.Errorf("pair %d: got source region %q, expected %q", i, s.SourceState.Region, pair.SourceRegion)
		}
		if s.GhostState.Region != pair.GhostRegion {
			t.Errorf("pair %d: got ghost region %q, expected %q", i, s.GhostState.Region, pair.GhostRegion)
		}
	}
}
