// pkg/client/client.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"encoding/gob"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	av "github.com/mmp/vice/pkg/aviation"
	"github.com/mmp/vice/pkg/log"
	"github.com/mmp/vice/pkg/math"
	"github.com/mmp/vice/pkg/platform"
	"github.com/mmp/vice/pkg/server"
	"github.com/mmp/vice/pkg/sim"
	"github.com/mmp/vice/pkg/util"

	"github.com/gorilla/websocket"
)

type ControlClient struct {
	controllerToken string
	client          *RPCClient

	speechWs                 *websocket.Conn
	speechCh                 chan sim.PilotSpeech
	bufferedSpeech           []sim.PilotSpeech
	playingSpeech            bool
	holdSpeech               bool
	lastSpeechHoldTime       time.Time
	awaitReadbackCallsign    av.ADSBCallsign
	lastTransmissionCallsign av.ADSBCallsign

	lg *log.Logger
	mu sync.Mutex

	lastUpdateRequest time.Time
	lastReturnedTime  time.Time
	updateCall        *pendingCall
	lastUpdateLatency time.Duration

	pendingCalls []*pendingCall

	SessionStats SessionStats

	// This is all read-only data that we expect other parts of the system
	// to access directly.
	State sim.State
}

// This is the client-side representation of a server (perhaps could be better-named...)
type Server struct {
	*RPCClient

	HaveTTS bool

	name        string
	configs     map[string]map[string]*server.Configuration
	runningSims map[string]*server.RemoteSim
}

type SessionStats struct {
	Departures    int
	Arrivals      int
	IntraFacility int
	Overflights   int

	SignOnTime time.Time

	seenCallsigns map[av.ADSBCallsign]interface{}
}

func (s *SessionStats) Update(ss *sim.State) {
	for _, trk := range ss.Tracks {
		if fp := trk.FlightPlan; fp != nil {
			if fp.TrackingController != ss.UserTCP {
				continue // not ours
			}
			if _, ok := s.seenCallsigns[trk.ADSBCallsign]; ok {
				continue // seen it already
			}
			s.seenCallsigns[trk.ADSBCallsign] = nil
			if trk.IsDeparture() {
				s.Departures++
			} else if trk.IsArrival() {
				s.Arrivals++
			} else if trk.IsOverflight() {
				s.Overflights++
			}
		}
	}
}

func (c *ControlClient) RPCClient() *RPCClient {
	return c.client
}

type RPCClient struct {
	*rpc.Client
}

func debuggerIsRunning() bool {
	dlv, ok := os.LookupEnv("_")
	return ok && strings.HasSuffix(dlv, "/dlv")
}

func (c *RPCClient) callWithTimeout(serviceMethod string, args any, reply any) error {
	pc := &pendingCall{
		Call:      c.Go(serviceMethod, args, reply, nil),
		IssueTime: time.Now(),
	}

	for {
		select {
		case <-pc.Call.Done:
			return pc.Call.Error

		case <-time.After(5 * time.Second):
			if !debuggerIsRunning() {
				return server.ErrRPCTimeout
			}
		}
	}
}

type pendingCall struct {
	Call      *rpc.Call
	IssueTime time.Time
	Callback  func(*sim.EventStream, *sim.State, error)
}

func makeRPCCall(call *rpc.Call, callback func(error)) *pendingCall {
	return &pendingCall{
		Call:      call,
		IssueTime: time.Now(),
		Callback: func(es *sim.EventStream, state *sim.State, err error) {
			if callback != nil {
				callback(err)
			}
		},
	}
}

func makeStateUpdateRPCCall(call *rpc.Call, update *sim.StateUpdate, callback func(error)) *pendingCall {
	return &pendingCall{
		Call:      call,
		IssueTime: time.Now(),
		Callback: func(es *sim.EventStream, state *sim.State, err error) {
			if err == nil {
				update.Apply(state, es)
			}
			if callback != nil {
				callback(err)
			}
		},
	}
}

func (p *pendingCall) CheckFinished(es *sim.EventStream, state *sim.State) bool {
	select {
	case c := <-p.Call.Done:
		if p.Callback != nil {
			p.Callback(es, state, c.Error)
		}
		return true

	default:
		return false
	}
}

