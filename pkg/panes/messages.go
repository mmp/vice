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
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/renderer"
	"github.com/mmp/vice/pkg/sim"
)

type Message struct {
	contents string
	system   bool
	error    bool
	global   bool
}

type MessagesPane struct {
	FontIdentifier              renderer.FontIdentifier
	ContactTransmissionsStatic  bool
	ReadbackTransmissionsStatic bool

	font             *renderer.Font
	scrollbar        *ScrollBar
	events           *sim.EventsSubscription
	messages         []Message
	staticAudioIndex int
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

	pcm := brownNoise(platform.AudioSampleRate / 4) // 1/4 second
	var err error
	mp.staticAudioIndex, err = p.AddPCM(pcm, platform.AudioSampleRate)
	if err != nil {
		lg.Error("Error adding static audio effect: %v", err)
	}
}

func brownNoise(n int) []byte {
	brownNoise := make([]float32, n)

	var prev float32
	for i := 0; i < n; i++ {
		// Generate a small random change (-1 to +1 range)
		randomChange := (rand.Float32()*2 - 1) * 0.04
		prev += randomChange

		// Apply damping to avoid runaway values
		prev *= 0.98

		// Ensure values stay within -1.0 to +1.0 for 16-bit PCM
		brownNoise[i] = math.Max(-1.0, math.Min(1.0, prev))
	}

	// Convert to 16-bit PCM scale (-32768 to +32767)
	pcm := make([]byte, 2*n)
	for i, sample := range brownNoise {
		v := int(sample * 32767)
		pcm[2*i] = byte(v >> 8)
		pcm[2*i+1] = byte(v & 0xff)
	}

	return pcm
}

func (mp *MessagesPane) LoadedSim(client *sim.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
}

func (mp *MessagesPane) ResetSim(client *sim.ControlClient, ss sim.State, pl platform.Platform, lg *log.Logger) {
	mp.messages = nil
}

func (mp *MessagesPane) CanTakeKeyboardFocus() bool { return false }

func (mp *MessagesPane) Upgrade(prev, current int) {
	if prev < 31 {
		mp.ContactTransmissionsStatic = true
	}
}

func (mp *MessagesPane) DrawUI(p platform.Platform, config *platform.Config) {
	if newFont, changed := renderer.DrawFontPicker(&mp.FontIdentifier, "Font"); changed {
		mp.font = newFont
	}
	imgui.Checkbox("Play audio static after pilot initial contact transmissions", &mp.ContactTransmissionsStatic)
	imgui.Checkbox("Play audio static after pilot readback transmissions", &mp.ReadbackTransmissionsStatic)
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
				ctrl := ctx.ControlClient.Controllers[event.ToController]
				fullName := ctrl.RadioName
				if ac := ctx.ControlClient.Aircraft[event.Callsign]; ac != nil && ctx.ControlClient.State.IsDeparture(ac) {
					// Always refer to the controller as "departure" for departing aircraft.
					fullName = strings.ReplaceAll(fullName, "approach", "departure")
				}
				msg = Message{contents: prefix + fullName + ", " + radioCallsign + ", " + event.Message}
				if mp.ContactTransmissionsStatic {
					ctx.Platform.PlayAudioOnce(mp.staticAudioIndex)
				}
			} else {
				if len(event.Message) > 0 {
					event.Message = strings.ToUpper(event.Message[:1]) + event.Message[1:]
				}
				msg = Message{contents: prefix + event.Message + ". " + radioCallsign,
					error: event.Type == av.RadioTransmissionUnexpected,
				}
				if mp.ReadbackTransmissionsStatic {
					ctx.Platform.PlayAudioOnce(mp.staticAudioIndex)
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
