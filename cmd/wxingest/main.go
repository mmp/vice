package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const bucketName = "vice-wx"

var dryRun = flag.Bool("dryrun", false, "Don't upload to GCS or archive local files")
var local = flag.Bool("local", false, "Store processed files locally")
var doArchive = flag.Bool("archive", true, "Archive files (locally or to GCS)")
var nWorkers = flag.Int("nworkers", 32, "Number of worker goroutines for concurrent uploads")

func main() {
	flag.Parse()

	usage := func() {
		fmt.Fprintf(os.Stderr, "usage: wxingest [flags] <metar|wx|hrrr> <ingest-dir>\nwhere [flags] may be:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if len(flag.Args()) != 2 {
		usage()
	}

	dir := flag.Args()[1]
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", dir, err)
		os.Exit(1)
	}

	var st StorageBackend
	if *dryRun {
		if *local {
			fmt.Fprintf(os.Stderr, "Can't give both -local and -dryrun")
			os.Exit(1)
		}
		st = &DryRunBackend{}
	} else if *local {
		st = MakeFileBackend(bucketName)

		if *doArchive {
			fmt.Fprintf(os.Stderr, "Disabling -archive for local run\n")
			*doArchive = false
		}
	} else {
		var err error
		st, err = MakeGCSBackend(bucketName)
		if err != nil {
			LogFatal("%v", err)
		}
	}

	launchHTTPServer()

	switch flag.Args()[0] {
	case "metar", "METAR":
		ingestMETAR(st)
	case "wx", "WX":
		ingestWX(st)
	case "hrrr", "HRRR":
		ingestHRRR(st)
	default:
		usage()
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

func EnqueueFiles(dir string, ch chan<- string) error {
	err := filepath.WalkDir(dir, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() || strings.HasSuffix(path, ".tmp") {
			return nil
		}
		ch <- path
		return nil
	})
	close(ch)
	return err
}

var startTime time.Time

func launchHTTPServer() {
	startTime = time.Now()

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
		os.Exit(1)
	}
}
