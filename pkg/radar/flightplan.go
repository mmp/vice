package radar

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mmp/vice/pkg/panes"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"
)

const ( // interim alt types
	Normal = iota
	Procedure
	Local
)

func FormatAltitude[T ~int | ~float32](alt T) string { // should this go in pkg/util/generic.go?
	return fmt.Sprintf("%03v", int(alt+50)/100)
}

// Returns all aircraft that match the given suffix. If instructor is true,
// returns all matching aircraft; otherwise only ones under the current
// controller's control are considered for matching.
func TracksFromACIDSuffix(ctx *panes.Context, suffix string) []*sim.Track {
	match := func(trk *sim.Track) bool {
		if trk.IsUnassociated() {
			return strings.HasSuffix(string(trk.ADSBCallsign), suffix)
		} else {
			fp := trk.FlightPlan
			if !strings.HasSuffix(string(fp.ACID), suffix) {
				return false
			}

			if fp.ControllingController == ctx.UserTCP || ctx.Client.State.AreInstructorOrRPO(ctx.UserTCP) {
				return true
			}

			// Hold for release aircraft still in the list
			if ctx.Client.State.ResolveController(trk.FlightPlan.TrackingController) == ctx.UserTCP &&
				trk.FlightPlan.ControllingController == "" {
				return true
			}
			return false
		}
	}
	return slices.Collect(util.FilterSeq(maps.Values(ctx.Client.State.Tracks), match))
}