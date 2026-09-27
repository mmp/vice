- Scenario updates: IND (Ethan Hawes), D01 (Mike Fries), S56 (Elliott B)
- Added delay before launching subsequent same-exit departures
adjustments for TAS
- Flight model
  - Added "climb via SID/descend via STAR, except maintain (altitude)" instructions (`CVS/A100`, `DVS/A120`) and by voice.
  - Traffic calls ending "has you in sight and will maintain visual separation" (`TRAFFIC/INSIGHT`) now get just "roger", without a callsign
  - Landmarks on charted visual approaches can be called (`RP/3/3`, or by voice: "the Dumbarton bridge is at your 3 o'clock, 3 miles, report in sight").
  - Fixed vectored aircraft given "intercept localizer" then flying procedure turns after being cleared for the approach
  - Fixed multiple bugs with airspeed handling and altitude/temperature
  - VFRs are better at scud-running under B and C airspace shelves
  - Fixed VFR airwork descending below the ground
- ERAM
  - Altitudes that virtual controllers assign along a route are now entered in the datablock:
    `/d` and `/c` amend the assigned altitude and `/dv`/`/cv` set interim altitude
  - Add datablock portal fence option
  - Add support for altitude limits, including the `QD` command
  - Added support for block altitudes, including the `QZ` command
  - HSF indicator is now on FDB line 2 and can be clicked to toggle HSF data
  - Show `-` rather than `+` in the FDB when below the datablock altitude
- Facility engineering
  - ERAM altitude limits can be specified per-controller and/or per-scenario
  - Added `/cvs` and `/dvs` waypoint actions for virtual controllers to issue "climb via SID"/"descend via STAR"
  - Added `/cv` and `/dv` waypoint actions for "climb via SID/descend via STAR, except maintain (altitude)"
  - Added an "eram" object to allow specifying ERAM datablock entries for assigned altitudes, etc.
  - An arrival's "cleared_altitude" now stops its descent, as documented; scenarios that expect the descent to continue must use `/dvs`.
  - Charted visual approaches may now give multiple routes in "waypoints", e.g. for procedures with several feeder legs
  - Charted visual approaches may now give "reporting_points": landmarks that pilots can be told to look for and report in sight

