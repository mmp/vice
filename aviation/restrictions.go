// aviation/restrictions.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mmp/vice/math"
)

///////////////////////////////////////////////////////////////////////////
// HILPT

type PTType int

const (
	PTUndefined = iota
	PTRacetrack
	PTStandard45
)

func (pt PTType) String() string {
	return []string{"undefined", "racetrack", "standard 45"}[pt]
}

type ProcedureTurn struct {
	Type         PTType
	RightTurns   bool
	ExitAltitude int     `json:",omitempty"`
	MinuteLimit  float32 `json:",omitempty"`
	NmLimit      float32 `json:",omitempty"`
	Entry180NoPT bool    `json:",omitempty"`
}

// LegLimit returns the extent of the procedure turn's outbound legs as a
// distance or a time, whichever the turn gives. Without one, the legs of
// an ILS, localizer, or VOR approach's turn are a minute long and an RNAV
// approach's are 4nm; both are zero for other approach types.
func (pt *ProcedureTurn) LegLimit(appr ApproachType) (nm, minutes float32) {
	switch {
	case pt.NmLimit > 0:
		return pt.NmLimit, 0
	case pt.MinuteLimit > 0:
		return 0, pt.MinuteLimit
	}
	switch appr {
	case ILSApproach, LocalizerApproach, VORApproach:
		return 0, 1
	case RNAVApproach:
		return 4, 0
	default:
		return 0, 0
	}
}

///////////////////////////////////////////////////////////////////////////
// NavigationRestriction

// NavigationRestriction is the shared base for altitude and speed restrictions.
// Range[0] is the floor, Range[1] is the ceiling.
// 0 means "no floor" (at or below); the type-specific max constant
// means "no ceiling" (at or above).
// Invariant: Range[0] <= Range[1].
type NavigationRestriction struct {
	Range [2]float32
}

// Target clamps val into the restriction's range.
func (r NavigationRestriction) Target(val float32) float32 {
	return math.Clamp(val, r.Range[0], r.Range[1])
}

// Satisfied returns true if val complies with the restriction.
func (r NavigationRestriction) Satisfied(val float32) bool {
	return val >= r.Range[0] && val <= r.Range[1]
}

// ExactValue returns the value if this is an "at" restriction (lo == hi),
// or false if it's a range.
func (r NavigationRestriction) ExactValue() (float32, bool) {
	if r.Range[0] == r.Range[1] {
		return r.Range[0], true
	}
	return 0, false
}

// ClampRange limits a range to satisfy the restriction;
// the returned Boolean indicates whether the ranges overlapped.
func (r NavigationRestriction) ClampRange(rng [2]float32) (c [2]float32, ok bool) {
	ok = rng[0] <= r.Range[1] && rng[1] >= r.Range[0]
	c[0] = math.Clamp(rng[0], r.Range[0], r.Range[1])
	c[1] = math.Clamp(rng[1], r.Range[0], r.Range[1])
	return
}

// encoded returns the restriction in compact text form, using maxVal as
// the sentinel for "no ceiling".
func (r NavigationRestriction) encoded(maxVal float32) string {
	if r.Range[0] != 0 {
		if r.Range[0] == r.Range[1] {
			return fmt.Sprintf("%.0f", r.Range[0])
		} else if r.Range[1] != maxVal {
			return fmt.Sprintf("%.0f-%.0f", r.Range[0], r.Range[1])
		} else {
			return fmt.Sprintf("%.0f+", r.Range[0])
		}
	} else if r.Range[1] != 0 && r.Range[1] != maxVal {
		return fmt.Sprintf("%.0f-", r.Range[1])
	} else {
		return ""
	}
}

///////////////////////////////////////////////////////////////////////////
// AltitudeRestriction

// MaxAltitude is used as the upper bound for "at or
// above" restrictions.  This lets the invariant Range[0] <= Range[1]
// always hold, eliminating sentinel checks in clamping/bounding code.
const MaxAltitude float32 = 100000

type AltitudeRestriction struct {
	NavigationRestriction
}

func MakeAtAltitudeRestriction(alt float32) AltitudeRestriction {
	return AltitudeRestriction{NavigationRestriction{Range: [2]float32{alt, alt}}}
}

