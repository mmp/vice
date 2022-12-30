// vatsim-internal.go
// Copyright(c) 2022 Matt Pharr.

package main

// #include "vatsim-internal.h"
import "C"

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

///////////////////////////////////////////////////////////////////////////
// VATSIMAircraftController

const CQBroadcast = "@94835"

type VATSIMAircraftController struct {
	vs            *VATSIMServer
	eventStreamId EventSubscriberId

	lastRadarPrimaryPosReport   time.Time
	lastRadarSecondaryPosReport time.Time

	primaryRadarCenter    Point2LL
	secondaryRadarCenters [3]Point2LL
	radarRange            int
}

func (v *VATSIMAircraftController) SetSquawk(callsign string, squawk Squawk) error {
	v.vs.send("#TM", "FP", callsign+" SET "+squawk.String())
	v.vs.send("$CQ", CQBroadcast, "BC", callsign, squawk.String())
	return nil
}

func (v *VATSIMAircraftController) SetSquawkAutomatic(callsign string) error {
	return nil
}

func (v *VATSIMAircraftController) SetScratchpad(callsign string, scratchpad string) error {
	v.vs.send("$CQ", CQBroadcast, "SC", callsign, scratchpad)
	return nil
}

func (v *VATSIMAircraftController) SetTemporaryAltitude(callsign string, alt int) error {
	v.vs.send("$CQ", CQBroadcast, "TA", callsign, alt)
	return nil
}

func (v *VATSIMAircraftController) SetVoiceType(callsign string, cap VoiceCapability) error {
	v.vs.send("$CQ", CQBroadcast, "VT", callsign, cap.String())
	v.vs.send("#PC", CQBroadcast, "CCP", "VT", callsign, cap.String())
	return nil
}

func (v *VATSIMAircraftController) AmendFlightPlan(callsign string, fp FlightPlan) error {
	rules := ""
	switch fp.Rules {
	case IFR:
		rules = "I"
	case VFR:
		rules = "V"
	case DVFR:
		rules = "D"
	case SVFR:
		rules = "S"
	}
	v.vs.send("$AM", "SERVER", callsign, rules, fp.AircraftType, fp.CruiseSpeed,
		fp.DepartureAirport, fp.DepartTimeEst, fp.DepartTimeActual, fp.Altitude, fp.ArrivalAirport,
		fp.Hours, fp.Minutes, fp.FuelHours, fp.FuelMinutes, fp.AlternateAirport,
		fp.Remarks, fp.Route)
	v.vs.send("$CQ", CQBroadcast, "FA", fp.Altitude)

	return nil
}

func (v *VATSIMAircraftController) PushFlightStrip(callsign string, controller string) error {
	if fs, ok := v.vs.flightStrips[callsign]; !ok {
		return ErrNoAircraftForCallsign
	} else {
		v.vs.send("#PC", controller, "CCP", "ST", callsign, 1 /* version */, fs.annotations[0],
			fs.annotations[1], fs.annotations[2], fs.annotations[3], fs.annotations[4],
			fs.annotations[5], fs.annotations[6], fs.annotations[7], fs.annotations[8])
		return nil
	}
}

func (v *VATSIMAircraftController) InitiateTrack(callsign string) error {
	v.vs.send("$CQ", CQBroadcast, "IT", callsign)
	return nil
}

func (v *VATSIMAircraftController) DropTrack(callsign string) error {
	v.vs.send("$CQ", CQBroadcast, "DR", callsign)
	v.vs.send("#TM", "FP", callsign+" release") // unlock flight plan
	return nil
}

func (v *VATSIMAircraftController) Handoff(callsign string, controller string) error {
	v.vs.send("$HO", controller, callsign)
	return nil
}

func (v *VATSIMAircraftController) AcceptHandoff(callsign string) error {
	ac := v.vs.GetAircraft(callsign)
	if ac == nil {
		return ErrNoAircraftForCallsign
	} else if controller := ac.InboundHandoffController; controller == "" {
		return ErrNotBeingHandedOffToMe
	} else {
		v.vs.send("$CQ", CQBroadcast, "HT", callsign, controller)
		v.vs.send("$HA", controller, callsign)
	}
	return nil
}

