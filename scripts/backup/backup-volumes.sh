#!/usr/bin/env bash
set -euo pipefail

umask 077

BACKUP_ROOT="/var/backups/ops/volumes"
TS="$(date +%Y%m%d-%H%M%S)"
install -d -m 0700 "$BACKUP_ROOT"
install -d -m 0750 /var/log/backup
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

if [ "$made" -eq 0 ]; then
  echo "no non-database volumes backed up" >&2
  exit 1
fi
find "$BACKUP_ROOT" -type f -name "*.tar.gz" -mtime +7 -delete
