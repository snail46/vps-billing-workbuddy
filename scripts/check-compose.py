#!/usr/bin/env python3
"""Structural assertions about the foundation environment.

These are the checks that would have caught the Gate's 24-minute hang before it
cost a single runner minute, plus the invariants the Gate relies on and that were
previously only verified by ad-hoc commands.

The bug it exists for: `backend/Dockerfile` declared ENTRYPOINT ["/app/server"]
while `deploy/docker-compose.yml` selected each service's binary with `command`.
Compose *overrides* CMD with `command` but *appends* `command` to ENTRYPOINT, so
the migrate service actually ran `/app/server /app/migrate up`. The API started
instead of the migration, never exited, and every service gated on
`service_completed_successfully` waited — silently, until the run was cancelled.

The file is parsed with a line scanner rather than a YAML library on purpose: the
checks must run in the local environment and in CI without adding a dependency,
and the assertions concern literal lines (a service's `command`, whether it has a
healthcheck) that anchors do not affect. To stay honest about that choice, the
scanner fails loudly when its assumptions stop holding — a scan that finds no
services, or a service with no body, is reported as an error rather than passed.

Usage:
    python scripts/check-compose.py [repository-root]
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

COMPOSE = "deploy/docker-compose.yml"
DOCKERFILE = "backend/Dockerfile"

EXPECTED_SERVICES = {"postgres", "redis", "migrate", "server", "worker", "user-web", "admin-web"}
BINARY_SERVICES = {"migrate": "/app/migrate", "server": "/app/server", "worker": "/app/worker"}

failures = 0


def check(condition: bool, message: str) -> None:
    global failures
    if condition:
        print(f"  ok    {message}")
    else:
        print(f"  FAIL  {message}")
        failures = 1


def parse_services(text: str) -> dict[str, list[str]]:
    """Returns each service's lines, keyed by service name."""
    services: dict[str, list[str]] = {}
    current: str | None = None
    inside = False

    for line in text.splitlines():
        if re.match(r"^services:\s*$", line):
            inside = True
            continue
        if not inside:
            continue
        # A new top-level key ends the services block.
        if re.match(r"^[A-Za-z]", line):
            break
        # Two-space indentation introduces a service; deeper indentation is its body.
        name = re.match(r"^  ([A-Za-z0-9_-]+):\s*$", line)
        if name:
            current = name.group(1)
            services[current] = []
            continue
        if current is not None:
            services[current].append(line)

    return services


def main(argv: list[str]) -> int:
    root = Path(argv[1]).resolve() if len(argv) > 1 else Path(__file__).resolve().parent.parent

    compose_path = root / COMPOSE
    dockerfile_path = root / DOCKERFILE
    for path in (compose_path, dockerfile_path):
        if not path.is_file():
            print(f"  FAIL  {path} is missing")
            return 1

    compose_text = compose_path.read_text(encoding="utf-8")
    dockerfile_text = dockerfile_path.read_text(encoding="utf-8")

    print(f"compose: {COMPOSE}")

    services = parse_services(compose_text)
    if services != EXPECTED_SERVICES and set(services) != EXPECTED_SERVICES:
        check(False, f"the services block parsed into {sorted(services)}; expected exactly "
                     f"{sorted(EXPECTED_SERVICES)}. If the file's shape changed, this scanner "
                     f"needs updating rather than ignoring.")
    else:
        check(True, f"exactly the {len(EXPECTED_SERVICES)} foundation services are declared")

    for name, body in sorted(services.items()):
        if not body:
            check(False, f"service {name} parsed with no body; the scanner is out of step "
                         f"with the file")
            continue

    # The assertions each service must satisfy. `command` selects the binary, so a
    # missing one means the service silently inherits the image default.
    for name, binary in sorted(BINARY_SERVICES.items()):
        body = services.get(name, [])
        match = next((re.match(r'^\s+command:\s*\[\s*"([^"]+)"', line) for line in body
                      if re.match(r'^\s+command:', line)), None)
        if match is None:
            check(False, f"{name} declares a command (otherwise it inherits the image default)")
            continue
        check(match.group(1) == binary,
              f"{name} runs {binary} (found {match.group(1)})")

    print(f"dockerfile: {DOCKERFILE}")

    # The regression guard. With ENTRYPOINT, compose appends `command` to it and
    # every service runs the server; CMD is what makes `command` replace it.
    entrypoint = [line for line in dockerfile_text.splitlines() if line.startswith("ENTRYPOINT")]
    check(not entrypoint,
          "the image declares no ENTRYPOINT, so compose's `command` replaces the default "
          "instead of being appended to it")
    check(any(line.startswith('CMD ["/app/server"]') for line in dockerfile_text.splitlines()),
          "the image's default CMD is the server")

    print(f"compose: {COMPOSE} (continued)")

    for name in ("postgres", "redis", "server"):
        check(any(re.match(r"^\s+healthcheck:\s*$", line) for line in services.get(name, [])),
              f"{name} declares a healthcheck")

    for name in ("server", "worker"):
        check(any("condition: service_completed_successfully" in line
                  for line in services.get(name, [])),
              f"{name} waits for the migration to complete rather than for the container to start")

    for volume in ("postgres_data", "redis_data"):
        check(re.search(rf"^  {volume}:\s*$", compose_text, re.MULTILINE) is not None,
              f"the {volume} volume is declared")

    print()
    if failures:
        print("check-compose FAILED")
        return 1
    print("check-compose passed")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
