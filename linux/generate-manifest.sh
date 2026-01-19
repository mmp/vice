#!/bin/bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "Usage: $0 <resources-dir> <output.json>" >&2
  exit 1
fi

ROOT_DIR="$(realpath "$1")"
OUT_FILE="$2"

if [[ ! -d "$ROOT_DIR" ]]; then
  echo "Error: $ROOT_DIR is not a directory" >&2
  exit 1
fi

tmpfile="$(mktemp)"
trap 'rm -f "$tmpfile"' EXIT

# Find files, sort for deterministic output
find "$ROOT_DIR" -type f -print0 \
  | sort -z \
  | while IFS= read -r -d '' file; do
      relpath="$(realpath --relative-to="$ROOT_DIR" "$file")"
      hash="$(sha256sum "$file" | awk '{print $1}')"
      printf '  "%s": "%s"\n' "$relpath" "$hash" >> "$tmpfile"
    done

# Write JSON
{
  echo "{"
  sed '$!s/$/,/' "$tmpfile"
  echo "}"
} > "$OUT_FILE"

echo "Generated \"$OUT_FILE\" with $(wc -l < "$tmpfile") entries"

