// eram/annotations.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package eram

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/renderer"
	"github.com/mmp/vice/scope"
	"github.com/mmp/vice/util"
)

// Graphic annotations are the rectangles, circles, lines, and text that the
// DRAW menu puts on the situation display (3.1). They are scratch pad marks
// for the user's own reference, so they are kept by the scope rather than
// shared through the sim.

// AnnotationShape is what an annotation draws.
type AnnotationShape int

const (
	AnnotationRectangle AnnotationShape = iota
	AnnotationCircle
	AnnotationLine
	AnnotationText
)

// AnnotationColor is one of the four colors of annotations, in the order of
// the Color Selection button's swatches: left to right, then top to bottom.
type AnnotationColor int

const (
	AnnotationGreen AnnotationColor = iota
	AnnotationCoral
	AnnotationWhite
	AnnotationYellow
	numAnnotationColors
)

const (
	maxAnnotations = 200
	// maxLinePoints is the number of points of a line annotation with
	// the most segments allowed, 29 (3.1.4.4).
	maxLinePoints = 30
	// Text annotations have at most this many lines of at most this many
	// characters (3.1.4).
	annotationTextChars = 20
	annotationTextLines = 5
	// annotationHandleGap is the space between an annotation and its
	// handle, and around the text in its composition box.
	annotationHandleGap = 4
)

// Annotation is a graphic annotation.
type Annotation struct {
	Shape AnnotationShape
	Color AnnotationColor
	// Map is set for an annotation anchored to the map, whose points are
	// latitude-longitude; the points of one anchored to the display are
	// window coordinates.
	Map bool
	// Points holds a rectangle's opposite corners, a circle's center, a
	// line's points, or the top left of text.
	Points [][2]float32
	// Radius is a circle's radius, in nautical miles if it is anchored to
	// the map and in pixels otherwise.
	Radius float32
	Text   []string
	Font   int
}

func (a *Annotation) toWindow(t *scope.Transformations, p [2]float32) [2]float32 {
	if a.Map {
		return t.WindowFromLatLongP(p)
	}
	return p
}

func (a *Annotation) fromWindow(t *scope.Transformations, w [2]float32) [2]float32 {
	if a.Map {
		return t.LatLongFromWindowP(w)
	}
	return w
}

// pixelsPerUnit returns the pixels that one unit of a circle's radius
// spans.
func (a *Annotation) pixelsPerUnit(ctx *scope.Context, t *scope.Transformations) float32 {
	if a.Map {
		return 1 / t.PixelDistanceNM(ctx.NmPerLongitude)
	}
	return 1
}

// moveRef returns the point that follows the cursor while the annotation
// is moved: a rectangle's top left corner, a circle's center, a line's
// first point, or the top left of text.
func (a *Annotation) moveRef() [2]float32 {
	if a.Shape == AnnotationRectangle {
		p0, p1 := a.Points[0], a.Points[1]
		return [2]float32{min(p0[0], p1[0]), max(p0[1], p1[1])}
	}
	return a.Points[0]
}

// orderCorners orders a rectangle's corners top left, then bottom right.
// Points are oriented alike whether they are window coordinates or
// latitude-longitude.
func (a *Annotation) orderCorners() {
	p0, p1 := a.Points[0], a.Points[1]
	a.Points[0] = [2]float32{min(p0[0], p1[0]), max(p0[1], p1[1])}
	a.Points[1] = [2]float32{max(p0[0], p1[0]), min(p0[1], p1[1])}
}

// translate moves the annotation so that its moveRef is at p.
func (a *Annotation) translate(p [2]float32) {
	d := math.Sub2f(p, a.moveRef())
	for i := range a.Points {
		a.Points[i] = math.Add2f(a.Points[i], d)
	}
}

func (a *Annotation) clone() Annotation {
	c := *a
	c.Points = slices.Clone(a.Points)
	c.Text = slices.Clone(a.Text)
	return c
}

// annotationFunction is the DRAW menu function that is active: the shape
// to create, or what a TBE on an annotation's handle does.
type annotationFunction int

