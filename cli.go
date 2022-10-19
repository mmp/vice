// cli.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/mmp/imgui-go/v4"
)

type CommandArgsFormat int

const (
	// Only one of aircraft, controller, or string should be set.
	CommandArgsAircraft = 1 << iota
	CommandArgsController
	CommandArgsString
	CommandArgsOptional // Can only be at the end. Allows 0 or 1 args.
	CommandArgsMultiple // Can only be at the end. Allows 0, 1, 2, ... args
)

type Command interface {
	Name() string
	Help() string
	Usage() string
	Syntax(isAircraftSelected bool) []CommandArgsFormat
	Run(cli *CLIPane, args []string) (string, error)
}

var (
	cliCommands []Command = []Command{
		&SetACTypeCommand{},
		&SetAltitudeCommand{isTemporary: false},
		&SetAltitudeCommand{isTemporary: true},
		&SetArrivalCommand{},
		&SetDepartureCommand{},
		&SetEquipmentSuffixCommand{},
		&SetIFRCommand{},
		&SetScratchpadCommand{},
		&SetSquawkCommand{},
		&SetVoiceCommand{},
		&SetVFRCommand{},

		&EditRouteCommand{},
		&NYPRDCommand{},
		&PRDCommand{},
		&SetRouteCommand{},

		&AcceptHandoffCommand{},
		&DropTrackCommand{},
		&HandoffCommand{},
		&PointOutCommand{},
		&RejectHandoffCommand{},
		&TrackAircraftCommand{},

		&FindCommand{},
		&MITCommand{},
		&DrawRouteCommand{},

		&InfoCommand{},
		&TimerCommand{},
		&ToDoCommand{},
		&TrafficCommand{},

		&WallopCommand{},

		&EchoCommand{},
	}
)

func checkCommands(cmds []Command) {
	seen := make(map[string]interface{})
	for _, c := range cmds {
		if _, ok := seen[c.Name()]; ok {
			lg.Errorf("%s: command has multiple definitions", c.Name())
		} else {
			seen[c.Name()] = nil
		}

		syntax := c.Syntax(false)
		for i := 0; i < len(syntax)-1; i++ {
			if syntax[i]&CommandArgsOptional != 0 {
				lg.Errorf("%s: optional arguments can only be at the end", c.Name())
			}
			if syntax[i]&CommandArgsMultiple != 0 {
				lg.Errorf("%s: multiple arguments can only be at the end", c.Name())
			}
			if syntax[i]&CommandArgsOptional != 0 && syntax[i]&CommandArgsMultiple != 0 {
				lg.Errorf("%s: cannot specify both optional and multiple arguments", c.Name())
			}
		}
	}
}

type ConsoleTextStyle int

const (
	ConsoleTextRegular = iota
	ConsoleTextEmphasized
	ConsoleTextError
)

type ConsoleEntry struct {
	text  []string
	style []ConsoleTextStyle
}

func (e *ConsoleEntry) Add(t string, s ConsoleTextStyle) {
	e.text = append(e.text, t)
	e.style = append(e.style, s)
}

func (e *ConsoleEntry) Draw(p [2]float32, style TextStyle, cs *ColorScheme) *TextDrawBuilder {
	t := &TextDrawBuilder{}
	for i := range e.text {
		switch e.style[i] {
		case ConsoleTextRegular:
			style.color = cs.Text

		case ConsoleTextEmphasized:
			style.color = cs.TextHighlight

		case ConsoleTextError:
			style.color = cs.TextError
		}

		t.AddText(e.text[i], p, style)
		if i < len(e.text)-1 {
			bx, _ := style.font.BoundText(e.text[i], 0)
			p[0] += float32(bx)
		}
	}
	return t
}

const consoleLimit = 250

type CLIInput struct {
	cmd      string
	cursor   int
	tabStops []int
	paramSet []bool
}

type CLIPane struct {
	history       []CLIInput
	historyOffset int // for up arrow / downarrow. Note: counts from the end! 0 when not in history
	savedInput    CLIInput
	mutex         sync.Mutex

	console           []*ConsoleEntry
	consoleViewOffset int // lines from the end (for pgup/down)
	errorCount        map[string]int

	FontIdentifier FontIdentifier
	font           *Font

	SpecialKeys map[string]*string

	input  CLIInput
	status string

	cb CommandBuffer
}

func NewCLIPane() *CLIPane {
	font := GetDefaultFont()
	return &CLIPane{
		FontIdentifier: font.id,
		font:           font,
		SpecialKeys:    make(map[string]*string),
		errorCount:     make(map[string]int)}
}

func (cli *CLIPane) Duplicate(nameAsCopy bool) Pane {
	return &CLIPane{
		FontIdentifier: cli.FontIdentifier,
		font:           cli.font,
		errorCount:     make(map[string]int)}
}

func (cli *CLIPane) Activate(cs *ColorScheme) {
	if cli.font = GetFont(cli.FontIdentifier); cli.font == nil {
		cli.font = GetDefaultFont()
		cli.FontIdentifier = cli.font.id
	}
	if cli.errorCount == nil {
		cli.errorCount = make(map[string]int)
	}
	if cli.SpecialKeys == nil {
		cli.SpecialKeys = make(map[string]*string)
	}
	lg.RegisterErrorMonitor(cli)

	checkCommands(cliCommands)
}

func (cli *CLIPane) Deactivate() {
	lg.DeregisterErrorMonitor(cli)
}

func (cli *CLIPane) ErrorReported(msg string) {
	// Remove excess spaces
	msg = strings.Join(strings.Fields(msg), " ")
	// Although vice isn't multithreaded, sector file parsing is, so we may
	// get concurrent calls to this...
	cli.mutex.Lock()
	defer cli.mutex.Unlock()
	cli.errorCount[msg] = cli.errorCount[msg] + 1

	isPow10 := func(v int) bool {
		for v != 0 {
			if v == 1 {
				return true
			}
			if v%10 != 0 {
				return false
			}
			v /= 10
		}
		return false
	}
	n := cli.errorCount[msg]
	if n == 1 {
		cli.AddConsoleEntry([]string{"Internal Error: ", msg}, []ConsoleTextStyle{ConsoleTextError, ConsoleTextRegular})
	} else if isPow10(n) {
		cli.AddConsoleEntry([]string{fmt.Sprintf("Internal Error (%dx): ", n), msg},
			[]ConsoleTextStyle{ConsoleTextError, ConsoleTextRegular})
	}
}

func (cli *CLIPane) Update(updates *ControlUpdates) {
	// Add any text/radio messages to the console
	for _, m := range updates.messages {
		switch m.messageType {
		case TextBroadcast:
			cli.AddConsoleEntry([]string{"[BROADCAST] " + m.sender + ": ", m.contents},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})
		case TextWallop:
			cli.AddConsoleEntry([]string{"[WALLOP] " + m.sender + ": ", m.contents},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})
		case TextATC:
			cli.AddConsoleEntry([]string{"[ATC] " + m.sender + ": ", m.contents},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})
		case TextFrequency:
			fm := positionConfig.MonitoredFrequencies(m.frequencies)
			if len(fm) > 0 {
				freq := strings.Join(Map(fm, func(f Frequency) string { return f.String() }), ", ")
				cli.AddConsoleEntry([]string{freq + ": ", m.contents},
					[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})
			}
		case TextPrivate:
			cli.AddConsoleEntry([]string{m.sender + ": ", m.contents},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})
		}
	}
}

func (cli *CLIPane) Name() string { return "Command Line Interface" }

