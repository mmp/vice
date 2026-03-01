// server/wx.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"os"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

func MakeWXProvider(ctx context.Context, serverAddress string, lg *log.Logger) (wx.Provider, error) {
	if g, err := MakeGCSProvider(lg); err == nil {
		return g, nil
	} else if r, err := MakeRPCProvider(ctx, serverAddress, lg); err == nil {
		return r, nil
	} else {
		return MakeResourcesWXProvider(lg), nil
	}
}

///////////////////////////////////////////////////////////////////////////
// RPCProvider

type RPCProvider struct {
	client *rpc.Client
	lg     *log.Logger
}

func MakeRPCProvider(ctx context.Context, serverAddress string, lg *log.Logger) (wx.Provider, error) {
	lg.Debugf("%s: connecting for WX", serverAddress)
	start := time.Now()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", serverAddress)
	if err != nil {
		lg.Warnf("%s: unable to connect: %v", serverAddress, err)
		return nil, fmt.Errorf("unable to connect to WX server: %w", err)
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
		client: client,
		lg:     lg,
	}

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

func (r *RPCProvider) GetPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	args := PrecipURLArgs{
		Facility: facility,
		Time:     t,
	}
	var result PrecipURL
	if err := r.callWithTimeout(GetPrecipURLRPC, args, &result); err != nil {
		return "", time.Time{}, err
	}
	return result.URL, result.NextTime, nil
}

func (r *RPCProvider) GetAtmosGrid(facility string, t time.Time, primaryAirport string) (atmos *wx.AtmosByPointSOA, time time.Time, nextTime time.Time, err error) {
	args := GetAtmosArgs{
		Facility:       facility,
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

	precipManifest *wx.Manifest

	atmosManifest  *wx.Manifest
	atmosFetchDone chan struct{}

	gcsClient *util.GCSClient
	lg        *log.Logger
}

func MakeGCSProvider(lg *log.Logger) (wx.Provider, error) {
	g := &GCSProvider{
		metarFetchDone: make(chan struct{}),
		atmosFetchDone: make(chan struct{}),

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

	// We need to fetch something synchronously to see if we're online
	var rawPrecipManifest wx.RawManifest
	if err := g.getObject(wx.ManifestPath("precip"), &rawPrecipManifest); err != nil {
		return nil, err
	}
	g.precipManifest = wx.MakeManifest(rawPrecipManifest)

	go func() {
		defer close(g.metarFetchDone)
		err := g.fetchCached(wx.METARFilename, &g.metar)
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

func (g *GCSProvider) GetPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	times, ok := g.precipManifest.GetTimestamps(facility)
	if !ok {
		return "", time.Time{}, errors.New(facility + ": unknown facility")
	}

	idx, err := util.FindTimeAtOrBefore(times, t)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%s: %w", facility, err)
	}

	var nextTime time.Time
	if idx+1 < len(times) {
		nextTime = times[idx+1]
	}

	path := wx.BuildObjectPath("precip", facility, times[idx])

	url, err := g.gcsClient.GetURL(path, 4*time.Hour)
	if err != nil {
		return "", time.Time{}, err
	}

	return url, nextTime, nil
}

func (g *GCSProvider) GetAtmosGrid(facility string, t time.Time, primaryAirport string) (atmos *wx.AtmosByPointSOA, atmosTime time.Time, nextTime time.Time, err error) {
	<-g.atmosFetchDone

	times, ok := g.atmosManifest.GetTimestamps(facility)
	if !ok {
		// No atmos data for this facility - try to create fallback from METAR
		atmos, err = g.createFallbackAtmos(primaryAirport, t)
		if err != nil {
			err = fmt.Errorf("%s: no atmos data and fallback failed: %w", facility, err)
		}
		return
	}

	var idx int
	idx, err = util.FindTimeAtOrBefore(times, t)
	if err != nil {
		err = fmt.Errorf("atmos/%s: %w", facility, err)
		return
	}

	path := wx.BuildObjectPath("atmos", facility, times[idx])

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
	atmosByTime map[string]*wx.AtmosByTime // facility -> AtmosByTime. Populated on-demand, protected by mu.
	mu          util.LoggingMutex
	lg          *log.Logger
}

func MakeResourcesWXProvider(lg *log.Logger) wx.Provider {
	return &ResourcesWXProvider{
		atmosByTime: make(map[string]*wx.AtmosByTime),
		lg:          lg,
	}
}

func (r *ResourcesWXProvider) GetPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	return "", time.Time{}, errors.New("precipitation data not available in offline mode")
}

func (r *ResourcesWXProvider) getAtmosByTime(facility string) (*wx.AtmosByTime, error) {
	r.mu.Lock(r.lg)
	defer r.mu.Unlock(r.lg)

	if atmos, ok := r.atmosByTime[facility]; ok {
		return atmos, nil
	}

	atmos, err := wx.GetAtmosByTime(facility)
	if err != nil {
		return nil, err
	}
	r.atmosByTime[facility] = atmos
	return atmos, nil
}

func (r *ResourcesWXProvider) GetAtmosGrid(facility string, tGet time.Time, primaryAirport string) (*wx.AtmosByPointSOA, time.Time, time.Time, error) {
	atmosByTime, err := r.getAtmosByTime(facility)
	if err != nil {
		// No atmos data for this facility - try to create fallback from METAR
		atmos, fallbackErr := r.createFallbackAtmos(primaryAirport, tGet)
		if fallbackErr != nil {
			return nil, time.Time{}, time.Time{}, fmt.Errorf("%s: no atmos data and fallback failed: %w", facility, fallbackErr)
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
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%s: no atmospheric data available at or before requested time", facility)
	}

	// Convert single sample stack to an AtmosByPointSOA.
	atmosByPoint := wx.MakeAtmosByPoint()
	fac, ok := av.DB.LookupFacility(facility)
	if !ok {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("%s: unknown facility", facility)
	}
	atmosByPoint.SampleStacks[fac.Center()] = sampleStack

	atmosByPointSOA, err := atmosByPoint.ToSOA()
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	return &atmosByPointSOA, t0, t1, nil
}

func (r *ResourcesWXProvider) createFallbackAtmos(primaryAirport string, t time.Time) (*wx.AtmosByPointSOA, error) {
	metarMap, err := wx.GetMETAR([]string{primaryAirport})
	if err != nil {
		return nil, err
	}
	metarSOA, ok := metarMap[primaryAirport]
	if !ok {
		return nil, fmt.Errorf("no METAR data for %s", primaryAirport)
	}

	return createFallbackAtmosFromMETARSOA(metarSOA, t, primaryAirport)
}
