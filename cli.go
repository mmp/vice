// cli.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

func ErrorConsoleEntry(err error) []*ConsoleEntry {
	if err == nil {
		return nil
	}
	return ErrorStringConsoleEntry(err.Error())
}

func ErrorStringConsoleEntry(err string) []*ConsoleEntry {
	if err == "" {
		return nil
	}
	e := &ConsoleEntry{text: []string{err}, style: []ConsoleTextStyle{ConsoleTextError}}
	return []*ConsoleEntry{e}
}

func StringConsoleEntry(s string) []*ConsoleEntry {
	if s == "" {
		return nil
	}
	var entries []*ConsoleEntry
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			e := &ConsoleEntry{text: []string{line}, style: []ConsoleTextStyle{ConsoleTextRegular}}
			entries = append(entries, e)
		}
	}
	return entries
}

func (e *ConsoleEntry) Draw(p [2]float32, style TextStyle, cs *ColorScheme) *TextDrawBuilder {
	t := &TextDrawBuilder{}
	for i := range e.text {
		switch e.style[i] {
		case ConsoleTextRegular:
			style.Color = cs.Text

		case ConsoleTextEmphasized:
			style.Color = cs.TextHighlight

		case ConsoleTextError:
			style.Color = cs.TextError
		}

		t.AddText(e.text[i], p, style)
		if i < len(e.text)-1 {
			bx, _ := style.Font.BoundText(e.text[i], 0)
			p[0] += float32(bx)
		}
	}
	return t
}

const consoleLimit = 500

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

	console           *RingBuffer[*ConsoleEntry]
	consoleViewOffset int // lines from the end (for pgup/down)
	errorCount        map[string]int

	FontIdentifier FontIdentifier
	font           *Font

	input    CLIInput
	status   string
	eventsId EventSubscriberId

	messageReplyRecipients [10]*TextMessage
	nextMessageReplyId     int

	// fkey stuff
	activeFKeyCommand FKeyCommand
	fKeyFocusField    int      // which input field is focused
	fKeyCursorPos     int      // cursor position in the current input field
	fKeyArgs          []string // user input for each command argument
	fKeyArgErrors     []string

	cb CommandBuffer
}

func NewCLIPane() *CLIPane {
	return &CLIPane{}
}

func (cli *CLIPane) Duplicate(nameAsCopy bool) Pane {
	return &CLIPane{
		FontIdentifier: cli.FontIdentifier,
		font:           cli.font,
		console:        NewRingBuffer[*ConsoleEntry](consoleLimit),
		errorCount:     make(map[string]int),
		eventsId:       eventStream.Subscribe(),
	}
}

func (cli *CLIPane) Activate() {
	if cli.font = GetFont(cli.FontIdentifier); cli.font == nil {
		cli.font = GetDefaultFont()
		cli.FontIdentifier = cli.font.id
	}
	if cli.errorCount == nil {
		cli.errorCount = make(map[string]int)
	}
	if cli.console == nil {
		cli.console = NewRingBuffer[*ConsoleEntry](consoleLimit)
	}
	if *devmode {
		lg.RegisterErrorMonitor(cli)
	}

	cli.eventsId = eventStream.Subscribe()

	checkCommands(cliCommands)
}

