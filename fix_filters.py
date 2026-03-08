#!/usr/bin/env python3
"""Fix filter discrepancies from scenario data reorganization.

When scenario definitions were split into per-scenario files and per-facility
configurations, filters that were previously per-scenario became per-facility.
This script restores missing filters and updates conflicting parameters to use
the most conservative (largest ceiling/radius) values.
"""

import json
import sys
from pathlib import Path

CONFIGS = Path("resources/configurations")


def load_json(path):
    with open(path) as f:
        return json.load(f)


def save_json(path, data):
    with open(path, "w") as f:
        json.dump(data, f, indent=2)
        f.write("\n")


def fix_zbw_a90():
    """Add missing arrival_drop, departure, and secondary_drop filters from bos.json old."""
    path = CONFIGS / "ZBW" / "A90.json"
    data = load_json(path)
    filters = data["facility_adaptations"]["filters"]

    filters["arrival_drop"] = [
        {
            "id": "ABOS",
            "description": "BOS ARRIVAL FILTER",
            "type": "polygon",
            "vertices": "N042.22.27.335,W071.00.54.354 N042.22.38.157,W071.01.08.417 N042.22.19.068,W071.01.39.014 N042.21.45.821,W071.01.17.124 N042.21.22.955,W071.01.30.362 N042.21.07.094,W071.01.21.683 N042.20.44.792,W071.00.35.375 N042.20.59.060,W070.59.38.384 N042.21.18.451,W070.58.59.273 N042.22.03.371,W070.59.21.630 N042.22.09.331,W070.59.54.424 N042.22.44.309,W070.59.37.148 N042.23.03.700,W071.00.25.680",
            "floor": 0,
            "ceiling": 200,
        },
        {
            "id": "BEDA",
            "description": "BED ARRIVAL FILTER ",
            "type": "polygon",
            "vertices": "N042.28.37.176,W071.18.30.443 N042.28.31.038,W071.17.37.214 N042.28.48.273,W071.17.17.191 N042.28.33.565,W071.16.49.478 N042.28.24.075,W071.16.55.823 N042.28.22.084,W071.16.14.130 N042.28.02.185,W071.16.15.448 N042.28.00.661,W071.17.10.187 N042.27.35.653,W071.17.36.115 N042.27.47.024,W071.18.08.085 N042.28.05.673,W071.17.53.034 N042.28.10.795,W071.18.31.486",
            "floor": 0,
            "ceiling": 800,
        },
        {
            "id": "BVYA",
            "description": "BVY ARRIVAL FILTER ",
            "type": "polygon",
            "vertices": "N042.35.06.120,W070.55.18.804 N042.35.38.763,W070.55.53.082 N042.35.55.133,W070.55.21.469 N042.35.11.682,W070.54.37.770 N042.35.17.573,W070.54.14.095 N042.34.58.443,W070.54.02.202 N042.34.53.843,W070.54.19.698 N042.34.46.990,W070.54.12.474 N042.34.37.143,W070.54.33.953 N042.34.46.990,W070.54.45.653 N042.34.33.229,W070.55.40.200 N042.34.56.260,W070.55.52.230",
            "floor": 0,
            "ceiling": 400,
        },
        {
            "id": "LWMA",
            "description": "LWM ARRIVAL FILTER ",
            "type": "circle",
            "center": "N042.43.03.064,W071.07.16.898",
            "radius": 1,
            "floor": 0,
            "ceiling": 500,
        },
        {
            "id": "OWDA",
            "description": "OWD ARRIVAL FILTER ",
            "type": "circle",
            "center": "N042.11.23.445,W071.10.16.030",
            "radius": 1,
            "floor": 0,
            "ceiling": 700,
        },
    ]

    filters["departure"] = [
        {
            "id": "BOD",
            "description": "BOS DEPARTURE FILTER FOR WHO",
            "type": "polygon",
            "vertices": "N042.22.27.335,W071.00.54.354 N042.22.38.157,W071.01.08.417 N042.22.19.068,W071.01.39.014 N042.21.45.821,W071.01.17.124 N042.21.22.955,W071.01.30.362 N042.21.07.094,W071.01.21.683 N042.20.44.792,W071.00.35.375 N042.20.59.060,W070.59.38.384 N042.21.18.451,W070.58.59.273 N042.22.03.371,W070.59.21.630 N042.22.09.331,W070.59.54.424 N042.22.44.309,W070.59.37.148 N042.23.03.700,W071.00.25.680",
            "floor": 0,
            "ceiling": 600,
        }
    ]

    filters["secondary_drop"] = [
        {
            "id": "Z01",
            "description": "BCT PRIMARY DEPARTURE DROP FILTER",
            "type": "polygon",
            "vertices": "N043.50.05.598,W072.28.12.663 N044.01.20.488,W071.04.13.125 N043.05.40.232,W069.48.23.192 N042.35.56.849,W069.53.31.605 N042.21.31.374,W069.26.24.420 N041.18.24.510,W068.54.08.437 N040.49.07.219,W069.35.22.091 N040.34.12.452,W070.30.22.467 N040.50.27.832,W071.27.41.270 N041.24.35.587,W071.31.49.890 N041.58.32.864,W071.58.01.979 N042.39.52.643,W072.25.07.708 N043.23.19.520,W072.27.34.403 N043.23.19.520,W072.27.34.403",
            "floor": 0,
            "ceiling": 99999,
        }
    ]

    save_json(path, data)
    print(f"Fixed {path}")


