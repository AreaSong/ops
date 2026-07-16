#!/usr/bin/env bash
set -euo pipefail

CONFIG_FILE="${R2_BACKUP_ENV:-/etc/ops/r2-backup.env}"
BACKUP_ROOT="${BACKUP_ROOT:-/var/backups/ops}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERIFY_SCRIPT="$SCRIPT_DIR/verify-backup-set-r2.sh"
LOG_DIR="/var/log/backup"
METRIC_OUT="/var/lib/node_exporter/textfile_collector/r2-backup.prom"
LOCK_FILE="/run/lock/ops-r2-backup.lock"
REMOTE_NAME="r2"
RCLONE_ARGS=()
DRY_RUN=0
SKIP_VERIFY=0

usage() {
  cat <<'USAGE'
Usage: sync-r2.sh [--dry-run] [--skip-verify]

Upload local backup artifacts from /var/backups/ops to Cloudflare R2.
Secrets are read from /etc/ops/r2-backup.env and are never stored in Git.
Successful non-dry-run uploads verify the latest manifest and every selected
artifact by downloading them from R2 and checking SHA-256.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      RCLONE_ARGS+=(--dry-run)
      ;;
    --skip-verify)
      SKIP_VERIFY=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
  shift
done

mkdir -p "$LOG_DIR"

if [ ! -r "$CONFIG_FILE" ]; then
  echo "R2 config file is missing or unreadable: $CONFIG_FILE" >&2
  exit 1
fi

if ! command -v rclone >/dev/null 2>&1; then
  echo "rclone is required but not installed" >&2
  exit 1
fi

if [ "$DRY_RUN" -eq 0 ] && [ "$SKIP_VERIFY" -eq 0 ] && [ ! -x "$VERIFY_SCRIPT" ]; then
  echo "R2 verification script is missing or not executable: $VERIFY_SCRIPT" >&2
  exit 1
fi

if [ ! -d "$BACKUP_ROOT" ]; then
  echo "backup root not found: $BACKUP_ROOT" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$CONFIG_FILE"
set +a

required_vars=(
  R2_BUCKET
  R2_ENDPOINT
  R2_PREFIX
  R2_ACCESS_KEY_ID
  R2_SECRET_ACCESS_KEY
)

for name in "${required_vars[@]}"; do
  if [ -z "${!name:-}" ]; then
    echo "missing required config: $name" >&2
    exit 1
  fi
done

if [[ ! "$R2_ENDPOINT" =~ ^https://[^/@?#]+(:[0-9]+)?/?$ ]]; then
  echo "R2_ENDPOINT must be an HTTPS origin without credentials, path, query, or fragment" >&2
  exit 1
fi
R2_ENDPOINT="${R2_ENDPOINT%/}"

case "$R2_PREFIX" in
  "") ;;
  */) ;;
  *) R2_PREFIX="${R2_PREFIX}/" ;;
esac

export RCLONE_CONFIG_R2_TYPE="s3"
export RCLONE_CONFIG_R2_PROVIDER="Cloudflare"
export RCLONE_CONFIG_R2_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
export RCLONE_CONFIG_R2_ENDPOINT="$R2_ENDPOINT"
export RCLONE_CONFIG_R2_REGION="auto"

remote_path="${REMOTE_NAME}:${R2_BUCKET}/${R2_PREFIX}"

write_success_metric() {
  local tmp
  tmp="${METRIC_OUT}.tmp"
  mkdir -p "$(dirname "$METRIC_OUT")"
  {
    echo '# HELP r2_backup_last_success_timestamp Unix timestamp of latest successful R2 backup sync.'
    echo '# TYPE r2_backup_last_success_timestamp gauge'
    printf 'r2_backup_last_success_timestamp{bucket="%s",prefix="%s"} %s\n' "$R2_BUCKET" "$R2_PREFIX" "$(date +%s)"
  } > "$tmp"
  mv "$tmp" "$METRIC_OUT"
  chmod 0644 "$METRIC_OUT"
}

(
  flock -n 9 || {
    echo "another R2 backup sync is already running" >&2
    exit 1
  }

  echo "syncing $BACKUP_ROOT to $remote_path"
  # Cloudflare R2 can return 501 for post-upload HEAD checks with this rclone build.
  rclone --config /dev/null copy "$BACKUP_ROOT" "$remote_path" \
    --s3-no-check-bucket \
    --s3-no-head \
    --fast-list \
    --transfers "${R2_TRANSFERS:-4}" \
    --checkers "${R2_CHECKERS:-8}" \
    --log-level INFO \
    --stats-one-line \
    "${RCLONE_ARGS[@]}"

  if [ "$DRY_RUN" -eq 0 ]; then
    if [ "$SKIP_VERIFY" -eq 0 ]; then
      R2_BACKUP_ENV="$CONFIG_FILE" \
        R2_VERIFY_ENV="${R2_VERIFY_ENV:-/etc/ops/r2-verify.env}" \
        "$VERIFY_SCRIPT"
    else
      echo "WARNING: R2 content verification explicitly skipped" >&2
    fi
    write_success_metric
  fi
  echo "R2 backup sync completed"
) 9>"$LOCK_FILE"