func (cli *CLIPane) DrawUI() {
	if newFont, changed := DrawFontPicker(&cli.FontIdentifier, "Font"); changed {
		cli.font = newFont
	}

	imgui.Separator()
	flags := imgui.TableFlagsBordersH | imgui.TableFlagsBordersOuterV | imgui.TableFlagsRowBg
	imgui.Text("Key Bindings")
	const textWidth = 200
	if imgui.BeginTableV(fmt.Sprintf("SpecialKeys##%p", cli), 4, flags, imgui.Vec2{}, 0.0) {
		imgui.TableSetupColumnV("Key", imgui.TableColumnFlagsWidthFixed, 20., 0)
		imgui.TableSetupColumnV("Command", imgui.TableColumnFlagsWidthFixed, textWidth, 0)
		imgui.TableSetupColumnV("Key##Shift", imgui.TableColumnFlagsWidthFixed, 50., 0)
		imgui.TableSetupColumnV("Command##Shift", imgui.TableColumnFlagsWidthFixed, textWidth, 0)
		imgui.TableHeadersRow()
		for i := 1; i <= 12; i++ {
			imgui.TableNextRow()

			k := func(key string) {
				imgui.TableNextColumn()
				imgui.Text(key)
				imgui.TableNextColumn()
				sp := cli.SpecialKeys[key]
				if sp == nil {
					sp = new(string)
					cli.SpecialKeys[key] = sp
				}
				imgui.SetNextItemWidth(textWidth)
				imgui.InputText("##"+key, sp)
			}

			key := fmt.Sprintf("F%d", i)
			k(key)
			k("Shift-" + key)
		}

		imgui.EndTable()
	}
}

func (cli *CLIPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	cli.cb.Reset()
	ctx.SetWindowCoordinateMatrices(&cli.cb)

	style := TextStyle{font: cli.font, lineSpacing: 1, color: ctx.cs.Text}
	cursorStyle := TextStyle{font: cli.font, lineSpacing: 0,
		color: ctx.cs.Background, drawBackground: true, backgroundColor: ctx.cs.Text}
	statusStyle := TextStyle{font: cli.font, lineSpacing: 0, color: ctx.cs.TextError}
	lineHeight := float32(style.font.size + style.lineSpacing)

	// Draw the console buffer.
	// Save some space for top/bottom padding and the input and the status line.
	consoleLinesVisible := int((ctx.paneExtent.Height() - 3*lineHeight) / lineHeight)

	// Process user input
	io := imgui.CurrentIO()
	if !io.WantCaptureKeyboard() {
		prevCallsign := ""
		if positionConfig.selectedAircraft != nil {
			prevCallsign = positionConfig.selectedAircraft.Callsign()
		}

		// Execute command if enter was typed
		hitEnter := cli.updateInput(consoleLinesVisible, ctx.platform)
		if hitEnter {
			if cli.input.AnyParametersUnset() {
				cli.status = "Some alias parameters are still unset."
			} else {
				if len(cli.input.cmd) > 0 {
					cmd := string(cli.input.cmd)

					cli.history = append(cli.history, cli.input)
					// Reset this state here so that aliases and commands
					// like 'editroute' can populate the next command
					// input.
					cli.input = CLIInput{}

					output, err := cli.runCommand(cmd)

					// Add the command and its output to the console history
					if prevCallsign != "" {
						cli.AddBasicConsoleEntry(prevCallsign+"> "+cmd, ConsoleTextRegular)
					} else {
						cli.AddBasicConsoleEntry("> "+cmd, ConsoleTextRegular)
					}
					if err != nil {
						cli.AddBasicConsoleEntry(err.Error(), ConsoleTextError)
					}
					for _, str := range strings.Split(output, "\n") {
						if str != "" {
							cli.AddBasicConsoleEntry(str, ConsoleTextRegular)
						}
					}
				}

				cli.consoleViewOffset = 0
				cli.historyOffset = 0
				cli.status = ""
			}
		}
	}

	// Draw the console history above the command prompt
	left := float32(cli.font.size) / 2
	y := (float32(consoleLinesVisible) + 2.5) * lineHeight // 2.5 for the stuff below
	for i := 0; i < consoleLinesVisible; i++ {
		idx := len(cli.console) - 1 - cli.consoleViewOffset - consoleLinesVisible + 1 + i
		if idx >= 0 {
			td := cli.console[idx].Draw([2]float32{left, y}, style, ctx.cs)
			td.GenerateCommands(&cli.cb)
		}
		y -= lineHeight
	}

	// Draw text for the input, one line above the status line
	inputPos := [2]float32{left, 2.5 * lineHeight}
	cli.input.EmitDrawCommands(inputPos, style, cursorStyle, &cli.cb)

	// status
	if cli.status != "" {
		sd := TextDrawBuilder{}
		// Half line of spacing below it
		sd.AddText(cli.status, [2]float32{left, 1.5 * lineHeight}, statusStyle)
		sd.GenerateCommands(&cli.cb)
	}

	cb.Call(cli.cb)
}

func (ci *CLIInput) EmitDrawCommands(inputPos [2]float32, style TextStyle, cursorStyle TextStyle, cb *CommandBuffer) {
	td := TextDrawBuilder{}
	prompt := ""
	if positionConfig.selectedAircraft != nil {
		prompt = positionConfig.selectedAircraft.Callsign()
	}
	prompt = prompt + "> "
	if ci.cursor == len(ci.cmd) {
		// cursor at the end
		td.AddTextMulti([]string{prompt + string(ci.cmd), " "}, inputPos,
			[]TextStyle{style, cursorStyle})
	} else {
		// cursor in the middle
		sb := prompt + ci.cmd[:ci.cursor]
		sc := ci.cmd[ci.cursor : ci.cursor+1]
		se := ci.cmd[ci.cursor+1:]
		styles := []TextStyle{style, cursorStyle, style}
		td.AddTextMulti([]string{sb, sc, se}, inputPos, styles)
	}
	td.GenerateCommands(cb)
}

func (ci *CLIInput) InsertAtCursor(s string) {
	if len(s) == 0 {
		return
	}

	// Is the cursor at a parameter stop that hasn't been set? Record that
	// it's set and delete the "_" before the insertion.
	if ci.InitialParameterSetting() {
		ci.DeleteAfterCursor()
	}

	ci.cmd = ci.cmd[:ci.cursor] + s + ci.cmd[ci.cursor:]

	// update parameter positions
	for i := range ci.tabStops {
		if ci.cursor <= ci.tabStops[i] {
			ci.tabStops[i] += len(s)
		}
	}

	// place cursor after the inserted text
	ci.cursor += len(s)
}

func (ci *CLIInput) DeleteBeforeCursor() {
	if ci.cursor > 0 {
		ci.cmd = ci.cmd[:ci.cursor-1] + ci.cmd[ci.cursor:]

		// TODO: should we allow deleting tab stops? (e.g., if cursor == tabStops[i])?
		for i := range ci.tabStops {
			if ci.cursor <= ci.tabStops[i] {
				ci.tabStops[i]--
			}
		}

		ci.cursor--
	}
}

func (ci *CLIInput) DeleteAfterCursor() {
	if ci.cursor < len(ci.cmd) {
		ci.cmd = ci.cmd[:ci.cursor] + ci.cmd[ci.cursor+1:]

		// TODO: allow deleting tab stops?
		for i := range ci.tabStops {
			if ci.cursor < ci.tabStops[i] {
				ci.tabStops[i]--
			}
		}
	}
}

func (ci *CLIInput) InitialParameterSetting() bool {
	for i, stop := range ci.tabStops {
		if ci.cursor == stop && !ci.paramSet[i] {
			// Arguably this is an obscure place to set this...
			ci.paramSet[i] = true
			return true
		}
	}
	return false
}

func (ci *CLIInput) AnyParametersUnset() bool {
	for _, s := range ci.paramSet {
		if !s {
			return true
		}
	}
	return false
}

func (ci *CLIInput) InitializeParameters(cmd string) (string, bool) {
	ci.tabStops = nil
	ci.paramSet = nil
	base := 0
	c := cmd
	for {
		idx := strings.Index(c, "$_")
		if idx == -1 {
			break
		}

		base += idx
		ci.tabStops = append(ci.tabStops, base)
		ci.paramSet = append(ci.paramSet, false)
		base++ // account for the _ we'll be adding
		c = c[idx+2:]
	}

	return strings.ReplaceAll(cmd, "$_", "_"), len(ci.tabStops) > 0
}

func (ci *CLIInput) ParameterCursor() int {
	if len(ci.tabStops) > 0 {
		return ci.tabStops[0]
	}
	return 0
}

func (ci *CLIInput) TabNext() bool {
	return ci.tab(1)
}

func (ci *CLIInput) TabPrev() bool {
	return ci.tab(-1)
}

