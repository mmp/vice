package main

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"

	"cloud.google.com/go/storage"
	"github.com/mmp/vice/util"
	"google.golang.org/api/iterator"
)

type Archiver struct {
	existing map[string]int64
	flags    int
	ctx      context.Context
	bucket   *storage.BucketHandle
	base     string
}

const (
	ArchiverFlagsDryRun = 1 << iota
	ArchiverFlagsNoCheckArchived
	ArchiverFlagsArchiveStorageClass
)

func MakeArchiver(base string, flags int) (*Archiver, error) {
	if !*doArchive {
		return nil, nil
	}

	ctx := context.Background()
	_, bucket, err := gcsInit(ctx, bucketName)
	if err != nil {
		return nil, err
	}

	arch := &Archiver{
		existing: make(map[string]int64),
		flags:    flags,
		ctx:      ctx,
		bucket:   bucket,
		base:     util.Select(flags&ArchiverFlagsArchiveStorageClass != 0, "archive-archive-class", "archive"),
	}

	if flags&ArchiverFlagsDryRun == 0 && flags&ArchiverFlagsNoCheckArchived == 0 {
		// See what has been archived already
		query := storage.Query{
			Projection: storage.ProjectionNoACL,
			Prefix:     path.Join(arch.base, base),
		}
		LogInfo("Archiver: listing existing objects in %q", query.Prefix)

		it := bucket.Objects(ctx, &query)
		for {
			if obj, err := it.Next(); err == iterator.Done {
				break
			} else if err != nil {
				return nil, err
			} else {
				arch.existing[obj.Name] = obj.Size
			}
		}

		LogInfo("Archiver: found %d objects, %s", arch.ArchivedFiles(), ByteCount(arch.ArchivedFileSize()))
	}

	return arch, nil
}

func (a *Archiver) ArchivedFiles() int {
	if a == nil {
		return 0
	}
	return len(a.existing)
}

func (a *Archiver) ArchivedFileSize() int64 {
	if a == nil {
		return 0
	}

	var s int64
	for _, sz := range a.existing {
		s += sz
	}
	return s
}

// Returned bool indicates whether we want to upload; error must be nil for
// it to have been successful though. Note that it is safe for multiple
// goroutines to call Archive simultaneously.
func (a *Archiver) Archive(fn string) (err error) {
	if a == nil {
		return nil
	}

	if a.flags&ArchiverFlagsDryRun != 0 {
		return
	}

	var fs os.FileInfo
	fs, err = os.Stat(fn)
	if err != nil {
		return
	}

	objfn := path.Join(a.base, fn)

	if sz, ok := a.existing[objfn]; !ok || sz != fs.Size() {
		var f *os.File
		if f, err = os.Open(fn); err != nil {
			return
		}
		defer f.Close()

		objw := a.bucket.Object(objfn).NewWriter(a.ctx)

		if a.flags&ArchiverFlagsArchiveStorageClass != 0 {
			objw.StorageClass = "ARCHIVE"
		}

		if _, err = io.Copy(objw, f); err != nil {
			return
		}

		if err = objw.Close(); err == nil {
			LogInfo("%s->%s: archived to GCS", fn, objfn)
		}
	}

	// Always archive locally
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(objfn), 0755); err == nil {
			if err = os.Rename(fn, objfn); err == nil {
				LogInfo("%s->%s: renamed locally", fn, objfn)
			}
		}
	}

	return
}
