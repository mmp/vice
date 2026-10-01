// wx/provider.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/rpc"
	"slices"
	"sync"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

// Provider is the public WX entry point for accessing historical weather data, handling
// fallbacks based on the local capabilities (network availability, GCS credentials, etc.)
type Provider struct {
	lg             *log.Logger
	backend        weatherBackend
	resources      *resourcesBackend
	atmosGridCache *expirable.LRU[atmosGridCacheKey, atmosGridResult]
}

// weatherBackend is the package-local abstraction for some of the mechanics of
// servicing calling code requests.
type weatherBackend interface {
	getPrecipURL(facility string, t time.Time) (string, time.Time, error)
	getAtmosGrid(facility string, t time.Time, station string) (*AtmosByPointSOA, time.Time, time.Time, error)
}

// ObjectStore is the remote object storage that the GCS weather backend reads.
// It is satisfied by *gcs.Client; keeping it behind an interface lets release
// builds omit the GCS client, and with it the oauth2 dependency.
type ObjectStore interface {
	GetReader(path string) (io.ReadCloser, error)
	GetURL(path string, lifetime time.Duration) (string, error)
}

type atmosGridResult struct {
	atmos    *AtmosByPointSOA
	time     time.Time
	nextTime time.Time
}

type atmosGridCacheKey struct {
	facility string
	station  string
	time     time.Time
}

const (
	backendFallbackTimeout = 2 * time.Second
	atmosGridCacheSize     = 24
	atmosGridCacheTTL      = time.Hour
)

func newProvider(lg *log.Logger, backend weatherBackend) *Provider {
	resources := newResourcesBackend(lg)
	if backend == nil {
		backend = resources
	}

	return &Provider{
		lg:             lg,
		backend:        backend,
		resources:      resources,
		atmosGridCache: expirable.NewLRU[atmosGridCacheKey, atmosGridResult](atmosGridCacheSize, nil, atmosGridCacheTTL),
	}
}

///////////////////////////////////////////////////////////////////////////
// Provider construction

// MakeProvider constructs the concrete WX provider: one that reads GCS
// directly if there are credentials for it, else one that asks the vice
// server at serverAddress, if there is one. Either falls back to the bundled
// resources for a request that fails. Making it doesn't touch the network.
func MakeProvider(serverAddress string, lg *log.Logger) *Provider {
	if store := gcsStore(lg); store != nil {
		if backend, err := makeGCSBackend(store, lg); err == nil {
			lg.Infof("Using GCS weather provider")
			return newProvider(lg, backend)
		} else {
			lg.Warnf("Have credentials but unable to read the weather manifests: %v", err)
		}
	} else if serverAddress != "" {
		lg.Infof("Using RPC weather provider")
		return newProvider(lg, makeRPCBackend(serverAddress, lg))
	}
	return newProvider(lg, nil)
}

// CheckGCS returns an error if the process has GCS credentials but can't
// get weather from GCS with them: it checks that it can read the bundled
// manifests, fetch an atmospheric grid, and download a radar image through
// a signed URL, retrying for a while before giving up. It returns nil if
// there are no credentials. The public server runs it at startup so that it
// exits rather than serving degraded weather.
func CheckGCS(lg *log.Logger) error {
	store := gcsStore(lg)
	if store == nil {
		return nil
	}
	g, err := makeGCSBackend(store, lg)
	if err != nil {
		return err
	}

	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(10 * time.Second)
		}
		if err = g.check(); err == nil {
			return nil
		}
		lg.Warnf("Unable to get weather from GCS: %v", err)
	}
	return fmt.Errorf("unable to get weather from GCS: %w", err)
}

///////////////////////////////////////////////////////////////////////////
// Provider API

// GetPrecipURL returns a URL to access the specified precipitation radar image.
// Returns the image at-or-before the given time, or an empty URL if that
// image is too old to stand for it, along with the time of the next image.
func (p *Provider) GetPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	r, err := callBackend(p, func() (PrecipURL, error) {
		url, nextTime, err := p.backend.getPrecipURL(facility, t)
		return PrecipURL{URL: url, NextTime: nextTime}, err
	})
	if err == nil || p.backend == p.resources {
		return r.URL, r.NextTime, err
	}

	p.lg.Warnf("Falling back to local precip resources: %v", err)
	return p.resources.getPrecipURL(facility, t)
}