func (ci *CLIInput) tab(step int) bool {
	if len(ci.cmd) == 0 {
		return false
	}

	start := ci.cursor
	pos := start
	for {
		pos = (pos + step) % len(ci.cmd)
		if pos < 0 {
			pos += len(ci.cmd)
		}

		if pos == start {
			lg.Errorf("tab went all the way around without finding a parameter? cursor %d, stops %+v",
				ci.cursor, ci.tabStops)
			return false
		}

		for _, stop := range ci.tabStops {
			if pos == stop {
				ci.cursor = stop
				return true
			}
		}
	}
}

// Simple, one string, same style
func (cli *CLIPane) AddBasicConsoleEntry(str string, style ConsoleTextStyle) {
	e := &ConsoleEntry{}
	e.text = append(e.text, str)
	e.style = append(e.style, style)
	cli.console = append(cli.console, e)

	if len(cli.console) > consoleLimit {
		// FIXME: this will be slow if consoleLimit is big. Use a ring
		// buffer instead?
		copy(cli.console, cli.console[1:])
		cli.console = cli.console[:consoleLimit]
	}
}

func (cli *CLIPane) AddConsoleEntry(str []string, style []ConsoleTextStyle) {
	n := len(str)
	if len(str) != len(style) {
		lg.ErrorfUp1("Mismatching slice lengths: %d vs %d", len(str), len(style))
		if len(style) < len(str) {
			n = len(style)
		}
	}

	e := &ConsoleEntry{}
	e.text = append(e.text, str[:n]...)
	e.style = append(e.style, style[:n]...)
	cli.console = append(cli.console, e)

	if len(cli.console) > consoleLimit {
		copy(cli.console, cli.console[1:])
		cli.console = cli.console[:consoleLimit]
	}
}

func (cli *CLIPane) updateInput(consoleLinesVisible int, platform Platform) (hitEnter bool) {
	// Grab keyboard input
	cli.input.InsertAtCursor(platform.InputCharacters())

	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyUpArrow)) {
		if cli.historyOffset == len(cli.history) {
			cli.status = "Reached end of history."
		} else {
			if cli.historyOffset == 0 {
				cli.savedInput = cli.input // save current input in case we return
			}
			cli.historyOffset++
			cli.input = cli.history[len(cli.history)-cli.historyOffset]
			cli.input.cursor = len(cli.input.cmd)
			cli.status = ""
		}
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyDownArrow)) {
		if cli.historyOffset == 0 {
			cli.status = "Reached end of history."
		} else {
			cli.historyOffset--
			if cli.historyOffset == 0 {
				cli.input = cli.savedInput
				cli.savedInput = CLIInput{}
			} else {
				cli.input = cli.history[len(cli.history)-cli.historyOffset]
			}
			cli.input.cursor = len(cli.input.cmd)
		}
	}

	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyLeftArrow)) {
		if cli.input.cursor > 0 {
			cli.input.cursor--
		}
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyRightArrow)) {
		if cli.input.cursor < len(cli.input.cmd) {
			cli.input.cursor++
		}
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyHome)) {
		cli.input.cursor = 0
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEnd)) {
		cli.input.cursor = len(cli.input.cmd)
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyBackspace)) {
		cli.input.DeleteBeforeCursor()
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyDelete)) {
		cli.input.DeleteAfterCursor()
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEscape)) {
		if cli.input.cursor > 0 {
			cli.input = CLIInput{}
			cli.status = ""
		} else {
			positionConfig.selectedAircraft = nil
		}
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyTab)) {
		io := imgui.CurrentIO()
		if io.KeyShiftPressed() {
			if !cli.input.TabPrev() {
				cli.status = "no parameter stops"
			}
		} else {
			if !cli.input.TabNext() {
				cli.status = "no parameter stops"
			}
		}
	}

	// history-related
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyPageUp)) {
		// Keep one line from before
		cli.consoleViewOffset += consoleLinesVisible - 1
		// Don't go past the end
		if cli.consoleViewOffset > len(cli.console)-consoleLinesVisible {
			cli.consoleViewOffset = len(cli.console) - consoleLinesVisible
			if cli.consoleViewOffset < 0 {
				cli.consoleViewOffset = 0
			}
		}
		return
	}
	if imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyPageDown)) {
		cli.consoleViewOffset -= consoleLinesVisible - 1
		if cli.consoleViewOffset < 0 {
			cli.consoleViewOffset = 0
		}
		return
	}

	// Check the function keys
	const F1 = 290
	for i := 0; i < 12; i++ {
		if !imgui.IsKeyPressed(F1 + i) {
			continue
		}

		name := fmt.Sprintf("F%d", i+1)
		io := imgui.CurrentIO()
		if io.KeyShiftPressed() {
			name = "Shift-" + name
		}

		if t, ok := cli.SpecialKeys[name]; ok {
			cli.input.InsertAtCursor(*t)
		}
	}

	// Other than paging through history, everything henceforth changes the input.
	return imgui.IsKeyPressed(imgui.GetKeyIndex(imgui.KeyEnter))
}

func matchingAircraft(s string) []*Aircraft {
	s = strings.ToUpper(s)

	// if there's an exact match, then take that.
	if ac := server.GetAircraft(s); ac != nil {
		return []*Aircraft{ac}
	}

	// Otherwise return all that match
	now := server.CurrentTime()
	return server.GetFilteredAircraft(func(ac *Aircraft) bool {
		return !ac.LostTrack(now) && strings.Contains(ac.Callsign(), s)
	})
}

func lookupCommand(n string) Command {
	for _, c := range cliCommands {
		if c.Name() == n {
			return c
		}
	}
	return nil
}

