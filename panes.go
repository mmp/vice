// panes.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mmp/imgui-go/v4"
)

// Panes (should) mostly operate in window coordinates: (0,0) is lower
// left, just in their own pane, oblivious to the full window size.  Higher
// level code will handle positioning the panes in the main window.
type Pane interface {
	Name() string

	Duplicate(nameAsCopy bool) Pane

	Activate()
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

	thumbnail bool
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
	cb.LoadProjectionMatrix(Identity3x3().Ortho(0, w, 0, h))
	cb.LoadModelViewMatrix(Identity3x3())
}

// Helper function to unmarshal the JSON of a Pane of a given type T.
func unmarshalPaneHelper[T Pane](data []byte) (Pane, error) {
	var p T
	err := json.Unmarshal(data, &p)
	return p, err
}

func unmarshalPane(paneType string, data []byte) (Pane, error) {
	switch paneType {
	case "":
		// nil pane
		return nil, nil

	case "*main.AirportInfoPane":
		return unmarshalPaneHelper[*AirportInfoPane](data)

	case "*main.CLIPane":
		return unmarshalPaneHelper[*CLIPane](data)

	case "*main.EmptyPane":
		return unmarshalPaneHelper[*EmptyPane](data)

	case "*main.FlightPlanPane":
		return unmarshalPaneHelper[*FlightPlanPane](data)

	case "*main.FlightStripPane":
		return unmarshalPaneHelper[*FlightStripPane](data)

	case "*main.NotesViewPane":
		return unmarshalPaneHelper[*NotesViewPane](data)

	case "*main.ImageViewPane":
		return unmarshalPaneHelper[*ImageViewPane](data)

	case "*main.PerformancePane":
		return unmarshalPaneHelper[*PerformancePane](data)

	case "*main.RadarScopePane":
		return unmarshalPaneHelper[*RadarScopePane](data)

	case "*main.ReminderPane":
		return unmarshalPaneHelper[*ReminderPane](data)

	case "*main.STARSPane":
		return unmarshalPaneHelper[*STARSPane](data)

	case "*main.TabbedPane":
		return unmarshalPaneHelper[*TabbedPane](data)

	default:
		lg.Errorf("%s: Unhandled type in config file", paneType)
		return NewEmptyPane(), nil // don't crash at least
	}
}

///////////////////////////////////////////////////////////////////////////
// AirportInfoPane

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

	lastATIS       map[string][]ATIS
	seenDepartures map[string]interface{}
	seenArrivals   map[string]interface{}

	FontIdentifier FontIdentifier
	font           *Font

	eventsId EventSubscriberId

	sb *ScrollBar
	cb CommandBuffer
}

func NewAirportInfoPane() *AirportInfoPane {
	// Reasonable (I hope) defaults...
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
	}
}

func (a *AirportInfoPane) Duplicate(nameAsCopy bool) Pane {
	dupe := *a
	dupe.Airports = DuplicateMap(a.Airports)
	dupe.lastATIS = DuplicateMap(a.lastATIS)
	dupe.seenDepartures = DuplicateMap(a.seenDepartures)
	dupe.seenArrivals = DuplicateMap(a.seenArrivals)
	dupe.eventsId = eventStream.Subscribe()
	dupe.sb = NewScrollBar(4, false)
	dupe.cb = CommandBuffer{}
	return &dupe
}

