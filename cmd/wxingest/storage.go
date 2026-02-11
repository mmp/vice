package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	fpath "path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/mmp/vice/util"
	"github.com/vmihailenco/msgpack/v5"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type StorageBackend interface {
	List(path string) (map[string]int64, error)
	ChanList(ctx context.Context, path string, ch chan<- string) error
	OpenRead(path string) (io.ReadCloser, error)
	ReadObject(path string, result any) error
	Store(path string, r io.Reader) (int64, error)
	StoreObject(path string, object any) (int64, error)
	Delete(path string) error
	Close()
}

// Pool a limited number of them to keep memory use under control.
var zstdEncoders chan *zstd.Encoder

func init() {
	const nenc = 16
	zstdEncoders = make(chan *zstd.Encoder, nenc)
	for range nenc {
		ze, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1))
		if err != nil {
			panic(err)
		}
		zstdEncoders <- ze
	}
}

type SinkWriter struct{}

func (w *SinkWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

type DryRunBackend struct {
	g StorageBackend // for read-only operations
}

func (d DryRunBackend) List(path string) (map[string]int64, error) {
	return d.g.List(path)
}

func (d DryRunBackend) ChanList(ctx context.Context, path string, ch chan<- string) error {
	return d.g.ChanList(ctx, path, ch)
}

func (d DryRunBackend) OpenRead(path string) (io.ReadCloser, error) {
	return d.g.OpenRead(path)
}

func (d DryRunBackend) ReadObject(path string, result any) error {
	return d.g.ReadObject(path, result)
}

func (d DryRunBackend) Store(path string, r io.Reader) (int64, error) {
	return io.Copy(&SinkWriter{}, r)
}

func (d DryRunBackend) StoreObject(path string, object any) (int64, error) {
	cw := &CountingWriter{Writer: &SinkWriter{}}

	zw := <-zstdEncoders
	defer func() { zstdEncoders <- zw }()
	zw.Reset(cw)

	if err := msgpack.NewEncoder(zw).Encode(object); err != nil {
		return 0, err
	} else if err := zw.Close(); err != nil {
		return 0, err
	}

	return cw.N, nil
}

func (d DryRunBackend) Delete(path string) error { return nil }

func (d DryRunBackend) Close() {}

type GCSBackend struct {
	ctx       context.Context
	client    *storage.Client
	bucket    *storage.BucketHandle
	listOps   atomic.Int64
	insertOps atomic.Int64
	deleteOps atomic.Int64
}

func MakeGCSBackend(bucketName string) (StorageBackend, error) {
	credsJSON := os.Getenv("VICE_GCS_CREDENTIALS")
	if credsJSON == "" {
		return nil, fmt.Errorf("VICE_GCS_CREDENTIALS environment variable not set")
	}

	client, err := storage.NewClient(context.Background(), option.WithCredentialsJSON([]byte(credsJSON)))
	if err != nil {
		return nil, err
	}

	return &GCSBackend{
		ctx:    context.Background(),
		client: client,
		bucket: client.Bucket(bucketName),
	}, nil
}

func (g *GCSBackend) List(path string) (map[string]int64, error) {
	path = fpath.Clean(path)
	query := storage.Query{
		Projection: storage.ProjectionNoACL,
		Prefix:     path,
	}

	it := g.bucket.Objects(g.ctx, &query)
	pager := iterator.NewPager(it, 1000, "")

	m := make(map[string]int64)
	for {
		var objects []*storage.ObjectAttrs
		pageToken, err := pager.NextPage(&objects)
		if err != nil {
			return nil, err
		}

		g.listOps.Add(1)

		for _, obj := range objects {
			if fpath.Clean(obj.Name) != path { // don't return the root ~folder
				m[obj.Name] = obj.Size
			}
		}

		if pageToken == "" {
			break
		}
	}

	return m, nil
}

func (g *GCSBackend) ChanList(ctx context.Context, path string, ch chan<- string) error {
	path = fpath.Clean(path)
	query := storage.Query{
		Projection: storage.ProjectionNoACL,
		Prefix:     path,
	}

	it := g.bucket.Objects(ctx, &query)
	pager := iterator.NewPager(it, 1000, "")

	for {
		var objects []*storage.ObjectAttrs
		pageToken, err := pager.NextPage(&objects)
		if err != nil {
			return err
		}

		g.listOps.Add(1)

		for _, obj := range objects {
			if fpath.Clean(obj.Name) != path { // don't return the root ~folder
				select {
				case <-ctx.Done():
					return ctx.Err()
				case ch <- obj.Name:
				}
			}
		}

		if pageToken == "" {
			return nil
		}
	}
}

func (g *GCSBackend) OpenRead(path string) (io.ReadCloser, error) {
	return g.bucket.Object(path).NewReader(g.ctx)
}

func (g *GCSBackend) ReadObject(path string, result any) error {
	r, err := g.OpenRead(path)
	if err != nil {
		return err
	}
	defer r.Close()

	zr, err := zstd.NewReader(r)
	if err != nil {
		return err
	}
	defer zr.Close()

	return msgpack.NewDecoder(zr).Decode(result)
}

func (g *GCSBackend) Store(path string, r io.Reader) (int64, error) {
	g.insertOps.Add(1)

	objw := g.bucket.Object(path).NewWriter(g.ctx)
	n, err := io.Copy(objw, r)
	if err != nil {
		return n, err
	}
	return n, objw.Close()
}

func (g *GCSBackend) StoreObject(path string, object any) (int64, error) {
	g.insertOps.Add(1)

	objw := g.bucket.Object(path).NewWriter(g.ctx)
	cw := &CountingWriter{Writer: objw}

	zw := <-zstdEncoders
	defer func() { zstdEncoders <- zw }()
	zw.Reset(cw)

	if err := msgpack.NewEncoder(zw).Encode(object); err != nil {
		return 0, err
	} else if err := zw.Close(); err != nil {
		return 0, err
	} else if err := objw.Close(); err != nil {
		return 0, err
	}

	return cw.N, nil
}

func (g *GCSBackend) Delete(path string) error {
	if !strings.HasPrefix(path, "scrape/") {
		return errors.New("Can only delete from scrape/")
	}
	g.deleteOps.Add(1)
	return g.bucket.Object(path).Delete(g.ctx)
}

func (g *GCSBackend) Close() { g.client.Close() }

func (g *GCSBackend) ReportClassAOperations() {
	list := g.listOps.Load()
	insert := g.insertOps.Load()
	del := g.deleteOps.Load()
	cost := float32(list+insert+del) / 1000 * 0.005
	LogInfo("GCS Class A operations: %d list, %d insert, %d delete, %d total (~$%.02f)", list, insert, del, list+insert+del, cost)
}

///////////////////////////////////////////////////////////////////////////

// TrackingBackend wraps a StorageBackend and tracks bytes uploaded/downloaded
type TrackingBackend struct {
	sb   StorageBackend
	up   atomic.Int64
	down atomic.Int64
}

func NewTrackingBackend(sb StorageBackend) *TrackingBackend {
	return &TrackingBackend{sb: sb}
}

func (t *TrackingBackend) List(path string) (map[string]int64, error) {
	return t.sb.List(path)
}

func (t *TrackingBackend) ChanList(ctx context.Context, path string, ch chan<- string) error {
	return t.sb.ChanList(ctx, path, ch)
}

func (t *TrackingBackend) OpenRead(path string) (io.ReadCloser, error) {
	rc, err := t.sb.OpenRead(path)
	if err != nil {
		return nil, err
	}
	// Wrap the reader to count bytes
	return &countingReadCloser{
		ReadCloser: rc,
		n:          &t.down,
	}, nil
}

func (t *TrackingBackend) ReadObject(path string, result any) error {
	// We don't need to track bytes for ReadObject since it uses OpenRead internally
	// and OpenRead already tracks the bytes
	return t.sb.ReadObject(path, result)
}

func (t *TrackingBackend) Store(path string, r io.Reader) (int64, error) {
	// Wrap the reader to count bytes
	cr := &countingReader{
		Reader: r,
		n:      &t.up,
	}
	n, err := t.sb.Store(path, cr)
	return n, err
}

func (t *TrackingBackend) StoreObject(path string, object any) (int64, error) {
	n, err := t.sb.StoreObject(path, object)
	if err == nil {
		t.up.Add(n)
	}
	return n, err
}

func (t *TrackingBackend) Delete(path string) error {
	return t.sb.Delete(path)
}

func (t *TrackingBackend) Close() {
	t.sb.Close()
}

func (t *TrackingBackend) MergeStats(other *TrackingBackend) {
	t.up.Add(other.up.Load())
	t.down.Add(other.down.Load())
}

func (t *TrackingBackend) ReportStats() {
	upBytes := t.up.Load()
	downBytes := t.down.Load()
	LogInfo("Transfer statistics: uploaded %s, downloaded %s, total %s",
		util.ByteCount(upBytes),
		util.ByteCount(downBytes),
		util.ByteCount(upBytes+downBytes))
}

// countingReader wraps an io.Reader and counts bytes read
type countingReader struct {
	io.Reader
	n *atomic.Int64
}

func (cr *countingReader) Read(p []byte) (n int, err error) {
	n, err = cr.Reader.Read(p)
	cr.n.Add(int64(n))
	return n, err
}

// countingReadCloser wraps an io.ReadCloser and counts bytes read
type countingReadCloser struct {
	io.ReadCloser
	n *atomic.Int64
}

func (trc *countingReadCloser) Read(p []byte) (n int, err error) {
	n, err = trc.ReadCloser.Read(p)
	trc.n.Add(int64(n))
	return n, err
}

///////////////////////////////////////////////////////////////////////////
// LocalBackend - writes files to a local directory

type LocalBackend struct {
	dir         string
	gcsForReads StorageBackend // for read operations (downloading HRRR, etc.)
	totalBytes  atomic.Int64
	totalFiles  atomic.Int64
	bytesPerFac sync.Map // facilityID -> *atomic.Int64
}

func MakeLocalBackend(dir string, gcsForReads StorageBackend) (*LocalBackend, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory %s: %w", dir, err)
	}
	return &LocalBackend{
		dir:         dir,
		gcsForReads: gcsForReads,
	}, nil
}

