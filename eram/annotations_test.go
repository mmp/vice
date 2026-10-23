// eram/annotations_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/platform"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
)

type annotationTest struct {
	t          *testing.T
	ep         *Scope
	ctx        *scope.Context
	transforms scope.Transformations
}

// makeAnnotationTest returns a scope with the DRAW menu displayed.
func makeAnnotationTest(t *testing.T) *annotationTest {
	ep, ctx := makeToolbarTestScope()
	for i := range ep.systemFont {
		ep.systemFont[i] = &renderer.Font{Size: 13}
	}
	ctx.Platform = warpingPlatform{}
	ctx.NmPerLongitude = 45
	ep.toolbar.main.open = []string{drawMenu.id}
	return &annotationTest{t: t, ep: ep, ctx: ctx,
		transforms: scope.GetTransformations(ctx.DrawExtent, 45, math.Point2LL{-77, 39}, 100, 0)}
}

// move moves the cursor to p, as the trackball does over a frame.
func (a *annotationTest) move(p [2]float32) {
	a.ctx.Mouse = &platform.MouseState{Pos: p}
	a.ep.handleAnnotationMouse(a.ctx, a.transforms)
}

// click clicks at p and returns where the cursor is afterward.
func (a *annotationTest) click(p [2]float32, button imgui.MouseButton) [2]float32 {
	a.ctx.Mouse = &platform.MouseState{Pos: p}
	a.ctx.Mouse.Clicked[button] = true
	a.ep.handleAnnotationMouse(a.ctx, a.transforms)
	return a.ctx.Mouse.Pos
}

func (a *annotationTest) pick(p [2]float32) [2]float32 {
	return a.click(p, platform.MouseButtonPrimary)
}

func (a *annotationTest) enter(p [2]float32) [2]float32 {
	return a.click(p, platform.MouseButtonTertiary)
}

func (a *annotationTest) activate(f annotationFunction) {
	a.ep.clickAnnotationFunction(f, toolbarClick{})
}

// bounds returns the window extent of the i'th annotation.
func (a *annotationTest) bounds(i int) math.Extent2D {
	return a.ep.annotationBounds(a.ctx, &a.transforms, &a.ep.Annotations[i])
}

// handle returns the center of the i'th annotation's handle.
func (a *annotationTest) handle(i int) [2]float32 {
	return a.ep.annotationHandle(a.ctx, &a.transforms, &a.ep.Annotations[i]).Center()
}

// typeText types s and presses the given keys.
func (a *annotationTest) typeText(s string, keys ...imgui.Key) {
	a.ctx.Keyboard = &platform.KeyboardState{Input: s, Pressed: make(map[imgui.Key]any)}
	for _, k := range keys {
		a.ctx.Keyboard.Pressed[k] = nil
	}
	if !a.ep.handleAnnotationKeyboard(a.ctx) {
		a.t.Fatalf("typing %q went past the DRAW menu", s)
	}
}

func near(p, q [2]float32) bool {
	return math.Distance2f(p, q) < 1.5
}

func checkBounds(t *testing.T, what string, got math.Extent2D, p0, p1 [2]float32) {
	t.Helper()
	if !near(got.P0, p0) || !near(got.P1, p1) {
		t.Errorf("%s: %v, want %v-%v", what, got, p0, p1)
	}
}

