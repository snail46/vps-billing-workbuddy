#!/usr/bin/env bash
#
# Local verification for the foundation.
#
# Runs everything that can be checked without a container runtime. ADR-003
# records why that boundary exists: the development machine has neither Docker
# nor WSL, so the compose Gate and the integration test tier run in CI, and
# everything else is expected to pass locally before a push.
#
# Usage (from the repository root):
#
#   bash scripts/verify-local.sh
#
# `set -e` is deliberately NOT used: this script exists to run every check and
# report the whole picture in one pass, so each command's failure is handled
# explicitly instead of aborting at the first problem.

set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root" || exit 1

# Sticky flag: any failure sets it, and the script exits non-zero at the end.
failures=0

section() { printf '\n\033[1m== %s ==\033[0m\n' "$1"; }
ok() { printf '  ok    %s\n' "$1"; }
bad() {
  printf '  FAIL  %s\n' "$1"
  failures=1
}
skip() { printf '  skip  %s\n' "$1"; }

# Runs a command as a named check. The label is what a reader needs, so the
# command's own output is left in place rather than captured.
run() {
  local label="$1"
  shift
  if "$@"; then
    ok "$label"
  else
    bad "$label"
  fi
}

require() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'error: %s is required but was not found on PATH\n' "$1" >&2
    exit 127
  fi
}

require go
require npm

# ---------------------------------------------------------------------------
section "Backend"
# ---------------------------------------------------------------------------
cd backend || exit 1

unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  bad "gofmt (these files need formatting)"
  printf '%s\n' "$unformatted"
else
  ok gofmt
fi

run "go vet" go vet ./...
run "go build" go build ./...

# The integration tier needs a database, so its tests skip themselves here. They
# run in the backend-integration CI job, which provides PostgreSQL and Redis.
run "go test (unit tier; integration tier skipped without TEST_DATABASE_URL)" \
  go test -count=1 ./...

if command -v golangci-lint >/dev/null 2>&1; then
  run "golangci-lint" golangci-lint run ./...
else
  skip "golangci-lint (not installed; CI always runs it)"
fi

cd "$repo_root" || exit 1

# ---------------------------------------------------------------------------
section "Frontend"
# ---------------------------------------------------------------------------
cd frontend || exit 1

run "npm run typecheck" npm run typecheck
run "npm run lint" npm run lint
run "npm test" npm test
run "npm run build" npm run build

cd "$repo_root" || exit 1

# ---------------------------------------------------------------------------
section "Configuration syntax"
# ---------------------------------------------------------------------------
# Static validation only. CI performs the authoritative `docker compose config -q`
# check, which validates compose semantics rather than just YAML syntax.
if command -v python3 >/dev/null 2>&1 || command -v python >/dev/null 2>&1; then
  python_bin="$(command -v python3 || command -v python)"
  # Relative paths on purpose: this script has already changed into the
  # repository root, and a bash-style path such as /c/... is not resolvable by a
  # native interpreter on Windows.
  if ! "$python_bin" - <<'PY'
import pathlib
import sys

try:
    import yaml
except ImportError:
    print("  skip  yaml parse (PyYAML not installed)")
    sys.exit(0)

failed = False
for relative in ("deploy/docker-compose.yml", ".github/workflows/ci.yml"):
    try:
        yaml.safe_load(pathlib.Path(relative).read_text(encoding="utf-8"))
        print(f"  ok    yaml parses: {relative}")
    except Exception as error:
        print(f"  FAIL  yaml: {relative}: {error}")
        failed = True
sys.exit(1 if failed else 0)
PY
  then
    failures=1
  fi
else
  skip "yaml parse (no python interpreter)"
fi

printf '\n'
if [ "$failures" -ne 0 ]; then
  printf '\033[31mLocal verification FAILED\033[0m\n'
  exit 1
fi

printf '\033[32mLocal verification passed.\033[0m\n'
printf 'Not covered here (needs a container runtime, see ADR-003): the compose Gate and the integration test tier.\n'