def fix_zla_sct():
    """Fix SCT filters: update ACQ ceiling, update inhibit_ca values, add LGBNOCA, add inhibit_msaw."""
    path = CONFIGS / "ZLA" / "SCT.json"
    data = load_json(path)
    filters = data["facility_adaptations"]["filters"]

    # Update auto_acquisition ceiling from 26000 to 28000 (most conservative, from sctla)
    filters["auto_acquisition"][0]["ceiling"] = 28000

    # Update inhibit_ca entries to most conservative values
    for entry in filters["inhibit_ca"]:
        if entry["id"] == "LAXNOCA":
            entry["ceiling"] = 5000
            entry["radius"] = 10
            entry["center"] = "N033.56.33.635,W118.24.39.226"
        elif entry["id"] == "ONTNOCA":
            entry["ceiling"] = 4300
            entry["radius"] = 8
        elif entry["id"] == "PSPNOCA":
            entry["ceiling"] = 4000
            entry["radius"] = 11
        elif entry["id"] == "SANNOCA":
            entry["ceiling"] = 5000
            entry["radius"] = 12.4
        elif entry["id"] == "SNANOCA":
            entry["ceiling"] = 3000
            entry["radius"] = 12.4

    # Add missing LGBNOCA entry (from sna.json old)
    filters["inhibit_ca"].append(
        {
            "id": "LGBNOCA",
            "description": "LGB CONFLICT SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 3000,
            "radius": 11,
            "center": "KLGB",
        }
    )

    # Add inhibit_msaw (new filter type, from delrey + ont old data)
    filters["inhibit_msaw"] = [
        {
            "id": "LAXMSAW",
            "description": "PSP MSAW SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 3000,
            "radius": 10,
            "center": "KLAX",
        },
        {
            "id": "LGBMSAW",
            "description": "ONT MSAW SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 3000,
            "radius": 8,
            "center": "KLGB",
        },
        {
            "id": "SNAMSAW",
            "description": "PSP MSAW SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 3000,
            "radius": 5,
            "center": "KSNA",
        },
        {
            "id": "SMOMSAW",
            "description": "PSP MSAW SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 3000,
            "radius": 3,
            "center": "KSMO",
        },
        {
            "id": "HHRMSAW",
            "description": "ONT MSAW SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 3000,
            "radius": 3,
            "center": "KHHR",
        },
        {
            "id": "ONTMSAW",
            "description": "ONT MSAW SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 4300,
            "radius": 8,
            "center": "KONT",
        },
        {
            "id": "PDPMSAW",
            "description": "PSP DP MSAW SUPPRESS",
            "type": "polygon",
            "vertices": "N033.49.35.921,W116.31.10.999 N033.54.38.457,W116.36.04.196 N033.58.33.276,W116.31.26.022 N034.03.00.876,W116.31.54.285 N034.03.16.531,W116.29.50.222 N033.58.56.787,W116.29.28.249 N033.58.06.140,W116.24.37.056 N033.56.01.074,W116.25.15.481 N033.57.16.125,W116.30.17.193 N033.54.34.914,W116.32.45.509 N033.50.17.724,W116.29.27.947",
            "floor": 0,
            "ceiling": 5500,
        },
        {
            "id": "PSPMSAW",
            "description": "PSP MSAW SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 5500,
            "radius": 3.4,
            "center": "KPSP",
        },
        {
            "id": "SBDMSAW",
            "description": "SBD MSAW SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 4300,
            "radius": 4,
            "center": "KSBD",
        },
    ]

    save_json(path, data)
    print(f"Fixed {path}")


