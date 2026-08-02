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

	"github.com/mmp/vice/util"
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

// timetableResourceRoot is where the built-in timetables live in the resources.
const timetableResourceRoot = "traffic/timetables"

// LoadBuiltinTimetables reads every timetable Vice ships with. Only the server
// wants them all, and only at startup, to record which airports have one; a sim
// or a preview flies a single timetable and should ask for that airport's alone.
func LoadBuiltinTimetables() (TimetableCatalog, error) {
	return LoadTimetableCatalog(util.GetResourcesFS(), timetableResourceRoot)
}

// LoadAirportTimetables reads the timetables Vice ships with for one airport.
func LoadAirportTimetables(airport string) (TimetableCatalog, error) {
	return LoadTimetableCatalogForAirport(util.GetResourcesFS(), timetableResourceRoot, airport)
}

// LoadTimetableCatalogForAirport is LoadTimetableCatalog for a single airport,
// reading only that airport's directory. Finding one timetable shouldn't cost
// more each time a timetable is added for some other airport, which is what
// parsing every airport's CSVs to find it would come to. An airport with no
// directory of its own has no timetables, which is not an error.
func LoadTimetableCatalogForAirport(filesystem fs.FS, root, airport string) (TimetableCatalog, error) {
	if normalizeAirportCode(airport) == "" {
		return TimetableCatalog{}, nil
	}
	return loadTimetables(filesystem, root, normalizeAirportCode(airport))
}

// LoadTimetableCatalog discovers CSV files in airport directories
// directly below root. For example, timetables/KMSP/Summer Weekday.csv is
// exposed as a KMSP timetable named "Summer Weekday".
func LoadTimetableCatalog(filesystem fs.FS, root string) (TimetableCatalog, error) {
	return loadTimetables(filesystem, root, "")
}

// loadTimetables reads the timetables below root: all of them, or only those of
// the airport onlyAirport names if it is non-empty. An airport's directory is
// named for it, but not necessarily in the same case, so match directories
// rather than assume a name: reading the root costs nothing next to parsing the
// CSVs below it.
func loadTimetables(filesystem fs.FS, root, onlyAirport string) (TimetableCatalog, error) {
	root = path.Clean(root)
	directories, err := fs.ReadDir(filesystem, root)
	if err != nil {
		return TimetableCatalog{}, fmt.Errorf("load built-in timetables: %w", err)
	}

	var catalog TimetableCatalog
	seen := make(map[string]string)
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		airport := normalizeAirportCode(directory.Name())
		if onlyAirport != "" && airport != onlyAirport {
			continue
		}

		// Two directories naming the same airport: neither holds all of the
		// airport's timetables, so no load of them can be trusted.
		if previous, ok := seen[airport]; ok {
			return TimetableCatalog{}, fmt.Errorf("load built-in timetables: %s has timetables in "+
				"both %s and %s", airport, previous, directory.Name())
		}
		seen[airport] = directory.Name()

		timetables, err := loadAirportTimetables(filesystem, root, directory.Name())
		if err != nil {
			return TimetableCatalog{}, fmt.Errorf("load built-in timetables: %w", err)
		}
		catalog.Timetables = append(catalog.Timetables, timetables...)
	}

	sortTimetables(catalog.Timetables)
	return catalog, nil
}

// loadAirportTimetables reads the CSV files directly inside one airport's
// directory. Nested files are intentionally ignored.
func loadAirportTimetables(filesystem fs.FS, root, directory string) ([]Timetable, error) {
	airport := normalizeAirportCode(directory)
	if airport == "" {
		return nil, fmt.Errorf("%s: unable to determine airport from directory", path.Join(root, directory))
	}

	entries, err := fs.ReadDir(filesystem, path.Join(root, directory))
	if err != nil {
		return nil, err
	}

	var timetables []Timetable
	seen := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(path.Ext(entry.Name()), ".csv") {
			continue
		}
		filename := path.Join(root, directory, entry.Name())

		name := strings.TrimSpace(strings.TrimSuffix(entry.Name(), path.Ext(entry.Name())))
		if name == "" {
			return nil, fmt.Errorf("%s: timetable filename must have a name before the .csv extension", filename)
		}

		key := strings.ToLower(name)
		if previous, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate built-in timetable %q in %s and %s", airport+"/"+name,
				previous, filename)
		}

		timetable, err := loadDiscoveredTimetable(filesystem, filename, airport, name)
		if err != nil {
			return nil, err
		}

		seen[key] = filename
		timetables = append(timetables, timetable)
	}
	return timetables, nil
}

func sortTimetables(timetables []Timetable) {
	sort.Slice(timetables, func(i, j int) bool {
		if timetables[i].Airport != timetables[j].Airport {
			return timetables[i].Airport < timetables[j].Airport
		}
		return timetables[i].Name < timetables[j].Name
	})
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
