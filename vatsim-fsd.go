// vatsim.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

///////////////////////////////////////////////////////////////////////////
// VATSIMServer

// The details of the following implementation and its decoding of the
// VATSIM protocol are heavily due to the wonderful vatsimfsdparser package
// (https://github.com/Sequal32/vatsimfsdparser). (Too bad this wasn't
// written in rust!).  Also useful were the FSD Unofficial docs:
// https://fsd-doc.norrisng.ca/site/.

type MalformedMessageError struct {
	Err string
}

func (m MalformedMessageError) Error() string {
	return m.Err
}

type IgnoredMessageError struct {
	Err string
}

func (i IgnoredMessageError) Error() string {
	return i.Err
}

type UnknownMessageError struct {
	Err string
}

func (u UnknownMessageError) Error() string {
	return u.Err
}

type VATSIMMessage struct {
	contents string
	time     time.Time
}

type VATSIMServer struct {
	callsign    string
	client      ControlClient
	messageChan chan VATSIMMessage
	conn        *net.TCPConn
	windowTitle string

	lastMessageActualTime   time.Time
	lastMessageReportedTime time.Time
	timeRateMultiplier      float32
}

func NewVATSIMServer(callsign string, facility Facility, position *Position,
	client ControlClient, address string) (*VATSIMServer, error) {
	v := &VATSIMServer{callsign: callsign, client: client}
	v.messageChan = make(chan VATSIMMessage, 4096)

	if !strings.ContainsAny(address, ":") {
		address += ":6809"
	}

	raddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil, err
	}

	if v.conn, err = net.DialTCP("tcp", nil, raddr); err != nil {
		return nil, err
	}

	v.windowTitle = v.callsign + " " + v.conn.RemoteAddr().String()

	// v.conn.SetNoDelay(true)

	// As messages arrive, send them along the channel
	go func(v *VATSIMServer) {
		defer func() {
			if err := recover(); err != nil {
				lg.Errorf("Panic stack: %s", string(debug.Stack()))
				ShowErrorDialog("Error encountered while receiving VATSIM messages: %v", err)
			}
		}()

		r := bufio.NewReader(v.conn)
		for {
			if str, err := r.ReadString('\n'); err == nil {
				v.messageChan <- VATSIMMessage{contents: str, time: time.Now()}
			} else if err == io.EOF {
				lg.Printf("EOF--closing the channel etc")
				close(v.messageChan)
				return
			} else {
				lg.Printf("Exiting VATSIMServer goroutine: %v", err)
				return
			}
		}
	}(v)

	return v, nil
}

func NewVATSIMReplayServer(filename string, offsetSeconds int, replayRate float32,
	client ControlClient) (*VATSIMServer, error) {
	lg.Printf("Initializing VATSIMServer for replay of %s, offset %d, rate %f",
		filename, offsetSeconds, replayRate)

	v := &VATSIMServer{callsign: "NO_CALLSIGN", client: client}
	v.messageChan = make(chan VATSIMMessage, 4096)
	v.windowTitle = filename + " replay"

	var err error
	var f io.ReadCloser
	if f, err = os.Open(filename); err != nil {
		return nil, fmt.Errorf("%s: %s", filename, err)
	}

	decoder := json.NewDecoder(f)
	offset := -time.Duration(time.Duration(float32(offsetSeconds)/replayRate) * time.Second)
	replayStart := time.Now().Add(offset)

	lg.Printf("now %s, start %s", time.Now(), replayStart)

	// Same structure definition as is used to serialize in vsniff.
	type Message struct {
		Time     time.Time
		Sent     bool
		Contents string
	}

	var next Message
	if err = decoder.Decode(&next); err != nil {
		return nil, fmt.Errorf("%s: error decoding initial message: %w", filename, err)
	}

	// Serve up the messages
	go func() {
		defer func() {
			if err := recover(); err != nil {
				lg.Errorf("Panic stack: %s", string(debug.Stack()))
				ShowErrorDialog("Error encountered during VATSIM replay: %v", err)
			}
		}()

		streamStart := next.Time
		for {
			replayDuration := time.Duration(float64(time.Since(replayStart)) * float64(replayRate))
			nextMessageDuration := next.Time.Sub(streamStart)

			if replayDuration > nextMessageDuration {
				// Don't send messages that the client originally sent
				if !next.Sent {
					msg := strings.TrimSpace(next.Contents) + "\n"
					v.messageChan <- VATSIMMessage{contents: msg, time: next.Time}
				}

				if err := decoder.Decode(&next); err == io.EOF {
					lg.Printf("%s: Reached EOF", filename)
					f.Close()
					time.Sleep(24 * time.Hour)
				} else if err != nil {
					lg.Errorf("%s: %v", filename, err)
				}
			} else {
				sleepDuration := nextMessageDuration - replayDuration - 3*time.Millisecond
				if sleepDuration < time.Millisecond {
					sleepDuration = time.Millisecond
				}
				time.Sleep(sleepDuration)
			}
		}
	}()

	return v, nil
}

