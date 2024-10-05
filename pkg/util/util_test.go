// pkg/util/util_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWrapText(t *testing.T) {
	input := "this is a test_with_a_long_line of stuff"
	expected := "this is \n  a \n  test_with_a_long_line \n  of \n  stuff"
	wrap, lines := WrapText(input, 8, 2, false)
	if wrap != expected {
		t.Errorf("wrapping gave %q; expected %q", wrap, expected)
	}
	if lines != 5 {
		t.Errorf("wrapping returned %d lines, expected 5", lines)
	}
}

func TestStopShouting(t *testing.T) {
	input := "UNITED AIRLINES (North America)"
	expected := "United Airlines (North America)"
	ss := StopShouting(input)
	if ss != expected {
		t.Errorf("Got %q, expected %q", ss, expected)
	}
}

func TestTransientMap(t *testing.T) {
	ts := NewTransientMap[int, int]()
	ts.Add(1, 10, 250*time.Millisecond)
	ts.Add(2, 20, 750*time.Millisecond)

	// Should have both
	if v, ok := ts.Get(1); !ok {
		t.Errorf("transient set doesn't have expected entry")
	} else if v != 10 {
		t.Errorf("transient set didn't return expected value")
	}
	if v, ok := ts.Get(2); !ok {
		t.Errorf("transient set doesn't have expected entry")
	} else if v != 20 {
		t.Errorf("transient set didn't return expected value")
	}

	// Note that after this point this test has the potential to be flaky,
	// if the thread is not scheduled for ~250+ms; it's possible that more
	// time will elapse than we think and thence some of the checks may not
	// add up...
	time.Sleep(500 * time.Millisecond)

	// Should just have 2
	if _, ok := ts.Get(1); ok {
		t.Errorf("transient set still has value that it shouldn't")
	}
	if v, ok := ts.Get(2); !ok {
		t.Errorf("transient set doesn't have expected entry")
	} else if v != 20 {
		t.Errorf("transient set didn't return expected value")
	}

	time.Sleep(250 * time.Millisecond)

	if _, ok := ts.Get(1); ok {
		t.Errorf("transient set still has value that it shouldn't")
	}
	if _, ok := ts.Get(2); ok {
		t.Errorf("transient set still has value that it shouldn't")
	}
}

func TestMapSlice(t *testing.T) {
	a := []int{1, 2, 3, 4, 5}
	b := MapSlice[int, float32](a, func(i int) float32 { return 2 * float32(i) })
	if len(a) != len(b) {
		t.Errorf("lengths mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if float32(2*a[i]) != b[i] {
			t.Errorf("value %d mismatch %f vs %f", i, float32(2*a[i]), b[i])
		}
	}
}

func TestDeleteSliceElement(t *testing.T) {
	a := []int{1, 2, 3, 4, 5}
	a = DeleteSliceElement(a, 2)
	if !slices.Equal(a, []int{1, 2, 4, 5}) {
		t.Errorf("Slice element delete incorrect")
	}
	a = DeleteSliceElement(a, 3)
	if !slices.Equal(a, []int{1, 2, 4}) {
		t.Errorf("Slice element delete incorrect")
	}
	a = DeleteSliceElement(a, 0)
	if !slices.Equal(a, []int{2, 4}) {
		t.Errorf("Slice element delete incorrect")
	}
	a = DeleteSliceElement(a, 1)
	if !slices.Equal(a, []int{2}) {
		t.Errorf("Slice element delete incorrect")
	}
	a = DeleteSliceElement(a, 0)
	if !slices.Equal(a, nil) {
		t.Errorf("Slice element delete incorrect")
	}
}

func TestInsertSliceElement(t *testing.T) {
	a := []int{1, 2, 4, 5}
	a = InsertSliceElement(a, 2, 3)
	if !slices.Equal(a, []int{1, 2, 3, 4, 5}) {
		t.Errorf("Slice insert incorrect: %+v", a)
	}

	a = InsertSliceElement(a, 0, 0)
	if !slices.Equal(a, []int{0, 1, 2, 3, 4, 5}) {
		t.Errorf("Slice insert incorrect: %+v", a)
	}

	a = InsertSliceElement(a, 6, 6)
	if !slices.Equal(a, []int{0, 1, 2, 3, 4, 5, 6}) {
		t.Errorf("Slice insert incorrect: %+v", a)
	}
}

func TestFilterSlice(t *testing.T) {
	a := []int{1, 2, 3, 4, 5}
	b := FilterSlice(a, func(i int) bool { return i%2 == 0 })
	if len(b) != 2 || b[0] != 2 || b[1] != 4 {
		t.Errorf("filter evens failed: %+v", b)
	}

	c := FilterSlice(a, func(i int) bool { return i >= 3 })
	if len(c) != 3 || c[0] != 3 || c[1] != 4 || c[2] != 5 {
		t.Errorf("filter >=3 failed: %+v", c)
	}
}

func TestRingBuffer(t *testing.T) {
	rb := NewRingBuffer[int](10)

	if rb.Size() != 0 {
		t.Errorf("empty should have zero size")
	}

	rb.Add(0, 1, 2, 3, 4)
	if rb.Size() != 5 {
		t.Errorf("expected size 5; got %d", rb.Size())
	}
	for i := 0; i < 5; i++ {
		if rb.Get(i) != i {
			t.Errorf("returned unexpected value")
		}
	}

	for i := 5; i < 18; i++ {
		rb.Add(i)
	}
	if rb.Size() != 10 {
		t.Errorf("expected size 10")
	}
	for i := 0; i < 10; i++ {
		if rb.Get(i) != 8+i {
			t.Errorf("after filling, at %d got %d, expected %d", i, rb.Get(i), 8+i)
		}
	}
}

