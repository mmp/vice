package eram

import (
	"time"

	"github.com/mmp/vice/math"
)

// ERAM Short-Term Conflict Alert (STCA). Every caUpdateInterval of sim time
// the pane predicts each eligible track pair's 3D trajectories
// caLookaheadSeconds into the future and alerts if the pair is ever
// simultaneously within both the lateral and vertical separation minima.
// Targets in conflict render with "flashing" (brightness-cycling) full
// datablocks.

const (
	caUpdateInterval   = 5 * time.Second // sim-time between detection passes
	caLookaheadSeconds = 240
	caSampleStep       = 5 // seconds between prediction samples

	caLateralMinimum        = 5.0   // nm
	caReducedLateralMinimum = 3.0   // nm; applies when both targets are in reduced separation airspace
	caReducedSepCeiling     = 23000 // ft; reduced separation airspace is at or below FL230
	caVerticalMinimum       = 1000  // ft
	caVerticalSlop          = 5     // ft; keeps exactly-1000-ft-separated targets from alerting on float error

	caLevelRateThreshold  = 300 // ft/min; below this the target is treated as level
	caLevelDBAltTolerance = 200 // ft; DB altitude within this of current altitude = at assigned altitude

	caDimFactor = 0.5 // datablock brightness scale during the dim phase of the flash cycle
)

// caTarget is a snapshot of one aircraft's state for conflict prediction,
// in flat NM coordinates.
type caTarget struct {
	pos   [2]float32 // NM
	vel   [2]float32 // NM/second
	alt   float32    // ft
	rate  float32    // ft/minute
	dbAlt int        // data block altitude (hard or interim), ft; 0 if unset
}

// caAltitudeEnvelope returns the [lo, hi] altitude band the target may
// occupy t seconds from now. Climbing/descending targets extrapolate at
// their current rate, leveling off at the data block altitude if they are
// moving toward it. A level target whose data block altitude differs from
// its current altitude may start toward it at any time, so it occupies the
// whole band between the two.
func caAltitudeEnvelope(tgt caTarget, t float32) (lo, hi float32) {
	db := float32(tgt.dbAlt)
	if math.Abs(tgt.rate) > caLevelRateThreshold {
		pred := tgt.alt + tgt.rate*t/60
		if tgt.dbAlt != 0 {
			if tgt.rate > 0 && db > tgt.alt {
				pred = min(pred, db)
			} else if tgt.rate < 0 && db < tgt.alt {
				pred = max(pred, db)
			}
		}
		return pred, pred
	}
	if tgt.dbAlt != 0 && math.Abs(db-tgt.alt) > caLevelDBAltTolerance {
		return min(tgt.alt, db), max(tgt.alt, db)
	}
	return tgt.alt, tgt.alt
}

// caIntervalGap returns the vertical distance between two altitude bands,
// 0 if they overlap.
func caIntervalGap(alo, ahi, blo, bhi float32) float32 {
	if ahi < blo {
		return blo - ahi
	}
	if bhi < alo {
		return alo - bhi
	}
	return 0
}

// caConflict reports whether the pair is predicted to be simultaneously
// within the lateral and vertical separation minima at any point within the
// lookahead window.
func caConflict(a, b caTarget) bool {
	// Quick outs: skip the sampling loop if the pair can't get within the
	// minima inside the window even at maximum closure.
	sep := math.Length2f(math.Sub2f(a.pos, b.pos))
	maxClosure := (math.Length2f(a.vel) + math.Length2f(b.vel)) * caLookaheadSeconds
	if sep > caLateralMinimum+maxClosure {
		return false
	}
	bandSpan := func(t caTarget) float32 {
		if math.Abs(t.rate) <= caLevelRateThreshold && t.dbAlt != 0 {
			return math.Abs(float32(t.dbAlt) - t.alt)
		}
		return 0
	}
	maxVertClosure := (math.Abs(a.rate)+math.Abs(b.rate))/60*caLookaheadSeconds +
		bandSpan(a) + bandSpan(b)
	if math.Abs(a.alt-b.alt) > caVerticalMinimum+maxVertClosure {
		return false
	}

	for t := float32(0); t <= caLookaheadSeconds; t += caSampleStep {
		pa := math.Add2f(a.pos, math.Scale2f(a.vel, t))
		pb := math.Add2f(b.pos, math.Scale2f(b.vel, t))
		alo, ahi := caAltitudeEnvelope(a, t)
		blo, bhi := caAltitudeEnvelope(b, t)

		latMin := float32(caLateralMinimum)
		if ahi <= caReducedSepCeiling && bhi <= caReducedSepCeiling {
			latMin = caReducedLateralMinimum
		}

		if math.Length2f(math.Sub2f(pa, pb)) < latMin &&
			caIntervalGap(alo, ahi, blo, bhi) < caVerticalMinimum-caVerticalSlop {
			return true
		}
	}
	return false
}
