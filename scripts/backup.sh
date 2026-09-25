#!/usr/bin/env bash
# Logical backup of the deployment's PostgreSQL (ADR-015 §3).
# Usage: scripts/backup.sh [output-directory]
set -euo pipefail
cd "$(dirname "$0")/.."

out_dir="${1:-backups}"
mkdir -p "$out_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
target="$out_dir/vps_billing_$stamp.sql.gz"

echo "dumping to $target"
docker compose -f deploy/docker-compose.yml exec -T postgres \
  pg_dump -U vps_billing -d vps_billing --no-owner --clean \
  | gzip > "$target"
echo "backup complete: $target"
