// pkg/sim/eventstream_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"testing"
	"time"

	"github.com/mmp/vice/rand"
)

func TestEventStream(t *testing.T) {
	es := NewEventStream(nil)
	defer es.Destroy()

	es.Post(Event{})
	sub := es.Subscribe()
	if len(sub.Get()) != 0 {
		t.Errorf("Returned non-empty slice")
	}

	es.Post(Event{Type: 1})
	es.Post(Event{Type: 2})
	s := sub.Get()
	if len(s) != 2 {
		t.Errorf("didn't return 2 item slice")
	}

	if s[0].Type != 1 {
		t.Errorf("Expected type 1, got %v", s[0])
	}
	if s[1].Type != 2 {
		t.Errorf("Expected type 1, got %v", s[1])
	}

	if len(sub.Get()) != 0 {
		t.Errorf("Returned non-empty slice")
	}
}

func TestEventStreamCompact(t *testing.T) {
	es := NewEventStream(nil)
	defer es.Destroy()

	// multiple consumers, at different offsets
	subs := [4]*EventsSubscription{es.Subscribe(), es.Subscribe(), es.Subscribe(), es.Subscribe()}
	// consume probability
	p := [4]float32{1, 0.75, 0.05, 0.5}
	// next value we expect to get from the stream
	var idx [4]int

	i, iter := 0, 0
	r := rand.Make()
	for i < 65536 {
		// Add a bunch of consecutive numbers to the stream
		n := r.Intn(255)
		for j := range n {
			es.Post(Event{Type: EventType((i + j) % int(FlightPlanDirectEvent+1))})
		}
		i += n

		if iter == 1 {
			subs[1].Unsubscribe()
		}

		for c, prob := range p {
			if r.Float32() > prob || (iter > 0 && c == 1) /* unsubscribed */ {
				continue
			}
			s := subs[c].Get()
			for _, sv := range s {
				if idx[c] != int(sv.Type) {
					t.Errorf("expected %d, got %d for consumer %d", idx[c], int(sv.Type), c)
				}
				idx[c] = (idx[c] + 1) % int(FlightPlanDirectEvent+1)
			}
		}

		es.mu.Lock()
		es.compact()
		es.mu.Unlock()

		iter++
	}

	if cap(es.events) > i/2 {
		t.Errorf("is compaction not happening? len %d cap %d", len(es.events), cap(es.events))
	}
}

func TestPeerVoiceEventRoundTrip(t *testing.T) {
	es := NewEventStream(nil)
	defer es.Destroy()

	sub := es.Subscribe()
	defer sub.Unsubscribe()

	es.Post(Event{
		Type:        PeerVoiceEvent,
		SourceTCW:   "TCW-1",
		SenderToken: "tok-A",
		VoiceChunk:  []int16{1, 2, 3, 4},
		VoiceEnd:    false,
	})
	es.Post(Event{
		Type:        PeerVoiceEvent,
		SourceTCW:   "TCW-1",
		SenderToken: "tok-A",
		VoiceEnd:    true,
	})

	got := sub.Get()
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Type != PeerVoiceEvent || string(got[0].SourceTCW) != "TCW-1" ||
		got[0].SenderToken != "tok-A" || len(got[0].VoiceChunk) != 4 {
		t.Errorf("event[0] = %+v", got[0])
	}
	if !got[1].VoiceEnd {
		t.Errorf("event[1] should have VoiceEnd=true, got %+v", got[1])
	}
	if PeerVoiceEvent.String() != "PeerVoice" {
		t.Errorf("PeerVoiceEvent.String() = %q, want %q", PeerVoiceEvent.String(), "PeerVoice")
	}
}

func TestEvent_PlayAtRoundTrip(t *testing.T) {
	target := NewSimTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
	e := Event{
		Type:           RadioTransmissionEvent,
		DestinationTCW: "TCW-1",
		PlayAt:         target,
	}
	if !e.PlayAt.Equal(target) {
		t.Errorf("PlayAt not preserved; want %v got %v", target, e.PlayAt)
	}
}

func TestEvent_SpokenVoiceRoundTrip(t *testing.T) {
	e := Event{
		Type:        RadioTransmissionEvent,
		SpokenVoice: "am_adam",
	}
	if e.SpokenVoice != "am_adam" {
		t.Errorf("SpokenVoice not preserved; got %q", e.SpokenVoice)
	}
}
