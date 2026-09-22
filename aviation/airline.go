// aviation/airline.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package aviation

import (
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/math"
	"github.com/mmp/vice/rand"
	"github.com/mmp/vice/util"
)

type AirlineSpecifier struct {
	ICAO          string   `json:"icao"`
	Callsign      string   `json:"callsign,omitempty"`
	Fleet         string   `json:"fleet,omitempty"`
	AircraftTypes []string `json:"types,omitempty"`
}

type ArrivalAirline struct {
	AirlineSpecifier
	Airport ICAOAirportCode `json:"airport"`
}

type TypeOfFlight int

const (
	FlightTypeUnknown TypeOfFlight = iota
	FlightTypeDeparture
	FlightTypeArrival
	FlightTypeOverflight
)

func (a AirlineSpecifier) Aircraft(db Database) []FleetAircraft {
	if a.Fleet == "" && len(a.AircraftTypes) == 0 {
		return airlineFleets(db, a.ICAO)["default"]
	} else if a.Fleet != "" {
		return airlineFleets(db, a.ICAO)[a.Fleet]
	} else {
		var f []FleetAircraft
		for _, ty := range a.AircraftTypes {
			f = append(f, FleetAircraft{ICAO: ty, Count: 1})
		}
		return f
	}
}

func CallsignClashesWithExisting(currentCallsigns []ADSBCallsign, proposed string, uniqueSuffix bool) bool {
	if uniqueSuffix {
		// Reject if the last 2 characters of callsign match an existing callsign.
		suffixMatches := func(cs ADSBCallsign) bool {
			return len(proposed) >= 2 && strings.HasSuffix(string(cs), proposed[len(proposed)-2:])
		}
		return slices.ContainsFunc(currentCallsigns, suffixMatches)
	}
	// Reject only if there's an exact match
	return slices.Contains(currentCallsigns, ADSBCallsign(proposed))
}

func (a *AirlineSpecifier) Check(db Database, e *util.ErrorLogger) {
	defer e.CheckDepth(e.CurrentDepth())

	e.Push("Airline " + a.ICAO)
	defer e.Pop()

	a.ICAO = strings.ToUpper(strings.TrimSpace(a.ICAO))
	a.Callsign = strings.ToUpper(strings.TrimSpace(a.Callsign))

	if a.Callsign != "" {
		for _, ch := range a.Callsign {
			if (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
				e.ErrorString("callsign has invalid character %q", ch)
				break
			}
		}
	}

	if a.Callsign != "" && a.ICAO != "" {
		e.ErrorString("cannot specify both \"callsign\" and \"icao\"")
		return
	}
	if a.ICAO == "" && a.Callsign != "" {
		if icao := icaoFromCallsign(a.Callsign); icao != "" {
			a.ICAO = icao
		}
	}

	if a.Callsign != "" {
		if a.Fleet != "" {
			e.ErrorString("cannot specify \"fleet\" with a specific callsign")
			return
		}
		if len(a.AircraftTypes) == 0 {
			e.ErrorString("must specify \"types\" when a specific callsign is given")
			return
		}
	}

	var al Airline
	if a.ICAO != "" {
		var ok bool
		al, ok = db.Airline(a.ICAO)
		if !ok {
			e.ErrorString("airline not known")
			return
		}
	}

	if a.Fleet == "" && len(a.AircraftTypes) == 0 {
		if a.ICAO == "" {
			e.ErrorString("must specify \"types\" when no \"icao\" is provided")
			return
		}
		a.Fleet = "default"
	}
	if a.Fleet != "" {
		if a.ICAO == "" {
			e.ErrorString("must specify \"icao\" when \"fleet\" is set")
			return
		}
		if len(a.AircraftTypes) != 0 {
			e.ErrorString(`cannot specify both "fleet" and "types"`)
			return
		}
		if _, ok := al.Fleets[a.Fleet]; !ok {
			e.ErrorString(`"fleet" %s unknown`, a.Fleet)
			return
		}
	}

	// This flags errors in many scenarios; disabled for now pending a future fixup.
	// https://github.com/mmp/vice/issues/827
	/*
		for _, ty := range a.AircraftTypes {
			var valid []string
			for _, fac := range al.Fleets {
				for _, ac := range fac {
					valid = append(valid, ac.ICAO)
				}
			}
			if !slices.Contains(valid, ty) {
				slices.Sort(valid)
				valid = slices.Compact(valid)
				e.ErrorString("aircraft type %q is not in any of the airline's fleets. Options: %s", ty, strings.Join(valid, " "))
			}
		}
	*/

	for _, ac := range a.Aircraft(db) {
		e.Push("Aircraft " + ac.ICAO)
		if perf, ok := db.AircraftPerformance(ac.ICAO); !ok {
			e.ErrorString("aircraft not present in performance database")
		} else {
			if perf.Speed.Min < 35 || perf.Speed.Landing < 35 || perf.Speed.CruiseTAS < 35 ||
				perf.Speed.MaxTAS < 35 || perf.Speed.Min > perf.Speed.MaxTAS {
				e.ErrorString("aircraft's speed specification is questionable: %+v", perf.Speed)
			}
			if perf.Rate.Climb == 0 || perf.Rate.Descent == 0 || perf.Rate.Accelerate == 0 ||
				perf.Rate.Decelerate == 0 {
				e.ErrorString("aircraft's rate specification is questionable: %+v", perf.Rate)
			}
		}
		e.Pop()
	}
}

