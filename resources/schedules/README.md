# Real World Schedules

Built-in airport schedules are stored in one directory per ICAO airport code.
Each airport directory contains a `schedule.json` manifest and one or more CSV
schedule files.

Schedule data is intentionally added separately from the schedule engine so
that every bundled flight can be sourced and reviewed.
