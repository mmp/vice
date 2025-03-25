// pkg/util/mem.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import "sync"

type ObjectArena[T any] struct {
	pool   []T
	offset int
	mu     sync.Mutex
}

func (a *ObjectArena[T]) AllocClear() *T {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.offset == len(a.pool) {
		var t T
		a.pool = append(a.pool, t)
	}

	p := &a.pool[a.offset]
	var t T
	*p = t

	a.offset++
	return p
}

func (a *ObjectArena[T]) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.offset = 0
}

func (a *ObjectArena[T]) Cap() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cap(a.pool)
}