func (v *VATSIMServer) SetSquawk(callsign string, squawk Squawk) {
}

func (v *VATSIMServer) SetScratchpad(callsign string, scratchpad string) {
}

func (v *VATSIMServer) SetRoute(callsign string, route string) {
}

func (v *VATSIMServer) SetDeparture(callsign string, airport string) {
}

func (v *VATSIMServer) SetArrival(callsign string, airport string) {
}

func (v *VATSIMServer) SetAltitude(callsign string, alt int) {
}

func (v *VATSIMServer) SetTemporaryAltitude(callsign string, alt int) {
}

func (v *VATSIMServer) SetAircraftType(callsign string, ac string) {
}

func (v *VATSIMServer) SetFlightRules(callsign string, r FlightRules) {
}

func (v *VATSIMServer) PushFlightStrip(callsign string, controller string) {
}

func (v *VATSIMServer) InitiateTrack(callsign string) {
}

func (v *VATSIMServer) Handoff(callsign string, controller string) {
}

func (v *VATSIMServer) AcceptHandoff(callsign string) {
}

func (v *VATSIMServer) RejectHandoff(callsign string) {
}

func (v *VATSIMServer) DropTrack(callsign string) {
}

func (v *VATSIMServer) PointOut(callsign string, controller string) {
}

func (v *VATSIMServer) SendTextMessage(m TextMessage) {
}

func (v *VATSIMServer) Disconnect() {
	// Is this legit? (With a goroutine using it concurrently?) It
	//v.conn.Close()
	v.windowTitle = "[Disconnected]"
}

func (v *VATSIMServer) CurrentTime() time.Time {
	// Elapsed time in seconds (actual) since we last received a message
	ds := time.Since(v.lastMessageActualTime).Seconds()
	ds *= float64(v.timeRateMultiplier)
	s := time.Duration(ds * float64(time.Second))
	return v.lastMessageReportedTime.Add(s)
}

func (v *VATSIMServer) Description() string {
	return "VATSIM"
}

func (v *VATSIMServer) GetWindowTitle() string {
	return v.windowTitle
}

func (v *VATSIMServer) GetUpdates() {
	// Receive messages here; this runs in the same thread as the GUI et
	// al., so there's nothing to worry about w.r.t. races.
	for {
		select {
		case msg, ok := <-v.messageChan:
			if !ok {
				// No more messages
				return
			}

			v.lastMessageActualTime = time.Now()
			v.lastMessageReportedTime = msg.time

			// TODO: do we need to handle quoted ':'s?
			strs := strings.Split(strings.TrimSpace(msg.contents), ":")
			if len(strs) == 0 {
				lg.Errorf("vatsim: empty message received?")
				break
			}
			if len(strs[0]) == 0 {
				lg.Errorf("vatsim: empty first field? \"%s\"", msg.contents)
				break
			}

			var err error
			switch strs[0][0] {
			case '@':
				err = v.handleAt(strs)

			case '%':
				err = v.handlePct(strs)

			default:
				if len(strs[0]) < 3 {
					err = MalformedMessageError{}
				} else {
					cmd := strs[0][:3]
					sender := strings.ToUpper(strs[0][3:])
					args := strs[1:]
					switch cmd {
					case "#AA":
						err = v.handleAA(sender, args)
					case "#AP":
						err = v.handleAP(sender, args)
					case "$AR":
						err = v.handleAR(sender, args)
					case "$CQ":
						err = v.handleCQ(sender, args)
					case "$CR":
						err = v.handleCR(sender, args)
					case "#DA":
						err = v.handleDA(sender, args)
					case "$DI":
						err = v.handleDI(sender, args)
					case "#DL":
						err = v.handleDL(sender, args)
					case "#DP":
						err = v.handleDP(sender, args)
					case "$ER":
						err = v.handleER(sender, args)
					case "$FP":
						err = v.handleFP(sender, args)
					case "$HA":
						err = v.handleHA(sender, args)
					case "$HO":
						err = v.handleHO(sender, args)
					case "#PC":
						err = v.handlePC(sender, args)
					case "#SB":
						err = v.handleSB(sender, args)
					case "#SL":
						err = v.handleSL(sender, args)
					case "#ST":
						err = v.handleST(sender, args)
					case "#TM":
						err = v.handleTM(sender, args)
					case "$ZC":
						err = v.handleZC(sender, args)
					case "$ZR":
						err = v.handleZR(sender, args)
					default:
						err = UnknownMessageError{cmd}
					}
				}
			}

			if err != nil {
				if _, ok := err.(IgnoredMessageError); ok {
					// don't log it...
				} else {
					lg.Printf("FSD message error: %T: %s: %s", err, err, msg.contents)
				}
			}

		default:
			// Nothing more on the channel
			return
		}
	}
}

