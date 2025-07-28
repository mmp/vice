// pkg/util/generic_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"maps"
	"slices"
	"testing"
	"time"
)

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
	b := FilterSlice([]int{1, 2, 3, 4, 5}, func(i int) bool { return i%2 == 0 })
	if len(b) != 2 || b[0] != 2 || b[1] != 4 {
		t.Errorf("filter evens failed: %+v", b)
	}

	odd := FilterSlice([]int{1, 2, 3, 4, 5}, func(i int) bool { return i%2 == 1 })
	if len(odd) != 3 || odd[0] != 1 || odd[1] != 3 || odd[2] != 5 {
		t.Errorf("filter odds failed: %+v", b)
	}

	c := FilterSlice([]int{1, 2, 3, 4, 5}, func(i int) bool { return i >= 3 })
	if len(c) != 3 || c[0] != 3 || c[1] != 4 || c[2] != 5 {
		t.Errorf("filter >=3 failed: %+v", c)
	}
}

func TestFilterSliceInPlace(t *testing.T) {
	a := []int{1, 2, 3, 4, 5}
	b := FilterSliceInPlace(a, func(i int) bool { return i%2 == 0 })
	if len(b) != 2 || b[0] != 2 || b[1] != 4 {
		t.Errorf("filter evens failed: %+v", b)
	}
	if a[0] != 2 || a[1] != 4 {
		t.Errorf("in place didn't reuse memory")
	}

	a = []int{1, 2, 3, 4, 5}
	odd := FilterSliceInPlace(a, func(i int) bool { return i%2 == 1 })
	if len(odd) != 3 || odd[0] != 1 || odd[1] != 3 || odd[2] != 5 {
		t.Errorf("filter odds failed: %+v", b)
	}
	if a[0] != 1 || a[1] != 3 || a[2] != 5 {
		t.Errorf("in place didn't reuse memory")
	}

	a = []int{1, 2, 3, 4, 5}
	c := FilterSliceInPlace(a, func(i int) bool { return i >= 3 })
	if len(c) != 3 || c[0] != 3 || c[1] != 4 || c[2] != 5 {
		t.Errorf("filter >=3 failed: %+v", c)
	}
	if a[0] != 3 || a[1] != 4 || a[2] != 5 {
		t.Errorf("in place didn't reuse memory")
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

		if o.A != nil && *o.A != c.str {
			t.Errorf("String decode mismatch: %q gave %q expected %q", c.json, *o.A, c.str)
		}
		if o.B != nil && !maps.Equal(*o.B, c.m) {
			t.Errorf("Map decode mismatch: %q gave %v expected %v", c.json, *o.B, c.m)
		}
	}
}

func TestAllPermutations(t *testing.T) {
	s := []int{1, 2, 3}
	count := 0
	for p := range AllPermutations(s) {
		count++
		vals := make([]int, 0)
		for _, v := range p {
			vals = append(vals, v)
		}
		if len(vals) != len(s) {
			t.Errorf("permutation has wrong length: %d vs %d", len(vals), len(s))
		}
	}
	if count != 6 {
		t.Errorf("expected 6 permutations, got %d", count)
	}
}

func TestFilterSeq(t *testing.T) {
	s := []int{1, 2, 3, 4, 5}
	count := 0
	for v := range FilterSeq(slices.Values(s), func(v int) bool { return v%2 == 0 }) {
		count++
		if v%2 != 0 {
			t.Errorf("filtered sequence contains odd number: %d", v)
		}
	}
	if count != 2 {
		t.Errorf("expected 2 even numbers, got %d", count)
	}
}

func TestFilterSeq2(t *testing.T) {
	m := map[string]int{
		"one": 1, "two": 2, "ten": 10, "zero": 0, "six": 6,
	}

	type testcase struct {
		pred   func(string, int) bool
		expect map[string]int
	}
	for i, c := range []testcase{
		testcase{
			pred:   func(s string, v int) bool { return v > 6 },
			expect: map[string]int{"ten": 10},
		},
		testcase{
			pred:   func(s string, v int) bool { return len(s) == 3 },
			expect: map[string]int{"one": 1, "two": 2, "ten": 10, "six": 6},
		},
		testcase{
			pred:   func(s string, v int) bool { return true },
			expect: m,
		},
		testcase{
			pred:   func(s string, v int) bool { return false },
			expect: nil,
		},
	} {
		r := maps.Collect(FilterSeq2(maps.All(m), c.pred))
		if !maps.Equal(r, c.expect) {
			t.Errorf("case %d: got %+v expected %+v", i, r, c.expect)
		}
	}
}

func TestSeqKeyValues(t *testing.T) {
	m := map[string]int{
		"one": 1, "two": 2, "ten": 10, "zero": 0, "six": 6,
	}

	mv := slices.Collect(maps.Values(m))
	slices.Sort(mv)
	v := slices.Collect(Seq2Values(maps.All(m)))
	slices.Sort(v)
	if !slices.Equal(mv, v) {
		t.Errorf("values mismatch: got %+v expected %+v", v, mv)
	}

	mk := slices.Collect(maps.Keys(m))
	slices.Sort(mk)
	k := slices.Collect(Seq2Keys(maps.All(m)))
	slices.Sort(k)
	if !slices.Equal(mk, k) {
		t.Errorf("values mismatch: got %+v expected %+v", k, mk)
	}
}

