// radar/route_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package radar

import (
	"testing"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
)

// The tests use 60 nm per degree of longitude and no magnetic variation,
// so nm coordinates are (longitude, latitude) times 60 and magnetic
// headings are true.
const testNmPerLongitude = 60

func testWalker(rc RouteDrawContext) *routeWalker {
	return newRouteWalker(testNmPerLongitude, 0, rc, renderer.GetColoredLinesDrawBuilder(), renderer.RGB{}, NewDrawnRoutes())
}

func fixAt(name string, x, y float32, groups ...av.WaypointActionGroup) av.Waypoint {
	wp := av.Waypoint{Fix: name, Location: math.NM2LL([2]float32{x, y}, testNmPerLongitude)}
	if len(groups) > 0 {
		wp.InitExtra().ActionGroups = groups
	}
	return wp
}

func heading(h int16) av.WaypointActions {
	return av.WaypointActions{Heading: av.WaypointHeadingAction{Heading: h}}
}

func nmPoint(x, y float32) math.Point2LL { return math.NM2LL([2]float32{x, y}, testNmPerLongitude) }

// labelAt returns where the label with the given text was placed.
func labelAt(t *testing.T, w *routeWalker, text string) [2]float32 {
	t.Helper()
	for _, l := range w.labels {
		if l.text == text {
			return l.p
		}
	}
	var texts []string
	for _, l := range w.labels {
		texts = append(texts, l.text)
	}
	t.Fatalf("no label %q; have %v", text, texts)
	return [2]float32{}
}

func expectNear(t *testing.T, what string, p, want [2]float32, tol float32) {
	t.Helper()
	if math.Distance2f(p, want) > tol {
		t.Errorf("%s: got %v, want %v", what, p, want)
	}
}

func TestDistanceTrigger(t *testing.T) {
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", 0, 0, av.WaypointActionGroup{
			Actions: heading(90),
			Until:   av.WaypointActionTermination{Type: av.WaypointActionDistance, Distance: 5},
		}),
		fixAt("B", 20, 0),
	})
	expectNear(t, "@d5.0", labelAt(t, w, "@d5.0"), [2]float32{5, 0}, 0.05)
	expectNear(t, "pen", w.pen.p, [2]float32{20, 0}, 0.05)
}

func TestDMETrigger(t *testing.T) {
	dme := func(d float32, atOrAbove bool) av.WaypointActionTermination {
		return av.WaypointActionTermination{Type: av.WaypointActionDME, DMEFix: "FIX", DMEDistance: d,
			DMEFixLocation: nmPoint(0, 0), AtOrAbove: atOrAbove}
	}

	// Inside the circle, at or beyond: the far crossing.
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", -1, 0, av.WaypointActionGroup{Actions: heading(90), Until: dme(3, true)}),
		fixAt("B", 20, 0),
	})
	expectNear(t, "@FIX-D3.0+", labelAt(t, w, "@FIX-D3.0+"), [2]float32{3, 0}, 0.05)

	// Outside the circle, within: the near crossing.
	w = testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", -10, 0, av.WaypointActionGroup{Actions: heading(90), Until: dme(3, false)}),
		fixAt("B", 20, 0),
	})
	expectNear(t, "@FIX-D3.0-", labelAt(t, w, "@FIX-D3.0-"), [2]float32{-3, 0}, 0.05)

	// Outside, at or beyond: met at the fix, so it labels the fix.
	w = testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", -10, 0, av.WaypointActionGroup{Actions: heading(90), Until: dme(3, true)}),
		fixAt("B", 20, 0),
	})
	if w.fixes[0].actions != "h090/@FIX-D3.0+" {
		t.Errorf("fix actions %q", w.fixes[0].actions)
	}

	// A heading that misses the circle: indeterminate.
	w = testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", -10, 5, av.WaypointActionGroup{Actions: heading(90), Until: dme(3, false)}),
		fixAt("B", 20, 0),
	})
	expectNear(t, "@FIX-D3.0-?", labelAt(t, w, "@FIX-D3.0-?"), [2]float32{-10 + indeterminateLegLength, 5}, 0.05)
}

