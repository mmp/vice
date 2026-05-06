// wx/provider.go
// Copyright(c) 2022-2025 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package wx

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

	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
)

// Provider is the public WX entry point for accessing historical weather data, handling
// fallbacks based on the local capabilities (network availability, GCS credentials, etc.)
type Provider struct {
	lg        *log.Logger
	backend   weatherBackend
	resources *resourcesBackend
}

// weatherBackend is the package-local abstraction for some of the mechanics of
// servicing calling code requests.
type weatherBackend interface {
	getPrecipURL(facility string, t time.Time) (string, time.Time, error)
	getAtmosGrid(facility string, t time.Time, primaryAirport string) (*AtmosByPointSOA, time.Time, time.Time, error)
}

type atmosGridResult struct {
	atmos    *AtmosByPointSOA
	time     time.Time
	nextTime time.Time
	err      error
}

const backendFallbackTimeout = 2 * time.Second

func newProvider(lg *log.Logger, backend weatherBackend) *Provider {
	resources := newResourcesBackend(lg)
	if backend == nil {
		backend = resources
	}

	return &Provider{
		lg:        lg,
		backend:   backend,
		resources: resources,
	}
}

///////////////////////////////////////////////////////////////////////////
// Provider construction

// MakeProvider constructs the concrete WX provider.
func MakeProvider(serverAddress string, lg *log.Logger) *Provider {
	if creds := os.Getenv("VICE_GCS_CREDENTIALS"); creds != "" {
		// We have credentials, assume they are valid (and any failure will be network-related).
		if backend, err := makeGCSBackend(creds, lg); err == nil {
			lg.Infof("Using GCS weather provider")
			return newProvider(lg, backend)
		} else {
			lg.Warnf("Have credentials but unable to initialize GCS weather provider: %v", err)
			return newProvider(lg, nil)
		}
	} else if backend, err := makeRPCBackend(serverAddress, lg); err == nil {
		lg.Infof("Using RPC weather provider")
		return newProvider(lg, backend)
	} else {
		lg.Warnf("Unable to initialize RPC weather provider: %v", err)
		return newProvider(lg, nil)
	}
}

///////////////////////////////////////////////////////////////////////////
// Provider API

// GetPrecipURL returns a URL to access the specified precipitation radar image.
// Returns the image at-or-before the given time.
func (p *Provider) GetPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	// Precip requests are cheap for GCS because URL signing is local. RPC has
	// its own timeout; resources returns a typed unavailable error.
	url, nextTime, err := p.backend.getPrecipURL(facility, t)
	if err == nil || p.backend == p.resources {
		return url, nextTime, err
	}

	p.lg.Warnf("Falling back to local precip resources: %v", err)
	return p.resources.getPrecipURL(facility, t)
}

// GetAtmosGrid returns atmospheric grid for simulation.
// GCS and RPC provide full spatial grids; local fallback provides a single
// averaged sample. Returns atmos, its time, and the next time in the series.
// If primaryAirport is non-empty and no atmos data is available, creates a
// fallback grid from the primary airport's METAR wind data.
func (p *Provider) GetAtmosGrid(facility string, t time.Time, primaryAirport string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	ar := p.getAtmosGridFromBackend(facility, t, primaryAirport)
	atmos, atmosTime, nextTime, err := ar.atmos, ar.time, ar.nextTime, ar.err
	if err == nil || p.backend == p.resources {
		return atmos, atmosTime, nextTime, err
	}

	p.lg.Warnf("Falling back to local atmos resources: %v", err)
	return p.resources.getAtmosGrid(facility, t, primaryAirport)
}

func (p *Provider) getAtmosGridFromBackend(facility string, t time.Time, primaryAirport string) atmosGridResult {
	if p.backend == p.resources {
		atmos, atmosTime, nextTime, err := p.backend.getAtmosGrid(facility, t, primaryAirport)
		return atmosGridResult{atmos: atmos, time: atmosTime, nextTime: nextTime, err: err}
	}

	// Network-backed atmos fetches must return before the client marks the
	// local RPC connection dead. If the backend stalls, return to the caller and
	// let the normal resources fallback path handle the request.
	ch := make(chan atmosGridResult, 1)
	go func() {
		atmos, atmosTime, nextTime, err := p.backend.getAtmosGrid(facility, t, primaryAirport)
		ch <- atmosGridResult{atmos: atmos, time: atmosTime, nextTime: nextTime, err: err}
	}()

	select {
	case ar := <-ch:
		return ar
	case <-time.After(backendFallbackTimeout):
		return atmosGridResult{err: fmt.Errorf("weather backend timeout after %s", backendFallbackTimeout)}
	}
}

///////////////////////////////////////////////////////////////////////////
// GCS backend

