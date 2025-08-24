package main

import (
	"bytes"
	_ "embed"
	"encoding/gob"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
)

func ingestWX(st StorageBackend) {
	flags := ArchiverFlagsNoCheckArchived
	if *dryRun {
		flags = flags | ArchiverFlagsDryRun
	}
	arch, err := MakeArchiver("WX", flags)
	if err != nil {
		LogFatal("Archiver: %v", err)
	}

	ch := make(chan string)
	var wg sync.WaitGroup
	var totalBytes int64
	for range *nWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range ch {
				n, err := processWX(st, path, arch)
				if err != nil {
					LogError("%s: %v", path, err)
				}
				atomic.AddInt64(&totalBytes, n)
			}
		}()
	}

	if err := EnqueueFiles("WX", ch); err != nil {
		LogFatal("%v", err)
	}
	wg.Wait()

	LogInfo("Total of %s of WX stored this run", util.ByteCount(totalBytes))
}

func processWX(st StorageBackend, path string, arch *Archiver) (int64, error) {
	// Parse time
	t, err := time.Parse(time.RFC3339, strings.TrimSuffix(filepath.Base(path), ".gob"))
	if err != nil {
		return 0, err
	}
	t = t.UTC()

	scraped, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	type WXScraped struct {
		PNG        []byte
		Resolution int
		Latitude   float32
		Longitude  float32
	}
	var wxs WXScraped
	if err := gob.NewDecoder(bytes.NewReader(scraped)).Decode(&wxs); err != nil {
		return 0, err
	}

	img, err := png.Decode(bytes.NewReader(wxs.PNG))
	if err != nil {
		return 0, err
	}

	type WXProcessed struct {
		DBZ        []byte
		Resolution int
		Latitude   float32
		Longitude  float32
	}
	wxp := WXProcessed{
		DBZ:        util.DeltaEncode(wx.RadarImageToDBZ(img)),
		Resolution: wxs.Resolution,
		Latitude:   wxs.Latitude,
		Longitude:  wxs.Longitude,
	}

	tracon := strings.Split(path, "/")[1]
	fn := fmt.Sprintf("WX/%s/%d/%02d/%02d/%s.msgpack.zst", tracon, t.Year(), t.Month(), t.Day(), t.Format("150405"))
	n, err := st.Store(fn, wxp)

	if err == nil {
		err = arch.Archive(path)
	}

	return n, err
}
