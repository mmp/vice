// pkg/util/chan.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import "unsafe"

// ChunkedChan buffers up multiple sent items in an array and then sends
// slices of them on the channel in order to reduce chan overhead when many
// small objects are being sent.
type ChunkedChan[T any] struct {
	ch    chan []T
	accum []T
	nbuf  int
}

// MakeChunkedChan initializes a ChunkedChan; len is the size of the channel where
// 0 gives an unbuffered channel, etc. Note that because ChunkedChan buffers T objects,
func MakeChunkedChan[T any](len int) *ChunkedChan[T] {
	var t T
	return &ChunkedChan[T]{
		ch: make(chan []T, len),
		// Roughly a megabyte of buffering.
		nbuf: max(16, 1024*1024/int(max(1, unsafe.Sizeof(t)))),
	}
}

// Send takes a single item that will eventually be sent on the channel in
// a slice of other T objects.
func (c *ChunkedChan[T]) Send(t T) {
	if c.accum == nil {
		c.accum = make([]T, 0, c.nbuf)
	}

	c.accum = append(c.accum, t)

	if len(c.accum) == c.nbuf { // Send it and clean the slate.
		c.ch <- c.accum
		c.accum = nil
	}
}

// Ch gives a channel for receiving a slice of T objects; returning a channel allows
func (c *ChunkedChan[T]) Ch() <-chan []T {
	return c.ch
}

func (c *ChunkedChan[T]) Close() {
	if len(c.accum) > 0 {
		c.ch <- c.accum
	}
	c.accum = nil
	close(c.ch)
}
