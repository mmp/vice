// pkg/util/generic.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"encoding/json"
	"iter"
	"maps"
	"slices"
	"time"

	"github.com/mmp/vice/pkg/math"

	"github.com/iancoleman/orderedmap"
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
// OrderedMap

type OrderedMap struct {
	orderedmap.OrderedMap
}

func (o *OrderedMap) CheckJSON(json interface{}) bool {
	_, ok := json.(map[string]interface{})
	return ok
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

func (s *SingleOrArray[V]) CheckJSON(json interface{}) bool {
	return TypeCheckJSON[V](json) || TypeCheckJSON[[]V](json)
}

///////////////////////////////////////////////////////////////////////////
// OneOf

type OneOf[A, B any] struct {
	A *A
	B *B
}

func (o OneOf[A, B]) MarshalJSON() ([]byte, error) {
	if o.A != nil {
		return json.Marshal(*o.A)
	} else if o.B != nil {
		return json.Marshal(*o.B)
	} else {
		return []byte("null"), nil
	}
}

func (o *OneOf[A, B]) UnmarshalJSON(j []byte) error {
	o.A = nil
	o.B = nil
	if string(j) == "null" {
		return nil
	}

	var a A
	if err := json.Unmarshal(j, &a); err == nil {
		o.A = &a
		return nil
	}
	var b B
	err := json.Unmarshal(j, &b)
	if err == nil {
		o.B = &b
	}
	return err
}

func (o OneOf[A, B]) CheckJSON(json interface{}) bool {
	return TypeCheckJSON[A](json) || TypeCheckJSON[B](json)
}

///////////////////////////////////////////////////////////////////////////

func Select[T any](sel bool, a, b T) T {
	if sel {
		return a
	} else {
		return b
	}
}

// SortedMapKeys returns the keys of the given map, sorted from low to high.
func SortedMapKeys[K constraints.Ordered, V any](m map[K]V) []K {
	return slices.Sorted(maps.Keys(m))
}

// DuplicateMap returns a newly allocated map
// that stores copies of all the values in the given map.
func DuplicateMap[K comparable, V any](m map[K]V) map[K]V {
	mnew := make(map[K]V, len(m))
	maps.Copy(mnew, m)
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

// DeleteSliceElement deletes the i-th element of the given slice,
// returning the resulting slice.
//
// Note that the provided slice s is modified!
func DeleteSliceElement[V any](s []V, i int) []V {
	return slices.Delete(s, i, i+1)
}

// InsertSliceElement inserts the given value v at the index i in the slice s,
// moving all elements after i one place forward.
func InsertSliceElement[V any](s []V, i int, v V) []V {
	return slices.Insert(s, i, v)
}

// MapSlice returns the slice that is the result of
// applying the provided xform function to all the elements of the given slice.
func MapSlice[F, T any](from []F, xform func(F) T) []T {
	to := make([]T, len(from))
	for i := range from {
		to[i] = xform(from[i])
	}
	return to
}

// FilterSlice applies the given filter function pred to the given slice,
// returning a new slice that only contains elements where pred returned true.
func FilterSlice[V any](s []V, pred func(V) bool) []V {
	var filtered []V
	for i := range s {
		if pred(s[i]) {
			filtered = append(filtered, s[i])
		}
	}
	return filtered
}

// FilterSliceInPlace applies the given filter function pred to the given
// slice, returning a slice constructed from the provided slice's memory
// that only contains elements where pred returned true.
func FilterSliceInPlace[V any](s []V, pred func(V) bool) []V {
	var out int
	for i := range s {
		if pred(s[i]) {
			if i != out {
				s[out] = s[i]
			}
			out++
		}
	}
	return s[:out]
}

// AllPermutations returns an iterator over all permutations of the given
// slice.  Each permutation can then be iterated over.
func AllPermutations[S ~[]E, E any](s S) iter.Seq[iter.Seq2[int, E]] {
	if len(s) == 0 {
		return func(yield func(iter.Seq2[int, E]) bool) {}
	}

	// https://stackoverflow.com/a/30230552
	// Fisher-Yates shuffle offsets
	shuffle := make([]int, len(s))
	next := func() {
		for i := len(shuffle) - 1; i >= 0; i-- {
			if i == 0 || shuffle[i] < len(shuffle)-i-1 {
				shuffle[i]++
				return
			}
			shuffle[i] = 0
		}
	}

	return func(yield func(iter.Seq2[int, E]) bool) {
		for shuffle[0] < len(s) {
			// TODO: it would be nice to incrementally maintain perm in next()
			perm := make([]int, len(s))
			for i := range perm {
				perm[i] = i
			}

			if !yield(func(yield2 func(int, E) bool) {
				for i := range s {
					perm[i], perm[i+shuffle[i]] = perm[i+shuffle[i]], perm[i]

					if !yield2(i, s[perm[i]]) {
						return
					}
				}
			}) {
				return
			}

			next()
		}
	}
}

func SliceReverseValues[Slice ~[]E, E any](s Slice) iter.Seq[E] {
	return func(yield func(E) bool) {
		for _, v := range SliceReverseValues2(s) {
			if !yield(v) {
				break
			}
		}
	}
}

func SliceReverseValues2[Slice ~[]E, E any](s Slice) iter.Seq2[int, E] {
	return func(yield func(int, E) bool) {
		for i := len(s) - 1; i >= 0; i-- {
			if !yield(i, s[i]) {
				break
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// operations on iterator sequences

// FilterSeq applies uses the given predicate function to filter the
// elements given by a sequence iterator, returning an iterator over the
// filtered elements.
func FilterSeq[T any](seq iter.Seq[T], pred func(T) bool) iter.Seq[T] {
	return func(yield func(T) bool) {
		for v := range seq {
			if pred(v) {
				if !yield(v) {
					return
				}
			}
		}
	}
}

func SeqContains[T comparable](seq iter.Seq[T], v T) bool {
	for s := range seq {
		if s == v {
			return true
		}
	}
	return false
}

func SeqContainsFunc[T any](seq iter.Seq[T], check func(T) bool) bool {
	for s := range seq {
		if check(s) {
			return true
		}
	}
	return false
}

func MapSeq[T, U any](seq iter.Seq[T], f func(T) U) iter.Seq[U] {
	return func(yield func(U) bool) {
		for v := range seq {
			if !yield(f(v)) {
				break
			}
		}
	}
}

func MapSeq2[K, V, K2, V2 any](seq iter.Seq2[K, V], f func(K, V) (K2, V2)) iter.Seq2[K2, V2] {
	return func(yield func(K2, V2) bool) {
		for k, v := range seq {
			if !yield(f(k, v)) {
				break
			}
		}
	}
}
