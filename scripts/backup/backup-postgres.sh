#!/usr/bin/env bash
set -euo pipefail

umask 077
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ "${OPS_BACKUP_JOB_WRAPPED:-0}" != 1 ]; then
  exec "$SCRIPT_DIR/run-backup-job.sh" postgres "$@"
fi

BACKUP_ROOT="/var/backups/ops/postgres"
TS="$(date +%Y%m%d-%H%M%S)"
install -d -m 0700 "$BACKUP_ROOT"
install -d -m 0750 /var/log/backup
containers=(sub2api-postgres account-vault-postgres-1 areaforge-postgres)
made=0

for c in "${containers[@]}"; do
  if docker ps --format "{{.Names}}" | grep -Fxq "$c"; then
    out="$BACKUP_ROOT/${c}-${TS}.sql.gz"
    docker exec "$c" sh -c 'user="${POSTGRES_USER:-postgres}"; pg_dumpall -U "$user"' | gzip -c > "$out"
    gzip -t "$out"
    [ -s "$out" ]
    chmod 0600 "$out"
    echo "$out"
    made=$((made + 1))
  else
    echo "skip missing container: $c" >&2
  fi
done

if [ "$made" -eq 0 ]; then
  echo "no postgres containers backed up" >&2
  exit 1
fi
find "$BACKUP_ROOT" -type f -name "*.sql.gz" -mtime +7 -delete
