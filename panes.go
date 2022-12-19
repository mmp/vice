// panes.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/mmp/imgui-go/v4"
)

// Panes (should) mostly operate in window coordinates: (0,0) is lower
// left, just in their own pane, oblivious to the full window size.  Higher
// level code will handle positioning the panes in the main window.
type Pane interface {
	Name() string

	Duplicate(nameAsCopy bool) Pane

	Activate(cs *ColorScheme)
	Deactivate()

	CanTakeKeyboardFocus() bool

	Draw(ctx *PaneContext, cb *CommandBuffer)
}

type PaneUIDrawer interface {
	DrawUI()
}

type PaneContext struct {
	paneExtent       Extent2D
	parentPaneExtent Extent2D

	platform  Platform
	cs        *ColorScheme
	mouse     *MouseState
	keyboard  *KeyboardState
	haveFocus bool
	events    *EventStream
}

type MouseState struct {
	Pos           [2]float32
	Down          [MouseButtonCount]bool
	Clicked       [MouseButtonCount]bool
	Released      [MouseButtonCount]bool
	DoubleClicked [MouseButtonCount]bool
	Dragging      [MouseButtonCount]bool
	DragDelta     [2]float32
	Wheel         [2]float32
}

const (
	MouseButtonPrimary   = 0
	MouseButtonSecondary = 1
	MouseButtonTertiary  = 2
	MouseButtonCount     = 3
)

func (ctx *PaneContext) InitializeMouse(fullDisplayExtent Extent2D) {
	ctx.mouse = &MouseState{}

	// Convert to pane coordinates:
	// imgui gives us the mouse position w.r.t. the full window, so we need
	// to subtract out displayExtent.p0 to get coordinates w.r.t. the
	// current pane.  Further, it has (0,0) in the upper left corner of the
	// window, so we need to flip y w.r.t. the full window resolution.
	pos := imgui.MousePos()
	ctx.mouse.Pos[0] = pos.X - ctx.paneExtent.p0[0]
	ctx.mouse.Pos[1] = fullDisplayExtent.p1[1] - 1 - ctx.paneExtent.p0[1] - pos.Y

	io := imgui.CurrentIO()
	wx, wy := io.MouseWheel()
	ctx.mouse.Wheel = [2]float32{wx, -wy}

	for b := 0; b < MouseButtonCount; b++ {
		ctx.mouse.Down[b] = imgui.IsMouseDown(b)
		ctx.mouse.Released[b] = imgui.IsMouseReleased(b)
		ctx.mouse.Clicked[b] = imgui.IsMouseClicked(b)
		ctx.mouse.DoubleClicked[b] = imgui.IsMouseDoubleClicked(b)
		ctx.mouse.Dragging[b] = imgui.IsMouseDragging(b, 0)
		if ctx.mouse.Dragging[b] {
			delta := imgui.MouseDragDelta(b, 0.)
			// Negate y to go to pane coordinates
			ctx.mouse.DragDelta = [2]float32{delta.X, -delta.Y}
			imgui.ResetMouseDragDelta(b)
		}
	}
}

type Key int

const (
	KeyEnter = iota
	KeyUpArrow
	KeyDownArrow
	KeyLeftArrow
	KeyRightArrow
	KeyHome
	KeyEnd
	KeyBackspace
	KeyDelete
	KeyEscape
	KeyTab
	KeyPageUp
	KeyPageDown
	KeyShift
	KeyControl
	KeyAlt
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
)

type KeyboardState struct {
	Input   string
	Pressed map[Key]interface{}
}

func NewKeyboardState() *KeyboardState {
	keyboard := &KeyboardState{Pressed: make(map[Key]interface{})}

	keyboard.Input = platform.InputCharacters()

	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEnter)) {
		keyboard.Pressed[KeyEnter] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyDownArrow)) {
		keyboard.Pressed[KeyDownArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyUpArrow)) {
		keyboard.Pressed[KeyUpArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyLeftArrow)) {
		keyboard.Pressed[KeyLeftArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyRightArrow)) {
		keyboard.Pressed[KeyRightArrow] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyHome)) {
		keyboard.Pressed[KeyHome] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEnd)) {
		keyboard.Pressed[KeyEnd] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyBackspace)) {
		keyboard.Pressed[KeyBackspace] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyDelete)) {
		keyboard.Pressed[KeyDelete] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEscape)) {
		keyboard.Pressed[KeyEscape] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyTab)) {
		keyboard.Pressed[KeyTab] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyPageUp)) {
		keyboard.Pressed[KeyPageUp] = nil
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyPageDown)) {
		keyboard.Pressed[KeyPageDown] = nil
	}
	const ImguiF1 = 290
	for i := 0; i < 12; i++ {
		if imgui.IsKeyPressed(ImguiF1 + i) {
			keyboard.Pressed[Key(int(KeyF1)+i)] = nil
		}
	}
	io := imgui.CurrentIO()
	if io.KeyShiftPressed() {
		keyboard.Pressed[KeyShift] = nil
	}
	if io.KeyCtrlPressed() {
		keyboard.Pressed[KeyControl] = nil
	}
	if io.KeyAltPressed() {
		keyboard.Pressed[KeyAlt] = nil
	}

	return keyboard
}

func (k *KeyboardState) IsPressed(key Key) bool {
	_, ok := k.Pressed[key]
	return ok
}

func (ctx *PaneContext) SetWindowCoordinateMatrices(cb *CommandBuffer) {
	w := float32(int(ctx.paneExtent.Width() + 0.5))
	h := float32(int(ctx.paneExtent.Height() + 0.5))
	proj := mgl32.Ortho2D(0, w, 0, h)
	cb.LoadProjectionMatrix(proj)
	cb.LoadModelViewMatrix(mgl32.Ident4())
}

type AirportInfoPane struct {
	Airports map[string]interface{}

	ShowTime        bool
	ShowMETAR       bool
	ShowATIS        bool
	ShowUncleared   bool
	ShowDepartures  bool
	ShowDeparted    bool
	ShowArrivals    bool
	ShowControllers bool

	lastATIS       map[string]string
	seenDepartures map[string]interface{}
	seenArrivals   map[string]interface{}

	FontIdentifier FontIdentifier
	font           *Font

	sb *ScrollBar
	cb CommandBuffer
}

func NewAirportInfoPane() *AirportInfoPane {
	// Reasonable (I hope) defaults...
	font := GetDefaultFont()
	return &AirportInfoPane{
		Airports:        make(map[string]interface{}),
		ShowTime:        true,
		ShowMETAR:       true,
		ShowATIS:        true,
		ShowUncleared:   true,
		ShowDepartures:  true,
		ShowDeparted:    true,
		ShowArrivals:    true,
		ShowControllers: true,

		lastATIS:       make(map[string]string),
		seenDepartures: make(map[string]interface{}),
		seenArrivals:   make(map[string]interface{}),

		font:           font,
		FontIdentifier: font.id,
		sb:             NewScrollBar(4, false),
	}
}

func (a *AirportInfoPane) Duplicate(nameAsCopy bool) Pane {
	dupe := *a
	dupe.Airports = DuplicateMap(a.Airports)
	dupe.lastATIS = DuplicateMap(a.lastATIS)
	dupe.seenDepartures = DuplicateMap(a.seenDepartures)
	dupe.seenArrivals = DuplicateMap(a.seenArrivals)
	dupe.sb = NewScrollBar(4, false)
	dupe.cb = CommandBuffer{}
	return &dupe
}

