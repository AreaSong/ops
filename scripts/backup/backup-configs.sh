#!/usr/bin/env bash
set -euo pipefail

umask 077
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ "${OPS_BACKUP_JOB_WRAPPED:-0}" != 1 ]; then
  exec "$SCRIPT_DIR/run-backup-job.sh" configs "$@"
fi

BACKUP_ROOT="${BACKUP_CONFIG_BACKUP_ROOT:-/var/backups/ops/configs}"
SOURCE_ROOT="${BACKUP_CONFIG_SOURCE_ROOT:-/}"
LOG_DIR="${BACKUP_CONFIG_LOG_DIR:-/var/log/backup}"
TS="$(date +%Y%m%d-%H%M%S)"
OUT="$BACKUP_ROOT/configs-$TS.tar.gz"
install -d -m 0700 "$BACKUP_ROOT"
install -d -m 0750 "$LOG_DIR"

SOURCE_ROOT="${SOURCE_ROOT%/}"
[ -n "$SOURCE_ROOT" ] || SOURCE_ROOT="/"
[ -d "$SOURCE_ROOT" ] || {
  echo "config source root not found: $SOURCE_ROOT" >&2
  exit 1
}

WORK_DIR="$(mktemp -d "${TMPDIR:-/var/tmp}/ops-config-backup.XXXXXX")"
cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM
install -d -m 0700 "$WORK_DIR/backup-metadata"
COVERAGE_TSV="$WORK_DIR/coverage.tsv"
COVERAGE_JSON="$WORK_DIR/backup-metadata/config-coverage.json"
: > "$COVERAGE_TSV"

items=()
missing_required=0

source_pattern() {
  local pattern="$1"
  if [ "$SOURCE_ROOT" = "/" ]; then
    printf '/%s\n' "$pattern"
  else
    printf '%s/%s\n' "$SOURCE_ROOT" "$pattern"
  fi
}

add_spec() {
  local requirement="$1"
  local classification="$2"
  local pattern="$3"
  local full_pattern match relative
  local matches=()

  full_pattern="$(source_pattern "$pattern")"
  while IFS= read -r match; do
    matches+=("$match")
  done < <(compgen -G "$full_pattern" | LC_ALL=C sort || true)
  if [ "${#matches[@]}" -eq 0 ]; then
    printf '%s\t%s\t%s\t%s\n' "missing" "$requirement" "$classification" "/$pattern" >> "$COVERAGE_TSV"
    if [ "$requirement" = "required" ]; then
      missing_required=1
      echo "required config path is missing: /$pattern" >&2
    fi
    return
  fi

  for match in "${matches[@]}"; do
    if [ "$SOURCE_ROOT" = "/" ]; then
      relative="${match#/}"
    else
      relative="${match#"$SOURCE_ROOT"/}"
    fi
    [[ "$relative" != *$'\n'* && "$relative" != *$'\t'* ]] || {
      echo "unsupported config path: $match" >&2
      exit 1
    }
    items+=("$relative")
    printf '%s\t%s\t%s\t%s\n' "included" "$requirement" "$classification" "/$relative" >> "$COVERAGE_TSV"
  done
}

add_external_secret() {
  printf '%s\t%s\t%s\t%s\n' "external-secret-required" "external" "secret" "$1" >> "$COVERAGE_TSV"
}

# 核心恢复入口缺失时失败关闭；其余主机能力按实际安装情况记录覆盖率。
add_spec required configuration etc/nginx
add_spec required configuration opt/ops
add_spec required configuration etc/ssh/sshd_config
add_spec required configuration etc/ufw/ufw.conf
add_spec required configuration etc/sysctl.d
add_spec required configuration etc/areasong-ops/services.json
add_spec required executable usr/local/libexec/areasong-ops
add_spec required configuration etc/systemd/system/areasong-ops-runner.service
add_spec optional secret-bearing etc/x-ui
add_spec optional secret-bearing etc/account-vault
add_spec optional secret-bearing opt/services
add_spec optional secret-bearing var/lib/sub2api/docker-compose.yml
add_spec optional secret-bearing var/lib/sub2api/compose.yml
add_spec optional secret-bearing 'var/lib/sub2api/*.env'
add_spec optional configuration opt/areaforge/docker-compose.prod.yml
add_spec optional secret-bearing opt/areaforge/.env.production
add_spec optional configuration var/lib/ops/account-vault-release
add_spec optional configuration 'etc/systemd/system/x-ui.service'
add_spec optional configuration 'etc/systemd/system/areaforge-*.service'
add_spec optional configuration 'etc/systemd/system/areaforge-*.timer'
add_spec optional configuration 'etc/cron.d/ops-*'
add_spec optional configuration etc/logrotate.d/ops-observability
add_spec optional configuration etc/docker/daemon.json
add_spec optional configuration 'etc/ssh/sshd_config.d/*.conf'
add_spec optional configuration etc/audit/auditd.conf
add_spec optional configuration 'etc/audit/rules.d/*.rules'
add_spec optional configuration etc/fail2ban/jail.local
add_spec optional configuration 'etc/fail2ban/jail.d/*.conf'
add_spec optional configuration etc/ufw/user.rules
add_spec optional configuration etc/ufw/user6.rules
add_spec optional configuration etc/default/ufw

# 这些凭据和私钥故意不进入普通 R2 配置包，恢复时必须从独立密钥托管取回。
add_external_secret '/etc/ops/*.env'
add_external_secret '/etc/observability/*secret*'
add_external_secret '/etc/areasong-ops/web.env'
add_external_secret '/var/lib/areasong-ops/credentials/alertmanager-github.env'
add_external_secret '/etc/letsencrypt/**/privkey*.pem'
add_external_secret '/root/.acme.sh/**/*.key'

[ "$missing_required" -eq 0 ] || exit 1
[ "${#items[@]}" -gt 0 ] || {
  echo "no config items found" >&2
  exit 1
}

/usr/bin/python3 - "$COVERAGE_TSV" "$COVERAGE_JSON" <<'PY'
import datetime as dt
import json
import sys

source, destination = sys.argv[1:]
entries = []
with open(source, encoding="utf-8") as handle:
    for line in handle:
        status, requirement, classification, path = line.rstrip("\n").split("\t", 3)
        entries.append(
            {
                "path": path,
                "status": status,
                "requirement": requirement,
                "classification": classification,
            }
        )
payload = {
    "schema_version": 1,
    "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
    "entries": entries,
}
with open(destination, "w", encoding="utf-8") as handle:
    json.dump(payload, handle, ensure_ascii=True, indent=2, sort_keys=True)
    handle.write("\n")
PY

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
    --exclude='*.bak-*' \
    -czf "$OUT" \
    -C "$SOURCE_ROOT" "${items[@]}" \
    -C "$WORK_DIR" backup-metadata/config-coverage.json

tar -tzf "$OUT" >/dev/null
chmod 0600 "$OUT"
find "$BACKUP_ROOT" -type f -name "configs-*.tar.gz" -mtime +7 -delete
printf "%s\n" "$OUT"