const (
	noAnnotationFunction annotationFunction = iota
	createRectangle
	createCircle
	createLine
	createText
	applyAnnotationFont
	deleteAnnotation
	deleteAllAnnotations
)

// shape returns the shape that a function creates, if it creates one.
func (f annotationFunction) shape() (AnnotationShape, bool) {
	switch f {
	case createRectangle:
		return AnnotationRectangle, true
	case createCircle:
		return AnnotationCircle, true
	case createLine:
		return AnnotationLine, true
	case createText:
		return AnnotationText, true
	default:
		return 0, false
	}
}

// frameMode is what the trackball does to the annotation being created or
// edited.
type frameMode int

const (
	noFrame frameMode = iota
	// sizingFrame moves a rectangle's bottom right corner or a circle's
	// edge.
	sizingFrame
	// movingFrame moves the whole annotation by its moveRef.
	movingFrame
	// drawingLine moves the end of a line's last segment.
	drawingLine
	// composingText takes the text from the keyboard.
	composingText
)

// drawMenuState is the state of the DRAW menu's functions.
type drawMenuState struct {
	function annotationFunction
	// openerInstance and openerID are the instance and the id of the
	// button that activated the function; the Circle Location Text Box
	// displays below the CIRCLE button clicked.
	openerInstance *toolbarInstance
	openerID       string
	// mode and frame are the annotation being created or edited: frame is
	// the white copy of it that follows the trackball, and editing is the
	// index of the annotation it replaces when it is anchored, or -1 for
	// a new one.
	mode    frameMode
	frame   Annotation
	editing int
	// text is what has been typed for a text annotation.
	text      string
	circleBox *circleLocationBox
}

// circleLocationBox is the Circle Location Text Box, which takes the
// location of a circle to create from the keyboard (3.1.3.4).
type circleLocationBox struct {
	input string
	err   error // the last entry's error: errCircleInvalid or errCircleNotFound
	// extent and exit are the box and its exit pick area as last drawn.
	extent, exit math.Extent2D
}

var (
	errCircleInvalid  = errors.New("INVALID")
	errCircleNotFound = errors.New("NOT FOUND")
)

///////////////////////////////////////////////////////////////////////////
// DRAW menu functions

// clickAnnotationFunction activates a DRAW menu function, ending any other
// one, or ends it if it is active. DEL ALL deletes the annotations when it
// is clicked while active (3.1.4.3).
func (ep *Scope) clickAnnotationFunction(f annotationFunction, c toolbarClick) {
	as := &ep.drawMenu
	if as.function != f {
		ep.cancelAnnotationFunction()
		as.function = f
		if c.button != nil {
			as.openerInstance, as.openerID = c.button.instance, c.button.button.id
		}
		return
	}
	if f == deleteAllAnnotations {
		ep.Annotations = nil
	}
	ep.cancelAnnotationFunction()
}

// cancelAnnotationFunction ends the active DRAW menu function and the
// creation or edit of an annotation, as CLEAR does.
func (ep *Scope) cancelAnnotationFunction() {
	ep.drawMenu = drawMenuState{}
}

// drawMenuDisplayed reports whether the DRAW menu is displayed anywhere.
// The handles of the annotations are displayed only while it is, and its
// functions stay active only while it is.
func (ep *Scope) drawMenuDisplayed() bool {
	return slices.ContainsFunc(ep.toolbar.instances(), func(inst *toolbarInstance) bool {
		return slices.Contains(ep.openMenus(inst), drawMenu.id)
	})
}

// annotationHandlesDisplayed reports whether the annotations' handles are
// displayed: not while an annotation is being created or edited, nor while
// there is text in the MCA (3.1.4.5).
func (ep *Scope) annotationHandlesDisplayed() bool {
	as := &ep.drawMenu
	_, creating := as.function.shape()
	return ep.drawMenuDisplayed() && !creating && as.mode == noFrame && len(ep.input) == 0
}

///////////////////////////////////////////////////////////////////////////
// Mouse

