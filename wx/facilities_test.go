// wx/facilities_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"testing"

	"github.com/mmp/vice/aviation/db"
)

// TestFacilityRegionMatchesLocation catches facilities added to the database
// without being added to FacilityRegion, which would leave them with the
// CONUS radar images and grids, neither of which covers them.
func TestFacilityRegionMatchesLocation(t *testing.T) {
	db.InitDB()

	check := func(id string, fac db.Facility) {
		want := "conus"
		if c := fac.Center(); c.Latitude() > 50 {
			want = "alaska"
		} else if c.Longitude() < -150 {
			want = "hawaii"
		}
		if got := FacilityRegion(id); got != want {
			t.Errorf("%s at %v: got region %q, want %q", id, fac.Center(), got, want)
		}
	}
	for id, tracon := range db.DB.TRACONs {
		check(id, tracon.Facility)
	}
	for id, artcc := range db.DB.ARTCCs {
		check(id, artcc)
	}
}