func (v *VATSIMAircraftController) RejectHandoff(callsign string) error {
	ac := v.vs.GetAircraft(callsign)
	if ac == nil {
		return ErrNoAircraftForCallsign
	} else if controller := ac.InboundHandoffController; controller == "" {
		return ErrNotBeingHandedOffToMe
	} else {
		v.vs.send("#PC", controller, "CCP", "HC", callsign)
	}
	return nil
}

func (v *VATSIMAircraftController) CancelHandoff(callsign string) error {
	ac := v.vs.GetAircraft(callsign)
	if ac == nil {
		return ErrNoAircraftForCallsign
	} else if controller := ac.OutboundHandoffController; controller == "" {
		return ErrNotHandingOffAircraft
	} else {
		v.vs.send("#PC", controller, "CCP", "HC", callsign)
	}
	return nil
}

func (v *VATSIMAircraftController) PointOut(callsign string, controller string) error {
	v.vs.send("#PC", controller, "CCP", "PT", callsign)
	return nil
}

func (v *VATSIMAircraftController) SendTextMessage(m TextMessage) error {
	to := ""
	switch m.messageType {
	case TextBroadcast:
		to = "*"

	case TextWallop:
		to = "*S"

	case TextATC:
		to = "@49999"

	case TextFrequency:
		to = strings.Join(MapSlice(m.frequencies, func(f Frequency) string {
			if f == Frequency(0) {
				return "@99998"
			} else {
				return fmt.Sprintf("@%5d", f%100000)
			}
		}), "&")

	case TextPrivate:
		if ctrl := v.vs.GetController(m.recipient); ctrl != nil {
			to = ctrl.Callsign
		} else if ac := v.vs.GetAircraft(m.recipient); ac != nil {
			to = ac.Callsign
		} else {
			return ErrNoControllerOrAircraft
		}

	default:
		return fmt.Errorf("%s: unexpected message type for SendTextMessage",
			m.messageType.String())
	}

	v.vs.send("#TM", to, m.contents)
	return nil
}

func (v *VATSIMAircraftController) RequestControllerATIS(controller string) error {
	c := v.vs.GetController(controller)
	if c == nil {
		return ErrNoController
	}
	v.vs.send("$CQ", c.Callsign, "ATIS")
	return nil
}

func (v *VATSIMAircraftController) Disconnect() {
	eventStream.Unsubscribe(v.eventStreamId)
	v.eventStreamId = InvalidEventSubscriberId
}

///////////////////////////////////////////////////////////////////////////

var (
	// Client session key and challenge key
	csk, cck string

	sysuid string = getSysuid()
	myip   string

	pendingINFReplies []string

	lastMETARRequest time.Time
)

// "challenge response", sorta obfuscated;
func cr(challenge string, key string) string {
	// Our id mod 3 is zero, so key order is ABC, but the challenge is
	// swapped at the midpoint.
	nc := len(challenge)
	bytes := key[:12]
	bytes += challenge[nc/2:]
	bytes += key[12:22]
	bytes += challenge[:nc/2]
	bytes += key[22:]

	sum := md5.Sum([]byte(bytes))
	return strings.ToLower(hex.EncodeToString(sum[:]))
}

func getSysuid() string {
	return C.GoString(C.getSysuid())
}