// handleAnnotationMouse handles the trackball for the DRAW menu's
// functions: the creation of an annotation, the frame of one being
// created or edited, and the annotations' handles. It runs after the
// toolbars have taken their clicks. Only a frame takes those over the
// views and the toolbars' backgrounds.
func (ep *Scope) handleAnnotationMouse(ctx *scope.Context, transforms scope.Transformations) {
	if !ep.drawMenuDisplayed() {
		ep.cancelAnnotationFunction()
		return
	}
	mouse := ctx.Mouse
	if mouse == nil {
		return
	}
	as := &ep.drawMenu
	t := &transforms
	ep.followAnnotationFrame(ctx, t, mouse.Pos)

	primary, tertiary := ep.mousePrimaryClicked(mouse), ep.mouseTertiaryClicked(mouse)
	if !primary && !tertiary {
		return
	}
	if as.mode == noFrame && (ep.viewCovers(mouse.Pos) || ep.toolbarCovers(mouse.Pos)) {
		return
	}
	if as.mode != noFrame {
		ep.clickAnnotationFrame(ctx, t, mouse.Pos, tertiary)
		ep.consumeMouseClick(mouse)
		return
	}
	if _, ok := as.function.shape(); ok {
		if tertiary {
			// The first point is picked (3.1.3).
			ep.reportInvalidClick(true)
		} else {
			ep.startAnnotation(ctx, t, mouse.Pos)
		}
		ep.consumeMouseClick(mouse)
		return
	}
	if !ep.annotationHandlesDisplayed() {
		return
	}
	// The newest annotation is drawn over the others.
	for i := len(ep.Annotations) - 1; i >= 0; i-- {
		if ep.annotationHandle(ctx, t, &ep.Annotations[i]).Inside(mouse.Pos) {
			ep.clickAnnotationHandle(ctx, t, i, tertiary)
			ep.consumeMouseClick(mouse)
			return
		}
	}
}

// followAnnotationFrame moves the frame of the annotation being created or
// edited with the cursor.
func (ep *Scope) followAnnotationFrame(ctx *scope.Context, t *scope.Transformations, p [2]float32) {
	as := &ep.drawMenu
	f := &as.frame
	switch as.mode {
	case sizingFrame:
		if f.Shape == AnnotationCircle {
			f.Radius = math.Distance2f(f.toWindow(t, f.Points[0]), p) / f.pixelsPerUnit(ctx, t)
		} else {
			f.Points[1] = f.fromWindow(t, p)
		}
	case movingFrame:
		f.translate(f.fromWindow(t, p))
	case drawingLine:
		f.Points[len(f.Points)-1] = f.fromWindow(t, p)
	}
}

// startAnnotation starts creating an annotation of the active function's
// shape at p, in the selected color and anchor. A rectangle starts at its
// top left corner and a line at its first point, and a circle starts at
// its center with the O SIZE radius if it is anchored to the map. Text
// starts with the box it is composed in.
func (ep *Scope) startAnnotation(ctx *scope.Context, t *scope.Transformations, p [2]float32) {
	if len(ep.Annotations) >= maxAnnotations {
		ep.reportInvalidClick(false)
		return
	}
	as := &ep.drawMenu
	ps := ep.currentPrefs()
	shape, _ := as.function.shape()
	f := Annotation{Shape: shape, Color: ps.Draw.Color, Map: ps.Draw.MapAnchor}
	f.Points = [][2]float32{f.fromWindow(t, p)}
	as.editing = -1
	switch shape {
	case AnnotationRectangle:
		f.Points = append(f.Points, f.Points[0])
		as.mode = sizingFrame
	case AnnotationCircle:
		if f.Map {
			f.Radius = float32(ps.Draw.CircleRadius)
		}
		as.mode = sizingFrame
	case AnnotationLine:
		f.Points = append(f.Points, f.Points[0])
		as.mode = drawingLine
	case AnnotationText:
		f.Font = ps.Draw.Font
		as.mode = composingText
	}
	as.frame = f
	as.circleBox = nil
	if as.mode == sizingFrame {
		ctx.SetMousePosition(annotationSizeRef(ctx, t, &as.frame))
	}
}

