// nav/turncompare_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// A/B comparison sweeps between the simulation-based and analytic turn
// predicates, plus a conformance test that pins the turnPath model to the
// tick-by-tick flight model. The sweeps fly each scenario twice, once with
// each predicate deciding, with the probes recording what the other one
// would have done, and then compare trigger timing and the quality of the
// flown ground track.

package nav

import (
	"fmt"
	"testing"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
)

///////////////////////////////////////////////////////////////////////////
// turnPath conformance

// TestTurnPathMatchesFlightModel integrates turns with the actual per-tick
// flight model and checks that turnPath predicts the same ground track, so
// that any future change to the turn dynamics in TargetHeading that is not
// mirrored in predictTurnPath breaks a test rather than the predicates.
func TestTurnPathMatchesFlightModel(t *testing.T) {
	for _, ac := range []struct {
		typ      string
		ias, alt float32
	}{
		{"C172", 110, 3000},
		{"A320", 250, 10000},
		{"A320", 280, 24000},
		{"B744", 290, 35000},
	} {
		for _, turnDeg := range []float32{15, 45, -45, 90, -150} {
			for _, wind := range []struct{ dir, kts float32 }{
				{0, 0},
				{110, 30}, // crosswind for the initial northerly heading
				{200, 50},
			} {
				name := fmt.Sprintf("%s/%.0fkt/turn%.0f/wind%.0f@%.0f", ac.typ, ac.ias, turnDeg, wind.kts, wind.dir)
				t.Run(name, func(t *testing.T) {
					f := NewArrivalFlight(t, ArrivalConfig{
						Waypoints:        "SAJUL", // far away; irrelevant while on a heading
						DepartureAirport: "KMCO",
						ArrivalAirport:   "KJFK",
						AircraftType:     ac.typ,
						InitialAltitude:  ac.alt,
						InitialSpeed:     ac.ias,
						AssignedAltitude: ac.alt,
						InitialHeading:   20,
					})
					if wind.kts > 0 {
						f.SetWind(wind.dir, wind.kts)
					}
					f.AssignSpeed(ac.ias)

					// Let the speed settle wings-level on the heading: the
					// model assumes constant TAS, and some configurations
					// (e.g. an IAS above the mach limit at altitude) take a
					// while to reach a speed the aircraft can hold.
					stable := 0
					for prev := float32(0); stable < 5 && f.tick < 400; {
						f.tickOnce()
						if math.Abs(f.nav.FlightState.IAS-prev) < 0.01 {
							stable++
						} else {
							stable = 0
						}
						prev = f.nav.FlightState.IAS
					}
					if stable < 5 {
						t.Fatalf("IAS never stabilized; still %.1f", f.nav.FlightState.IAS)
					}

					hdg := math.NormalizeHeading(f.nav.FlightState.Heading + math.MagneticHeading(turnDeg))
					turn := av.TurnDirection(av.TurnClosest)
					if turnDeg >= 150 {
						turn = av.TurnRight
					} else if turnDeg <= -150 {
						turn = av.TurnLeft
					}

					wxs := f.weather(f.nav.FlightState.Altitude)
					tp := f.nav.predictTurnPath(hdg, turn, wxs)

					// Start the turn the same way the predicates model it:
					// effective immediately, no pilot delay.
					f.nav.Heading = NavHeading{Assigned: &hdg, Turn: &turn}
					f.nav.DeferredNavHeading = nil

					nmPerLong := f.nav.FlightState.NmPerLongitude
					var maxErr float32
					for tick := 1; tick <= int(tp.dur)+10; tick++ {
						f.tickOnce()
						pReal := math.LL2NM(f.nav.FlightState.Position, nmPerLong)
						pModel := tp.position(min(float32(tick), tp.dur))
						if tick > int(tp.dur) {
							// Past rollout the model continues straight on
							// the target heading; the aircraft does too.
							pModel = tp.position(float32(tick))
						}
						maxErr = max(maxErr, math.Distance2f(pReal, pModel))
					}

					if maxErr > 0.1 {
						t.Errorf("turnPath diverges from flight model: max error %.3fnm (dur %.0fs omega %.2f radius %.2f)",
							maxErr, tp.dur, tp.omega, tp.radius)
					}

					if hd := math.HeadingDifference(f.nav.FlightState.Heading, hdg); hd > 1 {
						t.Errorf("aircraft heading %.1f not settled on target %.1f", f.nav.FlightState.Heading, hdg)
					}
				})
			}
		}
	}
}

