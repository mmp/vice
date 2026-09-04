// radar/route.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package radar

import (
	"fmt"
	"iter"
	gomath "math"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/panes"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/util"
)

// RouteDrawContext is what drawing a route knows about the flights on it
// beyond its waypoints. All of it is route-level: a route is drawn once for
// every aircraft that flies it.
type RouteDrawContext struct {
	Departure       bool
	FieldElevation  int // ft MSL, for departures
	ClearedAltitude int // ft MSL, which departures climb to
	InitialAltitude int // ft MSL; 0 if the route doesn't fix it
	ApproachType    av.ApproachType
}

func DepartureRouteContext(icao string, er *av.ExitRoute) RouteDrawContext {
	rc := RouteDrawContext{
		Departure:       true,
		FieldElevation:  av.DB.Airports[icao].Elevation,
		ClearedAltitude: er.ClearedAltitude,
	}
	if rc.ClearedAltitude == 0 {
		rc.ClearedAltitude = er.AssignedAltitude
	}
	return rc
}

// ArrivalRouteContext takes the arrival's initial altitude if it gives a
// single one; each aircraft picks from a list at random.
func ArrivalRouteContext(arr av.Arrival) RouteDrawContext {
	var rc RouteDrawContext
	if len(arr.InitialAltitudes) == 1 {
		rc.InitialAltitude = arr.InitialAltitudes[0]
	}
	return rc
}

func OverflightRouteContext(of av.Overflight) RouteDrawContext {
	var rc RouteDrawContext
	if len(of.InitialAltitudes) == 1 {
		rc.InitialAltitude = of.InitialAltitudes[0]
	}
	return rc
}

func ApproachRouteContext(appr *av.Approach) RouteDrawContext {
	return RouteDrawContext{ApproachType: appr.Type}
}

// DepartureGroup identifies the departure routes that are selected and drawn
// together: those flying the same SID--or leaving via the same exit, for
// routes with no SID--that are open to the same aircraft. Routes open to
// different aircraft fly different paths, so they are never grouped.
type DepartureGroup struct {
	SID      string           // empty for a route with no SID
	Exit     av.ExitID        // the route's exit, when it has no SID
	Aircraft av.AircraftClass // zero if the route is open to every aircraft
}

// DepartureRoute is one of a scenario's departure routes together with the
// group it belongs to.
type DepartureRoute struct {
	Group  DepartureGroup
	Runway av.RunwayID
	Exit   av.ExitID
	Route  *av.ExitRoute
}

// ScenarioDepartureRoutes returns the routes departures fly from the
// airport's runways in the current scenario, in runway then exit order.
func ScenarioDepartureRoutes(ap *av.Airport, rates map[av.RunwayID]map[string]float32) iter.Seq[DepartureRoute] {
	return func(yield func(DepartureRoute) bool) {
		if ap == nil {
			return
		}
		for _, rwy := range util.SortedMapKeys(rates) {
			for exit, routes := range util.SortedMap(ap.DepartureRoutes[rwy]) {
				for _, route := range routes {
					group := DepartureGroup{SID: route.SID, Aircraft: route.Aircraft}
					if route.SID == "" {
						group.Exit = exit
					}
					if !yield(DepartureRoute{Group: group, Runway: rwy, Exit: exit, Route: route}) {
						return
					}
				}
			}
		}
	}
}

// DrawnRoutes records what a frame's route drawing has drawn so far, so
// that routes sharing fixes and legs draw each fix once and stack their
// labels rather than overprinting them.
type DrawnRoutes struct {
	fixes  map[string]any
	blocks []textBlock
}

// textBlock is the text placed for one point of a route.
type textBlock struct {
	at    [2]float32 // window coordinates of the point
	next  [2]float32 // where the block's next line goes
	lines []string
}

func NewDrawnRoutes() *DrawnRoutes {
	return &DrawnRoutes{fixes: make(map[string]any)}
}

// HasFix reports whether the fix has been drawn this frame.
func (d *DrawnRoutes) HasFix(fix string) bool {
	_, ok := d.fixes[fix]
	return ok
}

// ClaimFix records that the fix is being drawn and reports whether it had
// not been already.
func (d *DrawnRoutes) ClaimFix(fix string) bool {
	if d.HasFix(fix) {
		return false
	}
	d.fixes[fix] = nil
	return true
}

// Label draws text for the point at, in window coordinates, pushed off it
// in the direction dir. Text near a block placed earlier this frame joins
// it as a new line, unless the block already has that line.
func (d *DrawnRoutes) Label(td *renderer.TextDrawBuilder, style renderer.TextStyle, at, dir [2]float32, text string) {
	crowding := 3 * float32(style.Font.Size)
	for i := range d.blocks {
		if b := &d.blocks[i]; math.Distance2f(b.at, at) < crowding {
			if !slices.Contains(b.lines, text) {
				b.next = td.AddText(text+"\n", b.next, style)
				b.lines = append(b.lines, text)
			}
			return
		}
	}

	ext := style.Font.LayoutBounds(text, 0)
	p := math.Add2f(at, math.Scale2f(dir, labelOffset))
	if dir[0] < 0 {
		p[0] -= ext.Width()
	}
	p[1] += ext.Height() / 2
	d.blocks = append(d.blocks, textBlock{at: at, next: td.AddText(text+"\n", p, style), lines: []string{text}})
}

// DrawWaypoints draws a route as aircraft fly it: its legs, the turns
// between them, where each action group's trigger is expected to be met,
// and the radials, DME circles, and courses those depend on, followed by
// the fixes' names and restrictions. Lines go to ld in lat-long
// coordinates; text and the fix markers go to td, pd, and ldr in window
// coordinates.
func DrawWaypoints(ctx *panes.Context, waypoints []av.Waypoint, rc RouteDrawContext, drawn *DrawnRoutes,
	transforms ScopeTransformations, td *renderer.TextDrawBuilder, style renderer.TextStyle,
	ld *renderer.ColoredLinesDrawBuilder, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder, color renderer.RGB) {
	w := newRouteWalker(ctx.NmPerLongitude, ctx.MagneticVariation, rc, ld, color, drawn)
	w.walk(waypoints)
	w.drawLabels(waypoints, transforms, td, style, pd, ldr)
}

