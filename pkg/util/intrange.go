// pkg/util/intrange.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"errors"
	"math/bits"

	"github.com/mmp/vice/pkg/rand"
)

type IntRangeSet struct {
	First, Last   int
	AvailableBits []uint64
}

var (
	ErrIntRangeSetEmpty            = errors.New("set is empty")
	ErrIntRangeSetOutOfRange       = errors.New("value out of range")
	ErrIntRangeReturnedValueInSet  = errors.New("value returned is already in the set")
	ErrIntRangeSetValueUnavailable = errors.New("value not currently present in the set")
)

// [first,last]
func MakeIntRangeSet(first, last int) *IntRangeSet {
	nints := last - first + 1
	nalloc := (nints + 63) / 64

	s := &IntRangeSet{
		First:         first,
		Last:          last,
		AvailableBits: make([]uint64, nalloc),
	}

	// Set all available bits until the last element of s.AvailableBits
	for i := range len(s.AvailableBits) - 1 {
		s.AvailableBits[i] = ^uint64(0)
	}

	// Just set the low bits for the partial use of the last entry
	slop := 64*nalloc - nints
	s.AvailableBits[nalloc-1] = ^uint64(0) >> slop

	return s
}

func (s *IntRangeSet) GetRandom(r *rand.Rand) (int, error) {
	start := r.Intn(len(s.AvailableBits)) // random starting point in p.AssignedBits
	rot := r.Intn(64)                     // random rotation to randomize search start within each uint64

	for i := range len(s.AvailableBits) {
		// Start the search at start, then wrap around.
		idx := (start + i) % len(s.AvailableBits)

		if s.AvailableBits[idx] == 0 {
			// All are assigned in this chunk of 64.
			continue
		}

		// Randomly rotate the bits so that when we start searching for a
		// set bit starting from the low bit, we effectively randomize
		// which bit index we're starting from.
		available := bits.RotateLeft64(s.AvailableBits[idx], rot)

		// Find the last set bit and then map that back to a bit index in
		// the unrotated bits.
		bit := (bits.TrailingZeros64(available) + 64 - rot) % 64

		// Record that we've taken it
		s.AvailableBits[idx] &= ^(1 << bit)

		return s.First + 64*idx + bit, nil
	}

	return 0, ErrIntRangeSetEmpty
}

func (s *IntRangeSet) indices(v int) (int, int, error) {
	if v < s.First || v > s.Last {
		return 0, 0, ErrIntRangeSetOutOfRange
	}
	offset := int(v - s.First)
	return offset / 64, offset % 64, nil
}

func (s *IntRangeSet) IsAvailable(v int) bool {
	if idx, bit, err := s.indices(v); err == nil {
		return s.AvailableBits[idx]&(1<<bit) != 0
	}
	return false
}

func (s *IntRangeSet) Return(v int) error {
	if s.IsAvailable(v) {
		return ErrIntRangeReturnedValueInSet
	} else if idx, bit, err := s.indices(v); err != nil {
		return err
	} else {
		// Set the bit
		s.AvailableBits[idx] |= 1 << bit
		return nil
	}
}

func (s *IntRangeSet) Take(v int) error {
	if idx, bit, err := s.indices(v); err != nil {
		return err
	} else if !s.IsAvailable(v) {
		return ErrIntRangeSetValueUnavailable
	} else {
		// Clear the bit
		s.AvailableBits[idx] &= ^(1 << bit)
		return nil
	}
}

func (s *IntRangeSet) Count() int {
	// Count the number of set bits in the range
	n := 0
	for _, b := range s.AvailableBits {
		n += bits.OnesCount64(b)
	}
	return n
}

func (s *IntRangeSet) Clone() *IntRangeSet {
	clone := &IntRangeSet{
		First:         s.First,
		Last:          s.Last,
		AvailableBits: make([]uint64, len(s.AvailableBits)),
	}
	copy(clone.AvailableBits, s.AvailableBits)

	return clone
}

func (s *IntRangeSet) InRange(v int) bool {
	return v >= s.First && v <= s.Last
}
