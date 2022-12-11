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
		v.controllers[sender] = &Controller{
			callsign: sender,
			name:     args[2],
			cid:      args[3],
			rating:   rating}
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
		v.pilots[callsign] = &Pilot{
			callsign: callsign,
			cid:      args[2],
			name:     args[7],
			rating:   rating}
		return nil
	}
}

// $ARserver:ABE_APP:METAR:KABE 232251Z 24006KT ...
func handleAR(v *VATSIMServer, sender string, args []string) error {
	fields := strings.Fields(args[3])
	if len(fields) < 3 {
		return MalformedMessageError{"Expected >= 3 fields in METAR text"}
	}
	i := 0
	next := func() string {
		if i == len(fields) {
			return ""
		}
		s := fields[i]
		i++
		return s
	}

	m := METAR{airport: next(), time: next(), wind: next()}
	if m.wind == "AUTO" {
		m.auto = true
		m.wind = next()
	}

	for {
		s := next()
		if s == "" {
			break
		}
		if s[0] == 'A' || s[0] == 'Q' {
			m.altimeter = s
			break
		}
		m.weather += s + " "
	}
	m.weather = strings.TrimRight(m.weather, " ")

	if s := next(); s != "RMK" {
		// TODO: improve the METAR parser...
		lg.Printf("Expecting RMK where %s is in METAR \"%s\"", s, args[3])
	} else {
		for s != "" {
			s = next()
			m.rmk += s + " "
		}
		m.rmk = strings.TrimRight(m.rmk, " ")
	}

	v.metar[m.airport] = m

	return nil
}

// Flightplan
func handleFP(v *VATSIMServer, sender string, args []string) error {
	var fp FlightPlan

	switch args[2] {
	case "I":
		fp.rules = IFR
	case "V":
		fp.rules = VFR
	case "D":
		fp.rules = DVFR
	case "S":
		fp.rules = SVFR
	default:
		return MalformedMessageError{"Unexpected flight rules: " + args[2]}
	}

	fp.actype = args[3]

	var err error
	if fp.cruiseSpeed, err = strconv.Atoi(args[4]); err != nil {
		return MalformedMessageError{"Unable to parse cruise airspeed: " + args[4]}
	}

	fp.depart = args[5]

	if fp.departTimeEst, err = strconv.Atoi(args[6]); err != nil {
		return MalformedMessageError{"Unable to parse departTime: " + args[6]}
	}
	if fp.departTimeActual, err = strconv.Atoi(args[7]); err != nil {
		return MalformedMessageError{"Unable to parse departTime: " + args[7]}
	}

	if args[8] != "" {
		if strings.HasPrefix(strings.ToUpper(args[8]), "FL") {
			if alt, err := strconv.Atoi(args[8][2:]); err != nil {
				return MalformedMessageError{"Unable to parse altitude: " + args[8]}
			} else {
				fp.altitude = alt * 100
			}
		} else if alt, err := strconv.Atoi(args[8]); err != nil {
			return MalformedMessageError{"Unable to parse altitude: " + args[8]}
		} else {
			fp.altitude = alt
		}
	}

	fp.arrive = args[9]

	if fp.hours, err = strconv.Atoi(args[10]); err != nil {
		return MalformedMessageError{"Unable to parse enroute hours: " + args[10]}
	}
	if fp.minutes, err = strconv.Atoi(args[11]); err != nil {
		return MalformedMessageError{"Unable to parse enroute minutes: " + args[11]}
	}
	if fp.fuelHours, err = strconv.Atoi(args[12]); err != nil {
		return MalformedMessageError{"Unable to parse fuel hours: " + args[12]}
	}
	if fp.fuelMinutes, err = strconv.Atoi(args[13]); err != nil {
		return MalformedMessageError{"Unable to parse fuel minutes: " + args[13]}
	}

	fp.alternate = args[14]
	fp.remarks = args[15]
	fp.route = args[16]

	ac := v.getOrCreateAircraft(sender)
	ac.flightPlan = &fp

	if strings.Contains(fp.remarks, "/v/") || strings.Contains(fp.remarks, "/V/") {
		ac.voiceCapability = VoiceFull
	} else if strings.Contains(fp.remarks, "/r/") || strings.Contains(fp.remarks, "/R/") {
		ac.voiceCapability = VoiceReceive
	} else if strings.Contains(fp.remarks, "/t/") || strings.Contains(fp.remarks, "/T/") {
		ac.voiceCapability = VoiceText
	}

	return nil
}

// Accepted handoff: TWR accepted N11TV from APP
// $HAABE_TWR:ABE_APP:N11TV
func handleHA(v *VATSIMServer, sender string, args []string) error {
	from, callsign := args[1], args[2]
	ac := v.getOrCreateAircraft(callsign)

	if ac.trackingController != from {
		lg.Printf("%s: %s is tracking but %s accepted h/o from %s?", callsign, ac.trackingController,
			sender, from)
	}

	if from == v.callsign {
		// from us!
		ac.outboundHandoffController = ""
		eventStream.Post(&AcceptedHandoffEvent{controller: sender, ac: ac})
	}

	ac.trackingController = sender

	return nil
}