func (a *AirportInfoPane) Activate(cs *ColorScheme) {
	if a.font = GetFont(a.FontIdentifier); a.font == nil {
		a.font = GetDefaultFont()
		a.FontIdentifier = a.font.id
	}
	if a.lastATIS == nil {
		a.lastATIS = make(map[string]string)
	}
	if a.seenDepartures == nil {
		a.seenDepartures = make(map[string]interface{})
	}
	if a.seenArrivals == nil {
		a.seenArrivals = make(map[string]interface{})
	}
	if a.sb == nil {
		a.sb = NewScrollBar(4, false)
	}

	// FIXME: temporary transition
	if a.Airports == nil {
		a.Airports = make(map[string]interface{})
	}
}

func (a *AirportInfoPane) Deactivate() {}

func (a *AirportInfoPane) Name() string {
	n := "Airport Information"
	if len(a.Airports) > 0 {
		n += ": " + strings.Join(SortedMapKeys(a.Airports), ",")
	}
	return n
}

func (a *AirportInfoPane) DrawUI() {
	a.Airports = drawAirportSelector(a.Airports, "Airports")
	if newFont, changed := DrawFontPicker(&a.FontIdentifier, "Font"); changed {
		a.font = newFont
	}
	imgui.Checkbox("Show time", &a.ShowTime)
	imgui.Checkbox("Show weather", &a.ShowMETAR)
	imgui.Checkbox("Show ATIS", &a.ShowATIS)
	imgui.Checkbox("Show uncleared aircraft", &a.ShowUncleared)
	imgui.Checkbox("Show aircraft to depart", &a.ShowDepartures)
	imgui.Checkbox("Show departed aircraft", &a.ShowDeparted)
	imgui.Checkbox("Show arriving aircraft", &a.ShowArrivals)
	imgui.Checkbox("Show controllers", &a.ShowControllers)
}

type Arrival struct {
	aircraft     *Aircraft
	distance     float32
	sortDistance float32
}

type Departure struct {
	*Aircraft
}

func getDistanceSortedArrivals(airports map[string]interface{}) []Arrival {
	var arr []Arrival
	now := server.CurrentTime()
	for _, ac := range server.GetFilteredAircraft(func(ac *Aircraft) bool {
		if ac.OnGround() || ac.LostTrack(now) || ac.flightPlan == nil {
			return false
		}
		_, ok := airports[ac.flightPlan.ArrivalAirport]
		return ok
	}) {
		pos := ac.Position()
		// Filter ones where we don't have a valid position
		if pos[0] != 0 && pos[1] != 0 {
			dist := nmdistance2ll(database.FAA.airports[ac.flightPlan.ArrivalAirport].Location, pos)
			sortDist := dist + float32(ac.Altitude())/300.
			arr = append(arr, Arrival{aircraft: ac, distance: dist, sortDistance: sortDist})
		}
	}

	sort.Slice(arr, func(i, j int) bool {
		return arr[i].sortDistance < arr[j].sortDistance
	})

	return arr
}

func (a *AirportInfoPane) CanTakeKeyboardFocus() bool { return false }

