#!/usr/bin/env bash
#
# Prints the schema version the migration history should be at: the highest
# numeric prefix among the up migrations, as a plain integer.
#
# This exists so the expectation is derived in exactly one place. It was
# previously a literal in two CI steps, then duplicated shell arithmetic, and the
# second form produced "0003" where golang-migrate reports "3" — a mismatch that
# reads like a migration defect and is not one. Consolidating it here removes the
# duplication, and scripts/check-migrations.py compares this script's output
# against its own parse so the two cannot drift apart unnoticed.
#
# Usage:
#   scripts/expected-migration-version.sh <migrations-directory>
#
# The directory is passed in rather than assumed, because the two callers run from
# different working directories.

set -uo pipefail

directory="${1:?usage: expected-migration-version.sh <migrations-directory>}"

latest="$(
  ls "$directory"/*.up.sql 2>/dev/null \
    | sed 's#.*/##; s/_.*//' \
    | sort -n \
    | tail -1
)"

if [ -z "$latest" ]; then
  printf 'expected-migration-version: no up migrations found in %s\n' "$directory" >&2
  exit 1
fi

# golang-migrate records the version as an integer, so leading zeros are removed
# rather than echoed. `10#` forces base ten, otherwise a value such as `0008` would
# be read as octal.
printf '%s\n' "$((10#$latest))"
