#!/usr/bin/env bash
set -euo pipefail

BACKUP_ROOT="/var/backups/ops/redis"
TS="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_ROOT/redis-$TS.tar.gz"
DATA_DIR="/root/sub2api-deploy/redis_data"
mkdir -p "$BACKUP_ROOT" /var/log/backup

if docker ps --format "{{.Names}}" | grep -Fxq sub2api-redis; then
  docker exec sub2api-redis sh -c 'redis-cli BGSAVE >/dev/null 2>&1 || true' || true
  sleep 2
fi

if [ ! -d "$DATA_DIR" ]; then
  echo "redis data dir not found: $DATA_DIR" >&2
  exit 1
fi

tar -czf "$OUT" -C "$(dirname "$DATA_DIR")" "$(basename "$DATA_DIR")"
tar -tzf "$OUT" >/dev/null
find "$BACKUP_ROOT" -type f -name "redis-*.tar.gz" -mtime +7 -delete
printf "%s\n" "$OUT"