func MakeAtOrAboveAltitudeRestriction(alt float32) AltitudeRestriction {
	return AltitudeRestriction{NavigationRestriction{Range: [2]float32{alt, MaxAltitude}}}
}

func MakeAtOrBelowAltitudeRestriction(alt float32) AltitudeRestriction {
	return AltitudeRestriction{NavigationRestriction{Range: [2]float32{0, alt}}}
}

func MakeRangeAltitudeRestriction(low, high float32) AltitudeRestriction {
	return AltitudeRestriction{NavigationRestriction{Range: [2]float32{low, high}}}
}

func (a *AltitudeRestriction) UnmarshalJSON(b []byte) error {
	// For backwards compatibility with saved scenarios, we allow
	// unmarshaling from the single-valued altitude restrictions we had
	// before.
	if alt, err := strconv.Atoi(string(b)); err == nil {
		a.Range = [2]float32{float32(alt), float32(alt)}
		return nil
	} else {
		// Otherwise declare a temporary variable with matching structure
		// but a different type to avoid an infinite loop when
		// json.Unmarshal is called.
		ar := struct{ Range [2]float32 }{}
		if err := json.Unmarshal(b, &ar); err == nil {
			a.Range = ar.Range
			// Migrate old serialized "at or above" {alt, 0} to {alt, MaxAlt}.
			if a.Range[0] != 0 && a.Range[1] == 0 {
				a.Range[1] = MaxAltitude
			}
			return nil
		} else {
			return err
		}
	}
}

// TargetAltitude clamps alt into the restriction's range.
func (a AltitudeRestriction) TargetAltitude(alt float32) float32 {
	return a.Target(alt)
}

// Encoded returns the restriction in the encoded form used in scenario
// configuration files, e.g. "5000+" for "at or above 5000".
func (a AltitudeRestriction) Encoded() string {
	return a.encoded(MaxAltitude)
}

///////////////////////////////////////////////////////////////////////////
// SpeedRestriction

// MaxRestrictionSpeed is the sentinel value used as the upper bound for
// "at or above" speed restrictions. It is not a real airspeed limit;
// compare against aircraft performance for actual maxima.
const MaxRestrictionSpeed float32 = 1000

type SpeedRestriction struct {
	NavigationRestriction
	IsMach bool
}

func MakeAtSpeedRestriction(speed float32) SpeedRestriction {
	return SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: [2]float32{speed, speed}}}
}

func MakeMachRestriction(mach float32) SpeedRestriction {
	return SpeedRestriction{
		NavigationRestriction: NavigationRestriction{Range: [2]float32{mach, mach}},
		IsMach:                true,
	}
}

func MakeAtOrAboveSpeedRestriction(speed float32) SpeedRestriction {
	return SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: [2]float32{speed, MaxRestrictionSpeed}}}
}

func MakeAtOrBelowSpeedRestriction(speed float32) SpeedRestriction {
	return SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: [2]float32{0, speed}}}
}

func MakeRangeSpeedRestriction(low, high float32) SpeedRestriction {
	return SpeedRestriction{NavigationRestriction: NavigationRestriction{Range: [2]float32{low, high}}}
}

func (s *SpeedRestriction) UnmarshalJSON(b []byte) error {
	// null: leave as zero value (no restriction).
	if string(b) == "null" {
		return nil
	}
	// Plain number: treat as "at" restriction (backwards compat with existing JSON).
	if spd, err := strconv.Atoi(string(b)); err == nil {
		s.Range = [2]float32{float32(spd), float32(spd)}
		return nil
	}
	// String form: "250-", "210+", "180-210", "210".
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		sr, err := ParseSpeedRestriction(str)
		if err != nil {
			return err
		}
		*s = *sr
		return nil
	}
	// Struct form: {"Range": [lo, hi], "IsMach": bool}.
	ar := struct {
		Range  [2]float32
		IsMach bool
	}{}
	if err := json.Unmarshal(b, &ar); err == nil {
		s.Range = ar.Range
		s.IsMach = ar.IsMach
		if s.IsMach && s.Range[0] != s.Range[1] {
			return fmt.Errorf("mach restriction must be exact")
		}
		return nil
	} else {
		return err
	}
}

// Encoded returns the restriction in compact text form, e.g. "210+", "250-", "210".
func (s SpeedRestriction) Encoded() string {
	if s.IsMach {
		if s.Range[0] != s.Range[1] {
			return ""
		}
		return fmt.Sprintf("M%02d", int(s.Range[0]*100+.5))
	}
	return s.encoded(MaxRestrictionSpeed)
}

