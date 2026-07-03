#!/usr/bin/env bash
set -euo pipefail

BACKUP_ROOT="/var/backups/ops/volumes"
TS="$(date +%Y%m%d-%H%M%S)"
mkdir -p "$BACKUP_ROOT" /var/log/backup
made=0

if [ -d /var/lib/sub2api/data ]; then
  out="$BACKUP_ROOT/sub2api-data-$TS.tar.gz"
  tar --exclude="data/logs" \
      --exclude="data/logs/*" \
      -czf "$out" -C /var/lib/sub2api data
  tar -tzf "$out" >/dev/null
  echo "$out"
  made=$((made + 1))
fi

if docker volume inspect jadeai-data >/dev/null 2>&1; then
  mp="$(docker volume inspect -f '{{.Mountpoint}}' jadeai-data)"
  if [ -d "$mp" ]; then
    out="$BACKUP_ROOT/jadeai-data-$TS.tar.gz"
    tar -czf "$out" -C "$mp" .
    tar -tzf "$out" >/dev/null
    echo "$out"
    made=$((made + 1))
  fi
fi

if [ "$made" -eq 0 ]; then
  echo "no non-database volumes backed up" >&2
  exit 1
fi
find "$BACKUP_ROOT" -type f -name "*.tar.gz" -mtime +7 -delete
