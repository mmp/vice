// nav/turncompare_test.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// Quality tests for the turn-anticipation predicates: a conformance test
// that pins the turnPath model to the tick-by-tick flight model, and sweeps
// that fly outbound fly-by turns, localizer intercepts, and @t course joins
// across aircraft, geometries, and winds, asserting the quality of the
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
// outbound turn sweep

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
	fireTick   int     // tick the turn fix was sequenced
	distAtFire float32 // distance from the fix when the turn started, nm
	gsAtFire   float32 // ground speed when the turn started, kts
	overshoot  float32 // max distance past the outbound course on the far side, nm
	settleTime int     // ticks from fireTick until within 0.1nm of the course for 10 consecutive ticks
	endOffset  float32 // |cross-track| at the end of the measurement window, nm
}

// runOutboundCase flies inbound leg A->B with course change c.turnDeg at B
// and measures how the aircraft starts and completes the turn.
func runOutboundCase(t *testing.T, c outboundCase) outboundMetrics {
	t.Helper()

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
	// distance from C: past that the aircraft legitimately leaves the
	// course as it anticipates the turn at C.
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

func TestOutboundTurnSweep(t *testing.T) {
	for _, c := range outboundSweepCases(testing.Short()) {
		t.Run(c.name(), func(t *testing.T) {
			m := runOutboundCase(t, c)

			// Sequencing must come from anticipating the turn, not from the
			// almost-at-the-fix fallback that fires within 2 seconds of the
			// fix. Course changes past 100 degrees are exempt: their fly-by
			// anticipation is capped (see shouldTurnForOutbound), and in
			// strong winds the crossing may only be found near the fix.
			if a := math.Abs(c.turnDeg); a >= 30 && a <= 100 && m.distAtFire < 2.5*m.gsAtFire/3600 {
				t.Errorf("sequenced only %.2fnm from the fix at %.0fkt", m.distAtFire, m.gsAtFire)
			}

			// Up to the 100 degree anticipation cap a fly-by should roll
			// out on the outbound course and stay there; larger course
			// changes necessarily loop past it. Speed and altitude changes
			// during the turn get extra slack since the turn radius is
			// predicted from the state at its start.
			if math.Abs(c.turnDeg) <= 100 {
				limit := float32(0.2)
				if c.slowTo != 0 || c.climbTo != 0 {
					limit = 0.6
				}
				if m.overshoot > limit {
					t.Errorf("overshot the outbound course by %.3fnm", m.overshoot)
				}
				if m.settleTime == 0 {
					t.Errorf("never settled on the outbound course")
				} else if m.endOffset > 0.12 {
					t.Errorf("ended %.3fnm off course", m.endOffset)
				}
			}
		})
	}
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
	established int // tick InterceptState reached OnApproachCourse; 0 = never
	vectors     bool
	nearRunway  bool    // measurement ended within 1.5nm of the threshold
	endSD       float32 // signed centerline distance at that point
	crossings   int
	maxOff      float32 // max |centerline distance| from established+30 on
}

func runInterceptCase(t *testing.T, c interceptCase) interceptMetrics {
	t.Helper()

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
	var prevSD float32
	for f.tick < 900 {
		f.tickOnce()
		if math.NMDistance2LLFast(f.nav.FlightState.Position, apg.Threshold, apg.NmPerLongitude) < 1.5 {
			// Nearly at the runway; landing behavior takes over from here.
			m.nearRunway, m.endSD = true, f.SignedCenterlineDistance()
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

// checkInterceptCase verifies that the intercept reaches a definite outcome
// and, when the aircraft establishes on the approach course, that the
// capture is clean.
func checkInterceptCase(t *testing.T, c interceptCase, maxOff float32) interceptMetrics {
	t.Helper()

	m := runInterceptCase(t, c)
	if m.established != 0 && m.vectors {
		t.Errorf("both established (tick %d) and requested vectors", m.established)
	} else if m.established == 0 && !m.vectors {
		// A tailwind can push the aircraft to the runway before the
		// on-course state is flagged; that's fine as long as it got there
		// converged on the centerline.
		if !m.nearRunway || math.Abs(m.endSD) > 0.3 {
			t.Errorf("no definite outcome: nearRunway=%v endSD=%.3f", m.nearRunway, m.endSD)
		}
	}
	if m.established != 0 {
		if m.crossings > 2 {
			t.Errorf("crossed the centerline %d times", m.crossings)
		}
		if m.maxOff > maxOff {
			t.Errorf("strayed %.3fnm off the centerline when established", m.maxOff)
		}
	}
	return m
}

func TestInterceptSweep(t *testing.T) {
	aircraft := []struct {
		typ string
		ias float32
	}{{"A320", 180}, {"C172", 100}, {"E75L", 210}, {"B744", 170}}
	laterals := map[float32]float32{10: 1, 20: 2, 30: 2.5, 43: 3}

	vectored := 0
	check := func(t *testing.T, c interceptCase) {
		if m := checkInterceptCase(t, c, 0.15); m.vectors {
			vectored++
		}
	}

	for _, ac := range aircraft {
		for angle, lateral := range laterals {
			base := interceptCase{acType: ac.typ, ias: ac.ias, angle: angle, distNM: 11, lateral: -lateral, windDir: -1}
			t.Run(base.name(), func(t *testing.T) { check(t, base) })

			wind := base
			wind.windDir, wind.windKts = 270, 30
			t.Run(wind.name(), func(t *testing.T) { check(t, wind) })
		}
	}

	// Wind direction sweep at a typical and at the steepest intercept angle.
	for _, angle := range []float32{20, 43} {
		for dir := float32(0); dir < 360; dir += 45 {
			c := interceptCase{acType: "A320", ias: 180, angle: angle, distNM: 11,
				lateral: -laterals[angle], windDir: dir, windKts: 30}
			t.Run(c.name(), func(t *testing.T) { check(t, c) })
		}
	}

	// The near-limit 43 degree intercepts routinely blow through and end
	// with a request for vectors (the simulation-based predicates behaved
	// the same way); the shallower ones essentially never do. Guard against
	// wholesale regressions in either direction.
	if vectored > 22 {
		t.Errorf("%d intercepts ended requesting vectors", vectored)
	}
}

// TestInterceptOvershootSweep starts aircraft close to the centerline at
// converging angles so that the intercept blows through, exercising the
// Correctable/MajorOvershoot classifications and recovery.
func TestInterceptOvershootSweep(t *testing.T) {
	for _, angle := range []float32{20, 30, 40} {
		for _, lateral := range []float32{0.2, 0.3, 0.5, 0.8} {
			for _, wind := range []struct{ dir, kts float32 }{{-1, 0}, {270, 30}, {90, 30}} {
				c := interceptCase{acType: "A320", ias: 180, angle: angle, distNM: 10,
					lateral: -lateral, windDir: wind.dir, windKts: wind.kts}
				t.Run(c.name(), func(t *testing.T) { checkInterceptCase(t, c, 0.25) })
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// @t course intercept sweep

// runCourseInterceptCase is a parameterized version of the flights in
// course_test.go: the aircraft leaves SKORR on a heading hOff degrees off
// the direct course to WAVEY with an @t termination onto a course cOff
// degrees off it, and must join that course and then track it.
func runCourseInterceptCase(t *testing.T, hOff, cOff, windRel float32, windKts float32) (joinTick int, maxOff float32) {
	t.Helper()

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
			maxOff = max(maxOff, math.Abs(courseOffset(f, "WAVEY", course)))
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

func TestCourseInterceptSweep(t *testing.T) {
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
			joinTick, maxOff := runCourseInterceptCase(t, c.hOff, c.cOff, c.windRel, c.windKts)

			if joinTick < 60 {
				t.Errorf("joined the course at tick %d; expected to fly the heading first", joinTick)
			}
			if maxOff > 0.5 {
				t.Errorf("strayed %.2fnm from the course after joining", maxOff)
			}
		})
	}
}

///////////////////////////////////////////////////////////////////////////
// benchmarks

// benchOutboundState flies the standard outbound geometry until the
// aircraft is inside the decision window for a 90 degree turn, where the
// predicate runs its full evaluation every tick.
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

func BenchmarkShouldTurnForOutbound(b *testing.B) {
	f, pB, hdg := benchOutboundState(b)
	wxs := f.weather(f.nav.FlightState.Altitude)
	b.ResetTimer()
	for range b.N {
		f.nav.shouldTurnForOutbound(pB, hdg, av.TurnClosest, wxs)
	}
}

// benchInterceptState flies a 30 degree localizer intercept until the
// aircraft is close enough that the predicate evaluates the full turn.
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

func BenchmarkShouldTurnToIntercept(b *testing.B) {
	f, p0, hdg := benchInterceptState(b)
	wxs := f.weather(f.nav.FlightState.Altitude)
	b.ResetTimer()
	for range b.N {
		f.nav.shouldTurnToIntercept(p0, hdg, av.TurnClosest, wxs)
	}
}