const (
	stubLength             = 3   // nm of an open-ended heading's arrow
	indeterminateLegLength = 5   // nm of a leg whose trigger can't be placed
	radialMarkLength       = 10  // nm of a radial drawn before a crossing or join
	tickHalfLength         = 0.3 // nm each side of a trigger point
	arrowLength            = 0.5 // nm
	labelOffset            = 8   // pixels from the line to its label

	// A leg whose heading would meet its course at less than
	// minInterceptAngle degrees while more than shallowInterceptOffset nm
	// from it misses it and turns to a 45 degree intercept, as nav's
	// turnToInterceptIfHeadingMisses does. Within onCourseOffset nm of the
	// course, the aircraft is on it: nav turns onto a course it is that
	// close to right away, while the crossing of the line the walker
	// measures can be far off or never for a nearly parallel leg.
	minInterceptAngle      = 10   // degrees
	shallowInterceptOffset = 2    // nm
	onCourseOffset         = 0.08 // nm
)

// routePen is the walker's state along the route, in nm coordinates.
type routePen struct {
	p, dir     [2]float32 // dir is a unit vector
	groupStart [2]float32 // where the current action group took effect
	atFix      bool       // at a fix, having drawn nothing past it
	profile    routeProfile
}

// routeLabel is text drawn at a point of the route, pushed off the line
// in the offset direction.
type routeLabel struct {
	p, offset [2]float32
	text      string
}

// fixAnchor records where a fix was drawn and the route's directions in
// and out of it, for placing its label.
type fixAnchor struct {
	index             int
	p                 [2]float32
	inbound, outbound [2]float32 // unit vectors; zero if none
	actions           string     // the actions and triggers that apply at the fix, in route syntax
}

type markKind int

const (
	markNone markKind = iota
	markRadial
	markDMEArc
	markCourse
)

// triggerMark is the geometry a trigger depends on, drawn near its point.
type triggerMark struct {
	kind   markKind
	center [2]float32 // the radial's fix or the DME circle's center
	dir    [2]float32 // the radial's or the course's direction
	radius float32
}

// routeWalker draws a route by walking it as nav flies it.
type routeWalker struct {
	nmPerLongitude, magneticVariation float32
	rc                                RouteDrawContext
	ld                                *renderer.ColoredLinesDrawBuilder
	color                             renderer.RGB
	drawn                             *DrawnRoutes

	pen          routePen
	nextFix      [2]float32
	haveNextFix  bool
	disconnected bool // on an open-ended heading, off the route until the next fix
	airborne     bool // departures: past the runway, so the profile climbs
	dashed       bool // lines are dashed: a procedure turn this route doesn't fly

	fixes  []fixAnchor
	labels []routeLabel
}

func newRouteWalker(nmPerLongitude, magneticVariation float32, rc RouteDrawContext,
	ld *renderer.ColoredLinesDrawBuilder, color renderer.RGB, drawn *DrawnRoutes) *routeWalker {
	w := &routeWalker{
		nmPerLongitude:    nmPerLongitude,
		magneticVariation: magneticVariation,
		rc:                rc,
		ld:                ld,
		color:             color,
		drawn:             drawn,
		airborne:          !rc.Departure,
	}
	switch {
	case rc.Departure:
		w.pen.profile = routeProfile{
			altitude:       float32(rc.FieldElevation),
			known:          true,
			target:         float32(rc.ClearedAltitude),
			fieldElevation: float32(rc.FieldElevation),
		}
	case rc.InitialAltitude != 0:
		w.pen.profile = routeProfile{altitude: float32(rc.InitialAltitude), known: true}
	}
	return w
}

func (w *routeWalker) nm(p math.Point2LL) [2]float32 { return math.LL2NM(p, w.nmPerLongitude) }
func (w *routeWalker) ll(p [2]float32) math.Point2LL { return math.NM2LL(p, w.nmPerLongitude) }

// headingVector returns the direction of a magnetic heading.
func (w *routeWalker) headingVector(h int16) [2]float32 {
	return math.HeadingVector(math.MagneticToTrue(math.MagneticHeading(h), w.magneticVariation))
}

// radialVector returns the direction of a radial referenced to the given
// variation: its navaid's station declination rather than the area's.
func (w *routeWalker) radialVector(radial int16, variation float32) [2]float32 {
	return math.HeadingVector(math.MagneticToTrue(math.MagneticHeading(radial), variation))
}

func rightNormal(d [2]float32) [2]float32 { return [2]float32{d[1], -d[0]} }
func leftNormal(d [2]float32) [2]float32  { return [2]float32{-d[1], d[0]} }

// rotate turns a direction clockwise by the given degrees.
func rotate(dir [2]float32, degrees float32) [2]float32 {
	return math.SinCos(math.Atan2(dir[0], dir[1]) + math.Radians(degrees))
}

// signedTurn returns the degrees of the shorter turn from one direction to
// another, positive for a right turn.
func signedTurn(from, to [2]float32) float32 {
	angle := math.Degrees(math.Atan2(to[0], to[1]) - math.Atan2(from[0], from[1]))
	for angle > 180 {
		angle -= 360
	}
	for angle <= -180 {
		angle += 360
	}
	return angle
}

func direction(from, to [2]float32) [2]float32 {
	if v := math.Sub2f(to, from); math.Length2f(v) > 0 {
		return math.Normalize2f(v)
	}
	return [2]float32{0, 1}
}

func (w *routeWalker) walk(wps []av.Waypoint) {
	if len(wps) == 0 {
		return
	}
	w.pen.p = w.nm(wps[0].Location)
	w.pen.dir = [2]float32{0, 1}
	if h, ok := wps[0].HeadingAction(); ok && h.Heading != 0 && h.Fix == "" {
		w.pen.dir = w.headingVector(h.Heading)
	} else if len(wps) > 1 {
		w.pen.dir = direction(w.pen.p, w.nm(wps[1].Location))
	}
	w.pen.atFix = true

	for i := range wps {
		wp := &wps[i]
		if i > 0 {
			w.arrive(wps, i)
		}
		if w.rc.Departure && i == 1 {
			// Past the point 3/4 of the way down the runway, which stands
			// in for the takeoff roll.
			w.airborne = true
		}
		w.pen.profile.CrossFix(wp)

		fa := fixAnchor{index: i, p: w.pen.p}
		if i > 0 {
			fa.inbound = w.pen.dir
		}
		w.fixes = append(w.fixes, fa)

		w.haveNextFix = i+1 < len(wps)
		if w.haveNextFix {
			w.nextFix = w.nm(wps[i+1].Location)
			if wp.ProcedureTurn() != nil {
				w.procedureTurn(wps, i)
			}
		}
		groups := wp.ActionGroups()
		if len(groups) > 0 {
			w.fixes[len(w.fixes)-1].actions = strings.TrimPrefix(groups[0].Actions.Encoded(), "/")
		}
		for j, g := range groups {
			var next *av.WaypointActionGroup
			if j+1 < len(groups) {
				next = &groups[j+1]
			}
			if !w.flyGroup(g, next) {
				break
			}
		}
	}
}

