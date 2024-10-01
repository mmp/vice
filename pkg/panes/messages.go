// pkg/panes/messages.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"

	"github.com/mmp/imgui-go/v4"
)

type Message struct {
	contents string
	system   bool
	error    bool
	global   bool
}

type CLIInput struct {
	cmd    string
	cursor int
}

type MessagesPane struct {
	KeepFocusAfterTrackSlew bool

	FontIdentifier renderer.FontIdentifier
	font           *renderer.Font
	scrollbar      *ScrollBar
	events         *sim.EventsSubscription
	messages       []Message

	// Command-input-related
	input         CLIInput
	history       []CLIInput
	historyOffset int // for up arrow / downarrow. Note: counts from the end! 0 when not in history
	savedInput    CLIInput
}

func init() {
	RegisterUnmarshalPane("MessagesPane", func(d []byte) (Pane, error) {
		var p MessagesPane
		err := json.Unmarshal(d, &p)
		return &p, err
	})
}

func NewMessagesPane() *MessagesPane {
	return &MessagesPane{
		FontIdentifier: renderer.FontIdentifier{Name: "Inconsolata Condensed Regular", Size: 16},
	}
}

func (mp *MessagesPane) DisplayName() string { return "Messages/Commands" }

func (mp *MessagesPane) Hide() bool { return false }

func (mp *MessagesPane) Activate(r renderer.Renderer, p platform.Platform, eventStream *sim.EventStream, lg *log.Logger) {
	if mp.font = renderer.GetFont(mp.FontIdentifier); mp.font == nil {
		mp.font = renderer.GetDefaultFont()
		mp.FontIdentifier = mp.font.Id
	}
	if mp.scrollbar == nil {
		mp.scrollbar = NewVerticalScrollBar(4, true)
	}
	mp.events = eventStream.Subscribe()
}

func (mp *MessagesPane) LoadedSim(client *sim.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
}

func (mp *MessagesPane) ResetSim(client *sim.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	mp.messages = nil
}

func (mp *MessagesPane) CanTakeKeyboardFocus() bool { return true }

func (mp *MessagesPane) DrawUI(p platform.Platform, config *platform.Config) {
	if newFont, changed := renderer.DrawFontPicker(&mp.FontIdentifier, "Font"); changed {
		mp.font = newFont
	}
	imgui.Checkbox("Keep focus after slewing track for control command", &mp.KeepFocusAfterTrackSlew)
}

func (mp *MessagesPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	mp.processEvents(ctx)

	if ctx.Mouse != nil && ctx.Mouse.Clicked[platform.MouseButtonPrimary] {
		ctx.KeyboardFocus.Take(mp)
	}
	mp.processKeyboard(ctx)

	nLines := len(mp.messages) + 1 /* prompt */
	lineHeight := float32(mp.font.Size + 1)
	visibleLines := int(ctx.PaneExtent.Height() / lineHeight)
	mp.scrollbar.Update(nLines, visibleLines, ctx)

	drawWidth := ctx.PaneExtent.Width()
	if mp.scrollbar.Visible() {
		drawWidth -= float32(mp.scrollbar.PixelExtent())
	}

	td := renderer.GetTextDrawBuilder()
	defer renderer.ReturnTextDrawBuilder(td)

	indent := float32(2)

	scrollOffset := mp.scrollbar.Offset()
	y := lineHeight

	// Draw the prompt and any input text
	cliStyle := renderer.TextStyle{Font: mp.font, Color: renderer.RGB{1, 1, .2}}
	cursorStyle := renderer.TextStyle{Font: mp.font, LineSpacing: 0,
		Color: renderer.RGB{1, 1, .2}, DrawBackground: true, BackgroundColor: renderer.RGB{1, 1, 1}}
	ci := mp.input

	prompt := "> "
	if !ctx.HaveFocus {
		// Don't draw the cursor if we don't have keyboard focus
		td.AddText(prompt+ci.cmd, [2]float32{indent, y}, cliStyle)
	} else if ci.cursor == len(ci.cmd) {
		// cursor at the end
		td.AddTextMulti([]string{prompt + string(ci.cmd), " "}, [2]float32{indent, y},
			[]renderer.TextStyle{cliStyle, cursorStyle})
	} else {
		// cursor in the middle
		sb := prompt + ci.cmd[:ci.cursor]
		sc := ci.cmd[ci.cursor : ci.cursor+1]
		se := ci.cmd[ci.cursor+1:]
		styles := []renderer.TextStyle{cliStyle, cursorStyle, cliStyle}
		td.AddTextMulti([]string{sb, sc, se}, [2]float32{indent, y}, styles)
	}
	y += lineHeight

	for i := scrollOffset; i < math.Min(len(mp.messages), visibleLines+scrollOffset+1); i++ {
		// TODO? wrap text
		msg := mp.messages[len(mp.messages)-1-i]

		s := renderer.TextStyle{Font: mp.font, Color: msg.Color()}
		td.AddText(msg.contents, [2]float32{indent, y}, s)
		y += lineHeight
	}

	ctx.SetWindowCoordinateMatrices(cb)
	if ctx.HaveFocus {
		// Yellow border around the edges
		ld := renderer.GetLinesDrawBuilder()
		defer renderer.ReturnLinesDrawBuilder(ld)

		w, h := ctx.PaneExtent.Width(), ctx.PaneExtent.Height()
		ld.AddLineLoop([][2]float32{{0, 0}, {w, 0}, {w, h}, {0, h}})
		cb.SetRGB(renderer.RGB{1, 1, 0}) // yellow
		ld.GenerateCommands(cb)
	}
	mp.scrollbar.Draw(ctx, cb)
	td.GenerateCommands(cb)
}