// Login stuff
func handleDI(v *VATSIMServer, sender string, args []string) error {
	lg.Printf("Connected to %s", args[1])

	clientChallenge := args[3]

	// private key
	k := "3c7a6505a0f6600d9cd7d42ca2a17823"
	csk = cr(clientChallenge, k)
	cck = csk // initial challenge key is same as the session key

	// This must be the first response, so let's kick it off immediately.
	const clientId = "24bd"
	cid := globalConfig.VatsimCID
	v.send("$ID", "SERVER", clientId, ViceVersionString, "0" /* major */, "1", /* minor */
		cid, sysuid, "" /* no server challenge */)

	// Get auth token...
	password := globalConfig.VatsimPassword
	url := "https://auth.vatsim.net/api/fsd-jwt"
	resp, err := http.Post(url+"?cid="+cid+"&password="+password, "text/html", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	var response struct {
		Success      bool   `json:"success"`
		ErrorMessage string `json:"error_msg"`
		Token        string `json:"token"`
	}
	if err := decoder.Decode(&response); err != nil {
		return err
	}
	if !response.Success {
		lg.Printf("VATSIM login error %+v", response)
		v.Disconnect()
		ShowErrorDialog("Error logging in to VATSIM: " + response.ErrorMessage)
		return nil
	}

	// Next send the login message
	v.send("#AA", "SERVER", globalConfig.VatsimName, cid, response.Token,
		int(globalConfig.VatsimRating), 100 /* protocol version */)

	v.send("$CQ", "SERVER", "IP")
	v.send("$CQ", "SERVER", "CAPS")
	// Send off the query about whether we are a legit ATC
	v.send("$CQ", "SERVER", "ATC")

	return nil
}

func (v *VATSIMAircraftController) SetRadarCenters(primary Point2LL, secondary [3]Point2LL, rangeNm int) error {
	// Not authenticated yet
	if csk == "" {
		return nil
	}

	sendPrimary := primary != v.primaryRadarCenter || rangeNm != v.radarRange
	sendSecondary := false
	for i := range secondary {
		sendSecondary = sendSecondary || secondary[i] != v.secondaryRadarCenters[i]
	}

	v.primaryRadarCenter = primary
	v.secondaryRadarCenters = secondary
	v.radarRange = rangeNm

	if v.primaryRadarCenter.IsZero() {
		return nil
	}

	if time.Since(v.lastRadarSecondaryPosReport) > 60*time.Second {
		sendSecondary = true
	}
	if time.Since(v.lastRadarPrimaryPosReport) > 15*time.Second {
		sendPrimary = true
	}

	if sendPrimary || sendSecondary {
		v.lastRadarPrimaryPosReport = time.Now()

		freq := "99998"
		if positionConfig.primaryFrequency != Frequency(0) {
			freq = fmt.Sprintf("%5d", positionConfig.primaryFrequency%100000)
		}
		for tx, enabled := range positionConfig.txFrequencies {
			if enabled != nil && *enabled && tx != positionConfig.primaryFrequency {
				if len(freq) > 0 {
					freq += "&"
				}
				freq += fmt.Sprintf("%5d", tx%100000)
			}
		}

		v.vs.send("%", freq, int(positionConfig.VatsimFacility), v.radarRange,
			int(globalConfig.VatsimRating), v.primaryRadarCenter.Latitude(), v.primaryRadarCenter.Longitude(), 0)
	}
	if sendSecondary {
		v.lastRadarSecondaryPosReport = time.Now()

		idx := 0
		for _, center := range v.secondaryRadarCenters {
			if !center.IsZero() {
				v.vs.send("'", idx, center.Latitude(), center.Longitude())
				idx++
			}
		}
	}

	return nil
}

// Auth
func handleZC(v *VATSIMServer, sender string, args []string) error {
	// Any need to make sure it's sent to us? (args[0])
	challenge := args[2]

	response := cr(challenge, cck)
	// Update the challenge key
	bytes := csk + response
	sum := md5.Sum([]byte(bytes))
	cck = strings.ToLower(hex.EncodeToString(sum[:]))

	v.send("$ZR", "SERVER", response)

	return nil
}

// Disconnect, now.
func handleBangBang(v *VATSIMServer, position string, args []string) error {
	reason := args[0]

	tm := TextMessage{
		sender:      "Supervisor: " + position,
		contents:    "you have been disconnected: " + reason,
		messageType: TextPrivate}

	eventStream.Post(&TextMessageEvent{message: &tm})

	v.Disconnect()

	return nil
}

func sendINFReply(v *VATSIMServer, sender string) {
	// client system information, for supe/admin
	pos, _ := database.Locate(positionConfig.PrimaryRadarCenter)
	info := fmt.Sprintf("CID=%s %s IP=%s SYS_UID=%s FSVER= LT=%f LO=%f AL=0 %s",
		globalConfig.VatsimCID, ViceVersionString, myip, sysuid,
		pos.Latitude(), pos.Longitude(), globalConfig.VatsimName)

	v.send("#TM", sender, info)
}

func init() {
	makeVatsimAircraftController = func(vs *VATSIMServer) AircraftController {
		c := &VATSIMAircraftController{
			vs:                    vs,
			eventStreamId:         eventStream.Subscribe(),
			primaryRadarCenter:    positionConfig.primaryRadarCenterLocation,
			secondaryRadarCenters: positionConfig.secondaryRadarCentersLocation,
		}

		return c
	}

	vatsimUpdateCallback = func(v *VATSIMServer) {
		// Assorted requests / updates that are periodically sent to the
		// server.  Don't do these until after we've gotten the #DI from the
		// server and have made the initial responses.
		if csk != "" {
			if time.Since(lastMETARRequest) > 5*time.Minute {
				for ap := range v.airportsForWeather {
					v.send("$AX", "SERVER", "METAR", strings.ToUpper(ap))
				}
				lastMETARRequest = time.Now()
			}
		}

		// The delegate may be nil e.g. during a replay...
		if controller, ok := v.controlDelegate.(*VATSIMAircraftController); ok {
			for _, event := range eventStream.Get(controller.eventStreamId) {
				if added, ok := event.(*AddedAircraftEvent); ok {
					ac := added.ac
					v.send("$CQ", "SERVER", "FP", ac.Callsign)
					v.send("$CQ", ac.Callsign, "RN")
					v.send("$CQ", CQBroadcast, "WH", ac.Callsign)
					v.send("#SB", CQBroadcast, ac.Callsign, "PIR") // ???
				}
			}
		}
	}

	r := func(s *VATSIMMessageSpec) { vatsimMessageSpecs = append(vatsimMessageSpecs, s) }

	// #AA stacks on top of the public definition...
	r(NewMessageSpec("#AA", 7, func(v *VATSIMServer, sender string, args []string) error {
		v.send("$CQ", "SERVER", "ATC", sender) // ask server if it's legit
		v.send("$CQ", sender, "RN")
		return nil
	}))

	r(NewMessageSpec("$CQ::ATIS", 3, func(v *VATSIMServer, sender string, args []string) error {
		lines := strings.Split(positionConfig.ControllerATIS, "\n")
		for _, line := range lines {
			v.send("$CR", sender, "ATIS", "T", line)
		}
		v.send("$CR", sender, "ATIS", "E", fmt.Sprintf("%d", 1+len(lines)))
		return nil
	}))

	r(NewMessageSpec("$CQ::CAPS", 2, func(v *VATSIMServer, sender string, args []string) error {
		v.send("$CR", sender, "CAPS", "VERSION=1", "SECPOS=1", "ATCINFO=1", "ATCMULTI=1", "MODELDESC=1")
		return nil
	}))

	r(NewMessageSpec("$CQ::HLP", 4, func(v *VATSIMServer, sender string, args []string) error {
		// Convert help requests into text messages.
		tm := TextMessage{
			sender:      sender,
			contents:    "HELP: " + args[3],
			messageType: TextATC}

		eventStream.Post(&TextMessageEvent{message: &tm})

		return nil
	}))

	r(NewMessageSpec("$CQ::INF", 2, func(v *VATSIMServer, sender string, args []string) error {
		if myip == "" {
			// We haven't gotten this message back but should real soon now; wait to reply until we have it.
			pendingINFReplies = append(pendingINFReplies, sender)
		} else {
			sendINFReply(v, sender)
		}
		return nil
	}))

	r(NewMessageSpec("$CQ::IP", 3, func(v *VATSIMServer, sender string, args []string) error {
		if len(myip) > 0 {
			v.send("$CR", sender, "IP", myip)
		}
		return nil
	}))

	r(NewMessageSpec("$CQ::NOHLP", 3, func(v *VATSIMServer, sender string, args []string) error {
		// Just ignore it.
		return nil
	}))

	r(NewMessageSpec("$CQ::RN", 3, func(v *VATSIMServer, sender string, args []string) error {
		v.send("$CR", sender, "RN", globalConfig.VatsimName,
			database.sectorFileId, int(globalConfig.VatsimRating))
		return nil
	}))

	r(NewMessageSpec("$CQ::WH", 4, func(v *VATSIMServer, sender string, args []string) error {
		callsign := args[3]
		// Am I tracking the aircraft?
		//lg.Printf("WH: %s - %s", sender, strings.Join(args, ":"))
		ac := v.GetAircraft(callsign)
		if ac != nil && ac.TrackingController == v.callsign {
			v.send("#PC", sender, "CCP", "IH", callsign)

			v.send("#PC", sender, "CCP", "SC", callsign, ac.Scratchpad)
			if ac.Squawk != 0 {
				v.send("#PC", sender, "CCP", "BC", callsign, ac.Squawk)
			}
			if ac.VoiceCapability != VoiceUnknown {
				v.send("#PC", sender, "CCP", "VT", callsign, int(ac.VoiceCapability))
			}
			v.send("#PC", sender, "CCP", "TA", callsign, ac.TempAltitude)
		}
		return nil
	}))

	r(NewMessageSpec("$CR::ATC", 4, func(v *VATSIMServer, s string, args []string) error {
		if len(args) == 4 || args[4] == v.callsign {
			v.atcValid = args[3] == "Y"
			if !v.atcValid && positionConfig.VatsimFacility != FacilityOBS {
				ShowErrorDialog("Insufficient level to control specified position.")
			}
		}
		return nil
	}))

	r(NewMessageSpec("$CR::IP", 4, func(v *VATSIMServer, s string, args []string) error {
		myip = args[3]
		// Send any pending $CQ::INF replies now that we know our external IP.
		for _, sender := range pendingINFReplies {
			sendINFReply(v, sender)
		}
		pendingINFReplies = nil

		return nil
	}))

	r(NewMessageSpec("$DI", 4, handleDI))

	r(NewMessageSpec("$ER", 4, func(v *VATSIMServer, sender string, args []string) error {
		switch args[2] {
		case "001":
			ShowErrorDialog("VATSIM: callsign in use")
			v.Disconnect()
		case "002":
			ShowErrorDialog("VATSIM: invalid callsign")
			v.Disconnect()
		case "003":
			ShowErrorDialog("VATSIM: already registered")
			v.Disconnect()
		case "004":
			// syntax error in packet; our bad...
			lg.Printf("Server error %s: %s", args[2], args[3])
		case "005":
			// invalid source callsign; our bad...
			lg.Printf("Server error %s: %s", args[2], args[3])
		case "006":
			ShowErrorDialog("VATSIM: invalid CID or password")
			v.Disconnect()
		case "007":
			// no such callsign; our bad
			lg.Printf("Server error %s: %s", args[2], args[3])
		case "008":
			// No flight plan; ignore
		case "009":
			// no such weather profile; should inform the user
		case "010":
			ShowErrorDialog("VATSIM: invalid protocol revision")
			v.Disconnect()
		case "011":
			ShowErrorDialog("VATSIM: requested rating is too high")
			v.Disconnect()
		case "012":
			ShowErrorDialog("VATSIM: too many pilots connected")
			v.Disconnect()
		case "013":
			ShowErrorDialog("VATSIM: CID was suspended")
			v.Disconnect()
		case "014":
			ShowErrorDialog("VATSIM: not a valid control position")
			v.Disconnect()
		case "015":
			ShowErrorDialog("VATSIM: your rating is too low to control this position")
			v.Disconnect()
		default:
			lg.Printf("Server error %s: %s", args[2], args[3])
		}
		return nil
	}))

	r(NewMessageSpec("$FP", 17, func(v *VATSIMServer, sender string, args []string) error {
		v.send("#TM", "FP", sender+" GET") // ask for the squawk code as well
		return nil
	}))

	r(NewMessageSpec("#PC::CCP:ID", 4, func(v *VATSIMServer, sender string, args []string) error {
		// Are you a modern client?
		v.send("#PC", sender, "CCP", "DI")
		return nil
	}))

	r(NewMessageSpec("$PI", 3, func(v *VATSIMServer, sender string, args []string) error {
		if args[1] == "*" || args[1] == v.callsign {
			v.send("$PO", sender, args[2])
		}
		return nil
	}))

	r(NewMessageSpec("$ZC", 3, handleZC))

	r(NewMessageSpec("$!!", 2, handleBangBang))
}