func (cli *CLIPane) Deactivate() {
	lg.DeregisterErrorMonitor(cli)
	eventStream.Unsubscribe(cli.eventsId)
	cli.eventsId = InvalidEventSubscriberId
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

func (cli *CLIPane) CanTakeKeyboardFocus() bool { return true }

func (cli *CLIPane) processEvents(es *EventStream) {
	for _, event := range es.Get(cli.eventsId) {
		switch v := event.(type) {
		case *SelectedAircraftEvent:
			if cli.activeFKeyCommand != nil {
				// If the user selected an aircraft after initiating an fkey
				// command, use the aircraft regardless of whether the command
				// things it's valid; assume the user knows what they are doing
				// and that it will be valid when the command executes.  (And
				// if it's not, an error will be issued then!)
				cli.setFKeyAircraft(v.ac.Callsign, false)
			}

		case *PointOutEvent:
			cli.AddConsoleEntry([]string{v.controller, ": point out " + v.ac.Callsign},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})

		case *OfferedHandoffEvent:
			cli.AddConsoleEntry([]string{v.controller, ": offered handoff " + v.ac.Callsign},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})

		case *AcceptedHandoffEvent:
			cli.AddConsoleEntry([]string{v.controller, ": accepted handoff " + v.ac.Callsign},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})

		case *RejectedHandoffEvent:
			cli.AddConsoleEntry([]string{v.controller, ": rejected handoff " + v.ac.Callsign},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})

		case *CanceledHandoffEvent:
			cli.AddConsoleEntry([]string{v.controller, ": canceled handoff offer " + v.ac.Callsign},
				[]ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular})

		case *TextMessageEvent:
			m := v.message

			recordMessage := func(mtype string) {
				time := server.CurrentTime().UTC().Format("15:04:05Z")
				var id string
				if i := cli.getReplyId(m); i >= 0 {
					id = fmt.Sprintf("/%d ", i)
				}

				sendRecip := m.sender
				if mtype != "" {
					sendRecip += FontAwesomeIconArrowRight + mtype
				}
				cli.AddConsoleEntry([]string{id, "[" + time + "] " + sendRecip + ": ", m.contents},
					[]ConsoleTextStyle{ConsoleTextRegular, ConsoleTextEmphasized, ConsoleTextRegular})
			}
			switch m.messageType {
			case TextBroadcast:
				recordMessage("BROADCAST")

			case TextWallop:
				recordMessage("WALLOP")

			case TextATC:
				recordMessage("ATC")

			case TextFrequency:
				if fm := positionConfig.MonitoredFrequencies(m.frequencies); len(fm) > 0 {
					freq := strings.Join(MapSlice(fm, func(f Frequency) string { return f.String() }), ", ")
					recordMessage(freq)
				}

			case TextPrivate:
				if strings.ToUpper(m.sender) == "SERVER" {
					// a "DM" from the server isn't the same as a regular DM...
					recordMessage("")
				} else {
					recordMessage(server.Callsign())
				}
			}
		}
	}
}

func (cli *CLIPane) getReplyId(m *TextMessage) int {
	if strings.ToUpper(m.sender) == "SERVER" {
		return -1
	}

	// Pre-populate a TextMessage with the recipient for a reply to the
	// given message.
	var recip TextMessage
	switch m.messageType {
	case TextBroadcast, TextWallop, TextPrivate:
		recip.messageType = TextPrivate
		recip.recipient = m.sender

	case TextATC:
		recip.messageType = TextATC

	case TextFrequency:
		recip.messageType = TextFrequency
		recip.frequencies = DuplicateSlice(m.frequencies)
	}

	// Is it there already?
	for i, prev := range cli.messageReplyRecipients {
		if prev == nil || prev.messageType != recip.messageType {
			continue
		}
		switch prev.messageType {
		case TextPrivate:
			if prev.recipient == recip.recipient {
				return i
			}

		case TextATC:
			return i

		case TextFrequency:
			if SliceEqual(prev.frequencies, recip.frequencies) {
				return i
			}
		}
	}

	id := cli.nextMessageReplyId
	cli.messageReplyRecipients[id] = &recip
	cli.nextMessageReplyId = (cli.nextMessageReplyId + 1) % 10
	return id
}

func (cli *CLIPane) Name() string { return "Command Line Interface" }

func (cli *CLIPane) DrawUI() {
	if newFont, changed := DrawFontPicker(&cli.FontIdentifier, "Font"); changed {
		cli.font = newFont
	}
}