func NewControlClient(ss sim.State, controllerToken string, wsURL string, client *RPCClient, lg *log.Logger) *ControlClient {
	cc := &ControlClient{
		controllerToken:   controllerToken,
		client:            client,
		lg:                lg,
		lastUpdateRequest: time.Now(),
		State:             ss,
	}

	if wsURL != "" {
		cc.speechWs, cc.speechCh = initializeSpeechWebsocket(controllerToken, wsURL, lg)
	}

	cc.SessionStats.SignOnTime = ss.SimTime
	cc.SessionStats.seenCallsigns = make(map[av.ADSBCallsign]interface{})
	return cc
}

func initializeSpeechWebsocket(controllerToken string, wsURL string, lg *log.Logger) (*websocket.Conn, chan sim.PilotSpeech) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+controllerToken)

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+wsURL+"/speech", header)
	if err != nil {
		lg.Errorf("speech websocket: %v", err)
		return nil, nil
	}

	speechCh := make(chan sim.PilotSpeech)
	go func() {
		for {
			ty, r, err := conn.NextReader()
			if err != nil {
				var cerr *websocket.CloseError
				if errors.As(err, &cerr) {
					// all good, we're shutting down
					lg.Errorf("websocket closed; exiting client reader")
				} else {
					lg.Errorf("speech websocket read: %T, %v", err, err)
				}
				return
			}
			if ty != websocket.BinaryMessage {
				lg.Errorf("expected binary message, got %d", ty)
				continue
			}

			gr := gob.NewDecoder(r)
			var ps sim.PilotSpeech
			if err := gr.Decode(&ps); err != nil {
				lg.Errorf("PilotSpeech: %v", err)
				continue
			}

			speechCh <- ps
		}
	}()

	return conn, speechCh
}

func (c *ControlClient) Status() string {
	if c == nil {
		return "[disconnected]"
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.State.SimDescription == "" {
		return "[disconnected]"
	} else {
		stats := c.SessionStats
		deparr := fmt.Sprintf(" [ %d departures %d arrivals %d intrafacility %d overflights ]",
			stats.Departures, stats.Arrivals, stats.IntraFacility, stats.Overflights)
		return c.State.UserTCP + c.State.SimDescription + deparr
	}
}

func (c *ControlClient) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.client.callWithTimeout("Sim.SignOff", c.controllerToken, nil); err != nil {
		c.lg.Errorf("Error signing off from sim: %v", err)
	}
	if c.speechWs != nil {
		c.speechWs.Close()
		c.speechWs = nil
	}
	c.State.Tracks = nil
	c.State.UnassociatedFlightPlans = nil
	c.State.Controllers = nil
}

func (c *ControlClient) addCall(pc *pendingCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingCalls = append(c.pendingCalls, pc)
}

func (c *ControlClient) ControllerAirspace(id string) []av.ControllerAirspaceVolume {
	var vols []av.ControllerAirspaceVolume
	for _, pos := range c.State.GetConsolidatedPositions(id) {
		for _, sub := range util.SortedMapKeys(c.State.Airspace[pos]) {
			vols = append(vols, c.State.Airspace[pos][sub]...)
		}
	}
	return vols
}

func (c *ControlClient) GetUpdates(eventStream *sim.EventStream, p platform.Platform, onErr func(error)) {
	if c.client == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.updateCall != nil {
		if c.updateCall.CheckFinished(eventStream, &c.State) {
			c.updateCall = nil
			c.SessionStats.Update(&c.State)
			return
		}
		checkTimeout(c.updateCall, eventStream, onErr)
	}

	c.updateSpeech(p)

	c.checkPendingRPCs(eventStream, onErr)

	// Wait in seconds between update fetches; no less than 50ms
	rate := math.Clamp(1/c.State.SimRate, 0.05, 1)
	if d := time.Since(c.lastUpdateRequest); d > time.Duration(rate*float32(time.Second)) {
		if c.updateCall != nil {
			c.lg.Warnf("GetUpdates still waiting for %s on last update call", d)
			return
		}
		c.lastUpdateRequest = time.Now()

		var update sim.StateUpdate
		c.updateCall = makeStateUpdateRPCCall(c.client.Go("Sim.GetStateUpdate", c.controllerToken, &update, nil), &update,
			func(err error) {
				d := time.Since(c.updateCall.IssueTime)
				c.lastUpdateLatency = d
				if d > 250*time.Millisecond {
					c.lg.Warnf("Slow world update response %s", d)
				} else {
					c.lg.Debugf("World update response time %s", d)
				}
			})
	}
}