func (a *AirportInfoPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	// It's slightly little wasteful to keep asking each time; better would
	// be to only do so when either our airports change or there's a new
	// server connection.  Not a big deal in the grand scheme of things,
	// however.
	for ap := range a.Airports {
		server.AddAirportForWeather(ap)
	}

	cs := ctx.cs

	var str strings.Builder
	style := TextStyle{Font: a.font, Color: cs.Text}
	var strs []string
	var styles []TextStyle
	nLines := 1

	flush := func() {
		if str.Len() == 0 {
			return
		}
		s := str.String()
		nLines += strings.Count(s, "\n")
		strs = append(strs, s)
		str.Reset()
		styles = append(styles, style)
		style = TextStyle{Font: a.font, Color: cs.Text} // a reasonable default
	}

	now := server.CurrentTime()
	if a.ShowTime {
		str.WriteString(now.UTC().Format("Time: 15:04:05Z\n\n"))
	}

	if a.ShowMETAR {
		var metar []*METAR
		for ap := range a.Airports {
			if m := server.GetMETAR(ap); m != nil {
				metar = append(metar, m)
			}
		}

		if len(metar) > 0 {
			sort.Slice(metar, func(i, j int) bool {
				return metar[i].AirportICAO < metar[j].AirportICAO
			})
			str.WriteString("Weather:\n")
			for _, m := range metar {
				str.WriteString(fmt.Sprintf("  %4s ", m.AirportICAO))
				flush()
				style.Color = cs.TextHighlight
				str.WriteString(fmt.Sprintf("%s ", m.Altimeter))
				flush()
				if m.Auto {
					str.WriteString("AUTO ")
					flush()
				}
				style.Color = cs.TextHighlight
				str.WriteString(fmt.Sprintf("%s ", m.Wind))
				flush()
				str.WriteString(fmt.Sprintf("%s\n", m.Weather))
			}
			str.WriteString("\n")
		}
	}

	if a.ShowATIS {
		var atis []string
		for ap := range a.Airports {
			for _, at := range server.GetAirportATIS(ap) {
				atis = append(atis, fmt.Sprintf("  %-12s: %s", at.Airport, at.Contents))

				if oldATIS, ok := a.lastATIS[at.Airport]; oldATIS != at.Contents {
					a.lastATIS[at.Airport] = at.Contents
					// don't play a sound the first time we get the ATIS.
					if ok {
						eventStream.Post(&UpdatedATISEvent{airport: at.Airport})
					}
				}
			}
		}
		if len(atis) > 0 {
			sort.Strings(atis)
			str.WriteString("ATIS:\n")
			for _, a := range atis {
				str.WriteString(a)
				str.WriteString("\n")
			}
			str.WriteString("\n")
		}
	}

	var uncleared, departures, airborne []Departure
	for _, ac := range server.GetFilteredAircraft(func(ac *Aircraft) bool {
		return ac.flightPlan != nil && !ac.LostTrack(now)
	}) {
		if _, ok := a.Airports[ac.flightPlan.DepartureAirport]; ok {
			if ac.OnGround() {
				if ac.assignedSquawk == 0 {
					uncleared = append(uncleared, Departure{Aircraft: ac})
				} else {
					departures = append(departures, Departure{Aircraft: ac})
				}
			} else {
				airborne = append(airborne, Departure{Aircraft: ac})
			}
		}
	}

	if a.ShowUncleared && len(uncleared) > 0 {
		str.WriteString("Uncleared:\n")
		sort.Slice(uncleared, func(i, j int) bool {
			return uncleared[i].Callsign() < uncleared[j].Callsign()
		})
		for _, ac := range uncleared {
			str.WriteString(fmt.Sprintf("  %-8s %3s %4s-%4s %8s %5d\n", ac.Callsign(),
				ac.flightPlan.Rules, ac.flightPlan.DepartureAirport, ac.flightPlan.ArrivalAirport,
				ac.flightPlan.AircraftType, ac.flightPlan.Altitude))

			// Route
			if len(ac.flightPlan.Route) > 0 {
				str.WriteString("    ")
				str.WriteString(ac.flightPlan.Route)
				str.WriteString("\n")
			}

			if _, ok := a.seenArrivals[ac.Callsign()]; !ok {
				a.seenArrivals[ac.Callsign()] = nil
				globalConfig.AudioSettings.HandleEvent(AudioEventFlightPlanFiled)
			}
		}
		str.WriteString("\n")
	}

	if a.ShowDepartures && len(departures) > 0 {
		str.WriteString("Departures:\n")
		sort.Slice(departures, func(i, j int) bool {
			return departures[i].Callsign() < departures[j].Callsign()
		})
		for _, ac := range departures {
			route := ac.flightPlan.Route
			if len(route) > 10 {
				route = route[:10]
				route += ".."
			}
			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %5d %12s", ac.Callsign(),
				ac.flightPlan.Rules, ac.flightPlan.DepartureAirport, ac.flightPlan.AircraftType,
				ac.scratchpad, ac.flightPlan.Altitude, route))

			// Make sure the squawk is good
			if ac.mode != Charlie || ac.squawk != ac.assignedSquawk {
				str.WriteString(" sq:")
				if ac.mode != Charlie {
					str.WriteString("[C]")
				}
				if ac.squawk != ac.assignedSquawk {
					str.WriteString(ac.assignedSquawk.String())
				}
			}
			str.WriteString("\n")
		}
		str.WriteString("\n")
	}

	if a.ShowDeparted && len(airborne) > 0 {
		sort.Slice(airborne, func(i, j int) bool {
			ai := &airborne[i]
			di := nmdistance2ll(database.FAA.airports[ai.flightPlan.ArrivalAirport].Location, ai.Position())
			aj := &airborne[j]
			dj := nmdistance2ll(database.FAA.airports[aj.flightPlan.ArrivalAirport].Location, aj.Position())
			return di < dj
		})

		str.WriteString("Departed:\n")
		for _, ac := range airborne {
			route := ac.flightPlan.Route
			if len(route) > 10 {
				route = route[:10]
				route += ".."
			}

			alt := ac.Altitude()
			alt = (alt + 50) / 100 * 100
			var clearedAlt string
			if ac.tempAltitude != 0 {
				clearedAlt = fmt.Sprintf("%5dT", ac.tempAltitude)
			} else {
				clearedAlt = fmt.Sprintf("%5d ", ac.flightPlan.Altitude)
			}
			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %s %5d %12s\n", ac.Callsign(),
				ac.flightPlan.Rules, ac.flightPlan.DepartureAirport, ac.flightPlan.AircraftType,
				ac.scratchpad, clearedAlt, alt, route))
		}
		str.WriteString("\n")
	}

	arrivals := getDistanceSortedArrivals(a.Airports)
	if a.ShowArrivals && len(arrivals) > 0 {
		str.WriteString("Arrivals:\n")
		for _, arr := range arrivals {
			ac := arr.aircraft
			alt := ac.Altitude()
			alt = (alt + 50) / 100 * 100

			// Try to extract the STAR from the flight plan.
			route := ac.flightPlan.Route
			star := route[strings.LastIndex(route, " ")+1:]
			if len(star) > 7 {
				star = star[len(star)-7:]
			}

			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %5d  %5d %3dnm %s\n", ac.Callsign(),
				ac.flightPlan.Rules, ac.flightPlan.ArrivalAirport, ac.flightPlan.AircraftType, ac.scratchpad,
				ac.tempAltitude, alt, int(arr.distance), star))

			if _, ok := a.seenArrivals[ac.Callsign()]; !ok {
				globalConfig.AudioSettings.HandleEvent(AudioEventNewArrival)
				a.seenArrivals[ac.Callsign()] = nil
			}
		}
		str.WriteString("\n")
	}

	controllers := server.GetAllControllers()
	if a.ShowControllers && len(controllers) > 0 {
		str.WriteString("Controllers:\n")

		for _, suffix := range []string{"CTR", "APP", "DEP", "TWR", "GND", "DEL", "FSS", "ATIS", "OBS"} {
			first := true
			for _, ctrl := range controllers {
				if server.Callsign() == ctrl.Callsign || !strings.HasSuffix(ctrl.Callsign, suffix) {
					continue
				}

				if ctrl.Frequency == 0 {
					continue
				}

				if first {
					str.WriteString(fmt.Sprintf("  %-4s  ", suffix))
					first = false
				} else {
					str.WriteString("        ")
				}

				if ctrl.RequestRelief {
					flush()
					style.Color = cs.TextHighlight
				}

				str.WriteString(fmt.Sprintf(" %-12s %s", ctrl.Callsign, ctrl.Frequency))

				if ctrl.RequestRelief {
					flush()
				}

				if pos := ctrl.GetPosition(); pos != nil {
					str.WriteString(fmt.Sprintf(" %-3s %s", pos.SectorId, pos.Scope))
				}
				str.WriteString("\n")
			}
		}
	}

	flush()

	nVisibleLines := (int(ctx.paneExtent.Height()) - a.font.size) / a.font.size
	a.sb.Update(nLines, nVisibleLines, ctx)
	textOffset := a.sb.Offset()

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	sz2 := float32(a.font.size) / 2
	texty := ctx.paneExtent.Height() - sz2 + float32(textOffset*a.font.size)
	td.AddTextMulti(strs, [2]float32{sz2, texty}, styles)

	a.cb.Reset()
	ctx.SetWindowCoordinateMatrices(&a.cb)
	td.GenerateCommands(&a.cb)

	a.sb.Draw(ctx, &a.cb)

	cb.Call(a.cb)
}

///////////////////////////////////////////////////////////////////////////
// EmptyPane

type EmptyPane struct {
	// Empty struct types may all have the same address, which breaks
	// assorted assumptions elsewhere in the system....
	wtfgo int
}

func NewEmptyPane() *EmptyPane { return &EmptyPane{} }

func (ep *EmptyPane) Activate(cs *ColorScheme)   {}
func (ep *EmptyPane) Deactivate()                {}
func (ep *EmptyPane) CanTakeKeyboardFocus() bool { return false }

func (ep *EmptyPane) Duplicate(nameAsCopy bool) Pane { return &EmptyPane{} }
func (ep *EmptyPane) Name() string                   { return "(Empty)" }

func (ep *EmptyPane) Draw(ctx *PaneContext, cb *CommandBuffer) {}

///////////////////////////////////////////////////////////////////////////
// FlightPlanPane

type FlightPlanPane struct {
	FontIdentifier FontIdentifier
	font           *Font

	ShowRemarks bool
}

func NewFlightPlanPane() *FlightPlanPane {
	font := GetDefaultFont()
	return &FlightPlanPane{FontIdentifier: font.id, font: font}
}

func (fp *FlightPlanPane) Activate(cs *ColorScheme) {
	if fp.font = GetFont(fp.FontIdentifier); fp.font == nil {
		fp.font = GetDefaultFont()
		fp.FontIdentifier = fp.font.id
	}
}

func (fp *FlightPlanPane) Deactivate()                {}
func (fp *FlightPlanPane) CanTakeKeyboardFocus() bool { return false }

