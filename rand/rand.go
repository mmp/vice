// pkg/rand/rand.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package rand

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"iter"
	mrand "math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/vmihailenco/msgpack/v5"
	"golang.org/x/exp/constraints"
)

///////////////////////////////////////////////////////////////////////////
// Rand

// Rand wraps math/rand/v2's PCG generator and provides project-specific
// helpers (inclusive ranges, sampling, weighted sampling, etc.) that the
// stdlib does not. State serializes via PCG.MarshalBinary; we expose it as
// JSON and msgpack so a *Rand can ride along inside the persisted Sim.
type Rand struct {
	pcg *mrand.PCG
	r   *mrand.Rand
}

func Make() *Rand {
	r := &Rand{pcg: mrand.NewPCG(0, 0)}
	r.r = mrand.New(r.pcg)
	r.Seed(uint64(time.Now().UnixNano()))
	return r
}

func (r *Rand) Seed(s uint64) {
	r.pcg.Seed(s, 0)
}

func (r *Rand) Intn(n int) int {
	return r.r.IntN(n)
}

// IntRange returns a uniformly-sampled value in [low, high].
func (r *Rand) IntRange(low, high int) int {
	return low + r.r.IntN(high-low+1)
}

func (r *Rand) Int31n(n int32) int32 {
	return r.r.Int32N(n)
}

func (r *Rand) Float32() float32 {
	return r.r.Float32()
}

func (r *Rand) Float32Range(low, high float32) float32 {
	t := r.r.Float32()
	return (1-t)*low + t*high
}

func (r *Rand) DurationRange(low, high time.Duration) time.Duration {
	if high <= low {
		return low
	}
	return low + time.Duration(r.r.Int64N(int64(high-low)))
}

func (r *Rand) Uint32() uint32 {
	return r.r.Uint32()
}

func (r *Rand) Bool() bool {
	return r.r.Uint32()&1 == 0
}

///////////////////////////////////////////////////////////////////////////
// Serialization

func (r *Rand) MarshalBinary() ([]byte, error) {
	return r.pcg.MarshalBinary()
}

func (r *Rand) UnmarshalBinary(data []byte) error {
	if r.pcg == nil {
		r.pcg = mrand.NewPCG(0, 0)
	}
	if err := r.pcg.UnmarshalBinary(data); err != nil {
		return err
	}
	r.r = mrand.New(r.pcg)
	return nil
}

func (r *Rand) MarshalJSON() ([]byte, error) {
	data, err := r.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(data))
}

func (r *Rand) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return r.UnmarshalBinary(data)
}

func (r *Rand) MarshalMsgpack() ([]byte, error) {
	data, err := r.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return msgpack.Marshal(data)
}

func (r *Rand) UnmarshalMsgpack(b []byte) error {
	var data []byte
	if err := msgpack.Unmarshal(b, &data); err != nil {
		return err
	}
	return r.UnmarshalBinary(data)
}

///////////////////////////////////////////////////////////////////////////
// Helpers

func ShuffleSlice[Slice ~[]E, E any](s Slice, r *Rand) {
	n := len(s)
	for i := range n - 1 {
		j := i + r.Intn(n-i)
		s[i], s[j] = s[j], s[i]
	}
}

// PermutationElement returns the ith element of a random permutation of the
// set of integers [0...,n-1].
// i/n, p is hash, via Andrew Kensler
func PermutationElement(i int, n int, p uint32) int {
	ui, l := uint32(i), uint32(n)
	w := l - 1
	w |= w >> 1
	w |= w >> 2
	w |= w >> 4
	w |= w >> 8
	w |= w >> 16
	for {
		ui ^= p
		ui *= 0xe170893d
		ui ^= p >> 16
		ui ^= (ui & w) >> 4
		ui ^= p >> 8
		ui *= 0x0929eb3f
		ui ^= p >> 23
		ui ^= (ui & w) >> 1
		ui *= 1 | p>>27
		ui *= 0x6935fa69
		ui ^= (ui & w) >> 11
		ui *= 0x74dcb303
		ui ^= (ui & w) >> 2
		ui *= 0x9e501cc3
		ui ^= (ui & w) >> 2
		ui *= 0xc860a3df
		ui &= w
		ui ^= ui >> 5
		if ui < l {
			break
		}
	}
	return int((ui + p) % l)
}

func PermuteSlice[Slice ~[]E, E any](s Slice, seed uint32) iter.Seq2[int, E] {
	return func(yield func(int, E) bool) {
		for i := range len(s) {
			ip := PermutationElement(i, len(s), seed)
			if !yield(ip, s[ip]) {
				break
			}
		}
	}
}

// SampleSlice uniformly randomly samples an element of a non-empty slice.
func SampleSlice[T any](r *Rand, slice []T) T {
	return slice[r.Intn(len(slice))]
}

func Sample[T any](r *Rand, t ...T) T {
	return t[r.Intn(len(t))]
}

// SampleFiltered uniformly randomly samples a slice, returning the index
// of the sampled item, using provided predicate function to filter the
// items that may be sampled.  An index of -1 is returned if the slice is
// empty or the predicate returns false for all items.
func SampleFiltered[T any](r *Rand, slice []T, pred func(T) bool) int {
	idx := -1
	candidates := 0
	for i, v := range slice {
		if pred(v) {
			candidates++
			p := float32(1) / float32(candidates)
			if r.Float32() < p {
				idx = i
			}
		}
	}
	return idx
}

// SampleWeighted randomly samples an element from the given slice with the
// probability of choosing each element proportional to the value returned
// by the provided callback.
func SampleWeighted[T any, W constraints.Integer | constraints.Float](r *Rand, slice []T, weight func(T) W) (T, bool) {
	return SampleWeightedSeq(r, slices.Values(slice), weight)
}

func SampleSeq[T any](r *Rand, it iter.Seq[T]) (sample T, ok bool) {
	// Weighted reservoir sampling...
	n := 0
	for v := range it {
		n += 1
		p := float32(1) / float32(n)
		if r.Float32() < p {
			sample = v
			ok = true
		}
	}
	return
}

func SampleWeightedSeq[T any, W constraints.Integer | constraints.Float](r *Rand, it iter.Seq[T], weight func(T) W) (sample T, ok bool) {
	// Weighted reservoir sampling...
	var sumWt W
	for v := range it {
		w := weight(v)
		if w == 0 {
			continue
		}

		sumWt += w
		p := float32(w) / float32(sumWt)
		if r.Float32() < p {
			sample = v
			ok = true
		}
	}
	return
}

var (
	//go:embed nouns.txt
	nounsFile string
	nounList  = strings.Split(nounsFile, "\n")

	//go:embed adjectives.txt
	adjectivesFile string
	adjectiveList  = strings.Split(adjectivesFile, "\n")
)

func (r *Rand) AdjectiveNoun() string {
	return strings.TrimSpace(adjectiveList[r.Intn(len(adjectiveList))]) + "-" +
		strings.TrimSpace(nounList[r.Intn(len(nounList))])
}
