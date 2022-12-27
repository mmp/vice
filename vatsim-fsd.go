// vatsim-fsd.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"strconv"
	"strings"
)

// The details of the initial implementation and its decoding of the VATSIM
// protocol are heavily due to the wonderful vatsimfsdparser package
// (https://github.com/Sequal32/vatsimfsdparser). (Too bad this wasn't
// written in rust!).  Also useful were the FSD Unofficial docs:
// https://fsd-doc.norrisng.ca/site/.

// Add ATC
func handleAA(v *VATSIMServer, sender string, args []string) error {
	if rating, err := parseRating(args[5]); err != nil {
		return err
	} else {
		ctrl := &Controller{
			Callsign: sender,
			Name:     args[2],
			CID:      args[3],
			Rating:   rating,
		}
		eventStream.Post(&AddedControllerEvent{Controller: ctrl})
		v.controllers[sender] = ctrl
		return nil
	}
}

// Add Pilot
// #AP(callsign):SERVER:(cid):(password):(rating):(protocol version):(simtype):(full name ICAO)
func handleAP(v *VATSIMServer, callsign string, args []string) error {
	if rating, err := parseRating(args[4]); err != nil {
		return err
	} else {
		// We're ignoring things like protocol version (args[4]) and simulator
		// type (args[5]) here...
		pilot := &Pilot{
			Callsign: callsign,
			CID:      args[2],
			Name:     args[7],
			Rating:   rating}
		eventStream.Post(&AddedPilotEvent{Pilot: pilot})
		v.pilots[callsign] = pilot
		return nil
	}
}

// $ARserver:ABE_APP:METAR:KABE 232251Z 24006KT ...
func handleAR(v *VATSIMServer, sender string, args []string) error {
	if metar, err := ParseMETAR(args[3]); err == nil {
		v.metar[metar.AirportICAO] = *metar
		eventStream.Post(&ReceivedMETAREvent{METAR: *metar})
	} else {
		lg.Errorf("%s: %v", args[3], err)
	}
	return nil
}

// Flightplan
func handleFP(v *VATSIMServer, sender string, args []string) error {
	var fp FlightPlan

	switch args[2] {
	case "I":
		fp.Rules = IFR
	case "V":
		fp.Rules = VFR
	case "D":
		fp.Rules = DVFR
	case "S":
		fp.Rules = SVFR
	default:
		return MalformedMessageError{"Unexpected flight rules: " + args[2]}
	}

	fp.AircraftType = args[3]

	var err error
	if fp.CruiseSpeed, err = strconv.Atoi(args[4]); err != nil {
		return MalformedMessageError{"Unable to parse cruise airspeed: " + args[4]}
	}

	fp.DepartureAirport = args[5]

	if fp.DepartTimeEst, err = strconv.Atoi(args[6]); err != nil {
		return MalformedMessageError{"Unable to parse departTime: " + args[6]}
	}
	if fp.DepartTimeActual, err = strconv.Atoi(args[7]); err != nil {
		return MalformedMessageError{"Unable to parse departTime: " + args[7]}
	}

	if fp.Altitude, err = ParseAltitude(args[8]); err != nil && args[8] != "" {
		return MalformedMessageError{err.Error()}
	}

	fp.ArrivalAirport = args[9]

	if fp.Hours, err = strconv.Atoi(args[10]); err != nil {
		return MalformedMessageError{"Unable to parse enroute hours: " + args[10]}
	}
	if fp.Minutes, err = strconv.Atoi(args[11]); err != nil {
		return MalformedMessageError{"Unable to parse enroute minutes: " + args[11]}
	}
	if fp.FuelHours, err = strconv.Atoi(args[12]); err != nil {
		return MalformedMessageError{"Unable to parse fuel hours: " + args[12]}
	}
	if fp.FuelMinutes, err = strconv.Atoi(args[13]); err != nil {
		return MalformedMessageError{"Unable to parse fuel minutes: " + args[13]}
	}

	fp.AlternateAirport = args[14]
	fp.Remarks = args[15]
	fp.Route = args[16]

	ac := v.getOrCreateAircraft(sender)
	ac.FlightPlan = &fp

	if strings.Contains(fp.Remarks, "/v/") || strings.Contains(fp.Remarks, "/V/") {
		ac.VoiceCapability = VoiceFull
	} else if strings.Contains(fp.Remarks, "/r/") || strings.Contains(fp.Remarks, "/R/") {
		ac.VoiceCapability = VoiceReceive
	} else if strings.Contains(fp.Remarks, "/t/") || strings.Contains(fp.Remarks, "/T/") {
		ac.VoiceCapability = VoiceText
	}

	return nil
}

