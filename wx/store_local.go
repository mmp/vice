// wx/store_local.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

//go:build !release

package wx

import (
	"context"
	"os"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/util/gcs"
)

// gcsStore returns a store backed by the vice weather bucket, or nil if no
// credentials are configured. Only development builds read from GCS directly;
// see store_release.go.
func gcsStore(lg *log.Logger) ObjectStore {
	creds := os.Getenv("VICE_GCS_CREDENTIALS")
	if creds == "" {
		return nil
	}

	client, err := gcs.MakeClient("vice-wx", gcs.Config{
		Context:     context.Background(),
		Timeout:     4 * time.Second,
		Credentials: []byte(creds),
	})
	if err != nil {
		lg.Warnf("Unable to create GCS client for weather: %v", err)
		return nil
	}
	return client
}
