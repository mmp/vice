- Scenario updates: IND (Ethan Hawes)
- Added delay before launching subsequent same-exit departures
- Added "climb via SID/descend via STAR, except maintain (altitude)" instructions (`CVS/A100`, `DVS/A120`) and by voice.
- ERAM
  - Altitudes that virtual controllers assign along a route are now entered in the datablock:
    `/d` and `/dv` amend the assigned altitude and a `/c` or `/cv` short of it is an interim altitude
  - Add datablock portal fence option
  - Add support for altitude limits, including the `QD` command
- Facility engineering
  - ERAM altitude limits can be specified per-controller and/or per-scenario
  - Added `/cvs` and `/dvs` waypoint actions for virtual controllers to issue "climb via SID"/"descend via STAR"
  - Added `/cv` and `/dv` waypoint actions for "climb via SID/descend via STAR, except maintain (altitude)"
  - Added an "eram" object for arrivals, overflights, and departure routes that gives aircraft
    datablock entries when they spawn: assigned, interim, and procedure altitudes, headings,
    speeds, and free text
  - An arrival's "cleared_altitude" now stops its descent, as documented; scenarios that expect the descent to continue must use `/dvs`.
