// pkg/sim/eventstream.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
)

type EventSubscriberId int

// EventStream provides a basic pub/sub event interface that allows any
// part of the system to post an event to the stream and other parts to
// subscribe and receive messages from the stream. It is the backbone for
// communicating events, world updates, and user actions across the various
// parts of the system.
type EventStream struct {
	mu            sync.Mutex
	events        []Event
	subscriptions map[*EventsSubscription]interface{}
	lastPost      time.Time
	warnedLong    bool
	done          chan struct{}
	lg            *log.Logger
}

type EventsSubscription struct {
	stream *EventStream
	// offset is offset in the EventStream stream array up to which the
	// subscriber has consumed events so far.
	offset      int
	source      string
	lastGet     time.Time
	warnedNoGet bool
}

func (e *EventsSubscription) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("offset", e.offset),
		slog.String("source", e.source),
		slog.Time("last_get", e.lastGet))
}

func (e *EventsSubscription) PostEvent(event Event) {
	e.stream.Post(event)
}

func NewEventStream(lg *log.Logger) *EventStream {
	es := &EventStream{
		subscriptions: make(map[*EventsSubscription]interface{}),
		lastPost:      time.Now(),
		done:          make(chan struct{}),
		lg:            lg,
	}
	go es.monitor()
	return es
}

