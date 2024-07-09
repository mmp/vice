// pkg/util/prof.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"fmt"
	"os"
	"os/signal"
	"runtime/pprof"
)

type Profiler struct {
	cpu, mem *os.File
}

func CreateProfiler(cpu, mem string) (Profiler, error) {
	p := Profiler{}

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