// CheckJSON implements util.JSONChecker so the JSON type checker accepts
// plain numbers and strings in addition to the struct form.
func (s SpeedRestriction) CheckJSON(json any) bool {
	switch json.(type) {
	case float64, string, map[string]any, nil:
		return true
	}
	return false
}

// IsZero returns true if the speed restriction is unset.
func (s SpeedRestriction) IsZero() bool {
	return s.Range[0] == 0 && s.Range[1] == 0
}

// ParseSpeedRestriction parses a speed restriction from compact text form:
// "210", "210+", "210-", "180-210".
func ParseSpeedRestriction(s string) (*SpeedRestriction, error) {
	if s == "" {
		return nil, fmt.Errorf("empty speed restriction")
	}
	if strings.HasPrefix(s, "M") || strings.HasPrefix(s, "m") {
		machStr := s[1:]
		machStr = strings.TrimSuffix(machStr, "+")
		machStr = strings.TrimSuffix(machStr, "-")
		mach, err := strconv.Atoi(machStr)
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing speed restriction: %v", s, err)
		}
		sr := MakeMachRestriction(float32(mach) / 100)
		return &sr, nil
	}

	if low, high, ok := strings.Cut(s, "-"); ok {
		// Either a range or at-or-below
		min, err := strconv.Atoi(low)
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing speed restriction: %v", s, err)
		}
		if high != "" {
			max, err := strconv.Atoi(high)
			if err != nil {
				return nil, fmt.Errorf("%s: error parsing speed restriction: %v", s, err)
			}
			sr := MakeRangeSpeedRestriction(float32(min), float32(max))
			return &sr, nil

		} else {
			sr := MakeAtOrBelowSpeedRestriction(float32(min))
			return &sr, nil
		}
	} else {
		// Single speed or at-or-above
		low, aoa := strings.CutSuffix(s, "+")
		min, err := strconv.Atoi(low)
		if err != nil {
			return nil, fmt.Errorf("%s: error parsing speed restriction: %v", s, err)
		}
		if aoa {
			sr := MakeAtOrAboveSpeedRestriction(float32(min))
			return &sr, nil
		} else {
			sr := MakeAtSpeedRestriction(float32(min))
			return &sr, nil
		}
	}
}

// ParseDistanceDirection parses strings like "5W" or "10NE" into a distance
// in miles and a cardinal/ordinal direction.
func ParseDistanceDirection(s string) (int, math.CardinalOrdinalDirection, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i == len(s) {
		return 0, 0, fmt.Errorf("invalid distance/direction")
	}
	dist, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, 0, err
	}
	dir, err := math.ParseCardinalOrdinalDirection(s[i:])
	if err != nil {
		return 0, 0, err
	}
	return dist, dir, nil
}

// ParseSyntheticCrossingFix parses a synthetic cross-distance waypoint name
// like "_DETGY/5W" and returns the named fix, distance, and direction.
func ParseSyntheticCrossingFix(fix string) (string, int, math.CardinalOrdinalDirection, bool) {
	if !strings.HasPrefix(fix, "_") {
		return "", 0, 0, false
	}
	parts := strings.Split(fix[1:], "/")
	if len(parts) < 2 || parts[0] == "" {
		return "", 0, 0, false
	}
	dist, dir, err := ParseDistanceDirection(parts[1])
	if err != nil {
		return "", 0, 0, false
	}
	return parts[0], dist, dir, true
}

// ParseSyntheticDMEFix parses a synthetic cross-DME waypoint name like
// "_04L_10DME" and returns the runway and distance (in nm) from the threshold.
func ParseSyntheticDMEFix(fix string) (string, int, bool) {
	if !strings.HasPrefix(fix, "_") || !strings.HasSuffix(fix, "DME") {
		return "", 0, false
	}
	inner := strings.TrimSuffix(fix[1:], "DME")
	i := strings.LastIndex(inner, "_")
	if i <= 0 || i == len(inner)-1 {
		return "", 0, false
	}
	dist, err := strconv.Atoi(inner[i+1:])
	if err != nil {
		return "", 0, false
	}
	return inner[:i], dist, true
}
