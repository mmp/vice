// pkg/util/generic.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/mmp/vice/pkg/math"

	"golang.org/x/exp/constraints"
)

///////////////////////////////////////////////////////////////////////////
// TransientMap

// TransientMap represents a set of objects with a built-in expiry time in
// the future; after an item's time passes, it is automatically removed
// from the set.
type TransientMap[K comparable, V any] struct {
	m map[K]valueTime[V]
}

type valueTime[V any] struct {
	v V
	t time.Time
}

func NewTransientMap[K comparable, V any]() *TransientMap[K, V] {
	return &TransientMap[K, V]{m: make(map[K]valueTime[V])}
}

func (t *TransientMap[K, V]) flush() {
	now := time.Now()
	for k, vt := range t.m {
		if now.After(vt.t) {
			delete(t.m, k)
		}
	}
}

// Add adds a given value to the set; it will no longer be there after the
// specified duration has passed.
func (t *TransientMap[K, V]) Add(key K, value V, d time.Duration) {
	t.m[key] = valueTime[V]{v: value, t: time.Now().Add(d)}
}

// Get looks up the given key in the map and returns its value and a
// Boolean that indicates whether it was found.
func (t *TransientMap[K, V]) Get(key K) (V, bool) {
	t.flush()
	vt, ok := t.m[key]
	return vt.v, ok
}

// Delete deletes the item in the map with the given key, if present.
func (t *TransientMap[K, V]) Delete(key K) {
	delete(t.m, key)
}

///////////////////////////////////////////////////////////////////////////
// RingBuffer

// RingBuffer represents an array of no more than a given maximum number of
// items.  Once it has filled, old items are discarded to make way for new
// ones.
type RingBuffer[V any] struct {
	entries []V
	max     int
	index   int
}

func NewRingBuffer[V any](capacity int) *RingBuffer[V] {
	return &RingBuffer[V]{max: capacity}
}

// Add adds all of the provided values to the ring buffer.
func (r *RingBuffer[V]) Add(values ...V) {
	for _, v := range values {
		if len(r.entries) < r.max {
			// Append to the entries slice if it hasn't yet hit the limit.
			r.entries = append(r.entries, v)
		} else {
			// Otherwise treat r.entries as a ring buffer where
			// (r.index+1)%r.max is the oldest entry and successive newer
			// entries follow.
			r.entries[r.index%r.max] = v
		}
		r.index++
	}
}

// Size returns the total number of items stored in the ring buffer.
func (r *RingBuffer[V]) Size() int {
	return math.Min(len(r.entries), r.max)
}

// Get returns the specified element of the ring buffer where the index i
// is between 0 and Size()-1 and 0 is the oldest element in the buffer.
func (r *RingBuffer[V]) Get(i int) V {
	return r.entries[(r.index+i)%len(r.entries)]
}

///////////////////////////////////////////////////////////////////////////
// SingleOrArray

// SingleOrArray makes it possible to have an object in a JSON file that
// may be initialized with either a single value or an array of values.  In
// either case, the object's value is represented by a slice of the
// underlying type.
type SingleOrArray[V any] []V

func (s *SingleOrArray[V]) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = nil
		return nil
	}

	if n := len(b); n > 2 && b[0] == '[' && b[n-1] == ']' { // Array
		var v []V
		err := json.Unmarshal(b, &v)
		if err != nil {
			return err
		}
		*s = v
		return nil
	} else {
		var v V
		err := json.Unmarshal(b, &v)
		if err != nil {
			return err
		}
		*s = []V{v}
		return nil
	}
}

func init() {
	// Register a custom checker function for SingleOrArray so that our
	// json type validation pass can handle it.
	RegisterJSONTypeChecker[SingleOrArray[int]](func(json interface{}) bool {
		if _, ok := json.(float64); ok {
			// A single number is fine.
			return true
		} else if arr, ok := json.([]interface{}); ok {
			// Otherwise it has to be an array of numbers.
			for _, v := range arr {
				if _, ok := v.(float64); !ok {
					return false
				}
			}
			return true
		} else {
			return false
		}
	})
}

