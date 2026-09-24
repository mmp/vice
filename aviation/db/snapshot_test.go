// aviation/db/snapshot_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package db

import (
	"bytes"
	"maps"
	"testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
	InitDB()

	hash := DB.Hash()
	if again := DB.Hash(); again != hash {
		t.Fatalf("hash changed between calls: %s, then %s", hash, again)
	}

	var buf bytes.Buffer
	if err := DB.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	d, err := Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}

	// Every exported field goes into the hash, so a match shows the decoded
	// database holds the same data.
	if got := d.Hash(); got != hash {
		t.Errorf("decoded database hash %s, want %s", got, hash)
	}
	if !maps.Equal(d.faaToICAO, DB.faaToICAO) {
		t.Errorf("decoded FAA index has %d entries, want %d", len(d.faaToICAO), len(DB.faaToICAO))
	}
}