// Accepted handoff: TWR accepted N11TV from APP
// $HAABE_TWR:ABE_APP:N11TV
func handleHA(v *VATSIMServer, sender string, args []string) error {
	from, callsign := args[1], args[2]
	ac := v.getOrCreateAircraft(callsign)

	if ac.TrackingController != from {
		lg.Printf("%s: %s is tracking but %s accepted h/o from %s?", callsign, ac.TrackingController,
			sender, from)
	}

	if from == v.callsign {
		// from us!
		ac.OutboundHandoffController = ""
		eventStream.Post(&AcceptedHandoffEvent{controller: sender, ac: ac})
	}

	ac.TrackingController = sender

	return nil
}

// Handoff request: DEP wants to hand FDX off to APP
// $HOABE_DEP:ABE_APP:FDX901
func handleHO(v *VATSIMServer, sender string, args []string) error {
	receiver, callsign := args[1], args[2]
	if receiver == v.callsign {
		ac := v.getOrCreateAircraft(callsign)
		ac.InboundHandoffController = sender
		eventStream.Post(&OfferedHandoffEvent{controller: sender, ac: ac})
	}

	return nil
}

// Text message
func handleTM(v *VATSIMServer, sender string, args []string) error {
	// #TM(from):(frequency):(message)
	// frequency:
	// *           broadcast
	// *S          wallop
	// @49999      ATC
	// @[freq]     frequency
	// (otherwise) private message
	freq := args[1]
	tm := TextMessage{sender: sender, contents: strings.Join(args[2:], ":")}
	if freq == "*" {
		tm.messageType = TextBroadcast
	} else if freq == "*S" {
		tm.messageType = TextWallop
	} else if freq == "@49999" {
		tm.messageType = TextATC
	} else if freq[0] == '@' {
		tm.messageType = TextFrequency
		for _, f := range strings.Split(freq, "&") {
			if tf, err := parseFrequency(f[1:]); err != nil {
				return err
			} else {
				tm.frequencies = append(tm.frequencies, tf)
			}
		}
	} else {
		tm.messageType = TextPrivate
	}

	eventStream.Post(&TextMessageEvent{message: &tm})

	return nil
}

// Aircraft update
// @(mode):(callsign):(squawk):(rating):(lat):(lon):(alt):(groundspeed):(num1):(num2)
func handleAt(v *VATSIMServer, trmode string, args []string) error {
	callsign := args[1]

	var mode TransponderMode
	switch trmode {
	case "S":
		mode = Standby
	case "N":
		mode = Charlie
	case "Y":
		mode = Ident
	default:
		return MalformedMessageError{"Unexpected squawk type: " + args[0]}
	}

	squawk, err := ParseSquawk(args[2])
	if err != nil {
		return err
	}

	var altitude, groundspeed int
	var surfaces uint64

	latlong, err := parseLatitudeLongitude(args[4], args[5])
	if err != nil {
		return err
	}

	if altitude, err = strconv.Atoi(args[6]); err != nil {
		return MalformedMessageError{"Error parsing altitude in update: " + args[6]}
	}
	if groundspeed, err = strconv.Atoi(args[7]); err != nil {
		return MalformedMessageError{"Error parsing ground speed in update: " + args[7]}
	}
	if surfaces, err = strconv.ParseUint(args[8], 10, 64); err != nil {
		return MalformedMessageError{"Error parsing flight surfaces in update: " + args[8]}
	}
	if _, err = strconv.Atoi(args[9]); err != nil {
		// args[9] is a pressure delta: altitude + pressure gives pressure
		// altitude (currently ignored--is this what we should be reporting
		// on the scope, though?)
		return MalformedMessageError{"Error parsing pressure in update: " + args[9]}
	}

	// Decode flight surfaces; we ignore pitch and bank and just grab heading
	heading := float32((surfaces>>2)&0x3ff) / 1024 * 360
	if heading < 0 {
		heading += 360
	}
	if heading >= 360 {
		heading -= 360
	}

	ac := v.getOrCreateAircraft(callsign)
	ac.Squawk = squawk
	ac.Mode = mode
	ac.AddTrack(RadarTrack{
		Position:    latlong,
		Altitude:    int(altitude),
		Groundspeed: int(groundspeed),
		Heading:     heading,
		Time:        v.CurrentTime()})

	return nil
}

