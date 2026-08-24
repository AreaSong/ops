#!/usr/bin/env bash
set -euo pipefail

umask 077
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ "${OPS_BACKUP_JOB_WRAPPED:-0}" != 1 ]; then
  exec "$SCRIPT_DIR/run-backup-job.sh" redis "$@"
fi

BACKUP_ROOT="${REDIS_BACKUP_ROOT:-/var/backups/ops/redis}"
TS="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_ROOT/redis-$TS.tar.gz"
SUB2API_ENV_FILE="${SUB2API_BACKUP_ENV_FILE:-/opt/services/sub2api/.env}"
ENV_READER="${OPS_RESTORE_ENV_READER:-$SCRIPT_DIR/restore_env.py}"
if [ -n "${REDIS_DATA_DIR:-}" ]; then
  DATA_DIR="$REDIS_DATA_DIR"
  DATA_DIR_EXPLICIT=1
elif [ -f "$SUB2API_ENV_FILE" ]; then
  DATA_DIR="$("$ENV_READER" --file "$SUB2API_ENV_FILE" --get SUB2API_REDIS_DATA_DIR --default /var/lib/sub2api/redis_data)"
  DATA_DIR_EXPLICIT=0
else
  DATA_DIR="/var/lib/sub2api/redis_data"
  DATA_DIR_EXPLICIT=0
fi
if [ "$DATA_DIR_EXPLICIT" -ne 1 ]; then
  case "$DATA_DIR" in
    /var/lib/sub2api/*) ;;
    *) echo "unsafe Redis data directory: $DATA_DIR" >&2; exit 1 ;;
  esac
fi
LOG_DIR="${BACKUP_LOG_DIR:-/var/log/backup}"
RDB_FILE="$DATA_DIR/dump.rdb"
ACL_FILE="$DATA_DIR/users.acl"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

install -d -m 0700 "$BACKUP_ROOT"
install -d -m 0750 "$LOG_DIR"

if ! docker ps --format "{{.Names}}" | grep -Fxq sub2api-redis; then
  echo "redis container not running: sub2api-redis" >&2
  exit 1
fi

read_persistence() {
  docker exec sub2api-redis redis-cli INFO persistence | tr -d '\r'
}

persistence_field() {
  local name="$1"
  awk -F: -v name="$name" '$1 == name {print $2}'
}

persistence="$(read_persistence)"
in_progress="$(printf '%s\n' "$persistence" | persistence_field rdb_bgsave_in_progress)"
last_save_before="$(printf '%s\n' "$persistence" | persistence_field rdb_last_save_time)"
[[ "${last_save_before:-}" =~ ^[0-9]+$ ]] || { echo "redis last-save timestamp is invalid" >&2; exit 1; }
[ "${in_progress:-1}" = 0 ] || { echo "redis BGSAVE is already in progress" >&2; exit 1; }
rdb_mtime_before="$(stat -c '%y' "$RDB_FILE" 2>/dev/null || printf 'missing')"
request_started_at="$(date +%s)"

docker exec sub2api-redis redis-cli BGSAVE >/dev/null
for _ in $(seq 1 60); do
  persistence="$(read_persistence)"
  in_progress="$(printf '%s\n' "$persistence" | persistence_field rdb_bgsave_in_progress)"
  last_status="$(printf '%s\n' "$persistence" | persistence_field rdb_last_bgsave_status)"
  if [ "${in_progress:-1}" = "0" ]; then
    if [ "${last_status:-}" = "ok" ]; then
      break
    fi
    echo "redis BGSAVE finished with status: ${last_status:-unknown}" >&2
    exit 1
  fi
  sleep 1
done

persistence="$(read_persistence)"
in_progress="$(printf '%s\n' "$persistence" | persistence_field rdb_bgsave_in_progress)"
last_status="$(printf '%s\n' "$persistence" | persistence_field rdb_last_bgsave_status)"
last_save_after="$(printf '%s\n' "$persistence" | persistence_field rdb_last_save_time)"
if [ "${in_progress:-1}" != "0" ] || [ "${last_status:-}" != "ok" ]; then
  echo "redis BGSAVE did not complete in time" >&2
  exit 1
fi
[[ "${last_save_after:-}" =~ ^[0-9]+$ ]] || { echo "redis last-save timestamp is invalid" >&2; exit 1; }
[ "$last_save_after" -ge "$request_started_at" ] || {
  echo "redis last-save timestamp did not advance for this backup" >&2
  exit 1
}

if [ ! -s "$RDB_FILE" ]; then
  echo "redis RDB snapshot not found or empty: $RDB_FILE" >&2
  exit 1
fi
rdb_mtime_after="$(stat -c '%y' "$RDB_FILE")"
[ "$rdb_mtime_after" != "$rdb_mtime_before" ] || {
  echo "redis RDB mtime did not advance for this backup" >&2
  exit 1
}

mkdir -p "$TMP_DIR/redis_data"
cp -p "$RDB_FILE" "$TMP_DIR/redis_data/dump.rdb"

ACL_INCLUDED="no"
if [ -s "$ACL_FILE" ]; then
  cp -p "$ACL_FILE" "$TMP_DIR/redis_data/users.acl"
  chmod 0600 "$TMP_DIR/redis_data/users.acl"
  ACL_INCLUDED="yes"
fi

cat > "$TMP_DIR/metadata.txt" <<META
created_at=$TS
source_container=sub2api-redis
source_data_dir=$DATA_DIR
format=redis-rdb-snapshot
rdb_last_save_time=$last_save_after
rdb_mtime=$rdb_mtime_after
aclfile_included=$ACL_INCLUDED
aclfile_note=users.acl contains Redis ACL password hashes when included; keep backup artifacts root-only
restore_note=restore dump.rdb into Redis data dir during a controlled maintenance window
META

tar -czf "$OUT" -C "$TMP_DIR" metadata.txt redis_data
chmod 0600 "$OUT"
tar -tzf "$OUT" >/dev/null
find "$BACKUP_ROOT" -type f -name "redis-*.tar.gz" -mtime +7 -delete
printf "%s\n" "$OUT"