func (mp *MessagesPane) processKeyboard(ctx *Context) {
	if ctx.Keyboard == nil || !ctx.HaveFocus {
		return
	}

	// Grab keyboard input
	if len(mp.input.cmd) > 0 && mp.input.cmd[0] == '/' {
		mp.input.InsertAtCursor(ctx.Keyboard.Input)
	} else {
		mp.input.InsertAtCursor(strings.ToUpper(ctx.Keyboard.Input))
	}

	if ctx.Keyboard.WasPressed(platform.KeyUpArrow) {
		if mp.historyOffset < len(mp.history) {
			if mp.historyOffset == 0 {
				mp.savedInput = mp.input // save current input in case we return
			}
			mp.historyOffset++
			mp.input = mp.history[len(mp.history)-mp.historyOffset]
			mp.input.cursor = len(mp.input.cmd)
		}
	}
	if ctx.Keyboard.WasPressed(platform.KeyDownArrow) {
		if mp.historyOffset > 0 {
			mp.historyOffset--
			if mp.historyOffset == 0 {
				mp.input = mp.savedInput
				mp.savedInput = CLIInput{}
			} else {
				mp.input = mp.history[len(mp.history)-mp.historyOffset]
			}
			mp.input.cursor = len(mp.input.cmd)
		}
	}

	if (ctx.Keyboard.WasPressed(platform.KeyControl) || ctx.Keyboard.WasPressed(platform.KeySuper)) && ctx.Keyboard.WasPressed(platform.KeyV) {
		c, err := ctx.Platform.GetClipboard().Text()
		if err == nil {
			mp.input.InsertAtCursor(c)
		}
	}
	if ctx.Keyboard.WasPressed(platform.KeyLeftArrow) {
		if mp.input.cursor > 0 {
			mp.input.cursor--
		}
	}

	if ctx.Keyboard.WasPressed(platform.KeyRightArrow) {
		if mp.input.cursor < len(mp.input.cmd) {
			mp.input.cursor++
		}
	}
	if ctx.Keyboard.WasPressed(platform.KeyHome) {
		mp.input.cursor = 0
	}
	if ctx.Keyboard.WasPressed(platform.KeyEnd) {
		mp.input.cursor = len(mp.input.cmd)
	}
	if ctx.Keyboard.WasPressed(platform.KeyBackspace) {
		mp.input.DeleteBeforeCursor()
	}
	if ctx.Keyboard.WasPressed(platform.KeyDelete) {
		mp.input.DeleteAfterCursor()
	}
	if ctx.Keyboard.WasPressed(platform.KeyEscape) {
		if mp.input.cursor > 0 {
			mp.input = CLIInput{}
		}
	}

	if ctx.Keyboard.WasPressed(platform.KeyEnter) && strings.TrimSpace(mp.input.cmd) != "" {
		mp.runCommands(ctx)
	}
}

func (msg *Message) Color() renderer.RGB {
	switch {
	case msg.error:
		return renderer.RGB{.9, .1, .1}
	case msg.global, msg.system:
		return renderer.RGB{0.012, 0.78, 0.016}
	default:
		return renderer.RGB{1, 1, 1}
	}
}

func (mp *MessagesPane) runCommands(ctx *Context) {
	mp.input.cmd = strings.TrimSpace(mp.input.cmd)

	if mp.input.cmd[0] == '/' {
		ctx.ControlClient.SendGlobalMessage(sim.GlobalMessage{
			FromController: ctx.ControlClient.Callsign,
			Message:        ctx.ControlClient.Callsign + ": " + mp.input.cmd[1:],
		})
		mp.messages = append(mp.messages, Message{contents: ctx.ControlClient.Callsign + ": " + mp.input.cmd[1:], global: true})
		mp.history = append(mp.history, mp.input)
		mp.input = CLIInput{}
		return
	} else if mp.input.cmd == "P" {
		ctx.ControlClient.ToggleSimPause()
		mp.history = append(mp.history, mp.input)
		mp.input = CLIInput{}
		return
	} else {
		mp.messages = append(mp.messages, Message{contents: mp.input.cmd + ": command unknown", error: true})
	}
}

