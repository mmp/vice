// sim/timetable_catalog.go
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

// Timetable describes one timetable distributed in Vice's resources.
// Flights are loaded and validated when the catalog is read so malformed
// resource files fail early rather than when a scenario is launched.
type Timetable struct {
	ID          string
	Name        string
	Airport     string
	Description string
	Flights     []TimetableFlight
}

// TimetableCatalog contains all valid timetables found below a resource
// root. Timetables are sorted first by airport and then by display name.
type TimetableCatalog struct {
	Timetables []Timetable
}

// Find returns a built-in timetable by airport and ID.
func (c TimetableCatalog) Find(airport, id string) (Timetable, bool) {
	airport = normalizeAirportCode(airport)
	id = strings.TrimSpace(id)
	for _, timetable := range c.Timetables {
		if timetable.Airport == airport && timetable.ID == id {
			return timetable, true
		}
	}
	return Timetable{}, false
}

// TimetableSummary is the small, client-facing description of a
// built-in timetable. The full flight list stays on the server until a
// scenario is launched.
type TimetableSummary struct {
	ID          string
	Name        string
	Airport     string
	Description string
}

// Summary returns the client-facing metadata for a built-in timetable.
func (s Timetable) Summary() TimetableSummary {
	return TimetableSummary{
		ID:          s.ID,
		Name:        s.Name,
		Airport:     s.Airport,
		Description: s.Description,
	}
}

// SummariesForAirport returns client-facing timetable metadata for airport.
func (c TimetableCatalog) SummariesForAirport(airport string) []TimetableSummary {
	timetables := c.ForAirport(airport)
	summaries := make([]TimetableSummary, len(timetables))
	for i, timetable := range timetables {
		summaries[i] = timetable.Summary()
	}
	return summaries
}

// ForAirport returns timetables published for airport. The returned slice is a
// copy and may be modified by the caller.
func (c TimetableCatalog) ForAirport(airport string) []Timetable {
	airport = normalizeAirportCode(airport)
	var timetables []Timetable
	for _, timetable := range c.Timetables {
		if timetable.Airport == airport {
			timetables = append(timetables, timetable)
		}
	}
	return timetables
}

// LoadTimetableCatalog discovers CSV files in airport directories
// directly below root. For example, timetables/KMSP/Summer Weekday.csv is
// exposed as a KMSP timetable named "Summer Weekday".
func LoadTimetableCatalog(filesystem fs.FS, root string) (TimetableCatalog, error) {
	var catalog TimetableCatalog
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
		// root/KMSP/Timetable.csv. Nested files are intentionally ignored.
		directory := path.Dir(filename)
		if path.Dir(directory) != root {
			return nil
		}

		airport := normalizeAirportCode(path.Base(directory))
		if airport == "" {
			return fmt.Errorf("%s: unable to determine airport from directory", filename)
		}

		name := strings.TrimSpace(strings.TrimSuffix(entry.Name(), path.Ext(entry.Name())))
		if name == "" {
			return fmt.Errorf("%s: timetable filename must have a name before the .csv extension", filename)
		}

		key := airport + "/" + strings.ToLower(name)
		if previous, ok := seen[key]; ok {
			return fmt.Errorf("duplicate built-in timetable %q in %s and %s", airport+"/"+name, previous, filename)
		}

		timetable, err := loadDiscoveredTimetable(filesystem, filename, airport, name)
		if err != nil {
			return err
		}

		seen[key] = filename
		catalog.Timetables = append(catalog.Timetables, timetable)
		return nil
	})
	if err != nil {
		return TimetableCatalog{}, fmt.Errorf("load built-in timetables: %w", err)
	}

	sort.Slice(catalog.Timetables, func(i, j int) bool {
		if catalog.Timetables[i].Airport != catalog.Timetables[j].Airport {
			return catalog.Timetables[i].Airport < catalog.Timetables[j].Airport
		}
		return catalog.Timetables[i].Name < catalog.Timetables[j].Name
	})
	return catalog, nil
}

func loadDiscoveredTimetable(
	filesystem fs.FS,
	filename string,
	airport string,
	name string,
) (Timetable, error) {
	csvFile, err := filesystem.Open(filename)
	if err != nil {
		return Timetable{}, fmt.Errorf("open %s: %w", filename, err)
	}

	flights, loadErr := LoadTimetableCSV(csvFile)
	closeErr := csvFile.Close()
	if loadErr != nil {
		return Timetable{}, fmt.Errorf("%s: %w", filename, loadErr)
	}
	if closeErr != nil {
		return Timetable{}, fmt.Errorf("close %s: %w", filename, closeErr)
	}

	timetable := Timetable{
		ID:          name,
		Name:        timetableDisplayName(name),
		Airport:     airport,
		Description: "",
		Flights:     flights,
	}
	if err := validateTimetable(timetable); err != nil {
		return Timetable{}, fmt.Errorf("%s: %w", filename, err)
	}

	return timetable, nil
}
func timetableDisplayName(id string) string {
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