///////////////////////////////////////////////////////////////////////////

func Select[T any](sel bool, a, b T) T {
	if sel {
		return a
	} else {
		return b
	}
}

// FlattenMap takes a map and returns separate slices corresponding to the
// keys and values stored in the map.  (The slices are ordered so that the
// i'th key corresponds to the i'th value, needless to say.)
func FlattenMap[K comparable, V any](m map[K]V) ([]K, []V) {
	keys := make([]K, 0, len(m))
	values := make([]V, 0, len(m))
	for k, v := range m {
		keys = append(keys, k)
		values = append(values, v)
	}
	return keys, values
}

// SortedMapKeys returns the keys of the given map, sorted from low to high.
func SortedMapKeys[K constraints.Ordered, V any](m map[K]V) []K {
	keys, _ := FlattenMap(m)
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// DuplicateMap returns a newly-allocated map that stores copies of all of
// the values in the given map.
func DuplicateMap[K comparable, V any](m map[K]V) map[K]V {
	mnew := make(map[K]V)
	for k, v := range m {
		mnew[k] = v
	}
	return mnew
}

// FilterMap returns a newly-allocated result that is the result of
// applying the given predicate function to all of the elements in the
// given map and only including those for which the predicate returned
// true.
func FilterMap[K comparable, V any](m map[K]V, pred func(K, V) bool) map[K]V {
	mnew := make(map[K]V)
	for k, v := range m {
		if pred(k, v) {
			mnew[k] = v
		}
	}
	return mnew
}

// ReduceSlice applies the provided reduction function to the given slice,
// starting with the provided initial value.  The update rule applied is
// result=reduce( value, result), where the initial value of result is
// given by the initial parameter.
func ReduceSlice[V any, R any](s []V, reduce func(V, R) R, initial R) R {
	result := initial
	for _, v := range s {
		result = reduce(v, result)
	}
	return result
}

// ReduceMap applies the provided reduction function to the given map,
// starting with the provided initial value.  The update rule applied is
// result=reduce(key, value, result), where the initial value of result is
// given by the initial parameter.
func ReduceMap[K comparable, V any, R any](m map[K]V, reduce func(K, V, R) R, initial R) R {
	result := initial
	for k, v := range m {
		result = reduce(k, v, result)
	}
	return result
}

func MapContains[K comparable, V any](m map[K]V, pred func(K, V) bool) bool {
	for k, v := range m {
		if pred(k, v) {
			return true
		}
	}
	return false
}

// DuplicateSlice returns a newly-allocated slice that is a copy of the
// provided one.
func DuplicateSlice[V any](s []V) []V {
	dupe := make([]V, len(s))
	copy(dupe, s)
	return dupe
}

// DeleteSliceElement deletes the i'th element of the given slice,
// returning the resulting slice.  Note that the provided slice s is
// modified!
func DeleteSliceElement[V any](s []V, i int) []V {
	// First move any subsequent elements down one position.
	if i+1 < len(s) {
		copy(s[i:], s[i+1:])
	}
	// And drop the now-unnecessary final element.
	return s[:len(s)-1]
}

// InsertSliceElement inserts the given value v at the index i in the slice
// s, moving all elements after i one place forward.
func InsertSliceElement[V any](s []V, i int, v V) []V {
	s = append(s, v) // just to grow the slice (unless i == len(s))
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// MapSlice returns the slice that is the result of applying the provided
// xform function to all of the elements of the given slice.
func MapSlice[F, T any](from []F, xform func(F) T) []T {
	var to []T
	for _, item := range from {
		to = append(to, xform(item))
	}
	return to
}

// FilterSlice applies the given filter function pred to the given slice,
// returning a new slice that only contains elements where pred returned
// true.
func FilterSlice[V any](s []V, pred func(V) bool) []V {
	var filtered []V
	for _, item := range s {
		if pred(item) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