// variadic args are (int field, string expected contents)
func checkArgs(args []string, count int, checks ...interface{}) error {
	if len(args) != count {
		return MalformedMessageError{"Insufficient arguments"}
	}
	if len(checks)%2 != 0 {
		lg.ErrorfUp1("Malformed checks passed to checkArgs")
		return nil
	}
	for i := 0; i < len(checks); i += 2 {
		if offset, ok := checks[i].(int); !ok {
			lg.ErrorfUp1("Expected integer for %dth check", i)
		} else {
			if str, ok := checks[i+1].(string); !ok {
				lg.ErrorfUp1("Expected string for %dth check", i+1)
			} else {
				if args[offset] != str {
					msg := fmt.Sprintf("Expected \"%s\" for argument %d; got \"%s\"", str, offset, args[offset])
					return MalformedMessageError{msg}
				}
			}
		}
	}
	return nil
}

// Add ATC
func (v *VATSIMServer) handleAA(position string, args []string) error {
	if err := checkArgs(args, 6, 0, "SERVER"); err != nil {
		return err
	}

	rating, err := parseRating(args[4])
	if err != nil {
		return err
	}

	v.client.ControllerAdded(Controller{
		callsign: position,
		name:     args[1],
		cid:      args[2],
		rating:   rating})

	return nil
}

// Add Pilot
// #AP(callsign):SERVER:(cid):(password):(rating):(protocol version):(simtype):(full name ICAO)
func (v *VATSIMServer) handleAP(callsign string, args []string) error {
	if err := checkArgs(args, 7, 0, "SERVER", 2, ""); err != nil {
		return err
	}

	rating, err := parseRating(args[3])
	if err != nil {
		return err
	}

	// We're ignoring things like protocol version (args[4]) and simulator
	// type (args[5]) here...
	v.client.PilotAdded(Pilot{
		callsign: callsign,
		cid:      args[1],
		name:     args[6],
		rating:   rating})

	return nil
}

// METAR response
// $ARserver:ABE_APP:METAR:KABE 232251Z 24006KT ...
func (v *VATSIMServer) handleAR(sender string, args []string) error {
	if sender != "SERVER" {
		return IgnoredMessageError{"Ignoring $AR message not from server"}
	}
	if err := checkArgs(args, 3, 1, "METAR"); err != nil {
		return err
	}

	fields := strings.Fields(args[2])
	if len(fields) < 3 {
		return MalformedMessageError{"Expected >= 3 fields"}
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
		lg.Errorf("Expecting RMK where %s is in METAR \"%s\"", s, args[2])
	} else {
		for s != "" {
			s = next()
			m.rmk += s + " "
		}
		m.rmk = strings.TrimRight(m.rmk, " ")
	}

	v.client.METARReceived(m)
	return nil
}

