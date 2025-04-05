// pkg/util/sync.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"encoding/json"
	"log/slog"
	gomath "math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmp/vice/pkg/log"

	"github.com/shirou/gopsutil/cpu"
)

///////////////////////////////////////////////////////////////////////////
// AtomicBool

// AtomicBool is a simple wrapper around atomic.Bool that adds support for
// JSON marshaling/unmarshaling.
type AtomicBool struct {
	atomic.Bool
}

func (a AtomicBool) MarshalJSON() ([]byte, error) {
	b := a.Load()
	return json.Marshal(b)
}

func (a *AtomicBool) UnmarshalJSON(data []byte) error {
	var b bool
	err := json.Unmarshal(data, &b)
	if err == nil {
		a.Store(b)
	}
	return err
}

///////////////////////////////////////////////////////////////////////////
// LoggingMutex

var heldMutexesMutex sync.Mutex
var heldMutexes map[*LoggingMutex]interface{} = make(map[*LoggingMutex]interface{})

type LoggingMutex struct {
	sync.Mutex
	acq      time.Time
	acqStack []log.StackFrame
}

func (l *LoggingMutex) Lock(lg *log.Logger) {
	tryTime := time.Now()
	lg.Debug("attempting to acquire mutex", slog.Any("mutex", l))

	if !l.Mutex.TryLock() {
		// Lock with timeout.
		locked := make(chan struct{}, 1)

		go func() {
			l.Mutex.Lock()
			locked <- struct{}{}
		}()

		select {
		case <-locked:

		case <-time.After(10 * time.Second):
			lg.Error("unable to acquire mutex after 10 seconds", slog.Any("mutex", l),
				slog.Any("held_mutexes", heldMutexes))

			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			usage, _ := cpu.Percent(time.Second, false)

			lg.Errorf("CPU: %d%% alloc: %dMB total alloc: %dMB sys mem: %dMB goroutines: %d",
				int(gomath.Round(usage[0])), m.Alloc/(1024*1024), m.TotalAlloc/(1024*1024), m.Sys/(1024*1024),
				runtime.NumGoroutine())
		}
	}

	heldMutexesMutex.Lock()
	heldMutexes[l] = nil
	heldMutexesMutex.Unlock()

	l.acq = time.Now()
	l.acqStack = log.Callstack(l.acqStack)
	w := l.acq.Sub(tryTime)
	lg.Debug("acquired mutex", slog.Any("mutex", l), slog.Duration("wait", w))
	if w > time.Second {
		lg.Warn("long wait to acquire mutex", slog.Any("mutex", l), slog.Duration("wait", w))
	}
}

func (l *LoggingMutex) Unlock(lg *log.Logger) {
	heldMutexesMutex.Lock()
	// Though it may seem like we could unlock this sooner, holding it
	// until this function returns ensures that if we end up doing logging
	// in the code below, other mutexes aren't unlocked while we're trying
	// to log the held ones.
	defer heldMutexesMutex.Unlock()

	if _, ok := heldMutexes[l]; !ok {
		lg.Error("mutex not held", slog.Any("held_mutexes", heldMutexes))
	}
	delete(heldMutexes, l)

	if d := time.Since(l.acq); d > time.Second {
		lg.Warn("mutex held for over 1 second", slog.Any("mutex", l), slog.Duration("held", d),
			slog.Any("held_mutexes", heldMutexes))
	}

	l.acq = time.Time{}
	l.acqStack = nil
	l.Mutex.Unlock()

	lg.Debug("released mutex", slog.Any("mutex", l))
}

func (l *LoggingMutex) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Time("acq", l.acq),
		slog.Duration("held", time.Since(l.acq)),
		slog.Any("acq_stack", l.acqStack))
}
