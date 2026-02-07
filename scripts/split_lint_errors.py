#!/usr/bin/env python3
"""
Read vice -lint output and write one .txt file per error type under lint-error-lists/.
Usage: python split_lint_errors.py < lint_output.txt
   or:  python split_lint_errors.py path/to/lint_output.txt
"""
import re
import sys
from pathlib import Path

# Order matters: first match wins.
CATEGORIES = [
    ("initial_controller_not_found.txt", lambda s: 'not found for "initial_controller"' in s),
    ("controller_referenced_not_in_facility_config.txt", lambda s: "referenced in route/flow but not defined in facility configuration" in s),
    ("inbound_assignment_not_in_control_positions.txt", lambda s: 'which is not in "control_positions"' in s),
    ("position_not_in_control_positions.txt", lambda s: 'position "' in s and 'not found in "control_positions"' in s),
    ("control_position_unknown_in_scenario.txt", lambda s: 'control position "' in s and "unknown in scenario" in s),
    ("departure_controller_unknown.txt", lambda s: 'departure_controller "' in s and " unknown" in s),
    ("handoff_controller_not_found.txt", lambda s: 'No controller found with id "' in s and " for handoff" in s),
    ("airport_altimeters_not_in_airports.txt", lambda s: 'Airport "' in s and 'in "altimeters" not found' in s),
    ("controller_configs_position_not_in_control_positions.txt", lambda s: 'in "controller_configs" not defined in "control_positions"' in s),
    ("coordination_lists_airport_not_defined.txt", lambda s: '"coordination_lists"' in s and 'Airport "' in s and 'not defined in scenario group' in s),
    ("coordination_lists_hold_for_release.txt", lambda s: "isn't \"hold_for_release\" but is in \"coordination_lists\"" in s),
    ("video_map_default_maps_not_found.txt", lambda s: 'video map "' in s and 'in "default_maps" not found' in s),
    ("scope_char_redundant.txt", lambda s: '"scope_char" is redundant' in s),
]

# Fallback for any TRACON line that didn't match
OTHER = "other_errors.txt"


def main():
    if len(sys.argv) > 1:
        with open(sys.argv[1]) as f:
            lines = f.readlines()
    else:
        lines = sys.readlines()

    out_dir = Path(__file__).resolve().parent.parent / "lint-error-lists"
    out_dir.mkdir(exist_ok=True)

    buckets = {name: [] for name, _ in CATEGORIES}
    buckets[OTHER] = []

    for raw in lines:
        line = raw.rstrip("\n")
        if not line.startswith("TRACON "):
            continue
        assigned = False
        for name, pred in CATEGORIES:
            if pred(line):
                buckets[name].append(line)
                assigned = True
                break
        if not assigned:
            buckets[OTHER].append(line)

    for name, bucket in buckets.items():
        path = out_dir / name
        with open(path, "w") as f:
            f.write("\n".join(bucket))
            if bucket:
                f.write("\n")
        print(f"{path.relative_to(out_dir.parent)}: {len(bucket)} lines")

    return 0


if __name__ == "__main__":
    sys.exit(main())
