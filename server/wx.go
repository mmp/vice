// server/wx.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
	"golang.org/x/sync/errgroup"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

func MakeWXProvider(serverAddress string, lg *log.Logger) (wx.Provider, error) {
	if g, err := MakeGCSProvider(lg); err == nil {
		return g, nil
	} else if r, err := MakeRPCProvider(serverAddress, lg); err == nil {
		return r, nil
	} else {
		return MakeResourcesWXProvider(lg), nil
	}
}

///////////////////////////////////////////////////////////////////////////
// RPCProvider

type RPCProvider struct {
	metar          map[string]wx.METARSOA
	requestedMETAR map[string]struct{}

	timeIntervals map[string][]util.TimeInterval
	intervalsDone chan struct{}

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
		metar:          make(map[string]wx.METARSOA),
		requestedMETAR: make(map[string]struct{}),
		intervalsDone:  make(chan struct{}),
		client:         client,
		lg:             lg,
	}

	go func() {
		defer close(r.intervalsDone)
		var intervals map[string][]util.TimeInterval
		if err := r.callWithTimeout(GetTimeIntervalsRPC, struct{}{}, &intervals); err != nil {
			lg.Errorf("GetTimeIntervals RPC failed: %v", err)
			intervals = make(map[string][]util.TimeInterval)
		}
		r.timeIntervals = intervals
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

func (r *RPCProvider) GetAvailableTimeIntervals() map[string][]util.TimeInterval {
	<-r.intervalsDone
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

func (r *RPCProvider) GetAtmosGrid(tracon string, t time.Time, primaryAirport string) (atmos *wx.AtmosByPointSOA, time time.Time, nextTime time.Time, err error) {
	args := GetAtmosArgs{
		TRACON:         tracon,
		Time:           t,
		PrimaryAirport: primaryAirport,
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
	metar          wx.CompressedMETAR
	metarFetchDone chan struct{}

	precipManifest  *wx.Manifest
	precipFetchDone chan struct{}

	atmosManifest  *wx.Manifest
	atmosFetchDone chan struct{}

	timeIntervals map[string][]util.TimeInterval
	intervalsDone chan struct{}

	gcsClient *util.GCSClient
	lg        *log.Logger
}

func MakeGCSProvider(lg *log.Logger) (wx.Provider, error) {
	g := &GCSProvider{
		metarFetchDone:  make(chan struct{}),
		precipFetchDone: make(chan struct{}),
		atmosFetchDone:  make(chan struct{}),
		intervalsDone:   make(chan struct{}),

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
		err := g.fetchCached(wx.METARFilename, &g.metar)
		if err != nil {
			g.lg.Errorf("%v", err)
		}
	}()

	go func() {
		defer close(g.precipFetchDone)
		var err error
		g.precipManifest, err = g.fetchManifest("precip")
		if err != nil {
			g.lg.Errorf("%v", err)
		}
	}()

	go func() {
		defer close(g.atmosFetchDone)
		var err error
		g.atmosManifest, err = g.fetchManifest("atmos")
		if err != nil {
			g.lg.Errorf("%v", err)
		}
	}()

	go func() {
		defer close(g.intervalsDone)
		g.timeIntervals = g.computeAllTimeIntervals()
	}()

	return g, nil
}

func (g *GCSProvider) fetchCached(path string, result any) error {
	if t, err := util.CacheRetrieveObject("wx/"+path, result); err == nil && time.Since(t) < 6*time.Hour {
		g.lg.Infof("%s: retrieved from cache", path)
		return nil
	}

	if err := g.getObject(path, result); err != nil {
		return err
	}

	return util.CacheStoreObject("wx/"+path, result)
}

func (g *GCSProvider) fetchManifest(prefix string) (*wx.Manifest, error) {
	manifestPath := wx.ManifestPath(prefix)

	var rawManifest wx.RawManifest
	if err := g.fetchCached(manifestPath, &rawManifest); err != nil {
		return nil, err
	}

	return wx.MakeManifest(rawManifest), nil
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

func (g *GCSProvider) GetAvailableTimeIntervals() map[string][]util.TimeInterval {
	<-g.intervalsDone
	return g.timeIntervals
}

func (g *GCSProvider) computeAllTimeIntervals() map[string][]util.TimeInterval {
	<-g.precipFetchDone
	<-g.atmosFetchDone

	result := make(map[string][]util.TimeInterval)

	// Get all TRACONs from precip manifest
	tracons := g.precipManifest.TRACONs()

	for _, tracon := range tracons {
		precipTimes, ok := g.precipManifest.GetTimestamps(tracon)
		if !ok {
			continue
		}

		atmosTimes, ok := g.atmosManifest.GetTimestamps(tracon)
		if !ok {
			continue
		}

		precipIntervals := wx.PrecipIntervals(precipTimes)
		atmosIntervals := wx.AtmosIntervals(atmosTimes)
		intervals := wx.MergeAndAlignToMidnight(precipIntervals, atmosIntervals)

		if len(intervals) > 0 {
			result[tracon] = intervals
		}
	}

	g.lg.Infof("GCSProvider: computed time intervals for %d TRACONs", len(result))
	return result
}

func (g *GCSProvider) GetMETAR(airports []string) (map[string]wx.METARSOA, error) {
	<-g.metarFetchDone

	m := make(map[string]wx.METARSOA)
	for _, ap := range airports {
		soa, err := g.metar.GetAirportMETARSOA(ap)
		if err == nil {
			// It's fine if we're missing some.
			m[ap] = soa
		}
	}
	return m, nil
}

func (g *GCSProvider) GetPrecipURL(tracon string, t time.Time) (string, time.Time, error) {
	<-g.precipFetchDone

	times, ok := g.precipManifest.GetTimestamps(tracon)
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

	path := wx.BuildObjectPath("precip", tracon, times[idx])

	url, err := g.gcsClient.GetURL(path, 4*time.Hour)
	if err != nil {
		return "", time.Time{}, err
	}

	return url, nextTime, nil
}

func (g *GCSProvider) GetAtmosGrid(tracon string, t time.Time, primaryAirport string) (atmos *wx.AtmosByPointSOA, atmosTime time.Time, nextTime time.Time, err error) {
	<-g.atmosFetchDone

	times, ok := g.atmosManifest.GetTimestamps(tracon)
	if !ok {
		// No atmos data for this TRACON - try to create fallback from METAR
		atmos, err = g.createFallbackAtmos(primaryAirport, t)
		if err != nil {
			err = fmt.Errorf("%s: no atmos data and fallback failed: %w", tracon, err)
		}
		return
	}

	var idx int
	idx, err = util.FindTimeAtOrBefore(times, t)
	if err != nil {
		err = fmt.Errorf("atmos/%s: %w", tracon, err)
		return
	}

	path := wx.BuildObjectPath("atmos", tracon, times[idx])

	var atmosSOA wx.AtmosByPointSOA
	if err = g.getObject(path, &atmosSOA); err == nil {
		atmos = &atmosSOA
		atmosTime = times[idx].UTC()
		if idx+1 < len(times) {
			nextTime = times[idx+1]
		}
	}
	return
}

func (g *GCSProvider) createFallbackAtmos(primaryAirport string, t time.Time) (*wx.AtmosByPointSOA, error) {
	<-g.metarFetchDone

	metarSOA, err := g.metar.GetAirportMETARSOA(primaryAirport)
	if err != nil {
		return nil, fmt.Errorf("failed to get METAR for %s: %w", primaryAirport, err)
	}

	return createFallbackAtmosFromMETARSOA(metarSOA, t, primaryAirport)
}

func createFallbackAtmosFromMETARSOA(metarSOA wx.METARSOA, t time.Time, primaryAirport string) (*wx.AtmosByPointSOA, error) {
	metars := metarSOA.Decode()
	if len(metars) == 0 {
		return nil, fmt.Errorf("no METAR data for %s", primaryAirport)
	}

	metar := wx.METARForTime(metars, t)
	ap := av.DB.Airports[primaryAirport]

	return wx.MakeFallbackAtmosFromMETAR(metar, ap.Location)
}

///////////////////////////////////////////////////////////////////////////
// ResourcesWXProvider

// ResourcesWXProvider implements the wx.Provider interface, providing
// information from the local resources directory (more or less intended
// for offline use of vice).
type ResourcesWXProvider struct {
	compressedMETAR wx.CompressedMETAR
	atmosByTime     map[string]*wx.AtmosByTime // tracon -> AtmosByTime. Populated on-demand, protected by mu.
	timeIntervals   map[string][]util.TimeInterval

	initDone chan struct{}
	mu       util.LoggingMutex
	lg       *log.Logger
}

func MakeResourcesWXProvider(lg *log.Logger) wx.Provider {
	r := &ResourcesWXProvider{
		atmosByTime: make(map[string]*wx.AtmosByTime),
		initDone:    make(chan struct{}),
		lg:          lg,
	}

	go func() {
		if err := r.loadWX(); err != nil {
			lg.Errorf("ResourcesWXProvider: %v", err)
		}
		close(r.initDone)
	}()

	return r
}

func (r *ResourcesWXProvider) loadWX() error {
	_, err := r.loadMETAR()
	if err != nil {
		return err
	}

	r.timeIntervals = r.computeAllTimeIntervals()

	r.lg.Infof("ResourcesWXProvider loaded: %d airports' METAR, %d TRACONs with time intervals", r.compressedMETAR.Len(), len(r.timeIntervals))

	return nil
}

func (r *ResourcesWXProvider) computeAllTimeIntervals() map[string][]util.TimeInterval {
	result := make(map[string][]util.TimeInterval)

	// Discover available TRACONs by walking the resources
	util.WalkResources("wx/atmos", func(path string, d fs.DirEntry, filesystem fs.FS, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		// Extract TRACON name from filename (e.g., "wx/atmos/PHL.msgpack.zst" -> "PHL")
		filename := filepath.Base(path)
		tracon := strings.TrimSuffix(filename, ".msgpack.zst")

		atmosByTime, err := r.readAtmosByTimeInternal(tracon)
		if err != nil {
			r.lg.Warnf("Failed to load atmos for %s: %v", tracon, err)
			return nil
		}

		var atmosTimes []time.Time
		for t := range atmosByTime.SampleStacks {
			atmosTimes = append(atmosTimes, t)
		}
		slices.SortFunc(atmosTimes, func(a, b time.Time) int { return a.Compare(b) })

		intervals := wx.MergeAndAlignToMidnight(wx.AtmosIntervals(atmosTimes))
		if len(intervals) > 0 {
			result[tracon] = intervals
		}

		return nil
	})

	return result
}

func (r *ResourcesWXProvider) loadMETAR() ([]time.Time, error) {
	r.mu.Lock(r.lg)
	defer r.mu.Unlock(r.lg)

	// Read the raw file without LoadResource's automatic decompression,
	// since LoadCompressedMETAR handles zstd decompression itself.
	f, err := fs.ReadFile(util.GetResourcesFS(), "wx/"+wx.METARFilename)
	if err != nil {
		return nil, fmt.Errorf("failed to read METAR file: %w", err)
	}

	// Load compressed METAR map using canonical abstraction
	r.compressedMETAR, err = wx.LoadCompressedMETAR(bytes.NewReader(f))
	if err != nil {
		return nil, err
	}

	// Collect all airports first
	var airports []string
	for icao := range r.compressedMETAR.Airports() {
		airports = append(airports, icao)
	}

	// Process in parallel with bounded concurrency
	var mu sync.Mutex
	var allTimes []time.Time
	var eg errgroup.Group
	eg.SetLimit(32)

	for _, icao := range airports {
		eg.Go(func() error {
			metar, err := r.compressedMETAR.GetAirportMETAR(icao)
			if err != nil {
				return err
			}
			times := make([]time.Time, len(metar))
			for i, m := range metar {
				times[i] = m.Time
			}

			mu.Lock()
			allTimes = append(allTimes, times...)
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	slices.SortFunc(allTimes, func(a, b time.Time) int { return a.Compare(b) })
	allTimes = slices.Compact(allTimes)

	return allTimes, nil
}

// readAtmosByTimeInternal reads atmos data without waiting for init.
// Used during initialization.
func (r *ResourcesWXProvider) readAtmosByTimeInternal(tracon string) (*wx.AtmosByTime, error) {
	path := "wx/atmos/" + tracon + ".msgpack.zst"

	// Read the raw file to avoid panicking if it doesn't exist
	f, err := fs.ReadFile(util.GetResourcesFS(), path)
	if err != nil {
		return nil, fmt.Errorf("atmos data not available for %s: %w", tracon, err)
	}

	// Decompress the zstd-compressed data
	zr, err := zstd.NewReader(bytes.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd reader for %s: %w", tracon, err)
	}
	defer zr.Close()

	var atmosTimesSOA wx.AtmosByTimeSOA
	if err := msgpack.NewDecoder(zr).Decode(&atmosTimesSOA); err != nil {
		return nil, err
	}

	atmosByTime := atmosTimesSOA.ToAOS()

	return &atmosByTime, nil
}

// readAtmosByTime reads atmos data after waiting for init to complete.
func (r *ResourcesWXProvider) readAtmosByTime(tracon string) (*wx.AtmosByTime, error) {
	<-r.initDone
	return r.readAtmosByTimeInternal(tracon)
}

func (r *ResourcesWXProvider) GetAvailableTimeIntervals() map[string][]util.TimeInterval {
	<-r.initDone
	return r.timeIntervals
}

func (r *ResourcesWXProvider) GetMETAR(airports []string) (map[string]wx.METARSOA, error) {
	<-r.initDone

	m := make(map[string]wx.METARSOA)
	for _, icao := range airports {
		if metarSOA, err := r.compressedMETAR.GetAirportMETARSOA(icao); err == nil {
			m[icao] = metarSOA
		}
	}

	return m, nil
}

func (r *ResourcesWXProvider) GetPrecipURL(tracon string, t time.Time) (string, time.Time, error) {
	return "", time.Time{}, errors.New("precipitation data not available in offline mode")
}

func (r *ResourcesWXProvider) getAtmosByTime(tracon string) (*wx.AtmosByTime, error) {
	r.mu.Lock(r.lg)
	defer r.mu.Unlock(r.lg)

	if atmosTimes, ok := r.atmosByTime[tracon]; ok {
		return atmosTimes, nil
	} else {
		atmosByTime, err := r.readAtmosByTime(tracon)
		if err != nil {
			return nil, err
		}
		r.atmosByTime[tracon] = atmosByTime
		return atmosByTime, nil
	}
}

func (r *ResourcesWXProvider) GetAtmosGrid(tracon string, tGet time.Time, primaryAirport string) (*wx.AtmosByPointSOA, time.Time, time.Time, error) {
	<-r.initDone

	atmosByTime, err := r.getAtmosByTime(tracon)
	if err != nil {
		// No atmos data for this TRACON - try to create fallback from METAR
		atmos, fallbackErr := r.createFallbackAtmos(primaryAirport, tGet)
		if fallbackErr != nil {
			return nil, time.Time{}, time.Time{}, fmt.Errorf("%s: no atmos data and fallback failed: %w", tracon, fallbackErr)
		}
		return atmos, time.Time{}, time.Time{}, nil
	}

	// Find the time at or before the requested time as well as the next
	// time where we have atmos data. At some point we might want to start
	// caching []time.Time for each entry in r.atmosByTime so that we can
	// do a binary search rather than a linear search, though GetAtmosGrid
	// is only called ~hourly (and via .WIND in STARS) and searches through
	// a few months' worth of hourly data, so this is likely not much of an
	// issue.
	var t0, t1 time.Time
	var sampleStack *wx.AtmosSampleStack

	for tStack, stack := range atmosByTime.SampleStacks {
		if tStack.Before(tGet) || tStack.Equal(tGet) {
			if t0.IsZero() || tStack.After(t0) {
				t0 = tStack
				sampleStack = stack
			}
		} else if t1.IsZero() || tStack.Before(t1) {
			t1 = tStack
		}
	}

	if t0.IsZero() {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%s: no atmospheric data available at or before requested time", tracon)
	}

	// Convert single sample stack to an AtmosByPointSOA.
	atmosByPoint := wx.MakeAtmosByPoint()
	pTRACON := av.DB.TRACONs[tracon].Center()
	atmosByPoint.SampleStacks[pTRACON] = sampleStack

	atmosByPointSOA, err := atmosByPoint.ToSOA()
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	return &atmosByPointSOA, t0, t1, nil
}

func (r *ResourcesWXProvider) createFallbackAtmos(primaryAirport string, t time.Time) (*wx.AtmosByPointSOA, error) {
	metarSOA, err := r.compressedMETAR.GetAirportMETARSOA(primaryAirport)
	if err != nil {
		return nil, err
	}

	return createFallbackAtmosFromMETARSOA(metarSOA, t, primaryAirport)
}
