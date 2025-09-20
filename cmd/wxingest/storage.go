package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	fpath "path/filepath"
	"strings"
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
	ChanList(path string, ch chan<- string) error
	OpenRead(path string) (io.ReadCloser, error)
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

func (d DryRunBackend) ChanList(path string, ch chan<- string) error {
	return d.g.ChanList(path, ch)
}

func (d DryRunBackend) OpenRead(path string) (io.ReadCloser, error) {
	return d.g.OpenRead(path)
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
	ctx    context.Context
	client *storage.Client
	bucket *storage.BucketHandle
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

func (g GCSBackend) List(path string) (map[string]int64, error) {
	path = fpath.Clean(path)
	query := storage.Query{
		Projection: storage.ProjectionNoACL,
		Prefix:     path,
	}

	m := make(map[string]int64)
	it := g.bucket.Objects(g.ctx, &query)
	for {
		if obj, err := it.Next(); err == iterator.Done {
			break
		} else if err != nil {
			return nil, err
		} else if fpath.Clean(obj.Name) != path { // don't return the root ~folder
			m[obj.Name] = obj.Size
		}
	}

	return m, nil
}

func (g GCSBackend) ChanList(path string, ch chan<- string) error {
	path = fpath.Clean(path)
	query := storage.Query{
		Projection: storage.ProjectionNoACL,
		Prefix:     path,
	}

	it := g.bucket.Objects(g.ctx, &query)
	for {
		select {
		case <-g.ctx.Done():
			return g.ctx.Err()
		default:
		}

		if obj, err := it.Next(); err == iterator.Done {
			return nil
		} else if err != nil {
			return err
		} else if fpath.Clean(obj.Name) != path { // don't return the root ~folder
			ch <- obj.Name
		}
	}
}

func (g GCSBackend) OpenRead(path string) (io.ReadCloser, error) {
	return g.bucket.Object(path).NewReader(g.ctx)
}

func (g GCSBackend) Store(path string, r io.Reader) (int64, error) {
	objw := g.bucket.Object(path).NewWriter(g.ctx)
	n, err := io.Copy(objw, r)
	if err != nil {
		return n, err
	}
	return n, objw.Close()
}

func (g GCSBackend) StoreObject(path string, object any) (int64, error) {
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

func (g GCSBackend) Delete(path string) error {
	if !strings.HasPrefix(path, "scrape/") {
		return errors.New("Can only delete from scrape/")
	}
	return g.bucket.Object(path).Delete(g.ctx)
}

func (g GCSBackend) Close() { g.client.Close() }

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

func (t *TrackingBackend) ChanList(path string, ch chan<- string) error {
	return t.sb.ChanList(path, ch)
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
