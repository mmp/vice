package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	fpath "path/filepath"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
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