// Client info request
func (v *VATSIMServer) handleCQ(sender string, args []string) error {
	if len(args) < 2 {
		return MalformedMessageError{"Insufficient arguments"}
	}

	cmd := args[1]
	switch cmd {
	case "ACC":
		// aircraft configuration (for model matching)
		return IgnoredMessageError{"$CQ::ACC"}

	case "ATC":
		// Is valid ATC?
		return IgnoredMessageError{"$CQ::ATC"}

	case "ATIS":
		// ATIS
		return IgnoredMessageError{"$CQ::ATIS"}

	case "BC":
		// set beacon code
		return v.assignSquawk(args, 2, 3)

	case "BY":
		// Request relief -- actually this is .break
		v.client.RequestRelief(sender)
		return nil

	case "CAPS":
		// Capabilities
		return IgnoredMessageError{"$CQ::CAPS"}

	case "C?":
		// COM1Freq
		return IgnoredMessageError{"$CQ::C?"}

	case "DR":
		// drop track
		if len(args) != 3 {
			return MalformedMessageError{"Insufficient arguments"}
		} else {
			callsign := args[2]
			v.client.TrackDropped(callsign, sender)
			return nil
		}

	case "EST":
		// estimated arrival time????
		// $CQMMUN_APP:@94835:EST:NKS870:KDFW:0:2:2022-08-14T16:32:33.5663139Z
		return IgnoredMessageError{"$CQ::EST"}

	case "FA":
		// set final altitude
		return v.assignAltitude(args, 2, 3)

	case "FP":
		// flight plan
		return IgnoredMessageError{"$CQ::" + cmd}

	case "HI":
		v.client.CancelRequestRelief(sender)
		return nil

	case "HLP":
		// request help
		return IgnoredMessageError{"$CQ::" + cmd}

	case "HT":
		// accept handoff
		// handoff track: sender, args[3] is receiving?
		if len(args) != 4 {
			return MalformedMessageError{"Insufficient arguments"}
		} else {
			callsign := args[2]
			to := args[3]
			v.client.HandoffAccepted(callsign, sender, to)
			return nil
		}

	case "INF":
		// client system information
		return IgnoredMessageError{"$CQ::INF"}

	case "IP":
		// public IP (WTH?)
		return IgnoredMessageError{"$CQ::IP"}

	case "IPC":
		// not seen but included in vatsimfsdparser handling
		return IgnoredMessageError{"$CQ::IPC"}

	case "IT":
		// initiate track
		if len(args) != 3 {
			return MalformedMessageError{"Insufficient arguments"}
		} else {
			// args[0] seems to always be @94835
			callsign := args[2]
			v.client.TrackInitiated(callsign, sender)
			return nil
		}

	case "NEWATIS":
		switch len(args) {
		case 4:
			// I think this is the expected way
			// $CQKEKY_ATIS:@94835:NEWATIS:ATIS A:  VRB05KT - A3006
			letter := args[2][len(args[2])-1]
			v.client.ATISReceived(sender, letter, args[3])
			return nil

		case 3:
			// But occasionally it comes in like this
			// CQKORF_ATIS:@94835:NEWATIS:ATIS D - wind 07009KT alt 3000
			if len(args[2]) < 8 {
				return MalformedMessageError{"Insufficient ATIS text" + args[2]}
			}
			letter := args[2][5]
			v.client.ATISReceived(sender, letter, args[2][8:])
			return nil

		default:
			return MalformedMessageError{"Insufficient arguments"}
		}

	case "NEWINFO":
		return IgnoredMessageError{"$CQ::NEWINFO"}

	case "NOHLP":
		// cancel request help
		return IgnoredMessageError{"$CQ::" + cmd}

	case "RN":
		// real name
		// TODO: this is another controller or a/c requesting our real
		// name; need to send a response with it...
		if len(args) != 2 {
			return MalformedMessageError{"Insufficient arguments"}
		}
		return IgnoredMessageError{"$CQ::RN"}

	case "SC":
		// set scratchpad
		return v.setScratchpad(args, 2, 3)

	case "SV": // server
		return IgnoredMessageError{"$CQ::SV"}

	case "TA":
		// set temp aptitude
		return v.assignTemporaryAltitude(args, 2, 3)

	case "VT":
		// set voice type
		return v.setVoiceType(args, 2, 3)

	case "WH":
		// who has
		return IgnoredMessageError{"$CQ::WH"}

	default:
		return UnknownMessageError{"$CQ::" + cmd}
	}
}

