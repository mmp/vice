// util/release.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package util

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/mmp/vice/log"
)

// Release is a vice release published on GitHub.
type Release struct {
	TagName string    `json:"tag_name"`
	Created time.Time `json:"created_at"`
}

// NewerRelease returns the most recent non-beta release if it postdates the
// running executable's build and nil otherwise. It makes a network request
// that may take some time or time out, so it should be called
// asynchronously.
func NewerRelease(lg *log.Logger) *Release {
	const url = "https://api.github.com/repos/mmp/vice/releases"

	resp, err := http.Get(url)
	if err != nil {
		lg.Warn("new release GET error", slog.String("url", url), slog.Any("error", err))
		return nil
	}
	defer resp.Body.Close()

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		lg.Errorf("JSON decode error: %v", err)
		return nil
	}

	var newest *Release
	for i := range releases {
		if strings.Contains(releases[i].TagName, "-beta") {
			continue
		}
		if newest == nil || releases[i].Created.After(newest.Created) {
			newest = &releases[i]
		}
	}
	if newest == nil {
		lg.Warnf("No vice releases found?")
		return nil
	}
	lg.Infof("newest release found: %v", newest)

	built, err := buildTime()
	if err != nil {
		lg.Errorf("%v", err)
		return nil
	}

	if newest.Created.UTC().After(built.UTC()) {
		lg.Infof("build time %s newest release %s -> release is newer",
			built.UTC().String(), newest.Created.UTC().String())
		return newest
	}

	lg.Infof("build time %s newest release %s -> build is newer",
		built.UTC().String(), newest.Created.UTC().String())
	return nil
}

// buildTime returns when the running executable's source was committed.
func buildTime() (time.Time, error) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return time.Time{}, errors.New("unable to read build info")
	}
	for _, setting := range bi.Settings {
		if setting.Key == "vcs.time" {
			t, err := time.Parse(time.RFC3339, setting.Value)
			if err != nil {
				return time.Time{}, fmt.Errorf("parsing build time %q: %w", setting.Value, err)
			}
			return t, nil
		}
	}
	return time.Time{}, errors.New("build time unavailable in BuildInfo.Settings")
}
