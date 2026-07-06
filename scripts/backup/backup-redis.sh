#!/usr/bin/env bash
set -euo pipefail

BACKUP_ROOT="/var/backups/ops/redis"
TS="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_ROOT/redis-$TS.tar.gz"
DATA_DIR="/var/lib/sub2api/redis_data"
RDB_FILE="$DATA_DIR/dump.rdb"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$BACKUP_ROOT" /var/log/backup

if ! docker ps --format "{{.Names}}" | grep -Fxq sub2api-redis; then
  echo "redis container not running: sub2api-redis" >&2
  exit 1
fi

# Create an online RDB snapshot and wait until Redis reports it completed.
docker exec sub2api-redis redis-cli BGSAVE >/dev/null 2>&1 || true
for _ in $(seq 1 60); do
  persistence="$(docker exec sub2api-redis redis-cli INFO persistence | tr -d '\r')"
  in_progress="$(printf '%s\n' "$persistence" | awk -F: '$1 == "rdb_bgsave_in_progress" {print $2}')"
  last_status="$(printf '%s\n' "$persistence" | awk -F: '$1 == "rdb_last_bgsave_status" {print $2}')"
  if [ "${in_progress:-1}" = "0" ]; then
    if [ "${last_status:-}" = "ok" ]; then
      break
    fi
    echo "redis BGSAVE finished with status: ${last_status:-unknown}" >&2
    exit 1
  fi
  sleep 1
done

persistence="$(docker exec sub2api-redis redis-cli INFO persistence | tr -d '\r')"
in_progress="$(printf '%s\n' "$persistence" | awk -F: '$1 == "rdb_bgsave_in_progress" {print $2}')"
last_status="$(printf '%s\n' "$persistence" | awk -F: '$1 == "rdb_last_bgsave_status" {print $2}')"
if [ "${in_progress:-1}" != "0" ] || [ "${last_status:-}" != "ok" ]; then
  echo "redis BGSAVE did not complete in time" >&2
  exit 1
fi

if [ ! -s "$RDB_FILE" ]; then
  echo "redis RDB snapshot not found or empty: $RDB_FILE" >&2
  exit 1
fi

mkdir -p "$TMP_DIR/redis_data"
cp -p "$RDB_FILE" "$TMP_DIR/redis_data/dump.rdb"
cat > "$TMP_DIR/metadata.txt" <<META
created_at=$TS
source_container=sub2api-redis
source_data_dir=$DATA_DIR
format=redis-rdb-snapshot
restore_note=restore dump.rdb into Redis data dir during a controlled maintenance window
META

tar -czf "$OUT" -C "$TMP_DIR" metadata.txt redis_data/dump.rdb
tar -tzf "$OUT" >/dev/null
find "$BACKUP_ROOT" -type f -name "redis-*.tar.gz" -mtime +7 -delete
printf "%s\n" "$OUT"