func (cli *CLIPane) expandVariables(cmd string) (expanded string, err error) {
	// We'll start by expanding cmd out into individual arguments. The
	// first step is to split them based on whitespace.
	initialArgs := strings.Split(cmd, " ")

	// Now we need to patch things up for the builtin functions--the goal
	// is that if there is a function use like $foo(bar bat) that we turn
	// it into the entries "$foo", "bar bat" in the result array. Or, for
	// $foo(), we end up with "$foo", "". Thus, the function expansion
	// implementations can always assume that whatever was passed to it is
	// available as a single string in the following argument.

	// $func(arg), all in a single string. Capture groups give the two parts.
	rsingle := regexp.MustCompile("(^\\$\\w+?)\\((.*)\\)$")

	// $func(arg, with no closing paren. Again two capture groups.
	ropen := regexp.MustCompile("^(\\$\\w+?)\\((.*)$")

	// ...), closing multi-arg function
	rclose := regexp.MustCompile("^(.*)\\)$")

	var groupedArgs []string
	for i := 0; i < len(initialArgs); i++ {
		arg := initialArgs[i]
		if len(arg) == 0 /* ?? */ || arg[0] != '$' {
			groupedArgs = append(groupedArgs, arg)
			continue
		}

		if m := rsingle.FindStringSubmatch(arg); m != nil {
			if len(m) != 3 {
				lg.Errorf("%s -> %+v (length %d, not 3!)", arg, m, len(m))
			} else {
				groupedArgs = append(groupedArgs, m[1], m[2])
			}
		} else if m := ropen.FindStringSubmatch(arg); m != nil {
			if len(m) != 3 {
				lg.Errorf("%s -> %+v (length %d, not 3!)", arg, m, len(m))
				continue
			}

			fn := m[1]
			groupedArgs = append(groupedArgs, m[1])

			funArg := m[2]
			// Now slurp up args until we reach one with the closing paren.
			i++
			for i < len(initialArgs) {
				arg = initialArgs[i]
				if m := rclose.FindStringSubmatch(arg); m != nil {
					if len(m) != 2 {
						lg.Errorf("%s -> %+v (length %d, not 2!)", arg, m, len(m))
					} else {
						funArg += " " + m[1]
					}
					break
				} else {
					funArg += " " + arg
				}
				i++
			}

			if i == len(initialArgs) {
				err = fmt.Errorf("%s: no closing parenthesis found after function alias", fn)
			}
			groupedArgs = append(groupedArgs, funArg)
		} else {
			// it's just a variable
			groupedArgs = append(groupedArgs, arg)
		}
	}

	var finalArgs []string
	for i := 0; i < len(groupedArgs); i++ {
		arg := groupedArgs[i]
		if err != nil {
			break
		}

		if len(arg) == 0 || arg[0] != '$' {
			finalArgs = append(finalArgs, arg)
			continue
		}

		// Helper for variables that expand do things based on the selected aircraft.
		// The provided callback can assume a non-nil *Aircraft...
		acarg := func(str func(*Aircraft) string) {
			if positionConfig.selectedAircraft != nil {
				finalArgs = append(finalArgs, str(positionConfig.selectedAircraft))
			} else if err == nil {
				err = fmt.Errorf("%s: unable to expand variable since no aircraft is selected.", arg)
			}
		}

		metararg := func(airport string, str func(m *METAR) string) {
			if m := server.GetMETAR(strings.ToUpper(airport)); m != nil {
				finalArgs = append(finalArgs, str(m))
			} else if err == nil {
				err = fmt.Errorf("%s: METAR for airport is not available.", airport)
			}
		}

		fixarg := func(fix string, str func(a *Aircraft, pos Point2LL) string) {
			if positionConfig.selectedAircraft != nil {
				if pos, ok := database.Locate(fix); !ok {
					err = fmt.Errorf("%s: fix is unknown.", fix)
				} else if !pos.IsZero() {
					finalArgs = append(finalArgs, str(positionConfig.selectedAircraft, pos))
				}
			} else {
				err = fmt.Errorf("%s: unable to evaluate function since no aircraft is selected.", arg)
			}
		}

		funarg := func() string {
			i++
			if i < len(groupedArgs) {
				return groupedArgs[i]
			} else if err == nil {
				err = fmt.Errorf("%s: no argument passed to function", arg)
			}
			return ""
		}

		// These currently all follow VRC/EuroScope..
		// Missing ones:
		// Variables: $callsign, $com1, $myrealname, $atiscode
		// Functions: $type, $radioname, $freq, $atccallsign
		switch arg[1:] {
		case "aircraft":
			acarg(func(ac *Aircraft) string { return ac.callsign })

		case "alt":
			acarg(func(ac *Aircraft) string {
				if ac.tempAltitude != 0 {
					return fmt.Sprintf("%d", ac.tempAltitude)
				} else if ac.flightPlan != nil {
					return fmt.Sprintf("%d", ac.flightPlan.altitude)
				} else {
					return "???"
				}
			})

		case "altim":
			metararg(funarg(), func(m *METAR) string { return m.altimeter })

		case "arr":
			acarg(func(ac *Aircraft) string {
				if ac.flightPlan != nil {
					return ac.flightPlan.arrive
				} else {
					return "????"
				}
			})

		case "bear":
			// this currently gives the direction to fix with respect to
			// the aircraft, so e.g. "Kennedy is to your $bear(kjfk)"
			fixarg(funarg(), func(ac *Aircraft, p Point2LL) string {
				heading := headingp2ll(ac.Position(), p, database.MagneticVariation)
				return compass(heading)
			})

		case "calt":
			acarg(func(ac *Aircraft) string { return fmt.Sprintf("%d", ac.Altitude()) })

		case "cruise":
			acarg(func(ac *Aircraft) string {
				if ac.flightPlan != nil {
					return fmt.Sprintf("%d", ac.flightPlan.altitude)
				} else {
					return "????"
				}
			})

		case "dep":
			acarg(func(ac *Aircraft) string {
				if ac.flightPlan != nil {
					return ac.flightPlan.depart
				} else {
					return "????"
				}
			})

		case "dist":
			fixarg(funarg(), func(ac *Aircraft, p Point2LL) string {
				dist := nmdistance2ll(ac.Position(), p)
				idist := int(dist + 0.5)
				if idist <= 1 {
					return "1 mile"
				} else {
					return fmt.Sprintf("%d miles", idist)
				}
			})

		case "ftime":
			fa := funarg()
			if fa == "" {
				// nothing specified
				finalArgs = append(finalArgs, time.Now().UTC().Format("15:04:05Z"))
			} else if minutes, e := strconv.Atoi(fa); e != nil {
				if err != nil {
					err = fmt.Errorf("%s: expected integer number of minutes", fa)
				}
			} else {
				dtime := time.Now().Add(time.Duration(minutes) * time.Minute)
				finalArgs = append(finalArgs, dtime.UTC().Format("15:04:05Z"))
			}

		case "lc":
			finalArgs = append(finalArgs, strings.ToLower(funarg()))

		case "metar":
			metararg(funarg(), func(m *METAR) string { return m.String() })

		case "oclock":
			fixarg(funarg(), func(ac *Aircraft, p Point2LL) string {
				heading := headingp2ll(ac.Position(), p, database.MagneticVariation) - ac.Heading()
				return fmt.Sprintf("%d", headingAsHour(heading))
			})

		case "route":
			acarg(func(ac *Aircraft) string {
				if ac.flightPlan != nil {
					return ac.flightPlan.route
				} else {
					return "????"
				}
			})

		case "squawk":
			acarg(func(ac *Aircraft) string {
				if ac.assignedSquawk == Squawk(0) {
					return ac.squawk.String()
				} else {
					return ac.assignedSquawk.String()
				}
			})

		case "temp":
			acarg(func(ac *Aircraft) string { return fmt.Sprintf("%d", ac.tempAltitude) })

		case "time":
			finalArgs = append(finalArgs, time.Now().UTC().Format("15:04:05Z"))

		case "wind":
			metararg(funarg(), func(m *METAR) string { return m.wind })

		case "winds":
			acarg(func(ac *Aircraft) string {
				if ac.flightPlan == nil {
					return "???"
				}

				var airport, aptype string
				if ac.OnGround() {
					airport = strings.ToUpper(ac.flightPlan.depart)
					aptype = "departure"
				} else {
					airport = strings.ToUpper(ac.flightPlan.arrive)
					aptype = "arrival"
				}

				if m := server.GetMETAR(airport); m != nil {
					return m.wind
				} else if err == nil {
					err = fmt.Errorf("%s: METAR for %s airport is not available.", airport, aptype)
				}
				return ""
			})

		case "uc":
			finalArgs = append(finalArgs, strings.ToUpper(funarg()))

		default:
			return "", fmt.Errorf("%s: unknown variable", arg)
		}
	}

	expanded = strings.Join(finalArgs, " ")
	return
}

func (cli *CLIPane) runCommand(cmd string) (string, error) {
	cmdExpAliases, err := cli.ExpandAliases(cmd)
	if err != nil {
		return "", err
	}
	if cmdExpAliases != cmd {
		// One or more aliases were expanded. Are there any parameters we
		// need from the user?
		if newCmd, ok := cli.input.InitializeParameters(cmdExpAliases); ok {
			cli.input.cmd = newCmd
			cli.input.cursor = cli.input.ParameterCursor()
			// Back to the user for editing.
			return "", nil
		}
		// Otherwise fall through and execute the command specified by the
		// alias.
	}

	cmdExpAliasesVars, err := cli.expandVariables(cmdExpAliases)
	if err != nil {
		return "", err
	}

	fields := strings.Fields(cmdExpAliasesVars)
	if len(fields) == 0 {
		lg.Printf("unexpected no fields in command: %s", cmdExpAliasesVars)
		return "", nil
	}

	if fields[0] == "help" {
		switch len(fields) {
		case 1:
			var names []string
			for _, cmd := range cliCommands {
				names = append(names, cmd.Name())
			}
			sort.Strings(names)
			return fmt.Sprintf("available commands: %s", strings.Join(names, " ")), nil
		case 2:
			cmd := lookupCommand(fields[1])
			if cmd == nil {
				return "", fmt.Errorf("%s: unknown command", fields[1])
			} else {
				return fmt.Sprintf("%s: %s\nusage: %s %s", cmd.Name(), cmd.Help(),
					cmd.Name(), cmd.Usage()), nil
			}

		default:
			return "", fmt.Errorf("usage: help <command name>")
		}
	}

	// If it's a built-in command, run it
	if cmd := lookupCommand(fields[0]); cmd != nil {
		syntax := cmd.Syntax(positionConfig.selectedAircraft != nil)
		args := fields[1:]

		// Minimum and maximum number of args required from the user
		minArgc, maxArgc := len(syntax), len(syntax)
		if len(syntax) > 0 {
			last := syntax[len(syntax)-1]
			if last&CommandArgsOptional != 0 {
				minArgc--
			}
			if last&CommandArgsMultiple != 0 {
				minArgc--
				maxArgc += 100000 // oughta be enough...
			}
		}
		if positionConfig.selectedAircraft != nil {
			for _, s := range syntax {
				if s&CommandArgsAircraft != 0 {
					// We can get this one from selected.
					minArgc--
					break
				}
			}
		}

		if len(args) < minArgc {
			return "", fmt.Errorf("%s: insufficient arguments provided: %s",
				cmd.Name(), cmd.Usage())
		} else if len(args) > maxArgc {
			return "", fmt.Errorf("%s: excessive arguments provided: %s",
				cmd.Name(), cmd.Usage())
		}

		argSyntax := func(i int) CommandArgsFormat {
			if i < len(syntax) {
				return syntax[i]
			} else {
				return syntax[len(syntax)-1]
			}
		}

		// Parameter expansion and normalization
		for i := range args {
			syn := argSyntax(i)
			if syn&CommandArgsAircraft != 0 {
				// TODO: expansion
				args[i] = strings.ToUpper(args[i])
			} else if syn&CommandArgsController != 0 {
				args[i] = strings.ToUpper(args[i])
			}
		}

		return cmd.Run(cli, args)
	}

	// Otherwise see if we're selecting an aircraft...
	if len(fields) == 1 {
		matches := matchingAircraft(fields[0])
		switch len(matches) {
		case 0:
			// drop through to unknown command error
		case 1:
			positionConfig.selectedAircraft = matches[0]
			return "", nil
		default:
			msg := "Error: multiple aircraft match: "
			for _, ac := range matches {
				msg += ac.Callsign() + " "
			}
			return "", fmt.Errorf(msg)
		}
	}

	return "", fmt.Errorf("%s: unknown command", fields[0])
}