// A rectangle's top left corner is picked and its size set by the
// trackball; a TBP then moves it by its top left corner, and a TBE anchors
// it in the selected color (3.1.3). The first point can't be entered.
func TestAnnotationRectangle(t *testing.T) {
	a := makeAnnotationTest(t)
	ps := a.ep.currentPrefs()
	ps.Draw.Color = AnnotationGreen

	a.activate(createRectangle)
	a.enter([2]float32{500, 600})
	if a.ep.drawMenu.mode != noFrame || a.ep.drawMenu.function != createRectangle {
		t.Fatalf("a TBE for the first corner started %v", a.ep.drawMenu.mode)
	}
	a.pick([2]float32{500, 600})
	a.move([2]float32{700, 500})
	cursor := a.pick([2]float32{700, 500})
	if a.ep.drawMenu.mode != movingFrame || !near(cursor, [2]float32{500, 600}) {
		t.Fatalf("after the size was picked: mode %v, cursor at %v, want moving with it at the top left",
			a.ep.drawMenu.mode, cursor)
	}
	a.move([2]float32{520, 620})
	a.enter([2]float32{520, 620})

	if len(a.ep.Annotations) != 1 || a.ep.drawMenu.function != noAnnotationFunction {
		t.Fatalf("%d annotations, function %v after the TBE", len(a.ep.Annotations), a.ep.drawMenu.function)
	}
	if r := a.ep.Annotations[0]; r.Shape != AnnotationRectangle || r.Color != AnnotationGreen || !r.Map {
		t.Errorf("anchored %+v, want a green rectangle anchored to the map", r)
	}
	checkBounds(t, "rectangle", a.bounds(0), [2]float32{520, 520}, [2]float32{720, 620})

	// Anchored to the map, it pans with the map.
	a.transforms = scope.GetTransformations(a.ctx.DrawExtent, 45, math.Point2LL{-76.9, 39}, 100, 0)
	if b := a.bounds(0); near(b.P0, [2]float32{520, 520}) {
		t.Errorf("rectangle anchored to the map stayed at %v when the map moved", b)
	}
}

// A rectangle sized up and to the left of its first corner and anchored at
// once keeps its corners in the order picked; resizing it later still moves
// its bottom right corner.
func TestAnnotationRectangleResize(t *testing.T) {
	a := makeAnnotationTest(t)
	a.ep.currentPrefs().Draw.MapAnchor = false

	a.activate(createRectangle)
	a.pick([2]float32{700, 500})
	a.move([2]float32{500, 600})
	a.enter([2]float32{500, 600})
	checkBounds(t, "rectangle", a.bounds(0), [2]float32{500, 500}, [2]float32{700, 600})

	cursor := a.pick(a.handle(0))
	if a.ep.drawMenu.mode != movingFrame || !near(cursor, [2]float32{500, 600}) {
		t.Fatalf("TBP on the handle: mode %v, cursor at %v", a.ep.drawMenu.mode, cursor)
	}
	cursor = a.pick(cursor)
	if a.ep.drawMenu.mode != sizingFrame || !near(cursor, [2]float32{700, 500}) {
		t.Fatalf("TBP while moving: mode %v, cursor at %v, want sizing at the bottom right", a.ep.drawMenu.mode, cursor)
	}
	a.move([2]float32{750, 450})
	a.enter([2]float32{750, 450})
	if len(a.ep.Annotations) != 1 {
		t.Fatalf("%d annotations after resizing one", len(a.ep.Annotations))
	}
	checkBounds(t, "resized rectangle", a.bounds(0), [2]float32{500, 450}, [2]float32{750, 600})
}

// A circle anchored to the map starts with the O SIZE radius, with the
// cursor on its edge; one anchored to the display starts empty (3.1.3).
func TestAnnotationCircle(t *testing.T) {
	a := makeAnnotationTest(t)
	ps := a.ep.currentPrefs()
	ps.Draw.CircleRadius = 10

	a.activate(createCircle)
	center := [2]float32{800, 450}
	cursor := a.pick(center)
	f := &a.ep.drawMenu.frame
	if f.Radius != 10 || math.Abs(cursor[1]-center[1]) > 1 || cursor[0] <= center[0] {
		t.Fatalf("circle frame radius %v, cursor at %v, want 10 nm with it right of the center", f.Radius, cursor)
	}
	nm, c := f.pixelsPerUnit(a.ctx, &a.transforms), f.toWindow(&a.transforms, f.Points[0])
	a.move([2]float32{c[0] + 20*nm, c[1]})
	a.enter([2]float32{c[0] + 20*nm, c[1]})
	if c := a.ep.Annotations[0]; c.Shape != AnnotationCircle || math.Abs(c.Radius-20) > .01 {
		t.Errorf("anchored %+v, want a circle of 20 nm", c)
	}

	ps.Draw.MapAnchor = false
	a.activate(createCircle)
	a.pick(center)
	if r := a.ep.drawMenu.frame.Radius; r != 0 {
		t.Errorf("circle anchored to the display starts with radius %v", r)
	}
	// An empty circle can't be anchored.
	a.enter(center)
	if len(a.ep.Annotations) != 1 || a.ep.drawMenu.mode != sizingFrame {
		t.Errorf("TBE on an empty circle: %d annotations, mode %v", len(a.ep.Annotations), a.ep.drawMenu.mode)
	}
	a.move([2]float32{center[0] + 30, center[1]})
	a.enter([2]float32{center[0] + 30, center[1]})
	if c := a.ep.Annotations[1]; c.Map || c.Radius != 30 {
		t.Errorf("anchored %+v, want a circle of 30 pixels anchored to the display", c)
	}
}