// annotationSizeRef returns where the cursor is while an annotation is
// sized: at a rectangle's bottom right corner and at the right of a
// circle (figures 3-5 and 3-6).
func annotationSizeRef(ctx *scope.Context, t *scope.Transformations, a *Annotation) [2]float32 {
	if a.Shape == AnnotationCircle {
		c := a.toWindow(t, a.Points[0])
		return [2]float32{c[0] + a.Radius*a.pixelsPerUnit(ctx, t), c[1]}
	}
	w0, w1 := a.toWindow(t, a.Points[0]), a.toWindow(t, a.Points[1])
	return [2]float32{max(w0[0], w1[0]), min(w0[1], w1[1])}
}

// clickAnnotationFrame handles a click while an annotation is created or
// edited. TBE anchors it; TBP switches a rectangle or circle between
// sizing and moving, adds a point to a line, and places the text
// composition box.
func (ep *Scope) clickAnnotationFrame(ctx *scope.Context, t *scope.Transformations, p [2]float32, tertiary bool) {
	as := &ep.drawMenu
	f := &as.frame
	if tertiary {
		if as.mode == composingText {
			ep.anchorAnnotationText()
		} else {
			ep.anchorAnnotationFrame()
		}
		return
	}
	switch as.mode {
	case sizingFrame:
		as.mode = movingFrame
		ctx.SetMousePosition(f.toWindow(t, f.moveRef()))
	case movingFrame:
		if f.Shape != AnnotationRectangle && f.Shape != AnnotationCircle {
			// Lines and text can only be moved (3.1.4.5).
			ep.reportInvalidClick(false)
			return
		}
		if f.Shape == AnnotationRectangle {
			// Sizing moves the second corner, so it must be the bottom
			// right one.
			f.orderCorners()
		}
		as.mode = sizingFrame
		ctx.SetMousePosition(annotationSizeRef(ctx, t, f))
	case drawingLine:
		n := len(f.Points)
		if n >= maxLinePoints {
			ep.reportInvalidClick(false)
		} else if f.toWindow(t, f.Points[n-2]) != f.toWindow(t, f.Points[n-1]) {
			f.Points = append(f.Points, f.Points[n-1])
		}
	case composingText:
		f.Points[0] = f.fromWindow(t, p)
	}
}

// anchorAnnotationFrame anchors the annotation being created or edited,
// replacing the one edited, and ends the DRAW menu function. An empty
// rectangle or circle or a line without a segment can't be anchored.
func (ep *Scope) anchorAnnotationFrame() {
	as := &ep.drawMenu
	f := as.frame.clone()
	if f.Shape == AnnotationLine && as.mode == drawingLine {
		if n := len(f.Points); f.Points[n-1] == f.Points[n-2] {
			f.Points = f.Points[:n-1]
		}
	}
	empty := false
	switch f.Shape {
	case AnnotationRectangle:
		empty = f.Points[0][0] == f.Points[1][0] || f.Points[0][1] == f.Points[1][1]
	case AnnotationCircle:
		empty = f.Radius == 0
	case AnnotationLine:
		empty = len(f.Points) < 2
	}
	if empty {
		ep.reportInvalidClick(true)
		return
	}
	if as.editing >= 0 {
		ep.Annotations[as.editing] = f
	} else {
		ep.Annotations = append(ep.Annotations, f)
	}
	ep.cancelAnnotationFunction()
}

// clickAnnotationHandle handles a click on the handle of the i'th
// annotation. TBP selects it to be moved. TBE applies the selected color,
// or with FONT active applies the A FONT size to text, and with DEL active
// deletes it (3.1.2.3, 3.1.4.2, 3.1.4.3).
func (ep *Scope) clickAnnotationHandle(ctx *scope.Context, t *scope.Transformations, i int, tertiary bool) {
	as := &ep.drawMenu
	ps := ep.currentPrefs()
	a := &ep.Annotations[i]
	switch {
	case !tertiary && as.function == noAnnotationFunction:
		as.frame = a.clone()
		as.editing = i
		as.mode = movingFrame
		ctx.SetMousePosition(a.toWindow(t, a.moveRef()))
	case tertiary && as.function == noAnnotationFunction:
		a.Color = ps.Draw.Color
	case tertiary && as.function == applyAnnotationFont && a.Shape == AnnotationText:
		a.Font = ps.Draw.Font
		ep.cancelAnnotationFunction()
	case tertiary && as.function == deleteAnnotation:
		ep.Annotations = slices.Delete(ep.Annotations, i, i+1)
		ep.cancelAnnotationFunction()
	default:
		ep.reportInvalidClick(tertiary)
	}
}