// setOutbound records the direction the route leaves the last fix in, the
// first time it's known.
func (w *routeWalker) setOutbound(dir [2]float32) {
	if fa := &w.fixes[len(w.fixes)-1]; fa.outbound == [2]float32{} {
		fa.outbound = dir
	}
}

// arrive draws the route from the pen to wps[i].
func (w *routeWalker) arrive(wps []av.Waypoint, i int) {
	prev, wp := &wps[i-1], &wps[i]
	q := w.nm(wp.Location)
	switch {
	case w.disconnected:
		// The aircraft is on a heading until a controller sends it on;
		// show where the route resumes without implying it flies there.
		w.dashedLine(w.pen.p, q)
		w.pen.dir = direction(w.pen.p, q)
		w.pen.p = q
		w.disconnected = false
	case prev.Arc() != nil:
		w.arc(prev.Arc(), q)
	default:
		w.setOutbound(direction(w.pen.p, q))
		// Turns at plain fixes are fly-by and cut the corner, so they're
		// left as corners here; after a leg that ended elsewhere, or a
		// fly-over fix, the aircraft turns from where it is.
		if !w.pen.atFix || prev.FlyOver() {
			w.turnToward(q, wp.Turn())
		}
		w.straightTo(q, wp.ProcedureTurn() == nil)
	}
	w.pen.atFix = true
}

// flyGroup draws an action group's leg, labeling its trigger point with
// the trigger and the actions of the group that follows. It returns false
// if the group is open-ended, since the ones after it are never reached.
func (w *routeWalker) flyGroup(g av.WaypointActionGroup, next *av.WaypointActionGroup) bool {
	w.pen.groupStart = w.pen.p
	w.pen.profile.Apply(g.Actions)
	if g.Until.Type == av.WaypointActionNoTermination && !g.Actions.Heading.IsSet() {
		// The group only carries actions for the sim with nothing to fly:
		// nav fires them and goes on along the route.
		return true
	}
	dir := w.groupDirection(g.Actions.Heading)
	w.setOutbound(dir)

	until := g.Until
	if until.Type == av.WaypointActionNoTermination || (until.Type == av.WaypointActionCourse && !w.haveNextFix) {
		w.turnTo(dir, g.Actions.Heading.Turn)
		w.stub()
		return false
	}

	// A trigger that is already met when the group takes effect--a
	// departure that has climbed through its altitude, say--ends it before
	// the turn onto its heading.
	t, mark, ok := w.triggerDistance(until, dir)
	if !ok || t > 0 {
		w.turnTo(dir, g.Actions.Heading.Turn)
		t, mark, ok = w.triggerDistance(until, dir)
	}
	if mark.kind == markCourse && w.missesCourse(ok, dir, mark.dir) {
		// The heading would miss the course: as nav does, turn to meet it
		// at 45 degrees from this side of it.
		right := math.Dot(math.Sub2f(w.pen.p, w.nextFix), rightNormal(mark.dir)) > 0
		dir = rotate(mark.dir, util.Select(right, float32(-45), 45))
		w.turnTo(dir, av.TurnClosest)
		t, mark, ok = w.triggerDistance(until, dir)
		if !ok {
			// The intercept turn itself crossed the course, so the
			// aircraft rolls out on it about here.
			t, ok = 0, true
		}
	}
	text := until.Encoded()
	if !ok {
		t, text = indeterminateLegLength, text+"?"
	}
	if next != nil {
		text += next.Actions.Encoded()
	}
	text = strings.TrimPrefix(text, "/")

	crossing := math.Add2f(w.pen.p, math.Scale2f(dir, t))
	end := crossing
	if ok && mark.kind == markCourse {
		// nav starts the turn onto the course early enough to roll out on
		// it, so the leg ends a turn's lead before the crossing and the
		// turn is tangent to the course.
		angle := min(math.Abs(signedTurn(dir, mark.dir)), 120)
		lead := min(t, w.pen.profile.TurnRadius()*math.Tan(math.Radians(angle/2)))
		end = math.Add2f(end, math.Scale2f(dir, -lead))
	}

	switch {
	case end != w.pen.p:
		w.straightTo(end, true)
		w.pen.dir = dir
		w.tick(end, dir)
		w.labels = append(w.labels, routeLabel{p: end, offset: w.labelSide(dir, next), text: text})
		if ok {
			w.drawMark(mark, crossing)
		}
	case w.pen.atFix:
		// Met at the fix, so it goes with the fix's label.
		fa := &w.fixes[len(w.fixes)-1]
		fa.actions = strings.TrimPrefix(fa.actions+"/"+text, "/")
	case len(w.labels) > 0 && w.labels[len(w.labels)-1].p == w.pen.p:
		// Met where the previous trigger was.
		w.labels[len(w.labels)-1].text += "/" + text
	default:
		w.labels = append(w.labels, routeLabel{p: end, offset: w.labelSide(dir, next), text: text})
	}
	if ok && mark.kind == markCourse {
		w.turnTo(mark.dir, av.TurnClosest)
	}
	return true
}

// labelSide returns which side of a leg its trigger's label goes on: the
// left, unless the group that follows turns left.
func (w *routeWalker) labelSide(dir [2]float32, next *av.WaypointActionGroup) [2]float32 {
	if next != nil {
		if h := next.Actions.Heading; h.IsSet() && !h.PresentHeading && h.Fix == "" &&
			signedTurn(dir, w.headingVector(h.Heading)) < 0 {
			return rightNormal(dir)
		}
	}
	return leftNormal(dir)
}

// groupDirection returns the direction an action group's heading has the
// aircraft fly, drawing the path to a radial it joins.
func (w *routeWalker) groupDirection(h av.WaypointHeadingAction) [2]float32 {
	switch {
	case !h.IsSet() || h.PresentHeading:
		return w.pen.dir
	case h.Fix != "":
		return w.joinRadial(h)
	default:
		return w.headingVector(h.Heading)
	}
}

