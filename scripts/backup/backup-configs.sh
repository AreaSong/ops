#!/usr/bin/env bash
set -euo pipefail

BACKUP_ROOT="/var/backups/ops/configs"
TS="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_ROOT/configs-$TS.tar.gz"
mkdir -p "$BACKUP_ROOT" /var/log/backup

items=()
for p in /etc/x-ui /etc/nginx /opt/ops /opt/services; do
  [ -e "$p" ] && items+=("$p")
done
for p in /root/sub2api-deploy/docker-compose.yml /root/sub2api-deploy/compose.yml /root/sub2api-deploy/.env /root/sub2api-deploy/*.env; do
  [ -e "$p" ] && items+=("$p")
done

if [ "${#items[@]}" -eq 0 ]; then
  echo "no config items found" >&2
  exit 1
fi

tar --exclude="/opt/ops/.git" \
    --exclude="/root/sub2api-deploy/postgres_data" \
    --exclude="/root/sub2api-deploy/redis_data" \
    --exclude="/root/sub2api-deploy/data" \
    -czf "$OUT" "${items[@]}"

tar -tzf "$OUT" >/dev/null
find "$BACKUP_ROOT" -type f -name "configs-*.tar.gz" -mtime +7 -delete
printf "%s\n" "$OUT"
