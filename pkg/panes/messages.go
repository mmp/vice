// pkg/panes/messages.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package panes

import (
	"encoding/json"
	"log/slog"
	"slices"
	"strings"

	"github.com/mmp/imgui-go/v4"
	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

type Message struct {
	contents string
	system   bool
	error    bool
	global   bool
}

var audioAlerts map[string]string = map[string]string{
	"Aircraft Check In": "Aircraft_Check_In.mp3",
	"Radio Static":      "fm-radio-static-82334_2.mp3",
}

type MessagesPane struct {
	FontIdentifier             renderer.FontIdentifier
	AudioAlertSelection        string
	ContactTransmissionsAlert  bool
	ReadbackTransmissionsAlert bool

	font            *renderer.Font
	scrollbar       *ScrollBar
	events          *sim.EventsSubscription
	messages        []Message
	alertAudioIndex map[string]int
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

func (mp *MessagesPane) DisplayName() string { return "Messages" }

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

	mp.alertAudioIndex = make(map[string]int)
	for _, alert := range util.SortedMapKeys(audioAlerts) {
		idx, err := p.AddMP3(util.LoadResourceBytes("audio/" + audioAlerts[alert]))
		if err != nil {
			lg.Error("Error adding static audio effect: %v", err)
		}
		mp.alertAudioIndex[alert] = idx
	}

	if _, ok := mp.alertAudioIndex[mp.AudioAlertSelection]; !ok { // Not available (or unset)
		// Take the first one alphabetically.
		for _, alert := range util.SortedMapKeys(audioAlerts) {
			mp.AudioAlertSelection = alert
			break
		}
	}
}

func (mp *MessagesPane) LoadedSim(client *server.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
}

func (mp *MessagesPane) ResetSim(client *server.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	mp.messages = nil
}

func (mp *MessagesPane) CanTakeKeyboardFocus() bool { return false }

func (mp *MessagesPane) Upgrade(prev, current int) {
}

func (mp *MessagesPane) DrawUI(p platform.Platform, config *platform.Config) {
	if newFont, changed := renderer.DrawFontPicker(&mp.FontIdentifier, "Font"); changed {
		mp.font = newFont
	}

	imgui.Separator()
	if imgui.BeginComboV("Audio alert", mp.AudioAlertSelection, 0 /* flags */) {
		for _, alert := range util.SortedMapKeys(audioAlerts) {
			if imgui.SelectableV(alert, alert == mp.AudioAlertSelection, 0, imgui.Vec2{}) {
				mp.AudioAlertSelection = alert
				p.PlayAudioOnce(mp.alertAudioIndex[alert])
			}
		}
		imgui.EndCombo()
	}
	imgui.Checkbox("Play audio alert after pilot initial contact transmissions", &mp.ContactTransmissionsAlert)
	imgui.Checkbox("Play audio alert after pilot readback transmissions", &mp.ReadbackTransmissionsAlert)
}

func (mp *MessagesPane) Draw(ctx *Context, cb *renderer.CommandBuffer) {
	mp.processEvents(ctx)

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

func (mp *MessagesPane) processEvents(ctx *Context) {
	consolidateRadioTransmissions := func(events []sim.Event) []sim.Event {
		canConsolidate := func(a, b sim.Event) bool {
			return a.Type == sim.RadioTransmissionEvent && b.Type == sim.RadioTransmissionEvent &&
				a.Callsign == b.Callsign && a.Type == b.Type && a.ToController == b.ToController
		}
		var c []sim.Event
		for _, e := range events {
			if n := len(c); n > 0 && canConsolidate(e, c[n-1]) {
				c[n-1].Message += ", " + e.Message
				if e.RadioTransmissionType == av.RadioTransmissionUnexpected {
					c[n-1].RadioTransmissionType = av.RadioTransmissionUnexpected
				}
			} else {
				c = append(c, e)
			}
		}
		return c
	}

	for _, event := range consolidateRadioTransmissions(mp.events.Get()) {
		switch event.Type {
		case sim.RadioTransmissionEvent:
			// Collect multiple successive transmissions from the same
			// aircraft into a single transmission.

			toUs := event.ToController == ctx.ControlClient.PrimaryTCP
			amInstructor := ctx.ControlClient.Instructors[ctx.ControlClient.PrimaryTCP]
			if !toUs && !amInstructor {
				break
			}

			// Split the callsign into the ICAO and the flight number
			// Note: this is buggy if we process multiple senders in a
			// single call here, but that shouldn't happen...

			radioCallsign := event.Callsign
			if idx := strings.IndexAny(radioCallsign, "0123456789"); idx != -1 {
				// Try to get the telephony.
				icao, flight := radioCallsign[:idx], radioCallsign[idx:]
				if telephony, ok := av.DB.Callsigns[icao]; ok {
					radioCallsign = telephony + " " + flight
					if ac := ctx.ControlClient.Aircraft[event.Callsign]; ac != nil {
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

			prefix := ""
			if !toUs && amInstructor {
				prefix = "[to " + event.ToController + "] "
			}

			var msg Message
			if event.RadioTransmissionType == av.RadioTransmissionContact {
				name := event.ToController
				if ctrl, ok := ctx.ControlClient.Controllers[event.ToController]; ok {
					name = ctrl.RadioName
				}
				if ac := ctx.ControlClient.Aircraft[event.Callsign]; ac != nil && ctx.ControlClient.State.IsDeparture(ac) {
					// Always refer to the controller as "departure" for departing aircraft.
					name = strings.ReplaceAll(name, "approach", "departure")
				}
				msg = Message{contents: prefix + name + ", " + radioCallsign + ", " + event.Message}
				if mp.ContactTransmissionsAlert {
					ctx.Platform.PlayAudioOnce(mp.alertAudioIndex[mp.AudioAlertSelection])
				}
			} else {
				if len(event.Message) > 0 {
					event.Message = strings.ToUpper(event.Message[:1]) + event.Message[1:]
				}
				msg = Message{contents: prefix + event.Message + ". " + radioCallsign,
					error: event.Type == av.RadioTransmissionUnexpected,
				}
				if mp.ReadbackTransmissionsAlert {
					ctx.Platform.PlayAudioOnce(mp.alertAudioIndex[mp.AudioAlertSelection])
				}
			}
			ctx.Lg.Debug("radio_transmission", slog.String("callsign", event.Callsign), slog.Any("message", msg))
			mp.messages = append(mp.messages, msg)

		case sim.GlobalMessageEvent:
			if event.FromController != ctx.ControlClient.PrimaryTCP {
				for _, line := range strings.Split(event.Message, "\n") {
					mp.messages = append(mp.messages, Message{contents: line, global: true})
				}
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
}