func (cli *CLIPane) ExpandAliases(cmd string) (string, error) {
	if globalConfig.aliases == nil {
		return cmd, nil
	}

	// Syntax: <whitespace>.[A-Za-z0-9]+
	re := regexp.MustCompile("(\\.[[:alnum:]]+)")
	matches := re.FindAllStringIndex(cmd, -1)

	expanded := ""
	prevEnd := 0
	for _, match := range matches {
		expanded += cmd[prevEnd:match[0]]
		alias := cmd[match[0]:match[1]]
		if exp, ok := globalConfig.aliases[alias]; !ok {
			return "", fmt.Errorf("%s: alias unknown", alias)
		} else {
			ea, err := cli.ExpandAliases(exp)
			if err != nil {
				return "", err
			}

			expanded += ea
			prevEnd = match[1]
		}
	}
	expanded += cmd[prevEnd:]

	return expanded, nil
}

func (cli *CLIPane) ConsumeAircraftSelection(ac *Aircraft) bool {
	if ac != nil && len(cli.input.cmd) > 0 {
		cli.input.InsertAtCursor(" " + ac.Callsign())
		return true
	}
	return false
}

///////////////////////////////////////////////////////////////////////////
// Command implementations

func getCallsign(args []string) (string, []string) {
	if positionConfig.selectedAircraft != nil {
		return positionConfig.selectedAircraft.Callsign(), args
	} else {
		return args[0], args[1:]
	}
}

type SetACTypeCommand struct{}

func (*SetACTypeCommand) Name() string { return "actype" }
func (*SetACTypeCommand) Help() string {
	return "Sets the aircraft's type."
}
func (*SetACTypeCommand) Usage() string {
	return "<callsign> <type>"
}
func (*SetACTypeCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetACTypeCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	return "", amendFlightPlan(callsign, func(fp *FlightPlan) {
		fp.actype = strings.ToUpper(args[0])
	})
}

type SetAltitudeCommand struct {
	isTemporary bool
}

func (sa *SetAltitudeCommand) Name() string {
	if sa.isTemporary {
		return "tempalt"
	} else {
		return "alt"
	}
}
func (sa *SetAltitudeCommand) Usage() string {
	return "<callsign> <altitude>"
}
func (sa *SetAltitudeCommand) Help() string {
	if sa.isTemporary {
		return "Sets the aircraft's temporary clearance altitude."
	} else {
		return "Sets the aircraft's clearance altitude."
	}
}
func (*SetAltitudeCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (sa *SetAltitudeCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)

	altitude, err := strconv.Atoi(args[0])
	if err != nil {
		return "", err
	}
	if altitude < 1000 {
		altitude *= 100
	}

	if sa.isTemporary {
		err = server.SetTemporaryAltitude(callsign, altitude)
	} else {
		return "", amendFlightPlan(callsign, func(fp *FlightPlan) {
			fp.altitude = altitude
		})
	}
	return "", err
}

type SetArrivalCommand struct{}

func (*SetArrivalCommand) Name() string { return "arr" }
func (*SetArrivalCommand) Usage() string {
	return "<callsign> <airport>"
}
func (*SetArrivalCommand) Help() string {
	return "Sets the aircraft's arrival airport."
}
func (*SetArrivalCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetArrivalCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	if len(args[0]) > 5 {
		return "", ErrAirportTooLong
	}
	return "", amendFlightPlan(callsign, func(fp *FlightPlan) {
		fp.arrive = strings.ToUpper(args[0])
	})
}

type SetDepartureCommand struct{}

func (*SetDepartureCommand) Name() string { return "dep" }
func (*SetDepartureCommand) Usage() string {
	return "<callsign> <airport>"
}
func (*SetDepartureCommand) Help() string {
	return "Sets the aircraft's departure airport"
}
func (*SetDepartureCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetDepartureCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	if len(args[0]) > 5 {
		return "", ErrAirportTooLong
	}
	return "", amendFlightPlan(callsign, func(fp *FlightPlan) {
		fp.depart = strings.ToUpper(args[0])
	})
}

type SetEquipmentSuffixCommand struct{}

func (*SetEquipmentSuffixCommand) Name() string { return "equip" }
func (*SetEquipmentSuffixCommand) Usage() string {
	return "<callsign> <suffix>"
}
func (*SetEquipmentSuffixCommand) Help() string {
	return "Sets the aircraft's equipment suffix."
}
func (*SetEquipmentSuffixCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetEquipmentSuffixCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	if ac := server.GetAircraft(callsign); ac == nil {
		return "", ErrNoAircraftForCallsign
	} else if ac.flightPlan == nil {
		return "", ErrNoFlightPlanFiled
	} else {
		atype := ac.flightPlan.TypeWithoutSuffix()
		suffix := strings.ToUpper(args[0])
		if suffix[0] != '/' {
			suffix = "/" + suffix
		}
		ac.flightPlan.actype = atype + suffix
		return "", server.AmendFlightPlan(callsign, *ac.flightPlan)
	}
}

type SetIFRCommand struct{}

func (*SetIFRCommand) Name() string { return "ifr" }
func (*SetIFRCommand) Usage() string {
	return "<callsign>"
}
func (*SetIFRCommand) Help() string {
	return "Marks the aircraft as an IFR flight."
}
func (*SetIFRCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return nil
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*SetIFRCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	return "", amendFlightPlan(callsign, func(fp *FlightPlan) { fp.rules = IFR })
}

type SetScratchpadCommand struct{}

func (*SetScratchpadCommand) Name() string { return "scratchpad" }
func (*SetScratchpadCommand) Usage() string {
	return "<callsign> <contents--optional>"
}
func (*SetScratchpadCommand) Help() string {
	return "Sets the aircraft's scratchpad. If no contents are specified, the scratchpad is cleared."
}
func (*SetScratchpadCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsOptional}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString | CommandArgsOptional}
	}
}
func (*SetScratchpadCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	if len(args) == 0 {
		// clear scratchpad
		return "", server.SetScratchpad(callsign, "")
	} else {
		return "", server.SetScratchpad(callsign, strings.ToUpper(args[0]))
	}
}

type SetSquawkCommand struct{}

