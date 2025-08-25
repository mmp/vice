// pkg/util/chan.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/mmp/vice/log"
)

type TempFileRegistry struct {
	mu    sync.Mutex
	paths map[string]struct{} // group -> path
}

func MakeTempFileRegistry(lg *log.Logger) *TempFileRegistry {
	r := &TempFileRegistry{
		paths: make(map[string]struct{}),
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		r.RemoveAll()
		os.Exit(0)
	}()

	return r
}

func (t *TempFileRegistry) RegisterPath(path string) {
	t.mu.Lock()
	t.paths[path] = struct{}{}
	t.mu.Unlock()
}

func (t *TempFileRegistry) RemoveAllPrefix(prefix string) {
	t.mu.Lock()
	for path := range t.paths {
		if strings.HasPrefix(path, prefix) {
			os.Remove(path) // ignore errors
			delete(t.paths, path)
		}
	}
	t.mu.Unlock()
}

func (t *TempFileRegistry) RemoveAll() {
	t.mu.Lock()
	for path := range t.paths {
		os.Remove(path) // ignore errors
	}
	clear(t.paths)
	t.mu.Unlock()
}