func (fp *FlightPlanPane) DrawUI() {
	imgui.Checkbox("Show remarks", &fp.ShowRemarks)
	if newFont, changed := DrawFontPicker(&fp.FontIdentifier, "Font"); changed {
		fp.font = newFont
	}
}

func (fp *FlightPlanPane) Duplicate(nameAsCopy bool) Pane {
	return &FlightPlanPane{FontIdentifier: fp.FontIdentifier, font: fp.font}
}

func (fp *FlightPlanPane) Name() string { return "Flight Plan" }

func (fp *FlightPlanPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	ac := positionConfig.selectedAircraft
	if ac == nil {
		return
	}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	contents, _ := ac.GetFormattedFlightPlan(fp.ShowRemarks)

	sz2 := float32(fp.font.size) / 2
	spaceWidth, _ := fp.font.BoundText(" ", 0)
	ncols := (int(ctx.paneExtent.Width()) - fp.font.size) / spaceWidth
	indent := 3 + len(ac.Callsign())
	if ac.voiceCapability != VoiceFull {
		indent += 2
	}
	wrapped, _ := wrapText(contents, ncols, indent, true)
	td.AddText(wrapped, [2]float32{sz2, ctx.paneExtent.Height() - sz2},
		TextStyle{Font: fp.font, Color: ctx.cs.Text})

	ctx.SetWindowCoordinateMatrices(cb)
	td.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// NotesViewPane

type NotesViewPane struct {
	FontIdentifier FontIdentifier
	font           *Font

	expanded  map[*NotesNode]interface{}
	scrollbar *ScrollBar

	cb CommandBuffer
}

func NewNotesViewPane() *NotesViewPane {
	font := GetDefaultFont()
	return &NotesViewPane{
		FontIdentifier: font.id,
		font:           font,
		scrollbar:      NewScrollBar(4, false),
		expanded:       make(map[*NotesNode]interface{})}
}

func (nv *NotesViewPane) Activate(cs *ColorScheme) {
	if nv.font = GetFont(nv.FontIdentifier); nv.font == nil {
		nv.font = GetDefaultFont()
		nv.FontIdentifier = nv.font.id
	}
	nv.expanded = make(map[*NotesNode]interface{})
	nv.scrollbar = NewScrollBar(4, false)
}

func (nv *NotesViewPane) Deactivate() {}

func (nv *NotesViewPane) CanTakeKeyboardFocus() bool { return false }

func (nv *NotesViewPane) Duplicate(nameAsCopy bool) Pane {
	return &NotesViewPane{
		FontIdentifier: nv.FontIdentifier,
		font:           nv.font,
		expanded:       make(map[*NotesNode]interface{}),
		scrollbar:      NewScrollBar(4, false),
	}
}

func (nv *NotesViewPane) DrawUI() {
	if newFont, changed := DrawFontPicker(&nv.FontIdentifier, "Font"); changed {
		nv.font = newFont
	}
}

func (nv *NotesViewPane) Name() string { return "Notes View" }

func (nv *NotesViewPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	nv.cb.Reset()
	ctx.SetWindowCoordinateMatrices(&nv.cb)

	textStyle := TextStyle{Font: nv.font, Color: ctx.cs.Text}
	headerStyle := TextStyle{Font: nv.font, Color: ctx.cs.TextHighlight}

	scrollOffset, scrollbarWidth := nv.scrollbar.Offset(), float32(nv.scrollbar.Width())

	edgeSpace := nv.font.size / 2
	lineHeight := nv.font.size + 1
	visibleLines := (int(ctx.paneExtent.Height()) - edgeSpace) / lineHeight

	y0 := int(ctx.paneExtent.Height()) - edgeSpace
	y0 += scrollOffset * lineHeight
	y := y0

	spaceWidth, _ := nv.font.BoundText(" ", 0)
	columns := int(ctx.paneExtent.Width()-float32(2*edgeSpace)-scrollbarWidth) / spaceWidth
	nLines := 0
	var draw func(*NotesNode, int)
	draw = func(node *NotesNode, depth int) {
		if node == nil {
			return
		}

		indent := edgeSpace + (depth-1)*spaceWidth
		if node.title != "" {
			_, expanded := nv.expanded[node]

			hovered := func() bool {
				return ctx.mouse != nil && ctx.mouse.Pos[1] < float32(y) && ctx.mouse.Pos[1] >= float32(y-lineHeight)
			}
			mouseReleased := func() bool {
				return hovered() && ctx.mouse.Released[0]
			}

			if hovered() {
				rect := LinesDrawBuilder{}
				width := ctx.paneExtent.Width() - scrollbarWidth - 3*float32(edgeSpace)
				rect.AddPolyline([2]float32{float32(edgeSpace) / 2, float32(y)},
					[][2]float32{[2]float32{0, 0},
						[2]float32{width, 0},
						[2]float32{width, float32(-lineHeight)},
						[2]float32{0, float32(-lineHeight)}})
				nv.cb.SetRGB(ctx.cs.Text)
				rect.GenerateCommands(&nv.cb)
			}
			if mouseReleased() {
				if expanded {
					delete(nv.expanded, node)
				} else {
					nv.expanded[node] = nil
				}
				expanded = !expanded
			}

			title := ""
			if expanded {
				title = FontAwesomeIconCaretDown + node.title
			} else {
				title = FontAwesomeIconCaretRight + node.title
			}
			text, lines := wrapText(title, columns, 4, false)
			nLines += lines
			td.AddText(text, [2]float32{float32(indent), float32(y)}, headerStyle)
			y -= lines * lineHeight

			if !expanded {
				return
			}
		}
		for _, line := range node.text {
			text, lines := wrapText(line, columns, 4, false)
			nLines += lines
			td.AddText(text, [2]float32{float32(indent), float32(y)}, textStyle)
			y -= lines * lineHeight
		}
		for _, child := range node.children {
			draw(child, depth+1)
		}
	}
	draw(globalConfig.notesRoot, 0)

	nv.scrollbar.Draw(ctx, &nv.cb)
	nv.scrollbar.Update(nLines, visibleLines, ctx)

	td.GenerateCommands(&nv.cb)

	cb.Call(nv.cb)
}

///////////////////////////////////////////////////////////////////////////
// PerformancePane

type PerformancePane struct {
	disableVSync bool

	nFrames        uint64
	initialMallocs uint64

	// exponential averages of various time measurements (in ms)
	drawPanes float32
	drawImgui float32

	FontIdentifier FontIdentifier
	font           *Font

	// In order to measure frames per second over the last few seconds, we
	// start by maintaining two one-second time intervals [a,a+1] and
	// [a+1,a+2] (where numbers are seconds).  When a frame is drawn, a
	// corresponding per-interval counter is incremented.  Thus,
	// As long as the current time is in the full interval [a,a+2], then we
	// can estimate fps as the sum of the two counts divided by the elapsed
	// time since a.
	//
	// When the current time passes a+2, we discard the count for the first
	// interval, replacing it with the second count before zeroing the second
	// count.  The time a is then advanced by one second.
	//
	// In the implementation, we only store the starting time.
	frameCountStart time.Time
	framesCount     [2]int
}

func NewPerformancePane() *PerformancePane {
	font := GetDefaultFont()
	return &PerformancePane{FontIdentifier: font.id, font: font}
}

func (pp *PerformancePane) Duplicate(nameAsCopy bool) Pane {
	return &PerformancePane{FontIdentifier: pp.FontIdentifier, font: pp.font}
}

func (pp *PerformancePane) Activate(cs *ColorScheme) {
	if pp.font = GetFont(pp.FontIdentifier); pp.font == nil {
		pp.font = GetDefaultFont()
		lg.Printf("want %+v got %+v", pp.FontIdentifier, pp.font)
		pp.FontIdentifier = pp.font.id
	}
	pp.frameCountStart = time.Now()
	pp.framesCount[0] = 0
	pp.framesCount[1] = 0
}

func (pp *PerformancePane) Deactivate()                {}
func (pp *PerformancePane) CanTakeKeyboardFocus() bool { return false }

func (pp *PerformancePane) Name() string { return "Performance Information" }

func (pp *PerformancePane) DrawUI() {
	if newFont, changed := DrawFontPicker(&pp.FontIdentifier, "Font"); changed {
		pp.font = newFont
	}
	if imgui.Checkbox("Disable vsync", &pp.disableVSync) {
		platform.EnableVSync(!pp.disableVSync)
	}
}

func (pp *PerformancePane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	const initialFrames = 10

	pp.nFrames++

	var perf strings.Builder
	perf.Grow(512)

	// First report the framerate
	now := time.Now()
	if now.Before(pp.frameCountStart.Add(1 * time.Second)) {
		pp.framesCount[0]++
	} else if now.Before(pp.frameCountStart.Add(2 * time.Second)) {
		pp.framesCount[1]++
	} else {
		// roll them over
		pp.framesCount[0] = pp.framesCount[1]
		pp.framesCount[1] = 0
		pp.frameCountStart = pp.frameCountStart.Add(time.Second)
	}
	fps := float64(pp.framesCount[0]+pp.framesCount[1]) / time.Since(pp.frameCountStart).Seconds()
	perf.WriteString(fmt.Sprintf("Redraws per second: %.1f", fps))

	// Runtime breakdown
	update := func(d time.Duration, stat *float32) float32 {
		dms := float32(d.Microseconds()) / 1000. // duration in ms
		*stat = .97**stat + .03*dms
		return *stat
	}
	perf.WriteString(fmt.Sprintf("\ndraw panes %.2fms draw gui %.2fms",
		update(stats.drawPanes, &pp.drawPanes),
		update(stats.drawImgui, &pp.drawImgui)))

	// Memory stats
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	if pp.nFrames == initialFrames {
		pp.initialMallocs = mem.Mallocs
	}
	mallocsPerFrame := uint64(0)
	if pp.nFrames > initialFrames {
		mallocsPerFrame = (mem.Mallocs - pp.initialMallocs) / (pp.nFrames - initialFrames)
	}
	active1000s := (mem.Mallocs - mem.Frees) / 1000
	perf.WriteString(fmt.Sprintf("\nMallocs/frame %d (%dk active) %d MB in use",
		mallocsPerFrame, active1000s, mem.HeapAlloc/(1024*1024)))

	// Rendering stats
	perf.WriteString("\n" + stats.render.String())

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	sz2 := float32(pp.font.size) / 2
	td.AddText(perf.String(), [2]float32{sz2, ctx.paneExtent.Height() - sz2},
		TextStyle{Font: pp.font, Color: ctx.cs.Text})

	ctx.SetWindowCoordinateMatrices(cb)
	td.GenerateCommands(cb)
}

///////////////////////////////////////////////////////////////////////////
// ReminderPane

type ReminderPane struct {
	FontIdentifier FontIdentifier
	font           *Font

	cb CommandBuffer
}

type ReminderItem interface {
	Draw(text func(s string, color RGB), ctx *PaneContext)
}

type TimerReminderItem struct {
	end      time.Time
	note     string
	lastBeep time.Time
}

func (t *TimerReminderItem) Draw(text func(s string, color RGB), ctx *PaneContext) {
	now := time.Now()
	if now.After(t.end) {
		// Beep every 15s until cleared
		if now.Sub(t.lastBeep) > 15*time.Second {
			globalConfig.AudioSettings.HandleEvent(AudioEventTimerFinished)
			t.lastBeep = now
		}

		flashcycle := now.Second()
		if flashcycle&1 == 0 {
			text("--:-- ", ctx.cs.TextHighlight)
		} else {
			text("      ", ctx.cs.Text)
		}
	} else {
		remaining := t.end.Sub(now)
		remaining = remaining.Round(time.Second)
		minutes := int(remaining.Minutes())
		remaining -= time.Duration(minutes) * time.Minute
		seconds := int(remaining.Seconds())
		text(fmt.Sprintf("%02d:%02d ", minutes, seconds), ctx.cs.Text)
	}
	text(t.note, ctx.cs.Text)
}

type ToDoReminderItem struct {
	note string
}

func (t *ToDoReminderItem) Draw(text func(s string, color RGB), ctx *PaneContext) {
	text(t.note, ctx.cs.Text)
}

func NewReminderPane() *ReminderPane {
	font := GetDefaultFont()
	return &ReminderPane{FontIdentifier: font.id, font: font}
}

func (rp *ReminderPane) Duplicate(nameAsCopy bool) Pane {
	return &ReminderPane{FontIdentifier: rp.FontIdentifier, font: rp.font}
}

func (rp *ReminderPane) Activate(cs *ColorScheme) {
	if rp.font = GetFont(rp.FontIdentifier); rp.font == nil {
		rp.font = GetDefaultFont()
		rp.FontIdentifier = rp.font.id
	}
}

func (rp *ReminderPane) Deactivate()                {}
func (rp *ReminderPane) CanTakeKeyboardFocus() bool { return false }
func (rp *ReminderPane) Name() string               { return "Reminders" }

func (rp *ReminderPane) DrawUI() {
	if newFont, changed := DrawFontPicker(&rp.FontIdentifier, "Font"); changed {
		rp.font = newFont
	}
}

func (rp *ReminderPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	// We're not using imgui, so we have to handle hovered and clicked by
	// ourselves.  Here are the key quantities:
	indent := int(rp.font.size / 2) // left and top spacing
	checkWidth, _ := rp.font.BoundText(FontAwesomeIconSquare, 0)
	spaceWidth := int(rp.font.LookupGlyph(' ').AdvanceX)
	textIndent := indent + checkWidth + spaceWidth

	lineHeight := rp.font.size + 2
	// Current cursor position
	x, y := textIndent, int(ctx.paneExtent.Height())-indent

	// Initialize the command buffer before we get going.
	rp.cb.Reset()
	ctx.SetWindowCoordinateMatrices(&rp.cb)

	text := func(s string, color RGB) {
		td := GetTextDrawBuilder()
		defer ReturnTextDrawBuilder(td)
		td.AddText(s, [2]float32{float32(x), float32(y)}, TextStyle{Font: rp.font, Color: color})
		td.GenerateCommands(&rp.cb)

		bx, _ := rp.font.BoundText(s, 0)
		x += bx
	}
	hovered := func() bool {
		return ctx.mouse != nil && ctx.mouse.Pos[1] < float32(y) && ctx.mouse.Pos[1] >= float32(y-lineHeight)
	}
	buttonDown := func() bool {
		return hovered() && ctx.mouse.Down[0]
	}
	released := func() bool {
		return hovered() && ctx.mouse.Released[0]
	}

	var items []ReminderItem
	for i := range positionConfig.timers {
		items = append(items, &positionConfig.timers[i])
	}
	for i := range positionConfig.todos {
		items = append(items, &positionConfig.todos[i])
	}

	removeItem := len(items) // invalid -> remove nothing
	for i, item := range items {
		if hovered() {
			// Draw the selection box; we want this for both hovered() and
			// buttonDown(), so handle it separately. (Note that
			// buttonDown() implies hovered().)
			rect := LinesDrawBuilder{}
			width := ctx.paneExtent.Width()
			rect.AddPolyline([2]float32{float32(indent) / 2, float32(y)},
				[][2]float32{[2]float32{0, 0},
					[2]float32{width - float32(indent), 0},
					[2]float32{width - float32(indent), float32(-lineHeight)},
					[2]float32{0, float32(-lineHeight)}})
			rp.cb.SetRGB(ctx.cs.Text)
			rect.GenerateCommands(&rp.cb)
		}

		// Draw a suitable box
		x = indent
		if buttonDown() {
			text(FontAwesomeIconCheckSquare, ctx.cs.Text)
		} else {
			text(FontAwesomeIconSquare, ctx.cs.Text)
		}

		if released() {
			removeItem = i
		}

		x = textIndent
		item.Draw(text, ctx)
		y -= lineHeight
	}

	if removeItem < len(positionConfig.timers) {
		if removeItem == 0 {
			positionConfig.timers = positionConfig.timers[1:]
		} else {
			positionConfig.timers = append(positionConfig.timers[:removeItem],
				positionConfig.timers[removeItem+1:]...)
		}
	} else {
		removeItem -= len(positionConfig.timers)
		if removeItem < len(positionConfig.todos) {
			if removeItem == 0 {
				positionConfig.todos = positionConfig.todos[1:]
			} else {
				positionConfig.todos = append(positionConfig.todos[:removeItem],
					positionConfig.todos[removeItem+1:]...)
			}
		}
	}

	cb.Call(rp.cb)
}

///////////////////////////////////////////////////////////////////////////
// FlightStripPane

type FlightStripPane struct {
	FontIdentifier FontIdentifier
	font           *Font

	AutoAddDepartures         bool
	AutoAddArrivals           bool
	AddPushed                 bool
	CollectDeparturesArrivals bool

	Airports      map[string]interface{}
	strips        []string // callsigns
	addedAircraft map[string]interface{}

	mouseDragging       bool
	lastMousePos        [2]float32
	selectedStrip       int
	selectedAnnotation  int
	annotationCursorPos int

	eventsId  EventSubscriberId
	scrollbar *ScrollBar
}

func NewFlightStripPane() *FlightStripPane {
	font := GetDefaultFont()
	return &FlightStripPane{
		FontIdentifier:            font.id,
		font:                      font,
		AddPushed:                 true,
		CollectDeparturesArrivals: true,
		Airports:                  make(map[string]interface{}),
		addedAircraft:             make(map[string]interface{}),
		selectedStrip:             -1,
		selectedAnnotation:        -1,
		eventsId:                  eventStream.Subscribe(),
		scrollbar:                 NewScrollBar(4, true),
	}
}

func (fsp *FlightStripPane) Duplicate(nameAsCopy bool) Pane {
	return &FlightStripPane{
		FontIdentifier:            fsp.FontIdentifier,
		font:                      fsp.font,
		AutoAddDepartures:         fsp.AutoAddDepartures,
		AutoAddArrivals:           fsp.AutoAddArrivals,
		AddPushed:                 fsp.AddPushed,
		CollectDeparturesArrivals: fsp.CollectDeparturesArrivals,
		Airports:                  DuplicateMap(fsp.Airports),
		strips:                    DuplicateSlice(fsp.strips),
		addedAircraft:             DuplicateMap(fsp.addedAircraft),
		selectedStrip:             -1,
		selectedAnnotation:        -1,
		eventsId:                  eventStream.Subscribe(),
		scrollbar:                 NewScrollBar(4, true),
	}
}

func (fsp *FlightStripPane) Activate(cs *ColorScheme) {
	if fsp.font = GetFont(fsp.FontIdentifier); fsp.font == nil {
		fsp.font = GetDefaultFont()
		fsp.FontIdentifier = fsp.font.id
	}
	if fsp.addedAircraft == nil {
		fsp.addedAircraft = make(map[string]interface{})
	}
	if fsp.Airports == nil {
		fsp.Airports = make(map[string]interface{})
	}
	if fsp.scrollbar == nil {
		fsp.scrollbar = NewScrollBar(4, true)
	}
	fsp.eventsId = eventStream.Subscribe()
}

func (fsp *FlightStripPane) Deactivate() {
	eventStream.Unsubscribe(fsp.eventsId)
	fsp.eventsId = InvalidEventSubscriberId
}

func (fsp *FlightStripPane) isDeparture(ac *Aircraft) bool {
	if ac.flightPlan == nil {
		return false
	}
	_, ok := fsp.Airports[ac.flightPlan.DepartureAirport]
	return ok
}

func (fsp *FlightStripPane) isArrival(ac *Aircraft) bool {
	if ac.flightPlan == nil {
		return false
	}
	_, ok := fsp.Airports[ac.flightPlan.ArrivalAirport]
	return ok
}

func (fsp *FlightStripPane) CanTakeKeyboardFocus() bool { return true }

func (fsp *FlightStripPane) processEvents(es *EventStream) {
	possiblyAdd := func(ac *Aircraft) {
		callsign := ac.Callsign()
		if _, ok := fsp.addedAircraft[callsign]; ok {
			return
		}

		if ac.flightPlan == nil {
			return
		}
		if fsp.AutoAddDepartures && fsp.isDeparture(ac) {
			fsp.strips = append(fsp.strips, callsign)
			fsp.addedAircraft[callsign] = nil
		} else if fsp.AutoAddArrivals && fsp.isArrival(ac) {
			fsp.strips = append(fsp.strips, callsign)
			fsp.addedAircraft[callsign] = nil
		}
	}

	for _, event := range es.Get(fsp.eventsId) {
		switch v := event.(type) {
		case *PushedFlightStripEvent:
			if Find(fsp.strips, v.callsign) == -1 {
				fsp.strips = append(fsp.strips, v.callsign)
			}
		case *AddedAircraftEvent:
			possiblyAdd(v.ac)
		case *ModifiedAircraftEvent:
			possiblyAdd(v.ac)
		case *RemovedAircraftEvent:
			// Thus, if we later see the same callsign from someone else, we'll
			// treat them as new.
			delete(fsp.addedAircraft, v.ac.Callsign())
			fsp.strips = FilterSlice(fsp.strips, func(callsign string) bool { return callsign != v.ac.Callsign() })
		}
	}

	// TODO: is this needed? Shouldn't there be a RemovedAircraftEvent?
	fsp.strips = FilterSlice(fsp.strips, func(callsign string) bool {
		ac := server.GetAircraft(callsign)
		return ac != nil
	})

	if fsp.CollectDeparturesArrivals {
		isDeparture := func(callsign string) bool {
			if ac := server.GetAircraft(callsign); ac == nil {
				return false
			} else {
				return fsp.isDeparture(ac)
			}
		}
		dep := FilterSlice(fsp.strips, isDeparture)
		arr := FilterSlice(fsp.strips, func(callsign string) bool { return !isDeparture(callsign) })

		fsp.strips = fsp.strips[:0]
		fsp.strips = append(fsp.strips, dep...)
		fsp.strips = append(fsp.strips, arr...)
	}
}

func (fsp *FlightStripPane) Name() string { return "Flight Strips" }

func (fsp *FlightStripPane) DrawUI() {
	fsp.Airports = drawAirportSelector(fsp.Airports, "Airports")
	imgui.Checkbox("Automatically add departures", &fsp.AutoAddDepartures)
	imgui.Checkbox("Automatically add arrivals", &fsp.AutoAddArrivals)
	imgui.Checkbox("Add pushed flight strips", &fsp.AddPushed)
	imgui.Checkbox("Collect departures and arrivals together", &fsp.CollectDeparturesArrivals)

	if newFont, changed := DrawFontPicker(&fsp.FontIdentifier, "Font"); changed {
		fsp.font = newFont
	}
}

func (fsp *FlightStripPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	fsp.processEvents(ctx.events)

	// Font width and height
	bx, _ := fsp.font.BoundText(" ", 0)
	fw, fh := float32(bx), float32(fsp.font.size)

	ctx.SetWindowCoordinateMatrices(cb)

	// 4 lines of text, 2 lines on top and below for padding, 1 pixel separator line
	vpad := float32(2)
	stripHeight := 1 + 2*vpad + 4*fh

	visibleStrips := int(ctx.paneExtent.Height() / stripHeight)
	fsp.scrollbar.Update(len(fsp.strips), visibleStrips, ctx)

	indent := float32(int32(fw / 2))
	// column widths
	width0 := 10 * fw
	width1 := 6 * fw
	width2 := 5 * fw
	widthAnn := 5 * fw

	widthCenter := ctx.paneExtent.Width() - width0 - width1 - width2 - 3*widthAnn
	if fsp.scrollbar.Visible() {
		widthCenter -= float32(fsp.scrollbar.Width())
	}
	if widthCenter < 0 {
		// not sure what to do if it comes to this...
		widthCenter = 20 * fw
	}

	drawWidth := ctx.paneExtent.Width()
	if fsp.scrollbar.Visible() {
		drawWidth -= float32(fsp.scrollbar.Width())
	}

	// This can happen if, for example, the last aircraft is selected and
	// then another one is removed. It might be better if selectedAircraft
	// was a pointer to something that FlightStripPane managed, so that
	// this sort of case would be handled more naturally... (And note that
	// tracking the callsign won't work if we want to have strips for the
	// same aircraft twice in a pane, for what that's worth...)
	if fsp.selectedStrip >= len(fsp.strips) {
		fsp.selectedStrip = len(fsp.strips) - 1
	}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	ld := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(ld)
	selectionLd := GetLinesDrawBuilder()
	defer ReturnLinesDrawBuilder(selectionLd)

	// Draw from the bottom
	scrollOffset := fsp.scrollbar.Offset()
	y := stripHeight - 1 - vpad
	for i := scrollOffset; i < min(len(fsp.strips), visibleStrips+scrollOffset+1); i++ {
		callsign := fsp.strips[i]
		strip := server.GetFlightStrip(callsign)
		ac := server.GetAircraft(callsign)
		if ac == nil {
			lg.Errorf("%s: no aircraft for callsign?!", strip.callsign)
			continue
		}
		fp := ac.flightPlan

		style := TextStyle{Font: fsp.font, Color: ctx.cs.Text}
		if positionConfig.selectedAircraft != nil && positionConfig.selectedAircraft.Callsign() == callsign {
			style.Color = ctx.cs.TextHighlight
		}

		// Draw background quad for this flight strip
		qb := GetColoredTrianglesDrawBuilder()
		defer ReturnColoredTrianglesDrawBuilder(qb)
		bgColor := func() RGB {
			if fsp.isDeparture(ac) {
				return ctx.cs.DepartureStrip
			} else {
				return ctx.cs.ArrivalStrip
			}
		}()
		y0, y1 := y+1+vpad-stripHeight, y+1+vpad
		qb.AddQuad([2]float32{0, y0}, [2]float32{drawWidth, y0}, [2]float32{drawWidth, y1}, [2]float32{0, y1}, bgColor)
		qb.GenerateCommands(cb)

		x := indent

		// First column; 3 entries
		td.AddText(callsign, [2]float32{x, y}, style)
		if fp != nil {
			td.AddText(fp.AircraftType, [2]float32{x, y - fh*3/2}, style)
			td.AddText(fp.Rules.String(), [2]float32{x, y - fh*3}, style)
		}
		ld.AddLine([2]float32{width0, y}, [2]float32{width0, y - stripHeight})

		// Second column; 3 entries
		x += width0
		td.AddText(ac.assignedSquawk.String(), [2]float32{x, y}, style)
		td.AddText(fmt.Sprintf("%d", ac.tempAltitude), [2]float32{x, y - fh*3/2}, style)
		if fp != nil {
			td.AddText(fmt.Sprintf("%d", fp.Altitude), [2]float32{x, y - fh*3}, style)
		}
		ld.AddLine([2]float32{width0, y - 4./3.*fh}, [2]float32{width0 + width1, y - 4./3.*fh})
		ld.AddLine([2]float32{width0, y - 8./3.*fh}, [2]float32{width0 + width1, y - 8./3.*fh})
		ld.AddLine([2]float32{width0 + width1, y}, [2]float32{width0 + width1, y - stripHeight})

		// Third column; (up to) 4 entries
		x += width1
		if fp != nil {
			td.AddText(fp.DepartureAirport, [2]float32{x, y}, style)
			td.AddText(fp.ArrivalAirport, [2]float32{x, y - fh}, style)
			td.AddText(fp.AlternateAirport, [2]float32{x, y - 2*fh}, style)
		}
		td.AddText(ac.scratchpad, [2]float32{x, y - 3*fh}, style)
		ld.AddLine([2]float32{width0 + width1 + width2, y},
			[2]float32{width0 + width1 + width2, y - stripHeight})

		// Fourth column: route and remarks
		x += width2
		if fp != nil {
			cols := int(widthCenter / fw)
			// Line-wrap the route to fit the box and break it into lines.
			route, _ := wrapText(fp.Route, cols, 2 /* indent */, true)
			text := strings.Split(route, "\n")
			// Add a blank line if the route only used one line.
			if len(text) < 2 {
				text = append(text, "")
			}
			// Similarly for the remarks
			remarks, _ := wrapText(fp.Remarks, cols, 2 /* indent */, true)
			text = append(text, strings.Split(remarks, "\n")...)
			// Limit to the first four lines so we don't spill over.
			if len(text) > 4 {
				text = text[:4]
			}
			// Truncate all lines to the column limit; wrapText() lets things
			// spill over if it's unable to break a long word by itself on a
			// line, for example.
			for i, line := range text {
				if len(line) > cols {
					text[i] = text[i][:cols]
				}
			}
			td.AddText(strings.Join(text, "\n"), [2]float32{x, y}, style)
		}

		// Annotations
		x += widthCenter
		var editResult int
		for ai, ann := range strip.annotations {
			ix, iy := ai%3, ai/3
			xp, yp := x+float32(ix)*widthAnn+indent, y-float32(iy)*1.5*fh

			if ctx.haveFocus && fsp.selectedStrip == i && ai == fsp.selectedAnnotation {
				// If were currently editing this annotation, don't draw it
				// normally but instead draw it including a cursor, update
				// it according to keyboard input, etc.
				cursorStyle := TextStyle{Font: fsp.font, Color: bgColor,
					DrawBackground: true, BackgroundColor: style.Color}
				editResult, _ = uiDrawTextEdit(&strip.annotations[fsp.selectedAnnotation], &fsp.annotationCursorPos,
					ctx.keyboard, [2]float32{xp, yp}, style, cursorStyle, cb)
				if len(strip.annotations[fsp.selectedAnnotation]) >= 3 {
					// Limit it to three characters
					strip.annotations[fsp.selectedAnnotation] = strip.annotations[fsp.selectedAnnotation][:3]
					fsp.annotationCursorPos = min(fsp.annotationCursorPos, len(strip.annotations[fsp.selectedAnnotation]))
				}
			} else {
				td.AddText(ann, [2]float32{xp, yp}, style)
			}
		}

		// Only process this after drawing all of the annotations since
		// otherwise we can end up with cascading tabbing ahead and the
		// like.
		switch editResult {
		case TextEditReturnNone, TextEditReturnTextChanged:
			// nothing to do
		case TextEditReturnEnter:
			fsp.selectedStrip = -1
			wmReleaseKeyboardFocus()
		case TextEditReturnNext:
			fsp.selectedAnnotation = (fsp.selectedAnnotation + 1) % 9
			fsp.annotationCursorPos = len(strip.annotations[fsp.selectedAnnotation])
		case TextEditReturnPrev:
			// +8 rather than -1 to keep it positive for the mod...
			fsp.selectedAnnotation = (fsp.selectedAnnotation + 8) % 9
			fsp.annotationCursorPos = len(strip.annotations[fsp.selectedAnnotation])
		}

		// Horizontal lines
		ld.AddLine([2]float32{x, y - 4./3.*fh}, [2]float32{drawWidth, y - 4./3.*fh})
		ld.AddLine([2]float32{x, y - 8./3.*fh}, [2]float32{drawWidth, y - 8./3.*fh})
		// Vertical lines
		for i := 0; i < 3; i++ {
			xp := x + float32(i)*widthAnn
			ld.AddLine([2]float32{xp, y}, [2]float32{xp, y - stripHeight})
		}

		// Line at the top
		yl := y + 1 + vpad
		ld.AddLine([2]float32{0, yl}, [2]float32{drawWidth, yl})

		y += stripHeight
	}

	// Handle selection, deletion, and reordering
	if ctx.mouse != nil {
		// Ignore clicks if the mouse is over the scrollbar (and it's being drawn)
		if ctx.mouse.Clicked[MouseButtonPrimary] && ctx.mouse.Pos[0] <= drawWidth {
			// from the bottom
			stripIndex := int(ctx.mouse.Pos[1] / stripHeight)
			stripIndex += scrollOffset
			if stripIndex < len(fsp.strips) {
				io := imgui.CurrentIO()
				if io.KeyShiftPressed() {
					// delete the flight strip
					copy(fsp.strips[stripIndex:], fsp.strips[stripIndex+1:])
					fsp.strips = fsp.strips[:len(fsp.strips)-1]
				} else {
					// select the aircraft
					callsign := fsp.strips[stripIndex]
					positionConfig.selectedAircraft = server.GetAircraft(callsign)
				}
			}
		}
		if ctx.mouse.Dragging[MouseButtonPrimary] {
			fsp.mouseDragging = true
			fsp.lastMousePos = ctx.mouse.Pos

			// Offset so that the selection region is centered over the
			// line between two strips; the index then is to the lower one.
			splitIndex := int(ctx.mouse.Pos[1]/stripHeight + 0.5)
			yl := float32(splitIndex) * stripHeight
			selectionLd.AddLine([2]float32{0, yl}, [2]float32{drawWidth, yl})
		}
	}
	if fsp.mouseDragging && (ctx.mouse == nil || !ctx.mouse.Dragging[MouseButtonPrimary]) {
		fsp.mouseDragging = false

		if positionConfig.selectedAircraft == nil {
			lg.Printf("No selected aircraft for flight strip drag?!")
		} else {
			// Figure out the index for the selected aircraft.
			selectedIndex := func() int {
				callsign := positionConfig.selectedAircraft.Callsign()
				for i, fs := range fsp.strips {
					if fs == callsign {
						return i
					}
				}
				lg.Printf("Couldn't find %s in flight strips?!", callsign)
				return -1
			}()

			// The selected aircraft was set from the original mouse down so
			// now we just need to move it to be in the right place given where
			// the button was released.
			destinationIndex := int(fsp.lastMousePos[1]/stripHeight + 0.5)
			destinationIndex += scrollOffset
			destinationIndex = clamp(destinationIndex, 0, len(fsp.strips))

			if selectedIndex != -1 && selectedIndex != destinationIndex {
				// First remove it from the slice
				fs := fsp.strips[selectedIndex]
				fsp.strips = append(fsp.strips[:selectedIndex], fsp.strips[selectedIndex+1:]...)

				if selectedIndex < destinationIndex {
					destinationIndex--
				}

				// And stuff it in there
				fin := fsp.strips[destinationIndex:]
				fsp.strips = append([]string{}, fsp.strips[:destinationIndex]...)
				fsp.strips = append(fsp.strips, fs)
				fsp.strips = append(fsp.strips, fin...)
			}
		}
	}
	// Take focus if the user clicks in the annotations
	if ctx.mouse != nil && ctx.mouse.Clicked[MouseButtonPrimary] {
		annotationStartX := drawWidth - 3*widthAnn
		if xp := ctx.mouse.Pos[0]; xp >= annotationStartX && xp < drawWidth {
			stripIndex := int(ctx.mouse.Pos[1]/stripHeight) + scrollOffset
			if stripIndex < len(fsp.strips) {
				wmTakeKeyboardFocus(fsp, true)
				fsp.selectedStrip = stripIndex

				// Figure out which annotation was selected
				xa := int(ctx.mouse.Pos[0]-annotationStartX) / int(widthAnn)
				ya := 2 - (int(ctx.mouse.Pos[1])%int(stripHeight))/(int(stripHeight)/3)
				xa, ya = clamp(xa, 0, 2), clamp(ya, 0, 2) // just in case
				fsp.selectedAnnotation = 3*ya + xa

				callsign := fsp.strips[fsp.selectedStrip]
				strip := server.GetFlightStrip(callsign)
				fsp.annotationCursorPos = len(strip.annotations[fsp.selectedAnnotation])
			}
		}
	}
	fsp.scrollbar.Draw(ctx, cb)

	cb.SetRGB(ctx.cs.UIControl)
	cb.LineWidth(1)
	ld.GenerateCommands(cb)
	td.GenerateCommands(cb)

	cb.SetRGB(ctx.cs.TextHighlight)
	cb.LineWidth(3)
	selectionLd.GenerateCommands(cb)
}
