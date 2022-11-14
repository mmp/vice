// eventstream.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"runtime"
	"time"
)

type EventSubscriberId int

var (
	nextSubscriberId EventSubscriberId
	lastCompact      time.Time
)

// Reserve 0 as an invalid id so that zero-initialization of objects that
// store EventSubscriberIds works well.
const InvalidEventSubscriberId = 0

// EventStream provides a basic pub/sub event interface that allows any
// part of the system to post an event to the stream and other parts to
// subscribe and receive messages from the stream. It is the backbone for
// communicating events, world updates, and user actions across the various
// parts of the system.
type EventStream struct {
	stream      []interface{}
	subscribers map[EventSubscriberId]*EventSubscriber
}

type EventSubscriber struct {
	// offset is offset in the EventStream stream array up to which the
	// subscriber has consumed events so far.
	offset int
	source string
}

func NewEventStream() *EventStream {
	return &EventStream{subscribers: make(map[EventSubscriberId]*EventSubscriber)}
}

// Subscribe registers a new subscriber to the stream and returns an
// EventSubscriberId for the subscriber that can then be passed to other
// EventStream methods.
func (e *EventStream) Subscribe() EventSubscriberId {
	nextSubscriberId++ // start handing them out at 1
	id := nextSubscriberId

	// Record the subscriber's callsite, so that we can more easily debug
	// subscribers that aren't consuming events.
	_, fn, line, _ := runtime.Caller(1)
	source := fmt.Sprintf("%s:%d", fn, line)

	e.subscribers[id] = &EventSubscriber{
		offset: len(e.stream),
		source: source}
	return id
}

// Unsubscribe removes a subscriber from the subscriber list; the provided
// id can no longer be passed to the Get method to get events.
func (e *EventStream) Unsubscribe(id EventSubscriberId) {
	if _, ok := e.subscribers[id]; !ok {
		lg.ErrorfUp1("Attempted to unsubscribe invalid id: %d", id)
	}
	delete(e.subscribers, id)
}

// Post adds an event to the event stream. The type used to encode the
// event is arbitrary; it's up to the EventStream users to establish
// conventions.
func (e *EventStream) Post(event interface{}) {
	if *devmode {
		if s, ok := event.(interface{ String() string }); ok {
			lg.PrintfUp1("Post %s; %d subscribers stream length %d, cap %d",
				s.String(), len(e.subscribers), len(e.stream), cap(e.stream))
		} else {
			lg.PrintfUp1("Post %s; %d subscribers stream length %d, cap %d",
				s, len(e.subscribers), len(e.stream), cap(e.stream))
		}
	}

	// Ignore the event if no one's paying attention.
	if len(e.subscribers) > 0 {
		if len(e.stream)+1 == cap(e.stream) && *devmode && lg != nil {
			// Dump the state of things if the array's about to grow; in
			// general we expect it to pretty quickly reach steady state
			// with just a handful of entries.
			lg.Printf("%s", e.Dump())
		}

		e.stream = append(e.stream, event)
	}
}

// Get returns all of the events from the stream since the last time Get
// was called with the given id.  Note that events before an id was created
// with Subscribe are never reported for that id.
func (e *EventStream) Get(id EventSubscriberId) []interface{} {
	sub, ok := e.subscribers[id]
	if !ok {
		lg.ErrorfUp1("Attempted to get with invalid id: %d", id)
		return nil
	}

	s := e.stream[sub.offset:]
	sub.offset = len(e.stream)

	if time.Since(lastCompact) > 1*time.Second {
		e.compact()
		lastCompact = time.Now()
	}

	return s
}

// compact reclaims storage for events that all subscribers have seen; it
// is called periodically so that EventStream memory usage doesn't grow
// without bound.
func (e *EventStream) compact() {
	if lg != nil {
		lg.Printf("EventStream compact")
	}

	minOffset := len(e.stream)
	for _, sub := range e.subscribers {
		if sub.offset < minOffset {
			minOffset = sub.offset
		}
	}

	if minOffset > cap(e.stream)/2 {
		n := len(e.stream) - minOffset

		if lg != nil {
			lg.Printf("Compacting event stream from %d to %d elements", len(e.stream), n)
		}

		copy(e.stream, e.stream[minOffset:])
		e.stream = e.stream[:n]

		for _, sub := range e.subscribers {
			sub.offset -= minOffset
		}
	}
}

// Dump prints out information about the internals of the event stream that
// may be useful for debugging.
func (e *EventStream) Dump() string {
	s := fmt.Sprintf("stream: len %d cap %d", len(e.stream), cap(e.stream))
	if len(e.stream) > 0 {
		s += fmt.Sprintf("\n  last elt %v", e.stream[len(e.stream)-1])
	}
	for i, sub := range e.subscribers {
		s += fmt.Sprintf(" sub %d: %+v", i, sub)
	}
	return s
}

///////////////////////////////////////////////////////////////////////////

type SelectedAircraftEvent struct {
	ac *Aircraft
}

func (e *SelectedAircraftEvent) String() string {
	return "SelectedAircraftEvent: " + e.ac.callsign
}

type AddedAircraftEvent struct {
	ac *Aircraft
}

func (e *AddedAircraftEvent) String() string {
	return "AddedAircraftEvent: " + e.ac.callsign
}

type ModifiedAircraftEvent struct {
	ac *Aircraft
}

func (e *ModifiedAircraftEvent) String() string {
	return "ModifiedAircraftEvent: " + e.ac.callsign
}

type RemovedAircraftEvent struct {
	ac *Aircraft
}

func (e *RemovedAircraftEvent) String() string {
	return "RemovedAircraftEvent: " + e.ac.callsign
}

type UpdatedATISEvent struct {
	airport string
}

func (e *UpdatedATISEvent) String() string {
	return "UpdatedATISEvent: " + e.airport
}

type PushedFlightStripEvent struct {
	callsign string
}

func (e *PushedFlightStripEvent) String() string {
	return "PushedFlightStripEvent: " + e.callsign
}

type PointOutEvent struct {
	controller string
	ac         *Aircraft
}

func (e *PointOutEvent) String() string {
	return "PointOutEvent: " + e.controller + " " + e.ac.callsign
}

type AcceptedHandoffEvent struct {
	controller string
	ac         *Aircraft
}

func (e *AcceptedHandoffEvent) String() string {
	return "AcceptedHandoffEvent: " + e.controller + " " + e.ac.callsign
}

type OfferedHandoffEvent struct {
	controller string
	ac         *Aircraft
}

func (e *OfferedHandoffEvent) String() string {
	return "OfferedHandoffEvent: " + e.controller + " " + e.ac.callsign
}

type RejectedHandoffEvent struct {
	controller string
	ac         *Aircraft
}

func (e *RejectedHandoffEvent) String() string {
	return "RejectedHandoffEvent: " + e.controller + " " + e.ac.callsign
}

type TextMessageEvent struct {
	message *TextMessage
}

func (e *TextMessageEvent) String() string {
	return "TextMessageEvent: " + e.message.String()
}

