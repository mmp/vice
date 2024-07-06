package sim

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/rand"
	"github.com/mmp/vice/pkg/util"
)

var badCallsigns map[string]interface{} = map[string]interface{}{
	// 9/11
	"AAL11":  nil,
	"UAL175": nil,
	"AAL77":  nil,
	"UAL93":  nil,

	// Pilot suicide
	"MAS17":   nil,
	"MAS370":  nil,
	"GWI18G":  nil,
	"GWI9525": nil,
	"MSR990":  nil,

	// Hijackings
	"FDX705":  nil,
	"AFR8969": nil,

	// Selected major crashes (leaning toward callsigns vice uses or is
	// likely to use in the future, via
	// https://en.wikipedia.org/wiki/List_of_deadliest_aircraft_accidents_and_incidents
	"PAA1736": nil,
	"KLM4805": nil,
	"JAL123":  nil,
	"AIC182":  nil,
	"AAL191":  nil,
	"PAA103":  nil,
	"KAL007":  nil,
	"AAL587":  nil,
	"CAL140":  nil,
	"TWA800":  nil,
	"SWR111":  nil,
	"KAL801":  nil,
	"AFR447":  nil,
	"CAL611":  nil,
	"LOT5055": nil,
	"ICE001":  nil,
}

func (ss *State) sampleAircraft(icao, fleet string, lg *log.Logger) (*av.Aircraft, string) {
	al, ok := av.DB.Airlines[icao]
	if !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Chose airline %s, not found in database", icao)
		return nil, ""
	}

	if fleet == "" {
		fleet = "default"
	}

	fl, ok := al.Fleets[fleet]
	if !ok {
		// TODO: this also should be caught at validation time...
		lg.Errorf("Airline %s doesn't have a \"%s\" fleet!", icao, fleet)
		return nil, ""
	}

	// Sample according to fleet count
	var aircraft string
	acCount := 0
	for _, ac := range fl {
		// Reservoir sampling...
		acCount += ac.Count
		if rand.Float32() < float32(ac.Count)/float32(acCount) {
			aircraft = ac.ICAO
		}
	}

	perf, ok := av.DB.AircraftPerformance[aircraft]
	if !ok {
		// TODO: validation stage...
		lg.Errorf("Aircraft %s not found in performance database from fleet %+v, airline %s",
			aircraft, fleet, icao)
		return nil, ""
	}

	// random callsign
	callsign := strings.ToUpper(icao)
	for {
		format := "####"
		if len(al.Callsign.CallsignFormats) > 0 {
			format = rand.SampleSlice(al.Callsign.CallsignFormats)
		}

		id := ""
		for i, ch := range format {
			switch ch {
			case '#':
				if i == 0 {
					// Don't start with a 0.
					id += strconv.Itoa(1 + rand.Intn(9))
				} else {
					id += strconv.Itoa(rand.Intn(10))
				}
			case '@':
				id += string(rune('A' + rand.Intn(26)))
			}
		}
		if _, ok := ss.Aircraft[callsign+id]; ok {
			continue // it already exits
		} else if _, ok := badCallsigns[callsign+id]; ok {
			continue // nope
		} else {
			callsign += id
			break
		}
	}

	squawk := av.Squawk(rand.Intn(0o7000))

	acType := aircraft
	if perf.WeightClass == "H" {
		acType = "H/" + acType
	}
	if perf.WeightClass == "J" {
		acType = "J/" + acType
	}

	return &av.Aircraft{
		Callsign:       callsign,
		AssignedSquawk: squawk,
		Squawk:         squawk,
		Mode:           av.Charlie,
	}, acType
}