def fix_zny_n90():
    """Fix N90 filters: update 3 inhibit_ca values, add 6 missing entries."""
    path = CONFIGS / "ZNY" / "N90.json"
    data = load_json(path)
    filters = data["facility_adaptations"]["filters"]

    # Update existing entries to most conservative values
    for entry in filters["inhibit_ca"]:
        if entry["id"] in ("HVNNOCA", "ISPNOCA", "TEBNOCA"):
            entry["ceiling"] = 1500
            entry["radius"] = 3

    # Add 6 missing entries (from n90.json old)
    new_entries = [
        {
            "id": "HPNNOCA",
            "description": "HPN CONFLICT SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 1500,
            "radius": 3,
            "center": "KHPN",
        },
        {
            "id": "MMUNOCA",
            "description": "MMU CONFLICT SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 1500,
            "radius": 3,
            "center": "KMMU",
        },
        {
            "id": "DXRNOCA",
            "description": "DXR CONFLICT SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 1500,
            "radius": 3,
            "center": "KDXR",
        },
        {
            "id": "CDWNOCA",
            "description": "CDW CONFLICT SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 1500,
            "radius": 3,
            "center": "KCDW",
        },
        {
            "id": "SWFNOCA",
            "description": "SWF CONFLICT SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 1500,
            "radius": 3,
            "center": "KSWF",
        },
        {
            "id": "POUNOCA",
            "description": "POU CONFLICT SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 1500,
            "radius": 3,
            "center": "KPOU",
        },
    ]
    filters["inhibit_ca"].extend(new_entries)

    save_json(path, data)
    print(f"Fixed {path}")


