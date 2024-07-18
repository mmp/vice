// stats.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"log/slog"
	"runtime"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/renderer"
)

// Stats collects a few statistics related to rendering and time spent in
// various phases of the system.
type Stats struct {
	drawPanes renderer.RendererStats
	drawUI    renderer.RendererStats
	startTime time.Time
	redraws   int
}

var startupMallocs uint64

func (stats Stats) LogValue(lg *log.Logger) slog.Value {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	if startupMallocs == 0 { // first call
		startupMallocs = mem.Mallocs
	}

	elapsed := time.Since(lg.Start).Seconds()
	mallocsPerSecond := float64(mem.Mallocs-startupMallocs) / elapsed

	return slog.GroupValue(
		slog.Float64("redraws_per_second", float64(stats.redraws)/time.Since(stats.startTime).Seconds()),
		slog.Float64("mallocs_per_second", mallocsPerSecond),
		slog.Int64("active_mallocs", int64(mem.Mallocs-mem.Frees)),
		slog.Int64("memory_in_use", int64(mem.HeapAlloc)),
		slog.Any("draw_panes", stats.drawPanes),
		slog.Any("draw_ui", stats.drawUI))
}