// client response
func (v *VATSIMServer) handleCR(sender string, args []string) error {
	if len(args) < 2 {
		return MalformedMessageError{"Insufficient number of fields"}
	}

	cmd := args[1]
	// vatsimfsdparser suggests that all of the CQ messages can also appear
	// as CR, though the following are the only ones we've seen in the
	// wild.
	switch cmd {
	case "ATC":
		// Validate ATC
		// We get e.g. "ATC:N:DAL123" and "ATC:Y:JFK_APP" but its unclear
		// what this is telling us..
		return IgnoredMessageError{"$CR::ATC"}

	case "ATIS":
		return IgnoredMessageError{"$CR::ATIS"}

	case "CAPS":
		// capabilities...
		return IgnoredMessageError{"$CR::CAPS"}

	case "IP":
		// "here's my IP address," apparently
		return IgnoredMessageError{"$CR::IP"}

	case "RN":
		if len(args) != 5 {
			return MalformedMessageError{"Insufficient arguments"}
		}
		rating, err := parseRating(args[4])
		if err != nil {
			return err
		}
		v.client.UserAdded(sender, User{name: args[2], note: args[3], rating: rating})
		return nil

	default:
		return UnknownMessageError{"$CR::" + cmd}
	}
}

// Disconnect ATC
func (v *VATSIMServer) handleDA(position string, args []string) error {
	if err := checkArgs(args, 1); err != nil {
		return err
	}

	// cid := args[0]
	v.client.ControllerRemoved(position)

	return nil
}

// Login stuff
func (v *VATSIMServer) handleDI(sender string, args []string) error {
	return IgnoredMessageError{"$DI"}
}

// ??
func (v *VATSIMServer) handleDL(sender string, args []string) error {
	return IgnoredMessageError{"$DL"}
}

// Disconnect Pilot
func (v *VATSIMServer) handleDP(callsign string, args []string) error {
	if err := checkArgs(args, 1); err != nil {
		return err
	}

	// cid := args[0]
	v.client.PilotRemoved(callsign)

	return nil
}

// ???
func (v *VATSIMServer) handleER(sender string, args []string) error {
	return IgnoredMessageError{"$ER"}
}

// Flightplan
func (v *VATSIMServer) handleFP(sender string, args []string) error {
	if len(args) < 16 {
		return MalformedMessageError{"Insufficient arguments"}
	}

	var fp FlightPlan

	fp.callsign = sender
	switch args[1] {
	case "I":
		fp.rules = IFR
	case "V":
		fp.rules = VFR
	case "D":
		fp.rules = DVFR
	case "S":
		fp.rules = SVFR
	default:
		return MalformedMessageError{"Unexpected flight rules: " + args[1]}
	}

	fp.actype = args[2]

	if gs, err := strconv.Atoi(args[3]); err != nil {
		return MalformedMessageError{"Unable to parse groundspeed: " + args[3]}
	} else {
		fp.groundspeed = gs
	}

	fp.depart = args[4]
	// args[5]: departure time
	// args[6]: actual departure time
	fp.arrive = args[8]

	if strings.HasPrefix(strings.ToUpper(args[7]), "FL") {
		if alt, err := strconv.Atoi(args[7][2:]); err != nil {
			return MalformedMessageError{"Unable to parse altitude: " + args[7]}
		} else {
			fp.altitude = alt * 100
		}
	} else if alt, err := strconv.Atoi(args[7]); err != nil {
		return MalformedMessageError{"Unable to parse altitude: " + args[7]}
	} else {
		fp.altitude = alt
	}

	// args[9]: hours enroute
	// args[10]: minutes enroute
	// args[11]: fuel available hours
	// args[12]: fuel available minutes
	fp.alternate = args[13]
	fp.remarks = args[14]
	fp.route = args[15]
	fp.filed = v.CurrentTime()
	// TODO? amended by

	v.client.FlightPlanReceived(fp)

	return nil
}

// Accepted handoff: TWR accepted N11TV from APP
// $HAABE_TWR:ABE_APP:N11TV
func (v *VATSIMServer) handleHA(sender string, args []string) error {
	if len(args) != 2 {
		return MalformedMessageError{"Incorrect number of arguments"}
	}

	receiver := args[0]
	callsign := args[1]
	v.client.HandoffAccepted(callsign, sender, receiver)

	return nil
}

