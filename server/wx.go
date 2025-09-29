// server/wx.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

func MakeWXProvider(serverAddress string, lg *log.Logger) (wx.Provider, error) {
	if g, err := MakeGCSProvider(lg); err == nil {
		return g, nil
	}
	return MakeRPCProvider(serverAddress, lg)
}

///////////////////////////////////////////////////////////////////////////
// RPCProvider

type RPCProvider struct {
	timeIntervals   []util.TimeInterval
	timeIntervalsCh chan struct{}

	metar          map[string]wx.METARSOA
	requestedMETAR map[string]struct{}

	mu     util.LoggingMutex
	client *rpc.Client
	lg     *log.Logger
}

func MakeRPCProvider(serverAddress string, lg *log.Logger) (wx.Provider, error) {
	lg.Debugf("%s: connecting for TTS", serverAddress)
	start := time.Now()
	conn, err := net.DialTimeout("tcp", serverAddress, 5*time.Second)
	if err != nil {
		lg.Warnf("%s: unable to connect: %v", serverAddress, err)
		return nil, fmt.Errorf("unable to connect to TTS server: %w", err)
	}
	lg.Debugf("%s: connected in %s", serverAddress, time.Since(start))

	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		return nil, err
	}

	codec := util.MakeMessagepackClientCodec(cc)
	codec = util.MakeLoggingClientCodec(serverAddress, codec, lg)
	client := rpc.NewClientWithCodec(codec)

	r := &RPCProvider{
		timeIntervalsCh: make(chan struct{}),
		metar:           make(map[string]wx.METARSOA),
		requestedMETAR:  make(map[string]struct{}),
		client:          client,
		lg:              lg,
	}

	go func() {
		defer close(r.timeIntervalsCh)
		if err := r.callWithTimeout(GetTimeIntervalsRPC, struct{}{}, &r.timeIntervals); err != nil {
			lg.Errorf("%v", err)
		}
	}()

	return r, nil
}

func (r *RPCProvider) callWithTimeout(serviceMethod string, args any, reply any) error {
	call := r.client.Go(serviceMethod, args, reply, nil)

	for {
		select {
		case <-call.Done:
			return call.Error
		case <-time.After(15 * time.Second):
			if !util.DebuggerIsRunning() {
				return fmt.Errorf("%s: RPC timeout", serviceMethod)
			}
		}
	}
}

func (r *RPCProvider) GetAvailableTimeIntervals() []util.TimeInterval {
	<-r.timeIntervalsCh
	return r.timeIntervals
}

func (r *RPCProvider) GetMETAR(airports []string) (map[string]wx.METARSOA, error) {
	r.mu.Lock(r.lg)
	defer r.mu.Unlock(r.lg)

	var unrequested []string
	for _, ap := range airports {
		if _, ok := r.requestedMETAR[ap]; !ok {
			unrequested = append(unrequested, ap)
		}
	}

	if len(unrequested) > 0 {
		r.mu.Unlock(r.lg)

		ch := make(chan map[string]wx.METARSOA)
		go func() {
			defer close(ch)

			var m map[string]wx.METARSOA
			if err := r.callWithTimeout(GetMETARRPC, unrequested, &m); err != nil {
				r.lg.Errorf("%v", err)
			} else {
				ch <- m
			}
		}()

		m := <-ch
		r.mu.Lock(r.lg)
		for ap, metar := range m {
			r.metar[ap] = metar
		}
	}

	m := make(map[string]wx.METARSOA)
	for _, ap := range airports {
		if metar, ok := r.metar[ap]; ok {
			m[ap] = metar
		}
	}

	return m, nil
}

func (r *RPCProvider) GetPrecipURL(tracon string, t time.Time) (string, time.Time, error) {
	args := PrecipURLArgs{
		TRACON: tracon,
		Time:   t,
	}
	var result PrecipURL
	if err := r.callWithTimeout(GetPrecipURLRPC, args, &result); err != nil {
		return "", time.Time{}, err
	}
	return result.URL, result.NextTime, nil
}

