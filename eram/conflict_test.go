package eram

import (
	"testing"
)

func TestCAAltitudeEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tgt    caTarget
		t      float32
		lo, hi float32
	}{
		// Climbing toward the data block altitude: extrapolate then cap.
		{"climb below cap", caTarget{alt: 30000, rate: 2000, dbAlt: 34000}, 60, 32000, 32000},
		{"climb capped", caTarget{alt: 30000, rate: 2000, dbAlt: 34000}, 180, 34000, 34000},
		// Descending toward the data block altitude: cap at level-off.
		{"descent capped", caTarget{alt: 37000, rate: -1500, dbAlt: 35000}, 240, 35000, 35000},
		// Descending away from a DB altitude that is above: no cap.
		{"descent away from db", caTarget{alt: 30000, rate: -1000, dbAlt: 34000}, 240, 26000, 26000},
		// Climbing with no DB altitude: pure extrapolation.
		{"climb no db", caTarget{alt: 30000, rate: 1000, dbAlt: 0}, 240, 34000, 34000},
		// Level at the DB altitude: point envelope.
		{"level at db", caTarget{alt: 34000, rate: 0, dbAlt: 34000}, 120, 34000, 34000},
		// Level with DB altitude below: occupies the whole band at all t.
		{"level band below", caTarget{alt: 34000, rate: 0, dbAlt: 30000}, 0, 30000, 34000},
		{"level band below later", caTarget{alt: 34000, rate: 0, dbAlt: 30000}, 240, 30000, 34000},
		// Level with DB altitude above: band up to the DB altitude.
		{"level band above", caTarget{alt: 30000, rate: 0, dbAlt: 33000}, 60, 30000, 33000},
		// Level with no DB altitude: point envelope.
		{"level no db", caTarget{alt: 31000, rate: 0, dbAlt: 0}, 240, 31000, 31000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi := caAltitudeEnvelope(tc.tgt, tc.t)
			if lo != tc.lo || hi != tc.hi {
				t.Errorf("got [%v, %v], want [%v, %v]", lo, hi, tc.lo, tc.hi)
			}
		})
	}
}

func TestCAIntervalGap(t *testing.T) {
	if g := caIntervalGap(30000, 34000, 33000, 33000); g != 0 {
		t.Errorf("overlapping bands: got gap %v, want 0", g)
	}
	if g := caIntervalGap(30000, 32000, 33000, 33000); g != 1000 {
		t.Errorf("bands 1000 apart: got gap %v, want 1000", g)
	}
	if g := caIntervalGap(35000, 35000, 33000, 34000); g != 1000 {
		t.Errorf("reversed order bands: got gap %v, want 1000", g)
	}
}

func TestCAConflictHeadOn(t *testing.T) {
	// 360 kt = 0.1 NM/s. Head-on, co-altitude.
	a := caTarget{pos: [2]float32{0, 0}, vel: [2]float32{0.1, 0}, alt: 35000, rate: 0, dbAlt: 35000}
	b := caTarget{pos: [2]float32{50, 0}, vel: [2]float32{-0.1, 0}, alt: 35000, rate: 0, dbAlt: 35000}
	if !caConflict(a, b) {
		t.Error("head-on pair closing to <5nm within 4 min: want conflict")
	}
	// Too far apart: closest approach at t=240 is 55-48=7 nm.
	b.pos = [2]float32{55, 0}
	if caConflict(a, b) {
		t.Error("pair that stays >5nm apart for the full window: want no conflict")
	}
}

func TestCAConflictDataBlockAltitudeCap(t *testing.T) {
	// Spec example: A at FL370 descending with DB alt FL350; B level FL330
	// directly ahead. A levels at FL350 -> 2000 ft gap -> no alert.
	a := caTarget{pos: [2]float32{0, 0}, vel: [2]float32{0.1, 0}, alt: 37000, rate: -1500, dbAlt: 35000}
	b := caTarget{pos: [2]float32{1, 0}, vel: [2]float32{0.1, 0}, alt: 33000, rate: 0, dbAlt: 33000}
	if caConflict(a, b) {
		t.Error("descent capped at FL350 vs level FL330: want no conflict")
	}
	// No DB altitude: A descends through B's altitude -> alert.
	a.dbAlt = 0
	if !caConflict(a, b) {
		t.Error("uncapped descent through FL330: want conflict")
	}
	// DB altitude exactly 1000 ft above B: still separated -> no alert.
	a.dbAlt = 34000
	if caConflict(a, b) {
		t.Error("descent capped exactly 1000 ft above: want no conflict")
	}
}

func TestCAConflictLevelBand(t *testing.T) {
	// Spec example: A level FL350 with a lower DB altitude, B level FL340.
	// A may start down at any time -> alert.
	a := caTarget{pos: [2]float32{0, 0}, vel: [2]float32{0.1, 0}, alt: 35000, rate: 0, dbAlt: 33000}
	b := caTarget{pos: [2]float32{1, 0}, vel: [2]float32{0.1, 0}, alt: 34000, rate: 0, dbAlt: 34000}
	if !caConflict(a, b) {
		t.Error("level FL350 with DB alt below vs level FL340: want conflict")
	}
	// DB altitude matches current altitude on both -> no alert.
	a.dbAlt = 35000
	if caConflict(a, b) {
		t.Error("both level at their DB altitudes, 1000 ft apart: want no conflict")
	}
}

func TestCAConflictReducedSeparation(t *testing.T) {
	// Parallel tracks 4 nm apart, constant separation.
	mk := func(alt float32, y float32) caTarget {
		return caTarget{pos: [2]float32{0, y}, vel: [2]float32{0.1, 0}, alt: alt, rate: 0, dbAlt: int(alt)}
	}
	// At/below FL230 the minimum is 3 nm -> 4 nm apart is fine.
	if caConflict(mk(20000, 0), mk(20000, 4)) {
		t.Error("4nm lateral at FL200 (3nm minimum applies): want no conflict")
	}
	// Above FL230 the minimum is 5 nm -> 4 nm apart alerts.
	if !caConflict(mk(24000, 0), mk(24000, 4)) {
		t.Error("4nm lateral at FL240 (5nm minimum applies): want conflict")
	}
	// Within 3 nm at/below FL230 alerts.
	if !caConflict(mk(20000, 0), mk(20000, 2.5)) {
		t.Error("2.5nm lateral at FL200: want conflict")
	}
}