func TestRadialTrigger(t *testing.T) {
	radial := func(r int16) av.WaypointActionTermination {
		return av.WaypointActionTermination{Type: av.WaypointActionRadial, RadialFix: "FIX", Radial: r,
			RadialFixLocation: nmPoint(0, 5)}
	}

	// The 180 radial from a fix 5nm north of the leg crosses it at the origin.
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", -10, 0, av.WaypointActionGroup{Actions: heading(90), Until: radial(180)}),
		fixAt("B", 20, 0),
	})
	expectNear(t, "@FIX-R180", labelAt(t, w, "@FIX-R180"), [2]float32{0, 0}, 0.05)

	// The reciprocal radial doesn't count.
	w = testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", -10, 0, av.WaypointActionGroup{Actions: heading(90), Until: radial(360)}),
		fixAt("B", 20, 0),
	})
	labelAt(t, w, "@FIX-R360?")
}

func TestCourseTrigger(t *testing.T) {
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", 0, -5, av.WaypointActionGroup{
			Actions: heading(45),
			Until:   av.WaypointActionTermination{Type: av.WaypointActionCourse, Course: 90},
		}),
		fixAt("B", 10, 0),
	})
	// The turn starts a lead before the crossing at (5, 0) and rolls out
	// on the course, so the route arrives at B heading east.
	expectNear(t, "@crs090", labelAt(t, w, "@crs090"), [2]float32{5, 0}, 0.5)
	expectNear(t, "inbound to B", w.fixes[1].inbound, [2]float32{1, 0}, 0.02)
	expectNear(t, "pen", w.pen.p, [2]float32{10, 0}, 0.05)
}

func TestAltitudeTrigger(t *testing.T) {
	rc := RouteDrawContext{Departure: true, FieldElevation: 0, ClearedAltitude: 5000}
	altitude := func(alt int, atOrAbove bool) av.WaypointActionGroup {
		return av.WaypointActionGroup{
			Actions: heading(360),
			Until:   av.WaypointActionTermination{Type: av.WaypointActionAltitude, Altitude: alt, AtOrAbove: atOrAbove},
		}
	}

	// The profile holds field elevation until the runway's mid point, then
	// climbs 700 ft/nm: 350' at the departure end and 1000' 0.93nm past it.
	w := testWalker(rc)
	w.walk([]av.Waypoint{fixAt("4L", 0, 0), fixAt("4L-mid", 0, 1.5), fixAt("KXXX-22", 0, 2, altitude(1000, true)), fixAt("B", 0, 20)})
	expectNear(t, "@a1000+", labelAt(t, w, "@a1000+"), [2]float32{0, 2 + 650.0/700}, 0.05)

	// Descending to an altitude while climbing can't be placed.
	w = testWalker(rc)
	w.walk([]av.Waypoint{fixAt("4L", 0, 0), fixAt("4L-mid", 0, 1.5), fixAt("KXXX-22", 0, 2, altitude(200, false)), fixAt("B", 0, 20)})
	labelAt(t, w, "@a200-?")

	// Already above the altitude: met at the fix.
	w = testWalker(RouteDrawContext{InitialAltitude: 3000})
	w.walk([]av.Waypoint{fixAt("A", 0, 0, altitude(1000, true)), fixAt("B", 0, 20)})
	if w.fixes[0].actions != "h360/@a1000+" {
		t.Errorf("fix actions %q", w.fixes[0].actions)
	}

	// An arrival with no pinned altitude can't place it either.
	w = testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{fixAt("A", 0, 0, altitude(5000, true)), fixAt("B", 0, 20)})
	labelAt(t, w, "@a5000+?")
}

func TestOpenEndedHeading(t *testing.T) {
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", 0, 0, av.WaypointActionGroup{Actions: heading(90)}),
		fixAt("B", 10, 10),
	})
	if w.fixes[0].actions != "h090" {
		t.Errorf("fix actions %q", w.fixes[0].actions)
	}
	if w.disconnected {
		t.Errorf("still disconnected after reaching B")
	}
	expectNear(t, "pen", w.pen.p, [2]float32{10, 10}, 0.05)
	// The route resumes at B from the end of the stub.
	expectNear(t, "inbound to B", w.fixes[1].inbound, direction([2]float32{stubLength, 0}, [2]float32{10, 10}), 0.01)
}

