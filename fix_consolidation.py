#!/usr/bin/env python3
"""Fix consolidation discrepancies caused by scenario data reorganization.

Copies authoritative default_consolidation from old scenario files
(scenarios.orig/) into the new config files (resources/configurations/),
translating callsign-based keys to sector ID-based keys.

For conflict cases (multiple old scenarios sharing a config_id but with
different consolidations), creates new config entries and updates scenario
files to reference them.
"""

import json
import os
import sys
from collections import defaultdict

OLD_DIR = "scenarios.orig"
NEW_SCENARIO_DIR = "resources/scenarios"
CONFIG_DIR = "resources/configurations"


def load_json(path):
    with open(path) as f:
        return json.load(f)


def save_json(path, data):
    with open(path, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")


def build_config_map():
    """Map facility name (uppercase) -> config file path."""
    m = {}
    for artcc in os.listdir(CONFIG_DIR):
        artcc_dir = os.path.join(CONFIG_DIR, artcc)
        if not os.path.isdir(artcc_dir):
            continue
        for f in os.listdir(artcc_dir):
            if f.endswith(".json"):
                name = f[:-5]
                m[name] = os.path.join(artcc_dir, f)
    return m


def build_callsign_to_sector_id_from_config(config_data):
    """Build callsign -> sector_id map from new config file's control_positions.

    New config format: control_positions[sector_id] = {callsign: "...", ...}
    This gives us the correct (new) sector IDs for translation.
    """
    m = {}
    for sector_id, pos in config_data.get("control_positions", {}).items():
        callsign = pos.get("callsign", "")
        if callsign:
            m[callsign] = sector_id
    return m


def translate_consolidation(old_consol, cs_map):
    """Translate consolidation from callsigns to sector IDs.

    If a key is not found in the callsign map, it's assumed to already be a
    sector ID (as is the case for LIB scenarios).
    """
    translated = {}
    for key, values in old_consol.items():
        new_key = cs_map.get(key, key)
        new_values = sorted(cs_map.get(v, v) for v in values)
        translated[new_key] = new_values
    return translated


def find_config_for_facility(facility, config_map, config_id):
    """Find config file path for a facility.

    First tries direct match by facility name. If not found, searches all
    config files for one containing the configuration ID.
    """
    if facility in config_map:
        return config_map[facility]

    # Search all config files for this configuration ID
    for path in config_map.values():
        data = load_json(path)
        fa = data.get("facility_adaptations", {})
        configs = fa.get("configurations", {})
        if config_id in configs:
            return path
    return None


# Conflict resolution: explicit definitions for cases where multiple old
# scenarios share a config_id but need different consolidations.
#
# Format: (config_file_facility, base_config_id, scenario_filename) -> list of:
#   (scenario_name, new_config_id_or_None, consolidation)
# new_config_id=None means keep the base config_id.
CONFLICTS = {
    # CVG: two variants sharing config "CVG"
    ("CVG", "CVG", "cvg.json"): [
        ("CVG North Sats North", None,
         {"1F": ["1V"], "1N": ["1F", "1S"], "1W": ["1B", "1L", "1N"]}),
        ("CVG South Sats South", "CVS",
         {"1F": ["1V"], "1S": ["1F", "1N"], "1W": ["1B", "1L", "1S"]}),
    ],
    # JFK: five runway variants sharing config "JFK"
    ("N90", "JFK", "jfk.json"): [
        ("KJFK Depart 13L/R Land 13L", None,
         {"2G": ["2A", "2J", "2K", "3H"], "2K": ["2E", "2M"]}),
        ("KJFK Depart 13R Land 13L/22L", None,
         {"2G": ["2A", "2J", "2K", "3H"], "2K": ["2E", "2M"]}),
        ("KJFK Depart 22R Land 22 L/R", "J22",
         {"2G": ["2E", "2J", "2K", "3H"], "2K": ["2A", "2M"]}),
        ("KJFK Depart 31L Land 31L/31R", "J31",
         {"2G": ["2A", "2E", "2J", "2K", "3H"], "2K": ["2M"]}),
        ("KJFK Depart 4L Land 4L/R", "J04",
         {"2G": ["2A", "2K", "3H"], "2K": ["2E", "2J", "2M"]}),
    ],
    # LIB: five variants sharing config "LIB"
    ("N90", "LIB", "lib.json"): [
        ("Liberty Combined - JFK Owns Coney", None,
         {"5E": ["5T", "5W"], "5W": ["5S"]}),
        ("Liberty Combined - LGA Owns Coney", None,
         {"5E": ["5T", "5W"], "5W": ["5S"]}),
        ("Liberty East", "LBE",
         {"5E": []}),
        ("Liberty North", "LBN",
         {"5T": []}),
        ("Liberty South JFK Owns Coney", "LBS",
         {"5S": []}),
        ("Liberty South LGA Owns Coney", "LBS",
         {"5S": []}),
        ("Liberty West", "LBW",
         {"5W": []}),
    ],
    # D21: two variants sharing config "43X"
    ("D21", "43X", "d21.json"): [
        ("North Flow (DTW ARR: 4L/3R)", None,
         {"1A": ["1B", "1Y"], "1D": ["1P"], "1E": ["1W"],
          "1F": ["1A", "1E", "1H", "1R"], "1R": ["1V"], "1Y": ["1D"]}),
        ("North Flow (DTW ARR: 4R/4L/3R)", "43Z",
         {"1A": ["1B", "1Y", "1Z"], "1D": ["1P"], "1E": ["1W"],
          "1F": ["1A", "1E", "1H", "1R"], "1R": ["1V"], "1Y": ["1D"]}),
    ],
}

# Build a lookup: scenario_name -> (facility, base_config_id, scenario_filename) for quick check
CONFLICT_SCENARIOS = {}
for key, entries in CONFLICTS.items():
    for scenario_name, new_cid, consol in entries:
        CONFLICT_SCENARIOS[scenario_name] = key


def main():
    config_map = build_config_map()

    # Caches for files we load/modify
    config_cache = {}   # config_file_path -> data
    scenario_cache = {} # scenario_file_path -> data
    dirty_configs = set()
    dirty_scenarios = set()

    updated = []
    skipped = []
    created_configs = []
    scenario_updates = []

    # Process conflict cases first: create new config entries and update scenario refs
    for (facility, base_cid, sc_filename), entries in CONFLICTS.items():
        config_path = find_config_for_facility(facility, config_map, base_cid)
        if not config_path:
            print(f"ERROR: Cannot find config file for {facility}/{base_cid}")
            sys.exit(1)

        if config_path not in config_cache:
            config_cache[config_path] = load_json(config_path)
        config_data = config_cache[config_path]
        fa = config_data["facility_adaptations"]
        configs = fa["configurations"]

        if base_cid not in configs:
            print(f"ERROR: Config {base_cid} not found in {config_path}")
            sys.exit(1)

        base_config = configs[base_cid]

        # Find which scenario file to update
        scenario_file = os.path.join(NEW_SCENARIO_DIR, sc_filename)
        if not os.path.exists(scenario_file):
            print(f"ERROR: Scenario file not found: {scenario_file}")
            sys.exit(1)

        if scenario_file not in scenario_cache:
            scenario_cache[scenario_file] = load_json(scenario_file)
        scenario_data = scenario_cache[scenario_file]

        new_cids_created = set()
        for scenario_name, new_cid, consolidation in entries:
            target_cid = new_cid if new_cid else base_cid

            if new_cid and new_cid not in new_cids_created:
                # Create a new config entry by copying from base
                new_entry = {
                    "default_consolidation": consolidation,
                }
                # Copy inbound_assignments and departure_assignments from base
                if "inbound_assignments" in base_config:
                    new_entry["inbound_assignments"] = base_config["inbound_assignments"]
                if "departure_assignments" in base_config:
                    new_entry["departure_assignments"] = base_config["departure_assignments"]
                configs[new_cid] = new_entry
                new_cids_created.add(new_cid)
                dirty_configs.add(config_path)
                created_configs.append((config_path, new_cid, f"copied from {base_cid}"))

                # Update scenario file to reference new config_id
                scenarios = scenario_data.get("scenarios", {})
                if scenario_name in scenarios:
                    old_ref = scenarios[scenario_name].get("configuration")
                    scenarios[scenario_name]["configuration"] = new_cid
                    dirty_scenarios.add(scenario_file)
                    scenario_updates.append((scenario_file, scenario_name, old_ref, new_cid))
            elif new_cid and new_cid in new_cids_created:
                # Config already created, just update scenario reference
                scenarios = scenario_data.get("scenarios", {})
                if scenario_name in scenarios:
                    old_ref = scenarios[scenario_name].get("configuration")
                    if old_ref != new_cid:
                        scenarios[scenario_name]["configuration"] = new_cid
                        dirty_scenarios.add(scenario_file)
                        scenario_updates.append((scenario_file, scenario_name, old_ref, new_cid))

            if not new_cid:
                # Update the base config's consolidation
                current = base_config.get("default_consolidation")
                if current != consolidation:
                    base_config["default_consolidation"] = consolidation
                    dirty_configs.add(config_path)
                    updated.append((config_path, base_cid, scenario_name, "conflict base"))

    # Now process all old scenario files for non-conflict updates
    # Cache: config_path -> callsign-to-sector_id map (built from new config)
    cs_map_cache = {}

    for old_file in sorted(os.listdir(OLD_DIR)):
        if not old_file.endswith(".json"):
            continue
        if old_file.startswith("#") or old_file.endswith("~"):
            continue

        old_path = os.path.join(OLD_DIR, old_file)
        old_data = load_json(old_path)

        # Check for corresponding new scenario file
        new_scenario_path = os.path.join(NEW_SCENARIO_DIR, old_file)
        if not os.path.exists(new_scenario_path):
            skipped.append((old_file, "no corresponding new scenario file"))
            continue

        if new_scenario_path not in scenario_cache:
            scenario_cache[new_scenario_path] = load_json(new_scenario_path)
        new_scenario_data = scenario_cache[new_scenario_path]

        # Determine facility for config file lookup
        is_artcc = "artcc" in old_data
        if is_artcc:
            facility = old_data["artcc"]
        else:
            facility = new_scenario_data.get("tracon", old_data.get("tracon", ""))

        old_scenarios = old_data.get("scenarios", {})
        new_scenarios = new_scenario_data.get("scenarios", {})

        for sname, sdata in old_scenarios.items():
            # Get old consolidation
            old_config = sdata.get("configuration", {})
            if isinstance(old_config, str):
                # Already just a config_id string, no consolidation embedded
                skipped.append((old_file + "/" + sname, "configuration is a string, not object"))
                continue
            old_consol = old_config.get("default_consolidation")
            if not old_consol:
                continue  # No consolidation to fix

            # Skip conflict scenarios (already handled above)
            if sname in CONFLICT_SCENARIOS:
                continue

            # Get config_id from new scenario
            if sname not in new_scenarios:
                skipped.append((old_file + "/" + sname, "scenario not in new file"))
                continue

            config_id = new_scenarios[sname].get("configuration")
            if not config_id:
                skipped.append((old_file + "/" + sname, "no configuration in new scenario"))
                continue

            # Find config file
            config_path = find_config_for_facility(facility, config_map, config_id)
            if not config_path:
                skipped.append((old_file + "/" + sname, f"no config file for {facility}/{config_id}"))
                continue

            # Load config file
            if config_path not in config_cache:
                config_cache[config_path] = load_json(config_path)
            config_data = config_cache[config_path]
            fa = config_data["facility_adaptations"]
            configs = fa.get("configurations", {})

            if config_id not in configs:
                skipped.append((old_file + "/" + sname, f"config {config_id} not in {config_path}"))
                continue

            # Build callsign -> sector_id map from new config's control_positions
            # This ensures we use the correct (new) sector IDs
            if config_path not in cs_map_cache:
                cs_map_cache[config_path] = build_callsign_to_sector_id_from_config(config_data)
            cs_map = cs_map_cache[config_path]

            # Translate to sector IDs using new config's mapping
            translated = translate_consolidation(old_consol, cs_map)

            # Compare and update
            current = configs[config_id].get("default_consolidation")
            if current != translated:
                configs[config_id]["default_consolidation"] = translated
                dirty_configs.add(config_path)
                updated.append((config_path, config_id, sname, f"{current} -> {translated}"))
            else:
                updated.append((config_path, config_id, sname, "already correct"))

    # Save modified files
    for path in sorted(dirty_configs):
        save_json(path, config_cache[path])
    for path in sorted(dirty_scenarios):
        save_json(path, scenario_cache[path])

    # Print summary
    print("=" * 70)
    print("CONSOLIDATION UPDATES")
    print("=" * 70)
    for config_path, config_id, sname, desc in updated:
        print(f"  [{config_id}] {sname}")
        print(f"    {config_path}: {desc}")

    print()
    print("=" * 70)
    print("NEW CONFIGS CREATED")
    print("=" * 70)
    for config_path, new_cid, desc in created_configs:
        print(f"  [{new_cid}] in {config_path} ({desc})")

    print()
    print("=" * 70)
    print("SCENARIO FILE UPDATES")
    print("=" * 70)
    for scenario_file, sname, old_ref, new_ref in scenario_updates:
        print(f"  {scenario_file}: '{sname}' config {old_ref} -> {new_ref}")

    print()
    print("=" * 70)
    print("SKIPPED")
    print("=" * 70)
    for item, reason in skipped:
        print(f"  {item}: {reason}")

    print()
    print(f"Files modified: {len(dirty_configs)} configs, {len(dirty_scenarios)} scenarios")
    print(f"Consolidation updates: {len([u for u in updated if 'already correct' not in u[3]])}")
    print(f"Already correct: {len([u for u in updated if 'already correct' in u[3]])}")
    print(f"New configs created: {len(created_configs)}")
    print(f"Scenario refs updated: {len(scenario_updates)}")
    print(f"Skipped: {len(skipped)}")


if __name__ == "__main__":
    main()
