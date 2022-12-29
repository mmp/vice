// vatsim-util.go
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
	"path"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

///////////////////////////////////////////////////////////////////////////
// VATSIMConnection

// VATSIMMessage stores a single VATSIM network message, sent or received,
// along with the time it was generated.
type VATSIMMessage struct {
	Contents string
	Sent     bool
	Time     time.Time
}

// VATSIMConnection provides a simple abstraction for connections to the
// VATSIM network, including capabilities for sending and receiving
// messages.
type VATSIMConnection interface {
	// GetMessages returns all of the messages (if any) that have arrived
	// from the remote server since it was last called.
	GetMessages() []VATSIMMessage

	// SendMessage sends the specified message to the remote server. It takes the
	// sender's callsign (e.g. "JFK_TWR") as its first parameter and then one or more
	// untyped parameters. Parameters are then converted to strings and delineated by
	// colons.  The provided callsign is appended to the end of the first parameter.
	//
	// Thus, the parameters ("ABE_DEP", "$HO", "ABE_APP", "FBX901") would lead to the
	// string "$HOABE_DEP:ABE_APP:FDX901" being sent.
	SendMessage(callsign string, m ...interface{})

	// CurrentTime returns the current time, as far as the connection is concerned.
	// Its main use is for when a network trace is being replayed; then, it returns
	// time with respect to the time the trace was captured.
	CurrentTime() time.Time

	// GetWindowTitle returns a string to use in the vice window's
	// titlebar, indicating the connection type and general state.
	GetWindowTitle() string

	Connected() bool

	// Close closes the VATSIM connection.
	Close()
}

// VATSIMNetConnection implements the VATSIMConnection interface and handles
// the usual case of a true network connection to VATSIM.
type VATSIMNetConnection struct {
	address     string
	messageChan chan VATSIMMessage
	conn        *net.TCPConn
	connected   bool

	// All network traffic is logged in allMessages so that it can be saved
	// for replays or debugging.  Access to the allMessages array is protected
	// by messagesMutex.
	messagesMutex sync.Mutex
	allMessages   []VATSIMMessage
}

// NewVATSIMNetConnection attempts to initiate a VATSIM connection with the
// provided network address.
func NewVATSIMNetConnection(address string) (*VATSIMNetConnection, error) {
	if !strings.ContainsAny(address, ":") {
		address += ":6809"
	}
	lg.Printf("Connecting to %s", address)

	c := &VATSIMNetConnection{
		address:     address,
		messageChan: make(chan VATSIMMessage, 4096),
	}

	raddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil, err
	}

	if c.conn, err = net.DialTCP("tcp", nil, raddr); err != nil {
		return nil, err
	}
	c.conn.SetNoDelay(true) // traffic on a 2 µm final...

	c.connected = true

	// Receive messages in a separate goroutine and send them along the channel; this
	// lets us block on waiting for new messages without any bother.
	go func(c *VATSIMNetConnection) {
		defer func() {
			if err := recover(); err != nil {
				lg.Errorf("Panic stack: %s", string(debug.Stack()))
				ShowErrorDialog("Error encountered while receiving VATSIM messages: %v", err)
			}
		}()

		r := bufio.NewReader(c.conn)
		for {
			// Make sure to read an entire line's worth, up to a newline.
			if str, err := r.ReadString('\n'); err == nil {
				msg := VATSIMMessage{Contents: str, Sent: false, Time: time.Now()}
				// Add the message to the log
				c.messagesMutex.Lock()
				c.allMessages = append(c.allMessages, msg)
				c.messagesMutex.Unlock()
				// And send it on the chan
				c.messageChan <- msg
			} else {
				close(c.messageChan)
				c.connected = false
				lg.Printf("Exiting VATSIMServer goroutine: %v", err)
				return
			}
		}
	}(c)

	return c, nil
}

func (c *VATSIMNetConnection) GetMessages() []VATSIMMessage {
	var messages []VATSIMMessage
	for {
		select {
		case msg, ok := <-c.messageChan:
			if !ok {
				// The channel is closed.
				return messages
			}
			messages = append(messages, msg)

		default:
			// Nothing more waiting on the channel.
			return messages
		}
	}
}