// joinRadial draws the path to a fix's radial as nav flies it--via the fix
// from behind it, otherwise converging at up to 45 degrees--and returns
// the radial's direction with the pen on it.
func (w *routeWalker) joinRadial(h av.WaypointHeadingAction) [2]float32 {
	f := w.nm(h.FixLocation)
	dir := w.radialVector(h.Heading, h.FixVariation)
	w.turnToward(w.radialJoinPoint(f, dir), h.Turn)
	// nav resteers as it flies, so a turn that carries the aircraft past a
	// join point close by--the fix itself, when the radial starts near the
	// departure end of the runway--is followed by the next point along the
	// radial rather than a turn back to it.
	join := w.radialJoinPoint(f, dir)
	// Roll out on the radial: the leg ends a turn's lead short of the join
	// and the turn onto the radial is tangent to it.
	toJoin := direction(w.pen.p, join)
	angle := min(math.Abs(signedTurn(toJoin, dir)), 120)
	lead := min(math.Distance2f(w.pen.p, join), w.pen.profile.TurnRadius()*math.Tan(math.Radians(angle/2)))
	w.straightTo(math.Sub2f(join, math.Scale2f(toJoin, lead)), false)
	w.pen.dir = toJoin
	w.turnTo(dir, av.TurnClosest)
	w.drawRadial(f, dir, w.pen.p, 0)
	return dir
}

// radialJoinPoint returns the point the aircraft steers toward to join the
// given radial of fix and follow it outbound, as nav's radialSteeringPoint
// does: a point on the radial ahead of the pen's projection onto it, at
// least 2nm ahead and farther when the pen is farther off, or the fix
// itself from behind it.
func (w *routeWalker) radialJoinPoint(f, dir [2]float32) [2]float32 {
	v := math.Sub2f(w.pen.p, f)
	along := math.Dot(v, dir)
	if along < 0 {
		return f
	}
	across := math.Abs(math.Dot(v, rightNormal(dir)))
	return math.Add2f(f, math.Scale2f(dir, along+max(2, across)))
}

// triggerDistance returns how far along dir from the pen a trigger is met
// and the geometry it depends on; ok is false if that can't be determined.
func (w *routeWalker) triggerDistance(until av.WaypointActionTermination, dir [2]float32) (float32, triggerMark, bool) {
	switch until.Type {
	case av.WaypointActionAltitude:
		d, ok := w.pen.profile.DistanceToAltitude(float32(until.Altitude), until.AtOrAbove)
		return d, triggerMark{}, ok

	case av.WaypointActionDistance:
		// The sim measures a straight line from where the group took
		// effect, so after a turn this is a chord rather than distance flown.
		_, t1, ok := math.RayCircleIntersect(w.pen.p, dir, w.pen.groupStart, until.Distance)
		return max(t1, 0), triggerMark{}, ok

	case av.WaypointActionDME:
		return w.dmeTriggerDistance(until, dir)

	case av.WaypointActionRadial:
		// The radial extends from the fix in one direction only; crossing
		// its reciprocal doesn't count, as in nav's reachesRadial.
		f := w.nm(until.RadialFixLocation)
		rdir := w.radialVector(until.Radial, until.RadialFixVariation)
		_, t, _, ok := math.RaySegmentIntersect(w.pen.p, dir, f, math.Add2f(f, math.Scale2f(rdir, 500)))
		return t, triggerMark{kind: markRadial, center: f, dir: rdir}, ok

	case av.WaypointActionCourse:
		// The course leads to the next fix, so crossing its line beyond
		// the fix doesn't count, as in nav.
		cdir := w.headingVector(until.Course)
		if until.CourseFix != "" {
			// The radial names the line only, since a leg may fly it either
			// inbound or outbound; the fix gives the direction along it.
			cdir = w.radialVector(until.Course, until.CourseFixVariation)
			if math.Dot(math.Sub2f(w.nextFix, w.pen.p), cdir) < 0 {
				cdir = math.Scale2f(cdir, -1)
			}
		}
		mark := triggerMark{kind: markCourse, dir: cdir}
		// Already on the course--a SID whose charted heading is its runway's
		// and whose course is the radial the runway lies along, say--so the
		// aircraft turns onto it here rather than intercepting it.
		if math.Abs(math.SignedPointLineDistance(w.pen.p, w.nextFix, math.Add2f(w.nextFix, cdir))) < onCourseOffset {
			return 0, mark, true
		}
		_, t, _, ok := math.RaySegmentIntersect(w.pen.p, dir,
			math.Sub2f(w.nextFix, math.Scale2f(cdir, 500)), w.nextFix)
		return t, mark, ok

	default:
		return 0, triggerMark{}, false
	}
}

func (w *routeWalker) dmeTriggerDistance(until av.WaypointActionTermination, dir [2]float32) (float32, triggerMark, bool) {
	// Slant range is ignored: the altitude estimate's error dwarfs it.
	c := w.nm(until.DMEFixLocation)
	mark := triggerMark{kind: markDMEArc, center: c, radius: until.DMEDistance}
	inside := math.Distance2f(w.pen.p, c) < until.DMEDistance
	t0, t1, ok := math.RayCircleIntersect(w.pen.p, dir, c, until.DMEDistance)
	if until.AtOrAbove {
		if !inside {
			return 0, mark, true
		}
		return t1, mark, ok
	}
	if inside {
		return 0, mark, true
	}
	if !ok || t1 < 0 {
		return 0, mark, false
	}
	return max(t0, 0), mark, true
}

// missesCourse reports whether a course trigger's leg along dir misses its
// course: it never crosses it short of the next fix (ok is false), or it
// converges at a glancing angle from well off it.
func (w *routeWalker) missesCourse(ok bool, dir, cdir [2]float32) bool {
	if !ok {
		return true
	}
	off := math.Abs(math.SignedPointLineDistance(w.pen.p, w.nextFix, math.Add2f(w.nextFix, cdir)))
	return math.Abs(signedTurn(dir, cdir)) < minInterceptAngle && off > shallowInterceptOffset
}

// lineIntersection returns where the ray from the pen along dir meets the
// line through p with the given direction, if it does.
func (w *routeWalker) lineIntersection(dir, p, lineDir [2]float32) ([2]float32, float32, bool) {
	pi, t, _, ok := math.RaySegmentIntersect(w.pen.p, dir,
		math.Sub2f(p, math.Scale2f(lineDir, 500)), math.Add2f(p, math.Scale2f(lineDir, 500)))
	return pi, t, ok
}

