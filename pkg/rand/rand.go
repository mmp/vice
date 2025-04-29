// pkg/rand/rand.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package rand

import (
	_ "embed"
	"iter"
	"slices"
	"strings"
	"time"
)

///////////////////////////////////////////////////////////////////////////
// PCG32

// This is based on mtj's pcg32 implementation, updated with exported
// variables for the state (so we can serialize it properly.)

const (
	pcg32State      = 0x853c49e6748fea9b //  9600629759793949339
	pcg32Increment  = 0xda3e39cb94b95bdb // 15726070495360670683
	pcg32Multiplier = 0x5851f42d4c957f2d //  6364136223846793005
)

type PCG32 struct {
	State     uint64
	Increment uint64
}

func NewPCG32() PCG32 {
	return PCG32{pcg32State, pcg32Increment}
}

func (p *PCG32) Seed(state, sequence uint64) {
	p.Increment = (sequence << 1) | 1
	p.State = (state+p.Increment)*pcg32Multiplier + p.Increment
}

func (p *PCG32) Random() uint32 {
	// Advance 64-bit linear congruential generator to new state
	oldState := p.State
	p.State = oldState*pcg32Multiplier + p.Increment

	// Confuse and permute 32-bit output from old state
	xorShifted := uint32(((oldState >> 18) ^ oldState) >> 27)
	rot := uint32(oldState >> 59)
	return (xorShifted >> rot) | (xorShifted << ((-rot) & 31))
}

func (p *PCG32) Bounded(bound uint32) uint32 {
	if bound == 0 {
		return 0
	}
	threshold := -bound % bound
	for {
		r := p.Random()
		if r >= threshold {
			return r % bound
		}
	}
}

///////////////////////////////////////////////////////////////////////////
// Random numbers.

type Rand struct {
	PCG32
}

func Make() *Rand {
	r := &Rand{PCG32: NewPCG32()}
	r.Seed(uint64(time.Now().UnixNano()))
	return r
}

func (r *Rand) Seed(s uint64) {
	r.PCG32.Seed(s, pcg32Increment)
}

func (r *Rand) Intn(n int) int {
	return int(r.Bounded(uint32(n)))
}

func (r *Rand) Int31n(n int32) int32 {
	return int32(r.Bounded(uint32(n)))
}

func (r *Rand) Float32() float32 {
	return float32(r.Random()) / (1<<32 - 1)
}

func (r *Rand) Uint32() uint32 {
	return r.Random()
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
func SampleWeighted[T any](r *Rand, slice []T, weight func(T) int) (T, bool) {
	return SampleWeightedSeq(r, slices.Values(slice), weight)
}

func SampleWeightedSeq[T any](r *Rand, it iter.Seq[T], weight func(T) int) (sample T, ok bool) {
	// Weighted reservoir sampling...
	sumWt := 0
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
	nounList  []string

	//go:embed adjectives.txt
	adjectivesFile string
	adjectiveList  []string
)

func init() {
	nounList = strings.Split(nounsFile, "\n")
	adjectiveList = strings.Split(adjectivesFile, "\n")
}

func (r *Rand) AdjectiveNoun() string {
	return strings.TrimSpace(adjectiveList[r.Intn(len(adjectiveList))]) + "-" +
		strings.TrimSpace(nounList[r.Intn(len(nounList))])
}