func TestSeqContains(t *testing.T) {
	s := []int{1, 2, 3, 4, 5}
	if !SeqContains(slices.Values(s), 3) {
		t.Error("should contain 3")
	}
	if SeqContains(slices.Values(s), 6) {
		t.Error("should not contain 6")
	}
}

func TestMapSeq(t *testing.T) {
	s := []int{1, 2, 3, 4, 5}
	count := 0
	for v := range MapSeq(slices.Values(s), func(v int) int { return 2 * v }) {
		count++
		if v%2 != 0 {
			t.Errorf("mapped sequence contains odd number: %d", v)
		}
	}
	if count != 5 {
		t.Errorf("expected 5 doubled numbers, got %d", count)
	}
}

func TestSortedMapKeys(t *testing.T) {
	m := map[int]string{
		3: "three",
		1: "one",
		2: "two",
		4: "four",
	}

	keys := SortedMapKeys(m)
	expected := []int{1, 2, 3, 4}

	if !slices.Equal(keys, expected) {
		t.Errorf("SortedMapKeys returned %v, expected %v", keys, expected)
	}
}

func TestDuplicateMap(t *testing.T) {
	original := map[string]int{
		"a": 1,
		"b": 2,
		"c": 3,
	}

	duplicate := DuplicateMap(original)

	// Check that the maps are equal
	if !maps.Equal(original, duplicate) {
		t.Error("DuplicateMap should create an identical map")
	}

	// Check that modifying the duplicate doesn't affect the original
	duplicate["d"] = 4
	if maps.Equal(original, duplicate) {
		t.Error("Modifying duplicate should not affect original")
	}
}

func TestMapContains(t *testing.T) {
	m := map[string]int{
		"a": 1,
		"b": 2,
		"c": 3,
	}

	// Test with predicate that checks for value > 2
	if !MapContains(m, func(k string, v int) bool { return v > 2 }) {
		t.Error("MapContains should find value > 2")
	}

	// Test with predicate that checks for key "d"
	if MapContains(m, func(k string, v int) bool { return k == "d" }) {
		t.Error("MapContains should not find key \"d\"")
	}
}

func TestMapSeq2(t *testing.T) {
	m := map[int]string{
		1: "one",
		2: "two",
		3: "three",
	}

	// Test mapping keys and values
	count := 0
	for k, v := range MapSeq2(maps.All(m), func(k int, v string) (string, int) { return v, k }) {
		count++
		if k == "one" && v != 1 {
			t.Errorf("MapSeq2 key-value mapping incorrect: got %d for \"one\", expected 1", v)
		}
	}
	if count != 3 {
		t.Errorf("MapSeq2 should iterate 3 times, got %d", count)
	}
}

func TestSeqLookup(t *testing.T) {
	s := []int{1, 2, 3, 4, 5}

	if _, ok := SeqLookupFunc(slices.Values(s), func(a int) bool { return a == 0 }); ok {
		t.Errorf("unexpectedly found 0 in %+v", s)
	}

	if v, ok := SeqLookupFunc(slices.Values(s), func(a int) bool { return a%2 == 0 }); !ok {
		t.Errorf("didn't find even in %+v", s)
	} else if v != 2 {
		t.Errorf("didn't find 2 as first even in %+v: got %d", s, v)
	}
}

func TestSeqMinMaxIndexFunc(t *testing.T) {
	m := map[int]string{
		1: "one",
		2: "to",
		3: "three",
		4: "four",
	}

	if idx, ok := SeqMaxIndexFunc(maps.All(m), func(i int, s string) int { return i }); !ok {
		t.Errorf("unexpected ok == false")
	} else if idx != 4 {
		t.Errorf("expected 4 for max key, got %d", idx)
	}
	if idx, ok := SeqMaxIndexFunc(maps.All(m), func(i int, s string) int { return len(s) }); !ok {
		t.Errorf("unexpected ok == false")
	} else if idx != 3 {
		t.Errorf("expected 3 for max length key, got %d", idx)
	}
	if _, ok := SeqMaxIndexFunc(nil, func(i int, s string) int { return len(s) }); ok {
		t.Errorf("unexpected ok == true for empty seq")
	}

	if idx, ok := SeqMinIndexFunc(maps.All(m), func(i int, s string) int { return i }); !ok {
		t.Errorf("unexpected ok == false")
	} else if idx != 1 {
		t.Errorf("expected 1 for min key, got %d", idx)
	}
	if idx, ok := SeqMinIndexFunc(maps.All(m), func(i int, s string) int { return len(s) }); !ok {
		t.Errorf("unexpected ok == false")
	} else if idx != 2 {
		t.Errorf("expected 2 for min length key, got %d", idx)
	}
	if _, ok := SeqMinIndexFunc(nil, func(i int, s string) int { return len(s) }); ok {
		t.Errorf("unexpected ok == true for empty seq")
	}
}
