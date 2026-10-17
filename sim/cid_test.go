// sim/cid_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"strings"
	"testing"

	"github.com/mmp/vice/rand"
)

func TestCIDAllocator(t *testing.T) {
	a := NewCIDAllocator()
	r := rand.New(6502)

	var cids []string
	seen := make(map[string]bool)
	for {
		cid, err := a.Allocate(r)
		if err != nil {
			break
		}
		if seen[cid] {
			t.Errorf("%s: same CID returned multiple times", cid)
		}
		seen[cid] = true
		cids = append(cids, cid)
	}

	total := 0
	for _, s := range a.Sets {
		total += len(s.CharSets[0]) * len(s.CharSets[1]) * len(s.CharSets[2])
	}
	if len(cids) != total {
		t.Errorf("allocated %d CIDs before exhaustion, expected %d", len(cids), total)
	}

	// The 1000 all-digit CIDs come first, and each set is exhausted before the next one is used.
	set := 0
	for i, cid := range cids {
		if strings.ContainsAny(cid, "OI") {
			t.Errorf("%s: CID contains O or I", cid)
		}
		if allDigits := strings.Trim(cid, "0123456789") == ""; allDigits != (i < 1000) {
			t.Fatalf("%s: unexpected CID at allocation %d", cid, i)
		}
		for set < len(a.Sets) {
			if _, ok := a.Sets[set].index(cid); ok {
				break
			}
			set++
		}
		if set == len(a.Sets) {
			t.Fatalf("%s: CID allocated out of priority order", cid)
		}
	}

	for _, cid := range []string{"", "12", "1234", "1O2", "12I", "12a"} {
		if err := a.Release(cid); err == nil {
			t.Errorf("%q: expected error releasing invalid CID", cid)
		}
	}

	// Free a random subset, then allocate to exhaustion again: every CID returned must be one
	// that was freed.
	rand.ShuffleSlice(cids, r)
	freed := make(map[string]bool)
	for _, cid := range cids[:1000] {
		if err := a.Release(cid); err != nil {
			t.Fatalf("%s: unexpected error on Release(): %v", cid, err)
		}
		freed[cid] = true
	}
	if err := a.Release(cids[0]); err == nil {
		t.Errorf("%s: expected error releasing CID twice", cids[0])
	}

	for len(freed) > 0 {
		cid, err := a.Allocate(r)
		if err != nil {
			t.Fatalf("unable to allocate CID with %d freed: %v", len(freed), err)
		}
		if !freed[cid] {
			t.Fatalf("%s: CID returned by allocator was not freed", cid)
		}
		delete(freed, cid)
	}
	if cid, err := a.Allocate(r); err == nil {
		t.Errorf("%s: CID allocated after all were reallocated", cid)
	}
}

func TestCIDAllocatorDeterminism(t *testing.T) {
	a, b := NewCIDAllocator(), NewCIDAllocator()
	ra, rb := rand.New(1), rand.New(1)
	for range 2000 {
		ca, _ := a.Allocate(ra)
		cb, _ := b.Allocate(rb)
		if ca != cb {
			t.Fatalf("allocators with the same seed returned %s and %s", ca, cb)
		}
	}
}
