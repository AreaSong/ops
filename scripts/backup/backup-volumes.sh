#!/usr/bin/env bash
set -euo pipefail

umask 077
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ "${OPS_BACKUP_JOB_WRAPPED:-0}" != 1 ]; then
  exec "$SCRIPT_DIR/run-backup-job.sh" volumes "$@"
fi

BACKUP_ROOT="${BACKUP_VOLUME_BACKUP_ROOT:-/var/backups/ops/volumes}"
LOG_DIR="${BACKUP_VOLUME_LOG_DIR:-/var/log/backup}"
AREASONG_OPS_STATE_ROOT="${BACKUP_AREASONG_OPS_STATE_ROOT:-/var/lib/areasong-ops}"
AREASONG_OPS_SNAPSHOT_MAX_AGE_SECONDS="${BACKUP_AREASONG_OPS_SNAPSHOT_MAX_AGE_SECONDS:-90000}"
TS="$(date +%Y%m%d-%H%M%S)"
install -d -m 0700 "$BACKUP_ROOT"
install -d -m 0750 "$LOG_DIR"
made=0

backup_docker_volume() {
  local volume_name="$1"
  local output_prefix="$2"
  local mp out

  if ! docker volume inspect "$volume_name" >/dev/null 2>&1; then
    echo "skip missing volume: $volume_name" >&2
    return
  fi

  mp="$(docker volume inspect -f '{{.Mountpoint}}' "$volume_name")"
  if [ ! -d "$mp" ]; then
    echo "skip missing volume mountpoint: $volume_name" >&2
    return
  fi

  out="$BACKUP_ROOT/${output_prefix}-$TS.tar.gz"
  tar -czf "$out" -C "$mp" .
  tar -tzf "$out" >/dev/null
  chmod 0600 "$out"
  echo "$out"
  made=$((made + 1))
}

backup_directory() {
  local source_dir="$1"
  local output_prefix="$2"
  local out

  if [ ! -d "$source_dir" ]; then
    echo "skip missing directory: $source_dir" >&2
    return
  fi

  out="$BACKUP_ROOT/${output_prefix}-$TS.tar.gz"
  tar -czf "$out" -C "$source_dir" .
  tar -tzf "$out" >/dev/null
  chmod 0600 "$out"
  echo "$out"
  made=$((made + 1))
}

