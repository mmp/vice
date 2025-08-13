package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mmp/vice/util"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/vmihailenco/msgpack/v5"
	"google.golang.org/api/option"
)

type StorageBackend interface {
	Store(path string, object any) (int64, error)
	Close()
}

type SinkWriter struct{}

func (w *SinkWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

type DryRunBackend struct{}

func (d DryRunBackend) Store(path string, object any) (int64, error) {
	cw := &CountingWriter{Writer: &SinkWriter{}}

	if zw, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1)); err != nil {
		return 0, err
	} else if err := msgpack.NewEncoder(zw).Encode(object); err != nil {
		return 0, err
	} else if err := zw.Close(); err != nil {
		return 0, err
	}

	LogInfo("%s: would have stored %s if not -dryrun", path, util.ByteCount(cw.N))
	return cw.N, nil
}

func (d DryRunBackend) Close() {}

type GCSBackend struct {
	ctx    context.Context
	client *storage.Client
	bucket *storage.BucketHandle
}

func MakeGCSBackend(bucketName string) (*GCSBackend, error) {
	gcs := &GCSBackend{ctx: context.Background()}

	var err error
	gcs.client, gcs.bucket, err = gcsInit(gcs.ctx, bucketName)
	if err != nil {
		return nil, err
	}
	return gcs, nil
}

func gcsInit(ctx context.Context, bucketName string) (*storage.Client, *storage.BucketHandle, error) {
	credsJSON := os.Getenv("VICE_GCS_CREDENTIALS")
	if credsJSON == "" {
		return nil, nil, fmt.Errorf("VICE_GCS_CREDENTIALS environment variable not set")
	}

	client, err := storage.NewClient(ctx, option.WithCredentialsJSON([]byte(credsJSON)))
	if err != nil {
		return nil, nil, err
	}

	return client, client.Bucket(bucketName), nil
}

func (g GCSBackend) Store(path string, object any) (int64, error) {
	objw := g.bucket.Object(path).NewWriter(g.ctx)
	cw := &CountingWriter{Writer: objw}

	if zw, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1)); err != nil {
		return 0, err
	} else if err := msgpack.NewEncoder(zw).Encode(object); err != nil {
		return 0, err
	} else if err := zw.Close(); err != nil {
		return 0, err
	} else if err := objw.Close(); err != nil {
		return 0, err
	}

	LogInfo("%s: uploaded, %s", path, util.ByteCount(cw.N))
	return cw.N, nil
}

func (g GCSBackend) Close() { g.client.Close() }

type FileBackend struct {
	base string
}

func MakeFileBackend(base string) *FileBackend {
	return &FileBackend{base: base}
}

func (fb FileBackend) Store(path string, object any) (int64, error) {
	path = filepath.Join(fb.base, path)

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return 0, err
	}

	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}

	cw := &CountingWriter{Writer: f}

	if zw, err := zstd.NewWriter(cw, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1)); err != nil {
		f.Close()
		return 0, err
	} else if err := msgpack.NewEncoder(zw).Encode(object); err != nil {
		f.Close()
		return 0, err
	} else if err := zw.Close(); err != nil {
		f.Close()
		return 0, err
	} else if err := f.Close(); err != nil {
		return 0, err
	}

	LogInfo("%s: wrote %s", path, util.ByteCount(cw.N))
	return cw.N, nil

}

func (FileBackend) Close() {}