func (cli *CLIPane) Draw(ctx *PaneContext, cb *CommandBuffer) {
	cli.processEvents(ctx.events)

	cli.cb.Reset()
	ctx.SetWindowCoordinateMatrices(&cli.cb)

	if ctx.mouse != nil && ctx.mouse.Clicked[MouseButtonPrimary] {
		wmTakeKeyboardFocus(cli, false)
	}

	style := TextStyle{Font: cli.font, LineSpacing: 1, Color: ctx.cs.Text}
	cursorStyle := TextStyle{Font: cli.font, LineSpacing: 0,
		Color: ctx.cs.Background, DrawBackground: true, BackgroundColor: ctx.cs.Text}
	statusStyle := TextStyle{Font: cli.font, LineSpacing: 0, Color: ctx.cs.TextError}
	lineHeight := float32(style.Font.size + style.LineSpacing)

	// Draw the console buffer.
	// Save some space for top/bottom padding and the input and the status line.
	consoleLinesVisible := int((ctx.paneExtent.Height() - 3*lineHeight) / lineHeight)

	// Process user input
	if ctx.haveFocus && ctx.keyboard != nil {
		cli.processFKeys(ctx.keyboard)

		// If an f-key command is active, it takes priority and we won't
		// try to run a normal command.
		if cli.activeFKeyCommand == nil {
			prevCallsign := ""
			if positionConfig.selectedAircraft != nil {
				prevCallsign = positionConfig.selectedAircraft.Callsign
			}

			// Execute command if enter was typed
			hitEnter := cli.updateInput(consoleLinesVisible, ctx.keyboard)
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

						output := cli.runCommand(cmd)

						// Add the command and its output to the console history
						prompt := &ConsoleEntry{text: []string{""}, style: []ConsoleTextStyle{ConsoleTextRegular}}
						if prevCallsign != "" {
							prompt.text[0] = prevCallsign + "> " + cmd
						} else {
							prompt.text[0] = "> " + cmd
						}
						cli.console.Add(prompt)
						cli.console.Add(output...)
					}

					cli.consoleViewOffset = 0
					cli.historyOffset = 0
					cli.status = ""
				}
			}
		}
	}

	// Draw the console history above the command prompt
	left := float32(cli.font.size) / 2
	y := (float32(consoleLinesVisible) + 2.5) * lineHeight // 2.5 for the stuff below
	for i := 0; i < consoleLinesVisible; i++ {
		idx := cli.console.Size() - 1 - cli.consoleViewOffset - consoleLinesVisible + 1 + i
		if idx >= 0 {
			td := cli.console.Get(idx).Draw([2]float32{left, y}, style, ctx.cs)
			td.GenerateCommands(&cli.cb)
		}
		y -= lineHeight
	}

	// Draw text for the input, one line above the status line
	inputPos := [2]float32{left, 2.5 * lineHeight}
	if cli.activeFKeyCommand != nil {
		cli.drawFKeyStatus(inputPos, ctx)
	} else {
		cli.input.EmitDrawCommands(inputPos, style, cursorStyle, ctx.haveFocus, &cli.cb)
	}

	// status
	if cli.status != "" {
		td := GetTextDrawBuilder()
		defer ReturnTextDrawBuilder(td)
		// Half line of spacing below it
		td.AddText(cli.status, [2]float32{left, 1.5 * lineHeight}, statusStyle)
		td.GenerateCommands(&cli.cb)
	}

	cb.Call(cli.cb)
}

func (cli *CLIPane) processFKeys(keyboard *KeyboardState) {
	// See if any of the F-keys are pressed
	for i := 1; i <= 12; i++ {
		if keyboard.IsPressed(Key(KeyF1 - 1 + i)) {
			// Figure out which FKeyCommand is bound to the f-key, if any.
			var cmd string
			if keyboard.IsPressed(KeyShift) {
				if cmd = positionConfig.ShiftFKeyMappings[i]; cmd == "" {
					cli.status = "No command bound to shift-F" + fmt.Sprintf("%d", i)
				}
			} else {
				if cmd = positionConfig.FKeyMappings[i]; cmd == "" {
					cli.status = "No command bound to F" + fmt.Sprintf("%d", i)
				}
			}

			// If there's a command associated with the pressed f-key, set
			// things up to get its argument values from the user.
			if cmd != "" {
				cli.activeFKeyCommand = allFKeyCommands[cmd]
				if cli.activeFKeyCommand == nil {
					// This shouldn't happen unless the config.json file is
					// corrupt or a key used in the allFKeyCommands map has
					// changed.
					lg.Errorf(cmd + ": no f-key command of that name")
				} else {
					// Set things up to get the arguments for this command.
					cli.fKeyArgs = make([]string, len(cli.activeFKeyCommand.ArgTypes()))
					cli.fKeyArgErrors = make([]string, len(cli.activeFKeyCommand.ArgTypes()))
					cli.status = ""
					cli.fKeyFocusField = 0
					cli.fKeyCursorPos = 0

					if positionConfig.selectedAircraft != nil {
						// If an aircraft is currently selected, try using it for the command.
						// However, if it's invalid (e.g., the command is drop track, but we're
						// not tracking it, then don't force it...)
						cli.setFKeyAircraft(positionConfig.selectedAircraft.Callsign, true)
					}
				}
			}
		}
	}

	if keyboard.IsPressed(KeyEscape) {
		// Clear out the current command.
		cli.activeFKeyCommand = nil
		cli.status = ""
	}
}

