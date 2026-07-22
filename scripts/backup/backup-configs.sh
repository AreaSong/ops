#!/usr/bin/env bash
set -euo pipefail

umask 077
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ "${OPS_BACKUP_JOB_WRAPPED:-0}" != 1 ]; then
  exec "$SCRIPT_DIR/run-backup-job.sh" configs "$@"
fi

BACKUP_ROOT="/var/backups/ops/configs"
TS="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_ROOT/configs-$TS.tar.gz"
install -d -m 0700 "$BACKUP_ROOT"
install -d -m 0750 /var/log/backup

items=()
for p in /etc/x-ui /etc/nginx /etc/account-vault /opt/ops /opt/services /var/lib/ops/account-vault-release; do
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
chmod 0600 "$OUT"
find "$BACKUP_ROOT" -type f -name "configs-*.tar.gz" -mtime +7 -delete
printf "%s\n" "$OUT"
