// pkg/rand/rand.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package rand

import (
	_ "embed"
	"iter"
	"strings"

	"github.com/MichaelTJones/pcg"
)

///////////////////////////////////////////////////////////////////////////
// Random numbers.

type Rand struct {
	r *pcg.PCG32
}

func New() Rand {
	return Rand{r: pcg.NewPCG32()}
}

func (r *Rand) Seed(s int64) {
	r.r.Seed(uint64(s), 0xda3e39cb94b95bdb)
}

func (r *Rand) Intn(n int) int {
	return int(r.r.Bounded(uint32(n)))
}

func (r *Rand) Int31n(n int32) int32 {
	return int32(r.r.Bounded(uint32(n)))
}

func (r *Rand) Float32() float32 {
	return float32(r.r.Random()) / (1<<32 - 1)
}

func (r *Rand) Uint32() uint32 {
	return r.r.Random()
}

// Drop-in replacement for the subset of math/rand that we use...
var r Rand

func init() {
	r = New()
}

func Seed(s int64) {
	r.r.Seed(uint64(s), 0xda3e39cb94b95bdb)
}

func Intn(n int) int {
	return int(r.r.Bounded(uint32(n)))
}

func Int31n(n int32) int32 {
	return int32(r.r.Bounded(uint32(n)))
}

func Float32() float32 {
	return float32(r.r.Random()) / (1<<32 - 1)
}

func Uint32() uint32 {
	return r.Uint32()
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
func SampleSlice[T any](slice []T) T {
	return slice[Intn(len(slice))]
}

func Sample[T any](t ...T) T {
	return t[Intn(len(t))]
}

// SampleFiltered uniformly randomly samples a slice, returning the index
// of the sampled item, using provided predicate function to filter the
// items that may be sampled.  An index of -1 is returned if the slice is
// empty or the predicate returns false for all items.
func SampleFiltered[T any](slice []T, pred func(T) bool) int {
	idx := -1
	candidates := 0
	for i, v := range slice {
		if pred(v) {
			candidates++
			p := float32(1) / float32(candidates)
			if Float32() < p {
				idx = i
			}
		}
	}
	return idx
}

// SampleWeighted randomly samples an element from the given slice with the
// probability of choosing each element proportional to the value returned
// by the provided callback.
func SampleWeighted[T any](slice []T, weight func(T) int) int {
	// Weighted reservoir sampling...
	idx := -1
	sumWt := 0
	for i, v := range slice {
		w := weight(v)
		if w == 0 {
			continue
		}

		sumWt += w
		p := float32(w) / float32(sumWt)
		if Float32() < p {
			idx = i
		}
	}
	return idx
}

var (
	//go:embed nouns.txt
	nounsFile string
	nounList  []string

	//go:embed adjectives.txt
	adjectivesFile string
	adjectiveList  []string
)

func AdjectiveNoun() string {
	if nounList == nil {
		nounList = strings.Split(nounsFile, "\n")
	}
	if adjectiveList == nil {
		adjectiveList = strings.Split(adjectivesFile, "\n")
	}

	return strings.TrimSpace(adjectiveList[Intn(len(adjectiveList))]) + "-" +
		strings.TrimSpace(nounList[Intn(len(nounList))])
}
