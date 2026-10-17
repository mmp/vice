// sim/cid.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"errors"
	"math/bits"
	"slices"

	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

// CIDs are sequences of three alphanumeric characters. The letters "O" and "I" are not used, to
// prevent confusion with the numbers 0 and 1. CIDs are unique per aircraft across the NAS and are
// issued from a series of prioritized sets. Currently they are only unique within a single
// session, so we expect that the first set will be the only one drawn from in practice. It may be
// worth relaxing the priority a bit to better simulate allocation across the entire NAS.

// cidSet records which CIDs are free in one candidate set. The set is specified by three character
// sets that give the valid characters for each position of the CID.
type cidSet struct {
	// Free holds one bit per CID, set when the CID is available. CIDs are ordered by the indices
	// of their characters in CharSets, with the last character varying fastest.
	Free     []uint64
	CharSets [3][]byte
}

func makeCIDSet(c0, c1, c2 []byte) *cidSet {
	n := len(c0) * len(c1) * len(c2)
	free := make([]uint64, (n+63)/64)
	for i := range n {
		free[i/64] |= 1 << (i % 64)
	}

	return &cidSet{
		Free:     free,
		CharSets: [3][]byte{c0, c1, c2},
	}
}

// allocate claims a CID chosen uniformly at random from the set's free CIDs. It returns false if
// none are free.
func (s *cidSet) allocate(r *rand.Rand) (string, bool) {
	n := util.ReduceSlice(s.Free, 0, func(f uint64, n int) int {
		return n + bits.OnesCount64(f)
	})
	if n == 0 {
		return "", false
	}

	k := r.Intn(n)
	for i, w := range s.Free {
		if c := bits.OnesCount64(w); k >= c {
			k -= c
			continue
		}
		for b := range 64 {
			if w&(1<<b) == 0 {
				continue
			}
			if k == 0 {
				s.Free[i] &^= 1 << b
				return s.cid(64*i + b), true
			}
			k--
		}
	}
	return "", false // NOTREACHED
}

// cid returns the CID at index i of the set.
func (s *cidSet) cid(i int) string {
	n1, n2 := len(s.CharSets[1]), len(s.CharSets[2])
	return string([]byte{s.CharSets[0][i/(n1*n2)], s.CharSets[1][(i/n2)%n1], s.CharSets[2][i%n2]})
}

// index returns the index of the given CID in the set and whether the set includes it.
func (s *cidSet) index(cid string) (int, bool) {
	if len(cid) != 3 {
		return 0, false
	}
	i0 := slices.Index(s.CharSets[0], cid[0])
	i1 := slices.Index(s.CharSets[1], cid[1])
	i2 := slices.Index(s.CharSets[2], cid[2])
	if i0 == -1 || i1 == -1 || i2 == -1 {
		return 0, false
	}
	return (i0*len(s.CharSets[1])+i1)*len(s.CharSets[2]) + i2, true
}

// release frees the CID at index i of the set.
func (s *cidSet) release(i int) error {
	w, b := i/64, i%64
	if s.Free[w]&(1<<b) != 0 {
		return errors.New("CID is not allocated")
	}
	s.Free[w] |= 1 << b
	return nil
}

// CIDAllocator manages allocation of unique three-character CIDs.
type CIDAllocator struct {
	Sets []*cidSet
}

// NewCIDAllocator returns a CIDAllocator with all CIDs free.
func NewCIDAllocator() *CIDAllocator {
	preferredAlphas := []byte{'P', 'K', 'N', 'Y', 'T', 'V', 'F', 'R', 'C', 'D', 'E', 'W', 'A'}
	nonPreferredAlphas := []byte{'M', 'X', 'L', 'J', 'U', 'B', 'G', 'Q', 'S', 'H', 'Z'}
	digits := []byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9'}

	return &CIDAllocator{
		Sets: []*cidSet{
			makeCIDSet(digits, digits, digits),
			makeCIDSet(digits, digits, preferredAlphas),
			makeCIDSet(digits, preferredAlphas, preferredAlphas),
			makeCIDSet(digits, preferredAlphas, digits),
			makeCIDSet(digits, digits, nonPreferredAlphas),
			makeCIDSet(digits, nonPreferredAlphas, nonPreferredAlphas),
			makeCIDSet(digits, nonPreferredAlphas, preferredAlphas),
			makeCIDSet(digits, preferredAlphas, nonPreferredAlphas),
			makeCIDSet(digits, nonPreferredAlphas, digits),
		},
	}
}

// Allocate returns a CID chosen at random from the first set that has one available.
func (c *CIDAllocator) Allocate(r *rand.Rand) (string, error) {
	for _, s := range c.Sets {
		if cid, ok := s.allocate(r); ok {
			return cid, nil
		}
	}
	return "", errors.New("no free CIDs")
}

// Release frees a CID so it can be reused.
func (c *CIDAllocator) Release(cid string) error {
	for _, s := range c.Sets {
		if i, ok := s.index(cid); ok {
			return s.release(i)
		}
	}
	return errors.New("invalid CID")
}