// GetAtmosGrid returns atmospheric grid for simulation.
// GCS and RPC provide full spatial grids; local fallback provides a single
// averaged sample. Returns atmos, its time, and the next time in the series.
// If station is non-empty and no atmos data is available, creates a
// fallback grid from that station's METAR wind data.
func (p *Provider) GetAtmosGrid(facility string, t time.Time, station string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	if ar, ok := p.lookupAtmosGridCache(facility, t, station); ok {
		return ar.atmos, ar.time, ar.nextTime, nil
	}

	ar, err := callBackend(p, func() (atmosGridResult, error) {
		atmos, atmosTime, nextTime, err := p.backend.getAtmosGrid(facility, t, station)
		return atmosGridResult{atmos: atmos, time: atmosTime, nextTime: nextTime}, err
	})
	if err != nil && p.backend != p.resources {
		p.lg.Warnf("Falling back to local atmos resources: %v", err)
		ar.atmos, ar.time, ar.nextTime, err = p.resources.getAtmosGrid(facility, t, station)
	}

	if err == nil {
		p.cacheAtmosGrid(facility, station, ar)
	}
	return ar.atmos, ar.time, ar.nextTime, err
}

func (p *Provider) lookupAtmosGridCache(facility string, t time.Time, station string) (atmosGridResult, bool) {
	if p.atmosGridCache == nil {
		return atmosGridResult{}, false
	}

	t = t.UTC()
	for _, key := range p.atmosGridCache.Keys() {
		if key.facility != facility || key.station != station {
			continue
		}

		ar, ok := p.atmosGridCache.Get(key)
		if !ok || ar.atmos == nil || ar.time.After(t) {
			continue
		}
		if ar.nextTime.IsZero() {
			if ar.time.Equal(t) {
				return ar, true
			}
		} else if t.Before(ar.nextTime) {
			return ar, true
		}
	}
	return atmosGridResult{}, false
}

func (p *Provider) cacheAtmosGrid(facility, station string, ar atmosGridResult) {
	if p.atmosGridCache == nil || ar.atmos == nil || ar.time.IsZero() {
		return
	}
	key := atmosGridCacheKey{
		facility: facility,
		station:  station,
		time:     ar.time.UTC(),
	}
	ar.time = ar.time.UTC()
	ar.nextTime = ar.nextTime.UTC()
	p.atmosGridCache.Add(key, ar)
}

// callBackend makes a request of p's backend. A network-backed request must
// return before the client marks its connection to the local server dead,
// which it does after a few seconds, so if the backend stalls, it gives up
// and returns an error; the caller then falls back to the resources.
func callBackend[T any](p *Provider, request func() (T, error)) (T, error) {
	if p.backend == p.resources {
		return request()
	}

	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := request()
		ch <- result{v, err}
	}()

	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(backendFallbackTimeout):
		var zero T
		return zero, fmt.Errorf("weather backend timeout after %s", backendFallbackTimeout)
	}
}

///////////////////////////////////////////////////////////////////////////
// GCS backend

type gcsBackend struct {
	lg *log.Logger

	gcsClient      ObjectStore
	precipManifest *Manifest
	atmosManifest  *Manifest
}

// makeGCSBackend makes a backend that finds the objects in GCS through the
// bundled manifests, so making it doesn't touch the network.
func makeGCSBackend(store ObjectStore, lg *log.Logger) (*gcsBackend, error) {
	precip, err := bundledPrecipManifest()
	if err != nil {
		return nil, err
	}
	atmos, err := bundledAtmosManifest()
	if err != nil {
		return nil, err
	}

	return &gcsBackend{
		lg:             lg,
		gcsClient:      store,
		precipManifest: precip,
		atmosManifest:  atmos,
	}, nil
}

