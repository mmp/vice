// log/log_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package log

import (
	"errors"
	"net"
	"net/rpc"
	"testing"
	"time"
)

func pipeRPCClient(t *testing.T) *rpc.Client {
	t.Helper()
	c, s := net.Pipe()
	t.Cleanup(func() { s.Close() })
	return rpc.NewClient(c)
}

func TestCrashReportClientDialsAndCloses(t *testing.T) {
	l := New(false, "error", t.TempDir())
	dialed := pipeRPCClient(t)
	l.SetCrashReportDialer(func() (*rpc.Client, error) { return dialed, nil })

	c, release := l.crashReportClient(time.Hour)
	if c != dialed {
		t.Fatalf("got client %p, want the dialed one %p", c, dialed)
	}
	release()
	if err := dialed.Close(); !errors.Is(err, rpc.ErrShutdown) {
		t.Errorf("dialed client still open after release: Close returned %v", err)
	}
}

func TestCrashReportClientKeepsSetClientOpen(t *testing.T) {
	l := New(false, "error", t.TempDir())
	set := pipeRPCClient(t)
	l.SetCrashReportClient(set)
	l.SetCrashReportDialer(func() (*rpc.Client, error) {
		t.Error("dialed despite a set client")
		return nil, errors.New("unexpected dial")
	})

	c, release := l.crashReportClient(time.Hour)
	if c != set {
		t.Fatalf("got client %p, want the set one %p", c, set)
	}
	release()
	if err := set.Close(); err != nil {
		t.Errorf("release closed the set client: Close returned %v", err)
	}
}

func TestCrashReportClientDialFailureDoesNotWait(t *testing.T) {
	l := New(false, "error", t.TempDir())
	l.SetCrashReportDialer(func() (*rpc.Client, error) { return nil, errors.New("unreachable") })

	start := time.Now()
	c, release := l.crashReportClient(time.Hour)
	release()
	if c != nil {
		t.Errorf("got client %p from a failed dial", c)
	}
	if d := time.Since(start); d > time.Minute {
		t.Errorf("waited %s after a failed dial", d)
	}
}