func (cli *CLIPane) setFKeyAircraft(callsign string, mustMatch bool) {
	for i, ty := range cli.activeFKeyCommand.ArgTypes() {
		// Look for a command argument that takes an aircraft callsign.
		if _, ok := ty.(*AircraftCommandArg); ok {
			if mustMatch {
				// Make sure that the aircraft fulfills the arg's
				// requirements. (The cs != callsign check should be
				// unnecessary, but...)
				if cs, err := ty.Expand(callsign); err != nil || cs != callsign {
					continue
				}
			}

			cli.fKeyArgs[i] = callsign
			cli.fKeyArgErrors[i] = ""
			if cli.fKeyFocusField == i {
				if len(cli.fKeyArgs) > 0 {
					// If the cursor is currently in the input
					// field for the callsign, then skip to the
					// next field, if there is another one.
					cli.fKeyFocusField = (cli.fKeyFocusField + 1) % len(cli.fKeyArgs)
					cli.fKeyCursorPos = 0
				} else {
					// Otherwise move the cursor to the end of the input.
					cli.fKeyCursorPos = len(cli.fKeyArgs[i])
				}
			}
			break
		}
	}
}

func (cli *CLIPane) drawFKeyStatus(textp [2]float32, ctx *PaneContext) {
	if cli.activeFKeyCommand == nil {
		return
	}

	// Draw lines to delineate the top and bottom of the status bar
	ld := GetColoredLinesDrawBuilder()
	defer ReturnColoredLinesDrawBuilder(ld)

	ld.AddLine([2]float32{5, 1}, [2]float32{ctx.paneExtent.p1[0] - 5, 1}, ctx.cs.UIControl)
	h := ctx.paneExtent.Height() - 1
	ld.AddLine([2]float32{5, h}, [2]float32{ctx.paneExtent.p1[0] - 5, h}, ctx.cs.UIControl)
	cli.cb.LineWidth(1)
	ld.GenerateCommands(&cli.cb)

	cursorStyle := TextStyle{Font: ui.font, Color: ctx.cs.Background,
		DrawBackground: true, BackgroundColor: ctx.cs.Text}
	textStyle := TextStyle{Font: ui.font, Color: ctx.cs.Text}
	inputStyle := TextStyle{Font: ui.font, Color: ctx.cs.TextHighlight}
	errorStyle := TextStyle{Font: ui.font, Color: ctx.cs.TextError}

	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)

	// Command description
	textp = td.AddText(cli.activeFKeyCommand.Name(), textp, textStyle)

	// Draw text for all of the arguments, including both the prompt and the current value.
	argTypes := cli.activeFKeyCommand.ArgTypes()
	var textEditResult int
	for i, arg := range cli.fKeyArgs {
		// Prompt for the argument.
		textp = td.AddText(" "+argTypes[i].Prompt()+": ", textp, textStyle)

		if i == cli.fKeyFocusField && ctx.haveFocus {
			// If this argument currently has the cursor, draw a text editing field and handle
			// keyboard events.
			textEditResult, textp = uiDrawTextEdit(&cli.fKeyArgs[cli.fKeyFocusField], &cli.fKeyCursorPos,
				ctx.keyboard, textp, inputStyle, cursorStyle, &cli.cb)
			// All of the commands expect upper-case args, so always ensure that immediately.
			cli.fKeyArgs[cli.fKeyFocusField] = strings.ToUpper(cli.fKeyArgs[cli.fKeyFocusField])
		} else {
			// Otherwise it's an unfocused argument. If it's currently an
			// empty string, draw an underbar.
			if arg == "" {
				textp = td.AddText("_", textp, inputStyle)
			} else {
				textp = td.AddText(arg, textp, inputStyle)
			}
		}

		// If the user tried to run the command and there was an issue
		// related to this argument, print the error message.
		if cli.fKeyArgErrors[i] != "" {
			textp = td.AddText(" "+cli.fKeyArgErrors[i]+" ", textp, errorStyle)
		}

		// Expand the argument and see how many completions we find.
		completion, err := argTypes[i].Expand(arg)
		if err == nil {
			if completion != arg {
				// We have a single completion that is different than what the user typed;
				// draw an arrow and the completion text so the user can
				// see what will actually be used.
				textp = td.AddText(" "+FontAwesomeIconArrowRight+" "+completion, textp, textStyle)
			}
		} else {
			// Completions are implicitly validated so if there are none the user input is
			// not valid and if there are multiple it's ambiguous; either way, indicate
			// the input is not valid.
			textp = td.AddText(" "+FontAwesomeIconExclamationTriangle+" ", textp, errorStyle)
		}
	}

	// Handle changes in focus, etc., based on the input to the text edit
	// field.
	switch textEditResult {
	case TextEditReturnNone:
		// nothing

	case TextEditReturnTextChanged:
		// The user input changed, so clear out any error message since it
		// may no longer be valid.
		cli.status = ""
		cli.fKeyArgErrors = make([]string, len(cli.fKeyArgErrors))

	case TextEditReturnEnter:
		// The user hit enter; try to run the command

		// Run completion on all of the arguments; this also checks their validity.
		var completedArgs []string
		argTypes := cli.activeFKeyCommand.ArgTypes()
		cli.status = ""
		anyArgErrors := false
		for i, arg := range cli.fKeyArgs {
			if comp, err := argTypes[i].Expand(arg); err == nil {
				completedArgs = append(completedArgs, comp)
				cli.fKeyArgErrors[i] = ""
			} else {
				cli.fKeyArgErrors[i] = err.Error()
				anyArgErrors = true
			}
		}

		// Something went wrong, so don't try running the command.
		if anyArgErrors {
			break
		}

		err := cli.activeFKeyCommand.Do(completedArgs)
		if err != nil {
			// Failure. Grab the command's error message to display.
			cli.status = err.Error()
		} else {
			// Success; clear out the command.
			cli.activeFKeyCommand = nil
			cli.fKeyArgs = nil
			cli.fKeyArgErrors = nil
		}

	case TextEditReturnNext:
		// Go to the next input field.
		cli.fKeyFocusField = (cli.fKeyFocusField + 1) % len(cli.fKeyArgs)
		cli.fKeyCursorPos = len(cli.fKeyArgs[cli.fKeyFocusField])

	case TextEditReturnPrev:
		// Go to the previous input field.
		cli.fKeyFocusField = (cli.fKeyFocusField + len(cli.fKeyArgs) - 1) % len(cli.fKeyArgs)
		cli.fKeyCursorPos = len(cli.fKeyArgs[cli.fKeyFocusField])
	}

	td.GenerateCommands(&cli.cb)
}