func TestRadialJoin(t *testing.T) {
	// Track the 090 radial of a fix 3nm north of A: join it 3nm ahead of
	// A's projection onto it, then follow it until the trigger.
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", 0, 0, av.WaypointActionGroup{
			Actions: av.WaypointActions{Heading: av.WaypointHeadingAction{Heading: 90, Track: true, Fix: "FIX", FixLocation: nmPoint(-5, 3)}},
			Until:   av.WaypointActionTermination{Type: av.WaypointActionDistance, Distance: 10},
		}),
		fixAt("B", 30, 3),
	})
	p := labelAt(t, w, "@d10.0")
	if math.Abs(p[1]-3) > 0.05 {
		t.Errorf("trigger not on the radial: %v", p)
	}
	if math.Abs(math.Distance2f(p, [2]float32{0, 0})-10) > 0.05 {
		t.Errorf("trigger not 10nm from the group's start: %v", p)
	}
}

func TestProcedureTurn(t *testing.T) {
	racetrack := func(entry180NoPT bool) av.Waypoint {
		wp := fixAt("F", 0, 0)
		wp.InitExtra().ProcedureTurn = &av.ProcedureTurn{Type: av.PTRacetrack, RightTurns: true, NmLimit: 4,
			ExitAltitude: 3000, Entry180NoPT: entry180NoPT}
		return wp
	}

	// Arriving from the north onto a southbound inbound course is a direct
	// entry: a right turn at the fix onto the outbound leg on the west
	// side, 4nm north, and a turn back whose apex is a turn radius beyond
	// the leg. The exit altitude applies afterward.
	w := testWalker(RouteDrawContext{InitialAltitude: 5000})
	w.walk([]av.Waypoint{fixAt("P", 0, 3), racetrack(false), fixAt("N", 0, -10)})
	b := w.ld.Bounds()
	r := (&routeProfile{altitude: 5000, known: true}).TurnRadius()
	// The outbound leg's arrow barbs reach half an arrow length past it.
	if x0, want := b.P0[0]*testNmPerLongitude, -2*r-arrowLength/2; math.Abs(x0-want) > 0.05 {
		t.Errorf("racetrack extends to %v, want %v", x0, want)
	}
	if y1 := b.P1[1] * testNmPerLongitude; math.Abs(y1-(4+r)) > 0.05 {
		t.Errorf("outbound turn extends to %v, want %v", y1, 4+r)
	}
	expectNear(t, "inbound to N", w.fixes[2].inbound, [2]float32{0, -1}, 0.01)
	if w.pen.profile.altitude != 3000 {
		t.Errorf("exit altitude not applied: %v", w.pen.profile.altitude)
	}

	// The same arrival is within the no-PT semicircle: not flown.
	w = testWalker(RouteDrawContext{InitialAltitude: 5000})
	w.walk([]av.Waypoint{fixAt("P", 0, 3), racetrack(true), fixAt("N", 0, -10)})
	labelAt(t, w, "nopt180")
	if w.pen.profile.altitude != 5000 {
		t.Errorf("procedure turn flown from the no-PT side: altitude %v", w.pen.profile.altitude)
	}

	// Arriving from the south is a teardrop or parallel entry: flown.
	w = testWalker(RouteDrawContext{InitialAltitude: 5000})
	w.walk([]av.Waypoint{fixAt("P", 1, -3), racetrack(true), fixAt("N", 0, -10)})
	if w.pen.profile.altitude != 3000 {
		t.Errorf("procedure turn not flown: altitude %v", w.pen.profile.altitude)
	}
	expectNear(t, "inbound to N", w.fixes[2].inbound, [2]float32{0, -1}, 0.01)
}

