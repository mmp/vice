// sim/schedule_catalog.go
// Copyright(c) 2022-2026 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package sim

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

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

// LoadBuiltInScheduleCatalog discovers CSV files in airport directories
// directly below root. For example, schedules/KMSP/Summer Weekday.csv is
// exposed as a KMSP schedule named "Summer Weekday".
func LoadBuiltInScheduleCatalog(filesystem fs.FS, root string) (BuiltInScheduleCatalog, error) {
	var catalog BuiltInScheduleCatalog
	seen := make(map[string]string)
	root = path.Clean(root)

	err := fs.WalkDir(filesystem, root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(path.Ext(entry.Name()), ".csv") {
			return nil
		}

		// Only load CSV files directly inside an airport directory:
		// root/KMSP/Schedule.csv. Nested files are intentionally ignored.
		directory := path.Dir(filename)
		if path.Dir(directory) != root {
			return nil
		}

		airport := normalizeScheduleCode(path.Base(directory))
		if airport == "" {
			return fmt.Errorf("%s: unable to determine airport from directory", filename)
		}

		name := strings.TrimSpace(strings.TrimSuffix(entry.Name(), path.Ext(entry.Name())))
		if name == "" {
			return fmt.Errorf("%s: schedule filename must have a name before the .csv extension", filename)
		}

		key := airport + "/" + strings.ToLower(name)
		if previous, ok := seen[key]; ok {
			return fmt.Errorf("duplicate built-in schedule %q in %s and %s", airport+"/"+name, previous, filename)
		}

		schedule, err := loadDiscoveredSchedule(filesystem, filename, airport, name)
		if err != nil {
			return err
		}

		seen[key] = filename
		catalog.Schedules = append(catalog.Schedules, schedule)
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

func loadDiscoveredSchedule(
	filesystem fs.FS,
	filename string,
	airport string,
	name string,
) (BuiltInSchedule, error) {
	csvFile, err := filesystem.Open(filename)
	if err != nil {
		return BuiltInSchedule{}, fmt.Errorf("open %s: %w", filename, err)
	}

	flights, loadErr := LoadScheduleCSV(csvFile)
	closeErr := csvFile.Close()
	if loadErr != nil {
		return BuiltInSchedule{}, fmt.Errorf("%s: %w", filename, loadErr)
	}
	if closeErr != nil {
		return BuiltInSchedule{}, fmt.Errorf("close %s: %w", filename, closeErr)
	}

	schedule := BuiltInSchedule{
		ID:          name,
		Name:        scheduleDisplayName(name),
		Airport:     airport,
		Description: "",
		Timezone:    "",
		Flights:     flights,
	}
	if err := validateBuiltInSchedule(schedule); err != nil {
		return BuiltInSchedule{}, fmt.Errorf("%s: %w", filename, err)
	}

	return schedule, nil
}
func scheduleDisplayName(id string) string {
	id = strings.TrimSuffix(id, path.Ext(id))

	parts := strings.FieldsFunc(id, func(r rune) bool {
		return r == '_' || r == '-'
	})

	for i, part := range parts {
		if part == strings.ToUpper(part) {
			continue
		}
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		}
	}

	return strings.Join(parts, " ")
}