///////////////////////////////////////////////////////////////////////////
// Keyboard

// handleAnnotationKeyboard takes the keystrokes for the text being
// composed and, while the circle function is active, for the Circle
// Location Text Box, which typing displays. CLEAR ends the active DRAW
// menu function. It reports whether it took the keystrokes.
func (ep *Scope) handleAnnotationKeyboard(ctx *scope.Context) bool {
	as := &ep.drawMenu
	if as.function == noAnnotationFunction && as.mode == noFrame {
		return false
	}
	if _, ok := ctx.Keyboard.Pressed[imgui.KeyEscape]; ok {
		ep.cancelAnnotationFunction()
		return true
	}
	switch {
	case as.mode == composingText:
		ep.composeAnnotationText(ctx)
	case as.function == createCircle && as.mode == noFrame && (as.circleBox != nil || ctx.Keyboard.Input != ""):
		ep.typeCircleLocation(ctx)
	default:
		return false
	}
	return true
}

// composeAnnotationText adds the keystrokes to the text being composed;
// [Enter] anchors it.
func (ep *Scope) composeAnnotationText(ctx *scope.Context) {
	as := &ep.drawMenu
	for _, r := range strings.ToUpper(ctx.Keyboard.Input) {
		if r < ' ' || r > '~' {
			continue
		}
		if len(wrapAnnotationText(as.text+string(r))) > annotationTextLines {
			ep.soundAlarm()
			continue
		}
		as.text += string(r)
	}
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if n := len(as.text); n > 0 {
				as.text = as.text[:n-1]
			}
		case imgui.KeyEnter:
			ep.anchorAnnotationText()
		}
	}
}

// anchorAnnotationText anchors the text being composed; with none, the text
// function just ends.
func (ep *Scope) anchorAnnotationText() {
	as := &ep.drawMenu
	if strings.TrimSpace(as.text) == "" {
		ep.cancelAnnotationFunction()
		return
	}
	as.frame.Text = util.MapSlice(wrapAnnotationText(as.text), func(s string) string { return strings.TrimRight(s, " ") })
	ep.anchorAnnotationFrame()
}

// wrapAnnotationText breaks text into lines of annotationTextChars or
// fewer, at the last space that fits when there is one.
func wrapAnnotationText(text string) []string {
	var lines []string
	for len(text) > annotationTextChars {
		if cut := strings.LastIndexByte(text[:annotationTextChars+1], ' '); cut > 0 {
			lines = append(lines, text[:cut])
			text = text[cut+1:]
		} else {
			lines = append(lines, text[:annotationTextChars])
			text = text[annotationTextChars:]
		}
	}
	return append(lines, text)
}

// typeCircleLocation adds the keystrokes to the Circle Location Text Box;
// [Enter] creates the circle at the location entered.
func (ep *Scope) typeCircleLocation(ctx *scope.Context) {
	as := &ep.drawMenu
	if as.circleBox == nil {
		as.circleBox = &circleLocationBox{}
	}
	box := as.circleBox
	for _, r := range strings.ToUpper(ctx.Keyboard.Input) {
		if r >= ' ' && r <= '~' {
			box.input += string(r)
			box.err = nil
		}
	}
	for key := range ctx.Keyboard.Pressed {
		switch key {
		case imgui.KeyBackspace:
			if n := len(box.input); n > 0 {
				box.input = box.input[:n-1]
				box.err = nil
			}
		case imgui.KeyEnter:
			ep.enterCircleLocation(ctx)
		}
	}
}

