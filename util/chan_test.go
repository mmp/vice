// pkg/util/chan_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"testing"
)

func TestChunkedChanBasicSend(t *testing.T) {
	cc := MakeChunkedChan[int](1)

	// Send enough items to trigger a chunk
	expectedChunkSize := cc.nbuf
	for i := range expectedChunkSize {
		cc.Send(i)
	}

	// Should receive exactly one chunk
	chunk := <-cc.Ch()
	if len(chunk) != expectedChunkSize {
		t.Errorf("expected chunk size %d, got %d", expectedChunkSize, len(chunk))
	}
	for i := range chunk {
		if chunk[i] != i {
			t.Errorf("expected chunk[%d] = %d, got %d", i, i, chunk[i])
		}
	}

	cc.Close()
}

func TestChunkedChanPartialChunkOnClose(t *testing.T) {
	cc := MakeChunkedChan[string](1)

	// Send fewer items than chunk size
	testStrings := []string{"hello", "world", "test"}
	for _, s := range testStrings {
		cc.Send(s)
	}

	// Should not receive anything yet
	select {
	case <-cc.Ch():
		t.Error("should not receive chunk before Close")
	default:
		// Expected
	}

	// Close should flush the partial chunk
	cc.Close()

	// Should receive the partial chunk
	chunk := <-cc.Ch()
	if len(chunk) != len(testStrings) {
		t.Errorf("expected chunk size %d, got %d", len(testStrings), len(chunk))
	}
	for i, s := range testStrings {
		if chunk[i] != s {
			t.Errorf("expected chunk[%d] = %s, got %s", i, s, chunk[i])
		}
	}

	// Channel should be closed
	_, ok := <-cc.Ch()
	if ok {
		t.Error("expected channel to be closed")
	}
}

func TestChunkedChanMultipleChunks(t *testing.T) {
	cc := MakeChunkedChan[float64](4)

	chunkSize := cc.nbuf
	numChunks := 3
	remainder := 5

	// Send multiple full chunks plus a partial
	total := chunkSize*numChunks + remainder
	for i := range total {
		cc.Send(float64(i))
	}

	// Should receive exactly numChunks full chunks
	for n := range numChunks {
		chunk := <-cc.Ch()
		if len(chunk) != chunkSize {
			t.Errorf("chunk %d: expected size %d, got %d", n, chunkSize, len(chunk))
		}
		for i := range chunk {
			expected := float64(n*chunkSize + i)
			if chunk[i] != expected {
				t.Errorf("chunk %d[%d]: expected %f, got %f", n, i, expected, chunk[i])
			}
		}
	}

	// Should not have the partial chunk yet
	select {
	case <-cc.Ch():
		t.Error("should not receive partial chunk before Close")
	default:
		// Expected
	}

	// Close and get the remainder
	cc.Close()

	chunk := <-cc.Ch()
	if len(chunk) != remainder {
		t.Errorf("expected final chunk size %d, got %d", remainder, len(chunk))
	}
	for i := range remainder {
		expected := float64(numChunks*chunkSize + i)
		if chunk[i] != expected {
			t.Errorf("final chunk[%d]: expected %f, got %f", i, expected, chunk[i])
		}
	}
}

func TestChunkedChanEmptyClose(t *testing.T) {
	cc := MakeChunkedChan[int](1)

	// Close without sending anything
	cc.Close()

	// Channel should be closed with no data
	_, ok := <-cc.Ch()
	if ok {
		t.Error("expected channel to be closed")
	}
}