// ATC update
// %JFK_TWR:19100:4:20:5:40.63944:-73.76639:0
// (callsign):(frequency):(facility):(range):(rating):(lat):(long):(???? always 0 ????)
func handlePct(v *VATSIMServer, sender string, args []string) error {
	frequency, err := parseFrequency(args[1])
	if err != nil {
		return err
	}

	facility, err := strconv.Atoi(args[2])
	if err != nil {
		return MalformedMessageError{"Malformed facility: " + args[2]}
	}
	if facility < 0 || facility >= FacilityUndefined {
		return MalformedMessageError{"Invalid facility index: " + args[2]}
	}

	scopeRange, err := strconv.Atoi(args[3])
	if err != nil {
		return MalformedMessageError{"Invalid scope range: " + args[3]}
	}

	rating, err := parseRating(args[4])
	if err != nil {
		return err
	}

	latlong, err := parseLatitudeLongitude(args[5], args[6])
	if err != nil {
		return err
	}

	if ctrl, ok := v.controllers[sender]; ok {
		if pos := ctrl.GetPosition(); pos != nil {
			delete(v.controllerSectors, pos.SectorId)
		}
	}

	ctrl := &Controller{
		Callsign:   sender,
		Facility:   Facility(facility),
		Frequency:  frequency,
		ScopeRange: scopeRange,
		Rating:     rating,
		Location:   latlong}
	v.controllers[sender] = ctrl
	eventStream.Post(&ModifiedControllerEvent{ctrl})

	if pos := ctrl.GetPosition(); pos != nil {
		v.controllerSectors[pos.SectorId] = ctrl
	}

	return nil
}

func parseRating(s string) (NetworkRating, error) {
	if rating, err := strconv.Atoi(s); err != nil {
		return UndefinedRating, MalformedMessageError{"Invalid rating: " + s}
	} else if rating < 0 || rating > AdministratorRating {
		return UndefinedRating, MalformedMessageError{"Invalid rating: " + s}
	} else {
		return NetworkRating(rating), nil
	}
}

func parseFrequency(s string) (Frequency, error) {
	if frequency, err := strconv.Atoi(s); err != nil {
		return 0, MalformedMessageError{"Invalid frequency: " + s}
	} else {
		return Frequency(100000 + frequency), nil
	}
}

func parseLatitudeLongitude(lat, long string) (Point2LL, error) {
	latitude, err := strconv.ParseFloat(lat, 64)
	if err != nil {
		return Point2LL{}, MalformedMessageError{"Invalid latitude: " + lat}
	}

	longitude, err := strconv.ParseFloat(long, 64)
	if err != nil {
		return Point2LL{}, MalformedMessageError{"Invalid longitude: " + long}
	}

	return Point2LL{float32(longitude), float32(latitude)}, nil
}

func (v *VATSIMServer) altitudeAssigned(strs []string, csIndex int, altIndex int) error {
	if csIndex >= len(strs) || altIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to specify altitude"}
	}

	callsign := strs[csIndex]
	if alt, err := strconv.Atoi(strs[altIndex]); err != nil {
		return MalformedMessageError{"invalid altitude: " + strs[altIndex]}
	} else if ac := v.GetAircraft(callsign); ac != nil && ac.FlightPlan != nil {
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		ac.FlightPlan.Altitude = alt
	}
	return nil
}