func (l *LocalBackend) List(path string) (map[string]int64, error) {
	return l.gcsForReads.List(path)
}

func (l *LocalBackend) ChanList(ctx context.Context, path string, ch chan<- string) error {
	return l.gcsForReads.ChanList(ctx, path, ch)
}

func (l *LocalBackend) OpenRead(path string) (io.ReadCloser, error) {
	return l.gcsForReads.OpenRead(path)
}

func (l *LocalBackend) ReadObject(path string, result any) error {
	return l.gcsForReads.ReadObject(path, result)
}

func (l *LocalBackend) Store(path string, r io.Reader) (int64, error) {
	fullPath := fpath.Join(l.dir, path)
	if err := os.MkdirAll(fpath.Dir(fullPath), 0755); err != nil {
		return 0, err
	}

	f, err := os.Create(fullPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n, err := io.Copy(f, r)
	if err != nil {
		return n, err
	}

	l.totalBytes.Add(n)
	l.totalFiles.Add(1)
	return n, nil
}

func (l *LocalBackend) StoreObject(path string, object any) (int64, error) {
	fullPath := fpath.Join(l.dir, path)
	if err := os.MkdirAll(fpath.Dir(fullPath), 0755); err != nil {
		return 0, err
	}

	f, err := os.Create(fullPath)
	if err != nil {
		return 0, err
	}

	cw := &CountingWriter{Writer: f}

	zw := <-zstdEncoders
	defer func() { zstdEncoders <- zw }()
	zw.Reset(cw)

	if err := msgpack.NewEncoder(zw).Encode(object); err != nil {
		f.Close()
		return 0, err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}

	l.totalBytes.Add(cw.N)
	l.totalFiles.Add(1)

	// Track per-facility bytes from path like "atmos/ZDC/2026-01-28T00:00:00Z.msgpack.zst"
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		facilityID := parts[1]
		counter, _ := l.bytesPerFac.LoadOrStore(facilityID, &atomic.Int64{})
		counter.(*atomic.Int64).Add(cw.N)
	}

	return cw.N, nil
}

func (l *LocalBackend) Delete(path string) error {
	return nil // no-op for local backend
}

func (l *LocalBackend) Close() {
	if l.gcsForReads != nil {
		l.gcsForReads.Close()
	}
}

func (l *LocalBackend) ReportStats() {
	LogInfo("Local output statistics: %d files, %s total",
		l.totalFiles.Load(), util.ByteCount(l.totalBytes.Load()))

	// Report per-facility sizes
	type facSize struct {
		id   string
		size int64
	}
	var sizes []facSize
	l.bytesPerFac.Range(func(key, value any) bool {
		sizes = append(sizes, facSize{id: key.(string), size: value.(*atomic.Int64).Load()})
		return true
	})

	// Sort by size descending
	slices.SortFunc(sizes, func(a, b facSize) int {
		return int(b.size - a.size)
	})

	LogInfo("Per-facility sizes:")
	for _, fs := range sizes {
		LogInfo("  %s: %s", fs.id, util.ByteCount(fs.size))
	}
}
