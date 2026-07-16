#!/usr/bin/env bash
set -euo pipefail

umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${R2_VERIFY_ENV:-/etc/ops/r2-verify.env}"
UPLOAD_CONFIG_FILE="${R2_BACKUP_ENV:-/etc/ops/r2-backup.env}"
BACKUP_ROOT="${BACKUP_ROOT:-/var/backups/ops}"
METRIC_OUT="${R2_VERIFY_METRIC_OUT:-/var/lib/node_exporter/textfile_collector/backup-set-r2-verify.prom}"
LOCK_FILE="${R2_VERIFY_LOCK_FILE:-/run/lock/ops-r2-backup-verify.lock}"
REMOTE_NAME="r2"
STARTED_AT="$(date +%s)"
WORK_DIR="$(mktemp -d /var/tmp/ops-r2-verify.XXXXXX)"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

[ "$(id -u)" -eq 0 ] || fail "verify-backup-set-r2.sh must run as root"
for command_name in awk flock python3 rclone sha256sum stat; do
  command -v "$command_name" >/dev/null 2>&1 || fail "missing command: $command_name"
done
[ -r "$CONFIG_FILE" ] || fail "R2 config is missing or unreadable: $CONFIG_FILE"
[ -r "$UPLOAD_CONFIG_FILE" ] || fail "R2 upload config is missing or unreadable: $UPLOAD_CONFIG_FILE"
[ ! "$CONFIG_FILE" -ef "$UPLOAD_CONFIG_FILE" ] || \
  fail "R2 verification credentials must not reuse the upload credential file"
[ "$(stat -c '%u' "$CONFIG_FILE")" -eq 0 ] || fail "R2 verification config must be owned by root"
CONFIG_MODE="$(stat -c '%a' "$CONFIG_FILE")"
(( (8#$CONFIG_MODE & 077) == 0 )) || fail "R2 verification config must not be accessible by group or other"

set -a
# shellcheck disable=SC1090
. "$CONFIG_FILE"
set +a

for required in R2_BUCKET R2_ENDPOINT R2_PREFIX R2_ACCESS_KEY_ID R2_SECRET_ACCESS_KEY; do
  [ -n "${!required:-}" ] || fail "missing R2 config: $required"
done
if ! python3 - "$R2_ENDPOINT" <<'PY'
import sys
from urllib.parse import urlsplit

try:
    endpoint = urlsplit(sys.argv[1])
    port = endpoint.port
except ValueError:
    raise SystemExit(1)
valid = (
    endpoint.scheme == "https"
    and endpoint.hostname is not None
    and endpoint.username is None
    and endpoint.password is None
    and endpoint.path in ("", "/")
    and not endpoint.query
    and not endpoint.fragment
    and (port is None or 1 <= port <= 65535)
)
raise SystemExit(0 if valid else 1)
PY
then
  fail "R2_ENDPOINT must be an HTTPS origin without credentials, path, query, or fragment"
fi
R2_ENDPOINT="${R2_ENDPOINT%/}"
VERIFY_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
UPLOAD_ACCESS_KEY_ID="$(
  unset R2_ACCESS_KEY_ID
  # shellcheck disable=SC1090
  . "$UPLOAD_CONFIG_FILE"
  printf '%s' "${R2_ACCESS_KEY_ID:-}"
)"
[ -n "$UPLOAD_ACCESS_KEY_ID" ] || fail "R2 upload config is missing R2_ACCESS_KEY_ID"
[ "$VERIFY_ACCESS_KEY_ID" != "$UPLOAD_ACCESS_KEY_ID" ] || \
  fail "R2 verification must use a distinct access key from the upload credential"
case "$R2_PREFIX" in
  ""|*/) ;;
  *) R2_PREFIX="${R2_PREFIX}/" ;;
esac

export RCLONE_CONFIG_R2_TYPE="s3"
export RCLONE_CONFIG_R2_PROVIDER="Cloudflare"
export RCLONE_CONFIG_R2_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
export RCLONE_CONFIG_R2_ENDPOINT="$R2_ENDPOINT"
export RCLONE_CONFIG_R2_REGION="auto"

REMOTE_ROOT="${REMOTE_NAME}:${R2_BUCKET}/${R2_PREFIX}"
RESTORE_ROOT="$WORK_DIR/root"
install -d -m 0700 "$RESTORE_ROOT/manifests"