// Each TBP adds a point to a line, and a TBE ends it at the cursor; a line
// has at most 29 segments (3.1.4.4).
func TestAnnotationLine(t *testing.T) {
	a := makeAnnotationTest(t)
	a.ep.currentPrefs().Draw.MapAnchor = false

	a.activate(createLine)
	a.pick([2]float32{100, 100})
	a.move([2]float32{200, 150})
	a.pick([2]float32{200, 150})
	a.move([2]float32{300, 100})
	a.enter([2]float32{300, 100})
	want := [][2]float32{{100, 100}, {200, 150}, {300, 100}}
	if l := a.ep.Annotations[0]; l.Shape != AnnotationLine || !slices.Equal(l.Points, want) {
		t.Fatalf("anchored %+v, want a line through %v", l, want)
	}

	a.activate(createLine)
	for i := range maxLinePoints + 5 {
		p := [2]float32{float32(10 * i), 300}
		a.move(p)
		a.pick(p)
	}
	// The TBE at the last point picked adds no segment.
	a.enter(a.ctx.Mouse.Pos)
	if n := len(a.ep.Annotations[1].Points); n != maxLinePoints {
		t.Errorf("line of %d points, want %d", n, maxLinePoints)
	}
}

// Text is composed where it is picked, wraps at 20 characters, and is
// anchored by [Enter]; there are at most 5 lines (3.1.4).
func TestAnnotationText(t *testing.T) {
	a := makeAnnotationTest(t)
	ps := a.ep.currentPrefs()
	ps.Draw.Font = 3

	a.activate(createText)
	a.pick([2]float32{400, 400})
	if a.ep.drawMenu.mode != composingText {
		t.Fatalf("TBP with A active: mode %v", a.ep.drawMenu.mode)
	}
	a.typeText("bull dog d moa hot sector")
	a.typeText("", imgui.KeyEnter)
	if x := a.ep.Annotations[0]; !slices.Equal(x.Text, []string{"BULL DOG D MOA HOT", "SECTOR"}) || x.Font != 3 {
		t.Errorf("anchored %+v", x)
	}

	a.activate(createText)
	a.pick([2]float32{400, 200})
	a.typeText(strings.Repeat("X", 200))
	if got := wrapAnnotationText(a.ep.drawMenu.text); len(got) != annotationTextLines ||
		len(a.ep.drawMenu.text) != annotationTextLines*annotationTextChars {
		t.Errorf("composed %q, want 5 full lines", got)
	}
	a.typeText("", imgui.KeyEscape)
	if len(a.ep.Annotations) != 1 || a.ep.drawMenu.function != noAnnotationFunction {
		t.Errorf("CLEAR while composing: %d annotations, function %v", len(a.ep.Annotations), a.ep.drawMenu.function)
	}
}

func TestWrapAnnotationText(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{"", []string{""}},
		{"HOT ", []string{"HOT "}},
		{"ABCDEFGHI KLMNOPQRST UVW", []string{"ABCDEFGHI KLMNOPQRST", "UVW"}},
		{"ABCDEFGHIJ KLMNOPQRST UVW", []string{"ABCDEFGHIJ", "KLMNOPQRST UVW"}},
		{"ABCDEFGHIJKLMNOPQRSTUVW", []string{"ABCDEFGHIJKLMNOPQRST", "UVW"}},
	} {
		if got := wrapAnnotationText(c.text); !slices.Equal(got, c.want) {
			t.Errorf("wrap %q: %q, want %q", c.text, got, c.want)
		}
	}
}