func (r *RPCProvider) GetAtmosGrid(tracon string, t time.Time) (atmos *wx.AtmosByPointSOA, time time.Time, nextTime time.Time, err error) {
	args := GetAtmosArgs{
		TRACON: tracon,
		Time:   t,
	}
	var result GetAtmosResult
	if err = r.callWithTimeout(GetAtmosGridRPC, args, &result); err == nil {
		atmos = result.AtmosByPointSOA
		time = result.Time
		nextTime = result.NextTime
	}
	return
}

///////////////////////////////////////////////////////////////////////////
// GCSProvider

type GCSProvider struct {
	metar          map[string]wx.METARSOA
	metarFetchDone chan struct{}

	precipTimes     map[string][]time.Time
	precipFetchDone chan struct{}

	atmosTimes     map[string][]time.Time
	atmosFetchDone chan struct{}

	validIntervals     []util.TimeInterval
	validIntervalsDone chan struct{}

	gcsClient *util.GCSClient
	lg        *log.Logger
}

func MakeGCSProvider(lg *log.Logger) (wx.Provider, error) {
	g := &GCSProvider{
		metarFetchDone:     make(chan struct{}),
		precipFetchDone:    make(chan struct{}),
		atmosFetchDone:     make(chan struct{}),
		validIntervalsDone: make(chan struct{}),

		lg: lg,
	}

	creds := os.Getenv("VICE_GCS_CREDENTIALS")
	if creds == "" {
		return nil, errors.New("No GCS credentials; WX unavailable")
	}
	conf := util.GCSClientConfig{Timeout: time.Hour, Credentials: []byte(creds)}

	var err error
	g.gcsClient, err = util.MakeGCSClient("vice-wx", conf)
	if err != nil {
		return g, err
	}

	go func() {
		defer close(g.metarFetchDone)
		var err error
		g.metar, err = g.fetchMETAR()
		if err != nil {
			g.lg.Errorf("%v", err)
		}
	}()

	go func() {
		defer close(g.precipFetchDone)
		var err error
		g.precipTimes, err = g.fetchManifest("precip")
		if err != nil {
			g.lg.Errorf("%v", err)
		}
	}()

	go func() {
		defer close(g.atmosFetchDone)
		var err error
		g.atmosTimes, err = g.fetchManifest("atmos")
		if err != nil {
			g.lg.Errorf("%v", err)
		}
	}()

	go func() {
		defer close(g.validIntervalsDone)
		var err error
		g.validIntervals, err = g.collectTimeIntervals()
		if err != nil {
			g.lg.Errorf("%v", err)
		}
	}()

	return g, nil
}

func (g *GCSProvider) fetchMETAR() (map[string]wx.METARSOA, error) {
	var m map[string]wx.METARSOA
	err := g.fetchCached("METAR.msgpack.zst", &m)
	return m, err
}

func (g *GCSProvider) fetchCached(path string, result any) error {
	if t, err := util.CacheRetrieveObject("wx/"+path, result); err == nil && time.Since(t) < 6*time.Hour {
		fmt.Printf("%s: retrieved from cache\n", path)
		g.lg.Infof("%s: retrieved from cache", path)
		return nil
	}

	start := time.Now()

	if err := g.getObject(path, result); err != nil {
		return err
	}

	fmt.Printf("%s: fetched in %s\n", path, time.Since(start))
	g.lg.Infof("%s: fetched in %s", path, time.Since(start))

	return util.CacheStoreObject("wx/"+path, result)
}

func (g *GCSProvider) fetchManifest(prefix string) (map[string][]time.Time, error) {
	var tr []string
	if err := g.fetchCached(filepath.Join(prefix, "manifest.msgpack.zst"), &tr); err != nil {
		return nil, err
	}
	// Manifests are stored transposed, which makes them compress better..
	manifest, err := util.TransposeStrings(tr)
	if err != nil {
		return nil, err
	}

	// Parse consolidated manifest - files are in format "TRACON/timestamp.msgpack.zst"
	m := make(map[string][]time.Time)
	buf := make([]byte, 0, 32) // Reusable buffer for TRACON strings
	for _, relativePath := range manifest {
		parts := strings.Split(relativePath, "/")
		if len(parts) != 2 {
			g.lg.Warnf("%s: invalid path in %s manifest", relativePath, prefix)
			continue
		}

		if t, err := time.Parse(time.RFC3339, strings.TrimSuffix(parts[1], ".msgpack.zst")); err != nil {
			g.lg.Warnf("%v", err)
			continue
		} else {
			// Copy TRACON to buffer to break GC reference, then intern
			buf = buf[:0] // Reset buffer but keep capacity
			buf = append(buf, parts[0]...)
			traconKey := string(buf) // Create new string from buffer
			m[traconKey] = append(m[traconKey], t.UTC())
		}
	}

	// They should already be sorted by time, but...
	for _, times := range m {
		slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	}

	runtime.GC()

	return m, nil
}