copy_remote() {
  local relative_path="$1"
  local destination="$2"

  install -d -m 0700 "$(dirname "$destination")"
  rclone --config /dev/null copyto "${REMOTE_ROOT}${relative_path}" "$destination" \
    --s3-no-check-bucket \
    --s3-no-head \
    --log-level ERROR
  chmod 0600 "$destination"
}

exec 9>"$LOCK_FILE"
flock -n 9 || fail "another R2 verification is already running"

copy_remote "manifests/latest-manifest.txt" "$WORK_DIR/latest-manifest.txt"
MANIFEST_RELATIVE="$(tr -d '\r\n' < "$WORK_DIR/latest-manifest.txt")"
[[ "$MANIFEST_RELATIVE" =~ ^manifests/backup-set-[0-9]{8}-[0-9]{6}\.json$ ]] || \
  fail "invalid latest manifest pointer"
LOCAL_POINTER="$BACKUP_ROOT/manifests/latest-manifest.txt"
[ -r "$LOCAL_POINTER" ] || fail "local latest manifest pointer is missing"
LOCAL_MANIFEST_RELATIVE="$(tr -d '\r\n' < "$LOCAL_POINTER")"
[ "$MANIFEST_RELATIVE" = "$LOCAL_MANIFEST_RELATIVE" ] || \
  fail "R2 latest manifest pointer does not match the local complete set"

MANIFEST_PATH="$RESTORE_ROOT/$MANIFEST_RELATIVE"
SIDECAR_PATH="${MANIFEST_PATH}.sha256"
copy_remote "$MANIFEST_RELATIVE" "$MANIFEST_PATH"
copy_remote "${MANIFEST_RELATIVE}.sha256" "$SIDECAR_PATH"
(
  cd "$(dirname "$MANIFEST_PATH")"
  sha256sum -c "$(basename "$SIDECAR_PATH")"
) >/dev/null
LOCAL_MANIFEST="$BACKUP_ROOT/$MANIFEST_RELATIVE"
[ -r "$LOCAL_MANIFEST" ] || fail "local manifest is missing: $MANIFEST_RELATIVE"
[ "$(sha256sum "$MANIFEST_PATH" | awk '{print $1}')" = "$(sha256sum "$LOCAL_MANIFEST" | awk '{print $1}')" ] || \
  fail "R2 manifest does not match the local complete set"

mapfile -t ARTIFACTS < <(
  python3 "$SCRIPT_DIR/backup_manifest.py" list-artifacts --manifest "$MANIFEST_PATH"
)
[ "${#ARTIFACTS[@]}" -gt 0 ] || fail "manifest contains no artifacts"

for relative_path in "${ARTIFACTS[@]}"; do
  copy_remote "$relative_path" "$RESTORE_ROOT/$relative_path"
done

python3 "$SCRIPT_DIR/backup_manifest.py" verify \
  --backup-root "$RESTORE_ROOT" \
  --manifest "$MANIFEST_PATH"

DURATION_SECONDS="$(( $(date +%s) - STARTED_AT ))"
METRIC_TMP="${METRIC_OUT}.tmp"
install -d -m 0755 "$(dirname "$METRIC_OUT")"
{
  echo '# HELP backup_set_r2_verify_last_success_timestamp Unix timestamp of the latest complete R2 backup set verification.'
  echo '# TYPE backup_set_r2_verify_last_success_timestamp gauge'
  printf 'backup_set_r2_verify_last_success_timestamp %s\n' "$(date +%s)"
  echo '# HELP backup_set_r2_verify_duration_seconds Duration of the latest complete R2 backup set verification.'
  echo '# TYPE backup_set_r2_verify_duration_seconds gauge'
  printf 'backup_set_r2_verify_duration_seconds %s\n' "$DURATION_SECONDS"
  echo '# HELP backup_set_r2_verify_artifacts Artifacts verified from the latest R2 backup set.'
  echo '# TYPE backup_set_r2_verify_artifacts gauge'
  printf 'backup_set_r2_verify_artifacts %s\n' "${#ARTIFACTS[@]}"
} > "$METRIC_TMP"
mv "$METRIC_TMP" "$METRIC_OUT"
chmod 0644 "$METRIC_OUT"

echo "R2 backup set verification completed: manifest=$MANIFEST_RELATIVE artifacts=${#ARTIFACTS[@]} duration_seconds=$DURATION_SECONDS"