// Handoff request: DEP wants to hand FDX off to APP
// $HOABE_DEP:ABE_APP:FDX901
func (v *VATSIMServer) handleHO(sender string, args []string) error {
	if len(args) != 2 {
		return MalformedMessageError{"Incorrect number of arguments"}
	}

	receiver := args[0]
	callsign := args[1]
	v.client.HandoffRequested(callsign, sender, receiver)

	return nil
}

func (v *VATSIMServer) handlePC(sender string, args []string) error {
	if len(args) < 3 {
		return MalformedMessageError{"Insufficient number of arguments"}
	}

	cmd := args[2]
	switch cmd {
	case "BC": // Beacon code
		return v.assignSquawk(args, 3, 4)

	case "DI":
		// ??? request to us from another controller
		return IgnoredMessageError{"#PC::DI"}

	case "DP":
		// push to departures; have not seen this in the wild
		return IgnoredMessageError{"#PC::DP"}

	case "FA":
		return v.assignAltitude(args, 3, 4)

	case "HC":
		// handoff cancelled?
		// in sent messages, accepting a handoff?
		if len(args) != 4 {
			return MalformedMessageError{"Insufficient number of arguments"}
		}
		callsign := args[3]
		receiver := args[0]
		v.client.HandoffAccepted(callsign, sender, receiver)
		return nil

	case "ID":
		// informs us e.g. of the existence of a controller
		return IgnoredMessageError{"#PC::ID"}

	case "IH":
		// I have control
		// ??? another controller and a callsign...
		return IgnoredMessageError{"#PC::IH"}

	case "PT":
		// point out; untested
		if len(args) < 4 {
			return MalformedMessageError{"Insufficient number of arguments"}
		}
		to := args[1]
		callsign := args[3]
		v.client.PointOutReceived(callsign, sender, to)
		return nil

	case "SC":
		return v.setScratchpad(args, 3, 4)

	case "ST":
		// flight strip
		if len(args) < 4 {
			return MalformedMessageError{"Insufficient number of arguments"}
		}
		to := args[0]

		fs := FlightStrip{callsign: args[3]}
		if len(args) >= 5 {
			fs.formatId = args[4]
		}
		if len(args) >= 6 {
			fs.annotations = append(fs.annotations, args[5:]...)
		}
		v.client.FlightStripPushed(sender, to, fs)
		return nil

	case "TA":
		return v.assignTemporaryAltitude(args, 3, 4)

	case "VT":
		return v.setVoiceType(args, 3, 4)

	default:
		return UnknownMessageError{"#PC:" + cmd}
	}
}

// Plane information (model matching?)
// PIR: plane information request
// #SBABE_APP:ACA752:PIR
// PI plane information
// #SBACA752:ABE_APP:PI:GEN:EQUIPMENT=B38M:AIRLINE=ACA
func (v *VATSIMServer) handleSB(sender string, args []string) error {
	return IgnoredMessageError{"#SB"}
}

// ? Another type of position report. Seems to immediately preceed @
// position reports when it's present, though doesn't proceed all @'s, if
// an #ST has..
//
// #SLDAL264:40.6703200:-73.6121000:11082.20:11040.81:4269785380:78.0420:-0.7916:163.8597:0.0010:0.0005:0.0002:0.00
func (v *VATSIMServer) handleSL(sender string, args []string) error {
	return IgnoredMessageError{"#SL"}
}

// ? Another type of position report. Comes at a relatively high frequency
// when present (~5 times per second), and also has a few more digits of
// precision, so presumably this is VATSIM velocity?  (For control
// purposes, it's not clear that we should be paying attention to this.)
//
// #STAFL841:39.6686200:-74.6358600:26118.76:26053.72:4278192640:0.00
// #ST(callsign):(lat):(long):(alt?):(pressurealt?):(flags--heading,etc?):
func (v *VATSIMServer) handleST(sender string, args []string) error {
	return IgnoredMessageError{"#ST"}
}

