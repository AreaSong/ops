#!/usr/bin/env bash
set -euo pipefail

OUT="/var/lib/node_exporter/textfile_collector/backup.prom"
TMP="${OUT}.tmp"

mkdir -p "$(dirname "$OUT")"

now_for_latest() {
  local pattern="$1"
  local latest
  latest="$(find /var/backups/ops -type f -name "$pattern" -printf '%T@\n' 2>/dev/null | sort -n | tail -n 1 || true)"
  if [ -z "$latest" ]; then
    echo 0
  else
    printf '%.0f\n' "$latest"
  fi
}

{
  echo '# HELP backup_last_success_timestamp Unix timestamp of latest successful backup artifact.'
  echo '# TYPE backup_last_success_timestamp gauge'
  printf 'backup_last_success_timestamp{job="postgres"} %s\n' "$(now_for_latest '*.sql.gz')"
  printf 'backup_last_success_timestamp{job="redis"} %s\n' "$(now_for_latest 'redis-*.tar.gz')"
  printf 'backup_last_success_timestamp{job="configs"} %s\n' "$(now_for_latest 'configs-*.tar.gz')"
  printf 'backup_last_success_timestamp{job="volumes"} %s\n' "$(now_for_latest '*.tar.gz')"
} > "$TMP"

mv "$TMP" "$OUT"
chmod 0644 "$OUT"
