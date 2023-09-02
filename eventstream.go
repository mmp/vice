// eventstream.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"runtime"
	"sync"
	"time"
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
	lastCompact   time.Time
	subscriptions map[*EventsSubscription]interface{}
}

type EventPoster interface {
	PostEvent(Event)
}

type EventsSubscription struct {
	stream *EventStream
	// offset is offset in the EventStream stream array up to which the
	// subscriber has consumed events so far.
	offset int
	source string
}

func NewEventStream() *EventStream {
	return &EventStream{subscriptions: make(map[*EventsSubscription]interface{})}
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
		stream: e,
		offset: len(e.events),
		source: source,
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.subscriptions[sub] = nil
	return sub
}

// Unsubscribe removes a subscriber from the subscriber list
func (e *EventsSubscription) Unsubscribe() {
	e.stream.mu.Lock()
	defer e.stream.mu.Unlock()

	if _, ok := e.stream.subscriptions[e]; !ok {
		lg.ErrorfUp1("Attempted to unsubscribe invalid subscription: %+v", e)
	}
	delete(e.stream.subscriptions, e)
	e.stream = nil
}

// Post adds an event to the event stream. The type used to encode the
// event is arbitrary; it's up to the EventStream users to establish
// conventions.
func (e *EventStream) Post(event Event) {
	if false && *devmode {
		lg.InfofUp1("Post %s; %d subscribers stream length %d, cap %d",
			event.String(), len(e.subscriptions), len(e.events), cap(e.events))
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	lg.Infof("Event posted: %s", event.String())

	// Ignore the event if no one's paying attention.
	if len(e.subscriptions) > 0 {
		if len(e.events)+1 == cap(e.events) && *devmode && lg != nil {
			// Dump the state of things if the array's about to grow; in
			// general we expect it to pretty quickly reach steady state
			// with just a handful of entries.
			e.mu.Unlock()
			lg.Infof("%s", e.Dump())
			e.mu.Lock()
		}

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
		lg.ErrorfUp1("Attempted to get with unregistered subscription: %+v", e)
		return nil
	}

	events := e.stream.events[e.offset:]
	e.offset = len(e.stream.events)

	if time.Since(e.stream.lastCompact) > 1*time.Second {
		e.stream.compact()
		e.stream.lastCompact = time.Now()
	}

	return events
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

	if len(e.events) > 1000 && lg != nil {
		lg.Errorf("EventStream length %d", len(e.events))
	}

	if minOffset > cap(e.events)/2 {
		n := len(e.events) - minOffset

		copy(e.events, e.events[minOffset:])
		e.events = e.events[:n]

		for sub := range e.subscriptions {
			sub.offset -= minOffset
		}
	}
}

// Dump prints out information about the internals of the event stream that
// may be useful for debugging.
func (e *EventStream) Dump() string {
	e.mu.Lock()
	defer e.mu.Unlock()

	s := fmt.Sprintf("stream: len %d cap %d", len(e.events), cap(e.events))
	if len(e.events) > 0 {
		s += fmt.Sprintf("\n  last elt %v", e.events[len(e.events)-1])
	}
	for sub := range e.subscriptions {
		s += fmt.Sprintf(" sub %+v", sub)
	}
	return s
}

///////////////////////////////////////////////////////////////////////////

type EventType int

const (
	InitiatedTrackEvent = iota
	DroppedTrackEvent
	PushedFlightStripEvent
	PointOutEvent
	OfferedHandoffEvent
	AcceptedHandoffEvent
	CanceledHandoffEvent
	RejectedHandoffEvent
	RadioTransmissionEvent
	StatusMessageEvent
	ServerBroadcastMessageEvent
	NumEventTypes
)

func (t EventType) String() string {
	return []string{"InitiatedTrack", "DroppedTrack", "PushedFlightStrip", "PointOut",
		"OfferedHandoff", "AcceptedHandoff", "CanceledHandoff", "RejectedHandoff",
		"RadioTransmission", "StatusMessage", "ServerBroadcastMessage"}[t]
}

type Event struct {
	Type                  EventType
	Callsign              string
	FromController        string
	ToController          string // For radio transmissions, the controlling controller.
	Message               string
	RadioTransmissionType RadioTransmissionType // For radio transmissions only
}

func (e *Event) String() string {
	if e.Type == RadioTransmissionEvent {
		return fmt.Sprintf("%s: callsign %s controller %s->%s message %s type %v",
			e.Type, e.Callsign, e.FromController, e.ToController, e.Message, e.RadioTransmissionType)
	} else {
		return fmt.Sprintf("%s: callsign %s controller %s->%s message %s",
			e.Type, e.Callsign, e.FromController, e.ToController, e.Message)
	}
}
