#!/usr/bin/env python3
"""Assertions about the migration history.

Three properties have to hold together, and each has a way of quietly breaking:

1. **Reversibility.** The CI integration job applies the whole history forward and
   then rolls it back. A migration without a down script makes that impossible, and
   the job's failure would point at the migration rather than at the missing file.

2. **sqlc is generated from the migrations.** sqlc reads the schema from an
   explicit list of up migrations (`sqlc.yaml`), because a directory would include
   the down scripts and leave it with a schema in which nothing exists. A phase
   that adds a migration without adding it to that list gets generated code that
   silently does not know about the new tables — which surfaces later as a
   compile error in the worst case and as wrong SQL in the better one.

3. **Contiguous versions.** golang-migrate applies versions in order and reports
   the highest one; a gap means a migration that will never run on an environment
   that already passed the gap, which is invisible on a fresh database.

Usage:
    python scripts/check-migrations.py [repository-root]
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

MIGRATIONS = "backend/migrations"
SQLC_CONFIG = "backend/sqlc.yaml"

failures = 0


def check(condition: bool, message: str) -> None:
    global failures
    if condition:
        print(f"  ok    {message}")
    else:
        print(f"  FAIL  {message}")
        failures = 1


def main(argv: list[str]) -> int:
    root = Path(argv[1]).resolve() if len(argv) > 1 else Path(__file__).resolve().parent.parent

    migrations_dir = root / MIGRATIONS
    config_path = root / SQLC_CONFIG
    for path in (migrations_dir, config_path):
        if not path.exists():
            print(f"  FAIL  {path} is missing")
            return 1

    up_files = sorted(p.name for p in migrations_dir.glob("*.up.sql"))
    down_files = sorted(p.name for p in migrations_dir.glob("*.down.sql"))

    if not up_files:
        print("  FAIL  no up migrations were found; the glob or the layout has changed")
        return 1

    print(f"migrations: {MIGRATIONS}")

    ups = {name[: -len(".up.sql")] for name in up_files}
    downs = {name[: -len(".down.sql")] for name in down_files}
    for stem in sorted(ups - downs):
        check(False, f"{stem} has an up migration but no down migration")
    for stem in sorted(downs - ups):
        check(False, f"{stem} has a down migration but no up migration")
    if ups == downs:
        check(True, f"all {len(ups)} migrations are reversible (up and down both present)")

    # Versions are the numeric prefix before the first underscore.
    versions: list[int] = []
    for stem in sorted(ups):
        prefix = stem.split("_", 1)[0]
        if not re.fullmatch(r"\d+", prefix):
            check(False, f"{stem} does not start with a numeric version")
            continue
        versions.append(int(prefix))

    if versions:
        expected = list(range(1, len(versions) + 1))
        check(versions == expected,
              f"versions are contiguous from 1 ({versions[0]}..{versions[-1]}, "
              f"{len(versions)} migrations)")

    # sqlc must be generated from every up migration.
    config_text = config_path.read_text(encoding="utf-8")
    listed = set(re.findall(r'^\s*-\s*"migrations/([^"]+)"\s*$', config_text, re.MULTILINE))
    if not listed:
        check(False, f"no migration is listed under `schema:` in {SQLC_CONFIG}; the scanner or "
                     f"the configuration has changed")
    else:
        for name in up_files:
            if name not in listed:
                check(False, f"{name} is not listed in {SQLC_CONFIG}, so generated code will "
                             f"not see its tables")
        for name in sorted(listed - set(up_files)):
            check(False, f"{SQLC_CONFIG} lists migrations/{name}, which does not exist")
        if listed == set(up_files):
            check(True, f"sqlc is generated from all {len(up_files)} up migrations and nothing else")

    print()
    if failures:
        print("check-migrations FAILED")
        return 1
    print("check-migrations passed")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