// A TBE on a handle applies the selected color, or with FONT active the
// A FONT size, and with DEL active deletes the annotation; a TBP moves it.
// The handles are displayed only with the DRAW menu displayed, no shape
// being created, and the MCA empty.
func TestAnnotationHandles(t *testing.T) {
	a := makeAnnotationTest(t)
	ps := a.ep.currentPrefs()
	ps.Draw.MapAnchor = false
	a.ep.Annotations = []Annotation{
		{Shape: AnnotationText, Color: AnnotationYellow, Points: [][2]float32{{300, 300}}, Text: []string{"HOT"}, Font: 2},
		{Shape: AnnotationCircle, Color: AnnotationYellow, Points: [][2]float32{{800, 400}}, Radius: 50},
	}

	ps.Draw.Color = AnnotationCoral
	a.enter(a.handle(1))
	if c := a.ep.Annotations[1].Color; c != AnnotationCoral {
		t.Errorf("TBE on the handle left the color %v", c)
	}

	a.ep.input.Set("QQ")
	if a.ep.annotationHandlesDisplayed() {
		t.Error("handles displayed with text in the MCA")
	}
	a.ep.input.Clear()
	a.activate(createLine)
	if a.ep.annotationHandlesDisplayed() {
		t.Error("handles displayed while a line is to be created")
	}

	ps.Draw.Font = 6
	a.activate(applyAnnotationFont)
	a.enter(a.handle(0))
	if f := a.ep.Annotations[0].Font; f != 6 || a.ep.drawMenu.function != noAnnotationFunction {
		t.Errorf("FONT then TBE on the text: font %d, function %v", f, a.ep.drawMenu.function)
	}

	cursor := a.pick(a.handle(1))
	if !near(cursor, [2]float32{800, 400}) {
		t.Errorf("TBP on the circle's handle put the cursor at %v, want its center", cursor)
	}
	a.move([2]float32{900, 300})
	if c := a.ep.Annotations[1].Points[0]; c != [2]float32{800, 400} {
		t.Errorf("the original moved to %v before the move was anchored", c)
	}
	a.enter([2]float32{900, 300})
	if c := a.ep.Annotations[1].Points[0]; c != [2]float32{900, 300} {
		t.Errorf("circle moved to %v, want (900, 300)", c)
	}

	a.activate(deleteAnnotation)
	a.enter(a.handle(0))
	if len(a.ep.Annotations) != 1 || a.ep.Annotations[0].Shape != AnnotationCircle {
		t.Errorf("after DEL and a TBE on the text: %+v", a.ep.Annotations)
	}

	a.ep.toolbar.main.open = nil
	if a.ep.annotationHandlesDisplayed() {
		t.Error("handles displayed without the DRAW menu")
	}
	a.activate(createRectangle)
	a.move([2]float32{10, 10})
	if a.ep.drawMenu.function != noAnnotationFunction {
		t.Error("the rectangle function stayed active with the DRAW menu closed")
	}
}

// DEL ALL deletes the annotations when it is clicked a second time.
func TestAnnotationDeleteAll(t *testing.T) {
	a := makeAnnotationTest(t)
	a.ep.Annotations = []Annotation{{Shape: AnnotationCircle, Points: [][2]float32{{0, 0}}, Radius: 5}}
	a.activate(deleteAllAnnotations)
	if len(a.ep.Annotations) != 1 {
		t.Fatal("the first click on DEL ALL deleted the annotations")
	}
	a.activate(deleteAllAnnotations)
	if len(a.ep.Annotations) != 0 || a.ep.drawMenu.function != noAnnotationFunction {
		t.Errorf("after the second click: %d annotations, function %v", len(a.ep.Annotations), a.ep.drawMenu.function)
	}
}

// No annotation is created past the 200th.
func TestAnnotationLimit(t *testing.T) {
	a := makeAnnotationTest(t)
	a.ep.Annotations = make([]Annotation, maxAnnotations)
	a.activate(createText)
	a.pick([2]float32{400, 400})
	if a.ep.drawMenu.mode != noFrame {
		t.Errorf("started annotation %d", maxAnnotations+1)
	}
}

