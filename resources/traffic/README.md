# Historical Flight Data and Timetables

This directory holds the traffic data behind the sim's two non-scenario IFR
traffic sources. A scenario's own traffic definitions are the third source and
live in the scenario JSON, not here.

    flights/<FACILITY>.flt      historical flight data, one file per facility
    timetables/<AIRPORT>/*.csv  curated daily timetables, one directory per airport

## Historical flight data (`flights/*.flt`)

One file per facility, named as the scenarios name it: `N90.flt`, `ZBW.flt`, and
so on. Each holds the departures and arrivals at every airport that facility
generates IFR traffic at, over however many years have been imported. An airport
worked by more than one facility appears in each of their files, so a sim only
ever needs to read one.

The files are written by `cmd/importflights` from the
[MrAirspace/aircraft-flight-schedules](https://github.com/MrAirspace/aircraft-flight-schedules)
dataset, which extracts flights from ADS-B position reports and is published
quarterly under ODbL-1.0. Pass every quarter for a year in one run, since each
run rewrites a facility's file from scratch:

    go run ./cmd/importflights ~/av/aircraft-flight-schedules/*.parquet

Each flight records the airport it departs from or arrives at, its callsign, the
airport at the other end, the date, the UTC time, and the aircraft type. Times
are when the aircraft actually took off or touched down, so a sim launched with
this source flies the traffic that really operated on the selected date.
`aviation/flights.go` documents the file format and reads them back.

## Built-in timetables (`timetables/<AIRPORT>/*.csv`)

Curated daily timetables are stored with one subdirectory per airport; each CSV
file in an airport's directory is offered as a selectable timetable for
scenarios at that airport. Unlike the flight data above, a timetable is a single
day's cycle without dates, played from a user-selected local start time, and it
covers one airport rather than a whole facility.

See `website/facility-engineering.html` for documentation describing the CSV
format and how timetables integrate with scenarios.