///////////////////////////////////////////////////////////////////////////
// Procedure turns

// procedureTurn draws the procedure turn at wps[i] as the maneuvers nav
// flies for an aircraft arriving along the route, leaving the pen at the
// fix heading inbound. A turn the route doesn't fly is drawn dashed. Like
// the fix's label, the turn is drawn for the first route through the fix
// only; the entries of others arriving from other directions would just
// pile up on it.
func (w *routeWalker) procedureTurn(wps []av.Waypoint, i int) {
	pt := wps[i].ProcedureTurn()
	f := w.pen.p
	inbound := direction(f, w.nextFix)
	outbound := math.Scale2f(inbound, -1)
	arrival := w.pen.dir
	turn := util.Select(pt.RightTurns, av.TurnRight, av.TurnLeft)
	leg := procedureTurnLegLength(pt, w.rc.ApproachType, w.pen.profile.Groundspeed())

	flown := !(i > 0 && wps[i-1].NoPT()) &&
		!(pt.Entry180NoPT && math.HeadingDifference(math.VectorHeading(arrival), math.VectorHeading(inbound)) < 90)
	if w.drawn.HasFix(wps[i].Fix) {
		w.pen.dir = inbound
		if flown && pt.ExitAltitude != 0 {
			w.pen.profile.altitude, w.pen.profile.known = float32(pt.ExitAltitude), true
		}
		return
	}
	if pt.Entry180NoPT {
		w.noPTSector(f, inbound)
	}

	saved := w.pen
	w.dashed = !flown
	switch pt.Type {
	case av.PTRacetrack:
		if flown {
			w.racetrackEntry(f, inbound, arrival, turn, leg)
		}
		w.turnTo(outbound, turn)
		w.straightTo(math.Add2f(w.pen.p, math.Scale2f(outbound, leg)), true)
		w.turnTo(inbound, turn)
		w.straightTo(f, true)

	case av.PTStandard45:
		away := rotate(outbound, util.Select(pt.RightTurns, float32(-45), 45))
		w.turnTo(outbound, av.TurnClosest)
		w.straightTo(math.Add2f(w.pen.p, math.Scale2f(outbound, 1.2*leg)), true)
		w.turnTo(away, av.TurnClosest)
		w.straightTo(math.Add2f(w.pen.p, math.Scale2f(away, leg)), true)
		w.turnTo(math.Scale2f(away, -1), turn)
		w.interceptInbound(f, inbound)
	}
	w.dashed = false

	if !flown {
		w.pen = saved
		return
	}
	if pt.ExitAltitude != 0 {
		w.pen.profile.altitude, w.pen.profile.known = float32(pt.ExitAltitude), true
	}
}

// racetrackEntry draws the entry to a racetrack procedure turn for an
// aircraft arriving at the fix in the given direction, as
// racetrackEntryManeuvers in nav does: nothing for a direct entry, else a
// leg away from the fix, a turn to intercept the inbound course, and the
// return to the fix.
func (w *routeWalker) racetrackEntry(f, inbound, arrival [2]float32, turn av.TurnDirection, leg float32) {
	hold := av.Hold{
		InboundCourse: math.TrueToMagnetic(math.VectorHeading(inbound), w.magneticVariation),
		TurnDirection: turn,
	}
	right := turn == av.TurnRight
	switch hold.Entry(math.TrueToMagnetic(math.VectorHeading(arrival), w.magneticVariation)) {
	case av.HoldEntryParallel:
		outbound := math.Scale2f(inbound, -1)
		w.turnTo(outbound, av.TurnClosest)
		w.straightTo(math.Add2f(w.pen.p, math.Scale2f(outbound, leg)), true)
		w.turnTo(rotate(inbound, util.Select(right, float32(-40), 40)), util.Select(right, av.TurnLeft, av.TurnRight))
		w.interceptInbound(f, inbound)

	case av.HoldEntryTeardrop:
		teardrop := rotate(inbound, util.Select(right, float32(150), -150))
		w.turnTo(teardrop, av.TurnClosest)
		w.straightTo(math.Add2f(w.pen.p, math.Scale2f(teardrop, leg)), true)
		w.turnTo(math.Scale2f(teardrop, -1), turn)
		w.interceptInbound(f, inbound)
	}
}

// interceptInbound draws the leg from the pen to the inbound course
// through the fix and then along it to the fix.
func (w *routeWalker) interceptInbound(f, inbound [2]float32) {
	if p, _, ok := w.lineIntersection(w.pen.dir, f, inbound); ok {
		w.straightTo(p, false)
	}
	w.straightTo(f, true)
}

// noPTSector draws the boundary of the semicircle of arrival directions
// from which the procedure turn is not flown: the line through the fix
// perpendicular to the inbound course, labeled on the no-PT side.
func (w *routeWalker) noPTSector(f, inbound [2]float32) {
	n := math.Scale2f(rightNormal(inbound), 3)
	w.dashedLine(math.Sub2f(f, n), math.Add2f(f, n))
	outbound := math.Scale2f(inbound, -1)
	w.labels = append(w.labels, routeLabel{p: math.Add2f(f, math.Scale2f(outbound, 0.5)), offset: outbound, text: "nopt180"})
}

///////////////////////////////////////////////////////////////////////////
// Drawing primitives

// advance moves the profile along nm of flight, once the aircraft is off
// the runway.
func (w *routeWalker) advance(nm float32) {
	if w.airborne {
		w.pen.profile.Advance(nm)
	}
}

func (w *routeWalker) line(p0, p1 [2]float32) {
	if w.dashed {
		w.dashedLine(p0, p1)
	} else {
		w.ld.AddLine(w.ll(p0), w.ll(p1), w.color)
	}
}

func (w *routeWalker) dashedLine(p0, p1 [2]float32) {
	const dash, gap = 0.4, 0.25
	d := math.Distance2f(p0, p1)
	if d == 0 {
		return
	}
	v := math.Scale2f(math.Sub2f(p1, p0), 1/d)
	for t := float32(0); t < d; t += dash + gap {
		w.ld.AddLine(w.ll(math.Add2f(p0, math.Scale2f(v, t))), w.ll(math.Add2f(p0, math.Scale2f(v, min(t+dash, d)))), w.color)
	}
}

