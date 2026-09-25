// simlog/database.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package simlog

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mmp/vice/aviation/db"
)

// A log's Header names the static database its session ran on by the
// database's hash. The database itself goes beside the log, in the db
// directory, where the logs of every session run on the same data share one
// copy.

// DatabasePath returns the path of the database with the given hash for the
// logs in dir.
func DatabasePath(dir, hash string) string {
	return filepath.Join(dir, "db", hash+".db")
}

// SaveDatabase writes d beside the logs in dir, unless a database with the
// same hash is already there, and returns the hash.
func SaveDatabase(dir string, d *db.StaticDatabase) (string, error) {
	hash := d.Hash()
	path := DatabasePath(dir, hash)
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Write it under another name first so that a log never names a
	// database that was only partly written.
	f, err := os.CreateTemp(filepath.Dir(path), hash+".*.tmp")
	if err != nil {
		return "", err
	}
	err = f.Chmod(0o644) // as the logs are; CreateTemp makes it private
	w := bufio.NewWriter(f)
	if err == nil {
		err = d.Encode(w)
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return hash, nil
}

// LoadDatabase reads the database with the given hash that SaveDatabase
// wrote beside the logs in dir.
func LoadDatabase(dir, hash string) (*db.StaticDatabase, error) {
	path := DatabasePath(dir, hash)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	d, err := db.Decode(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}
