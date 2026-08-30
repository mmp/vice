- Scenario updates: PCT SHD (Ketan K)
- Added: IFIX/RADIAL: intercept a fix's radial; also available by voice ("intercept the WAVEY 050 radial inbound", etc.)
- Facility engineering
  - Added @t to route specifiers to allow specifying a track to fly inbound to the next fix
  - Added @crs to route specifiers to join a course inbound a bix
  - Added @d to route specifiers to fly a given distance from a point
  - Route specifier altitudes @a require "-"/"+" for below/above the altitude
  - Added support for extracting SIDs from the CIFP: if available, "sid" is sufficient in "departure_routes" without any "waypoints"

