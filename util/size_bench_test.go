package util

import (
	"io"
	"testing"
)

func BenchmarkSizeOf(b *testing.B) {
	type Node struct {
		Value    int
		Children []*Node
		Data     []byte
	}

	// Create a tree structure
	root := &Node{
		Value: 1,
		Data:  make([]byte, 100),
		Children: []*Node{
			{
				Value: 2,
				Data:  make([]byte, 200),
				Children: []*Node{
					{Value: 4, Data: make([]byte, 50)},
					{Value: 5, Data: make([]byte, 50)},
				},
			},
			{
				Value: 3,
				Data:  make([]byte, 200),
				Children: []*Node{
					{Value: 6, Data: make([]byte, 50)},
					{Value: 7, Data: make([]byte, 50)},
				},
			},
		},
	}

	b.Run("NoThreshold", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			SizeOf(root, io.Discard, false, 0)
		}
	})

	b.Run("WithThreshold", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			SizeOf(root, io.Discard, false, 1000)
		}
	})

	b.Run("LargeSlice", func(b *testing.B) {
		// Test with a large slice to see allocation differences
		data := make([]Node, 100)
		for i := range data {
			data[i] = Node{
				Value: i,
				Data:  make([]byte, 10),
			}
		}

		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			SizeOf(data, io.Discard, false, 0)
		}
	})
}
