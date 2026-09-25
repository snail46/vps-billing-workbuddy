#!/usr/bin/env bash
# Restore of a backup made by scripts/backup.sh (ADR-015 §3).
# Usage: scripts/restore.sh backups/vps_billing_<stamp>.sql.gz
# DANGEROUS: this drops and recreates the schema from the dump.
set -euo pipefail
cd "$(dirname "$0")/.."

dump="${1:?usage: scripts/restore.sh <dump.sql.gz>}"
if [ ! -f "$dump" ]; then
  echo "no such dump: $dump" >&2
  exit 1
fi
echo "restoring $dump — this REPLACES the current database. Ctrl-C to abort."
sleep 3
gunzip -c "$dump" | docker compose -f deploy/docker-compose.yml exec -T postgres \
  psql -U vps_billing -d vps_billing
echo "restore complete"