// tickOnce advances the flight one simulation second outside of Run().
func (f *FlightTest) tickOnce() UpdateResult {
	wxs := f.weather(f.nav.FlightState.Altitude)
	result := f.nav.UpdateWithWeather(f.callsign, wxs, nil, &f.fp, f.simTime, nil)
	f.simTime = f.simTime.Add(time.Second)
	f.tick++
	return result
}

///////////////////////////////////////////////////////////////////////////
// outbound sweep

type outboundCase struct {
	acType   string
	ias, alt float32
	legNM    float32 // length of the legs into and out of the turn fix
	turnDeg  float32 // signed course change at the middle fix; + right
	windRel  float32 // wind "from," degrees relative to the outbound course; -1 = calm
	windKts  float32
	slowTo   float32 // if nonzero, assigned at the start
	climbTo  float32 // if nonzero, assigned at the start
}

func (c outboundCase) name() string {
	s := fmt.Sprintf("%s/%.0fkt@%.0f/turn%.0f", c.acType, c.ias, c.alt, c.turnDeg)
	if c.windKts > 0 {
		s += fmt.Sprintf("/wind%.0f@rel%.0f", c.windKts, c.windRel)
	}
	if c.slowTo != 0 {
		s += fmt.Sprintf("/slow%.0f", c.slowTo)
	}
	if c.climbTo != 0 {
		s += fmt.Sprintf("/climb%.0f", c.climbTo)
	}
	return s
}

type outboundMetrics struct {
	fireTick   int     // tick the deciding predicate sequenced the turn fix
	otherFire  int     // first tick the non-deciding predicate returned true (0 = never before fireTick)
	distAtFire float32 // distance from the fix when the turn started, nm
	gsAtFire   float32 // ground speed when the turn started, kts
	overshoot  float32 // max distance past the outbound course on the far side, nm
	settleTime int     // ticks from fireTick until within 0.1nm of the course for 10 consecutive ticks
	endOffset  float32 // |cross-track| at the end of the measurement window, nm
}

