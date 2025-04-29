// pkg/util/intrange_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"errors"
	"testing"

	"github.com/mmp/vice/pkg/rand"
)

func TestMakeIntRangeSet(t *testing.T) {
	s := MakeIntRangeSet(10, 74) // 65 values → just fits into one uint64 + 1
	if got := s.Count(); got != 65 {
		t.Errorf("expected 65 available, got %d", got)
	}
	for v := 10; v <= 74; v++ {
		if !s.IsAvailable(v) {
			t.Errorf("value %d should be initially available", v)
		}
	}
}

func TestTakeAndReturn(t *testing.T) {
	s := MakeIntRangeSet(100, 110)
	err := s.Take(105)
	if err != nil {
		t.Fatalf("unexpected error taking 105: %v", err)
	}
	if s.IsAvailable(105) {
		t.Errorf("value 105 should not be available after Take")
	}
	err = s.Return(105)
	if err != nil {
		t.Fatalf("unexpected error returning 105: %v", err)
	}
	if !s.IsAvailable(105) {
		t.Errorf("value 105 should be available after Return")
	}
}

func TestTakeUnavailable(t *testing.T) {
	s := MakeIntRangeSet(0, 5)
	_ = s.Take(2)
	err := s.Take(2)
	if !errors.Is(err, ErrIntRangeSetValueUnavailable) {
		t.Errorf("expected ErrIntRangeSetValueUnavailable, got %v", err)
	}
}

func TestReturnAlreadyAvailable(t *testing.T) {
	s := MakeIntRangeSet(0, 5)
	err := s.Return(3)
	if !errors.Is(err, ErrIntRangeReturnedValueInSet) {
		t.Errorf("expected ErrIntRangeReturnedValueInSet, got %v", err)
	}
}

func TestOutOfRange(t *testing.T) {
	s := MakeIntRangeSet(10, 20)

	if s.IsAvailable(9) || s.IsAvailable(21) {
		t.Errorf("values out of range should return false on IsAvailable")
	}

	err := s.Take(21)
	if !errors.Is(err, ErrIntRangeSetOutOfRange) {
		t.Errorf("expected out-of-range error for Take, got %v", err)
	}

	err = s.Return(9)
	if !errors.Is(err, ErrIntRangeSetOutOfRange) {
		t.Errorf("expected out-of-range error for Return, got %v", err)
	}
}

func TestGetRandom(t *testing.T) {
	s := MakeIntRangeSet(1000, 1020)

	seen := map[int]bool{}
	r := rand.Make()
	for i := 0; i < 21; i++ {
		v, err := s.GetRandom(r)
		if err != nil {
			t.Fatalf("unexpected error from GetRandom: %v", err)
		}
		if seen[v] {
			t.Errorf("duplicate value returned from GetRandom: %d", v)
		}
		seen[v] = true
	}

	// Now it should be empty
	_, err := s.GetRandom(r)
	if !errors.Is(err, ErrIntRangeSetEmpty) {
		t.Errorf("expected ErrIntRangeSetEmpty after consuming all values, got %v", err)
	}
}

func TestCount(t *testing.T) {
	s := MakeIntRangeSet(0, 63)
	if s.Count() != 64 {
		t.Errorf("expected 64 available, got %d", s.Count())
	}
	_ = s.Take(10)
	_ = s.Take(20)
	if s.Count() != 62 {
		t.Errorf("expected 62 available after 2 takes, got %d", s.Count())
	}
	_ = s.Return(10)
	if s.Count() != 63 {
		t.Errorf("expected 63 available after return, got %d", s.Count())
	}
}