// Handoff request: DEP wants to hand FDX off to APP
// $HOABE_DEP:ABE_APP:FDX901
func handleHO(v *VATSIMServer, sender string, args []string) error {
	receiver, callsign := args[1], args[2]
	if receiver == v.callsign {
		ac := v.getOrCreateAircraft(callsign)
		ac.inboundHandoffController = sender
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
	ac.squawk = squawk
	ac.mode = mode
	ac.AddTrack(RadarTrack{
		position:    latlong,
		altitude:    int(altitude),
		groundspeed: int(groundspeed),
		heading:     heading,
		time:        v.CurrentTime()})

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
			delete(v.controllerSectors, pos.sectorId)
		}
	}

	ctrl := &Controller{
		callsign:   sender,
		facility:   Facility(facility),
		frequency:  frequency,
		scopeRange: scopeRange,
		rating:     rating,
		location:   latlong}
	v.controllers[sender] = ctrl

	if pos := ctrl.GetPosition(); pos != nil {
		v.controllerSectors[pos.sectorId] = ctrl
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
	} else if ac := v.GetAircraft(callsign); ac != nil && ac.flightPlan != nil {
		eventStream.Post(&ModifiedAircraftEvent{ac: ac})
		ac.flightPlan.altitude = alt
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
		ac.tempAltitude = alt
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
		ac.assignedSquawk = squawk
		return nil
	}
}

func (v *VATSIMServer) scratchpadSet(strs []string, csIndex int, spIndex int) error {
	if csIndex >= len(strs) || spIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to set scratchpad"}
	}

	callsign := strs[csIndex]
	ac := v.getOrCreateAircraft(callsign)
	ac.scratchpad = strs[spIndex]
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
		ac.voiceCapability = VoiceFull
	case "r":
		ac.voiceCapability = VoiceReceive
	case "t":
		ac.voiceCapability = VoiceText
	case "":
		ac.voiceCapability = VoiceUnknown
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
			ctrl.requestRelief = true
		}
		return nil
	}))

	ignore("$CQ::C?")
	ignore("$CQ::CAPS")

	r(NewMessageSpec("$CQ::DR", 4, func(v *VATSIMServer, sender string, args []string) error {
		callsign := args[3]
		ac := v.getOrCreateAircraft(callsign)

		if ac.trackingController != sender {
			lg.Printf("%s: %s dropped track but currently tracked by %s", callsign, sender,
				ac.trackingController)
		}

		ac.trackingController = ""
		ac.inboundHandoffController = ""
		ac.outboundHandoffController = ""

		return nil
	}))

	ignore("$CQ::EST")

	r(NewMessageSpec("$CQ::FA", 5, func(v *VATSIMServer, sender string, args []string) error {
		return v.altitudeAssigned(args, 3, 4)
	}))

	ignore("$CQ::FP")

	r(NewMessageSpec("$CQ::HI", 3, func(v *VATSIMServer, sender string, args []string) error {
		if ctrl := v.GetController(sender); ctrl != nil {
			ctrl.requestRelief = false
		}
		return nil
	}))

	ignore("$CQ::HLP")

	r(NewMessageSpec("$CQ::HT", 5, func(v *VATSIMServer, sender string, args []string) error {
		callsign := args[3]
		ac := v.getOrCreateAircraft(callsign)
		ac.trackingController = sender
		if ac.outboundHandoffController != "" {
			ac.outboundHandoffController = ""
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
		var atis string
		if len(args) == 5 {
			// I think this is the expected way
			// $CQKEKY_ATIS:@94835:NEWATIS:ATIS A:  VRB05KT - A3006
			letter := args[3][len(args[3])-1]
			atis = string(letter) + " " + args[4]
		} else {
			// But occasionally it comes in like this
			// CQKORF_ATIS:@94835:NEWATIS:ATIS D - wind 07009KT alt 3000
			if len(args[3]) < 8 {
				return MalformedMessageError{"Insufficient ATIS text" + args[3]}
			}
			letter := args[3][5]
			atis = string(letter) + " " + args[3][8:]
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

		for i, a := range v.atis[airport] {
			if a.callsign == sender {
				// Replace pre-existing
				v.atis[airport][i] = ATIS{callsign: sender, contents: atis}
				return nil
			}
		}
		// Add a new one
		v.atis[airport] = append(v.atis[airport], ATIS{callsign: sender, contents: atis})
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
			v.users[name] = &User{name: name, note: args[4], rating: rating}
			return nil
		}
	}))

	r(NewMessageSpec("#DA", 1, func(v *VATSIMServer, sender string, args []string) error {
		if ctrl, ok := v.controllers[sender]; ok {
			if pos := ctrl.GetPosition(); pos != nil {
				delete(v.controllerSectors, pos.sectorId)
			}
			delete(v.controllers, sender)
		}

		for _, ac := range v.aircraft {
			if ac.trackingController == sender {
				ac.trackingController = ""
				ac.inboundHandoffController = ""
			}
			if ac.outboundHandoffController == sender {
				// TODO: send a rejected handoff event if we were trying to
				// hand off to this controller?
				ac.outboundHandoffController = ""
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
		ac.outboundHandoffController = ""
		eventStream.Post(&RejectedHandoffEvent{controller: sender, ac: ac})
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