// A circle's location is a fix, a fix-radial-distance, or a
// latitude/longitude; one in the form of a fix that isn't adapted is not
// found, and anything else is invalid (3.1.3.4).
func TestLocateCircle(t *testing.T) {
	fixes := map[string]math.Point2LL{"OTT": {-76.8, 38.9}, "OTT090010": {-76.6, 38.9}}
	locate := func(s string) (math.Point2LL, bool) {
		p, ok := fixes[s]
		return p, ok
	}
	for _, c := range []struct {
		s    string
		want math.Point2LL
		err  error
	}{
		{"OTT", fixes["OTT"], nil},
		{"OTT090010", fixes["OTT090010"], nil},
		{"3900N/07700W", math.Point2LL{-77, 39}, nil},
		{"ZZZZZ", math.Point2LL{}, errCircleNotFound},
		{"OT T", math.Point2LL{}, errCircleInvalid},
		{"OTT/", math.Point2LL{}, errCircleInvalid},
		{"9OTT", math.Point2LL{}, errCircleInvalid},
		{"", math.Point2LL{}, errCircleInvalid},
	} {
		p, err := locateCircle(c.s, locate)
		if err != c.err || (err == nil && !near(p, c.want)) {
			t.Errorf("%q: %v, %v; want %v, %v", c.s, p, err, c.want, c.err)
		}
	}
}

// Typing with the circle function active displays the Circle Location Text
// Box below the CIRCLE button clicked.
func TestCircleLocationBox(t *testing.T) {
	a := makeAnnotationTest(t)
	a.ep.handleToolbarInput(a.ctx)
	pb := a.ep.toolbar.layout.findPlaced(&a.ep.toolbar.main, drawMenu.rows[0][1].id)
	if pb == nil {
		t.Fatal("no CIRCLE button in the DRAW menu")
	}
	a.ctx.Mouse = &platform.MouseState{Pos: pb.face.Center()}
	a.ctx.Mouse.Clicked[platform.MouseButtonPrimary] = true
	a.ep.handleToolbarInput(a.ctx)
	if a.ep.drawMenu.function != createCircle {
		t.Fatalf("clicking CIRCLE activated %v", a.ep.drawMenu.function)
	}

	a.typeText("ott")
	anchor, ok := a.ep.circleLocationBoxAnchor()
	if box := a.ep.drawMenu.circleBox; box == nil || box.input != "OTT" || !ok || anchor != pb.face.P0 {
		t.Errorf("box %+v at %v, want OTT below the CIRCLE button at %v", box, anchor, pb.face.P0)
	}
	a.typeText("", imgui.KeyBackspace)
	if box := a.ep.drawMenu.circleBox; box.input != "OT" {
		t.Errorf("after a backspace: %q", box.input)
	}
}

// A click on one of the Color Selection button's swatches selects its
// color.
func TestColorSelection(t *testing.T) {
	a := makeAnnotationTest(t)
	a.ep.handleToolbarInput(a.ctx)
	face := a.ep.toolbar.layout.findPlaced(&a.ep.toolbar.main, drawMenu.rows[0][6].id).face
	for c := range numAnnotationColors {
		a.ctx.Mouse = &platform.MouseState{Pos: colorSwatch(face, c).Center()}
		a.ctx.Mouse.Clicked[platform.MouseButtonTertiary] = true
		a.ep.handleToolbarInput(a.ctx)
		if got := a.ep.currentPrefs().Draw.Color; got != c {
			t.Errorf("click on swatch %d selected %d", c, got)
		}
	}
}

// The annotations are saved with the scope.
func TestAnnotationsSaved(t *testing.T) {
	ep := &Scope{Annotations: []Annotation{
		{Shape: AnnotationLine, Color: AnnotationCoral, Map: true, Points: [][2]float32{{-77, 39}, {-76, 38}}},
		{Shape: AnnotationText, Color: AnnotationWhite, Points: [][2]float32{{100, 200}}, Text: []string{"HOT"}, Font: 4},
	}}
	b, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Scope
	if err := json.Unmarshal(b, &loaded); err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(ep.Annotations, loaded.Annotations, func(x, y Annotation) bool {
		return x.Shape == y.Shape && x.Color == y.Color && x.Map == y.Map && slices.Equal(x.Points, y.Points) &&
			x.Radius == y.Radius && slices.Equal(x.Text, y.Text) && x.Font == y.Font
	}) {
		t.Errorf("saved %+v, loaded %+v", ep.Annotations, loaded.Annotations)
	}
}

// Preference sets from before the DRAW menu get its defaults.
func TestDrawPreferencesUpgrade(t *testing.T) {
	var p Preferences
	p.Upgrade(120, 121)
	if p.Draw != makeDefaultPreferences().Draw {
		t.Errorf("upgraded DRAW settings %+v", p.Draw)
	}
}