func (c *ControlClient) updateSpeech(p platform.Platform) {
	// See if anything new has arrived from the server
loop:
	for {
		select {
		case ps, ok := <-c.speechCh:
			if ok {
				//fmt.Printf("got speech %s\n", ps.Callsign)
				c.bufferedSpeech = append(c.bufferedSpeech, ps)
			}
		default:
			break loop
		}
	}

	if c.holdSpeech && time.Now().After(c.lastSpeechHoldTime) {
		// Time out user-requested holds after 5 seconds (if another
		// request hasn't kept the request alive).
		c.holdSpeech = false
	}

	if c.holdSpeech || c.playingSpeech || len(c.bufferedSpeech) == 0 || c.State.Paused {
		// Don't/can't kick off additional speech playback
		return
	}

	// Time to play speech
	if c.awaitReadbackCallsign != "" {
		// ; prioritize responses from issued aircraft instructions over cold calls.
		isResponse := func(ps sim.PilotSpeech) bool { return ps.Callsign == c.awaitReadbackCallsign }
		if idx := slices.IndexFunc(c.bufferedSpeech, isResponse); idx != -1 {
			bs := c.bufferedSpeech[idx]
			if err := p.TryEnqueueSpeechMP3(bs.MP3, func() {
				c.awaitReadbackCallsign = ""
				c.playingSpeech = false
				c.holdSpeech = true
				c.lastTransmissionCallsign = bs.Callsign
				//fmt.Printf("play completed %s @ %s\n", bs.Callsign, time.Now().String())
				c.lastSpeechHoldTime = time.Now().Add(3 * time.Second / 2)
			}); err == nil {
				//fmt.Printf("play awaited speech %s at %s\n", bs.Callsign, time.Now().String())
				c.bufferedSpeech = append(c.bufferedSpeech[:idx], c.bufferedSpeech[idx+1:]...)
				c.playingSpeech = true
			}
		}
	} else {
		bs := c.bufferedSpeech[0]
		if err := p.TryEnqueueSpeechMP3(bs.MP3, func() {
			c.playingSpeech = false
			c.holdSpeech = true
			//fmt.Printf("play completed %s at %s\n", bs.Callsign, time.Now().String())
			c.lastTransmissionCallsign = bs.Callsign
			c.lastSpeechHoldTime = time.Now().Add(2 * time.Second)
		}); err == nil {
			//fmt.Printf("play random speech %s at %s\n", bs.Callsign, time.Now().String())
			c.bufferedSpeech = c.bufferedSpeech[1:]
			c.playingSpeech = true
		}
	}
}

func (c *ControlClient) checkPendingRPCs(eventStream *sim.EventStream, onErr func(error)) {
	c.pendingCalls = slices.DeleteFunc(c.pendingCalls,
		func(call *pendingCall) bool { return call.CheckFinished(eventStream, &c.State) })

	for _, call := range c.pendingCalls {
		if checkTimeout(call, eventStream, onErr) {
			break
		}
	}
}

func checkTimeout(call *pendingCall, eventStream *sim.EventStream, onErr func(error)) bool {
	if time.Since(call.IssueTime) > 5*time.Second && !debuggerIsRunning() {
		eventStream.Post(sim.Event{
			Type:        sim.StatusMessageEvent,
			WrittenText: "No response from server for over 5 seconds. Network connection may be lost.",
		})
		if onErr != nil {
			onErr(server.ErrRPCTimeout)
		}
		return true
	}
	return false
}

func (c *ControlClient) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.client != nil
}

func (c *ControlClient) GetSimRate() float32 {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.State.SimRate == 0 {
		return 1
	}
	return c.State.SimRate
}