// enterCircleLocation creates a circle anchored to the map at the location
// in the Circle Location Text Box, with the O SIZE radius and the selected
// color, or displays the entry's error.
func (ep *Scope) enterCircleLocation(ctx *scope.Context) {
	box := ep.drawMenu.circleBox
	p, err := locateCircle(box.input, ctx.Client.State.Locate)
	if err == nil && len(ep.Annotations) >= maxAnnotations {
		err = errCircleInvalid
	}
	if err != nil {
		box.err = err
		ep.soundAlarm()
		return
	}
	ps := ep.currentPrefs()
	ep.Annotations = append(ep.Annotations, Annotation{Shape: AnnotationCircle, Color: ps.Draw.Color, Map: true,
		Points: [][2]float32{p}, Radius: float32(ps.Draw.CircleRadius)})
	ep.cancelAnnotationFunction()
}

// locateCircle locates the center of a circle entered as a fix name, a
// fix-radial-distance, or a latitude/longitude. An entry in the form of a
// fix name or a fix-radial-distance that isn't adapted is not found; any
// other entry that isn't a latitude/longitude is invalid.
func locateCircle(s string, locate func(string) (math.Point2LL, bool)) (math.Point2LL, error) {
	fixLike := s != "" && isAlpha(s[0]) && !strings.ContainsFunc(s, func(r rune) bool {
		return r > unicode.MaxASCII || !isAlphaNum(byte(r))
	})
	if fixLike {
		if p, ok := locate(s); ok {
			return p, nil
		}
		return math.Point2LL{}, errCircleNotFound
	}
	if p, err := math.ParseLatLong([]byte(s)); err == nil {
		return p, nil
	}
	return math.Point2LL{}, errCircleInvalid
}

///////////////////////////////////////////////////////////////////////////
// Drawing

// annotationRGB returns the color an annotation of the given color is drawn
// in: its COLOR brightness applies to all of the annotations of its color.
func (ep *Scope) annotationRGB(c AnnotationColor) renderer.RGB {
	return ep.currentPrefs().Draw.ColorBright[c].ScaleRGB(colors.annotation[c])
}

// annotationBounds returns the extent of an annotation in window
// coordinates.
func (ep *Scope) annotationBounds(ctx *scope.Context, t *scope.Transformations, a *Annotation) math.Extent2D {
	switch a.Shape {
	case AnnotationCircle:
		c, r := a.toWindow(t, a.Points[0]), a.Radius*a.pixelsPerUnit(ctx, t)
		return math.Extent2D{P0: [2]float32{c[0] - r, c[1] - r}, P1: [2]float32{c[0] + r, c[1] + r}}
	case AnnotationText:
		font := ep.ERAMFont(a.Font)
		tl := a.toWindow(t, a.Points[0])
		chars := util.ReduceSlice(a.Text, 0, func(s string, n int) int { return max(n, len(s)) })
		w, h := float32(chars)*charWidth(font), float32(len(a.Text))*lineHeight(font)
		return math.Extent2D{P0: [2]float32{tl[0], tl[1] - h}, P1: [2]float32{tl[0] + w, tl[1]}}
	default:
		e := math.EmptyExtent2D()
		for _, p := range a.Points {
			e = math.Union(e, a.toWindow(t, p))
		}
		return e
	}
}

// annotationHandle returns the extent of an annotation's handle, a box at
// its top left; a line's is beside its first point.
func (ep *Scope) annotationHandle(ctx *scope.Context, t *scope.Transformations, a *Annotation) math.Extent2D {
	s := cellHeight(ep.ERAMFont(2)) + 2
	b := ep.annotationBounds(ctx, t, a)
	right, top := b.P0[0]-annotationHandleGap, b.P1[1]
	if a.Shape == AnnotationLine {
		p := a.toWindow(t, a.Points[0])
		right, top = p[0]-annotationHandleGap, p[1]+s/2
	}
	return math.Extent2D{P0: [2]float32{right - s, top - s}, P1: [2]float32{right, top}}
}

