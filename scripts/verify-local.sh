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

# Optional. Used for the configuration checks that would otherwise only run in
# CI, so their absence is reported rather than silently skipped.
python_bin="$(command -v python3 || command -v python || true)"

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

# A Docker build stage sees only what its Dockerfile COPYs. A file the build
# reads but the Dockerfile never copies therefore passes every check above and
# fails only inside the image — which is how the Phase 0 Gate first broke, with
# tsconfig.base.json missing and `tsc` aborting with TS5083. This reproduces each
# image's type-check from the COPY set the Dockerfile actually declares.
if [ -n "$python_bin" ]; then
  # Relative paths on purpose, for the same reason as the yaml check below: a
  # bash-style path such as /c/... is not resolvable by a native interpreter on
  # Windows, which reads it as C:\c\... and exits 2 without running anything.
  for app in user-web admin-web; do
    run "docker build context type-checks $app" \
      "$python_bin" ../scripts/check-docker-context.py . "$app/Dockerfile" "$app"
  done
else
  skip "docker build context (no python interpreter)"
fi

cd "$repo_root" || exit 1

# ---------------------------------------------------------------------------
section "Configuration syntax"
# ---------------------------------------------------------------------------
# Static validation only. CI performs the authoritative `docker compose config -q`
# check, which validates compose semantics rather than just YAML syntax.
if [ -n "$python_bin" ]; then
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

# Structural assertions about the foundation environment. They live here rather
# than only in CI because the failure this guards against — the image declaring an
# ENTRYPOINT while compose selects the binary with `command` — is invisible to
# every other check and hung the Gate for 24 minutes before anyone could see it.
if [ -n "$python_bin" ]; then
  # A relative path, for the same reason as above.
  run "compose structure" "$python_bin" scripts/check-compose.py
  run "migration history" "$python_bin" scripts/check-migrations.py
else
  skip "compose structure (no python interpreter)"
  skip "migration history (no python interpreter)"
fi

printf '\n'
if [ "$failures" -ne 0 ]; then
  printf '\033[31mLocal verification FAILED\033[0m\n'
  exit 1
fi

printf '\033[32mLocal verification passed.\033[0m\n'
printf 'Not covered here (needs a container runtime, see ADR-003): the compose Gate and the integration test tier.\n'
