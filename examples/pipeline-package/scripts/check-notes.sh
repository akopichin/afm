#!/usr/bin/env bash
# Fails when the release notes are missing, lack the heading or still contain TODO markers.
set -euo pipefail

file="${1:?usage: check-notes.sh <file>}"
[[ -s "$file" ]] || { echo "$file is missing or empty" >&2; exit 1; }
grep -q '^# Release notes' "$file" || { echo "$file has no '# Release notes' heading" >&2; exit 1; }
if grep -q 'TODO' "$file"; then
    echo "$file still contains TODO" >&2
    exit 1
fi
echo "release notes look fine (package: $AFM_PIPELINE_DIR)"