// runOutboundCase flies inbound leg A->B with course change c.turnDeg at B
// and measures how the selected predicate starts and completes the turn.
func runOutboundCase(t *testing.T, c outboundCase, analyticDecides bool) outboundMetrics {
	t.Helper()

	saved := useAnalyticTurnPredicates
	useAnalyticTurnPredicates = analyticDecides
	defer func() { useAnalyticTurnPredicates = saved }()

	// Geometry around KJFK for realistic magnetic variation: A is legNM
	// inbound to B on true course 20, C is legNM past B on the turned
	// course. The legs must be long enough for the turn radius: a fast
	// jet's 90-degree fly-by consumes several miles on each side of the
	// fix.
	base := av.DB.Airports["KJFK"].Location
	nmPerLong := math.NMPerLongitudeAt(base)
	const inTrue = 20
	outTrue := math.NormalizeHeading(float32(inTrue) + c.turnDeg)
	pB := math.Offset2LL(base, math.TrueHeading(350), 40, nmPerLong)
	pA := math.Offset2LL(pB, math.TrueHeading(inTrue+180), c.legNM, nmPerLong)
	pC := math.Offset2LL(pB, math.TrueHeading(outTrue), c.legNM, nmPerLong)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pA.DMSString() + " " + pB.DMSString() + " " + pC.DMSString(),
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     c.acType,
		InitialAltitude:  c.alt,
		InitialSpeed:     c.ias,
		AssignedAltitude: c.alt,
	})
	if c.windKts > 0 {
		f.SetWind(math.NormalizeHeading(outTrue+c.windRel), c.windKts)
	}
	if c.slowTo != 0 {
		f.AssignSpeed(c.slowTo)
	} else {
		f.AssignSpeed(c.ias)
	}
	if c.climbTo != 0 {
		f.AssignAltitude(c.climbTo)
	}

	bFix := pB.DMSString()
	var m outboundMetrics

	outboundPredicateProbe = func(sim, analytic bool) {
		if len(f.nav.Waypoints) == 0 || f.nav.Waypoints[0].Fix != bFix {
			return
		}
		other := analytic
		if analyticDecides {
			other = sim
		}
		if other && m.otherFire == 0 {
			m.otherFire = f.tick
		}
	}
	defer func() { outboundPredicateProbe = nil }()

	// Signed distance from the outbound course line through B.
	pBnm := math.LL2NM(pB, nmPerLong)
	pB2 := math.Add2f(pBnm, math.SinCos(math.Radians(outTrue)))
	cross := func() float32 {
		return math.SignedPointLineDistance(math.LL2NM(f.nav.FlightState.Position, nmPerLong), pBnm, pB2)
	}

	var initialSide float32
	for f.tick < 1200 {
		if len(f.nav.Waypoints) > 0 && f.nav.Waypoints[0].Fix == bFix && initialSide == 0 {
			initialSide = math.Sign(cross())
		}
		result := f.tickOnce()
		if result.PassedWaypoint != nil && result.PassedWaypoint.Fix == bFix {
			m.fireTick = f.tick
			m.distAtFire = math.NMDistance2LLFast(f.nav.FlightState.Position, pB, nmPerLong)
			m.gsAtFire = f.nav.FlightState.GS
			break
		}
	}
	if m.fireTick == 0 {
		t.Fatalf("%s: fix was never sequenced", c.name())
	}

	// Measure the turn onto the outbound course, stopping at a fixed
	// distance from C so both the sim- and analytic-decided runs measure
	// over the same stretch of the leg: past that the aircraft
	// legitimately leaves the course as it anticipates the turn at C, and
	// the two runs sequence C at different times.
	settled := 0
	for f.tick < m.fireTick+400 {
		result := f.tickOnce()
		if result.PassedWaypoint != nil ||
			math.NMDistance2LLFast(f.nav.FlightState.Position, pC, nmPerLong) < c.legNM/3 {
			break
		}
		d := cross()
		m.overshoot = max(m.overshoot, -initialSide*d)
		if math.Abs(d) < 0.1 {
			if settled++; settled == 10 && m.settleTime == 0 {
				m.settleTime = f.tick - 9 - m.fireTick
			}
		} else {
			settled = 0
		}
		m.endOffset = math.Abs(d)
	}
	return m
}

func outboundSweepCases(short bool) []outboundCase {
	aircraft := []struct {
		typ      string
		ias, alt float32
		legNM    float32
	}{
		{"C172", 110, 3000, 14},  // turns at the full standard rate
		{"PC12", 170, 8000, 14},  // near the standard-rate boundary
		{"A320", 250, 10000, 16}, // bank-limited
		{"E75L", 290, 24000, 30}, // strongly bank-limited, ~6nm turn radius
		{"B744", 280, 35000, 30}, // 30 deg bank, slow roll rate, ~490 KTAS
	}
	turns := []float32{15, 30, -45, 60, -90, 120, -150, 175}
	if short {
		turns = []float32{30, -90, 175}
	}

	var cases []outboundCase
	for _, ac := range aircraft {
		for _, turn := range turns {
			// Calm and a crosswind case for every aircraft/turn pair.
			cases = append(cases,
				outboundCase{acType: ac.typ, ias: ac.ias, alt: ac.alt, legNM: ac.legNM, turnDeg: turn, windRel: -1},
				outboundCase{acType: ac.typ, ias: ac.ias, alt: ac.alt, legNM: ac.legNM, turnDeg: turn, windRel: 90, windKts: 30})
		}
	}

	// Wind direction sweep on a representative bank-limited case.
	for rel := float32(0); rel < 360; rel += 45 {
		cases = append(cases, outboundCase{acType: "A320", ias: 250, alt: 10000, legNM: 16, turnDeg: -90, windRel: rel, windKts: 30})
		if !short {
			cases = append(cases, outboundCase{acType: "A320", ias: 250, alt: 10000, legNM: 16, turnDeg: 60, windRel: rel, windKts: 15})
			cases = append(cases, outboundCase{acType: "E75L", ias: 290, alt: 24000, legNM: 30, turnDeg: 120, windRel: rel, windKts: 50})
		}
	}

	// Speed and altitude transients through the turn.
	for _, ac := range aircraft {
		cases = append(cases,
			outboundCase{acType: ac.typ, ias: ac.ias, alt: ac.alt, legNM: ac.legNM, turnDeg: -90, windRel: 90, windKts: 20, slowTo: max(ac.ias-60, 90)},
			outboundCase{acType: ac.typ, ias: ac.ias, alt: ac.alt, legNM: ac.legNM, turnDeg: 60, windRel: -1, climbTo: ac.alt + 6000})
	}
	return cases
}

