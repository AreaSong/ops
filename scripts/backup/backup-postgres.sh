#!/usr/bin/env bash
set -euo pipefail

umask 077
SCRIPT_DIR="$(cd -- "${BASH_SOURCE[0]%/*}" && pwd -P)"
# WRAPPED 只保留兼容标记；实际入口必须持有同一物理锁和内部合同。
if [[ "${1:-}" != --backup-internal-v1 ]]; then
  [[ "$#" -eq 0 && "${OPS_BACKUP_JOB_WRAPPED:-0}" != 1 ]] || { printf 'ERROR: internal_contract_required\n' >&2; exit 2; }
  exec "$SCRIPT_DIR/run-backup-job.sh" postgres
fi
[[ "$#" -eq 4 && "$2" == postgres ]] || { printf 'ERROR: internal_contract_rejected\n' >&2; exit 2; }
python3 -B "$SCRIPT_DIR/sub2api_backup_contract.py" shared "$2" "$3" "$4" 9 || exit 75
shift 4

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
# 清理需校验跨作业引用并单独批准，不能在备份结束时按年龄重新扫描删除。
