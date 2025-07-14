// pkg/util/rpc.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"bufio"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/rpc"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmp/vice/pkg/log"

	"github.com/vmihailenco/msgpack/v5"
)

var ErrRPCTimeout = errors.New("RPC call timed out")

///////////////////////////////////////////////////////////////////////////
// RPC/Networking stuff

type mpServerCodec struct {
	rwc    io.ReadWriteCloser
	dec    *msgpack.Decoder
	enc    *msgpack.Encoder
	encBuf *bufio.Writer
	lg     *log.Logger
	closed bool
}

func (c *mpServerCodec) ReadRequestHeader(r *rpc.Request) error {
	return c.dec.Decode(r)
}

func (c *mpServerCodec) ReadRequestBody(body any) error {
	if body == nil {
		return nil
	}
	return c.dec.Decode(body)
}

func (c *mpServerCodec) WriteResponse(r *rpc.Response, body any) (err error) {
	if err = c.enc.Encode(r); err != nil {
		c.Close()
		return
	}
	if err = c.enc.Encode(body); err != nil {
		c.lg.Errorf("rpc: error encoding body: %v", err)
		c.Close()
		return
	}
	return c.encBuf.Flush()
}

func (c *mpServerCodec) Close() error {
	if c.closed {
		// Only call c.rwc.Close once; otherwise the semantics are undefined.
		return nil
	}
	c.closed = true
	return c.rwc.Close()
}

func MakeMessagepackServerCodec(conn io.ReadWriteCloser, lg *log.Logger) rpc.ServerCodec {
	buf := bufio.NewWriter(conn)
	return &mpServerCodec{
		rwc:    conn,
		dec:    msgpack.NewDecoder(conn),
		enc:    msgpack.NewEncoder(buf),
		lg:     lg,
		encBuf: buf,
	}
}

type LoggingServerCodec struct {
	rpc.ServerCodec
	lg    *log.Logger
	label string
}

func MakeLoggingServerCodec(label string, c rpc.ServerCodec, lg *log.Logger) *LoggingServerCodec {
	return &LoggingServerCodec{ServerCodec: c, lg: lg, label: label}
}

func (c *LoggingServerCodec) ReadRequestHeader(r *rpc.Request) error {
	err := c.ServerCodec.ReadRequestHeader(r)
	c.lg.Info("server: got rpc request", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod), slog.Any("error", err))
	return err
}

func (c *LoggingServerCodec) WriteResponse(r *rpc.Response, body any) error {
	c.lg.Info("server: writing rpc response", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod),
		slog.String("type", fmt.Sprintf("%T", body)))
	err := c.ServerCodec.WriteResponse(r, body)
	c.lg.Info("server: rpc response written", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod),
		slog.Any("error", err))
	return err
}

type mpClientCodec struct {
	rwc    io.ReadWriteCloser
	dec    *msgpack.Decoder
	enc    *msgpack.Encoder
	encBuf *bufio.Writer
}

func (c *mpClientCodec) WriteRequest(r *rpc.Request, body any) (err error) {
	if err = c.enc.Encode(r); err != nil {
		return
	}
	if err = c.enc.Encode(body); err != nil {
		return
	}
	return c.encBuf.Flush()
}

func (c *mpClientCodec) ReadResponseHeader(r *rpc.Response) error {
	return c.dec.Decode(r)
}

func (c *mpClientCodec) ReadResponseBody(body any) error {
	if body == nil {
		return nil
	}
	return c.dec.Decode(body)
}

func (c *mpClientCodec) Close() error {
	return c.rwc.Close()
}

func MakeMessagepackClientCodec(conn io.ReadWriteCloser) rpc.ClientCodec {
	encBuf := bufio.NewWriter(conn)
	return &mpClientCodec{
		rwc:    conn,
		dec:    msgpack.NewDecoder(conn),
		enc:    msgpack.NewEncoder(encBuf),
		encBuf: encBuf,
	}
}

type LoggingClientCodec struct {
	rpc.ClientCodec
	lg    *log.Logger
	label string
}

func MakeLoggingClientCodec(label string, c rpc.ClientCodec, lg *log.Logger) *LoggingClientCodec {
	return &LoggingClientCodec{ClientCodec: c, lg: lg, label: label}
}

func (c *LoggingClientCodec) WriteRequest(r *rpc.Request, v any) error {
	err := c.ClientCodec.WriteRequest(r, v)
	c.lg.Debug("client: rpc request", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod),
		slog.Any("error", err))
	return err
}

func (c *LoggingClientCodec) ReadResponseHeader(r *rpc.Response) error {
	err := c.ClientCodec.ReadResponseHeader(r)
	c.lg.Debug("client: rpc response", slog.String("label", c.label),
		slog.String("service_method", r.ServiceMethod),
		slog.Any("error", err))
	return err
}

type CompressedConn struct {
	net.Conn
	r io.ReadCloser
	w *flate.Writer
}

func MakeCompressedConn(c net.Conn) (*CompressedConn, error) {
	cc := &CompressedConn{Conn: c}
	var err error
	cc.r = flate.NewReader(c)
	if cc.w, err = flate.NewWriter(c, 3); err != nil {
		return nil, err
	}
	return cc, nil
}

func (c *CompressedConn) Read(b []byte) (n int, err error) {
	n, err = c.r.Read(b)
	return
}

func (c *CompressedConn) Write(b []byte) (n int, err error) {
	n, err = c.w.Write(b)
	c.w.Flush()
	return
}

func (c *CompressedConn) Close() error {
	c.r.Close()
	c.w.Close()
	return c.Conn.Close()
}

var RXTotal, TXTotal int64

type LoggingConn struct {
	net.Conn
	lg             *log.Logger
	sent, received int64
	start          time.Time
	lastReport     time.Time
	mu             sync.Mutex
}

func MakeLoggingConn(c net.Conn, lg *log.Logger) *LoggingConn {
	return &LoggingConn{
		Conn:       c,
		lg:         lg,
		start:      time.Now(),
		lastReport: time.Now(),
	}
}

func GetLoggedRPCBandwidth() (int64, int64) {
	return atomic.LoadInt64(&RXTotal), atomic.LoadInt64(&TXTotal)
}

func (c *LoggingConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)

	atomic.AddInt64(&c.received, int64(n))
	atomic.AddInt64(&RXTotal, int64(n))
	c.maybeReport()

	return
}

func (c *LoggingConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)

	atomic.AddInt64(&c.sent, int64(n))
	atomic.AddInt64(&TXTotal, int64(n))
	c.maybeReport()

	return
}

func (c *LoggingConn) maybeReport() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if time.Since(c.lastReport) > 1*time.Minute {
		min := time.Since(c.start).Minutes()
		rec, sent := atomic.LoadInt64(&c.received), atomic.LoadInt64(&c.sent)
		c.lg.Info("bandwidth",
			slog.String("address", c.Conn.RemoteAddr().String()),
			slog.Int64("bytes_received", rec),
			slog.Int("bytes_received_per_minute", int(float64(rec)/min)),
			slog.Int64("bytes_transmitted", sent),
			slog.Int("bytes_transmitted_per_minute", int(float64(sent)/min)))
		c.lastReport = time.Now()
	}
}

func IsRPCServerError(err error) bool {
	_, ok := err.(rpc.ServerError)
	return ok || errors.Is(err, rpc.ErrShutdown)
}