// arrow draws an arrowhead at p pointing along dir.
func (w *routeWalker) arrow(p, dir [2]float32) {
	a := math.Atan2(dir[0], dir[1])
	for _, da := range []float32{math.Radians(float32(210)), -math.Radians(float32(210))} {
		w.ld.AddLine(w.ll(p), w.ll(math.Add2f(p, math.Scale2f(math.SinCos(a+da), arrowLength))), w.color)
	}
}

// tick draws a short line across the route at a trigger point.
func (w *routeWalker) tick(p, dir [2]float32) {
	n := math.Scale2f(rightNormal(dir), tickHalfLength)
	w.ld.AddLine(w.ll(math.Sub2f(p, n)), w.ll(math.Add2f(p, n)), w.color)
}

// straightTo draws a leg from the pen to q, with an arrow in the middle of
// one long enough to carry it.
func (w *routeWalker) straightTo(q [2]float32, withArrow bool) {
	d := math.Distance2f(w.pen.p, q)
	if d == 0 {
		return
	}
	w.line(w.pen.p, q)
	w.pen.dir = math.Scale2f(math.Sub2f(q, w.pen.p), 1/d)
	if withArrow && d > 2 {
		w.arrow(math.Mid2f(w.pen.p, q), w.pen.dir)
	}
	w.advance(d)
	w.pen.p = q
	w.pen.atFix = false
}

// turnTo draws a standard-rate turn from the pen's direction to dir,
// turning the given way or the shorter one for TurnClosest, and leaves the
// pen at its end.
func (w *routeWalker) turnTo(dir [2]float32, turn av.TurnDirection) {
	h0 := math.Atan2(w.pen.dir[0], w.pen.dir[1])
	angle := signedTurn(w.pen.dir, dir)
	switch turn {
	case av.TurnLeft:
		if angle > 0 {
			angle -= 360
		}
	case av.TurnRight:
		if angle < 0 {
			angle += 360
		}
	}
	if math.Abs(angle) < 3 {
		w.pen.dir = dir
		return
	}

	r := w.pen.profile.TurnRadius()
	right := angle > 0
	n := util.Select(right, rightNormal(w.pen.dir), leftNormal(w.pen.dir))
	center := math.Add2f(w.pen.p, math.Scale2f(n, r))
	// The point on the turn's circle where the heading is h.
	pos := func(h float32) [2]float32 {
		sc := math.SinCos(h)
		if right {
			return math.Add2f(center, math.Scale2f([2]float32{-sc[1], sc[0]}, r))
		}
		return math.Add2f(center, math.Scale2f([2]float32{sc[1], -sc[0]}, r))
	}

	steps := max(1, int(math.Abs(angle)/5+0.5))
	prev := w.pen.p
	for i := 1; i <= steps; i++ {
		p := pos(h0 + math.Radians(angle)*float32(i)/float32(steps))
		w.line(prev, p)
		prev = p
	}
	w.advance(r * math.Abs(math.Radians(angle)))
	w.pen.p = prev
	w.pen.dir = dir
	w.pen.atFix = false
}

func (w *routeWalker) turnToward(q [2]float32, turn av.TurnDirection) {
	if q != w.pen.p {
		w.turnTo(direction(w.pen.p, q), turn)
	}
}

// stub draws the arrow of an open-ended heading and leaves the pen at its
// end, off the route.
func (w *routeWalker) stub() {
	end := math.Add2f(w.pen.p, math.Scale2f(w.pen.dir, stubLength))
	w.line(w.pen.p, end)
	w.arrow(end, w.pen.dir)
	w.pen.p = end
	w.pen.atFix = false
	w.disconnected = true
}

// arc draws a DME arc from the pen to q. The radius is interpolated from
// the pen's distance to the center to q's, since the nm coordinates'
// fixed nm per longitude leaves the two slightly different.
func (w *routeWalker) arc(arc *av.DMEArc, q [2]float32) {
	pc := w.nm(arc.Center)
	r0, r1 := math.Distance2f(w.pen.p, pc), math.Distance2f(q, pc)
	a0 := float32(math.VectorHeading(math.Sub2f(w.pen.p, pc)))
	a1 := float32(math.VectorHeading(math.Sub2f(q, pc)))
	step := util.Select(arc.Direction.IsClockwise(), float32(1), -1)
	// The tangent at a point on the arc at angle a from the center.
	tangent := func(a float32) [2]float32 {
		return math.HeadingVector(math.TrueHeading(math.NormalizeHeading(a + 90*step)))
	}

	// Draw a segment every degree, around the arc in its direction; it
	// may sweep more than halfway around its circle.
	n := int(math.NormalizeHeading((a1 - a0) * step))
	a := a0
	prev := w.pen.p
	for i := 1; i < n-1; i++ {
		a = math.NormalizeHeading(a + step)
		r := math.Lerp(float32(i)/float32(n), r0, r1)
		p := math.Add2f(pc, math.Scale2f(math.SinCos(math.Radians(a)), r))
		w.line(prev, p)
		if i == n/2 {
			w.arrow(p, tangent(a))
		}
		prev = p
	}
	w.line(prev, q)
	w.advance(r0 * math.Radians(float32(n)))
	w.pen.p = q
	w.pen.dir = tangent(a1)
	w.pen.atFix = false
}

// drawRadial draws the part of a fix's radial leading to p dashed, from up
// to radialMarkLength before p to past nm beyond it.
func (w *routeWalker) drawRadial(fix, dir, p [2]float32, past float32) {
	along := math.Dot(math.Sub2f(p, fix), dir)
	start := math.Add2f(fix, math.Scale2f(dir, max(0, along-radialMarkLength)))
	w.dashedLine(start, math.Add2f(fix, math.Scale2f(dir, along+past)))
}

// drawMark draws the geometry a trigger met at p depends on.
func (w *routeWalker) drawMark(m triggerMark, p [2]float32) {
	switch m.kind {
	case markRadial:
		w.drawRadial(m.center, m.dir, p, 2)

	case markDMEArc:
		// A dashed arc of the DME circle around the crossing.
		v := math.Sub2f(p, m.center)
		a := math.Atan2(v[0], v[1])
		for deg := -20; deg < 20; deg += 4 {
			a0, a1 := a+math.Radians(float32(deg)), a+math.Radians(float32(deg+2))
			w.ld.AddLine(w.ll(math.Add2f(m.center, math.Scale2f(math.SinCos(a0), m.radius))),
				w.ll(math.Add2f(m.center, math.Scale2f(math.SinCos(a1), m.radius))), w.color)
		}

	case markCourse:
		w.dashedLine(math.Sub2f(p, math.Scale2f(m.dir, 3)), p)
	}
}

