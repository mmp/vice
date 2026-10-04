// cmd/scraperoutes/cloudrun.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

// Spreading a run across Cloud Run tasks. The queue is decided locally, where
// the flight data is (-plan); each task fetches every task-count'th pair of it
// with the usual delay between requests and leaves what it found in a file of
// its own (-worker); and those are recorded in the database locally once the
// tasks are done (-merge). cloudrun/run.sh moves the files between here and
// Cloud Storage and runs the job.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	av "github.com/mmp/vice/aviation"
)

const (
	planFile   = "pairs.json"
	resultsDir = "results"
)

// plan is what the tasks are handed: the pairs to fetch, keyed as in the
// database and most in need first, and how long to wait between requests.
type plan struct {
	Delay time.Duration `json:"delay"`
	Pairs []string      `json:"pairs"`
}

func writePlan(dir string, pairs []pair, delay time.Duration) {
	p := plan{Delay: delay}
	for _, pr := range pairs {
		p.Pairs = append(p.Pairs, pr.key())
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, planFile), b, 0o644)
	}
	if err != nil {
		fmt.Printf("%s: %v\n", dir, err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %d pairs to %s\n", len(p.Pairs), filepath.Join(dir, planFile))
}

// cloudRunTask returns which of the job's tasks this is and how many there
// are; outside a Cloud Run job, it is the only one.
func cloudRunTask() (int, int) {
	idx, err1 := strconv.Atoi(os.Getenv("CLOUD_RUN_TASK_INDEX"))
	count, err2 := strconv.Atoi(os.Getenv("CLOUD_RUN_TASK_COUNT"))
	if err1 != nil || err2 != nil || count < 1 {
		return 0, 1
	}
	return idx, count
}

// maxConsecutiveFailures is how many fetches in a row a task lets fail before
// it decides it has been shut out and stops: the rest of its share would fail
// the same way.
const maxConsecutiveFailures = 5

// runWorker fetches this task's share of the plan at loc, rewriting its
// results after every fetch so that an interrupted task keeps what it paid
// for. A retried task picks up where the failed attempt left off. It exits
// with an error if it was shut out, which gives Cloud Run's retry, on another
// instance, a chance at the rest.
func runWorker(loc string) {
	sh, err := openShared(loc)
	if err != nil {
		fmt.Printf("%s: %v\n", loc, err)
		os.Exit(1)
	}

	var p plan
	b, err := sh.read(planFile)
	if err == nil {
		err = json.Unmarshal(b, &p)
	}
	if err != nil {
		fmt.Printf("%s: %v\n", planFile, err)
		os.Exit(1)
	}

	idx, count := cloudRunTask()
	resultsFile := path.Join(resultsDir, fmt.Sprintf("task-%03d.json", idx))
	results := make(map[string]av.ScrapedRouteSet)
	if b, err := sh.read(resultsFile); err == nil {
		if err := json.Unmarshal(b, &results); err != nil {
			fmt.Printf("%s: %v\n", resultsFile, err)
			os.Exit(1)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("%s: %v\n", resultsFile, err)
		os.Exit(1)
	}
	save := func() {
		b, err := json.MarshalIndent(results, "", "  ")
		if err == nil {
			err = sh.write(resultsFile, b)
		}
		if err != nil {
			fmt.Printf("%s: %v\n", resultsFile, err)
			os.Exit(1)
		}
	}
	// Written up front, so that there is something to merge even when this
	// task's share is empty.
	save()

	var share []string
	for i, key := range p.Pairs {
		if i%count != idx {
			continue
		}
		if _, ok := results[key]; !ok {
			share = append(share, key)
		}
	}
	fmt.Printf("Task %d of %d: %d pairs to fetch, %d already fetched\n", idx, count, len(share), len(results))

	// Starting the tasks staggered across the delay spreads their requests
	// evenly over it, for whatever share the same address.
	if len(share) > 0 {
		time.Sleep(time.Duration(idx) * p.Delay / time.Duration(count))
	}

	client := &http.Client{Timeout: 30 * time.Second}
	failures := 0
	for i, key := range share {
		if i > 0 {
			time.Sleep(p.Delay)
		}
		from, to, ok := parsePairKey(key)
		if !ok {
			fmt.Printf("%q: not a city pair\n", key)
			continue
		}

		routes, err := fetchRoutes(client, from, to, domestic(from), domestic(to))
		if err != nil {
			fmt.Printf("%s->%s: %v\n", from, to, err)
			failures++
			if failures == maxConsecutiveFailures {
				fmt.Printf("Giving up after %d failures in a row with %d pairs left\n", failures, len(share)-i-1)
				os.Exit(1)
			}
			continue
		}
		failures = 0
		routes = cullRareRoutes(routes, domestic(from), domestic(to))

		if len(routes) == 0 {
			fmt.Printf("%s->%s: no routes found\n", from, to)
		}
		for _, r := range routes {
			fmt.Printf("%s->%s: %q %s\n", from, to, r.Route, describe(r))
		}
		results[key] = av.ScrapedRouteSet{Updated: time.Now().Format("2006-01-02"), Routes: routes}
		save()
	}
}

// mergeResults records what the tasks fetched into the database. An entry
// only replaces one fetched no later than it was.
func mergeResults(dir, dbPath string, dryRun bool) {
	var p plan
	b, err := os.ReadFile(filepath.Join(dir, planFile))
	if err == nil {
		err = json.Unmarshal(b, &p)
	}
	if err != nil {
		fmt.Printf("%s: %v\n", planFile, err)
		os.Exit(1)
	}

	files, err := filepath.Glob(filepath.Join(dir, resultsDir, "*.json"))
	if err != nil {
		fmt.Printf("%v\n", err)
		os.Exit(1)
	}

	sets := readRouteSets(dbPath)
	fetched, withRoutes := 0, 0
	for _, file := range files {
		for key, set := range readRouteSets(file) {
			fetched++
			if len(set.Routes) > 0 {
				withRoutes++
			}
			if old, ok := sets[key]; !ok || old.Updated <= set.Updated {
				sets[key] = set
			}
		}
	}
	fmt.Printf("%d tasks fetched %d of %d pairs, %d with routes\n", len(files), fetched, len(p.Pairs), withRoutes)

	if dryRun {
		fmt.Printf("Dry run: %s left unchanged\n", dbPath)
	} else {
		writeRouteSets(dbPath, sets)
	}
	if remaining := len(p.Pairs) - fetched; remaining > 0 {
		fmt.Printf("%d pairs weren't fetched; run again to continue\n", remaining)
	}
}

// parsePairKey splits a "KSFO-KPDX" database key into its airports.
func parsePairKey(key string) (from, to av.ICAOAirportCode, ok bool) {
	fromStr, toStr, ok := strings.Cut(key, "-")
	return av.ICAOAirportCode(fromStr), av.ICAOAirportCode(toStr), ok
}

// shared is where a run's plan and the tasks' results live: a
// gs://bucket/prefix, or a directory to try a task out locally.
type shared interface {
	read(name string) ([]byte, error) // fs.ErrNotExist if there's nothing there
	write(name string, b []byte) error
}

func openShared(loc string) (shared, error) {
	rest, ok := strings.CutPrefix(loc, "gs://")
	if !ok {
		return dirShared(loc), nil
	}
	bucket, prefix, _ := strings.Cut(rest, "/")
	// Credentials are the job's service account on Cloud Run, or the
	// application default credentials elsewhere.
	client, err := storage.NewClient(context.Background())
	if err != nil {
		return nil, err
	}
	// The results are rewritten whole each time, which is safe to repeat, but
	// the client only retries writes it knows to be idempotent unless told.
	client.SetRetry(storage.WithPolicy(storage.RetryAlways))
	return gcsShared{bucket: client.Bucket(bucket), prefix: prefix}, nil
}

type dirShared string

func (d dirShared) read(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(string(d), name))
}

func (d dirShared) write(name string, b []byte) error {
	p := filepath.Join(string(d), name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

type gcsShared struct {
	bucket *storage.BucketHandle
	prefix string
}

func (g gcsShared) read(name string) ([]byte, error) {
	r, err := g.bucket.Object(path.Join(g.prefix, name)).NewReader(context.Background())
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, fs.ErrNotExist
	} else if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (g gcsShared) write(name string, b []byte) error {
	w := g.bucket.Object(path.Join(g.prefix, name)).NewWriter(context.Background())
	if _, err := w.Write(b); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