func TestReduceSlice(t *testing.T) {
	v := []int{1, -2, 3, 4}

	if r := ReduceSlice(v, func(v int, r int) int { return v + r }, 10); r != 16 {
		t.Errorf("ReduceSlice with + got %d, not 16 expected", r)
	}

	if r := ReduceSlice(v, func(v int, r int) int { return v * r }, 2); r != -48 {
		t.Errorf("ReduceSlice with * got %d, not -48 expected", r)
	}
}

func TestReduceMap(t *testing.T) {
	m := map[int]string{
		0:  "hello",
		16: "foobar",
		2:  "greets",
		7:  "x",
	}

	reduce := func(k int, v string, length int) int {
		return length + len(v)
	}

	length := ReduceMap[int, string, int](m, reduce, 5)

	if length != 5+5+6+6+1 {
		t.Errorf("Expected %d from ReduceMap; got %d", 5+5+6+6+1, length)
	}
}

func TestSingleOrArrayJSON(t *testing.T) {
	type test struct {
		json   string
		expect []int
		err    bool
	}
	for _, c := range []test{
		test{json: "null", expect: []int{}},
		test{json: "1234", expect: []int{1234}},
		test{json: "[ 1, 4, 9]", expect: []int{1, 4, 9}},
		test{json: "hai", err: true},
		test{json: `{ "foo": 12 }`, err: true},
	} {
		var s SingleOrArray[int]
		err := s.UnmarshalJSON([]byte(c.json))
		if err != nil && c.err == false {
			t.Errorf("Unexpected error %q -> %v", c.json, err)
		} else if err == nil && c.err == true {
			t.Errorf("Expected error for %q but got none", c.json)
		}

		if slices.Compare(s, c.expect) != 0 {
			t.Errorf("Decode mismatch: %q gave %v expected %v", c.json, s, c.expect)
		}
	}
}

func TestOneOfJSON(t *testing.T) {
	type test struct {
		json string
		str  string
		m    map[string]string
		err  bool
	}
	for _, c := range []test{
		test{json: `"hello"`, str: "hello"},
		test{json: `{ "a": "1", "b": "2" }`, m: map[string]string{"a": "1", "b": "2"}},
		test{json: "1234", err: true},
	} {
		var o OneOf[string, map[string]string]
		err := o.UnmarshalJSON([]byte(c.json))

		if err != nil && c.err == false {
			t.Errorf("Unexpected error %q -> %v", c.json, err)
		} else if err == nil && c.err == true {
			t.Errorf("Expected error for %q but got none", c.json)
		}

		if c.str != "" {
			if o.A == nil {
				t.Errorf("Decode mismatch: %q gave no string, expected %q", c.json, c.str)
			} else if c.str != *o.A {
				t.Errorf("Decode mismatch: %q gave %q, expected %q", c.json, *o.A, c.str)
			}
			if o.B != nil {
				t.Errorf("Decode of %q gave map result: %v", c.json, *o.B)
			}
		}
		if len(c.m) > 0 {
			if o.B == nil || len(*o.B) == 0 {
				t.Errorf("Decode mismatch: %q gave no map, expected %v", c.json, c.m)
			} else if !maps.Equal(c.m, *o.B) {
				t.Errorf("Decode error %q gave mismatching maps: got %v expected %v",
					c.json, *o.B, c.m)
			}
		}
	}
}

func TestAllPermutations(t *testing.T) {
	for _, s := range [][]int{[]int{2, 4, 6, 8}, []int{2, 4, 6, 8, 10}, []int{1}, []int{}} {
		var seen [][]int

		for it := range AllPermutations(s) {
			var p []int
			for _, v := range it {
				if !slices.Contains(s, v) {
					t.Errorf("Permutation returned value %v not in slice", v)
				}
				p = append(p, v)
			}

			if len(p) != len(s) {
				t.Errorf("Perm has different number of values? %+v", p)
			}

			if slices.ContainsFunc(seen, func(a []int) bool { return slices.Compare(a, p) == 0 }) {
				t.Errorf("Seen %+v already", p)
			}
			seen = append(seen, p)
		}

		var factorial func(int) int
		factorial = func(n int) int {
			if n <= 2 {
				return n
			}
			return n * factorial(n-1)
		}
		if len(seen) != factorial(len(s)) {
			t.Errorf("Expected %d permutations, got %d", factorial(len(s)), len(seen))
		}
	}
}

func TestHash(t *testing.T) {
	h, err := Hash(strings.NewReader("hello world"))
	if err != nil {
		t.Errorf("hash error: %v", err)
	}
	if !slices.Equal(h, []byte{0xb9, 0x4d, 0x27, 0xb9, 0x93, 0x4d, 0x3e, 0x08, 0xa5, 0x2e, 0x52, 0xd7, 0xda, 0x7d, 0xab,
		0xfa, 0xc4, 0x84, 0xef, 0xe3, 0x7a, 0x53, 0x80, 0xee, 0x90, 0x88, 0xf7, 0xac, 0xe2, 0xef, 0xcd, 0xe9}) {
		t.Errorf("hash mismatch")
	}
}
