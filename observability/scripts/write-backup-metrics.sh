#!/usr/bin/env bash
set -euo pipefail

OUT="/var/lib/node_exporter/textfile_collector/backup.prom"
TMP="${OUT}.tmp"

mkdir -p "$(dirname "$OUT")"

now_for_latest() {
  local directory="$1"
  local pattern="$2"
  local latest
  latest="$(find "$directory" -maxdepth 1 -type f -name "$pattern" -printf '%T@\n' 2>/dev/null | sort -n | tail -n 1 || true)"
  if [ -z "$latest" ]; then
    echo 0
  else
    printf '%.0f\n' "$latest"
  fi
}

{
  echo '# HELP backup_last_success_timestamp Unix timestamp of latest successful backup artifact.'
  echo '# TYPE backup_last_success_timestamp gauge'
  printf 'backup_last_success_timestamp{backup="postgres-sub2api"} %s\n' "$(now_for_latest /var/backups/ops/postgres 'sub2api-postgres-*.sql.gz')"
  printf 'backup_last_success_timestamp{backup="postgres-account-vault"} %s\n' "$(now_for_latest /var/backups/ops/postgres 'account-vault-postgres-1-*.sql.gz')"
  printf 'backup_last_success_timestamp{backup="postgres-areaforge"} %s\n' "$(now_for_latest /var/backups/ops/postgres 'areaforge-postgres-*.sql.gz')"
  printf 'backup_last_success_timestamp{backup="redis"} %s\n' "$(now_for_latest /var/backups/ops/redis 'redis-*.tar.gz')"
  printf 'backup_last_success_timestamp{backup="configs"} %s\n' "$(now_for_latest /var/backups/ops/configs 'configs-*.tar.gz')"
  printf 'backup_last_success_timestamp{backup="volume-sub2api-data"} %s\n' "$(now_for_latest /var/backups/ops/volumes 'sub2api-data-*.tar.gz')"
  printf 'backup_last_success_timestamp{backup="volume-jadeai-data"} %s\n' "$(now_for_latest /var/backups/ops/volumes 'jadeai-data-*.tar.gz')"
  printf 'backup_last_success_timestamp{backup="volume-areaforge-uploads"} %s\n' "$(now_for_latest /var/backups/ops/volumes 'areaforge-uploads-*.tar.gz')"
  printf 'backup_last_success_timestamp{backup="volume-areaforge-ops-state"} %s\n' "$(now_for_latest /var/backups/ops/volumes 'areaforge-ops-state-*.tar.gz')"
} > "$TMP"

mv "$TMP" "$OUT"
chmod 0644 "$OUT"