func (v *VATSIMServer) temporaryAltitudeAssigned(strs []string, csIndex int, altIndex int) error {
	if csIndex >= len(strs) || altIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to specify temporary altitude"}
	}

	callsign := strs[csIndex]
	if alt, err := strconv.Atoi(strs[altIndex]); err != nil {
		return MalformedMessageError{"invalid temporary altitude: " + strs[altIndex]}
	} else {
		ac := v.getOrCreateAircraft(callsign)
		ac.TempAltitude = alt
		return nil
	}
}

func (v *VATSIMServer) squawkCodeAssigned(strs []string, csIndex int, sqIndex int) error {
	if csIndex >= len(strs) || sqIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments for squawk assignment"}
	}

	callsign := strs[csIndex]
	if squawk, err := ParseSquawk(strs[sqIndex]); err != nil {
		return err
	} else {
		ac := v.getOrCreateAircraft(callsign)
		ac.AssignedSquawk = squawk
		return nil
	}
}

func (v *VATSIMServer) scratchpadSet(strs []string, csIndex int, spIndex int) error {
	if csIndex >= len(strs) || spIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to set scratchpad"}
	}

	callsign := strs[csIndex]
	ac := v.getOrCreateAircraft(callsign)
	ac.Scratchpad = strs[spIndex]
	return nil
}

