package main

import (
	"flag"
	"fmt"
	"io"
	"iter"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"slices"
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
	"golang.org/x/sync/errgroup"
)

var dryRun = flag.Bool("dryrun", false, "Don't upload to GCS or archive local files")
var nWorkers = flag.Int("nworkers", 32, "Number of worker goroutines for concurrent uploads")
var profile = flag.Bool("profile", false, "Profile CPU/heap usage")
var hrrrQuick = flag.Bool("hrrrquick", false, "Fast-path HRRR run, no upload")

func main() {
	const bucketName = "vice-wx"

	flag.Parse()

	usage := func() {
		fmt.Fprintf(os.Stderr, "usage: wxingest [flags] [metar|precip|atmos]...\nwhere [flags] may be:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	av.InitDB()

	if *profile {
		prof, err := util.CreateProfiler("wxingest.cpu.prof", "wxingest.heap.prof")
		if err != nil {
			panic(err)
		}
		defer prof.Cleanup()
	}

	sb, err := MakeGCSBackend(bucketName)
	if err != nil {
		LogFatal("%v", err)
	}
	if *dryRun {
		sb = &DryRunBackend{g: sb}
	}
	defer sb.Close()

	launchHTTPServer()

	var eg errgroup.Group
	if len(flag.Args()) == 0 {
		eg.Go(func() error { return ingestMETAR(sb) })
		eg.Go(func() error { return ingestPrecip(sb) })
		eg.Go(func() error { return ingestHRRR(sb) })
	} else {
		for _, a := range flag.Args() {
			switch strings.ToLower(a) {
			case "metar":
				eg.Go(func() error { return ingestMETAR(sb) })
			case "precip":
				eg.Go(func() error { return ingestPrecip(sb) })
			case "atmos":
				eg.Go(func() error { return ingestHRRR(sb) })
			default:
				usage()
			}
		}
	}

	if err := eg.Wait(); err != nil {
		LogError("%v", err)
	}
}

type CountingWriter struct {
	io.Writer
	N int64
}

func (w *CountingWriter) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	w.N += int64(n)
	return n, err
}

func LogInfo(msg string, args ...any) {
	log.Printf("INFO "+msg, args...)
}

func LogError(msg string, args ...any) {
	log.Printf("ERROR "+msg, args...)
}

func LogFatal(msg string, args ...any) {
	log.Printf("FATAL "+msg, args...)
	os.Exit(1)
}

func generateManifests(sb StorageBackend, prefix string, items iter.Seq[string]) error {
	LogInfo("%s: updating manifests", prefix)

	var eg errgroup.Group
	ch := make(chan string)
	eg.Go(func() error {
		defer close(ch)
		for item := range items {
			ch <- item
		}
		return nil
	})

	for range *nWorkers {
		eg.Go(func() error {
			for item := range ch {
				paths, err := sb.List(filepath.Join(prefix, item))
				if err != nil {
					return err
				}
				var manifest []string
				for path := range paths {
					file := filepath.Base(path)
					if file != "manifest.msgpack.zst" {
						manifest = append(manifest, file)
					}
				}
				slices.Sort(manifest)

				mpath := filepath.Join(prefix, item, "manifest.msgpack.zst")
				sb.StoreObject(mpath, manifest)
				LogInfo("Stored %d items in %s", len(manifest), mpath)
			}
			return nil
		})
	}

	return eg.Wait()
}

func launchHTTPServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	if listener, err := net.Listen("tcp", ":8002"); err == nil {
		LogInfo("Launching HTTP server on port 8002")
		go http.Serve(listener, mux)
	} else {
		fmt.Fprintf(os.Stderr, "Unable to start HTTP server: %v", err)
	}
}
