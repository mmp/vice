package main

import (
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
	"github.com/mmp/vice/wx"
	"golang.org/x/sync/errgroup"
)

var dryRun = flag.Bool("dryrun", false, "Don't upload to GCS or archive local files")
var nWorkers = flag.Int("nworkers", 32, "Number of worker goroutines for concurrent uploads")
var profile = flag.Bool("profile", false, "Profile CPU/heap usage")
var hrrrQuick = flag.Bool("hrrrquick", false, "Fast-path HRRR run, no upload")
var localOutput = flag.String("local-output", "", "Write output to local directory instead of GCS (for testing)")
var singleTime = flag.String("single-time", "", "Process only a single timestamp (format: 2006-01-02T15:04:05Z)")
var manifestsOnly = flag.Bool("manifests-only", false, "Regenerate manifests without processing scraped data")
var facilitiesFile = flag.String("facilities", "facilities.json", "`file` with the facility list written by \"viceserver -wxfacilities\"; used by atmos ingest")

// taskIndex and taskCount identify this process's shard when running as a
// multi-task Cloud Run job; they are 0 and 1 otherwise.
var taskIndex, taskCount = cloudRunTask()

func cloudRunTask() (int, int) {
	idx, err1 := strconv.Atoi(os.Getenv("CLOUD_RUN_TASK_INDEX"))
	count, err2 := strconv.Atoi(os.Getenv("CLOUD_RUN_TASK_COUNT"))
	if err1 != nil || err2 != nil || count < 1 {
		return 0, 1
	}
	return idx, count
}

// inCloudRunJob reports whether we are running as a Cloud Run job; if so,
// manifest generation is deferred to a separate -manifests-only execution
// after all of the job's tasks have completed.
func inCloudRunJob() bool { return os.Getenv("CLOUD_RUN_JOB") != "" }

// shardOwns reports whether this process is responsible for the given work
// item; work is partitioned by hash across Cloud Run job tasks.
func shardOwns(key string) bool {
	if taskCount == 1 {
		return true
	}
	h := fnv.New32a()
	io.WriteString(h, key)
	return int(h.Sum32())%taskCount == taskIndex
}

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
	// run's error is reported by exit status as well as in the log: the
	// Cloud Run pipeline in cloudrun/run.sh runs the stages in dependency
	// order and must stop rather than build on incomplete data.
	if err := run(); err != nil {
		LogError("%v", err)
		os.Exit(1)
	}
}

func run() error {
	initZstdEncoders()

	const bucketName = "vice-wx"

	flag.Parse()

	usage := func() {
		fmt.Fprintf(os.Stderr, "usage: wxingest [flags] [metar|precip|atmos|atmosavg|atmosseries|tfr]...\nwhere [flags] may be:\n")
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

	if !inCloudRunJob() {
		launchHTTPServer()
	}

	var eg errgroup.Group
	if len(flag.Args()) == 0 {
		eg.Go(func() error { return ingestMETAR(sb) })
		eg.Go(func() error { return ingestPrecip(sb) })
		eg.Go(func() error { return ingestHRRR(sb) })
		eg.Go(func() error { return ingestTFRs(sb) })
	} else {
		for _, a := range flag.Args() {
			switch strings.ToLower(a) {
			case "metar":
				eg.Go(func() error { return ingestMETAR(sb) })
			case "precip":
				eg.Go(func() error { return ingestPrecip(sb) })
			case "atmos":
				eg.Go(func() error { return ingestHRRR(sb) })
			case "atmosavg":
				eg.Go(func() error { return backfillAtmosAvg(sb) })
			case "atmosseries":
				eg.Go(func() error { return rollupAtmosSeries(sb) })
			case "tfr", "tfrs":
				eg.Go(func() error { return ingestTFRs(sb) })
			default:
				usage()
			}
		}
	}

	ingestErr := eg.Wait()

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

	return ingestErr
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

func retry(attempts int, sleep time.Duration, fn func() error) error {
	var err error
	for range attempts {
		if err = fn(); err == nil {
			return nil
		}
		LogError("retryable error (will retry in %s): %v", sleep, err)
		time.Sleep(sleep)
		sleep *= 2
	}
	return err
}

// readWithRetry reads an object from storage, retrying transient I/O errors.
func readWithRetry(sb StorageBackend, path string) ([]byte, error) {
	var b []byte
	err := retry(3, 5*time.Second, func() error {
		r, err := sb.OpenRead(path)
		if err != nil {
			return err
		}
		defer r.Close()
		b, err = io.ReadAll(r)
		return err
	})
	return b, err
}

// storeObjectLocal writes a msgpack+zstd encoded object to a local file.
// Used as a fallback when GCS uploads fail.
func storeObjectLocal(localPath string, object any) error {
	b, err := encodeObject(object)
	if err != nil {
		return err
	}
	return os.WriteFile(localPath, b, 0644)
}

// storeManifest uploads a manifest, retrying on failure and finally falling
// back to saving it in a local file that can be uploaded manually.
func storeManifest(sb StorageBackend, manifest *wx.Manifest, objPath, localFile string) error {
	raw := manifest.RawManifest()
	var n int64
	err := retry(3, 10*time.Second, func() error {
		var err error
		n, err = sb.StoreObject(objPath, raw)
		return err
	})
	if err != nil {
		if localErr := storeObjectLocal(localFile, raw); localErr != nil {
			LogError("MANIFEST WRITE FAILED for %s and local save also failed: upload: %v, local: %v", objPath, err, localErr)
		} else {
			LogError("MANIFEST WRITE FAILED for %s: %v -- saved to %s; upload to gs://vice-wx/%s", objPath, err, localFile, objPath)
		}
		return err
	}

	LogInfo("Stored %d items in %s (%s)", manifest.TotalEntries(), objPath, util.ByteCount(n))
	return nil
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