func TestOutboundTurnComparisonSweep(t *testing.T) {
	var worstLate, worstEarly int
	var worstOvershootDelta float32

	for _, c := range outboundSweepCases(testing.Short()) {
		t.Run(c.name(), func(t *testing.T) {
			sim := runOutboundCase(t, c, false)
			an := runOutboundCase(t, c, true)

			// The analytic predicate may correctly fire earlier than the
			// sim one (which turns late for bank-limited aircraft), but
			// firing later means it missed the turn point.
			if an.fireTick > sim.fireTick+2 {
				t.Errorf("analytic fired at tick %d, sim at %d", an.fireTick, sim.fireTick)
			}
			worstLate = max(worstLate, an.fireTick-sim.fireTick)
			worstEarly = max(worstEarly, sim.fireTick-an.fireTick)

			// Sequencing must come from anticipating the turn, not from the
			// almost-at-the-fix fallback that fires within 2 seconds of the
			// fix; skipping over the turn point entirely and catching it
			// there would be a missed trigger. Some geometries (large turn
			// angles in strong wind) legitimately defeat anticipation, so
			// only fail if the sim managed to anticipate.
			atFix := func(m outboundMetrics) bool { return m.distAtFire < 2.5*m.gsAtFire/3600 }
			if math.Abs(c.turnDeg) >= 30 && atFix(an) && !atFix(sim) {
				t.Errorf("analytic sequenced only %.2fnm from the fix at %.0fkt; sim anticipated %.2fnm out",
					an.distAtFire, an.gsAtFire, sim.distAtFire)
			}

			// The turn the analytic starts must be at least as clean as the
			// sim's. After rolling out, waypoint navigation flies direct to
			// the next fix rather than re-tracking the course line, so the
			// quality comparisons are relative to the sim's track with only
			// loose absolute floors. Turns beyond 120 degrees necessarily
			// loop past the outbound course (ideal fly-by anticipation
			// would be tens of miles, which the entry gates rightly
			// forbid), so for those only compare the loops.
			if math.Abs(c.turnDeg) <= 120 {
				if an.overshoot > max(sim.overshoot+0.05, 0.15) {
					t.Errorf("analytic overshoot %.3fnm vs sim %.3fnm", an.overshoot, sim.overshoot)
				}
			} else if an.overshoot > sim.overshoot+0.5 {
				t.Errorf("analytic overshoot %.3fnm vs sim %.3fnm", an.overshoot, sim.overshoot)
			}
			worstOvershootDelta = max(worstOvershootDelta, an.overshoot-sim.overshoot)

			if sim.settleTime != 0 && an.settleTime != 0 && an.settleTime > sim.settleTime+15 {
				t.Errorf("analytic settled in %ds vs sim %ds", an.settleTime, sim.settleTime)
			} else if sim.settleTime != 0 && an.settleTime == 0 {
				t.Errorf("sim settled in %ds but analytic never settled", sim.settleTime)
			}
			if an.endOffset > max(sim.endOffset+0.05, 0.12) {
				t.Errorf("analytic ended %.3fnm off course vs sim %.3fnm", an.endOffset, sim.endOffset)
			}

			if t.Failed() {
				t.Logf("sim: %+v", sim)
				t.Logf("analytic: %+v", an)
			} else {
				t.Logf("stats: fireDelta=%+d overshootSim=%.2f overshootAn=%.2f settleSim=%d settleAn=%d",
					an.fireTick-sim.fireTick, sim.overshoot, an.overshoot, sim.settleTime, an.settleTime)
			}
		})
	}
	t.Logf("outbound sweep: analytic fired at most %d ticks after sim, at most %d before; worst overshoot delta %+.3fnm",
		worstLate, worstEarly, worstOvershootDelta)
}

