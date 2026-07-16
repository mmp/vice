// sim/schedule_catalog.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

const scheduleCatalogFilename = "schedule.json"

// BuiltInSchedule describes one schedule distributed in Vice's resources.
// Flights are loaded and validated when the catalog is read so malformed
// resource files fail early rather than when a scenario is launched.
type BuiltInSchedule struct {
	ID          string
	Name        string
	Airport     string
	Description string
	Timezone    string
	Flights     []ScheduledFlight
}

// BuiltInScheduleCatalog contains all valid schedules found below a resource
// root. Schedules are sorted first by airport and then by display name.
type BuiltInScheduleCatalog struct {
	Schedules []BuiltInSchedule
}

// Find returns a built-in schedule by airport and ID.
func (c BuiltInScheduleCatalog) Find(airport, id string) (BuiltInSchedule, bool) {
	airport = normalizeScheduleCode(airport)
	id = strings.TrimSpace(id)
	for _, schedule := range c.Schedules {
		if schedule.Airport == airport && schedule.ID == id {
			return schedule, true
		}
	}
	return BuiltInSchedule{}, false
}

// BuiltInScheduleSummary is the small, client-facing description of a
// built-in schedule. The full flight list stays on the server until a
// scenario is launched.
type BuiltInScheduleSummary struct {
	ID          string
	Name        string
	Airport     string
	Description string
	Timezone    string
}

// Summary returns the client-facing metadata for a built-in schedule.
func (s BuiltInSchedule) Summary() BuiltInScheduleSummary {
	return BuiltInScheduleSummary{
		ID:          s.ID,
		Name:        s.Name,
		Airport:     s.Airport,
		Description: s.Description,
		Timezone:    s.Timezone,
	}
}

// SummariesForAirport returns client-facing schedule metadata for airport.
func (c BuiltInScheduleCatalog) SummariesForAirport(airport string) []BuiltInScheduleSummary {
	schedules := c.ForAirport(airport)
	summaries := make([]BuiltInScheduleSummary, len(schedules))
	for i, schedule := range schedules {
		summaries[i] = schedule.Summary()
	}
	return summaries
}

// ForAirport returns schedules published for airport. The returned slice is a
// copy and may be modified by the caller.
func (c BuiltInScheduleCatalog) ForAirport(airport string) []BuiltInSchedule {
	airport = normalizeScheduleCode(airport)
	var schedules []BuiltInSchedule
	for _, schedule := range c.Schedules {
		if schedule.Airport == airport {
			schedules = append(schedules, schedule)
		}
	}
	return schedules
}

// LoadBuiltInScheduleCatalog discovers schedule.json files below root and
// loads the CSV files they reference.
func LoadBuiltInScheduleCatalog(filesystem fs.FS, root string) (BuiltInScheduleCatalog, error) {
	var catalog BuiltInScheduleCatalog
	seen := make(map[string]string)

	err := fs.WalkDir(filesystem, root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != scheduleCatalogFilename {
			return nil
		}

		schedules, err := loadScheduleManifest(filesystem, filename)
		if err != nil {
			return err
		}
		for _, schedule := range schedules {
			key := schedule.Airport + "/" + schedule.ID
			if previous, ok := seen[key]; ok {
				return fmt.Errorf("duplicate built-in schedule %q in %s and %s", key, previous, filename)
			}
			seen[key] = filename
			catalog.Schedules = append(catalog.Schedules, schedule)
		}
		return nil
	})
	if err != nil {
		return BuiltInScheduleCatalog{}, fmt.Errorf("load built-in schedules: %w", err)
	}

	sort.Slice(catalog.Schedules, func(i, j int) bool {
		if catalog.Schedules[i].Airport != catalog.Schedules[j].Airport {
			return catalog.Schedules[i].Airport < catalog.Schedules[j].Airport
		}
		return catalog.Schedules[i].Name < catalog.Schedules[j].Name
	})
	return catalog, nil
}

type scheduleManifest struct {
	Airport   string                  `json:"airport"`
	Schedules []scheduleManifestEntry `json:"schedules"`
}

type scheduleManifestEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	File        string `json:"file"`
	Description string `json:"description"`
	Timezone    string `json:"timezone"`
}

func loadScheduleManifest(filesystem fs.FS, filename string) ([]BuiltInSchedule, error) {
	contents, err := fs.ReadFile(filesystem, filename)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}

	var manifest scheduleManifest
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode %s: %w", filename, err)
	}

	manifest.Airport = normalizeScheduleCode(manifest.Airport)
	if manifest.Airport == "" {
		return nil, fmt.Errorf("%s: airport is required", filename)
	}
	if len(manifest.Schedules) == 0 {
		return nil, fmt.Errorf("%s: at least one schedule is required", filename)
	}

	directory := path.Dir(filename)
	ids := make(map[string]struct{})
	schedules := make([]BuiltInSchedule, 0, len(manifest.Schedules))
	for index, entry := range manifest.Schedules {
		entry.ID = strings.TrimSpace(entry.ID)
		entry.Name = strings.TrimSpace(entry.Name)
		entry.File = strings.TrimSpace(entry.File)
		entry.Description = strings.TrimSpace(entry.Description)
		entry.Timezone = strings.TrimSpace(entry.Timezone)

		prefix := fmt.Sprintf("%s schedule %d", filename, index+1)
		if entry.ID == "" || entry.Name == "" || entry.File == "" || entry.Timezone == "" {
			return nil, fmt.Errorf("%s: id, name, file, and timezone are required", prefix)
		}
		if _, ok := ids[entry.ID]; ok {
			return nil, fmt.Errorf("%s: duplicate schedule id %q", filename, entry.ID)
		}
		ids[entry.ID] = struct{}{}

		if path.Base(entry.File) != entry.File || path.Ext(entry.File) != ".csv" {
			return nil, fmt.Errorf("%s: file %q must be a CSV in the same directory", prefix, entry.File)
		}

		csvPath := path.Join(directory, entry.File)
		csvFile, err := filesystem.Open(csvPath)
		if err != nil {
			return nil, fmt.Errorf("%s: open %s: %w", prefix, csvPath, err)
		}
		flights, loadErr := LoadScheduleCSV(csvFile)
		closeErr := csvFile.Close()
		if loadErr != nil {
			return nil, fmt.Errorf("%s: %w", prefix, loadErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("%s: close %s: %w", prefix, csvPath, closeErr)
		}

		schedule := BuiltInSchedule{
			ID:          entry.ID,
			Name:        entry.Name,
			Airport:     manifest.Airport,
			Description: entry.Description,
			Timezone:    entry.Timezone,
			Flights:     flights,
		}

		if err := validateBuiltInSchedule(schedule); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", prefix, entry.File, err)
		}

		schedules = append(schedules, schedule)
	}
	return schedules, nil
}
