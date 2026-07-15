#!/usr/bin/env bash
set -euo pipefail

BACKUP_ROOT="/var/backups/ops/configs"
TS="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_ROOT/configs-$TS.tar.gz"
mkdir -p "$BACKUP_ROOT" /var/log/backup

items=()
for p in /etc/x-ui /etc/nginx /etc/account-vault /opt/ops /opt/services; do
  [ -e "$p" ] && items+=("$p")
done
for p in /var/lib/sub2api/docker-compose.yml /var/lib/sub2api/compose.yml /var/lib/sub2api/.env /var/lib/sub2api/*.env; do
  [ -e "$p" ] && items+=("$p")
done
for p in /opt/areaforge/docker-compose.prod.yml /opt/areaforge/.env.production; do
  [ -e "$p" ] && items+=("$p")
done

if [ "${#items[@]}" -eq 0 ]; then
  echo "no config items found" >&2
  exit 1
fi

tar --exclude="/opt/ops/.git" \
    --exclude="opt/ops/.git" \
    --exclude="/var/lib/sub2api/postgres_data" \
    --exclude="var/lib/sub2api/postgres_data" \
    --exclude="/var/lib/sub2api/redis_data" \
    --exclude="var/lib/sub2api/redis_data" \
    --exclude="/var/lib/sub2api/data" \
    --exclude="var/lib/sub2api/data" \
    --exclude="/var/lib/sub2api/data/*" \
    --exclude="var/lib/sub2api/data/*" \
    -czf "$OUT" "${items[@]}"

tar -tzf "$OUT" >/dev/null
find "$BACKUP_ROOT" -type f -name "configs-*.tar.gz" -mtime +7 -delete
printf "%s\n" "$OUT"