///////////////////////////////////////////////////////////////////////////
// localizer intercept sweep

type interceptCase struct {
	acType  string
	ias     float32
	angle   float32 // degrees between the initial heading and the localizer course
	distNM  float32 // initial position along the localizer from the threshold
	lateral float32 // lateral offset from the centerline; negative = left of outbound
	windDir float32 // degrees true "from"; -1 = calm
	windKts float32
}

func (c interceptCase) name() string {
	s := fmt.Sprintf("%s/%.0fkt/int%.0f/lat%.1f", c.acType, c.ias, c.angle, c.lateral)
	if c.windKts > 0 {
		s += fmt.Sprintf("/wind%.0f@%.0f", c.windKts, c.windDir)
	}
	return s
}

type interceptMetrics struct {
	firstTurnSim int // first tick the sim predicate returned Turn
	firstTurnAn  int // same for the analytic predicate, on the same trajectory
	mismatch     int // ticks where the two classified differently
	longestRun   int // longest consecutive span of such ticks
	nonAdjacent  int // ticks where the classes differed by more than one step
	established  int // tick InterceptState reached OnApproachCourse; 0 = never
	vectors      bool
	crossings    int
	maxOff       float32 // max |centerline distance| from established+30 on
}

func runInterceptCase(t *testing.T, c interceptCase, analyticDecides bool) interceptMetrics {
	t.Helper()

	saved := useAnalyticTurnPredicates
	useAnalyticTurnPredicates = analyticDecides
	defer func() { useAnalyticTurnPredicates = saved }()

	apg := LookupApproachGeometry(t, "KJFK", "I22L")
	courseMag := math.TrueToMagnetic(apg.RunwayHeading, apg.MagneticVariation)
	pos := apg.ThresholdOffset(c.distNM, c.lateral)

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     c.acType,
		InitialAltitude:  3000,
		InitialSpeed:     c.ias,
		InitialHeading:   float32(math.NormalizeHeading(courseMag - math.MagneticHeading(c.angle))),
	})
	if c.windKts > 0 {
		f.SetWind(c.windDir, c.windKts)
	}
	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")

	var m interceptMetrics
	run := 0
	interceptPredicateProbe = func(sim, analytic turnToInterceptResult) {
		if f.nav.Approach.InterceptState != InitialHeading {
			return
		}
		if sim == turnToInterceptTurn && m.firstTurnSim == 0 {
			m.firstTurnSim = f.tick
		}
		if analytic == turnToInterceptTurn && m.firstTurnAn == 0 {
			m.firstTurnAn = f.tick
		}
		if sim != analytic {
			m.mismatch++
			if run++; run > m.longestRun {
				m.longestRun = run
			}
			if d := int(sim) - int(analytic); d < -1 || d > 1 {
				m.nonAdjacent++
			}
		} else {
			run = 0
		}
	}
	defer func() { interceptPredicateProbe = nil }()

	var prevSD float32
	for f.tick < 900 {
		f.tickOnce()
		if math.NMDistance2LLFast(f.nav.FlightState.Position, apg.Threshold, apg.NmPerLongitude) < 1.5 {
			// Nearly at the runway; landing behavior takes over from here.
			break
		}
		sd := f.SignedCenterlineDistance()
		if f.tick > 5 && prevSD != 0 && math.Sign(sd) != math.Sign(prevSD) &&
			math.Abs(sd) > 0.02 && math.Abs(prevSD) > 0.02 {
			m.crossings++
		}
		prevSD = sd

		if f.nav.Approach.InterceptState == OnApproachCourse && m.established == 0 {
			m.established = f.tick
		}
		if m.established != 0 && f.tick > m.established+30 {
			m.maxOff = max(m.maxOff, math.Abs(sd))
		}
		if m.established != 0 && f.tick > m.established+120 {
			break
		}
		if f.nav.Approach.RequestVectors {
			m.vectors = true
			break
		}
	}
	return m
}