func (ci *CLIInput) EmitDrawCommands(inputPos [2]float32, style TextStyle, cursorStyle TextStyle,
	haveFocus bool, cb *CommandBuffer) {
	td := GetTextDrawBuilder()
	defer ReturnTextDrawBuilder(td)
	prompt := ""
	if positionConfig.selectedAircraft != nil {
		prompt = positionConfig.selectedAircraft.Callsign
	}
	prompt = prompt + "> "
	if !haveFocus {
		// Don't draw the cursor if we don't have keyboard focus
		td.AddText(prompt+ci.cmd, inputPos, style)
	} else if ci.cursor == len(ci.cmd) {
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
	if len(ci.cmd) == 0 || len(ci.tabStops) == 0 {
		return false
	}

	start := ci.cursor
	pos := start
	for i := 0; i < len(ci.cmd); i++ {
		pos = (pos + step) % len(ci.cmd)
		if pos < 0 {
			pos += len(ci.cmd)
		}

		for _, stop := range ci.tabStops {
			if pos == stop {
				ci.cursor = stop
				return true
			}
		}
	}

	lg.Errorf("tab went all the way around without finding a parameter? cursor %d, stops %+v",
		ci.cursor, ci.tabStops)
	return false
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
	cli.console.Add(e)
}

func (cli *CLIPane) updateInput(consoleLinesVisible int, keyboard *KeyboardState) (hitEnter bool) {
	if keyboard == nil {
		return false
	}

	// Grab keyboard input
	cli.input.InsertAtCursor(keyboard.Input)

	if keyboard.IsPressed(KeyUpArrow) {
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
	if keyboard.IsPressed(KeyDownArrow) {
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

	if keyboard.IsPressed(KeyLeftArrow) {
		if cli.input.cursor > 0 {
			cli.input.cursor--
		}
	}
	if keyboard.IsPressed(KeyRightArrow) {
		if cli.input.cursor < len(cli.input.cmd) {
			cli.input.cursor++
		}
	}
	if keyboard.IsPressed(KeyHome) {
		cli.input.cursor = 0
	}
	if keyboard.IsPressed(KeyEnd) {
		cli.input.cursor = len(cli.input.cmd)
	}
	if keyboard.IsPressed(KeyBackspace) {
		cli.input.DeleteBeforeCursor()
	}
	if keyboard.IsPressed(KeyDelete) {
		cli.input.DeleteAfterCursor()
	}
	if keyboard.IsPressed(KeyEscape) {
		if cli.input.cursor > 0 {
			cli.input = CLIInput{}
			cli.status = ""
		} else {
			positionConfig.selectedAircraft = nil
		}
	}
	if keyboard.IsPressed(KeyTab) {
		if keyboard.IsPressed(KeyShift) {
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
	if keyboard.IsPressed(KeyPageUp) {
		// Keep one line from before
		cli.consoleViewOffset += consoleLinesVisible - 1
		// Don't go past the end
		if cli.consoleViewOffset > cli.console.Size()-consoleLinesVisible {
			cli.consoleViewOffset = cli.console.Size() - consoleLinesVisible
			if cli.consoleViewOffset < 0 {
				cli.consoleViewOffset = 0
			}
		}
		return
	}
	if keyboard.IsPressed(KeyPageDown) {
		cli.consoleViewOffset -= consoleLinesVisible - 1
		if cli.consoleViewOffset < 0 {
			cli.consoleViewOffset = 0
		}
		return
	}

	// Other than paging through history, everything henceforth changes the input.
	return keyboard.IsPressed(KeyEnter)
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
		return !ac.LostTrack(now) && strings.Contains(ac.Callsign, s)
	})
}

func lookupCommand(n string) CLICommand {
	for _, c := range cliCommands {
		for _, name := range c.Names() {
			if name == n {
				return c
			}
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
			acarg(func(ac *Aircraft) string { return ac.Callsign })

		case "alt":
			acarg(func(ac *Aircraft) string {
				if ac.TempAltitude != 0 {
					return fmt.Sprintf("%d", ac.TempAltitude)
				} else if ac.FlightPlan != nil {
					return fmt.Sprintf("%d", ac.FlightPlan.Altitude)
				} else {
					return "???"
				}
			})

		case "altim":
			metararg(funarg(), func(m *METAR) string { return m.Altimeter })

		case "arr":
			acarg(func(ac *Aircraft) string {
				if ac.FlightPlan != nil {
					return ac.FlightPlan.ArrivalAirport
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
				if ac.FlightPlan != nil {
					return fmt.Sprintf("%d", ac.FlightPlan.Altitude)
				} else {
					return "????"
				}
			})

		case "dep":
			acarg(func(ac *Aircraft) string {
				if ac.FlightPlan != nil {
					return ac.FlightPlan.DepartureAirport
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
				if ac.FlightPlan != nil {
					return ac.FlightPlan.Route
				} else {
					return "????"
				}
			})

		case "squawk":
			acarg(func(ac *Aircraft) string {
				if ac.AssignedSquawk == Squawk(0) {
					return ac.Squawk.String()
				} else {
					return ac.AssignedSquawk.String()
				}
			})

		case "temp":
			acarg(func(ac *Aircraft) string { return fmt.Sprintf("%d", ac.TempAltitude) })

		case "time":
			finalArgs = append(finalArgs, time.Now().UTC().Format("15:04:05Z"))

		case "wind":
			metararg(funarg(), func(m *METAR) string { return m.Wind })

		case "winds":
			acarg(func(ac *Aircraft) string {
				if ac.FlightPlan == nil {
					return "???"
				}

				var airport, aptype string
				if ac.OnGround() {
					airport = strings.ToUpper(ac.FlightPlan.DepartureAirport)
					aptype = "departure"
				} else {
					airport = strings.ToUpper(ac.FlightPlan.ArrivalAirport)
					aptype = "arrival"
				}

				if m := server.GetMETAR(airport); m != nil {
					return m.Wind
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

func (cli *CLIPane) runCommand(cmd string) []*ConsoleEntry {
	cmdExpAliases, err := cli.ExpandAliases(cmd)
	if err != nil {
		return ErrorConsoleEntry(err)
	}
	if cmdExpAliases != cmd {
		// One or more aliases were expanded. Are there any parameters we
		// need from the user?
		if newCmd, ok := cli.input.InitializeParameters(cmdExpAliases); ok {
			cli.input.cmd = newCmd
			cli.input.cursor = cli.input.ParameterCursor()
			// Back to the user for editing.
			return nil
		}
		// Otherwise fall through and execute the command specified by the
		// alias.
	}

	cmdExpAliasesVars, err := cli.expandVariables(cmdExpAliases)
	if err != nil {
		return ErrorConsoleEntry(err)
	}

	fields := strings.Fields(cmdExpAliasesVars)
	if len(fields) == 0 {
		lg.Printf("unexpected no fields in command: %s", cmdExpAliasesVars)
		return nil
	}

	if fields[0] == "help" {
		switch len(fields) {
		case 1:
			var names []string
			for _, cmd := range cliCommands {
				names = append(names, cmd.Names()...)
			}
			sort.Strings(names)
			return StringConsoleEntry(fmt.Sprintf("available commands: %s", strings.Join(names, " ")))
		case 2:
			cmd := lookupCommand(fields[1])
			if cmd == nil {
				return ErrorStringConsoleEntry(fields[1] + "unknown command")
			} else {
				usage := fmt.Sprintf("%s: %s\nusage: %s %s", fields[1], cmd.Help(), fields[1], cmd.Usage())
				return StringConsoleEntry(usage)
			}

		default:
			return ErrorStringConsoleEntry("usage: help <command name>")
		}
	}

	// If it's a built-in command, run it
	if cmd := lookupCommand(fields[0]); cmd != nil {
		args := fields[1:]

		if cmd.TakesAircraft() && positionConfig.selectedAircraft == nil {
			return ErrorStringConsoleEntry(fields[0] + ": an aircraft must be selected to run this command")
		}
		var ctrl *Controller
		if cmd.TakesController() {
			if len(args) == 0 {
				return ErrorStringConsoleEntry(fields[0] + " : must specify a controller")
			}
			ctrl = server.GetController(args[0])
			if ctrl == nil {
				return ErrorStringConsoleEntry(args[0] + " : no such controller")
			}
			args = args[1:]
		}

		// Minimum and maximum number of args required from the user
		minArgc, maxArgc := cmd.AdditionalArgs()
		if len(args) < minArgc {
			return ErrorStringConsoleEntry(fields[0] + " : insufficient arguments provided: " + cmd.Usage())
		} else if len(args) > maxArgc {
			return ErrorStringConsoleEntry(fields[0] + ": excessive arguments provided: " + cmd.Usage())
		}

		return cmd.Run(fields[0], positionConfig.selectedAircraft, ctrl, args, cli)
	}

	// Otherwise see if we're selecting an aircraft...
	if len(fields) == 1 {
		matches := matchingAircraft(fields[0])
		switch len(matches) {
		case 0:
			// drop through to unknown command error
		case 1:
			positionConfig.selectedAircraft = matches[0]
			return nil
		default:
			msg := "Error: multiple aircraft match: "
			for _, ac := range matches {
				msg += ac.Callsign + " "
			}
			return ErrorStringConsoleEntry(msg)
		}
	}

	return ErrorStringConsoleEntry(fields[0] + ": unknown command")
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

func (cli *CLIPane) sendTextMessage(tm TextMessage) []*ConsoleEntry {
	sendRecip := server.Callsign() + FontAwesomeIconArrowRight
	switch tm.messageType {
	case TextBroadcast:
		sendRecip += "BROADCAST"

	case TextWallop:
		sendRecip += "WALLOP"

	case TextATC:
		sendRecip += "ATC"

	case TextFrequency:
		sendRecip += strings.Join(MapSlice(tm.frequencies, func(f Frequency) string { return f.String() }), ",")

	case TextPrivate:
		sendRecip += tm.recipient
	}

	if err := server.SendTextMessage(tm); err != nil {
		return ErrorConsoleEntry(err)
	}

	time := server.CurrentTime().UTC().Format("15:04:05Z")
	entry := &ConsoleEntry{
		text:  []string{"[" + time + "] " + sendRecip + ": ", tm.contents},
		style: []ConsoleTextStyle{ConsoleTextEmphasized, ConsoleTextRegular},
	}
	return []*ConsoleEntry{entry}
}
