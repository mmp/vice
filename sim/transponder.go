// sim/transponder.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

// initTransponderFault rolls against FaultyTransponderChance at spawn; on a
// hit, assigns a persistent Mode C offset (signed, magnitude 1-1000 ft) that
// stays fixed for the aircraft's life. The offset is applied wherever the
// scope reads Mode C altitude.
func (s *Sim) initTransponderFault(ac *Aircraft) {
	if s.FaultyTransponderChance <= 0 {
		return
	}
	if s.Rand.Float32()*100 >= s.FaultyTransponderChance {
		return
	}
	magnitude := 1 + s.Rand.Float32()*999
	if s.Rand.Float32() < 0.5 {
		magnitude = -magnitude
	}
	ac.TransponderAltOffset = magnitude
}