func (a AirlineSpecifier) sampleAcType(db Database, r *rand.Rand, departureAirport, arrivalAirport ICAOAirportCode, lg *log.Logger) string {
	if a.ICAO == "" {
		if len(a.AircraftTypes) == 0 {
			lg.Errorf("No aircraft types available for callsign %q", a.Callsign)
			return ""
		}
		actype := rand.SampleSlice(r, a.AircraftTypes)
		if _, ok := db.AircraftPerformance(actype); !ok {
			lg.Errorf("Aircraft %q not found in performance database for callsign %q", actype, a.Callsign)
			return ""
		}
		return actype
	}
	if _, ok := db.Airline(strings.ToUpper(a.ICAO)); !ok {
		// TODO: this should be caught at load validation time...
		lg.Errorf("Airline %q not found in database", a.ICAO)
		return ""
	}

	// Calculate flight distance to filter aircraft by CWT category
	depLoc, _ := db.AirportLocation(departureAirport)
	arrLoc, _ := db.AirportLocation(arrivalAirport)
	flightDistance := math.NMDistance2LL(depLoc, arrLoc)

	// Sample according to fleet count, filtering by maximum distance for CWT category
	var actype string

	// First attempt: filter aircraft by distance and sample weighted by fleet count
	filteredAircraft := make([]FleetAircraft, 0)
	for _, ac := range a.Aircraft(db) {
		// Filter based on flight distance and aircraft CWT category
		if flightDistance > 0 && !slices.Contains(extraLongRange, ac.ICAO) {
			if perf, ok := db.AircraftPerformance(ac.ICAO); ok {
				if maxRange, ok := cwtMaxRanges[perf.Category.CWT]; ok {
					// Check if flight distance exceeds category maximum (0 means no limit)
					if maxRange > 0 && flightDistance > maxRange {
						continue
					}
				}
			}
		}
		filteredAircraft = append(filteredAircraft, ac)
	}

	if len(filteredAircraft) > 0 {
		sampled, ok := rand.SampleWeighted(r, filteredAircraft, func(ac FleetAircraft) float32 {
			return float32(ac.Count)
		})

		if ok {
			actype = sampled.ICAO
		}
	}

	if actype == "" {
		// Try again without considering range.
		sampled, ok := rand.SampleWeighted(r, a.Aircraft(db), func(ac FleetAircraft) float32 {
			return float32(ac.Count)
		})

		if ok {
			actype = sampled.ICAO
		}
	}
	if actype != "" {
		if _, ok := db.AircraftPerformance(actype); !ok {
			// TODO: validation stage...
			lg.Errorf("Aircraft %q not found in performance database for airline %+v",
				actype, a)
			return ""
		}
	}
	return actype
}

func (a AirlineSpecifier) SampleAcType(db Database, r *rand.Rand, departureAirport, arrivalAirport ICAOAirportCode, lg *log.Logger) string {
	return a.sampleAcType(db, r, departureAirport, arrivalAirport, lg)
}