func (v *VATSIMServer) voiceTypeSet(strs []string, csIndex int, vtIndex int) error {
	if csIndex >= len(strs) || vtIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to set voice type"}
	}

	callsign := strs[csIndex]
	ac := v.getOrCreateAircraft(callsign)
	switch strs[vtIndex] {
	case "v":
		ac.VoiceCapability = VoiceFull
	case "r":
		ac.VoiceCapability = VoiceReceive
	case "t":
		ac.VoiceCapability = VoiceText
	case "":
		ac.VoiceCapability = VoiceUnknown
	default:
		return MalformedMessageError{"Unexpected voice capability: " + strs[vtIndex]}
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////

func init() {
	r := func(s *VATSIMMessageSpec) { vatsimMessageSpecs = append(vatsimMessageSpecs, s) }
	ignore := func(id string) {
		r(NewMessageSpec(id, 1, nil))
	}

	r(NewMessageSpec("%", 6, handlePct))

	r(NewMessageSpec("@", 10, handleAt))

	r(NewMessageSpec("#AA", 7, handleAA))

	r(NewMessageSpec("#AP:SERVER", 8, handleAP))

	r(NewMessageSpec("$AR::METAR", 4, handleAR))

	ignore("#CD")

	ignore("$CQ::ACC")
	ignore("$CQ::ATC")
	ignore("$CQ::ATIS")

	r(NewMessageSpec("$CQ::BC", 5, func(v *VATSIMServer, sender string, args []string) error {
		return v.squawkCodeAssigned(args, 3, 4)
	}))

	r(NewMessageSpec("$CQ::BY", 3, func(v *VATSIMServer, sender string, args []string) error {
		if ctrl := v.GetController(sender); ctrl != nil {
			ctrl.RequestRelief = true
		}
		return nil
	}))

	ignore("$CQ::C?")
	ignore("$CQ::CAPS")

	r(NewMessageSpec("$CQ::DR", 4, func(v *VATSIMServer, sender string, args []string) error {
		callsign := args[3]
		ac := v.getOrCreateAircraft(callsign)

		if ac.TrackingController != sender {
			lg.Printf("%s: %s dropped track but currently tracked by %s", callsign, sender,
				ac.TrackingController)
		}

		ac.TrackingController = ""
		ac.InboundHandoffController = ""
		ac.OutboundHandoffController = ""

		return nil
	}))

	ignore("$CQ::EST")

	r(NewMessageSpec("$CQ::FA", 5, func(v *VATSIMServer, sender string, args []string) error {
		return v.altitudeAssigned(args, 3, 4)
	}))

	ignore("$CQ::FP")

	r(NewMessageSpec("$CQ::HI", 3, func(v *VATSIMServer, sender string, args []string) error {
		if ctrl := v.GetController(sender); ctrl != nil {
			ctrl.RequestRelief = false
		}
		return nil
	}))

	ignore("$CQ::HLP")

	r(NewMessageSpec("$CQ::HT", 5, func(v *VATSIMServer, sender string, args []string) error {
		callsign := args[3]
		ac := v.getOrCreateAircraft(callsign)
		ac.TrackingController = sender
		if ac.OutboundHandoffController != "" {
			ac.OutboundHandoffController = ""
			eventStream.Post(&AcceptedHandoffEvent{controller: sender, ac: ac})
		}
		return nil
	}))

	ignore("$CQ::INF")
	ignore("$CQ::IP")

	r(NewMessageSpec("$CQ::IT", 4, func(v *VATSIMServer, sender string, args []string) error {
		callsign := args[3]
		return v.trackInitiated(callsign, sender)
	}))

	r(NewMessageSpec("$CQ::NEWATIS", 4, func(v *VATSIMServer, sender string, args []string) error {
		var contents, code string
		if len(args) == 5 {
			// I think this is the expected way
			// $CQKEKY_ATIS:@94835:NEWATIS:ATIS A:  VRB05KT - A3006
			code = string(args[3][len(args[3])-1])
			contents = args[4]
		} else {
			// But occasionally it comes in like this
			// CQKORF_ATIS:@94835:NEWATIS:ATIS D - wind 07009KT alt 3000
			if len(args[3]) < 8 {
				return MalformedMessageError{"Insufficient ATIS text" + args[3]}
			}
			code = string(args[3][5])
			contents = args[3][8:]
		}

		// First, figure out which airport this is for; we want to
		// associate senders ranging from KPHL_ATIS to those like
		// KPHL_A_ATIS and KPHL_D_ATIS with KPHL, so we'll just take
		// everything up to the first _.
		sc := strings.Split(sender, "_")
		if len(sc) == 0 {
			return MalformedMessageError{"Unexpected ATIS sender format" + sender}
		}
		airport := sc[0]

		atis := ATIS{Airport: sender, Code: code, Contents: contents}
		eventStream.Post(&ReceivedATISEvent{atis})
		for i, a := range v.atis[airport] {
			if a.Airport == sender {
				// Replace pre-existing
				v.atis[airport][i] = atis
				return nil
			}
		}
		// Add a new one
		v.atis[airport] = append(v.atis[airport], atis)
		return nil
	}))

	ignore("$CQ::NEWINFO")
	ignore("$CQ::NOHLP")
	ignore("$CQ::RN")

	r(NewMessageSpec("$CQ::SC", 5, func(v *VATSIMServer, sender string, args []string) error {
		return v.scratchpadSet(args, 3, 4)
	}))

	ignore("$CQ::SV")

	r(NewMessageSpec("$CQ::TA", 5, func(v *VATSIMServer, sender string, args []string) error {
		return v.temporaryAltitudeAssigned(args, 3, 4)
	}))

	ignore("$CQ::WH")

	r(NewMessageSpec("$CQ::VT", 5, func(v *VATSIMServer, sender string, args []string) error {
		return v.voiceTypeSet(args, 3, 4)
	}))

	ignore("$CR::ATC")

	r(NewMessageSpec("$CR::ATIS", 5, func(v *VATSIMServer, sender string, args []string) error {
		// Ignore all of the other subtypes
		if args[3] == "T" {
			tm := TextMessage{
				messageType: TextPrivate,
				sender:      sender,
				contents:    "ATIS: " + args[4],
			}
			eventStream.Post(&TextMessageEvent{message: &tm})
		}
		return nil
	}))

	ignore("$CR::CAPS")
	ignore("$CR::IP")

	r(NewMessageSpec("$CR::RN", 6, func(v *VATSIMServer, sender string, args []string) error {
		if rating, err := parseRating(args[5]); err != nil {
			return err
		} else {
			name := args[3]
			v.users[name] = &User{Name: name, Note: args[4], Rating: rating}
			return nil
		}
	}))

	r(NewMessageSpec("#DA", 1, func(v *VATSIMServer, sender string, args []string) error {
		if ctrl, ok := v.controllers[sender]; ok {
			eventStream.Post(&RemovedControllerEvent{Controller: ctrl})
			if pos := ctrl.GetPosition(); pos != nil {
				delete(v.controllerSectors, pos.SectorId)
			}
			delete(v.controllers, sender)
		}

		for _, ac := range v.aircraft {
			if ac.TrackingController == sender {
				ac.TrackingController = ""
				ac.InboundHandoffController = ""
			}
			if ac.OutboundHandoffController == sender {
				// TODO: send a rejected handoff event if we were trying to
				// hand off to this controller?
				ac.OutboundHandoffController = ""
			}
		}

		return nil
	}))

	ignore("$DI")
	ignore("#DL")

	r(NewMessageSpec("#DP", 2, func(v *VATSIMServer, callsign string, args []string) error {
		if ac := v.GetAircraft(callsign); ac != nil {
			eventStream.Post(&RemovedAircraftEvent{ac: ac})
		}
		delete(v.aircraft, callsign)
		delete(v.pilots, callsign)

		return nil
	}))

	r(NewMessageSpec("$FP", 17, handleFP))

	r(NewMessageSpec("$HA", 3, handleHA))

	r(NewMessageSpec("$HO", 3, handleHO))

	r(NewMessageSpec("#PC::CCP:BC", 6, func(v *VATSIMServer, sender string, args []string) error {
		return v.squawkCodeAssigned(args, 4, 5)
	}))

	ignore("#PC::CCP:DI")
	ignore("#PC::CCP:DP") // TODO: push departure

	r(NewMessageSpec("#PC::CCP:FA", 6, func(v *VATSIMServer, sender string, args []string) error {
		return v.altitudeAssigned(args, 4, 5)
	}))

	r(NewMessageSpec("#PC::CCP:HC", 5, func(v *VATSIMServer, sender string, args []string) error {
		callsign := args[4]
		ac := v.getOrCreateAircraft(callsign)
		if ac.OutboundHandoffController != "" {
			ac.OutboundHandoffController = ""
			eventStream.Post(&RejectedHandoffEvent{controller: sender, ac: ac})
		} else if ac.InboundHandoffController != "" {
			ac.InboundHandoffController = ""
			eventStream.Post(&CanceledHandoffEvent{controller: sender, ac: ac})
		}
		return nil
	}))

	ignore("#PC::CCP:ID")

	r(NewMessageSpec("#PC::CCP:IH", 5, func(v *VATSIMServer, sender string, args []string) error {
		return v.trackInitiated(args[4], sender)
	}))

	r(NewMessageSpec("#PC::CCP:PT", 5, func(v *VATSIMServer, sender string, args []string) error {
		if args[1] == v.callsign {
			callsign := args[4]
			ac := v.getOrCreateAircraft(callsign)
			eventStream.Post(&PointOutEvent{controller: sender, ac: ac})
		}
		return nil
	}))

	r(NewMessageSpec("#PC::CCP:SC", 6, func(v *VATSIMServer, sender string, args []string) error {
		return v.scratchpadSet(args, 4, 5)
	}))

	r(NewMessageSpec("#PC::CCP:ST", 5, func(v *VATSIMServer, sender string, args []string) error {
		to := args[1]
		if to != v.callsign {
			return nil
		}

		callsign := args[4]
		fs := &FlightStrip{callsign: callsign}
		if len(args) >= 7 {
			for i, ann := range args[6:] {
				if i == 9 {
					break
				}
				fs.annotations[i] = ann
			}
		}

		if _, ok := v.flightStrips[callsign]; ok {
			lg.Printf("%s: already have a flight strip but one was pushed. Taking the pushed one...?",
				callsign)
		}

		v.flightStrips[callsign] = fs
		eventStream.Post(&PushedFlightStripEvent{callsign: callsign})

		return nil
	}))

	r(NewMessageSpec("#PC::CCP:TA", 6, func(v *VATSIMServer, sender string, args []string) error {
		return v.temporaryAltitudeAssigned(args, 4, 5)
	}))

	r(NewMessageSpec("#PC::CCP:VT", 6, func(v *VATSIMServer, sender string, args []string) error {
		return v.voiceTypeSet(args, 4, 5)
	}))

	ignore("#SB")
	ignore("#SL")
	ignore("#ST")
	ignore("#TD")

	r(NewMessageSpec("#TM", 3, handleTM))

	ignore("#WD")
	ignore("$ZC")
	ignore("$ZR")
}
