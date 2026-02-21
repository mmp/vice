package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
	"golang.org/x/sync/errgroup"
)

var dryRun = flag.Bool("dryrun", false, "Don't upload to GCS or archive local files")
var nWorkers = flag.Int("nworkers", 16, "Number of worker goroutines for concurrent uploads")
var profile = flag.Bool("profile", false, "Profile CPU/heap usage")
var hrrrQuick = flag.Bool("hrrrquick", false, "Fast-path HRRR run, no upload")
var localOutput = flag.String("local-output", "", "Write output to local directory instead of GCS (for testing)")
var singleTime = flag.String("single-time", "", "Process only a single timestamp (format: 2006-01-02T15:04:05Z)")

// Cleanup coordination for signal handlers
var (
	cleanupFuncs []func()
	cleanupMu    sync.Mutex
	exitOnce     sync.Once
)

func registerCleanup(f func()) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	cleanupFuncs = append(cleanupFuncs, f)
}

func runAllCleanups() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	for _, f := range cleanupFuncs {
		f()
	}
}

func setupSignalHandler() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "Caught signal, cleaning up...")
		runAllCleanups()
		fmt.Fprintln(os.Stderr, "Cleanup complete, exiting")
		exitOnce.Do(func() { os.Exit(0) })
	}()
}

func main() {
	initZstdEncoders()

	const bucketName = "vice-wx"

	flag.Parse()

	usage := func() {
		fmt.Fprintf(os.Stderr, "usage: wxingest [flags] [metar|precip|atmos]...\nwhere [flags] may be:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	av.InitDB()

	setupSignalHandler()

	if *profile {
		prof, err := util.CreateProfiler("wxingest.cpu.prof", "wxingest.heap.prof")
		if err != nil {
			panic(err)
		}
		defer prof.Cleanup()
		registerCleanup(prof.Cleanup)
	}

	gcsBackend, err := MakeGCSBackend(bucketName)
	if err != nil {
		LogFatal("%v", err)
	}

	var sb StorageBackend
	var localBackend *LocalBackend

	if *localOutput != "" {
		// Use local backend for writes, GCS for reads (to get METAR/precip times)
		localBackend, err = MakeLocalBackend(*localOutput, gcsBackend)
		if err != nil {
			LogFatal("%v", err)
		}
		sb = localBackend
		LogInfo("Using local output directory: %s", *localOutput)
	} else {
		sb = gcsBackend
		if *dryRun {
			sb = &DryRunBackend{g: sb}
		}
		// Wrap with tracking backend to track bytes uploaded/downloaded
		sb = NewTrackingBackend(sb)
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

	// Report the total bytes transferred
	if localBackend != nil {
		localBackend.ReportStats()
	} else if tb, ok := sb.(*TrackingBackend); ok {
		tb.ReportStats()
	}

	// Report GCS Class A operations
	if gcb, ok := gcsBackend.(*GCSBackend); ok {
		gcb.ReportClassAOperations()
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
