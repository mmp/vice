package util

import (
	"testing"
)

func TestSizeOf(t *testing.T) {
	// Test basic types
	var i int64 = 42
	size := SizeOf(i, false, 0)
	if size != 8 {
		t.Errorf("Expected int64 size to be 8, got %d", size)
	}

	// Test string
	s := "hello"
	size = SizeOf(s, false, 0)
	if size < 16+5 { // string header + data
		t.Errorf("Expected string size to be at least %d, got %d", 16+5, size)
	}

	// Test slice
	slice := []int{1, 2, 3, 4, 5}
	size = SizeOf(slice, false, 0)
	expectedMin := 24 + 5*8 // slice header + 5 ints
	if size < int64(expectedMin) {
		t.Errorf("Expected slice size to be at least %d, got %d", expectedMin, size)
	}

	// Test struct
	type Person struct {
		Name    string
		Age     int
		Hobbies []string
		Data    map[string]int
	}

	p := Person{
		Name:    "Alice",
		Age:     30,
		Hobbies: []string{"reading", "swimming"},
		Data:    map[string]int{"score": 100, "level": 5},
	}

	size = SizeOf(p, false, 0)
	if size < 100 { // struct should be reasonably large
		t.Errorf("Expected struct size to be at least 100, got %d", size)
	}

	// Test pointer
	ptr := &Person{Name: "Bob", Age: 25}
	size = SizeOf(ptr, false, 0)
	if size < 8 { // at least pointer size
		t.Errorf("Expected pointer size to be at least 8, got %d", size)
	}

	// Test nil
	size = SizeOf(nil, false, 0)
	if size != 0 {
		t.Errorf("Expected nil size to be 0, got %d", size)
	}

	// Test circular reference handling
	type Node struct {
		Value int
		Next  *Node
	}
	n1 := &Node{Value: 1}
	n2 := &Node{Value: 2, Next: n1}
	n1.Next = n2 // circular reference
	
	size = SizeOf(n1, false, 0)
	if size == 0 || size > 1000 { // should handle circular refs gracefully
		t.Errorf("Unexpected size for circular reference: %d", size)
	}
}

func TestSizeOfWithPrintMembers(t *testing.T) {
	type TestStruct struct {
		ID      int64
		Name    string
		Values  []float64
		Mapping map[string]bool
	}

	ts := TestStruct{
		ID:      12345,
		Name:    "test object",
		Values:  []float64{1.1, 2.2, 3.3},
		Mapping: map[string]bool{"enabled": true, "debug": false},
	}

	// This test just ensures printMembers doesn't panic
	t.Log("Testing with printMembers=true:")
	size := SizeOf(ts, true, 0)
	if size < 100 {
		t.Errorf("Expected struct with data to be at least 100 bytes, got %d", size)
	}
}

func TestSizeOfWithThreshold(t *testing.T) {
	type LargeStruct struct {
		Data1 [1024]byte
		Data2 [2048]byte
		Small int
	}

	ls := LargeStruct{}

	t.Log("Testing with threshold=1000:")
	size := SizeOf(ls, false, 1000)
	// The total struct and its large fields should be printed
	expectedSize := int64(1024 + 2048 + 8) // Data1 + Data2 + Small
	if size < expectedSize {
		t.Errorf("Expected size to be at least %d, got %d", expectedSize, size)
	}

	// Test nested structures with threshold
	type Container struct {
		Items []LargeStruct
		Name  string
	}

	c := Container{
		Items: []LargeStruct{{}, {}},
		Name:  "test",
	}

	t.Log("Testing nested struct with threshold=2000:")
	size = SizeOf(c, false, 2000)
	if size < int64(2*int(expectedSize)) {
		t.Errorf("Expected container size to be at least %d, got %d", 2*int(expectedSize), size)
	}
}