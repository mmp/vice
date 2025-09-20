// wx/provider.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"time"

	"github.com/mmp/vice/util"
)

type Provider interface {
	GetAvailableTimeIntervals() []util.TimeInterval

	// Best effort, may not have it for all airports, but no error is returned for that.
	GetMETAR(airports []string) (map[string]METARSOA, error)

	// Returns the item at-or-before the given time
	GetPrecipURL(tracon string, t time.Time) (string, time.Time, error)
	// Returns atmos, it's time, the time for the next one in the series.
	GetAtmos(tracon string, t time.Time) (*AtmosSOA, time.Time, time.Time, error)
}