def fix_zoa_nct():
    """Fix NCT filters: add ACQ2, add inhibit_ca entries, update and add inhibit_msaw, add Q81."""
    path = CONFIGS / "ZOA" / "NCT.json"
    data = load_json(path)
    filters = data["facility_adaptations"]["filters"]

    # Add second auto_acquisition entry (from nctd/ncte, ceiling=30000)
    filters["auto_acquisition"].append(
        {
            "id": "ACQ2",
            "description": "NCT AUTO-ACQUISITION",
            "type": "polygon",
            "vertices": "N035.36.36.826,W121.24.53.728 N035.31.38.190,W122.24.56.063 N036.57.57.117,W124.02.02.140 N038.25.54.908,W123.11.06.897 N039.49.38.681,W121.55.53.878 N040.43.53.189,W118.26.18.652 N038.50.36.744,W117.52.26.099 N038.01.06.879,W119.06.15.979 N036.20.39.134,W118.40.44.815",
            "floor": 0,
            "ceiling": 30000,
        }
    )

    # Add 2 missing inhibit_ca entries (from nctd/ncte)
    filters["inhibit_ca"].extend(
        [
            {
                "id": "RNONOCA",
                "description": "RNO CONFLICT SUPPRESS",
                "type": "polygon",
                "floor": 0,
                "ceiling": 9500,
                "vertices": [
                    "N039.32.56.316,W119.49.19.716",
                    "N039.27.04.534,W119.51.36.551",
                    "N039.27.06.512,W119.41.00.031",
                    "N039.33.44.780,W119.41.42.575",
                ],
            },
            {
                "id": "SMFNOCA",
                "description": "SMF CONFLICT SUPPRESS",
                "type": "polygon",
                "floor": 0,
                "ceiling": 9500,
                "vertices": [
                    "N038.48.53.569,W121.39.29.531",
                    "N038.38.04.043,W121.38.43.965",
                    "N038.37.10.375,W121.32.06.095",
                    "N038.47.13.071,W121.31.19.486",
                ],
            },
        ]
    )

    # Update inhibit_msaw entries
    for entry in filters["inhibit_msaw"]:
        if entry["id"] == "OAKMSAW":
            entry["ceiling"] = 3000  # most conservative, from nctb
        elif entry["id"] == "SJCMSAW":
            entry["ceiling"] = 3900  # most conservative, from nctd
            entry["vertices"] = [
                "N037.11.55.264,W121.46.35.498",
                "N037.23.00.377,W122.01.49.808",
                "N037.27.50.800,W121.57.13.200",
                "N037.26.33.017,W121.52.58.234",
                "N037.14.26.313,W121.43.03.242",
            ]

    # Add 6 missing inhibit_msaw entries
    filters["inhibit_msaw"].extend(
        [
            {
                "id": "BRIXMVA",
                "description": "BRIXX STAR SEGMENT MSAW",
                "type": "polygon",
                "floor": 0,
                "ceiling": 4700,
                "vertices": [
                    "N037.15.06.632,W122.04.44.078",
                    "N037.16.34.908,W121.54.04.564",
                    "N037.13.13.377,W121.49.11.394",
                    "N037.08.51.024,W122.01.31.323",
                ],
            },
            {
                "id": "FRLNMVA",
                "description": "FRLON STAR SEGMENT MSAW",
                "type": "polygon",
                "floor": 0,
                "ceiling": 2000,
                "vertices": [
                    "N037.24.35.230,W122.07.15.580",
                    "N037.27.39.155,W121.58.20.134",
                    "N037.25.44.183,W121.56.54.550",
                    "N037.23.14.906,W122.06.01.367",
                ],
            },
            {
                "id": "SMFMSAW",
                "description": "SMF MSAW SUPPRESS",
                "type": "circle",
                "floor": 0,
                "ceiling": 1700,
                "center": "KSMF",
                "radius": 5,
            },
            {
                "id": "SACMSAW",
                "description": "SAC MSAW SUPPRESS",
                "type": "circle",
                "floor": 0,
                "ceiling": 1700,
                "center": "KSAC",
                "radius": 5,
            },
            {
                "id": "MHRMSAW",
                "description": "MHR MSAW SUPPRESS",
                "type": "circle",
                "floor": 0,
                "ceiling": 1700,
                "center": "KMHR",
                "radius": 5,
            },
            {
                "id": "RNOMSAW",
                "description": "RNO MSAW SUPPRESS",
                "type": "circle",
                "floor": 0,
                "ceiling": 8400,
                "center": "KRNO",
                "radius": 8,
            },
        ]
    )

    # Add second quicklook entry (from nctd)
    filters["quicklook"].append(
        {
            "id": "Q81",
            "description": "TOGA-SUTRO QUICKLOOK",
            "type": "polygon",
            "floor": 5000,
            "ceiling": 21000,
            "vertices": [
                "N037.22.54.060,W122.08.24.135",
                "N037.29.06.002,W122.09.55.184",
                "N037.33.05.737,W122.02.52.787",
                "N037.28.43.370,W121.43.03.544",
                "N037.13.01.732,W121.21.35.178",
                "N037.02.07.276,W121.34.16.063",
                "N037.08.08.287,W121.54.11.431",
            ],
        }
    )

    save_json(path, data)
    print(f"Fixed {path}")