func (g *GCSProvider) getObject(path string, obj any) error {
	r, err := g.gcsClient.GetReader(path)
	if err != nil {
		return err
	}
	defer r.Close()

	zr, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()

	return msgpack.NewDecoder(zr).Decode(obj)
}

func (g *GCSProvider) buildObjectPath(prefix string, t time.Time) string {
	return prefix + "/" + t.UTC().Format(time.RFC3339) + ".msgpack.zst"
}

func (g *GCSProvider) GetAvailableTimeIntervals() []util.TimeInterval {
	<-g.validIntervalsDone
	return g.validIntervals
}

func (g *GCSProvider) collectTimeIntervals() ([]util.TimeInterval, error) {
	<-g.metarFetchDone
	<-g.precipFetchDone
	<-g.atmosFetchDone

	// Arbitrarily, use PHL as the exemplar to see what we have data for.
	var metar []time.Time
	if phl, ok := g.metar["KPHL"]; !ok {
		return nil, errors.New("KPHL not found in metar")
	} else {
		for _, m := range wx.DecodeMETARSOA(phl) {
			metar = append(metar, m.Time.UTC())
		}
		slices.SortFunc(metar, func(a, b time.Time) int { return a.Compare(b) })
	}

	precip, ok := g.precipTimes["PHL"]
	if !ok {
		return nil, errors.New("PHL not found in precipTimes")
	}
	atmos, ok := g.atmosTimes["PHL"]
	if !ok {
		return nil, errors.New("PHL not found in atmosTimes")
	}

	return wx.FullDataDays(metar, precip, atmos), nil
}

func (g *GCSProvider) GetMETAR(airports []string) (map[string]wx.METARSOA, error) {
	<-g.metarFetchDone

	m := make(map[string]wx.METARSOA)
	for _, ap := range airports {
		if metar, ok := g.metar[ap]; ok {
			m[ap] = metar
		}
	}
	return m, nil
}

func (g *GCSProvider) GetPrecipURL(tracon string, t time.Time) (string, time.Time, error) {
	<-g.precipFetchDone

	times, ok := g.precipTimes[tracon]
	if !ok {
		return "", time.Time{}, errors.New(tracon + ": unknown TRACON")
	}

	idx, err := util.FindTimeAtOrBefore(times, t)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%s: %w", tracon, err)
	}

	var nextTime time.Time
	if idx+1 < len(times) {
		nextTime = times[idx+1]
	}

	path := g.buildObjectPath("precip/"+tracon, times[idx])

	url, err := g.gcsClient.GetURL(path, 4*time.Hour)
	if err != nil {
		return "", time.Time{}, err
	}

	return url, nextTime, nil
}

func (g *GCSProvider) GetAtmosGrid(tracon string, t time.Time) (atmos *wx.AtmosByPointSOA, atmosTime time.Time, nextTime time.Time, err error) {
	<-g.atmosFetchDone

	times, ok := g.atmosTimes[tracon]
	if !ok {
		err = errors.New(tracon + ": unknown TRACON")
		return
	}

	var idx int
	idx, err = util.FindTimeAtOrBefore(times, t)
	if err != nil {
		err = fmt.Errorf("atmos/%s: %w", tracon, err)
		return
	}

	path := g.buildObjectPath("atmos/"+tracon, times[idx])

	var atmosSOA wx.AtmosByPointSOA
	if err = g.getObject(path, &atmosSOA); err == nil {
		atmos = &atmosSOA
		atmosTime = times[idx].UTC()
		if idx+1 < len(times) {
			nextTime = times[idx+1].UTC()
		}
	}
	return
}