func TestRouteProfile(t *testing.T) {
	p := routeProfile{altitude: 100, known: true, target: 3000, fieldElevation: 100}
	p.Advance(100)
	if p.altitude != 3000 {
		t.Errorf("climb overshot or fell short: %v", p.altitude)
	}
	p.Advance(1)
	if p.altitude != 3000 {
		t.Errorf("climbed past the target: %v", p.altitude)
	}

	p = routeProfile{altitude: 100, known: true, target: 3000, fieldElevation: 100}
	if d, ok := p.DistanceToAltitude(2000, true); !ok || math.Abs(d-(1500/700.0+400/650.0)) > 0.01 {
		t.Errorf("distance to 2000: %v %v", d, ok)
	}
	if _, ok := p.DistanceToAltitude(4000, true); ok {
		t.Errorf("4000 is above the target but was placed")
	}

	var wp av.Waypoint
	wp.SetAltitudeRestriction(av.AltitudeRestriction{NavigationRestriction: av.NavigationRestriction{Range: [2]float32{3000, av.MaxAltitude}}})
	p = routeProfile{}
	p.CrossFix(&wp)
	if !p.known || p.altitude != 3000 {
		t.Errorf("at or above restriction: %+v", p)
	}
	p = routeProfile{altitude: 5000, known: true}
	p.CrossFix(&wp)
	if p.altitude != 5000 {
		t.Errorf("at or above restriction lowered the estimate: %+v", p)
	}

	if r := (&routeProfile{}).TurnRadius(); math.Abs(r-0.955) > 0.01 {
		t.Errorf("turn radius at 180 knots: %v", r)
	}

	pt := &av.ProcedureTurn{}
	if l := procedureTurnLegLength(pt, av.ILSApproach, 180); l != 3 {
		t.Errorf("ILS leg length %v, want 3", l)
	}
	if l := procedureTurnLegLength(pt, av.RNAVApproach, 180); l != 4 {
		t.Errorf("RNAV leg length %v, want 4", l)
	}
	if l := procedureTurnLegLength(&av.ProcedureTurn{MinuteLimit: 2}, av.ILSApproach, 210); l != 7 {
		t.Errorf("2 minute leg length %v, want 7", l)
	}
}

func TestActionsWithoutHeading(t *testing.T) {
	// A handoff at a fix is nothing to fly: the route goes straight on.
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", 0, 0, av.WaypointActionGroup{Actions: av.WaypointActions{HumanHandoff: true}}),
		fixAt("B", 10, 0),
	})
	if w.fixes[0].actions != "ho" {
		t.Errorf("fix actions %q", w.fixes[0].actions)
	}
	if len(w.labels) != 0 {
		t.Errorf("unexpected labels %v", w.labels)
	}
	expectNear(t, "inbound to B", w.fixes[1].inbound, [2]float32{1, 0}, 0.01)
}

func TestTriggerLabelCarriesNextActions(t *testing.T) {
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{
		fixAt("A", 0, 0,
			av.WaypointActionGroup{Actions: heading(90), Until: av.WaypointActionTermination{Type: av.WaypointActionDistance, Distance: 5}},
			av.WaypointActionGroup{Actions: av.WaypointActions{Heading: av.WaypointHeadingAction{Heading: 180, Turn: av.TurnRight}, ClimbAltitude: 5000}}),
		fixAt("B", 20, 0),
	})
	expectNear(t, "@d5.0/r180/c5000", labelAt(t, w, "@d5.0/r180/c5000"), [2]float32{5, 0}, 0.05)
}

func TestLongArc(t *testing.T) {
	// A counterclockwise arc from A to B about a center below them sweeps
	// 224 degrees around the south; the drawing must go around it rather
	// than take the short way, so it reaches past B on the east.
	a := fixAt("A", 0, 0)
	a.InitExtra().Arc = &av.DMEArc{Center: nmPoint(0.5, -0.2), Radius: 0.539, Direction: av.DMEArcDirectionCounterClockwise}
	w := testWalker(RouteDrawContext{})
	w.walk([]av.Waypoint{a, fixAt("B", 1, 0)})
	if x1 := w.ld.Bounds().P1[0] * testNmPerLongitude; x1 < 1.02 {
		t.Errorf("arc doesn't go around: extends only to x=%v", x1)
	}
}