def fix_zdc_pct():
    """Fix PCT filters: add ACQ3 and ACQ4, add 'I NOCA' inhibit_ca entry."""
    path = CONFIGS / "ZDC" / "PCT.json"
    data = load_json(path)
    filters = data["facility_adaptations"]["filters"]

    # Add ACQ3: MTV AUTO-ACQUISITION (from dca/pct-consolidated old)
    filters["auto_acquisition"].append(
        {
            "id": "ACQ3",
            "description": "MTV AUTO-ACQUISITION",
            "type": "polygon",
            "vertices": "N039.13.03.311,W077.25.51.708 N039.19.37.528,W077.03.50.383 N039.15.26.916,W076.44.08.254 N039.11.46.036,W076.30.16.094 N038.57.59.479,W076.26.02.503 N038.55.55.114,W076.13.31.944 N038.53.44.803,W076.03.13.524 N038.40.37.234,W076.02.15.296 N038.38.53.770,W076.21.35.068 N038.27.32.206,W076.19.52.977 N038.14.02.939,W076.39.44.170 N038.03.21.557,W076.56.58.725 N037.56.43.962,W077.07.06.159 N037.48.32.036,W077.13.11.949 N037.39.12.612,W077.18.29.371 N037.43.45.567,W077.34.17.601 N038.32.33.602,W078.21.12.930 N039.08.17.103,W078.26.24.118 N039.18.48.419,W078.04.34.658 N039.12.38.592,W077.38.22.185 N039.13.03.311,W077.25.51.708",
            "floor": 0,
            "ceiling": 23000,
        }
    )

    # Add ACQ4: JRV AUTO-ACQUISITION (from ric old)
    filters["auto_acquisition"].append(
        {
            "id": "ACQ4",
            "description": "JRV AUTO-ACQUISITION",
            "type": "polygon",
            "vertices": "N037.33.23.109,W078.54.05.443 N037.41.13.764,W079.32.24.580 N038.33.19.717,W079.34.35.399 N038.42.45.705,W078.15.19.116 N038.58.16.138,W077.42.15.699 N039.05.29.205,W076.24.45.379 N038.53.09.221,W074.51.31.525 N038.28.41.543,W074.51.56.519 N037.04.19.181,W075.45.54.052 N036.48.28.753,W077.40.54.263 N036.18.04.226,W078.09.45.241 N036.38.48.002,W079.06.00.269 N037.33.23.109,W078.54.05.443",
            "floor": 0,
            "ceiling": 25000,
        }
    )

    # Add "I NOCA" inhibit_ca entry (from ric old)
    filters["inhibit_ca"].append(
        {
            "id": "I NOCA",
            "description": "RICHMOND NO CA",
            "type": "polygon",
            "floor": 0,
            "ceiling": 5000,
            "vertices": [
                "N038.56.59.316,W077.28.51.829",
                "N038.59.45.813,W077.35.43.624",
                "N038.57.32.549,W077.36.42.456",
                "N038.55.44.210,W077.29.11.083",
                "N038.55.27.456,W077.28.36.860",
                "N038.40.33.279,W077.28.55.647",
                "N038.40.36.424,W077.25.38.772",
                "N038.55.32.400,W077.25.57.614",
                "N039.12.59.095,W077.25.41.381",
                "N039.12.54.742,W077.28.29.143",
                "N038.56.52.875,W077.28.37.108",
                "N038.56.59.316,W077.28.51.829",
            ],
        }
    )

    save_json(path, data)
    print(f"Fixed {path}")


def fix_zdv_d01():
    """Fix D01 filters: add CYSNOCA inhibit_ca entry from cys.json old."""
    path = CONFIGS / "ZDV" / "D01.json"
    data = load_json(path)
    filters = data["facility_adaptations"]["filters"]

    filters["inhibit_ca"].append(
        {
            "id": "CYSNOCA",
            "description": "CYS CONFLICT SUPPRESS",
            "type": "circle",
            "floor": 0,
            "ceiling": 9500,
            "radius": 10,
            "center": "N041.09.19.519,W104.48.39.012",
        }
    )

    save_json(path, data)
    print(f"Fixed {path}")


def main():
    fix_zbw_a90()
    fix_zla_sct()
    fix_zny_n90()
    fix_zoa_nct()
    fix_zdc_pct()
    fix_zdv_d01()
    print("All filter fixes applied.")


if __name__ == "__main__":
    main()