// check fetches the latest atmospheric grid for a facility and downloads its
// latest radar image.
func (g *gcsBackend) check() error {
	facility, ok := util.SeqLookupFunc(slices.Values(g.atmosManifest.Facilities()), func(f string) bool {
		_, ok := g.precipManifest.GetTimestamps(f)
		return ok
	})
	if !ok {
		return errors.New("no facility has both atmospheric data and radar")
	}

	atmosTimes, _ := g.atmosManifest.GetTimestamps(facility)
	if _, _, _, err := g.getAtmosGrid(facility, atmosTimes[len(atmosTimes)-1], ""); err != nil {
		return fmt.Errorf("%s: atmospheric grid: %w", facility, err)
	}

	precipTimes, _ := g.precipManifest.GetTimestamps(facility)
	url, _, err := g.getPrecipURL(facility, precipTimes[len(precipTimes)-1])
	if err != nil {
		return fmt.Errorf("%s: radar: %w", facility, err)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(url)
	if err != nil {
		return fmt.Errorf("%s: radar: %w", facility, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: radar: HTTP status %d", facility, resp.StatusCode)
	}
	if _, err := DecodePrecip(resp.Body); err != nil {
		return fmt.Errorf("%s: radar: %w", facility, err)
	}
	return nil
}

func (g *gcsBackend) getObject(path string, obj any) error {
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

func (g *gcsBackend) getPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	times, ok := g.precipManifest.GetTimestamps(facility)
	if !ok {
		return "", time.Time{}, errors.New(facility + ": unknown facility")
	}

	// times[next] is the first image after t.
	next, found := slices.BinarySearchFunc(times, t, func(a, b time.Time) int { return a.Compare(b) })
	if found {
		next++
	}
	var nextTime time.Time
	if next < len(times) {
		nextTime = times[next]
	}

	// When t is in a gap in the data, or before or after it, return no
	// image rather than a stale one. The tolerance is the one that start
	// times are chosen with, so a sim started in a covered day has radar.
	if next == 0 || t.Sub(times[next-1]) > precipIntervalTolerance {
		return "", nextTime, nil
	}

	path := BuildObjectPath("precip", facility, times[next-1])

	// Signing is local; success here does not prove that the client will be
	// able to download the image if the user's network is down.
	url, err := g.gcsClient.GetURL(path, 4*time.Hour)
	if err != nil {
		return "", time.Time{}, err
	}
	return url, nextTime, nil
}

func (g *gcsBackend) getAtmosGrid(facility string, t time.Time, station string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	times, ok := g.atmosManifest.GetTimestamps(facility)
	if !ok {
		atmos, err := createFallbackAtmos(station, facility, t)
		return atmos, time.Time{}, time.Time{}, err
	}

	idx, err := util.FindTimeAtOrBefore(times, t)
	if err != nil {
		return nil, time.Time{}, time.Time{}, fmt.Errorf("atmos/%s: %w", facility, err)
	}

	path := BuildObjectPath("atmos", facility, times[idx])
	var atmosSOA AtmosByPointSOA
	if err = g.getObject(path, &atmosSOA); err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	var nextTime time.Time
	if idx+1 < len(times) {
		nextTime = times[idx+1]
	}
	return &atmosSOA, times[idx].UTC(), nextTime, nil
}

///////////////////////////////////////////////////////////////////////////
// RPC backend

// Since the server package imports wx, we need to define the details of the WX RPCs
// here, since wx needs to be able to call them. This is slightly messy.
type PrecipURLArgs struct {
	Facility string
	Time     time.Time
}

type PrecipURL struct {
	URL      string // empty if there's no radar for the time
	NextTime time.Time
}

type GetAtmosArgs struct {
	Facility       string
	Time           time.Time
	WeatherStation string
}

type GetAtmosResult struct {
	AtmosByPointSOA *AtmosByPointSOA
	Time            time.Time
	NextTime        time.Time
}

const GetPrecipURLRPC = "SimManager.GetPrecipURL"
const GetAtmosGridRPC = "SimManager.GetAtmosGrid"

// rpcBackend gets WX data by proxying out to the public vice server. (This is slightly
// confusing since it's generally used by the server running locally for single-controller
// scenarios, which in turn is calling out to the public server--so "server" is somewhat
// overloaded.
type rpcBackend struct {
	serverAddress string
	lg            *log.Logger

	// mu is held while dialing, so that concurrent requests share a dial.
	mu     sync.Mutex
	client *rpc.Client

	// When the last dial failed, so that requests fail fast for a while
	// rather than each waiting out a dial on a network that is down.
	dialErr  error
	dialTime time.Time
}

// rpcRedialDelay is how long after a failed dial requests go to the
// resources rather than dialing again.
const rpcRedialDelay = 30 * time.Second

// makeRPCBackend doesn't wait to connect to the server: it starts
// connecting in the background so that the connection is likely to be ready
// for the first request, and a request connects if it isn't. When the server
// can't be reached, requests try again every so often, so weather recovers
// when the network does.
func makeRPCBackend(serverAddress string, lg *log.Logger) *rpcBackend {
	r := &rpcBackend{serverAddress: serverAddress, lg: lg}
	go func() {
		if _, err := r.connect(nil); err != nil {
			lg.Warnf("%v", err)
		}
	}()
	return r
}

// connect returns the connection to the WX server, dialing a new one if
// there is none or if the current one is stale, which is closed. The public
// server drops idle connections, so this long-lived backend has to be able
// to reconnect.
func (r *rpcBackend) connect(stale *rpc.Client) (*rpc.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.client != nil && r.client != stale {
		// Either it's fine or another request has already reconnected.
		return r.client, nil
	}
	if r.client != nil {
		r.client.Close()
		r.client = nil
	}
	if r.dialErr != nil && time.Since(r.dialTime) < rpcRedialDelay {
		return nil, r.dialErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", r.serverAddress)
	if err != nil {
		r.dialErr = fmt.Errorf("unable to connect to WX server %s: %w", r.serverAddress, err)
		r.dialTime = time.Now()
		return nil, r.dialErr
	}
	r.dialErr = nil

	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}

	codec := util.MakeMessagepackClientCodec(cc)
	codec = util.MakeLoggingClientCodec(r.serverAddress, codec, r.lg)
	r.client = rpc.NewClientWithCodec(codec)
	return r.client, nil
}

func (r *rpcBackend) call(serviceMethod string, args any, reply any) error {
	client, err := r.connect(nil)
	if err != nil {
		return err
	}

	err = callWithTimeout(client, serviceMethod, args, reply)
	if err == nil || !isConnDown(err) {
		return err
	}

	// The server closed the connection (e.g. it reaped an idle one).
	// Reconnect and retry once so weather keeps flowing without a restart.
	if client, err = r.connect(client); err != nil {
		return err
	}
	return callWithTimeout(client, serviceMethod, args, reply)
}

func callWithTimeout(client *rpc.Client, serviceMethod string, args any, reply any) error {
	call := client.Go(serviceMethod, args, reply, nil)
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

// isConnDown reports whether err means the RPC connection is gone, so the next
// call should reconnect rather than fail. net/rpc reports the closure as
// ErrShutdown on a fresh call and as ErrUnexpectedEOF on the one in flight.
func isConnDown(err error) bool {
	return errors.Is(err, rpc.ErrShutdown) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed)
}

func (r *rpcBackend) getPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	args := PrecipURLArgs{Facility: facility, Time: t}
	var result PrecipURL
	if err := r.call(GetPrecipURLRPC, args, &result); err != nil {
		return "", time.Time{}, err
	}
	return result.URL, result.NextTime, nil
}

func (r *rpcBackend) getAtmosGrid(facility string, t time.Time, station string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	args := GetAtmosArgs{Facility: facility, Time: t, WeatherStation: station}
	var result GetAtmosResult
	if err := r.call(GetAtmosGridRPC, args, &result); err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	return result.AtmosByPointSOA, result.Time, result.NextTime, nil
}

///////////////////////////////////////////////////////////////////////////
// Local resources fallback

// Local resources provide offline weather data: simplified wind and no
// precipitation radar data.

type resourcesBackend struct {
	lg *log.Logger
}

func newResourcesBackend(lg *log.Logger) *resourcesBackend {
	return &resourcesBackend{lg: lg}
}

// When running with bundled resources, precipitation images are unavailable, since a GCS key is
// needed to sign the request and that isn't available to client code; but if this is being called,
// the network is probably down, so that's not an issue anyway.
func (r *resourcesBackend) getPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	return "", time.Time{}, errors.New("precipitation data not available in offline mode")
}