func (a *AirportInfoPane) Activate() {
	if a.font = GetFont(a.FontIdentifier); a.font == nil {
		a.font = GetDefaultFont()
		a.FontIdentifier = a.font.id
	}
	if a.lastATIS == nil {
		a.lastATIS = make(map[string][]ATIS)
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
	for ap := range a.Airports {
		server.AddAirportForWeather(ap)
	}
	a.eventsId = eventStream.Subscribe()
}

func (a *AirportInfoPane) Deactivate() {
	eventStream.Unsubscribe(a.eventsId)
	a.eventsId = InvalidEventSubscriberId
}

func (a *AirportInfoPane) Name() string {
	n := "Airport Information"
	if len(a.Airports) > 0 {
		n += ": " + strings.Join(SortedMapKeys(a.Airports), ",")
	}
	return n
}

func (a *AirportInfoPane) DrawUI() {
	var changed bool
	if a.Airports, changed = drawAirportSelector(a.Airports, "Airports"); changed {
		for ap := range a.Airports {
			server.AddAirportForWeather(ap)
		}
	}
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

func getDistanceSortedArrivals(airports map[string]interface{}) []Arrival {
	var arr []Arrival
	now := server.CurrentTime()
	for _, ac := range server.GetFilteredAircraft(func(ac *Aircraft) bool {
		if ac.OnGround() || ac.LostTrack(now) || ac.FlightPlan == nil {
			return false
		}
		_, ok := airports[ac.FlightPlan.ArrivalAirport]
		return ok
	}) {
		pos := ac.Position()
		// Filter ones where we don't have a valid position
		if pos[0] != 0 && pos[1] != 0 {
			dist := nmdistance2ll(database.FAA.airports[ac.FlightPlan.ArrivalAirport].Location, pos)
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
	for _, event := range eventStream.Get(a.eventsId) {
		switch ev := event.(type) {
		case *NewServerConnectionEvent:
			// Let the server know which airports we want weather for.
			for ap := range a.Airports {
				server.AddAirportForWeather(ap)
			}

		case *ReceivedATISEvent:
			if _, ok := a.Airports[ev.ATIS.Airport]; ok {
				newATIS := ev.ATIS
				idx := FindIf(a.lastATIS[newATIS.Airport], func(a ATIS) bool { return a.AppDep == newATIS.AppDep })
				if idx == -1 {
					// First time we've ever seen it
					a.lastATIS[newATIS.Airport] = append(a.lastATIS[newATIS.Airport], newATIS)
				} else {
					oldATIS := a.lastATIS[newATIS.Airport][idx]
					if oldATIS.Code != newATIS.Code || oldATIS.Contents != newATIS.Contents {
						// It's an updated ATIS rather than the first time we're
						// seeing it (or a repeat), so play the notification sound,
						// if it's enabled.
						a.lastATIS[newATIS.Airport][idx] = newATIS
						globalConfig.AudioSettings.HandleEvent(AudioEventUpdatedATIS)
					}
				}
			}
		}
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
				airport := at.Airport
				if len(at.AppDep) > 0 {
					airport += "_" + at.AppDep
				}
				astr := fmt.Sprintf("  %-6s %s %s", airport, at.Code, at.Contents)
				atis = append(atis, astr)
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

	var uncleared, departures, airborne []*Aircraft
	for _, ac := range server.GetFilteredAircraft(func(ac *Aircraft) bool {
		return ac.FlightPlan != nil && !ac.LostTrack(now)
	}) {
		if _, ok := a.Airports[ac.FlightPlan.DepartureAirport]; ok {
			if ac.OnGround() {
				if ac.AssignedSquawk == 0 {
					uncleared = append(uncleared, ac)
				} else {
					departures = append(departures, ac)
				}
			} else {
				airborne = append(airborne, ac)
			}
		}
	}

	if a.ShowUncleared && len(uncleared) > 0 {
		str.WriteString("Uncleared:\n")
		sort.Slice(uncleared, func(i, j int) bool {
			return uncleared[i].Callsign < uncleared[j].Callsign
		})
		for _, ac := range uncleared {
			str.WriteString(fmt.Sprintf("  %-8s %3s %4s-%4s %8s %5d\n", ac.Callsign,
				ac.FlightPlan.Rules, ac.FlightPlan.DepartureAirport, ac.FlightPlan.ArrivalAirport,
				ac.FlightPlan.AircraftType, ac.FlightPlan.Altitude))

			// Route
			if len(ac.FlightPlan.Route) > 0 {
				str.WriteString("    ")
				str.WriteString(ac.FlightPlan.Route)
				str.WriteString("\n")
			}

			if _, ok := a.seenArrivals[ac.Callsign]; !ok {
				a.seenArrivals[ac.Callsign] = nil
				globalConfig.AudioSettings.HandleEvent(AudioEventFlightPlanFiled)
			}
		}
		str.WriteString("\n")
	}

	if a.ShowDepartures && len(departures) > 0 {
		str.WriteString("Departures:\n")
		sort.Slice(departures, func(i, j int) bool {
			return departures[i].Callsign < departures[j].Callsign
		})
		for _, ac := range departures {
			route := ac.FlightPlan.Route
			if len(route) > 10 {
				route = route[:10]
				route += ".."
			}
			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %5d %12s", ac.Callsign,
				ac.FlightPlan.Rules, ac.FlightPlan.DepartureAirport, ac.FlightPlan.AircraftType,
				ac.Scratchpad, ac.FlightPlan.Altitude, route))

			// Make sure the squawk is good
			if ac.Mode != Charlie || ac.Squawk != ac.AssignedSquawk {
				str.WriteString(" sq:")
				if ac.Mode != Charlie {
					str.WriteString("[C]")
				}
				if ac.Squawk != ac.AssignedSquawk {
					str.WriteString(ac.AssignedSquawk.String())
				}
			}
			str.WriteString("\n")
		}
		str.WriteString("\n")
	}

	writeWakeTurbulence := func(leader, follower *Aircraft) {
		if leader == nil || follower == nil {
			str.WriteString("       ")
			return
		}

		if req, err := RECATAircraftDistance(leader, follower); err != nil {
			str.WriteString("       ")
			return
		} else {
			if req == 0 { // minimum radar separation
				req = 3
			}
			d := nmdistance2ll(leader.Position(), follower.Position())
			if d > 9.9 {
				d = 9.9
			}
			if d < float32(req) {
				flush()
				style.Color = cs.TextHighlight
				str.WriteString(fmt.Sprintf("%3.1f/%dnm", d, req))
				flush()
			} else {
				str.WriteString(fmt.Sprintf("%3.1f/%dnm", d, req))
			}
		}
	}

	if a.ShowDeparted && len(airborne) > 0 {
		sort.Slice(airborne, func(i, j int) bool {
			ai := airborne[i]
			di := nmdistance2ll(database.FAA.airports[ai.FlightPlan.DepartureAirport].Location, ai.Position())
			aj := airborne[j]
			dj := nmdistance2ll(database.FAA.airports[aj.FlightPlan.DepartureAirport].Location, aj.Position())
			return di < dj
		})

		str.WriteString("Departed:\n")
		for i, ac := range airborne {
			route := ac.FlightPlan.Route
			if len(route) > 10 {
				route = route[:10]
				route += ".."
			}

			alt := ac.Altitude()
			alt = (alt + 50) / 100 * 100
			var clearedAlt string
			if ac.TempAltitude != 0 {
				clearedAlt = fmt.Sprintf("%5dT", ac.TempAltitude)
			} else {
				clearedAlt = fmt.Sprintf("%5d ", ac.FlightPlan.Altitude)
			}

			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %s %5d ", ac.Callsign,
				ac.FlightPlan.Rules, ac.FlightPlan.DepartureAirport, ac.FlightPlan.AircraftType,
				ac.Scratchpad, clearedAlt, alt))

			if i+1 < len(airborne) {
				writeWakeTurbulence(airborne[i+1], ac)
			} else {
				writeWakeTurbulence(nil, ac)
			}

			str.WriteString(fmt.Sprintf(" %12s\n", route))
		}
		str.WriteString("\n")
	}

	arrivals := getDistanceSortedArrivals(a.Airports)
	if a.ShowArrivals && len(arrivals) > 0 {
		str.WriteString("Arrivals:\n")
		for i, arr := range arrivals {
			ac := arr.aircraft
			alt := ac.Altitude()
			alt = (alt + 50) / 100 * 100

			// Try to extract the STAR from the flight plan.
			route := ac.FlightPlan.Route
			star := route[strings.LastIndex(route, " ")+1:]
			if len(star) > 7 {
				star = star[len(star)-7:]
			}

			str.WriteString(fmt.Sprintf("  %-8s %s %s %8s %3s %5d  %5d %3dnm ", ac.Callsign,
				ac.FlightPlan.Rules, ac.FlightPlan.ArrivalAirport, ac.FlightPlan.AircraftType, ac.Scratchpad,
				ac.TempAltitude, alt, int(arr.distance)))

			if i > 0 {
				writeWakeTurbulence(arrivals[i-1].aircraft, arrivals[i].aircraft)
			} else {
				writeWakeTurbulence(nil, nil)
			}

			str.WriteString(fmt.Sprintf(" %4s %s\n", ac.Squawk, star))

			if _, ok := a.seenArrivals[ac.Callsign]; !ok {
				globalConfig.AudioSettings.HandleEvent(AudioEventNewArrival)
				a.seenArrivals[ac.Callsign] = nil
			}
		}
		str.WriteString("\n")
	}

	controllers := server.GetAllControllers()
	if a.ShowControllers && len(controllers) > 0 {
		str.WriteString("Controllers:\n")
		sort.Slice(controllers, func(i, j int) bool { return controllers[i].Callsign < controllers[j].Callsign })

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

func (ep *EmptyPane) Activate()                  {}
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
	return &FlightPlanPane{}
}

func (fp *FlightPlanPane) Activate() {
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
	indent := 3 + len(ac.Callsign)
	if ac.VoiceCapability != VoiceFull {
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
	return &NotesViewPane{}
}

func (nv *NotesViewPane) Activate() {
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
	return &PerformancePane{}
}

func (pp *PerformancePane) Duplicate(nameAsCopy bool) Pane {
	return &PerformancePane{FontIdentifier: pp.FontIdentifier, font: pp.font}
}

func (pp *PerformancePane) Activate() {
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
	return &ReminderPane{}
}

func (rp *ReminderPane) Duplicate(nameAsCopy bool) Pane {
	return &ReminderPane{FontIdentifier: rp.FontIdentifier, font: rp.font}
}

func (rp *ReminderPane) Activate() {
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
	return &FlightStripPane{
		AddPushed:                 true,
		CollectDeparturesArrivals: true,
		Airports:                  make(map[string]interface{}),
		selectedStrip:             -1,
		selectedAnnotation:        -1,
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

func (fsp *FlightStripPane) Activate() {
	if fsp.font = GetFont(fsp.FontIdentifier); fsp.font == nil {
		fsp.font = GetDefaultFont()
		fsp.FontIdentifier = fsp.font.id
	}
	if fsp.addedAircraft == nil {
		fsp.addedAircraft = make(map[string]interface{})
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
	if ac.FlightPlan == nil {
		return false
	}
	_, ok := fsp.Airports[ac.FlightPlan.DepartureAirport]
	return ok
}

func (fsp *FlightStripPane) isArrival(ac *Aircraft) bool {
	if ac.FlightPlan == nil {
		return false
	}
	_, ok := fsp.Airports[ac.FlightPlan.ArrivalAirport]
	return ok
}

func (fsp *FlightStripPane) CanTakeKeyboardFocus() bool { return true }

func (fsp *FlightStripPane) processEvents(es *EventStream) {
	possiblyAdd := func(ac *Aircraft) {
		callsign := ac.Callsign
		if _, ok := fsp.addedAircraft[callsign]; ok {
			return
		}

		if ac.FlightPlan == nil {
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
			delete(fsp.addedAircraft, v.ac.Callsign)
			fsp.strips = FilterSlice(fsp.strips, func(callsign string) bool { return callsign != v.ac.Callsign })
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
	fsp.Airports, _ = drawAirportSelector(fsp.Airports, "Airports")
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
		fp := ac.FlightPlan

		style := TextStyle{Font: fsp.font, Color: ctx.cs.Text}
		if positionConfig.selectedAircraft != nil && positionConfig.selectedAircraft.Callsign == callsign {
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
		td.AddText(ac.AssignedSquawk.String(), [2]float32{x, y}, style)
		td.AddText(fmt.Sprintf("%d", ac.TempAltitude), [2]float32{x, y - fh*3/2}, style)
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
		td.AddText(ac.Scratchpad, [2]float32{x, y - 3*fh}, style)
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
				callsign := positionConfig.selectedAircraft.Callsign
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

///////////////////////////////////////////////////////////////////////////
// ImageViewPane

type ImageViewPane struct {
	Directory         string
	SelectedImage     string
	ImageCalibrations map[string]*ImageCalibration
	InvertImages      bool

	DrawAircraft bool
	AircraftSize int32

	enteredFixPos    [2]float32
	enteredFix       string
	enteredFixCursor int

	nImagesLoading int
	ctx            context.Context
	cancel         context.CancelFunc
	loadChan       chan LoadedImage

	loadedImages    map[string]*ImageViewImage
	showImageList   bool
	scrollBar       *ScrollBar
	dirSelectDialog *FileSelectDialogBox
}

type ImageCalibration struct {
	Fix     [2]string
	Pimage  [2][2]float32
	lastSet int
}

type LoadedImage struct {
	Name    string
	Pyramid []image.Image
}

type ImageViewImage struct {
	Name        string
	TexId       uint32
	AspectRatio float32
}

func NewImageViewPane() *ImageViewPane {
	return &ImageViewPane{
		Directory:         "/Users/mmp/vatsim/KPHL",
		ImageCalibrations: make(map[string]*ImageCalibration),
		AircraftSize:      16,
	}
}

func (iv *ImageViewPane) Duplicate(nameAsCopy bool) Pane {
	dupe := &ImageViewPane{
		Directory:         iv.Directory,
		SelectedImage:     iv.SelectedImage,
		ImageCalibrations: DuplicateMap(iv.ImageCalibrations),
		InvertImages:      iv.InvertImages,
		DrawAircraft:      iv.DrawAircraft,
		AircraftSize:      iv.AircraftSize,
		scrollBar:         NewScrollBar(4, false),
	}
	dupe.loadImages()
	return dupe
}

func (iv *ImageViewPane) Activate() {
	iv.scrollBar = NewScrollBar(4, false)
	iv.loadImages()
}

func (iv *ImageViewPane) loadImages() {
	iv.loadedImages = make(map[string]*ImageViewImage)
	iv.ctx, iv.cancel = context.WithCancel(context.Background())
	iv.loadChan = make(chan LoadedImage, 64)
	iv.nImagesLoading = 0

	de, err := os.ReadDir(iv.Directory)
	if err != nil {
		lg.Errorf("%s: %v", iv.Directory, err)
	}

	// Load the selected one first
	if FindIf(de, func(de os.DirEntry) bool { return de.Name() == iv.SelectedImage }) != -1 {
		iv.nImagesLoading++
		loadImage(iv.ctx, path.Join(iv.Directory, iv.SelectedImage), iv.InvertImages, iv.loadChan)
	}

	for _, entry := range de {
		ext := filepath.Ext(strings.ToUpper(entry.Name()))
		if entry.IsDir() || (ext != ".PNG" && ext != ".JPG" && ext != ".JPEG") {
			continue
		}
		if entry.Name() == iv.SelectedImage {
			// Already loaded it
			continue
		}

		filename := path.Join(iv.Directory, entry.Name())
		iv.nImagesLoading++
		go loadImage(iv.ctx, filename, iv.InvertImages, iv.loadChan)
	}
}

func loadImage(ctx context.Context, path string, invertImage bool, loadChan chan LoadedImage) {
	f, err := os.Open(path)
	if err != nil {
		lg.Errorf("%s: %v", path, err)
		return
	}
	defer f.Close()

	var img image.Image
	switch filepath.Ext(strings.ToUpper(path)) {
	case ".PNG":
		img, err = png.Decode(f)

	case ".JPG", ".JPEG":
		img, err = jpeg.Decode(f)
	}

	if err != nil {
		lg.Errorf("%s: unable to decode image: %v", path, err)
		return
	}

	if img != nil {
		if invertImage {
			rgbaImage, ok := img.(*image.RGBA)
			if !ok {
				rgbaImage = image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
				draw.Draw(rgbaImage, rgbaImage.Bounds(), img, img.Bounds().Min, draw.Src)
			}

			b := rgbaImage.Bounds()
			for py := b.Min.Y; py < b.Max.Y; py++ {
				for px := b.Min.X; px < b.Max.X; px++ {
					offset := 4*px + rgbaImage.Stride*py
					rgba := color.RGBA{
						R: rgbaImage.Pix[offset],
						G: rgbaImage.Pix[offset+1],
						B: rgbaImage.Pix[offset+2],
						A: rgbaImage.Pix[offset+3]}

					r, g, b := float32(rgba.R)/255, float32(rgba.G)/255, float32(rgba.B)/255
					// convert to YIQ
					y, i, q := .299*r+.587*g+.114*b, .596*r-.274*g-.321*b, .211*r-.523*g+.311*b
					// invert luminance
					y = 1 - y
					// And back...
					r, g, b = y+.956*i+.621*q, y-.272*i-.647*q, y-1.107*i+1.705*q
					quant := func(f float32) uint8 {
						f *= 255
						if f < 0 {
							f = 0
						} else if f > 255 {
							f = 255
						}
						return uint8(f)
					}

					rgbaImage.Pix[offset] = quant(r)
					rgbaImage.Pix[offset+1] = quant(g)
					rgbaImage.Pix[offset+2] = quant(b)
				}
			}
			img = rgbaImage
		}

		pyramid := GenerateImagePyramid(img)
		for {
			select {
			case <-ctx.Done():
				// Canceled; exit
				return

			case loadChan <- LoadedImage{Name: path, Pyramid: pyramid}:
				// success
				return

			case <-time.After(50 * time.Millisecond):
				// sleep
			}
		}
	}
}

func (iv *ImageViewPane) Deactivate() {
	iv.clearImages()
}

func (iv *ImageViewPane) clearImages() {
	iv.cancel()
	iv.loadChan = nil

	for _, im := range iv.loadedImages {
		renderer.DestroyTexture(im.TexId)
	}
	iv.loadedImages = nil
	iv.nImagesLoading = 0
}

func (iv *ImageViewPane) CanTakeKeyboardFocus() bool { return true }

func (iv *ImageViewPane) Name() string { return "ImageView: " + path.Base(iv.Directory) }

func (iv *ImageViewPane) DrawUI() {
	imgui.Checkbox("Draw aircraft", &iv.DrawAircraft)
	if iv.DrawAircraft {
		imgui.SliderIntV("Aircraft size", &iv.AircraftSize, 4, 64, "%d", 0)
	}

	imgui.Text("Directory: " + iv.Directory)
	imgui.SameLine()
	if imgui.Button("Select...") {
		if iv.dirSelectDialog == nil {
			iv.dirSelectDialog = NewDirectorySelectDialogBox("Select image directory...",
				iv.Directory, func(d string) {
					if d == iv.Directory {
						return
					}
					iv.clearImages()
					iv.Directory = d
					iv.SelectedImage = ""
					iv.loadImages()
				})
		}
		iv.dirSelectDialog.Activate()
	}
	if iv.dirSelectDialog != nil {
		iv.dirSelectDialog.Draw()
	}

	if imgui.Checkbox("Invert images", &iv.InvertImages) {
		iv.clearImages()
		iv.loadImages()
	}

	// TODO?: refresh button
}

func (iv *ImageViewPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	if iv.nImagesLoading > 0 {
		select {
		case im := <-iv.loadChan:
			texid := renderer.CreateTextureFromImages(im.Pyramid)
			aspect := float32(im.Pyramid[0].Bounds().Max.X-im.Pyramid[0].Bounds().Min.X) /
				float32(im.Pyramid[0].Bounds().Max.Y-im.Pyramid[0].Bounds().Min.Y)

			iv.loadedImages[im.Name] = &ImageViewImage{
				Name:        im.Name,
				TexId:       texid,
				AspectRatio: aspect,
			}

			iv.nImagesLoading--

		default:
		}
	}

	ctx.SetWindowCoordinateMatrices(cb)

	enteringCalibration := wm.keyboardFocusPane == iv

	if !enteringCalibration {
		if !iv.showImageList {
			if ctx.mouse != nil && ctx.mouse.Released[0] {
				iv.showImageList = true
			}
		} else {
			iv.drawImageList(ctx, cb)
			return
		}
	}

	iv.drawImage(ctx, cb)
	iv.drawAircraft(ctx, cb)
	iv.handleCalibration(ctx, cb)
}

func (iv *ImageViewPane) drawImageList(ctx *PaneContext, cb *CommandBuffer) {
	font := ui.fixedFont
	indent := float32(int(font.size / 2)) // left and top spacing
	lineHeight := font.size

	nVisibleLines := (int(ctx.paneExtent.Height()) - font.size) / font.size
	iv.scrollBar.Update(len(iv.loadedImages), nVisibleLines, ctx)
	iv.scrollBar.Draw(ctx, cb)
	textOffset := iv.scrollBar.Offset()

	// Current cursor position
	pText := [2]float32{indent, ctx.paneExtent.Height() - indent + float32(textOffset*font.size)}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	style := TextStyle{Font: font, Color: ctx.cs.Text}

	var previousLine []string
	matchPrevious := func(s string) string {
		cur := strings.Fields(s)
		var result []string
		spaces := 0
		for i, str := range cur {
			if i == len(previousLine) || str != previousLine[i] {
				result = cur[i:]
				break
			} else {
				spaces += 1 + len(str)
				if i == 0 {
					spaces++
				}
			}
		}
		previousLine = cur
		return fmt.Sprintf("%*c", spaces, ' ') + strings.Join(result, " ")
	}

	for _, name := range SortedMapKeys(iv.loadedImages) {
		hovered := func() bool {
			return ctx.mouse != nil && ctx.mouse.Pos[1] < pText[1] && ctx.mouse.Pos[1] >= pText[1]-float32(lineHeight)
		}
		if hovered() {
			// Draw the selection box.
			rect := LinesDrawBuilder{}
			width := ctx.paneExtent.Width()
			rect.AddPolyline([2]float32{indent / 2, float32(pText[1])},
				[][2]float32{[2]float32{0, 0},
					[2]float32{width - indent, 0},
					[2]float32{width - indent, float32(-lineHeight)},
					[2]float32{0, float32(-lineHeight)}})
			cb.SetRGB(ctx.cs.Text)
			rect.GenerateCommands(cb)

			if ctx.mouse.Released[0] {
				iv.SelectedImage = name
				iv.showImageList = false
			}
		}

		text := matchPrevious(filepath.Base(name))
		pText = td.AddText(text+"\n", pText, style)
	}
	td.GenerateCommands(cb)
}

func (iv *ImageViewPane) drawImage(ctx *PaneContext, cb *CommandBuffer) {
	image, ok := iv.loadedImages[iv.SelectedImage]
	if !ok {
		return
	}

	// Draw the selected image
	td := GetTexturedTrianglesDrawBuilder()
	defer ReturnTexturedTrianglesDrawBuilder(td)

	e := iv.getImageExtent(image, ctx)
	p := [4][2]float32{e.p0, [2]float32{e.p1[0], e.p0[1]}, e.p1, [2]float32{e.p0[0], e.p1[1]}}

	td.AddQuad(p[0], p[1], p[2], p[3], [2]float32{0, 1}, [2]float32{1, 1}, [2]float32{1, 0}, [2]float32{0, 0})

	cb.SetRGB(RGB{1, 1, 1})
	td.GenerateCommands(image.TexId, cb)

	// Possibly start calibration specification
	if ctx.mouse != nil && ctx.mouse.Released[1] {
		// remap mouse position from window coordinates to normalized [0,1]^2 image coordinates
		iv.enteredFixPos = sub2f(ctx.mouse.Pos, e.p0)
		iv.enteredFixPos[0] /= e.Width()
		iv.enteredFixPos[1] /= e.Height()
		iv.enteredFix = ""
		iv.enteredFixCursor = 0
		wmTakeKeyboardFocus(iv, true)
		return
	}
}

// returns window-space extent
func (iv *ImageViewPane) getImageExtent(image *ImageViewImage, ctx *PaneContext) Extent2D {
	w, h := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	var dw, dh float32
	if image.AspectRatio > w/h {
		h = w / image.AspectRatio
		dh = (ctx.paneExtent.Height() - h) / 2
	} else {
		w = h * image.AspectRatio
		dw = (ctx.paneExtent.Width() - w) / 2
	}

	p0 := [2]float32{dw, dh}
	return Extent2D{p0: p0, p1: add2f(p0, [2]float32{w, h})}
}

func (iv *ImageViewPane) drawAircraft(ctx *PaneContext, cb *CommandBuffer) {
	if !iv.DrawAircraft {
		return
	}

	cal, ok := iv.ImageCalibrations[iv.SelectedImage]
	if !ok {
		return
	}

	var pll [2]Point2LL
	if pll[0], ok = database.Locate(cal.Fix[0]); !ok {
		return
	}
	if pll[1], ok = database.Locate(cal.Fix[1]); !ok {
		return
	}

	var image *ImageViewImage
	if image, ok = iv.loadedImages[iv.SelectedImage]; !ok {
		return
	}

	// Find the  window coordinates of the marked points
	var pw [2][2]float32
	e := iv.getImageExtent(image, ctx)
	for i := 0; i < 2; i++ {
		pw[i] = e.Lerp(cal.Pimage[i])
	}

	// rotate to align
	llTheta := atan2(pll[1][1]-pll[0][1], pll[1][0]-pll[0][0])
	wTheta := atan2(pw[1][1]-pw[0][1], pw[1][0]-pw[0][0])
	scale := distance2f(pw[0], pw[1]) / distance2f(pll[0], pll[1])

	windowFromLatLong := Identity3x3().
		// translate so that the origin is at pw[0]
		Translate(pw[0][0], pw[0][1]).
		// scale it so that the second points line up
		Scale(scale, scale).
		// rotate to align the vector from p0 to p1 in texture space
		// with the vector from p0 to p1 in window space
		Rotate(wTheta-llTheta).
		// translate so pll[0] is the origin
		Translate(-pll[0][0], -pll[0][1])

	var icons []PlaneIconSpec
	// FIXME: draw in consistent order
	for _, ac := range server.GetAllAircraft() {
		// FIXME: cull based on altitude range
		icons = append(icons, PlaneIconSpec{
			P:       windowFromLatLong.TransformPoint(ac.Position()),
			Heading: ac.Heading(),
			Size:    float32(iv.AircraftSize)})
	}
	DrawPlaneIcons(icons, ctx.cs.Track, cb)
}

func (iv *ImageViewPane) handleCalibration(ctx *PaneContext, cb *CommandBuffer) {
	enteringCalibration := wm.keyboardFocusPane == iv

	if !enteringCalibration {
		return
	}

	if ctx.keyboard != nil && ctx.keyboard.IsPressed(KeyEscape) {
		wmReleaseKeyboardFocus()
		return
	}

	// indicate if it's invalid
	pInput := [2]float32{10, 20}
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	if _, ok := database.Locate(iv.enteredFix); iv.enteredFix != "" && !ok {
		style := TextStyle{Font: ui.fixedFont, Color: ctx.cs.Error}
		pInput = td.AddText(FontAwesomeIconExclamationTriangle+" ", pInput, style)
	}

	inputStyle := TextStyle{Font: ui.fixedFont, Color: ctx.cs.Text}
	pInput = td.AddText("> ", pInput, inputStyle)
	td.GenerateCommands(cb)

	cursorStyle := TextStyle{Font: ui.fixedFont, Color: ctx.cs.Background,
		DrawBackground: true, BackgroundColor: ctx.cs.Text}
	exit, _ := uiDrawTextEdit(&iv.enteredFix, &iv.enteredFixCursor, ctx.keyboard, pInput,
		inputStyle, cursorStyle, cb)
	iv.enteredFix = strings.ToUpper(iv.enteredFix)

	if exit == TextEditReturnEnter {
		cal, ok := iv.ImageCalibrations[iv.SelectedImage]
		if !ok {
			cal = &ImageCalibration{}
			iv.ImageCalibrations[iv.SelectedImage] = cal
		}

		for i, fix := range cal.Fix {
			if fix == iv.enteredFix {
				// new location for existing one
				cal.Pimage[i] = iv.enteredFixPos
				return
			}
		}
		// find a slot. any unset?
		for i, fix := range cal.Fix {
			if fix == "" {
				cal.Pimage[i] = iv.enteredFixPos
				cal.Fix[i] = iv.enteredFix
				return
			}
		}

		// alternate between the two
		i := (cal.lastSet + 1) % len(cal.Fix)
		cal.Pimage[i] = iv.enteredFixPos
		cal.Fix[i] = iv.enteredFix
		cal.lastSet = i
	}
}

///////////////////////////////////////////////////////////////////////////
// TabbedPane

type TabbedPane struct {
	ThumbnailHeight int32
	ActivePane      int
	Panes           []Pane `json:"-"` // These are serialized manually below...

	dragPane Pane
}

func NewTabbedPane() *TabbedPane {
	return &TabbedPane{
		ThumbnailHeight: 128,
	}
}

func (tp *TabbedPane) MarshalJSON() ([]byte, error) {
	// We actually do want the Panes marshaled automatically, though we
	// need to carry along their types as well.
	type JSONTabbedPane struct {
		TabbedPane
		PanesCopy []Pane `json:"Panes"`
		PaneTypes []string
	}

	jtp := JSONTabbedPane{TabbedPane: *tp}
	jtp.PanesCopy = tp.Panes
	for _, p := range tp.Panes {
		jtp.PaneTypes = append(jtp.PaneTypes, fmt.Sprintf("%T", p))
	}

	return json.Marshal(jtp)
}

func (tp *TabbedPane) UnmarshalJSON(data []byte) error {
	// First unmarshal all of the stuff that can be done automatically; using
	// a separate type avoids an infinite loop of UnmarshalJSON calls.
	type LocalTabbedPane TabbedPane
	var ltp LocalTabbedPane
	if err := json.Unmarshal(data, &ltp); err != nil {
		return err
	}
	*tp = (TabbedPane)(ltp)

	// Now do the panes.
	var p struct {
		PaneTypes []string
		Panes     []any
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}

	for i, paneType := range p.PaneTypes {
		// It's a little messy to round trip from unmarshaling into []any
		// and then re-marshaling each one individually into bytes, but I
		// haven't found a cleaner way.
		data, err := json.Marshal(p.Panes[i])
		if err != nil {
			return err
		}
		if pane, err := unmarshalPane(paneType, data); err != nil {
			return err
		} else {
			tp.Panes = append(tp.Panes, pane)
		}
	}

	return nil
}

func (tp *TabbedPane) Duplicate(nameAsCopy bool) Pane {
	dupe := &TabbedPane{}
	for _, p := range tp.Panes {
		dupe.Panes = append(dupe.Panes, p.Duplicate(nameAsCopy))
	}
	return dupe
}

func (tp *TabbedPane) Activate() {
	for _, p := range tp.Panes {
		p.Activate()
	}
}

func (tp *TabbedPane) Deactivate() {
	for _, p := range tp.Panes {
		p.Deactivate()
	}
}

func (tp *TabbedPane) CanTakeKeyboardFocus() bool {
	for _, p := range tp.Panes {
		if p.CanTakeKeyboardFocus() {
			return true
		}
	}
	return false
}

func (tp *TabbedPane) Name() string {
	return "Tabbed window"
}

func (tp *TabbedPane) DrawUI() {
	imgui.SliderIntV("Thumbnail height", &tp.ThumbnailHeight, 8, 256, "%d", 0)
	name, pane := uiDrawNewPaneSelector("Add new window...", "")
	if name != "" {
		pane.Activate()
		tp.Panes = append(tp.Panes, pane)
	}

	flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg
	if imgui.BeginTableV("##panes", 4, flags, imgui.Vec2{}, 0.0) {
		imgui.TableSetupColumn("Windows")
		imgui.TableSetupColumn("Up")
		imgui.TableSetupColumn("Down")
		imgui.TableSetupColumn("Trash")
		for i, pane := range tp.Panes {
			imgui.TableNextRow()
			imgui.TableNextColumn()

			imgui.PushID(fmt.Sprintf("%d", i))
			if imgui.SelectableV(pane.Name(), i == tp.ActivePane, 0, imgui.Vec2{}) {
				tp.ActivePane = i
			}

			imgui.TableNextColumn()
			if i > 0 && len(tp.Panes) > 1 {
				if imgui.Button(FontAwesomeIconArrowUp) {
					tp.Panes[i], tp.Panes[i-1] = tp.Panes[i-1], tp.Panes[i]
					if tp.ActivePane == i {
						tp.ActivePane = i - 1
					}
				}
			}
			imgui.TableNextColumn()
			if i+1 < len(tp.Panes) {
				if imgui.Button(FontAwesomeIconArrowDown) {
					tp.Panes[i], tp.Panes[i+1] = tp.Panes[i+1], tp.Panes[i]
					if tp.ActivePane == i {
						tp.ActivePane = i + 1
					}
				}
			}
			imgui.TableNextColumn()
			if imgui.Button(FontAwesomeIconTrash) {
				tp.Panes = FilterSlice(tp.Panes, func(p Pane) bool { return p != pane })
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}

	if tp.ActivePane < len(tp.Panes) {
		if uid, ok := tp.Panes[tp.ActivePane].(PaneUIDrawer); ok {
			imgui.Separator()
			imgui.Text(tp.Panes[tp.ActivePane].Name() + " configuration")
			uid.DrawUI()
		}
	}
}

func (tp *TabbedPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	// final aspect ratio, after the thumbnails at the top:
	// TODO (adjust to fit)
	w, h := ctx.paneExtent.Width(), ctx.paneExtent.Height()
	thumbHeight := float32(tp.ThumbnailHeight)
	aspect := w / (h - thumbHeight)
	thumbWidth := float32(int(aspect * thumbHeight))
	highDPIScale := platform.FramebufferSize()[1] / platform.DisplaySize()[1]

	nThumbs := len(tp.Panes) - 1
	if thumbWidth*float32(nThumbs) > w {
		thumbWidth = floor(w / float32(nThumbs))
		// aspect = w / (h - thumbHeight)
		// aspect = w / (h - (thumbWidth / aspect)) ->
		aspect = (w + thumbWidth) / h
		thumbHeight = floor(thumbWidth / aspect)
	}

	// Draw the thumbnail panes
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	// Center them horizontally
	x0 := (w - thumbWidth*float32(nThumbs)) / 2
	x1 := x0 + thumbWidth*float32(nThumbs)
	// Draw top/bottom lines
	ld.AddLine([2]float32{x0, h}, [2]float32{x1, h}, ctx.cs.UIControl)
	ld.AddLine([2]float32{x0, h - thumbHeight}, [2]float32{x1, h - thumbHeight}, ctx.cs.UIControl)

	// Vertical separator lines
	for i := 0; i <= nThumbs; i++ {
		x := x0 + float32(i)*thumbWidth
		ld.AddLine([2]float32{x, h}, [2]float32{x, h - thumbHeight}, ctx.cs.UIControl)
	}

	ctx.SetWindowCoordinateMatrices(cb)
	cb.LineWidth(1)
	ld.GenerateCommands(cb)

	// Draw the thumbnail contents
	px0 := x0
	for i, pane := range tp.Panes {
		if i == tp.ActivePane {
			continue
		}
		paneCtx := *ctx
		paneCtx.mouse = nil
		paneCtx.thumbnail = true

		// 1px offsets to preserve separator lines
		paneCtx.paneExtent = Extent2D{
			p0: [2]float32{ctx.paneExtent.p0[0] + px0 + 1, ctx.paneExtent.p0[1] + h - thumbHeight + 1},
			p1: [2]float32{ctx.paneExtent.p0[0] + px0 + thumbWidth - 1, ctx.paneExtent.p0[1] + h - 1},
		}
		sv := paneCtx.paneExtent.Scale(highDPIScale)

		cb.Scissor(int(sv.p0[0]), int(sv.p0[1]), int(sv.Width()), int(sv.Height()))
		cb.Viewport(int(sv.p0[0]), int(sv.p0[1]), int(sv.Width()), int(sv.Height()))

		// FIXME: what if pane calls wmTakeKeyboard focus. Will things get
		// confused?
		pane.Draw(&paneCtx, cb)
		cb.ResetState()

		px0 += thumbWidth
	}

	// Draw the active pane
	if tp.ActivePane < len(tp.Panes) {
		// Scissor to limit to the region below the thumbnails
		paneCtx := *ctx
		paneCtx.paneExtent.p1[1] = paneCtx.paneExtent.p0[1] + h - thumbHeight
		sv := paneCtx.paneExtent.Scale(highDPIScale)
		cb.Scissor(int(sv.p0[0]), int(sv.p0[1]), int(sv.Width()), int(sv.Height()))
		cb.Viewport(int(sv.p0[0]), int(sv.p0[1]), int(sv.Width()), int(sv.Height()))

		// Don't pass along mouse events if the mouse is outside of the
		// draw region (e.g., a thumbnail is being clicked...)
		if paneCtx.mouse != nil && paneCtx.mouse.Pos[1] >= h-thumbHeight {
			paneCtx.mouse = nil
		}

		tp.Panes[tp.ActivePane].Draw(&paneCtx, cb)

		cb.ResetState()
	}

	// See if a thumbnail was clicked on
	if ctx.mouse != nil && ctx.mouse.Released[MouseButtonPrimary] && ctx.mouse.Pos[1] >= h-thumbHeight {
		i := int((ctx.mouse.Pos[0] - x0) / thumbWidth)
		if i >= tp.ActivePane {
			// account for the active pane's thumbnail not being drawn
			i++
		}
		if i >= 0 && i < len(tp.Panes) {
			tp.ActivePane = i
		}
	}
}
