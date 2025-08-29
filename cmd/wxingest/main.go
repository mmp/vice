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
	"strings"

	av "github.com/mmp/vice/aviation"
	"github.com/mmp/vice/util"
)

var dryRun = flag.Bool("dryrun", false, "Don't upload to GCS or archive local files")
var nWorkers = flag.Int("nworkers", 32, "Number of worker goroutines for concurrent uploads")
var profile = flag.Bool("profile", false, "Profile CPU/heap usage")
var hrrrQuick = flag.Bool("hrrrquick", false, "Fast-path HRRR run, no upload")

func main() {
	const bucketName = "vice-wx"

	flag.Parse()

	usage := func() {
		fmt.Fprintf(os.Stderr, "usage: wxingest [flags] [metar|wx|hrrr]...\nwhere [flags] may be:\n")
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

	if len(flag.Args()) == 0 {
		ingestMETAR(sb)
		ingestWX(sb)
		ingestHRRR(sb)
	} else {
		for _, a := range flag.Args() {
			switch strings.ToLower(a) {
			case "metar":
				ingestMETAR(sb)
			case "wx":
				ingestWX(sb)
			case "hrrr":
				ingestHRRR(sb)
			default:
				usage()
			}
		}
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

func EnqueueObjects(sb StorageBackend, base string, ch chan<- string) error {
	objs, err := sb.List(base)
	if err == nil {
		LogInfo("%s: found %d objects", base, len(objs))
		for name := range objs {
			ch <- name
		}
	}
	close(ch)
	return err
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
		os.Exit(1)
	}
}