var badCallsigns map[string]any = map[string]any{
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
	"PSA5342": nil,
}

// cwtMaxRanges defines the maximum flight distances (in nautical miles)
// for each CWT category (semi ad-hoc, but seems to work reasonably); 0 means no limit.
var cwtMaxRanges = map[string]float32{
	"A": 0,    // A380
	"B": 0,    // Large widebody (B777, B787, A330, B747)
	"C": 6500, // Medium widebody (B767, A300, DC-10)
	"D": 5500, // Large widebody variants (A350-1000, B747SP, L-1011)
	"E": 4500, // B757
	"F": 3800, // Narrowbody jets (A320, B737)
	"G": 2000, // Regional jets/turboprops (CRJ, ATR, Dash 8)
	"H": 2000, // Light jets (Citation, Learjet)
	"I": 1200, // Small aircraft (King Air, Baron)
}

// Though category C, these can go quite far, so don't prohibit them.
var extraLongRange = []string{"A35K", "A359"}

// currentCallsigns will be empty if we don't care about unique suffixes.
func (a AirlineSpecifier) SampleAcTypeAndCallsign(db Database, r *rand.Rand, currentCallsigns []ADSBCallsign, uniqueSuffix bool, departureAirport, arrivalAirport ICAOAirportCode, lg *log.Logger) (actype, callsign string) {
	actype = a.sampleAcType(db, r, departureAirport, arrivalAirport, lg)
	if actype == "" {
		return "", ""
	}

	if a.Callsign != "" {
		callsign = strings.ToUpper(strings.TrimSpace(a.Callsign))
		if callsign == "" {
			return "", ""
		}
		if _, ok := badCallsigns[callsign]; ok {
			return "", ""
		}
		if CallsignClashesWithExisting(currentCallsigns, callsign, uniqueSuffix) {
			return "", ""
		}
		return actype, callsign
	}

	dbAirline, ok := db.Airline(strings.ToUpper(a.ICAO))
	if !ok {
		return "", ""
	}

	// random callsign
	var cs strings.Builder
	for range 100 {
		format := "####"
		if len(dbAirline.Callsign.CallsignFormats) > 0 {
			f, ok := rand.SampleWeighted(r, dbAirline.Callsign.CallsignFormats,
				func(f string) int {
					if _, wt, ok := strings.Cut(f, "x"); ok { // we have a weight
						if v, err := strconv.Atoi(wt); err == nil {
							return v
						}
					}
					return 1
				})
			if ok {
				format = f
			}
		}

		cs.WriteString(strings.ToUpper(dbAirline.ICAO))
	loop:
		for i, ch := range format {
			switch ch {
			case '#':
				if i == 0 {
					cs.WriteByte(byte('1' + r.Intn(9))) // Don't start with a 0.
				} else {
					cs.WriteByte(byte('0' + r.Intn(10)))
				}
			case '@':
				// Exclude I and O which can be confused with 1 and 0
				const letters = "ABCDEFGHJKLMNPQRSTUVWXYZ"
				cs.WriteByte(letters[r.Intn(len(letters))])
			case 'x':
				break loop
			}
		}
		if _, ok := badCallsigns[cs.String()]; ok {
			cs.Reset()
			continue // nope
		} else if slices.Contains(currentCallsigns, ADSBCallsign(cs.String())) {
			cs.Reset()
			continue
		} else if CallsignClashesWithExisting(currentCallsigns, cs.String(), uniqueSuffix) {
			cs.Reset()
			continue
		}
		return actype, cs.String()
	}

	return "", ""
}

func icaoFromCallsign(callsign string) string {
	if len(callsign) < 3 {
		return ""
	}
	for i := range 3 {
		ch := callsign[i]
		if ch < 'A' || ch > 'Z' {
			return ""
		}
	}
	return callsign[:3]
}

// airlineFleets gives the fleets the named airline publishes.
func airlineFleets(db Database, icao string) map[string][]FleetAircraft {
	al, ok := db.Airline(strings.ToUpper(icao))
	if !ok {
		return nil
	}
	return al.Fleets
}