// drawAnnotations draws the annotations, their handles while they are
// displayed, and the annotation being created or edited.
func (ep *Scope) drawAnnotations(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	as := &ep.drawMenu
	if len(ep.Annotations) == 0 && as.mode == noFrame {
		return
	}
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)
	t := &transforms

	handles := ep.annotationHandlesDisplayed()
	for i := range ep.Annotations {
		a := &ep.Annotations[i]
		color := ep.annotationRGB(a.Color)
		ep.addAnnotation(ld, td, ctx, t, a, color)
		if handles {
			h := ep.annotationHandle(ctx, t, a)
			ld.AddLineLoop(color, h.Corners())
			td.AddTextCentered(util.Select(a.Map, "M", "D"), h.Center(),
				renderer.TextStyle{Font: ep.ERAMFont(2), Color: color})
		}
	}

	f := &as.frame
	white := colors.annotationFrame
	switch as.mode {
	case noFrame:
	case composingText:
		ep.addTextCompositionBox(ld, td, t, f, as.text)
	case movingFrame, sizingFrame, drawingLine:
		if f.Shape == AnnotationText {
			ld.AddLineLoop(white, ep.annotationBounds(ctx, t, f).Expand(annotationHandleGap).Corners())
		} else {
			ep.addAnnotation(ld, td, ctx, t, f, white)
		}
		if f.Shape == AnnotationCircle && f.Map {
			// A map anchored circle shows its radius as it is sized
			// (3.1.3.3).
			td.AddTextCentered(fmt.Sprint(int(math.Round(f.Radius))), f.toWindow(t, f.Points[0]),
				renderer.TextStyle{Font: ep.ERAMFont(2), Color: white})
		}
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}

func (ep *Scope) addAnnotation(ld *renderer.ColoredLinesDrawBuilder, td *renderer.TextDrawBuilder,
	ctx *scope.Context, t *scope.Transformations, a *Annotation, color renderer.RGB) {
	switch a.Shape {
	case AnnotationRectangle:
		ld.AddLineLoop(color, ep.annotationBounds(ctx, t, a).Corners())
	case AnnotationCircle:
		ld.AddCircle(a.toWindow(t, a.Points[0]), a.Radius*a.pixelsPerUnit(ctx, t), 90, color)
	case AnnotationLine:
		ld.AddLineStrip(util.MapSlice(a.Points, func(p [2]float32) [2]float32 { return a.toWindow(t, p) }), color)
	case AnnotationText:
		font := ep.ERAMFont(a.Font)
		p := a.toWindow(t, a.Points[0])
		for _, line := range a.Text {
			td.AddText(line, p, renderer.TextStyle{Font: font, Color: color})
			p[1] -= lineHeight(font)
		}
	}
}

// addTextCompositionBox draws the box that text is composed in, which holds
// the most text a text annotation can have, with what has been typed and
// the cursor.
func (ep *Scope) addTextCompositionBox(ld *renderer.ColoredLinesDrawBuilder, td *renderer.TextDrawBuilder,
	t *scope.Transformations, f *Annotation, text string) {
	font := ep.ERAMFont(f.Font)
	tl := f.toWindow(t, f.Points[0])
	w, h := annotationTextChars*charWidth(font), annotationTextLines*lineHeight(font)
	box := math.Extent2D{P0: [2]float32{tl[0], tl[1] - h}, P1: [2]float32{tl[0] + w, tl[1]}}
	ld.AddLineLoop(colors.annotationFrame, box.Expand(annotationHandleGap).Corners())

	lines := wrapAnnotationText(text)
	lines[len(lines)-1] += "_"
	style := renderer.TextStyle{Font: font, Color: colors.annotationFrame}
	for _, line := range lines {
		td.AddText(line, tl, style)
		tl[1] -= lineHeight(font)
	}
}

///////////////////////////////////////////////////////////////////////////
// Circle Location Text Box

// circleLocationBoxAnchor returns the top left of the Circle Location Text
// Box: below the CIRCLE button that activated the circle function, or the
// same button in another DRAW menu if that one's is no longer displayed
// (figure 3-7).
func (ep *Scope) circleLocationBoxAnchor() ([2]float32, bool) {
	as, l := &ep.drawMenu, &ep.toolbar.layout
	pb := l.findPlaced(as.openerInstance, as.openerID)
	if pb == nil {
		pb = l.findPlaced(nil, as.openerID)
	}
	if pb == nil {
		return [2]float32{}, false
	}
	return pb.face.P0, true
}

