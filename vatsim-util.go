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

type VATSIMMessage struct {
	Contents string
	Sent     bool
	Time     time.Time
}

type VATSIMConnection interface {
	GetMessages() []VATSIMMessage
	SendMessage(callsign string, m ...interface{})

	CurrentTime() time.Time
	Close()
}

type VATSIMNetConnection struct {
	address     string
	messageChan chan VATSIMMessage
	conn        *net.TCPConn

	messagesMutex sync.Mutex
	allMessages   []VATSIMMessage
}

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
	c.conn.SetNoDelay(true)

	// As messages arrive, send them along the channel
	go func(c *VATSIMNetConnection) {
		defer func() {
			if err := recover(); err != nil {
				lg.Errorf("Panic stack: %s", string(debug.Stack()))
				ShowErrorDialog("Error encountered while receiving VATSIM messages: %v", err)
			}
		}()

		r := bufio.NewReader(c.conn)
		for {
			if str, err := r.ReadString('\n'); err == nil {
				msg := VATSIMMessage{Contents: str, Sent: false, Time: time.Now()}
				c.messagesMutex.Lock()
				c.allMessages = append(c.allMessages, msg)
				c.messagesMutex.Unlock()
				c.messageChan <- msg
			} else {
				close(c.messageChan)
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
				// No more messages
				return messages
			}
			messages = append(messages, msg)

		default:
			// Nothing more on the channel
			return messages
		}
	}
	return messages
}

func (c *VATSIMNetConnection) SendMessage(callsign string, fields ...interface{}) {
	msg := ""
	for i, f := range fields {
		switch v := f.(type) {
		case string:
			msg += v
		case int:
			msg += fmt.Sprintf("%d", v)
		case float32, float64:
			msg += fmt.Sprintf("%f", v)
		default:
			lg.Errorf("Unhandled type passed to SendMessage(): %T", v)
			continue
		}

		if i == 0 {
			msg += callsign
		}
		if i < len(fields)-1 {
			msg += ":"
		}
	}
	msg += "\r\n"

	if *logTraffic {
		lg.Printf("Sent: %s", msg)
	}

	if _, err := c.conn.Write([]byte(msg)); err != nil {
		lg.Errorf("Send error: %v", err)
	}

	c.messagesMutex.Lock()
	c.allMessages = append(c.allMessages, VATSIMMessage{Contents: msg, Time: time.Now(), Sent: true})
	c.messagesMutex.Unlock()
}

func (c *VATSIMNetConnection) CurrentTime() time.Time {
	return time.Now()
}

func (c *VATSIMNetConnection) Close() {
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

type VATSIMReplayConnection struct {
	// Timestamp of the first message
	streamStart time.Time
	// Time(*) at which we started replaying the stream. (*) adjusted to account for any requested
	// offset into the stream as well as replay rate time scaling, so this is actually the time
	// corresponding to the beginning of the stream.
	replayStart        time.Time
	timeRateMultiplier float32

	next VATSIMMessage

	f       io.ReadCloser
	decoder *json.Decoder
}

func NewVATSIMReplayConnection(filename string, offsetSeconds int, replayRate float32) (*VATSIMReplayConnection, error) {
	offset := -time.Duration(time.Duration(float32(offsetSeconds)/replayRate) * time.Second)

	c := &VATSIMReplayConnection{
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
	for r.next.Time.Before(streamNow) {
		// Don't report messages that the client originally sent
		if !r.next.Sent {
			msg := strings.TrimSpace(r.next.Contents) + "\r\n"
			messages = append(messages, VATSIMMessage{Contents: msg, Sent: false, Time: r.next.Time})
		}

		if err := r.decoder.Decode(&r.next); err == io.EOF {
			r.f.Close()
			r.f = nil
			break
		}
	}

	return messages
}

func (r *VATSIMReplayConnection) SendMessage(callsign string, m ...interface{}) {}

func (r *VATSIMReplayConnection) CurrentTime() time.Time {
	// How many seconds into the stream are we?
	ds := time.Since(r.replayStart).Seconds() * float64(r.timeRateMultiplier)
	s := time.Duration(ds * float64(time.Second))
	// Report time w.r.t. the stream
	return r.streamStart.Add(s)
}

func (r *VATSIMReplayConnection) Close() {
	if r.f != nil {
		r.f.Close()
	}
}

///////////////////////////////////////////////////////////////////////////

type VATSIMMessageFieldSpec struct {
	idx     int
	pattern string
}

func (s *VATSIMMessageFieldSpec) Match(str string) bool {
	// TODO: regexp? Capture groups? ...
	return strings.HasPrefix(str, s.pattern)
}

type VATSIMMessageSpec struct {
	id        string
	minFields int
	argFields []int
	match     []VATSIMMessageFieldSpec
	handler   func(v *VATSIMServer, sender string, args []string) error
}

// Indexing in arg argFields has 0 as the first field of the sent message.
// If it's nil, then all args are passed, including the one with the command.
func NewMessageSpec(id string, minFields int, argFields []int,
	handler func(v *VATSIMServer, sender string, args []string) error) *VATSIMMessageSpec {
	v := &VATSIMMessageSpec{minFields: minFields, argFields: argFields, handler: handler}
	for i, f := range strings.Split(id, ":") {
		if i == 0 {
			v.id = f
		} else {
			v.match = append(v.match, VATSIMMessageFieldSpec{idx: i, pattern: f})
		}
	}
	return v
}

func (s *VATSIMMessageSpec) Match(fields []string) (string, []string, bool) {
	if len(fields) < s.minFields {
		return "", nil, false
	}

	if !strings.HasPrefix(fields[0], s.id) {
		return "", nil, false
	}
	sender := fields[0][len(s.id):]

	for _, m := range s.match {
		if !m.Match(fields[m.idx]) {
			return "", nil, false
		}
	}

	var args []string
	if s.argFields == nil {
		// Pass all of them, in order
		args = fields
	} else {
		for _, i := range s.argFields {
			args = append(args, fields[i])
		}
	}

	return sender, args, true
}