type gcsBackend struct {
	lg *log.Logger

	gcsClient      *util.GCSClient
	precipManifest *Manifest
	atmosManifest  *Manifest
}

func makeGCSBackend(creds string, lg *log.Logger) (*gcsBackend, error) {
	gcsClient, err := util.MakeGCSClient("vice-wx", util.GCSClientConfig{
		Context:     context.Background(),
		Timeout:     4 * time.Second,
		Credentials: []byte(creds),
	})
	if err != nil {
		return nil, err
	}

	g := &gcsBackend{
		lg:        lg,
		gcsClient: gcsClient,
	}

	// Load the manifests synchronously before selecting GCS as the active
	// provider. These may come from the local cache, so this validates that
	// the provider has usable manifest data, not necessarily that the network
	// is currently reachable.
	if g.precipManifest, err = g.loadManifest("precip"); err != nil {
		return nil, err
	}
	if g.atmosManifest, err = g.loadManifest("atmos"); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *gcsBackend) loadManifest(prefix string) (*Manifest, error) {
	var raw RawManifest
	path := ManifestPath(prefix)

	if t, err := util.CacheRetrieveObject("wx/"+path, &raw); err == nil && time.Since(t) < 6*time.Hour {
		g.lg.Infof("%s: retrieved from cache", path)
	} else if err := g.getObject(path, &raw); err == nil {
		_ = util.CacheStoreObject("wx/"+path, raw)
	} else {
		return nil, err
	}
	return MakeManifest(raw), nil
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

	idx, err := util.FindTimeAtOrBefore(times, t)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%s: %w", facility, err)
	}

	var nextTime time.Time
	if idx+1 < len(times) {
		nextTime = times[idx+1]
	}

	path := BuildObjectPath("precip", facility, times[idx])

	// Signing is local; success here does not prove that the client will be
	// able to download the image if the user's network is down.
	url, err := g.gcsClient.GetURL(path, 4*time.Hour)
	if err != nil {
		return "", time.Time{}, err
	}
	return url, nextTime, nil
}

func (g *gcsBackend) getAtmosGrid(facility string, t time.Time, primaryAirport string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	times, ok := g.atmosManifest.GetTimestamps(facility)
	if !ok {
		atmos, err := createFallbackAtmos(primaryAirport, t)
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
	URL      string
	NextTime time.Time
}

type GetAtmosArgs struct {
	Facility       string
	Time           time.Time
	PrimaryAirport string
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
	client *rpc.Client
}

func makeRPCBackend(serverAddress string, lg *log.Logger) (*rpcBackend, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", serverAddress)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to WX server %s: %w", serverAddress, err)
	}

	cc, err := util.MakeCompressedConn(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}

	codec := util.MakeMessagepackClientCodec(cc)
	codec = util.MakeLoggingClientCodec(serverAddress, codec, lg)

	return &rpcBackend{client: rpc.NewClientWithCodec(codec)}, nil
}

func (r *rpcBackend) call(serviceMethod string, args any, reply any) error {
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

func (r *rpcBackend) getPrecipURL(facility string, t time.Time) (string, time.Time, error) {
	args := PrecipURLArgs{Facility: facility, Time: t}
	var result PrecipURL
	if err := r.call(GetPrecipURLRPC, args, &result); err != nil {
		return "", time.Time{}, err
	}
	return result.URL, result.NextTime, nil
}

func (r *rpcBackend) getAtmosGrid(facility string, t time.Time, primaryAirport string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	args := GetAtmosArgs{Facility: facility, Time: t, PrimaryAirport: primaryAirport}
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

func (r *resourcesBackend) getAtmosGrid(facility string, tGet time.Time, primaryAirport string) (*AtmosByPointSOA, time.Time, time.Time, error) {
	atmosByTime, err := GetAtmosByTime(facility)
	if err != nil {
		// No atmos data for this facility; try to create fallback from METAR.
		atmos, fallbackErr := createFallbackAtmos(primaryAirport, tGet)
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

func createFallbackAtmos(primaryAirport string, t time.Time) (*AtmosByPointSOA, error) {
	metarMap, err := GetMETAR([]string{primaryAirport})
	if err != nil {
		return nil, err
	}
	metarSOA, ok := metarMap[primaryAirport]
	if !ok {
		return nil, fmt.Errorf("no METAR data for %s", primaryAirport)
	}

	metars := metarSOA.Decode(primaryAirport)
	if len(metars) == 0 {
		return nil, fmt.Errorf("no METAR data for %s", primaryAirport)
	}

	metar := METARForTime(metars, t)
	ap := av.DB.Airports[primaryAirport]

	return MakeFallbackAtmosFromMETAR(metar, ap.Location)
}