// Subscribe registers a new subscriber to the stream and returns an
// EventSubscriberId for the subscriber that can then be passed to other
// EventStream methods.
func (e *EventStream) Subscribe() *EventsSubscription {
	// Record the subscriber's callsite, so that we can more easily debug
	// subscribers that aren't consuming events.
	_, fn, line, _ := runtime.Caller(1)
	source := fmt.Sprintf("%s:%d", fn, line)

	sub := &EventsSubscription{
		stream:  e,
		offset:  len(e.events),
		source:  source,
		lastGet: time.Now(),
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.subscriptions[sub] = nil
	return sub
}

func (e *EventStream) monitor() {
	tick := time.Tick(5 * time.Second)

	for {
		<-tick

		select {
		case <-e.done:
			return
		default:
		}

		e.mu.Lock()

		e.compact()

		if len(e.events) > 1000 && !e.warnedLong {
			// It's likely that one of the subscribers is out to lunch if
			// the stream has grown this long.
			e.lg.Warn("Long EventStream", slog.Int("length", len(e.events)),
				log.AnyPointerSlice("subscriptions", slices.Collect(maps.Keys(e.subscriptions))))
			e.warnedLong = true
		}

		// Check if any of the subscribers haven't been consuming events,
		// though only if events are being posted to the stream so we don't
		// complain when the sim is paused, etc.
		if time.Since(e.lastPost) < 5*time.Second {
			for sub := range e.subscriptions {
				if d := time.Since(sub.lastGet); d > 10*time.Second && !sub.warnedNoGet {
					e.lg.Warn("Subscriber has not called Get() recently",
						slog.Duration("duration", d), slog.Any("subscriber", sub))
					sub.warnedNoGet = true
				}
			}
		}

		e.mu.Unlock()
	}
}

// Unsubscribe removes a subscriber from the subscriber list
func (e *EventsSubscription) Unsubscribe() {
	e.stream.mu.Lock()
	defer e.stream.mu.Unlock()

	if _, ok := e.stream.subscriptions[e]; !ok {
		e.stream.lg.Errorf("Attempted to unsubscribe invalid subscription: %+v", e)
	}
	delete(e.stream.subscriptions, e)
	e.stream = nil
}

// Post adds an event to the event stream. The type used to encode the
// event is arbitrary; it's up to the EventStream users to establish
// conventions.
func (e *EventStream) Post(event Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.lg.Debug("posted event", slog.Any("event", event))

	// Ignore the event if no one's paying attention.
	if len(e.subscriptions) > 0 {
		e.lastPost = time.Now()
		e.events = append(e.events, event)
	}
}

// Get returns all of the events from the stream since the last time Get
// was called with the given id.  Note that events before an id was created
// with Subscribe are never reported for that id.
func (e *EventsSubscription) Get() []Event {
	e.stream.mu.Lock()
	defer e.stream.mu.Unlock()

	if _, ok := e.stream.subscriptions[e]; !ok {
		e.stream.lg.Errorf("Attempted to get with unregistered subscription: %+v", e)
		return nil
	}

	events := slices.Clone(e.stream.events[e.offset:])
	e.offset = len(e.stream.events)
	e.lastGet = time.Now()
	e.warnedNoGet = false

	return events
}

func (e *EventStream) Destroy() {
	e.mu.Lock()
	defer e.mu.Unlock()

	select {
	case e.done <- struct{}{}:
	default:
	}

	close(e.done)
	clear(e.subscriptions)
}

// compact reclaims storage for events that all subscribers have seen; it
// is called periodically so that EventStream memory usage doesn't grow
// without bound.
func (e *EventStream) compact() {
	minOffset := len(e.events)
	for sub := range e.subscriptions {
		if sub.offset < minOffset {
			minOffset = sub.offset
		}
	}

	if minOffset > cap(e.events)/2 {
		n := len(e.events) - minOffset

		copy(e.events, e.events[minOffset:])
		e.events = e.events[:n]

		for sub := range e.subscriptions {
			sub.offset -= minOffset
		}

		e.warnedLong = false // reset this after a successful compact.
	}
}

// implements slog.LogValuer
func (e *EventStream) LogValue() slog.Value {
	e.mu.Lock()
	defer e.mu.Unlock()

	items := []slog.Attr{slog.Int("len", len(e.events)), slog.Int("cap", cap(e.events))}
	if len(e.events) > 0 {
		items = append(items, slog.Any("last_element", e.events[len(e.events)-1]))
	}
	items = append(items, log.AnyPointerSlice("subscriptions", slices.Collect(maps.Keys(e.subscriptions))))
	return slog.GroupValue(items...)
}

///////////////////////////////////////////////////////////////////////////

type EventType int

const (
	PushedFlightStripEvent EventType = iota
	PointOutEvent
	OfferedHandoffEvent
	AcceptedHandoffEvent
	AcceptedRedirectedHandoffEvent
	CanceledHandoffEvent
	RejectedHandoffEvent
	RadioTransmissionEvent
	StatusMessageEvent
	ServerBroadcastMessageEvent
	GlobalMessageEvent
	AcknowledgedPointOutEvent
	RejectedPointOutEvent
	HandoffControlEvent
	SetGlobalLeaderLineEvent
	ForceQLEvent
	TransferAcceptedEvent
	TransferRejectedEvent
	RecalledPointOutEvent
	FlightPlanAssociatedEvent
	FixCoordinatesEvent
	NumEventTypes
)

func (t EventType) String() string {
	return []string{"PushedFlightStrip", "PointOut",
		"OfferedHandoff", "AcceptedHandoff", "AcceptedRedirectedHandoffEvent", "CanceledHandoff",
		"RejectedHandoff", "RadioTransmission", "StatusMessage", "ServerBroadcastMessage",
		"GlobalMessage", "AcknowledgedPointOut", "RejectedPointOut", "HandoffControl",
		"SetGlobalLeaderLine", "ForceQL", "TransferAccepted", "TransferRejected",
		"RecalledPointOut", "FlightPlanAssociated"}[t]
}

type Event struct {
	Type                  EventType
	ADSBCallsign          av.ADSBCallsign
	ACID                  ACID
	FromController        string
	ToController          string // For radio transmissions, the controlling controller.
	WrittenText           string
	SpokenText            string
	RadioTransmissionType av.RadioTransmissionType       // For radio transmissions only
	LeaderLineDirection   *math.CardinalOrdinalDirection // SetGlobalLeaderLineEvent
	WaypointInfo          []math.Point2LL
}

func (e *Event) String() string {
	switch e.Type {
	case RadioTransmissionEvent:
		return fmt.Sprintf("%s: ADSB callsign %q ACID %q controller %q->%q written %q spoken %q type %v",
			e.Type, e.ADSBCallsign, e.ACID, e.FromController, e.ToController, e.WrittenText, e.SpokenText,
			e.RadioTransmissionType)
	default:
		return fmt.Sprintf("%s: ADSB callsign %q ACID %q controller %q->%q written %q spoken %q",
			e.Type, e.ADSBCallsign, e.ACID, e.FromController, e.ToController, e.WrittenText, e.SpokenText)
	}
}

func (e Event) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("type", e.Type.String())}
	if e.ADSBCallsign != "" {
		attrs = append(attrs, slog.String("adsb_callsign", string(e.ADSBCallsign)))
	}
	if e.ACID != "" {
		attrs = append(attrs, slog.String("acid", string(e.ACID)))
	}
	if e.FromController != "" {
		attrs = append(attrs, slog.String("from_controller", e.FromController))
	}
	if e.ToController != "" {
		attrs = append(attrs, slog.String("to_controller", e.ToController))
	}
	if e.WrittenText != "" {
		attrs = append(attrs, slog.String("written_text", e.WrittenText))
	}
	if e.SpokenText != "" {
		attrs = append(attrs, slog.String("spoken_text", e.SpokenText))
	}
	return slog.GroupValue(attrs...)
}
