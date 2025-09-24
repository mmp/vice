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
	"slices"
	"strings"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
	"golang.org/x/sync/errgroup"
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

func (r *RPCProvider) GetAtmosGrid(tracon string, t time.Time) (atmos *wx.AtmosSOA, time time.Time, nextTime time.Time, err error) {
	args := GetAtmosArgs{
		TRACON: tracon,
		Time:   t,
	}
	var result GetAtmosResult
	if err = r.callWithTimeout(GetAtmosGridRPC, args, &result); err == nil {
		atmos = result.AtmosSOA
		time = result.Time
		nextTime = result.NextTime
	}
	return
}

///////////////////////////////////////////////////////////////////////////
// GCSProvider

type GCSProvider struct {
	metar        map[string]wx.METARSOA
	metarCh      chan map[string]wx.METARSOA
	metarTimesCh chan []time.Time

	timesCache     map[string][]time.Time // prefix -> times available in GCS
	timesCacheDone chan struct{}

	validIntervalsCh chan struct{}
	validIntervals   []util.TimeInterval

	mu        util.LoggingMutex
	gcsClient *util.GCSClient
	lg        *log.Logger
}

func MakeGCSProvider(lg *log.Logger) (wx.Provider, error) {
	g := &GCSProvider{
		metarCh:          make(chan map[string]wx.METARSOA, 1),
		validIntervalsCh: make(chan struct{}),

		metarTimesCh:   make(chan []time.Time, 1),
		timesCache:     make(map[string][]time.Time),
		timesCacheDone: make(chan struct{}),

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

	var eg errgroup.Group
	eg.Go(func() error { return g.fetchMETAR() })

	pch := make(chan string)
	eg.Go(func() error {
		defer close(pch)
		for tracon := range av.DB.TRACONs {
			pch <- "precip/" + tracon
		}
		for _, tracon := range wx.AtmosTRACONs {
			pch <- "atmos/" + tracon
		}
		return nil
	})
	for range 4 { // Limit parallelism because zstd is a memory pig.
		eg.Go(func() error { return g.fetchManifests(pch) })
	}
	go func() {
		if err := eg.Wait(); err != nil {
			g.lg.Errorf("%v", err)
		}
		close(g.timesCacheDone)
		go g.collectTimeIntervals()
	}()

	return g, nil
}

func (g *GCSProvider) fetchManifests(pch <-chan string) error {
	zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
	if err != nil {
		return err
	}

	for prefix := range pch {
		var manifest []string
		if r, err := g.gcsClient.GetReader(filepath.Join(prefix, "manifest.msgpack.zst")); err != nil {
			return err
		} else {
			zr.Reset(r)
			if err := msgpack.NewDecoder(zr).Decode(&manifest); err != nil {
				return err
			} else if err := r.Close(); err != nil {
				return err
			}
		}

		times := make([]time.Time, 0, len(manifest))
		for _, file := range manifest {
			if t, err := time.Parse(time.RFC3339, strings.TrimSuffix(file, ".msgpack.zst")); err != nil {
				return err
			} else {
				times = append(times, t)
			}
		}

		g.mu.Lock(g.lg)
		g.timesCache[prefix] = times
		g.mu.Unlock(g.lg)
	}
	return nil
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

	return msgpack.NewDecoder(zr).Decode(obj)
}

func (g *GCSProvider) fetchMETAR() error {
	defer close(g.metarCh)
	defer close(g.metarTimesCh)

	var m map[string]wx.METARSOA
	if t, err := util.CacheRetrieveObject("wx/METAR", &m); err != nil || time.Since(t) > 24*time.Hour {
		start := time.Now()

		if err := g.getObject("METAR.msgpack.zst", &m); err != nil {
			return err
		}

		m["KAAC"] = m["KOKC"] // fake METAR for KAAC
		// TODO: rewrite Raw METAR?

		if err := util.CacheStoreObject("wx/METAR", m); err != nil {
			return err
		}

		g.lg.Infof("Fetched METAR for %d airports in %s", len(m), time.Since(start))
	}

	var times []time.Time
	phlMETAR := wx.DecodeMETARSOA(m["KPHL"])
	for _, metar := range phlMETAR {
		times = append(times, metar.Time.UTC())
	}
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	g.metarTimesCh <- times

	g.metarCh <- m

	return nil
}

func (g *GCSProvider) getAvailableTimes(prefix string) []time.Time {
	<-g.timesCacheDone

	g.mu.Lock(g.lg)
	defer g.mu.Unlock(g.lg)
	return g.timesCache[prefix]
}

func (g *GCSProvider) buildObjectPath(prefix string, t time.Time) string {
	return prefix + "/" + t.UTC().Format(time.RFC3339) + ".msgpack.zst"
}

func (g *GCSProvider) GetAvailableTimeIntervals() []util.TimeInterval {
	<-g.validIntervalsCh // wait for intervals to be ready
	g.mu.Lock(g.lg)
	defer g.mu.Unlock(g.lg)
	return g.validIntervals
}

func (g *GCSProvider) collectTimeIntervals() {
	const (
		metarIntervalTolerance  = 75 * time.Minute
		precipIntervalTolerance = 40 * time.Minute
		atmosIntervalTolerance  = 65 * time.Minute
	)

	metar := <-g.metarTimesCh

	precip, ok := g.timesCache["precip/PHL"]
	if !ok {
		g.lg.Errorf("precip/PHL not found in timesCache")
	}
	atmos, ok := g.timesCache["atmos/PHL"]
	if !ok {
		g.lg.Errorf("atmos/PHL not found in timesCache")
	}

	mi := util.FindTimeIntervals(metar, metarIntervalTolerance)
	pi := util.FindTimeIntervals(precip, precipIntervalTolerance)
	ai := util.FindTimeIntervals(atmos, atmosIntervalTolerance)

	iv := util.MergeIntervals(mi, pi, ai)

	iv = util.MapSlice(iv, func(ti util.TimeInterval) util.TimeInterval {
		// Make sure we're in UTC.
		ti = util.TimeInterval{ti[0].UTC(), ti[1].UTC()}

		// Ensure that all intervals start and end at 0000Z by
		// advancing the start and pulling back the end as needed. Note
		// that this may give us some invalid intervals, but we will
		// cull those shortly.
		start := ti.Start().Truncate(24 * time.Hour)
		if !ti.Start().Equal(start) {
			// Interval doesn't start at midnight, so this day isn't fully covered
			start = start.Add(24 * time.Hour)
		}
		end := ti.End().Truncate(24 * time.Hour)

		return util.TimeInterval{start, end}
	})

	iv = util.FilterSliceInPlace(iv, func(in util.TimeInterval) bool {
		return in.Start().Before(in.End())
	})

	g.mu.Lock(g.lg)
	g.validIntervals = iv
	close(g.validIntervalsCh) // signal done
	g.mu.Unlock(g.lg)
}

func (g *GCSProvider) GetMETAR(airports []string) (map[string]wx.METARSOA, error) {
	g.mu.Lock(g.lg)
	defer g.mu.Unlock(g.lg)

	if g.metar == nil {
		g.metar = <-g.metarCh
		g.metarCh = nil
	}

	m := make(map[string]wx.METARSOA)
	for _, ap := range airports {
		if metar, ok := g.metar[ap]; ok {
			m[ap] = metar
		}
	}
	return m, nil
}

func (g *GCSProvider) getPath(prefix string, t time.Time) (string, time.Time, error) {
	times := g.getAvailableTimes(prefix)

	idx, err := util.FindTimeAtOrBefore(times, t)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%s: %w", prefix, err)
	}

	var next time.Time
	if idx+1 < len(times) {
		next = times[idx+1]
	}

	return g.buildObjectPath(prefix, times[idx]), next, nil
}

func (g *GCSProvider) GetPrecipURL(tracon string, t time.Time) (string, time.Time, error) {
	path, nextTime, err := g.getPath("precip/"+tracon, t)
	if err != nil {
		return "", time.Time{}, err
	}

	url, err := g.gcsClient.GetURL(path, 4*time.Hour)
	if err != nil {
		return "", time.Time{}, err
	}

	return url, nextTime, nil
}

func (g *GCSProvider) GetAtmosGrid(tracon string, t time.Time) (atmos *wx.AtmosSOA, atmosTime time.Time, nextTime time.Time, err error) {
	times := g.getAvailableTimes("atmos/" + tracon)
	if len(times) == 0 {
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
	var atmosSOA wx.AtmosSOA
	if err = g.getObject(path, &atmosSOA); err == nil {
		atmos = &atmosSOA
		atmosTime = times[idx].UTC()
		if idx+1 < len(times) {
			nextTime = times[idx+1].UTC()
		}
	}
	return
}