func (*SetSquawkCommand) Name() string { return "squawk" }
func (*SetSquawkCommand) Usage() string {
	return "<aircraft> <squawk--optional>"
}
func (*SetSquawkCommand) Help() string {
	return "Sets the aircraft's squawk code. If no code is provided and the aircraft is IFR, a code is assigned automatically."
}
func (*SetSquawkCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsOptional}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString | CommandArgsOptional}
	}
}
func (*SetSquawkCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	if len(args) == 0 {
		return "", server.SetSquawkAutomatic(callsign)
	} else {
		squawk, err := ParseSquawk(args[0])
		if err != nil {
			return "", err
		}
		return "", server.SetSquawk(callsign, squawk)
	}
}

type SetVoiceCommand struct{}

func (*SetVoiceCommand) Name() string { return "voice" }
func (*SetVoiceCommand) Usage() string {
	return "<aircraft> <voice type:v, r, or t>"
}
func (*SetVoiceCommand) Help() string {
	return "Sets the aircraft's voice communications type."
}
func (*SetVoiceCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString}
	}
}
func (*SetVoiceCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	return "", server.SetVoiceType(callsign, args[0])
}

type SetVFRCommand struct{}

func (*SetVFRCommand) Name() string { return "vfr" }
func (*SetVFRCommand) Usage() string {
	return "<callsign>"
}
func (*SetVFRCommand) Help() string {
	return "Marks the aircraft as a VFR flight."
}
func (*SetVFRCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*SetVFRCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	return "", amendFlightPlan(callsign, func(fp *FlightPlan) { fp.rules = VFR })
}

type EditRouteCommand struct{}

func (*EditRouteCommand) Name() string { return "editroute" }
func (*EditRouteCommand) Usage() string {
	return "<callsign>"
}
func (*EditRouteCommand) Help() string {
	return "Loads the aircraft's route into the command buffer for editing using the \"route\" command."
}
func (*EditRouteCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return nil
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*EditRouteCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	ac := server.GetAircraft(callsign)
	if ac == nil {
		return "", ErrNoAircraftForCallsign
	}
	if ac.flightPlan == nil {
		return "", ErrNoFlightPlan
	}

	cli.input.cmd = "route " + callsign + " "
	cli.input.cursor = len(cli.input.cmd)
	cli.input.cmd += ac.flightPlan.route

	return "", nil
}

type NYPRDEntry struct {
	Id            int       `json:"id"`
	AirportOrigin string    `json:"airport_origin"`
	AirportDest   string    `json:"airport_dest"`
	Route         string    `json:"route"`
	Hours1        string    `json:"hours1"`
	Hours2        string    `json:"hours2"`
	Hours3        string    `json:"hours3"`
	RouteType     string    `json:"route_type"`
	Area          string    `json:"area"`
	Altitude      string    `json:"altitude"`
	Aircraft      string    `json:"aircraft"`
	Direction     string    `json:"direction"`
	Seq           string    `json:"seq"`
	CenterOrigin  string    `json:"center_origin"`
	CenterDest    string    `json:"center_dest"`
	IsLocal       int       `json:"is_local"`
	Created       time.Time `json:"created_at"`
	Updated       time.Time `json:"updated_at"`
}

type NYPRDCommand struct{}

func (*NYPRDCommand) Name() string { return "nyprd" }
func (*NYPRDCommand) Usage() string {
	return "<callsign>"
}
func (*NYPRDCommand) Help() string {
	return "Looks up the aircraft's route in the ZNY preferred route database."
}
func (*NYPRDCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*NYPRDCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	ac := server.GetAircraft(callsign)
	if ac == nil {
		return "", ErrNoAircraftForCallsign
	}
	if ac.flightPlan == nil {
		return "", ErrNoFlightPlan
	}

	depart, arrive := ac.flightPlan.depart, ac.flightPlan.arrive
	url := fmt.Sprintf("https://nyartcc.org/prd/search?depart=%s&arrive=%s", depart, arrive)

	resp, err := http.Get(url)
	if err != nil {
		lg.Printf("PRD get err: %+v", err)
		return "", fmt.Errorf("nyprd: network error")
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	var prdEntries []NYPRDEntry
	if err := decoder.Decode(&prdEntries); err != nil {
		lg.Errorf("PRD decode err: %+v", err)
		return "", fmt.Errorf("error decoding PRD entry")
	}

	if len(prdEntries) == 0 {
		return "", fmt.Errorf("no PRD found for route from %s to %s", depart, arrive)
	}

	anyType := false
	anyArea := false
	anyAlt := false
	anyAC := false
	for _, entry := range prdEntries {
		anyType = anyType || (entry.RouteType != "")
		anyArea = anyArea || (entry.Area != "")
		anyAlt = anyAlt || (entry.Altitude != "")
		anyAC = anyAC || (entry.Aircraft != "")
	}

	var result strings.Builder
	w := tabwriter.NewWriter(&result, 0 /* min width */, 1 /* tab width */, 1 /* padding */, ' ', 0)
	w.Write([]byte("\tORG\tDST\t"))
	writeIf := func(b bool, s string) {
		if b {
			w.Write([]byte(s))
		}
	}

	writeIf(anyType, "TYPE\t")
	writeIf(anyArea, "AREA\t")
	writeIf(anyAlt, "ALT\t")
	writeIf(anyAC, "A/C\t")
	w.Write([]byte("ROUTE\n"))

	print := func(entry NYPRDEntry) {
		w.Write([]byte(entry.AirportOrigin + "\t" + entry.AirportDest + "\t"))
		writeIf(anyType, entry.RouteType+"\t")
		writeIf(anyArea, entry.Area+"\t")
		writeIf(anyAlt, entry.Altitude+"\t")
		writeIf(anyAC, entry.Aircraft+"\t")
		w.Write([]byte(entry.Route + "\n"))
	}

	// Print the required ones first, with an asterisk
	for _, entry := range prdEntries {
		if entry.IsLocal == 0 {
			continue
		}
		w.Write([]byte("*\t"))
		print(entry)
	}
	for _, entry := range prdEntries {
		if entry.IsLocal != 0 {
			continue
		}
		w.Write([]byte("\t"))
		print(entry)
	}
	w.Flush()

	return result.String(), nil
}

type PRDCommand struct{}

func (*PRDCommand) Name() string { return "faaprd" }
func (*PRDCommand) Usage() string {
	return "<callsign>"
}
func (*PRDCommand) Help() string {
	return "Looks up the aircraft's route in the FAA preferred route database."
}
func (*PRDCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*PRDCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	ac := server.GetAircraft(callsign)
	if ac == nil {
		return "", ErrNoAircraftForCallsign
	}
	if ac.flightPlan == nil {
		return "", ErrNoFlightPlan
	}

	depart, arrive := ac.flightPlan.depart, ac.flightPlan.arrive
	if len(depart) == 4 && depart[0] == 'K' {
		depart = depart[1:]
	}
	if len(arrive) == 4 && arrive[0] == 'K' {
		arrive = arrive[1:]
	}

	if prdEntries, ok := database.FAA.prd[AirportPair{depart, arrive}]; !ok {
		return "", fmt.Errorf(depart + "-" + arrive + ": no entry in FAA PRD")
	} else {
		anyType := false
		anyHour1, anyHour2, anyHour3 := false, false, false
		anyAC := false
		anyAlt, anyDir := false, false
		for _, entry := range prdEntries {
			anyType = anyType || (entry.Type != "")
			anyHour1 = anyHour1 || (entry.Hours[0] != "")
			anyHour2 = anyHour2 || (entry.Hours[1] != "")
			anyHour3 = anyHour3 || (entry.Hours[2] != "")
			anyAC = anyAC || (entry.Aircraft != "")
			anyAlt = anyAlt || (entry.Altitude != "")
			anyDir = anyDir || (entry.Direction != "")
		}

		var result strings.Builder
		w := tabwriter.NewWriter(&result, 0 /* min width */, 1 /* tab width */, 1 /* padding */, ' ', 0)
		w.Write([]byte("NUM\tORG\tDST\t"))

		writeIf := func(b bool, s string) {
			if b {
				w.Write([]byte(s))
			}
		}
		writeIf(anyType, "TYPE\t")
		writeIf(anyHour1, "HOUR1\t")
		writeIf(anyHour2, "HOUR2\t")
		writeIf(anyHour3, "HOUR3\t")
		writeIf(anyAC, "A/C\t")
		writeIf(anyAlt, "ALT\t")
		writeIf(anyDir, "DIR\t")
		w.Write([]byte("ROUTE\n"))

		for _, entry := range prdEntries {
			w.Write([]byte(entry.Seq + "\t" + entry.Depart + "\t" + entry.Arrive + "\t"))
			writeIf(anyType, entry.Type+"\t")
			writeIf(anyHour1, entry.Hours[0]+"\t")
			writeIf(anyHour2, entry.Hours[1]+"\t")
			writeIf(anyHour3, entry.Hours[2]+"\t")
			writeIf(anyAC, entry.Aircraft+"\t")
			writeIf(anyAlt, entry.Altitude+"\t")
			writeIf(anyDir, entry.Direction+"\t")
			w.Write([]byte(entry.Route + "\n"))
		}
		w.Flush()

		return result.String(), nil
	}
}

type SetRouteCommand struct{}

func (*SetRouteCommand) Name() string { return "route" }
func (*SetRouteCommand) Usage() string {
	return "<callsign> <route...>"
}
func (*SetRouteCommand) Help() string {
	return "Sets the specified aircraft's route to the one provided."
}
func (*SetRouteCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsMultiple}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsString | CommandArgsMultiple}
	}
}
func (*SetRouteCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	return "", amendFlightPlan(callsign, func(fp *FlightPlan) {
		fp.route = strings.ToUpper(strings.Join(args, " "))
	})
}