func (ci *CLIInput) InsertAtCursor(s string) {
	if len(s) == 0 {
		return
	}

	ci.cmd = ci.cmd[:ci.cursor] + s + ci.cmd[ci.cursor:]

	// place cursor after the inserted text
	ci.cursor += len(s)
}

func (ci *CLIInput) DeleteBeforeCursor() {
	if ci.cursor > 0 {
		ci.cmd = ci.cmd[:ci.cursor-1] + ci.cmd[ci.cursor:]
		ci.cursor--
	}
}

func (ci *CLIInput) DeleteAfterCursor() {
	if ci.cursor < len(ci.cmd) {
		ci.cmd = ci.cmd[:ci.cursor] + ci.cmd[ci.cursor+1:]
	}
}

func (mp *MessagesPane) processEvents(ctx *Context) {
	lastRadioCallsign := ""
	var lastRadioType av.RadioTransmissionType
	var unexpectedTransmission bool
	var transmissions []string

	addTransmissions := func() {
		// Split the callsign into the ICAO and the flight number
		// Note: this is buggy if we process multiple senders in a
		// single call here, but that shouldn't happen...
		callsign := lastRadioCallsign
		radioCallsign := lastRadioCallsign
		if idx := strings.IndexAny(callsign, "0123456789"); idx != -1 {
			// Try to get the telephony.
			icao, flight := callsign[:idx], callsign[idx:]
			if telephony, ok := av.DB.Callsigns[icao]; ok {
				radioCallsign = telephony + " " + flight
				if ac := ctx.ControlClient.Aircraft[callsign]; ac != nil {
					if fp := ac.FlightPlan; fp != nil {
						if strings.HasPrefix(fp.AircraftType, "H/") {
							radioCallsign += " heavy"
						} else if strings.HasPrefix(fp.AircraftType, "J/") || strings.HasPrefix(fp.AircraftType, "S/") {
							radioCallsign += " super"
						}
					}
				}
			}
		}

		response := strings.Join(transmissions, ", ")
		var msg Message
		if lastRadioType == av.RadioTransmissionContact {
			ctrl := ctx.ControlClient.Controllers[ctx.ControlClient.Callsign]
			fullName := ctrl.FullName
			if ac := ctx.ControlClient.Aircraft[callsign]; ac != nil && ctx.ControlClient.State.IsDeparture(ac) {
				// Always refer to the controller as "departure" for departing aircraft.
				fullName = strings.ReplaceAll(fullName, "approach", "departure")
			}
			msg = Message{contents: fullName + ", " + radioCallsign + ", " + response}
		} else {
			if len(response) > 0 {
				response = strings.ToUpper(response[:1]) + response[1:]
			}
			msg = Message{contents: response + ". " + radioCallsign, error: unexpectedTransmission}
		}
		ctx.Lg.Debug("radio_transmission", slog.String("callsign", callsign), slog.Any("message", msg))
		mp.messages = append(mp.messages, msg)
	}

	for _, event := range mp.events.Get() {
		switch event.Type {
		case sim.RadioTransmissionEvent:
			if event.ToController == ctx.ControlClient.Callsign {
				if event.Callsign != lastRadioCallsign || event.RadioTransmissionType != lastRadioType {
					if len(transmissions) > 0 {
						addTransmissions()
						transmissions = nil
						unexpectedTransmission = false
					}
					lastRadioCallsign = event.Callsign
					lastRadioType = event.RadioTransmissionType
				}
				transmissions = append(transmissions, event.Message)
				unexpectedTransmission = unexpectedTransmission || (event.RadioTransmissionType == av.RadioTransmissionUnexpected)
			}
		case sim.GlobalMessageEvent:
			if event.FromController != ctx.ControlClient.Callsign {
				mp.messages = append(mp.messages, Message{contents: event.Message, global: true})
			}
		case sim.StatusMessageEvent:
			// Don't spam the same message repeatedly; look in the most recent 5.
			n := len(mp.messages)
			start := math.Max(0, n-5)
			if !slices.ContainsFunc(mp.messages[start:],
				func(m Message) bool { return m.contents == event.Message }) {
				mp.messages = append(mp.messages,
					Message{
						contents: event.Message,
						system:   true,
					})
			}
		}
	}

	if len(transmissions) > 0 {
		addTransmissions()
	}
}