// Text message
func (v *VATSIMServer) handleTM(sender string, args []string) error {
	// #TM(from):(frequency):(message)
	// frequency:
	// *           broadcast
	// *S          wallop
	// @49999      ATC
	// @[freq]     frequency
	// (otherwise) private message
	freq := args[0]
	tm := TextMessage{sender: sender, contents: strings.Join(args[1:], ":")}
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

	v.client.TextMessageReceived(sender, tm)

	return nil
}

// ?? Auth?
func (v *VATSIMServer) handleZC(sender string, args []string) error {
	return IgnoredMessageError{"$ZC"}
}

// ?? Auth
func (v *VATSIMServer) handleZR(sender string, args []string) error {
	return IgnoredMessageError{"$ZR"}
}

// Aircraft update
// @(mode):(callsign):(squawk):(rating):(lat):(lon):(alt):(groundspeed):(num1):(num2)
func (v *VATSIMServer) handleAt(args []string) error {
	if len(args) != 10 {
		return MalformedMessageError{"Insufficient arguments"}
	}

	callsign := args[1]
	var mode TransponderMode
	switch args[0][1] {
	case 'S':
		mode = Standby
	case 'N':
		mode = Charlie
	case 'Y':
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

	pos := RadarTrack{
		position:    latlong,
		altitude:    int(altitude),
		groundspeed: int(groundspeed),
		heading:     heading,
		time:        v.CurrentTime()}

	v.client.PositionReceived(callsign, pos, squawk, mode)

	return nil
}

// ATC update
// %JFK_TWR:19100:4:20:5:40.63944:-73.76639:0
// (callsign):(frequency):(facility):(range):(rating):(lat):(long):(???? always 0 ????)
func (v *VATSIMServer) handlePct(args []string) error {
	if len(args) != 8 {
		return MalformedMessageError{"Insufficient arguments"}
	}

	callsign := args[0][1:]
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

	controller := Controller{
		callsign:   callsign,
		facility:   Facility(facility),
		frequency:  frequency,
		scopeRange: scopeRange,
		rating:     rating,
		location:   latlong}

	v.client.ControllerAdded(controller)

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
		return Frequency(100 + float32(frequency)/1000.), nil
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

func (v *VATSIMServer) assignAltitude(strs []string, csIndex int, altIndex int) error {
	if csIndex >= len(strs) || altIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to specify altitude"}
	}

	callsign := strs[csIndex]
	if alt, err := strconv.Atoi(strs[altIndex]); err != nil {
		return MalformedMessageError{"invalid altitude: " + strs[altIndex]}
	} else {
		v.client.AltitudeAssigned(callsign, alt)
		return nil
	}
}

func (v *VATSIMServer) assignTemporaryAltitude(strs []string, csIndex int, altIndex int) error {
	if csIndex >= len(strs) || altIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to specify temporary altitude"}
	}

	callsign := strs[csIndex]
	if alt, err := strconv.Atoi(strs[altIndex]); err != nil {
		return MalformedMessageError{"invalid temporary altitude: " + strs[altIndex]}
	} else {
		v.client.TemporaryAltitudeAssigned(callsign, alt)
		return nil
	}
}

func (v *VATSIMServer) assignSquawk(strs []string, csIndex int, sqIndex int) error {
	if csIndex >= len(strs) || sqIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments for squawk assignment"}
	}

	callsign := strs[csIndex]
	squawk, err := ParseSquawk(strs[sqIndex])
	if err != nil {
		return err
	}
	v.client.SquawkAssigned(callsign, squawk)
	return nil
}

func (v *VATSIMServer) setScratchpad(strs []string, csIndex int, spIndex int) error {
	if csIndex >= len(strs) || spIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to set scratchpad"}
	}

	callsign := strs[csIndex]
	v.client.ScratchpadSet(callsign, strs[spIndex])
	return nil
}

func (v *VATSIMServer) setVoiceType(strs []string, csIndex int, vtIndex int) error {
	if csIndex >= len(strs) || vtIndex >= len(strs) {
		return MalformedMessageError{"insufficient arguments to set voice type"}
	}

	callsign := strs[csIndex]
	var vc VoiceCapability
	switch strs[vtIndex] {
	case "v":
		vc = Voice
	case "r":
		vc = Receive
	case "t":
		vc = Text
	default:
		return MalformedMessageError{"Unexpected voice capability: " + strs[vtIndex]}
	}

	v.client.VoiceSet(callsign, vc)
	return nil
}