func compareInterceptRuns(t *testing.T, c interceptCase) {
	t.Helper()

	sim := runInterceptCase(t, c, false)
	an := runInterceptCase(t, c, true)

	// The two runs must reach the same disposition: either both establish
	// on the approach course or both give up and request vectors.
	if (sim.established != 0) != (an.established != 0) || sim.vectors != an.vectors {
		t.Errorf("dispositions differ: sim established=%d vectors=%v, analytic established=%d vectors=%v",
			sim.established, sim.vectors, an.established, an.vectors)
	}
	if sim.established != 0 && an.established != 0 {
		if d := an.established - sim.established; d < -15 || d > 15 {
			t.Errorf("established at tick %d vs sim %d", an.established, sim.established)
		}
		if an.crossings > 2 {
			t.Errorf("analytic run crossed the centerline %d times", an.crossings)
		}
		if an.maxOff > max(sim.maxOff+0.05, 0.15) {
			t.Errorf("analytic run strayed %.3fnm off the centerline when established, sim %.3fnm",
				an.maxOff, sim.maxOff)
		}
	}

	// Per-tick classification parity, measured on the sim-decided
	// trajectory where both predicates saw identical state.
	if sim.firstTurnSim != 0 && sim.firstTurnAn != 0 {
		if d := sim.firstTurnAn - sim.firstTurnSim; d < -3 || d > 3 {
			t.Errorf("first Turn classification at tick %d (analytic) vs %d (sim)", sim.firstTurnAn, sim.firstTurnSim)
		}
	}
	if sim.nonAdjacent > 0 {
		t.Errorf("%d ticks with non-adjacent classifications", sim.nonAdjacent)
	}
	if sim.longestRun > 2 {
		t.Errorf("classifications disagreed for %d consecutive ticks", sim.longestRun)
	}

	if t.Failed() {
		t.Logf("sim: %+v", sim)
		t.Logf("analytic: %+v", an)
	} else {
		t.Logf("stats: estDelta=%d firstTurnDelta=%d mismatch=%d longestRun=%d vectors=%v",
			an.established-sim.established, sim.firstTurnAn-sim.firstTurnSim,
			sim.mismatch, sim.longestRun, sim.vectors)
	}
}

func TestInterceptComparisonSweep(t *testing.T) {
	aircraft := []struct {
		typ string
		ias float32
	}{{"A320", 180}, {"C172", 100}, {"E75L", 210}, {"B744", 170}}
	laterals := map[float32]float32{10: 1, 20: 2, 30: 2.5, 43: 3}

	for _, ac := range aircraft {
		for angle, lateral := range laterals {
			base := interceptCase{acType: ac.typ, ias: ac.ias, angle: angle, distNM: 11, lateral: -lateral, windDir: -1}
			t.Run(base.name(), func(t *testing.T) { compareInterceptRuns(t, base) })

			wind := base
			wind.windDir, wind.windKts = 270, 30
			t.Run(wind.name(), func(t *testing.T) { compareInterceptRuns(t, wind) })
		}
	}

	// Wind direction sweep at a typical and at the steepest intercept angle.
	for _, angle := range []float32{20, 43} {
		for dir := float32(0); dir < 360; dir += 45 {
			c := interceptCase{acType: "A320", ias: 180, angle: angle, distNM: 11,
				lateral: -laterals[angle], windDir: dir, windKts: 30}
			t.Run(c.name(), func(t *testing.T) { compareInterceptRuns(t, c) })
		}
	}
}

