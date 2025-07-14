// pkg/util/prof.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"time"

	"github.com/mmp/vice/pkg/log"
	"github.com/shirou/gopsutil/cpu"
)

type Profiler struct {
	cpu, mem *os.File
}

func CreateProfiler(cpu, mem string) (Profiler, error) {
	p := Profiler{}

	// If the path is non-absolute, convert it to an absolute path
	// w.r.t. the current directory.  (This is to work around that vice
	// changes the working directory to above where the resources are,
	// which in turn was causing profiling data to be written in an
	// unexpected place...)
	absPath := func(p string) string {
		if p != "" && !filepath.IsAbs(p) {
			if cwd, err := os.Getwd(); err == nil {
				return filepath.Join(cwd, p)
			}
		}
		return p
	}
	cpu = absPath(cpu)
	mem = absPath(mem)

	var err error
	if cpu != "" {
		if p.cpu, err = os.Create(cpu); err != nil {
			return Profiler{}, fmt.Errorf("%s: unable to create CPU profile file: %v", cpu, err)
		} else if err = pprof.StartCPUProfile(p.cpu); err != nil {
			p.cpu.Close()
			return Profiler{}, fmt.Errorf("unable to start CPU profile: %v", err)
		}
	}

	if mem != "" {
		if p.mem, err = os.Create(mem); err != nil {
			return Profiler{}, fmt.Errorf("%s: unable to create memory profile file: %v", mem, err)
		}
	}

	if p.cpu != nil || p.mem != nil {
		// Catch ctrl-c and to write out the profile before exiting
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)

		go func() {
			<-sig
			if p.cpu != nil {
				pprof.StopCPUProfile()
				p.cpu.Close()
			}
			if p.mem != nil {
				if err := pprof.WriteHeapProfile(p.mem); err != nil {
					fmt.Fprintf(os.Stderr, "%s: unable to write memory profile file: %v", mem, err)
				}
				p.mem.Close()
			}
			os.Exit(0)
		}()
	}

	return p, nil
}

func (p *Profiler) Cleanup() {
	if p.cpu != nil {
		pprof.StopCPUProfile()
		p.cpu.Close()
		p.cpu = nil
	}
	if p.mem != nil {
		if err := pprof.WriteHeapProfile(p.mem); err != nil {
			fmt.Fprintf(os.Stderr, "unable to write memory profile file: %v", err)
		}
		p.mem.Close()
	}
}

func MonitorCPUUsage(limit int, panicIfWedged bool, lg *log.Logger) {
	const nhist = 10
	var history []float64
	go func() {
		t := time.Tick(1 * time.Second)
		for {
			<-t

			if usage, err := cpu.Percent(0, false); err != nil {
				lg.Errorf("cpu.Percent: %v", err)
			} else {
				history = append(history, usage[0])
				if n := len(history); n > nhist {
					history = history[1:]

					if slices.Min(history) > float64(limit) {
						lg.Warnf("Last %d ticks over %d utilization: %#v. Dumping", nhist, limit, history)

						writeProfile("mutex", lg)

						filename := fmt.Sprintf("dump%d.txt", time.Now().Unix())
						if f, err := os.Create(filename); err != nil {
							lg.Errorf("failed to create dump file: %v", err)
						} else {
							fmt.Fprint(f, DumpHeldMutexes(lg))
							fmt.Fprint(f, "\n")

							pprof.Lookup("goroutine").WriteTo(f, 2)

							f.Close()
						}

						if panicIfWedged {
							panic("bye")
						}
					} else if slices.Min(history[:n-3]) > float64(limit) {
						lg.Warnf("Last 3 ticks over %d utilization: %#v\n", limit, history)
					}
				}
			}
		}
	}()
}

// MonitorMemoryUsage launches a goroutine that periodically checks how much memory is in use; if it's above
// the threshold, it writes out a memory profile file and then bumps the threshold by the given increment.
func MonitorMemoryUsage(triggerMB int, incMB int, lg *log.Logger) {
	go func() {
		threshold := uint64(triggerMB) * 1024 * 1024
		delta := uint64(incMB) * 1024 * 1024

		t := time.Tick(5 * time.Second)
		for {
			<-t

			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)

			heapAlloc := memStats.HeapAlloc
			if heapAlloc > threshold {
				threshold += delta
				lg.Warn("Writing heap profile: heapAlloc=%d MB", heapAlloc/(1024*1024))
				writeProfile("heap", lg)
			}
		}
	}()
}

func writeProfile(ptype string, lg *log.Logger) {
	filename := fmt.Sprintf("%s_%d.pprof", ptype, time.Now().Unix())
	if f, err := os.Create(filename); err != nil {
		lg.Errorf("failed to create %q profile file: %v", ptype, err)
	} else {
		if err := pprof.Lookup(ptype).WriteTo(f, 0); err != nil {
			lg.Errorf("failed to write %q profile: %v", ptype, err)
		} else {
			lg.Warnf("%q profile written to %s", ptype, filename)
		}
		f.Close()
	}
}