func (c *VATSIMNetConnection) SendMessage(callsign string, fields ...interface{}) {
	msg := ""
	for i, f := range fields {
		// Append the string representation of |f| to the message
		// Currently, the message fields must be strings, integers, or
		// floats.  (That's all that's currently used.)
		switch v := f.(type) {
		case string:
			msg += v
		case int:
			msg += fmt.Sprintf("%d", v)
		case float32, float64:
			msg += fmt.Sprintf("%f", v)
		case Squawk:
			msg += v.String()
		default:
			lg.Errorf("Unhandled type passed to SendMessage(): %T", v)
			continue
		}

		// The first field gets our callsign appended
		if i == 0 {
			msg += callsign
		}
		// And colons in between fields until the last one.
		if i < len(fields)-1 {
			msg += ":"
		}
	}
	msg += "\r\n"

	if *logTraffic {
		lg.Printf("Sent: %s", msg)
	}

	if _, err := c.conn.Write([]byte(msg)); err != nil {
		lg.Printf("Send error: %v", err)
	}

	c.messagesMutex.Lock()
	c.allMessages = append(c.allMessages, VATSIMMessage{Contents: msg, Time: time.Now(), Sent: true})
	c.messagesMutex.Unlock()
}

func (c *VATSIMNetConnection) CurrentTime() time.Time {
	// For an actual network connection, the reported current time actually
	// is the current time.
	return time.Now()
}

func (c *VATSIMNetConnection) GetWindowTitle() string {
	status := func() string {
		if c.connected {
			return "Connected"
		} else {
			return "Disconnected"
		}
	}()
	return fmt.Sprintf("%s @ %s [%s - %s]", positionConfig.VatsimCallsign,
		positionConfig.primaryFrequency.String(), c.address, status)
}

func (c *VATSIMNetConnection) Connected() bool {
	return c.connected
}

func (c *VATSIMNetConnection) Close() {
	// Closing the connection will cause the goroutine to see an error,
	// close the chan, and exit.
	c.conn.Close()

	if *devmode && len(c.allMessages) > 0 {
		save := &YesOrNoModalClient{
			title: "Save Session?",
			query: "Would you like to save the traffic from this session\n" +
				"for debugging or future replaying?",
			ok: func() {
				home, err := os.UserHomeDir()
				if err != nil {
					home = ""
				}
				fn := "vice-session-" + time.Now().Format("2006-01-02@150405") + ".vsess"
				fn = path.Join(home, fn)
				if f, err := os.Create(fn); err != nil {
					lg.Errorf("%s: %v", fn, err)
				} else {
					defer f.Close()
					enc := json.NewEncoder(f)
					c.messagesMutex.Lock()
					defer c.messagesMutex.Unlock()

					for _, msg := range c.allMessages {
						if err := enc.Encode(msg); err != nil {
							lg.Errorf("encoding %+v: %v", msg, err)
							return
						}
					}
				}
			},
		}
		uiShowModalDialog(NewModalDialogBox(save), false)
	}
}

// VATSIMReplayConnection implements the VATSIMConnection interface for the
// purpose of replaying a captured network connection (e.g., using vsniff
// or VATSIMNetConnection's functionality for doing so.
type VATSIMReplayConnection struct {
	// Timestamp of the first message
	streamStart time.Time

	// Time(*) at which we started replaying the stream. (*) adjusted to
	// account for any requested offset into the stream as well as replay
	// rate time scaling, so this is actually the time corresponding to the
	// beginning of the stream.
	replayStart        time.Time
	timeRateMultiplier float32

	// next always stores the next undelivered message, so that we can peek
	// at its time and see when we should send it along.
	next VATSIMMessage

	filename string
	f        io.ReadCloser
	decoder  *json.Decoder
}

// NewVATSIMReplayConnection tries to create a new VATSIMReplayConnection
// from the specified file, which should contain a JSON-formatted array of
// VATSIMMessages.  It further takes an offset into the trace at which to
// start, specified in seconds, as well as a time rate multiplier for
// slowing down or speeding up the replay.
func NewVATSIMReplayConnection(filename string, offsetSeconds int, replayRate float32) (*VATSIMReplayConnection, error) {
	offset := -time.Duration(time.Duration(float32(offsetSeconds)/replayRate) * time.Second)

	c := &VATSIMReplayConnection{
		filename:           filename,
		replayStart:        time.Now().Add(offset),
		timeRateMultiplier: replayRate,
	}

	var err error
	if c.f, err = os.Open(filename); err != nil {
		return nil, fmt.Errorf("%s: %s", filename, err)
	}

	c.decoder = json.NewDecoder(c.f)

	if err = c.decoder.Decode(&c.next); err != nil {
		return nil, fmt.Errorf("%s: error decoding initial message: %w", filename, err)
	}
	c.streamStart = c.next.Time

	return c, nil
}

