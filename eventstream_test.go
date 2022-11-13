// eventstream_test.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"math/rand"
	"testing"
)

func TestEventStream(t *testing.T) {
	es := NewEventStream()

	es.Post(0)
	id := es.Subscribe()
	if len(es.Get(id)) != 0 {
		t.Errorf("Returned non-empty slice")
	}

	es.Post(1)
	es.Post(2)
	s := es.Get(id)
	if len(s) != 2 {
		t.Errorf("didn't return 2 item slice")
	}
	checkint := func(i interface{}, value int) {
		if v, ok := i.(int); !ok {
			t.Errorf("didn't return an integer")
		} else if v != value {
			t.Errorf("got value %d; expected %d", v, value)
		}
	}
	checkint(s[0], 1)
	checkint(s[1], 2)

	if len(es.Get(id)) != 0 {
		t.Errorf("Returned non-empty slice")
	}
}

func TestEventStreamCompact(t *testing.T) {
	es := NewEventStream()

	// multiple consumers, at different offsets
	id := [4]EventSubscriberId{es.Subscribe(), es.Subscribe(), es.Subscribe(), es.Subscribe()}
	// consume probability
	p := [4]float32{1, 0.75, 0.05, 0.5}
	// next value we expect to get from the stream
	var idx [4]int

	i, iter := 0, 0
	for i < 65536 {
		// Add a bunch of consecutive numbers to the stream
		n := rand.Intn(255)
		for j := 0; j < n; j++ {
			es.Post(i + j)
		}
		i += n

		if iter == 1 {
			es.Unsubscribe(id[1])
		}

		for c, prob := range p {
			if rand.Float32() > prob || (iter > 0 && c == 1) /* unsubscribed */ {
				continue
			}
			s := es.Get(id[c])
			for _, sv := range s {
				if idx[c] != sv {
					t.Errorf("expected %d, got %d for consumer %d", idx[c], sv, c)
				}
				idx[c]++
			}
		}

		es.compact()
		iter++
	}

	if cap(es.stream) > i/2 {
		t.Errorf("is compaction not happening? len %d cap %d", len(es.stream), cap(es.stream))
	}
}