func (s *Sim) CreateArrival(arrivalGroup string, arrivalAirport string, goAround bool, lg *log.Logger) (*av.Aircraft, error) {
	arrivals := s.State.ArrivalGroups[arrivalGroup]
	// Randomly sample from the arrivals that have a route to this airport.
	idx := rand.SampleFiltered(arrivals, func(ar av.Arrival) bool {
		_, ok := ar.Airlines[arrivalAirport]
		return ok
	})

	if idx == -1 {
		return nil, fmt.Errorf("unable to find route in arrival group %s for airport %s?!",
			arrivalGroup, arrivalAirport)
	}
	arr := arrivals[idx]

	airline := rand.SampleSlice(arr.Airlines[arrivalAirport])
	ac, acType := s.State.sampleAircraft(airline.ICAO, airline.Fleet, lg)
	if ac == nil {
		return nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightPlan = av.NewFlightPlan(av.IFR, acType, airline.Airport, arrivalAirport)

	// Figure out which controller will (for starters) get the arrival
	// handoff. For single-user, it's easy.  Otherwise, figure out which
	// control position is initially responsible for the arrival. Note that
	// the actual handoff controller will be resolved later when the
	// handoff happens, so that it can reflect which controllers are
	// actually signed in at that point.
	arrivalController := s.State.PrimaryController
	if len(s.State.MultiControllers) > 0 {
		var err error
		arrivalController, err = s.State.MultiControllers.GetArrivalController(arrivalGroup)
		if err != nil {
			lg.Error("Unable to resolve arrival controller", slog.Any("error", err),
				slog.Any("aircraft", ac))
		}

		if arrivalController == "" {
			arrivalController = s.State.PrimaryController
		}
	}

	if err := ac.InitializeArrival(s.State.Airports[arrivalAirport], s.State.ArrivalGroups,
		arrivalGroup, idx, arrivalController, goAround, s.State.NmPerLongitude, s.State.MagneticVariation, lg); err != nil {
		return nil, err
	}

	return ac, nil
}

func (s *Sim) CreateDeparture(departureAirport, runway, category string, challenge float32,
	lastDeparture *av.Departure, lg *log.Logger) (*av.Aircraft, *av.Departure, error) {
	ap := s.State.Airports[departureAirport]
	if ap == nil {
		return nil, nil, av.ErrUnknownAirport
	}

	idx := slices.IndexFunc(s.State.DepartureRunways,
		func(r ScenarioGroupDepartureRunway) bool {
			return r.Airport == departureAirport && r.Runway == runway && r.Category == category
		})
	if idx == -1 {
		return nil, nil, av.ErrUnknownRunway
	}
	rwy := &s.State.DepartureRunways[idx]

	var dep *av.Departure
	if s.sameDepartureCap == 0 {
		s.sameDepartureCap = rand.Intn(3) + 1 // Set the initial max same departure cap (1-3)
	}
	if rand.Float32() < challenge && lastDeparture != nil && s.sameGateDepartures < s.sameDepartureCap {
		// 50/50 split between the exact same departure and a departure to
		// the same gate as the last departure.
		pred := util.Select(rand.Float32() < .5,
			func(d av.Departure) bool { return d.Exit == lastDeparture.Exit },
			func(d av.Departure) bool {
				_, ok := rwy.ExitRoutes[d.Exit] // make sure the runway handles the exit
				return ok && ap.ExitCategories[d.Exit] == ap.ExitCategories[lastDeparture.Exit]
			})

		if idx := rand.SampleFiltered(ap.Departures, pred); idx == -1 {
			// This should never happen...
			lg.Errorf("%s/%s/%s: unable to sample departure", departureAirport, runway, category)
		} else {
			dep = &ap.Departures[idx]
		}

	}

	if dep == nil {
		// Sample uniformly, minding the category, if specified
		idx := rand.SampleFiltered(ap.Departures,
			func(d av.Departure) bool {
				_, ok := rwy.ExitRoutes[d.Exit] // make sure the runway handles the exit
				return ok && (rwy.Category == "" || rwy.Category == ap.ExitCategories[d.Exit])
			})

		if idx == -1 {
			// This shouldn't ever happen...
			return nil, nil, fmt.Errorf("%s/%s: unable to find a valid departure",
				departureAirport, rwy.Runway)
		}
		dep = &ap.Departures[idx]
	}

	if lastDeparture != nil && (dep.Exit == lastDeparture.Exit && s.sameGateDepartures >= s.sameDepartureCap) {
		return nil, nil, fmt.Errorf("couldn't make a departure")
	}

	// Same gate buffer is a random int between 3-4 that gives a period after a few same gate departures.
	// For example, WHITE, WHITE, WHITE, DIXIE, NEWEL, GAYEL, MERIT, DIXIE, DIXIE
	// Another same-gate departure will not be happen untill after MERIT (in this example) because of the buffer.
	sameGateBuffer := rand.Intn(2) + 3

	if s.sameGateDepartures >= s.sameDepartureCap+sameGateBuffer || (lastDeparture != nil && dep.Exit != lastDeparture.Exit) { // reset back to zero if its at 7 or if there is a new gate
		s.sameDepartureCap = rand.Intn(3) + 1
		s.sameGateDepartures = 0
	}

	airline := rand.SampleSlice(dep.Airlines)
	ac, acType := s.State.sampleAircraft(airline.ICAO, airline.Fleet, lg)
	if ac == nil {
		return nil, nil, fmt.Errorf("unable to sample a valid aircraft")
	}

	ac.FlightPlan = av.NewFlightPlan(av.IFR, acType, departureAirport, dep.Destination)
	exitRoute := rwy.ExitRoutes[dep.Exit]
	if err := ac.InitializeDeparture(ap, departureAirport, dep, runway, exitRoute,
		s.State.NmPerLongitude, s.State.MagneticVariation, s.State.Scratchpads,
		s.State.PrimaryController, s.State.MultiControllers, lg); err != nil {
		return nil, nil, err
	}

	/* Keep adding to World sameGateDepartures number until the departure cap + the buffer so that no more
	same-gate departures are launched, then reset it to zero. Once the buffer is reached, it will reset World sameGateDepartures to zero*/
	s.sameGateDepartures += 1

	return ac, dep, nil
}