func (r *VATSIMReplayConnection) GetMessages() []VATSIMMessage {
	if r.f == nil {
		// We previously reached EOF
		return nil
	}

	var messages []VATSIMMessage

	streamNow := r.CurrentTime() // Current time w.r.t. the stream

	// As long as the next message is before the current stream time, add
	// it to the messages array.
	for r.next.Time.Before(streamNow) {
		messages = append(messages, VATSIMMessage{
			Contents: strings.TrimSpace(r.next.Contents) + "\r\n",
			Sent:     r.next.Sent,
			Time:     r.next.Time,
		})

		if err := r.decoder.Decode(&r.next); err == io.EOF {
			r.f.Close()
			r.f = nil
			break
		}
	}

	return messages
}

func (r *VATSIMReplayConnection) SendMessage(callsign string, m ...interface{}) {}

func (r *VATSIMReplayConnection) GetWindowTitle() string {
	t := "replay " + r.filename + " - "
	if r.f != nil {
		t += "active"
	} else {
		t += "finished"
	}
	return t
}

func (r *VATSIMReplayConnection) CurrentTime() time.Time {
	// How many seconds into the stream are we?
	ds := time.Since(r.replayStart).Seconds() * float64(r.timeRateMultiplier)
	s := time.Duration(ds * float64(time.Second))
	// Report time w.r.t. the stream
	return r.streamStart.Add(s)
}

func (r *VATSIMReplayConnection) Connected() bool {
	return r.f != nil
}

func (r *VATSIMReplayConnection) Close() {
	if r.f != nil {
		r.f.Close()
	}
}

///////////////////////////////////////////////////////////////////////////
// VATSIMMessageSpec

// VATSIMMessageSpec represents a specification of a type of VATSIM
// message; it allows specifying a minimum number of fields and strings
// that particular fields must match.  When a match is found, a provided
// handler function can be called.
type VATSIMMessageSpec struct {
	minFields int
	match     []string
	handler   func(v *VATSIMServer, sender string, args []string) error
}

// NewMessageSpec returns a VATSIMMessageSpec corresponding to the provided
// message specification.  The specification is given by both a string with
// colon-separated matches as well as a minimum number of fields in the
// VATSIM message.
//
// The first field string is interpreted as a prefix that the first field
// in a VATSIM message must match; following field strings must be matched
// completely.  Thus, given the specification "$CQ::BC", the message:
//
// $CQEWR_1_DEL:@94835:BC:UAL1549:2334
//
// would match, since the $CQ prefix matches the first field, there is no
// match specified for the second field, and "BC" matches the third field.
// However, the message:
//
// $CQCLT_AF_TWR:@94835:BY
//
// would not match that specification since the third field isn't "BC".
//
// The sender of the message is taken to be the text after the match in the
// first field.
func NewMessageSpec(pattern string, minFields int,
	handler func(v *VATSIMServer, sender string, args []string) error) *VATSIMMessageSpec {
	return &VATSIMMessageSpec{
		minFields: minFields,
		match:     strings.Split(pattern, ":"),
		handler:   handler}
}

// Match checks to see if the provided VATSIM message (already split at
// colons into separate string fields) matches the VATSIMMessageSpec.  It
// returns the sender of the message (i.e., the text after the match in the
// first field), and a Boolean value indicating whether the match was
// successful.
func (s *VATSIMMessageSpec) Match(fields []string) (sender string, matched bool) {
	if len(fields) < s.minFields || len(fields) < len(s.match) {
		return
	}

	for i, m := range s.match {
		if i == 0 {
			if strings.HasPrefix(fields[0], m) {
				sender = fields[0][len(m):]
			} else {
				return
			}
		} else if m != "" && fields[i] != m {
			return
		}
	}

	matched = true
	return
}