type AcceptHandoffCommand struct{}

func (*AcceptHandoffCommand) Name() string { return "accept" }
func (*AcceptHandoffCommand) Usage() string {
	return "<callsign>"
}
func (*AcceptHandoffCommand) Help() string {
	return "Accepts the requested handoff of the specified aircraft."
}
func (*AcceptHandoffCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*AcceptHandoffCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	return "", server.AcceptHandoff(callsign)
}

type DropTrackCommand struct{}

func (*DropTrackCommand) Name() string { return "drop" }
func (*DropTrackCommand) Usage() string {
	return "<callsign>"
}
func (*DropTrackCommand) Help() string {
	return "Drops the track on the selected aircraft."
}
func (*DropTrackCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*DropTrackCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	return "", server.DropTrack(callsign)
}

type HandoffCommand struct{}

func (*HandoffCommand) Name() string { return "handoff" }
func (*HandoffCommand) Usage() string {
	return "<callsign> <controller>"
}
func (*HandoffCommand) Help() string {
	return "Hands off the specified aircraft to the specified controller."
}
func (*HandoffCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsController}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsController}
	}
}
func (*HandoffCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	return "", server.Handoff(callsign, args[0])
}

type PointOutCommand struct{}

func (*PointOutCommand) Name() string { return "pointout" }
func (*PointOutCommand) Usage() string {
	return "<callsign> <controller>"
}
func (*PointOutCommand) Help() string {
	return "Points the specified aircraft out to the specified controller."
}
func (*PointOutCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsController}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft, CommandArgsController}
	}
}
func (*PointOutCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, args := getCallsign(args)
	return "", server.PointOut(callsign, args[0])
}

type RejectHandoffCommand struct{}

func (*RejectHandoffCommand) Name() string { return "reject" }
func (*RejectHandoffCommand) Usage() string {
	return "<callsign>"
}
func (*RejectHandoffCommand) Help() string {
	return "Rejects a handoff request for the specified aircraft."
}
func (*RejectHandoffCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*RejectHandoffCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	return "", server.RejectHandoff(callsign)
}

type TrackAircraftCommand struct{}

func (*TrackAircraftCommand) Name() string { return "track" }
func (*TrackAircraftCommand) Usage() string {
	return "<callsign>"
}
func (*TrackAircraftCommand) Help() string {
	return "Initiates a track on the specified aircraft."
}
func (*TrackAircraftCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*TrackAircraftCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	return "", server.InitiateTrack(callsign)
}

type FindCommand struct{}

func (*FindCommand) Name() string { return "find" }
func (*FindCommand) Usage() string {
	return "<callsign, fix, VOR, DME, airport...>"
}
func (*FindCommand) Help() string {
	return "Finds the specified object and highlights it in any radar scopes in which it is visible."
}
func (*FindCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsOptional}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft | CommandArgsString}
	}
}
func (*FindCommand) Run(cli *CLIPane, args []string) (string, error) {
	var pos Point2LL
	if len(args) == 0 && positionConfig.selectedAircraft != nil {
		pos = positionConfig.selectedAircraft.Position()
	} else {
		name := strings.ToUpper(args[0])

		aircraft := matchingAircraft(name)
		if len(aircraft) == 1 {
			pos = aircraft[0].Position()
		} else if len(aircraft) > 1 {
			callsigns := Map(aircraft, func(a *Aircraft) string { return a.Callsign() })
			return "", fmt.Errorf("Multiple aircraft match: " + strings.Join(callsigns, ", "))
		} else {
			var ok bool
			if pos, ok = database.Locate(name); !ok {
				return "", fmt.Errorf("%s: no matches found", args[0])
			}
		}
	}
	positionConfig.highlightedLocation = pos
	positionConfig.highlightedLocationEndTime = time.Now().Add(3 * time.Second)
	return "", nil
}

type MITCommand struct{}

func (*MITCommand) Name() string { return "mit" }
func (*MITCommand) Usage() string {
	return "<zero, one, or more callsigns...>"
}
func (*MITCommand) Help() string {
	return "With no callsigns, this clears the current miles in trail list. " +
		"Otherwise, the specified aircraft are added to it."
}
func (*MITCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsAircraft | CommandArgsMultiple}
}
func (*MITCommand) Run(cli *CLIPane, args []string) (string, error) {
	if len(args) == 0 {
		// clear it
		positionConfig.mit = nil
	} else {
		for _, callsign := range args {
			ac := server.GetAircraft(callsign)
			if ac == nil {
				return "", fmt.Errorf("%s: aircraft does not exist", callsign)
			}

			positionConfig.mit = append(positionConfig.mit, ac)
		}
	}

	result := "Current MIT list: "
	for _, ac := range positionConfig.mit {
		result += ac.Callsign() + " "
	}
	return result, nil
}

type DrawRouteCommand struct{}

func (*DrawRouteCommand) Name() string { return "drawroute" }
func (*DrawRouteCommand) Usage() string {
	return "<callsign>"
}
func (*DrawRouteCommand) Help() string {
	return "Draws the route of the specified aircraft in any radar scopes in which it is visible."
}
func (*DrawRouteCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return nil
	} else {
		return []CommandArgsFormat{CommandArgsAircraft}
	}
}
func (*DrawRouteCommand) Run(cli *CLIPane, args []string) (string, error) {
	var ac *Aircraft
	if len(args) == 0 {
		ac = positionConfig.selectedAircraft
	} else {
		aircraft := matchingAircraft(strings.ToUpper(args[0]))
		if len(aircraft) == 1 {
			ac = aircraft[0]
		} else if len(aircraft) > 1 {
			callsigns := Map(aircraft, func(a *Aircraft) string { return a.Callsign() })
			return "", fmt.Errorf("Multiple aircraft match: " + strings.Join(callsigns, ", "))
		} else {
			return "", fmt.Errorf("%s: no matches found", args[0])
		}
	}
	if ac.flightPlan == nil {
		return "", ErrNoFlightPlan
	}

	positionConfig.drawnRoute = ac.flightPlan.depart + " " + ac.flightPlan.route + " " +
		ac.flightPlan.arrive
	positionConfig.drawnRouteEndTime = time.Now().Add(5 * time.Second)
	return "", nil
}

type InfoCommand struct{}