backup_areasong_ops_state() {
  local source_dir="$AREASONG_OPS_STATE_ROOT"
  local snapshot_dir="$source_dir/snapshots"
  local snapshot="" candidate
  local work_dir staged out
  local snapshots=()

  if [ ! -e "$source_dir" ]; then
    echo "skip missing directory: $source_dir" >&2
    return
  fi
  [ -d "$source_dir" ] && [ ! -L "$source_dir" ] || {
    echo "unsafe AreaSong Ops state root: $source_dir" >&2
    exit 1
  }
  [ -d "$snapshot_dir" ] && [ ! -L "$snapshot_dir" ] || {
    echo "AreaSong Ops snapshot directory is missing or unsafe" >&2
    exit 1
  }
  shopt -s nullglob
  snapshots=("$snapshot_dir"/ops-*.db)
  shopt -u nullglob
  for candidate in "${snapshots[@]}"; do
    [ -f "$candidate" ] && [ ! -L "$candidate" ] || continue
    if [ -z "$snapshot" ] || [ "$candidate" -nt "$snapshot" ]; then
      snapshot="$candidate"
    fi
  done
  [ -n "$snapshot" ] || {
    echo "AreaSong Ops has no safe SQLite snapshot" >&2
    exit 1
  }

  work_dir="$(mktemp -d "${TMPDIR:-/var/tmp}/areasong-ops-backup.XXXXXX")"
  staged="$work_dir/areasong-ops-state"
  install -d -m 0700 "$staged"
  cleanup_areasong_ops_backup() { rm -rf -- "$work_dir"; }
  trap cleanup_areasong_ops_backup EXIT

  /usr/bin/python3 - "$snapshot" "$staged/ops.db" "$AREASONG_OPS_SNAPSHOT_MAX_AGE_SECONDS" <<'PY'
import os
import shutil
import sqlite3
import sys
import time

source, destination, max_age_raw = sys.argv[1:]
max_age = int(max_age_raw)
if max_age <= 0:
    raise SystemExit("snapshot maximum age must be positive")
stat = os.lstat(source)
if not os.path.isfile(source) or os.path.islink(source):
    raise SystemExit("snapshot must be a regular non-symlink file")
age = time.time() - stat.st_mtime
if age < -60 or age > max_age:
    raise SystemExit(f"AreaSong Ops snapshot age is outside the allowed window: {int(age)}s")
connection = sqlite3.connect(f"file:{source}?mode=ro", uri=True)
try:
    result = connection.execute("PRAGMA integrity_check").fetchone()
    if result != ("ok",):
        raise SystemExit("AreaSong Ops snapshot integrity_check failed")
    if connection.execute("PRAGMA foreign_key_check").fetchone() is not None:
        raise SystemExit("AreaSong Ops snapshot foreign_key_check failed")
    required_columns = {
        "previews": {"id", "actor_hash", "service", "action", "confirmation_hash", "created_at", "expires_at"},
        "tasks": {"id", "idempotency_key", "request_hash", "actor_hash", "service", "action", "state", "preview_id", "snapshot_json", "created_at"},
        "events": {"sequence", "task_id", "occurred_at", "level", "message", "data_json"},
        "audit_entries": {"sequence", "occurred_at", "actor_hash", "event", "resource", "outcome", "detail_json"},
        "metadata": {"key", "value"},
    }
    tables = {row[0] for row in connection.execute(
        "SELECT name FROM sqlite_master WHERE type = 'table'"
    )}
    if not required_columns.keys() <= tables:
        raise SystemExit("AreaSong Ops snapshot is missing required tables")
    for table, required in required_columns.items():
        columns = {row[1] for row in connection.execute(f'PRAGMA table_info("{table}")')}
        missing = required - columns
        if missing:
            raise SystemExit(
                f"AreaSong Ops snapshot table {table} is missing required columns: {', '.join(sorted(missing))}"
            )
        count = connection.execute(f'SELECT COUNT(*) FROM "{table}"').fetchone()
        if count is None or len(count) != 1 or not isinstance(count[0], int) or count[0] < 0:
            raise SystemExit(f"AreaSong Ops snapshot table {table} row count is invalid")
finally:
    connection.close()
shutil.copyfile(source, destination)
os.chmod(destination, 0o600)
PY

  if [ -e "$source_dir/operations" ]; then
    [ -d "$source_dir/operations" ] && [ ! -L "$source_dir/operations" ] || {
      echo "unsafe AreaSong Ops operations directory" >&2
      exit 1
    }
    if find "$source_dir/operations" -xdev -type l -print -quit | grep -q .; then
      echo "AreaSong Ops operations directory contains a symbolic link" >&2
      exit 1
    fi
    cp -a "$source_dir/operations" "$staged/operations"
  fi

  out="$BACKUP_ROOT/areasong-ops-state-$TS.tar.gz"
  tar -czf "$out" -C "$work_dir" areasong-ops-state
  tar -tzf "$out" >/dev/null
  chmod 0600 "$out"
  echo "$out"
  made=$((made + 1))
  trap - EXIT
  cleanup_areasong_ops_backup
}

if [ -d /var/lib/sub2api/data ]; then
  out="$BACKUP_ROOT/sub2api-data-$TS.tar.gz"
  tar --exclude="data/logs" \
      --exclude="data/logs/*" \
      -czf "$out" -C /var/lib/sub2api data
  tar -tzf "$out" >/dev/null
  chmod 0600 "$out"
  echo "$out"
  made=$((made + 1))
fi

backup_docker_volume jadeai-data jadeai-data
backup_docker_volume areaforge_areaforge-uploads areaforge-uploads
backup_directory /opt/areaforge/ops-state areaforge-ops-state
backup_areasong_ops_state

if [ "$made" -eq 0 ]; then
  echo "no non-database volumes backed up" >&2
  exit 1
fi
find "$BACKUP_ROOT" -type f -name "*.tar.gz" -mtime +7 -delete