///////////////////////////////////////////////////////////////////////////
// Labels

// drawLabels draws the fixes' markers, names, and restrictions and the
// route's labels, in window coordinates.
func (w *routeWalker) drawLabels(wps []av.Waypoint, transforms ScopeTransformations, td *renderer.TextDrawBuilder,
	style renderer.TextStyle, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {
	for _, fa := range w.fixes {
		wp := &wps[fa.index]
		if w.rc.Departure && fa.index < 2 {
			// The runway's threshold and mid point aren't fixes.
			continue
		}
		// If we're given the same fix more than once (as may happen with
		// T-shaped RNAV arrivals for example), only draw it once. We'll
		// assume/hope that we're not seeing it with different restrictions...
		if w.drawn.ClaimFix(wp.Fix) {
			w.drawFix(wp, fa, transforms, td, style, pd, ldr)
		}
	}

	window := func(p [2]float32) [2]float32 { return transforms.windowFromLatLong.TransformPoint(w.ll(p)) }
	for _, l := range w.labels {
		pw := window(l.p)
		w.drawn.Label(td, style, pw, direction(pw, window(math.Add2f(l.p, l.offset))), l.text)
	}
}

func (w *routeWalker) drawFix(wp *av.Waypoint, fa fixAnchor, transforms ScopeTransformations, td *renderer.TextDrawBuilder,
	style renderer.TextStyle, pd *renderer.ColoredTrianglesDrawBuilder, ldr *renderer.ColoredLinesDrawBuilder) {
	color := w.color

	// Draw a circle at the waypoint's location
	const pointRadius = 2.5
	const nSegments = 8
	pw := transforms.WindowFromLatLongP(wp.Location)
	pd.AddCircle(pw, pointRadius, nSegments, color)

	// If /radius has been specified, draw a corresponding circle
	if wp.Radius() > 0 {
		w.ld.AddLatLongCircle(wp.Location, w.nmPerLongitude, wp.Radius(), 32, color)
	}

	// For /shift, extend the line beyond the waypoint (just in case)
	// and draw perpendicular bars at the ends.
	if wp.Shift() > 0 && fa.inbound != [2]float32{} {
		v := math.Scale2f(fa.inbound, wp.Shift()/2)
		e0, e1 := math.Sub2f(fa.p, v), math.Add2f(fa.p, v)
		w.line(fa.p, e1)

		perp := math.Scale2f(rightNormal(v), 0.125) // shorter
		w.line(math.Sub2f(e0, perp), math.Add2f(e0, perp))
		w.line(math.Sub2f(e1, perp), math.Add2f(e1, perp))
	}

	drawName := wp.Fix[0] != '_'
	if _, err := math.ParseLatLong([]byte(wp.Fix)); err == nil {
		// Also don't draw names that are directly specified as latlongs.
		drawName = false
	}

	inbound, outbound := fa.inbound, fa.outbound
	if inbound == [2]float32{} && outbound == [2]float32{} {
		outbound = [2]float32{0, 1}
	}
	offset := calculateOffset(style.Font, func(j int) ([2]float32, bool) {
		switch {
		case j < 0 && inbound != [2]float32{}:
			return math.Sub2f(fa.p, inbound), true
		case j > 0 && outbound != [2]float32{}:
			return math.Add2f(fa.p, outbound), true
		case j == 0:
			return fa.p, true
		default:
			return [2]float32{}, false
		}
	})

	// Draw the text for the waypoint, including fix name, any
	// properties, and altitude/speed restrictions.
	p := math.Add2f(pw, offset)
	var lines []string
	addLine := func(text string) {
		p = td.AddText(text+"\n", p, style)
		lines = append(lines, text)
	}
	if drawName {
		addLine(wp.Fix)
	}

	var flags []string
	for _, f := range []struct {
		set  bool
		name string
	}{{wp.IAF(), "IAF"}, {wp.IF(), "IF"}, {wp.FAF(), "FAF"}, {wp.NoPT(), "NoPT"}, {wp.FlyOver(), "FlyOver"},
		{wp.Land(), "Land"}, {wp.Delete(), "Delete"}} {
		if f.set {
			flags = append(flags, f.name)
		}
	}
	if len(flags) > 0 {
		addLine(strings.Join(flags, "/"))
	}
	if fa.actions != "" {
		addLine(fa.actions)
	}

	next := p // where a line added to the block later goes
	if wp.SpeedRestriction() != nil || wp.AltitudeRestriction() != nil {
		p[1] -= 0.25 * float32(style.Font.Size) // extra space for lines above if needed
		next = p

		if ar := wp.AltitudeRestriction(); ar != nil {
			pt := p           // draw position for text
			var width float32 // max width of altitudes drawn
			if ar.Range[1] != av.MaxAltitude {
				// Upper altitude
				pp := td.AddText(av.FormatAltitude(ar.Range[1]), pt, style)
				width = pp[0] - pt[0]
				pt[1] -= float32(style.Font.Size)
			}
			if ar.Range[0] != 0 && ar.Range[0] != ar.Range[1] {
				// Lower altitude, if present and different than upper.
				pp := td.AddText(av.FormatAltitude(ar.Range[0]), pt, style)
				width = max(width, pp[0]-pt[0])
				pt[1] -= float32(style.Font.Size)
			}
			next[1] = pt[1]

			// Now that we have the width, we can draw lines the specify the
			// restrictions.
			if ar.Range[1] != av.MaxAltitude {
				// At or below (or at)
				ldr.AddLine([2]float32{p[0], p[1] + 2}, [2]float32{p[0] + width, p[1] + 2}, color)
			}
			if ar.Range[0] != 0 {
				// At or above (or at)
				ldr.AddLine([2]float32{p[0], pt[1] - 2}, [2]float32{p[0] + width, pt[1] - 2}, color)
			}

			// update text draw position so that speed restrictions are
			// drawn in a reasonable place; note that we maintain the
			// original p[1] regardless of how many lines were drawn
			// for altitude restrictions.
			p[0] += width + 4
		}

		if sr := wp.SpeedRestriction(); sr != nil {
			p0 := p
			// Display the effective target speed with 'K' suffix
			var speedText string
			if sr.Range[0] == sr.Range[1] {
				speedText = fmt.Sprintf("%.0fK", sr.Range[0])
			} else if sr.Range[0] == 0 {
				speedText = fmt.Sprintf("%.0fK", sr.Range[1])
			} else if sr.Range[1] == av.MaxRestrictionSpeed {
				speedText = fmt.Sprintf("%.0fK", sr.Range[0])
			} else {
				speedText = fmt.Sprintf("%.0fK", sr.Range[1])
			}
			p1 := td.AddText(speedText, p, style)
			p1[1] -= float32(style.Font.Size)
			next[1] = min(next[1], p1[1])

			if sr.Range[1] != av.MaxRestrictionSpeed {
				// At or below (or at): line above
				ldr.AddLine([2]float32{p0[0], p0[1] + 2}, [2]float32{p1[0], p0[1] + 2}, color)
			}
			if sr.Range[0] != 0 {
				// At or above (or at): line below
				ldr.AddLine([2]float32{p0[0], p1[1] - 2}, [2]float32{p1[0], p1[1] - 2}, color)
			}
		}
	}
	w.drawn.blocks = append(w.drawn.blocks, textBlock{at: pw, next: next, lines: lines})
}

///////////////////////////////////////////////////////////////////////////
// Altitude and groundspeed profile

// routeProfile estimates the altitude and groundspeed of a typical jet
// along a route, for placing altitude triggers and sizing turns. It is
// driven only by the route's own data--field elevation, cleared altitude,
// restrictions, and climb and descent actions--so that one drawing serves
// every aircraft that flies the route. The altitude is unknown until one of
// those pins it.
type routeProfile struct {
	altitude       float32 // ft MSL; valid iff known
	known          bool
	target         float32 // ft MSL; 0 = level
	fieldElevation float32 // ft MSL, for the AGL bands
	gs             float32 // kt, if a speed restriction pinned it
}

// profileBand gives the climb gradient and groundspeed of a typical jet
// below a height above the field. The sim flies departures at 180 knots
// below 1500' AGL, 210 to 5000' AGL, and 250 below 10000' (nav/speed.go),
// climbing at around 2800 feet per minute less its derates while
// accelerating and above 5000' (nav/alt.go).
type profileBand struct {
	top      float32 // ft AGL
	gradient float32 // ft per nm climbing
	gs       float32 // kt
}

var profileBands = []profileBand{
	{top: 1500, gradient: 700, gs: 180},
	{top: 5000, gradient: 650, gs: 210},
	{top: 10000, gradient: 450, gs: 250},
	{top: gomath.MaxFloat32, gradient: 450, gs: 280},
}

// descentGradient is a typical 3 degree descent, in feet per nm.
const descentGradient = 300

func (p *routeProfile) band() profileBand {
	agl := float32(0)
	if p.known {
		agl = p.altitude - p.fieldElevation
	}
	return profileBands[slices.IndexFunc(profileBands, func(b profileBand) bool { return agl < b.top })]
}

// Groundspeed returns the estimated groundspeed in knots.
func (p *routeProfile) Groundspeed() float32 {
	if p.gs > 0 {
		return p.gs
	}
	return p.band().gs
}

// TurnRadius returns the radius in nm of a standard-rate turn at the
// estimated groundspeed, which is how nav flies maneuvers.
func (p *routeProfile) TurnRadius() float32 {
	return p.Groundspeed() / 3600 / math.Radians(float32(3))
}

// Advance moves the altitude estimate along nm of flight toward the target.
func (p *routeProfile) Advance(nm float32) {
	if !p.known || p.target == 0 {
		return
	}
	if p.target < p.altitude {
		p.altitude = max(p.target, p.altitude-nm*descentGradient)
		return
	}
	for _, b := range profileBands {
		top := b.top + p.fieldElevation
		if p.altitude >= top {
			continue
		}
		climb := min(top, p.target) - p.altitude
		if d := climb / b.gradient; d >= nm {
			p.altitude += nm * b.gradient
			return
		} else {
			p.altitude += climb
			nm -= d
		}
		if p.altitude >= p.target {
			return
		}
	}
}

// DistanceToAltitude returns the distance in nm at which the estimate
// reaches alt, climbing for an at-or-above condition and descending
// otherwise; zero if it is already met. ok is false if the altitude is
// unknown or the target doesn't take it there.
func (p *routeProfile) DistanceToAltitude(alt float32, atOrAbove bool) (float32, bool) {
	if !p.known {
		return 0, false
	}
	if !atOrAbove {
		if p.altitude <= alt {
			return 0, true
		}
		if p.target == 0 || p.target > alt {
			return 0, false
		}
		return (p.altitude - alt) / descentGradient, true
	}

	if p.altitude >= alt {
		return 0, true
	}
	if p.target < alt {
		return 0, false
	}
	var d float32
	a := p.altitude
	for _, b := range profileBands {
		top := b.top + p.fieldElevation
		if a >= top {
			continue
		}
		climb := min(top, alt) - a
		d += climb / b.gradient
		a += climb
		if a >= alt {
			break
		}
	}
	return d, true
}

// CrossFix applies a fix's altitude and speed restrictions to the estimate.
func (p *routeProfile) CrossFix(wp *av.Waypoint) {
	if ar := wp.AltitudeRestriction(); ar != nil {
		lo, hi := ar.Range[0], ar.Range[1]
		if p.known {
			p.altitude = math.Clamp(p.altitude, lo, hi)
		} else {
			p.altitude = util.Select(hi != av.MaxAltitude, hi, lo)
		}
		p.known = true
	}
	if sr := wp.SpeedRestriction(); sr != nil && !sr.IsMach {
		lo, hi := sr.Range[0], sr.Range[1]
		p.gs = util.Select(hi != av.MaxRestrictionSpeed, hi, lo)
	}
}

// Apply applies an action group's climb or descent to the estimate.
func (p *routeProfile) Apply(a av.WaypointActions) {
	if a.ClimbAltitude != 0 {
		p.target = float32(a.ClimbAltitude)
	}
	if a.DescendAltitude != 0 {
		p.target = float32(a.DescendAltitude)
	}
}

// procedureTurnLegLength returns the length in nm of a procedure turn's
// outbound legs at the given groundspeed.
func procedureTurnLegLength(pt *av.ProcedureTurn, appr av.ApproachType, gs float32) float32 {
	nm, minutes := pt.LegLimit(appr)
	switch {
	case nm > 0:
		return nm
	case minutes > 0:
		return minutes * gs / 60
	default:
		return 4
	}
}