// CurrentTime returns an extrapolated value that models the current Sim's time.
// (Because the Sim may be running remotely, we have to make some approximations,
// though they shouldn't cause much trouble since we get an update from the Sim
// at least once a second...)
func (c *ControlClient) CurrentTime() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	t := c.State.SimTime

	if !c.State.Paused && !c.lastUpdateRequest.IsZero() {
		d := time.Since(c.lastUpdateRequest)

		// Account for RPC overhead using half of the observed latency
		if c.lastUpdateLatency > 0 {
			d -= c.lastUpdateLatency / 2
		}
		d = max(0, d)

		// Account for sim rate
		d = time.Duration(float64(d) * float64(c.State.SimRate))

		t = t.Add(d)
	}

	// Make sure we don't ever go backward; this can happen due to
	// approximations in the above when an updated current time comes in
	// with a Sim update.
	if t.After(c.lastReturnedTime) {
		c.lastReturnedTime = t
	}
	return c.lastReturnedTime
}

func (c *ControlClient) TowerListAirports() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Figure out airport<-->tower list assignments. Sort the airports
	// according to their TowerListIndex, putting zero (i.e., unassigned)
	// indices at the end. Break ties alphabetically by airport name. The
	// first three then are assigned to the corresponding tower list.
	ap := util.SortedMapKeys(c.State.ArrivalAirports)
	sort.Slice(ap, func(a, b int) bool {
		ai := c.State.ArrivalAirports[ap[a]].TowerListIndex
		if ai == 0 {
			ai = 1000
		}
		bi := c.State.ArrivalAirports[ap[b]].TowerListIndex
		if bi == 0 {
			bi = 1000
		}
		if ai == bi {
			return a < b
		}
		return ai < bi
	})
	if len(ap) > 3 {
		ap = ap[:3]
	}
	return ap
}

func (c *ControlClient) StringIsSPC(s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return av.StringIsSPC(s) || slices.Contains(c.State.STARSFacilityAdaptation.CustomSPCs, s)
}

func (c *ControlClient) RadioIsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.speechWs != nil && (c.playingSpeech || c.awaitReadbackCallsign != "")
}

func (c *ControlClient) HoldRadioTransmissions() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.speechWs != nil {
		c.holdSpeech = true
		c.lastSpeechHoldTime = time.Now().Add(5 * time.Second)
	}
}

func (c *ControlClient) AllowRadioTransmissions() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.holdSpeech = false
}

func (c *ControlClient) LastTransmissionCallsign() av.ADSBCallsign {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lastTransmissionCallsign
}

type serverConnection struct {
	Server *Server
	Err    error
}

func (s *Server) Close() error {
	return s.RPCClient.Close()
}

func (s *Server) GetConfigs() map[string]map[string]*server.Configuration {
	return s.configs
}

func (s *Server) setRunningSims(rs map[string]*server.RemoteSim) {
	s.runningSims = rs
}

func (s *Server) GetRunningSims() map[string]*server.RemoteSim {
	return s.runningSims
}

func getClient(hostname string, lg *log.Logger) (*RPCClient, error) {
	conn, err := net.Dial("tcp", hostname)
	if err != nil {
		return nil, err
	}

	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		return nil, err
	}

	codec := util.MakeGOBClientCodec(cc)
	codec = util.MakeLoggingClientCodec(hostname, codec, lg)
	return &RPCClient{rpc.NewClientWithCodec(codec)}, nil
}

func TryConnectRemoteServer(hostname string, lg *log.Logger) chan *serverConnection {
	ch := make(chan *serverConnection, 1)
	go func() {
		if client, err := getClient(hostname, lg); err != nil {
			ch <- &serverConnection{Err: err}
			return
		} else {
			var cr server.ConnectResult
			start := time.Now()
			if err := client.callWithTimeout("SimManager.Connect", server.ViceRPCVersion, &cr); err != nil {
				ch <- &serverConnection{Err: err}
			} else {
				lg.Debugf("%s: server returned configuration in %s", hostname, time.Since(start))
				ch <- &serverConnection{
					Server: &Server{
						RPCClient:   client,
						HaveTTS:     cr.HaveTTS,
						name:        "Network (Multi-controller)",
						configs:     cr.Configurations,
						runningSims: cr.RunningSims,
					},
				}
			}
		}
	}()

	return ch
}

func BroadcastMessage(hostname, msg, password string, lg *log.Logger) {
	client, err := getClient(hostname, lg)
	if err != nil {
		lg.Errorf("unable to get client for broadcast: %v", err)
		return
	}

	err = client.callWithTimeout("SimManager.Broadcast", &server.SimBroadcastMessage{
		Password: password,
		Message:  msg,
	}, nil)

	if err != nil {
		lg.Errorf("broadcast error: %v", err)
	}
}
