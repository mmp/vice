// sim/time.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"time"

	"github.com/mmp/vice/nav"
	"github.com/vmihailenco/msgpack/v5"
)

// Time represents simulation time in the sim package — the virtual clock
// that advances according to the sim rate, pauses, etc. It is a distinct
// type from time.Time so that sim-time and wall-clock time cannot be
// accidentally mixed.
type Time struct {
	t time.Time
}

// NewSimTime wraps a time.Time as sim.Time. Use at the boundary where real
// time enters the simulation (e.g., from config start time).
func NewSimTime(t time.Time) Time {
	return Time{t: t}
}

// Time converts back to time.Time. Use when crossing out of the sim
// domain (e.g., weather lookups that need real timestamps).
func (s Time) Time() time.Time {
	return s.t
}

// NavTime converts to nav.Time for calls into the nav package.
func (s Time) NavTime() nav.Time {
	return nav.NewTime(s.t)
}

func (s Time) Add(d time.Duration) Time {
	return Time{t: s.t.Add(d)}
}

func (s Time) Sub(o Time) time.Duration {
	return s.t.Sub(o.t)
}

func (s Time) Before(o Time) bool {
	return s.t.Before(o.t)
}

func (s Time) After(o Time) bool {
	return s.t.After(o.t)
}

func (s Time) Equal(o Time) bool {
	return s.t.Equal(o.t)
}

func (s Time) Compare(o Time) int {
	return s.t.Compare(o.t)
}

func (s Time) IsZero() bool {
	return s.t.IsZero()
}

func (s Time) UnixMilli() int64 {
	return s.t.UnixMilli()
}

func (s Time) String() string {
	return s.t.String()
}

func (s Time) UTC() Time {
	return Time{t: s.t.UTC()}
}

func (s Time) Format(layout string) string {
	return s.t.Format(layout)
}

func (s Time) Hour() int {
	return s.t.Hour()
}

func (s Time) Minute() int {
	return s.t.Minute()
}

func (s Time) Second() int {
	return s.t.Second()
}

func (s Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.t)
}

func (s *Time) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &s.t)
}

func (s Time) MarshalMsgpack() ([]byte, error) {
	return msgpack.Marshal(s.t)
}

func (s *Time) UnmarshalMsgpack(b []byte) error {
	return msgpack.Unmarshal(b, &s.t)
}
