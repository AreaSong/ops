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
ENV_READER="${OPS_RESTORE_ENV_READER:-$SCRIPT_DIR/restore_env.py}"
SUB2API_ENV_FILE="${SUB2API_BACKUP_ENV_FILE:-/opt/services/sub2api/.env}"
AREAFORGE_ENV_FILE="${AREAFORGE_BACKUP_ENV_FILE:-/opt/areaforge/.env.production}"
if [ -n "${BACKUP_SUB2API_DATA_DIR:-}" ]; then
  SUB2API_DATA_DIR="$BACKUP_SUB2API_DATA_DIR"
elif [ -f "$SUB2API_ENV_FILE" ]; then
  SUB2API_DATA_DIR="$("$ENV_READER" --file "$SUB2API_ENV_FILE" --get SUB2API_DATA_DIR --default /var/lib/sub2api/data)"
else
  SUB2API_DATA_DIR="/var/lib/sub2api/data"
fi
if [ -n "${BACKUP_AREAFORGE_UPLOADS_VOLUME:-}" ]; then
  AREAFORGE_UPLOADS_VOLUME="$BACKUP_AREAFORGE_UPLOADS_VOLUME"
elif [ -f "$AREAFORGE_ENV_FILE" ]; then
  AREAFORGE_UPLOADS_VOLUME="$("$ENV_READER" --file "$AREAFORGE_ENV_FILE" --get AREAFORGE_UPLOADS_VOLUME --default areaforge_areaforge-uploads)"
else
  AREAFORGE_UPLOADS_VOLUME="areaforge_areaforge-uploads"
fi
if [ -n "${BACKUP_AREAFORGE_OPS_STATE_ROOT:-}" ]; then
  AREAFORGE_OPS_STATE_ROOT="$BACKUP_AREAFORGE_OPS_STATE_ROOT"
elif [ -f "$AREAFORGE_ENV_FILE" ]; then
  AREAFORGE_OPS_STATE_ROOT="$("$ENV_READER" --file "$AREAFORGE_ENV_FILE" --get AREAFORGE_OPS_STATE_HOST_DIR --default /opt/areaforge/ops-state)"
else
  AREAFORGE_OPS_STATE_ROOT="/opt/areaforge/ops-state"
fi
case "$SUB2API_DATA_DIR" in /var/lib/sub2api/*) ;; *) echo "unsafe Sub2API data directory: $SUB2API_DATA_DIR" >&2; exit 1 ;; esac
case "$AREAFORGE_OPS_STATE_ROOT" in /opt/areaforge/*) ;; *) echo "unsafe AreaForge ops-state directory: $AREAFORGE_OPS_STATE_ROOT" >&2; exit 1 ;; esac
case "$AREAFORGE_UPLOADS_VOLUME" in *[!A-Za-z0-9_.-]*|'') echo "unsafe AreaForge uploads volume: $AREAFORGE_UPLOADS_VOLUME" >&2; exit 1 ;; esac
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
  if [ ! -d "$source_dir" ] || [ -L "$source_dir" ]; then
    echo "unsafe AreaSong Ops state root: $source_dir" >&2
    exit 1
  fi
  if [ ! -d "$snapshot_dir" ] || [ -L "$snapshot_dir" ]; then
    echo "AreaSong Ops snapshot directory is missing or unsafe" >&2
    exit 1
  fi
  shopt -s nullglob
  snapshots=("$snapshot_dir"/ops-*.db)
  shopt -u nullglob
  for candidate in "${snapshots[@]}"; do
    if [ ! -f "$candidate" ] || [ -L "$candidate" ]; then
      continue
    fi
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
  cleanup_areasong_ops_backup() { rm -rf -- "$1"; }
  # EXIT 可能在函数局部作用域已退出后执行，必须固定本次 mktemp 路径。
  # shellcheck disable=SC2064
  trap "$(printf 'cleanup_areasong_ops_backup %q' "$work_dir")" EXIT

  /usr/bin/python3 -B "$SCRIPT_DIR/areasong_ops_snapshot.py" copy \
    "$snapshot" "$staged/ops.db" "$AREASONG_OPS_SNAPSHOT_MAX_AGE_SECONDS"

  if [ -e "$source_dir/operations" ]; then
    if [ ! -d "$source_dir/operations" ] || [ -L "$source_dir/operations" ]; then
      echo "unsafe AreaSong Ops operations directory" >&2
      exit 1
    fi
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
  cleanup_areasong_ops_backup "$work_dir"
}

if [ -d "$SUB2API_DATA_DIR" ]; then
  out="$BACKUP_ROOT/sub2api-data-$TS.tar.gz"
  tar --exclude="data/logs" \
      --exclude="data/logs/*" \
      -czf "$out" -C "$(dirname "$SUB2API_DATA_DIR")" "$(basename "$SUB2API_DATA_DIR")"
  tar -tzf "$out" >/dev/null
  chmod 0600 "$out"
  echo "$out"
  made=$((made + 1))
fi

backup_docker_volume jadeai-data jadeai-data
backup_docker_volume "$AREAFORGE_UPLOADS_VOLUME" areaforge-uploads
backup_directory "$AREAFORGE_OPS_STATE_ROOT" areaforge-ops-state
backup_areasong_ops_state

if [ "$made" -eq 0 ]; then
  echo "no non-database volumes backed up" >&2
  exit 1
fi
# 清理需校验跨作业引用并单独批准，不能在备份结束时按年龄重新扫描删除。