func (r *resourcesBackend) getAtmosGrid(facility string, tGet time.Time, station string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	atmosByTime, err := GetAtmosByTime(facility)
	if err != nil {
		// No atmos data for this facility; try to create fallback from METAR.
		atmos, fallbackErr := createFallbackAtmos(station, facility, tGet)
		if fallbackErr != nil {
			return nil, time.Time{}, time.Time{}, fmt.Errorf("%s: no atmos data and fallback failed: %w", facility, fallbackErr)
		}
		return atmos, time.Time{}, time.Time{}, nil
	}

	// Find the time at or before the requested time as well as the next
	// time where we have atmos data. This is intentionally linear: local
	// resources are only queried hourly and via .WIND.
	var t0, t1 time.Time
	var sampleStack *AtmosSampleStack

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

	// Convert a single sample stack to an AtmosByPointSOA.
	atmosByPoint := MakeAtmosByPoint()
	fac, ok := db.DB.LookupFacility(facility)
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

// createFallbackAtmos synthesizes an atmospheric grid for facility from the
// observations at its weather station. The single sample stack sits at the
// facility's center rather than at the station, since it stands in for the
// weather over the whole facility.
func createFallbackAtmos(station, facility string, t time.Time) (*AtmosByPointSOA, error) {
	fac, ok := db.DB.LookupFacility(facility)
	if !ok {
		return nil, fmt.Errorf("%s: unknown facility", facility)
	}

	metarMap, err := GetMETAR([]av.ICAOAirportCode{av.ICAOAirportCode(station)})
	if err != nil {
		return nil, err
	}
	metarSOA, ok := metarMap[av.ICAOAirportCode(station)]
	if !ok {
		return nil, fmt.Errorf("no METAR data for %s", station)
	}

	metars := metarSOA.Decode(station)
	if len(metars) == 0 {
		return nil, fmt.Errorf("no METAR data for %s", station)
	}

	return MakeFallbackAtmosFromMETAR(METARForTime(metars, t), fac.Center())
}