// TestInterceptOvershootClassificationSweep starts aircraft close to the
// centerline at converging angles so that the intercept blows through,
// exercising the Correctable/MajorOvershoot classifications and recovery.
func TestInterceptOvershootClassificationSweep(t *testing.T) {
	for _, angle := range []float32{20, 30, 40} {
		for _, lateral := range []float32{0.2, 0.3, 0.5, 0.8} {
			for _, wind := range []struct{ dir, kts float32 }{{-1, 0}, {270, 30}, {90, 30}} {
				c := interceptCase{acType: "A320", ias: 180, angle: angle, distNM: 10,
					lateral: -lateral, windDir: wind.dir, windKts: wind.kts}
				t.Run(c.name(), func(t *testing.T) { compareInterceptRuns(t, c) })
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// @t course intercept sweep

// runCourseInterceptCase is the A/B version of checkCourseIntercept in
// course_test.go: the aircraft leaves SKORR on a heading hOff degrees off
// the direct course to WAVEY with an @t termination onto a course cOff
// degrees off it, and must join that course and then track it.
func runCourseInterceptCase(t *testing.T, hOff, cOff, windRel float32, windKts float32,
	analyticDecides bool) (joinTick int, maxOff float32) {
	t.Helper()

	saved := useAnalyticTurnPredicates
	useAnalyticTurnPredicates = analyticDecides
	defer func() { useAnalyticTurnPredicates = saved }()

	skorr, _ := av.DB.LookupWaypoint("SKORR")
	wavey, _ := av.DB.LookupWaypoint("WAVEY")
	kjfk := av.DB.Airports["KJFK"]
	nmPerLong := math.NMPerLongitudeAt(kjfk.Location)
	magVar, err := av.DB.MagneticGrid.Lookup(kjfk.Location)
	if err != nil {
		t.Fatal(err)
	}

	direct := math.TrueToMagnetic(math.Heading2LL(skorr, wavey, nmPerLong), magVar)
	heading := math.NormalizeHeading(direct + math.MagneticHeading(hOff))
	course := math.NormalizeHeading(direct + math.MagneticHeading(cOff))

	f := NewArrivalFlight(t, ArrivalConfig{
		Waypoints:        fmt.Sprintf("SKORR/h%d@t%d WAVEY", int(heading), int(course)),
		DepartureAirport: "KJFK",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
	})
	if windKts > 0 {
		courseTrue := float32(math.MagneticToTrue(course, magVar))
		f.SetWind(math.NormalizeHeading(courseTrue+windRel), windKts)
	}

	flyingHeading := false
	for f.tick < 1200 {
		result := f.tickOnce()
		if result.PassedWaypoint != nil && result.PassedWaypoint.Fix == "WAVEY" {
			break
		}
		if !flyingHeading {
			flyingHeading = len(f.nav.Heading.Maneuvers) > 0
		} else if joinTick == 0 {
			if len(f.nav.Heading.Maneuvers) == 0 {
				joinTick = f.tick
			}
		} else if f.tick > joinTick+60 {
			maxOff = max(maxOff, math.Abs(courseOffset(f, course)))
		}
	}
	if joinTick == 0 {
		t.Fatalf("aircraft never joined the %03d course", int(course))
	}
	if maxOff == 0 {
		t.Fatalf("joined too close to WAVEY to measure course tracking")
	}
	return
}

func TestCourseInterceptComparisonSweep(t *testing.T) {
	type courseCase struct {
		hOff, cOff       float32
		windRel, windKts float32
	}
	var cases []courseCase
	for _, off := range []float32{10, 20, 30} {
		cases = append(cases,
			courseCase{-off, off, -1, 0},
			courseCase{-off, off, 90, 40},
			courseCase{off, -off, -90, 40})
	}
	cases = append(cases,
		courseCase{-20, 20, 0, 40},
		courseCase{-20, 20, 180, 40})

	for _, c := range cases {
		name := fmt.Sprintf("h%+.0f/c%+.0f", c.hOff, c.cOff)
		if c.windKts > 0 {
			name += fmt.Sprintf("/wind%.0f@rel%.0f", c.windKts, c.windRel)
		}
		t.Run(name, func(t *testing.T) {
			simJoin, simOff := runCourseInterceptCase(t, c.hOff, c.cOff, c.windRel, c.windKts, false)
			anJoin, anOff := runCourseInterceptCase(t, c.hOff, c.cOff, c.windRel, c.windKts, true)

			if d := anJoin - simJoin; d < -3 || d > 3 {
				t.Errorf("analytic joined the course at tick %d vs sim %d", anJoin, simJoin)
			}
			if anOff > 0.5 {
				t.Errorf("analytic strayed %.2fnm from the course after joining", anOff)
			}
			if anOff > simOff+0.1 {
				t.Errorf("analytic tracked the course %.2fnm off vs sim %.2fnm", anOff, simOff)
			}
		})
	}
}

///////////////////////////////////////////////////////////////////////////
// benchmarks

// benchOutboundState flies the standard outbound geometry until the
// aircraft is inside the decision window for a 90 degree turn, where the
// predicates run their full evaluation every tick.
func benchOutboundState(b *testing.B) (*FlightTest, math.Point2LL, math.MagneticHeading) {
	base := av.DB.Airports["KJFK"].Location
	nmPerLong := math.NMPerLongitudeAt(base)
	pB := math.Offset2LL(base, math.TrueHeading(350), 40, nmPerLong)
	pA := math.Offset2LL(pB, math.TrueHeading(200), 16, nmPerLong)
	pC := math.Offset2LL(pB, math.TrueHeading(110), 16, nmPerLong)

	f := NewArrivalFlight(b, ArrivalConfig{
		Waypoints:        pA.DMSString() + " " + pB.DMSString() + " " + pC.DMSString(),
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  10000,
		InitialSpeed:     250,
		AssignedAltitude: 10000,
	})
	for math.NMDistance2LLFast(f.nav.FlightState.Position, pB, nmPerLong) > 2.5 {
		f.tickOnce()
	}
	hdg := math.TrueToMagnetic(math.Heading2LL(pB, pC, nmPerLong), f.nav.FlightState.MagneticVariation)
	return f, pB, hdg
}

func BenchmarkShouldTurnForOutboundSim(b *testing.B) {
	f, pB, hdg := benchOutboundState(b)
	wxs := f.weather(f.nav.FlightState.Altitude)
	b.ResetTimer()
	for range b.N {
		f.nav.shouldTurnForOutboundSim(pB, hdg, av.TurnClosest, wxs)
	}
}

func BenchmarkShouldTurnForOutboundAnalytic(b *testing.B) {
	f, pB, hdg := benchOutboundState(b)
	wxs := f.weather(f.nav.FlightState.Altitude)
	b.ResetTimer()
	for range b.N {
		f.nav.shouldTurnForOutboundAnalytic(pB, hdg, av.TurnClosest, wxs)
	}
}

// benchInterceptState flies a 30 degree localizer intercept until the
// aircraft is close enough that the predicates evaluate the full turn.
func benchInterceptState(b *testing.B) (*FlightTest, math.Point2LL, math.MagneticHeading) {
	apg := LookupApproachGeometry(b, "KJFK", "I22L")
	courseMag := math.TrueToMagnetic(apg.RunwayHeading, apg.MagneticVariation)
	pos := apg.ThresholdOffset(11, -2.5)

	f := NewArrivalFlight(b, ArrivalConfig{
		Waypoints:        pos.DMSString() + " HAUPT/a6000 LEFER/a4000 ROSLY/a3000",
		DepartureAirport: "KMCO",
		ArrivalAirport:   "KJFK",
		AircraftType:     "A320",
		InitialAltitude:  3000,
		InitialSpeed:     180,
		InitialHeading:   float32(math.NormalizeHeading(courseMag - 30)),
	})
	f.ExpectApproach("I22L")
	f.ClearedApproach("I22L")
	for math.Abs(f.SignedCenterlineDistance()) > 1.2 {
		f.tickOnce()
	}
	return f, apg.Threshold, courseMag
}

func BenchmarkShouldTurnToInterceptSim(b *testing.B) {
	f, p0, hdg := benchInterceptState(b)
	wxs := f.weather(f.nav.FlightState.Altitude)
	b.ResetTimer()
	for range b.N {
		f.nav.shouldTurnToInterceptSim(p0, hdg, av.TurnClosest, wxs)
	}
}

func BenchmarkShouldTurnToInterceptAnalytic(b *testing.B) {
	f, p0, hdg := benchInterceptState(b)
	wxs := f.weather(f.nav.FlightState.Altitude)
	b.ResetTimer()
	for range b.N {
		f.nav.shouldTurnToInterceptAnalytic(p0, hdg, av.TurnClosest, wxs)
	}
}