// handleCircleLocationMouse handles clicks on the Circle Location Text Box:
// its exit pick area closes it and ends the circle function. It runs
// before the toolbars take their clicks, since the box is drawn over them.
func (ep *Scope) handleCircleLocationMouse(ctx *scope.Context) {
	box, mouse := ep.drawMenu.circleBox, ctx.Mouse
	if box == nil || mouse == nil || (!ep.mousePrimaryClicked(mouse) && !ep.mouseTertiaryClicked(mouse)) {
		return
	}
	if box.exit.Inside(mouse.Pos) {
		ep.cancelAnnotationFunction()
	}
	if box.extent.Inside(mouse.Pos) {
		ep.consumeMouseClick(mouse)
	}
}

// drawCircleLocationBox draws the Circle Location Text Box: its title and
// exit pick area, what has been typed, and the error of an entry that was
// rejected (figures 3-8 and 3-9).
func (ep *Scope) drawCircleLocationBox(ctx *scope.Context, transforms scope.Transformations, cb *renderer.CommandBuffer) {
	box := ep.drawMenu.circleBox
	if box == nil {
		return
	}
	anchor, ok := ep.circleLocationBoxAnchor()
	if !ok {
		return
	}
	ps := ep.currentPrefs()
	font := ep.ERAMToolbarFont()
	cw, rowH := charWidth(font), cellHeight(font)+4
	w := max(toolbarFaceWidth, 16*cw+2*titleBarButtonPad)
	rows := util.Select(box.err != nil, 3, 2)
	box.extent = math.Extent2D{P0: [2]float32{anchor[0], anchor[1] - float32(rows)*rowH},
		P1: [2]float32{anchor[0] + w, anchor[1]}}
	title := math.Extent2D{P0: [2]float32{anchor[0], anchor[1] - rowH}, P1: box.extent.P1}
	box.exit = math.Extent2D{P0: [2]float32{title.P1[0] - rowH, title.P0[1]}, P1: title.P1}

	trid := renderer.GetColoredTrianglesDrawBuilder()
	defer renderer.ReturnColoredTrianglesDrawBuilder(trid)
	ld := renderer.GetColoredLinesDrawBuilder()
	defer renderer.ReturnColoredLinesDrawBuilder(ld)
	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	addQuad(trid, box.extent, colors.popup.backgroundBlack)
	addQuad(trid, title, ps.Brightness.Toolbar.ScaleRGB(colors.view.opaqueBackground))
	border := ps.Brightness.Border.ScaleRGB(colors.view.border)
	ld.AddLineLoop(border, box.extent.Corners())
	ld.AddLine(box.exit.P0, [2]float32{box.exit.P0[0], box.exit.P1[1]}, border)
	ld.AddLine(title.P0, [2]float32{title.P1[0], title.P0[1]}, border)
	if ctx.Mouse != nil && box.exit.Inside(ctx.Mouse.Pos) {
		ld.AddLineLoop(colors.view.hoveredOutline, box.exit.Corners())
	}

	text := renderer.TextStyle{Font: font, Color: ps.Brightness.Text.ScaleRGB(colors.view.clockText)}
	td.AddTextCentered("CIRCLE", math.Extent2D{P0: title.P0, P1: [2]float32{box.exit.P0[0], title.P1[1]}}.Center(), text)
	td.AddTextCentered("X", box.exit.Center(), text)
	td.AddText(box.input+"_", [2]float32{anchor[0] + titleBarButtonPad, anchor[1] - rowH - 2}, text)
	if box.err != nil {
		td.AddTextCentered(box.err.Error(), [2]float32{box.extent.Center()[0], box.extent.P0[1] + rowH/2},
			renderer.TextStyle{Font: font, Color: ps.Brightness.Text.ScaleRGB(colors.yellow)})
	}

	transforms.LoadWindowViewingMatrices(cb)
	cb.LineWidth(1, ctx.DPIScale)
	trid.GenerateCommands(cb)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)
}