func (*InfoCommand) Name() string { return "i" }
func (*InfoCommand) Usage() string {
	return "<callsign, fix, VOR, DME, airport...>"
}
func (*InfoCommand) Help() string {
	return "Prints available information about the specified object."
}
func (*InfoCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	if isAircraftSelected {
		return []CommandArgsFormat{CommandArgsString | CommandArgsOptional}
	} else {
		return []CommandArgsFormat{CommandArgsAircraft | CommandArgsString}
	}
}
func (*InfoCommand) Run(cli *CLIPane, args []string) (string, error) {
	acInfo := func(ac *Aircraft) string {
		var result string
		var indent int
		if ac.flightPlan == nil {
			result = ac.Callsign() + ": no flight plan filed"
			indent = len(ac.Callsign()) + 1
		} else {
			result, indent = ac.GetFormattedFlightPlan(true)
			result = strings.TrimRight(result, "\n")
		}

		indstr := fmt.Sprintf("%*c", indent, ' ')
		if u := server.GetUser(ac.Callsign()); u != nil {
			result += fmt.Sprintf("\n%spilot: %s %s (%s)", indstr, u.name, u.rating, u.note)
		}
		if ac.flightPlan != nil {
			if tel := ac.Telephony(); tel != "" {
				result += fmt.Sprintf("\n%stele:  %s", indstr, tel)
			}
		}
		if c := server.GetTrackingController(ac.Callsign()); c != "" {
			result += fmt.Sprintf("\n%sTracked by: %s", indstr, c)
		}
		if c := server.InboundHandoffController(ac.Callsign()); c != "" {
			result += fmt.Sprintf("\n%sInbound handoff from %s", indstr, c)
		}
		if c := server.OutboundHandoffController(ac.Callsign()); c != "" {
			result += fmt.Sprintf("\n%sOutbound handoff from %s", indstr, c)
		}
		if ac.squawk != ac.assignedSquawk {
			result += fmt.Sprintf("\n%s*** Actual squawk: %s", indstr, ac.squawk)
		}
		if ac.LostTrack(server.CurrentTime()) {
			result += fmt.Sprintf("\n%s*** Lost Track!", indstr)
		}
		return result
	}

	if len(args) == 0 && positionConfig.selectedAircraft != nil {
		return acInfo(positionConfig.selectedAircraft), nil
	} else {
		name := strings.ToUpper(args[0])

		// e.g. "fft" matches both a VOR and a callsign, so report both...
		var info []string
		if navaid, ok := database.FAA.navaids[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s %s %s", name, stopShouting(navaid.name),
				navaid.navtype, navaid.location))
		}
		if fix, ok := database.FAA.fixes[name]; ok {
			info = append(info, fmt.Sprintf("%s: Fix %s", name, fix.location))
		}
		if ap, ok := database.FAA.airports[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s: %s, alt %d", name, stopShouting(ap.name),
				ap.location, ap.elevation))
		}
		if cs, ok := database.callsigns[name]; ok {
			info = append(info, fmt.Sprintf("%s: %s (%s)", name, cs.telephony, cs.company))
		}
		if ct := server.GetController(name); ct != nil {
			info = append(info, fmt.Sprintf("%s (%s) @ %7.3f, range %d", ct.callsign,
				ct.rating, ct.frequency, ct.scopeRange))
			if u := server.GetUser(name); u != nil {
				info = append(info, fmt.Sprintf("%s %s (%s)", u.name, u.rating, u.note))
			}
		}

		if len(info) > 0 {
			return strings.Join(info, "\n"), nil
		}

		aircraft := matchingAircraft(name)
		if len(aircraft) == 1 {
			return acInfo(aircraft[0]), nil
		} else if len(aircraft) > 1 {
			callsigns := Map(aircraft, func(a *Aircraft) string { return a.Callsign() })
			return "", fmt.Errorf("Multiple aircraft match: " + strings.Join(callsigns, ", "))
		} else {
			return "", fmt.Errorf("%s: unknown", name)
		}
	}
}

type TrafficCommand struct{}

func (*TrafficCommand) Name() string { return "traffic" }
func (*TrafficCommand) Usage() string {
	return "<callsign>"
}
func (*TrafficCommand) Help() string {
	return "Summarizes information related to nearby traffic for the specified aircraft."
}
func (*TrafficCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsAircraft}
}
func (*TrafficCommand) Run(cli *CLIPane, args []string) (string, error) {
	callsign, _ := getCallsign(args)
	ac := server.GetAircraft(callsign)
	if ac == nil {
		return "", fmt.Errorf("%s: aircraft does not exist", callsign)
	}

	type Traffic struct {
		ac       *Aircraft
		distance float32
	}
	now := server.CurrentTime()
	filter := func(a *Aircraft) bool {
		return a.Callsign() == ac.Callsign() || a.LostTrack(now) || a.OnGround()
	}

	lateralLimit := float32(6.)
	verticalLimit := 1500

	var traffic []Traffic
	for _, other := range server.GetFilteredAircraft(filter) {
		ldist := nmdistance2ll(ac.Position(), other.Position())
		vdist := abs(ac.Altitude() - other.Altitude())
		if ldist < lateralLimit && vdist < verticalLimit {
			traffic = append(traffic, Traffic{other, ldist})
		}
	}

	sort.Slice(traffic, func(i, j int) bool {
		if traffic[i].distance == traffic[j].distance {
			return traffic[i].ac.Callsign() < traffic[j].ac.Callsign()
		}
		return traffic[i].distance < traffic[j].distance
	})

	str := ""
	for _, t := range traffic {
		alt := (t.ac.Altitude() + 250) / 500 * 500
		hto := headingp2ll(ac.Position(), t.ac.Position(), database.MagneticVariation)
		hdiff := hto - ac.Heading()
		clock := headingAsHour(hdiff)
		actype := "???"
		if t.ac.flightPlan != nil {
			actype = t.ac.flightPlan.actype
		}
		str += fmt.Sprintf("  %-10s %2d o'c %2d mi %2s bound %-10s %5d' [%s]\n",
			ac.Callsign(), clock, int(t.distance+0.5),
			shortCompass(t.ac.Heading()), actype, int(alt), t.ac.Callsign())
	}
	if str != "" {
		str += "\n"
	}

	return str, nil
}

type TimerCommand struct{}

func (*TimerCommand) Name() string { return "timer" }
func (*TimerCommand) Usage() string {
	return "<minutes> <message...>"
}
func (*TimerCommand) Help() string {
	return "Starts a timer for the specified number of minutes with the associated message."
}
func (*TimerCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*TimerCommand) Run(cli *CLIPane, args []string) (string, error) {
	if minutes, err := strconv.ParseFloat(args[0], 64); err != nil {
		return "", fmt.Errorf("\"%s\": expected time in minutes", args[0])
	} else {
		end := time.Now().Add(time.Duration(minutes * float64(time.Minute)))
		timer := TimerReminderItem{end: end, note: strings.Join(args[1:], " ")}

		positionConfig.timers = append(positionConfig.timers, timer)
		sort.Slice(positionConfig.timers, func(i, j int) bool {
			return positionConfig.timers[i].end.Before(positionConfig.timers[j].end)
		})

		return "", nil
	}
}

type ToDoCommand struct{}

func (*ToDoCommand) Name() string { return "todo" }
func (*ToDoCommand) Usage() string {
	return "<message...>"
}
func (*ToDoCommand) Help() string {
	return "Adds a todo with the associated message to the todo list."
}
func (*ToDoCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*ToDoCommand) Run(cli *CLIPane, args []string) (string, error) {
	note := strings.Join(args[0:], " ")
	positionConfig.todos = append(positionConfig.todos, ToDoReminderItem{note: note})
	return "", nil
}

type EchoCommand struct{}

func (*EchoCommand) Name() string { return "echo" }
func (*EchoCommand) Usage() string {
	return "<message...>"
}
func (*EchoCommand) Help() string {
	return "Prints the parameters given to it."
}
func (*EchoCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*EchoCommand) Run(cli *CLIPane, args []string) (string, error) {
	return strings.Join(args, " "), nil
}

type WallopCommand struct{}

func (*WallopCommand) Name() string { return "wallop" }
func (*WallopCommand) Usage() string {
	return "[message]"
}
func (*WallopCommand) Help() string {
	return "Send the specified message to all online supervisors."
}
func (*WallopCommand) Syntax(isAircraftSelected bool) []CommandArgsFormat {
	return []CommandArgsFormat{CommandArgsString, CommandArgsString | CommandArgsMultiple}
}
func (*WallopCommand) Run(cli *CLIPane, args []string) (string, error) {
	tm := TextMessage{messageType: TextWallop, contents: strings.Join(args, " ")}
	return "", server.SendTextMessage(tm)
}
