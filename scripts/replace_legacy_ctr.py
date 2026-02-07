#!/usr/bin/env python3
"""
Replace legacy _CTR initial_controller values with standardized Zxx NN Name from output.json.
User-specified mappings first, then systematic PREFIX_NN_CTR from output.json.
"""
import json
import os
import re

SCENARIOS_DIR = os.path.join(os.path.dirname(__file__), "..", "resources", "scenarios")
OUTPUT_JSON = os.path.join(os.path.dirname(__file__), "..", "resources", "output.json")

# User-specified mappings (exact)
USER_MAP = {
    "NY_A1_CTR": "ZNY 26 Lancaster",
    "NY_A_CTR": "ZNY 26 Lancaster",
    "NY_B_CTR": "ZNY 56 Kennedy",
    "NY_B1_CTR": "ZNY 56 Kennedy",
    "NY_C_CTR": "ZNY 55 Yardley",
    "NY_C1_CTR": "ZNY 55 Yardley",
    "NY_D_CTR": "ZNY 72 Selinsgrove",
    "NY_D1_CTR": "ZNY 72 Selinsgrove",
    "NY_F_CTR": "ZNY 86 Atlantic",
    "NY_F1_CTR": "ZNY 86 Atlantic",
    "BOS_E_CTR": "ZBW 46 Boston",
    # Legacy sector numbers mapped to correct ARTCC sectors (56->35 for ZAU, 77->94 for ZKC)
    "CHI_56_CTR": "ZAU 35 BEARZ",
    "KC_77_CTR": "ZKC 94 (94) FARGO SH",
}

# Legacy prefix -> ARTCC code (for PREFIX_NN_CTR pattern)
PREFIX_TO_ARTCC = {
    "ZDC": "ZDC",
    "CLE": "ZOB",
    "IND": "ZID",
    "LAX": "ZLA",
    "CHI": "ZAU",
    "ABQ": "ZAB",
    "FTW": "ZFW",
    "KC": "ZKC",
    "MEM": "ZME",
    "MSP": "ZMP",
    "SLC": "ZLC",
    "BOS": "ZBW",
    "NY": "ZNY",
    "OAK": "ZOA",
    "HCF": "ZHN",
}

def load_output_controllers():
    with open(OUTPUT_JSON, "r", encoding="utf-8") as f:
        data = json.load(f)
    positions = data.get("control_positions", data)
    # Map (artcc_prefix, sector_id) -> full name. artcc_prefix is "ZNY", "ZOB", etc.
    by_artcc_sector = {}
    for name, info in positions.items():
        if not isinstance(info, dict):
            continue
        sector_id = info.get("sector_id")
        if not sector_id:
            continue
        # name is like "ZNY 26 Lancaster" or "ZOA 10 Sector"
        parts = name.split(None, 2)
        if len(parts) >= 2 and parts[0].startswith("Z"):
            artcc = parts[0]
            try:
                sid = str(int(parts[1]))  # normalize "08" -> "8" for matching? No, keep as-is
                sid = parts[1]  # keep original format
                by_artcc_sector[(artcc, sid)] = name
            except (ValueError, IndexError):
                pass
    return by_artcc_sector

def build_replacement_map(by_artcc_sector):
    """Build legacy_callsign -> standard_name map."""
    replacements = dict(USER_MAP)

    # ZDC_05_CTR, CLE_48_CTR, IND_34_CTR, etc.
    for prefix, artcc in PREFIX_TO_ARTCC.items():
        if prefix == "NY":
            # NY handled in USER_MAP except NY_08_CTR
            key = ("ZNY", "08")
            if key in by_artcc_sector:
                replacements["NY_08_CTR"] = by_artcc_sector[key]
            continue
        if prefix == "BOS":
            for sid in ("01", "02"):
                key = ("ZBW", sid)
                if key in by_artcc_sector:
                    replacements[f"BOS_{sid}_CTR"] = by_artcc_sector[key]
            continue
        if prefix == "HCF":
            for sid in ("02", "03", "04", "05"):
                key = ("ZHN", sid)
                if key in by_artcc_sector:
                    replacements[f"HCF_{sid}_CTR"] = by_artcc_sector[key]
            continue
        # For others: PREFIX_NN_CTR -> look up (ARTCC, NN)
        for (artcc_key, sector_id), name in by_artcc_sector.items():
            if artcc_key != artcc:
                continue
            replacements[f"{prefix}_{sector_id}_CTR"] = name

    return replacements

def replace_in_obj(obj, replacements, changes):
    if isinstance(obj, dict):
        if "initial_controller" in obj:
            old = obj["initial_controller"]
            if old in replacements:
                new = replacements[old]
                if old != new:
                    obj["initial_controller"] = new
                    changes.append((old, new))
        for v in obj.values():
            replace_in_obj(v, replacements, changes)
    elif isinstance(obj, list):
        for v in obj:
            replace_in_obj(v, replacements, changes)

def main():
    by_artcc_sector = load_output_controllers()
    replacements = build_replacement_map(by_artcc_sector)
    print(f"Loaded {len(replacements)} legacy -> standard mappings", flush=True)

    total_changes = []
    for name in sorted(os.listdir(SCENARIOS_DIR)):
        if not name.endswith(".json"):
            continue
        path = os.path.join(SCENARIOS_DIR, name)
        try:
            with open(path, "r", encoding="utf-8") as f:
                data = json.load(f)
        except Exception as e:
            print(f"{name}: read error {e}", flush=True)
            continue
        changes = []
        replace_in_obj(data, replacements, changes)
        if changes:
            with open(path, "w", encoding="utf-8") as f:
                json.dump(data, f, indent=2, ensure_ascii=False)
            for old, new in changes:
                total_changes.append((name, old, new))
            print(f"{name}: {len(changes)} replacements", flush=True)
    print(f"\nTotal: {len(total_changes)} replacements", flush=True)
    # Show unique (old, new) pairs
    seen = set()
    for _, old, new in total_changes:
        if (old, new) not in seen:
            seen.add((old, new))
            print(f"  {old!r} -> {new!r}", flush=True)

if __name__ == "__main__":
    main()
