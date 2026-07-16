#!/usr/bin/env bash
set -euo pipefail

umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${COMPLIANCE_ARCHIVE_VERIFY_ENV:-/etc/ops/compliance-archive-verify.env}"
METRIC_OUT="${COMPLIANCE_ARCHIVE_METRIC_OUT:-/var/lib/node_exporter/textfile_collector/compliance-log-archive.prom}"
LOCK_FILE="${COMPLIANCE_ARCHIVE_VERIFY_LOCK_FILE:-/run/lock/ops-compliance-archive-verify.lock}"
REMOTE_NAME="compliance"
LATEST_SHA_ONLY=0
LATEST_HEAD_ONLY=0
STARTED_AT="$(date +%s)"
WORK_DIR="$(mktemp -d /var/tmp/ops-compliance-verify.XXXXXX)"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

usage() {
  cat <<'USAGE'
Usage: verify-compliance-log-archive.sh [--latest-sha-only | --latest-head]

Download and verify the newest immutable compliance archive plus the complete
manifest hash chain. --latest-sha-only prints the current chain head, or 64
zeroes when the archive is empty, without publishing success metrics.
--latest-head prints "<sha256> <day>", using "-" for an empty archive.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --latest-sha-only) LATEST_SHA_ONLY=1 ;;
    --latest-head) LATEST_HEAD_ONLY=1 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
  shift
done
[ "$((LATEST_SHA_ONLY + LATEST_HEAD_ONLY))" -le 1 ] || fail "choose only one latest-head output mode"

[ "$(id -u)" -eq 0 ] || fail "verification must run as root"
for command_name in flock python3 rclone sha256sum stat; do
  command -v "$command_name" >/dev/null 2>&1 || fail "missing command: $command_name"
done
[ -r "$CONFIG_FILE" ] || fail "verification config is missing or unreadable: $CONFIG_FILE"
[ "$(stat -c '%u' "$CONFIG_FILE")" -eq 0 ] || fail "verification config must be owned by root"
CONFIG_MODE="$(stat -c '%a' "$CONFIG_FILE")"
(( (8#$CONFIG_MODE & 077) == 0 )) || fail "verification config must not be accessible by group or other"

set -a
# shellcheck disable=SC1090
. "$CONFIG_FILE"
set +a
for required in R2_BUCKET R2_ENDPOINT R2_ACCESS_KEY_ID R2_SECRET_ACCESS_KEY; do
  [ -n "${!required:-}" ] || fail "missing compliance archive config: $required"
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
R2_PREFIX="${R2_PREFIX:-}"
case "$R2_PREFIX" in
  ""|*/) ;;
  *) R2_PREFIX="${R2_PREFIX}/" ;;
esac

export RCLONE_CONFIG_COMPLIANCE_TYPE="s3"
export RCLONE_CONFIG_COMPLIANCE_PROVIDER="Cloudflare"
export RCLONE_CONFIG_COMPLIANCE_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export RCLONE_CONFIG_COMPLIANCE_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
export RCLONE_CONFIG_COMPLIANCE_ENDPOINT="$R2_ENDPOINT"
export RCLONE_CONFIG_COMPLIANCE_REGION="auto"

REMOTE_ROOT="${REMOTE_NAME}:${R2_BUCKET}/${R2_PREFIX}"
MANIFEST_REMOTE_ROOT="${REMOTE_ROOT}manifests/LosAngeles"
CHAIN_ROOT="$WORK_DIR/chain"
LATEST_ROOT="$WORK_DIR/latest"
install -d -m 0700 "$CHAIN_ROOT" "$LATEST_ROOT"

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

CHAIN_VERIFIED=0
CHAIN_COUNT=""
verify_remote_chain() {
  [ "$CHAIN_VERIFIED" -eq 0 ] || return 0
  rm -rf "$CHAIN_ROOT"
  install -d -m 0700 "$CHAIN_ROOT"
  rclone --config /dev/null copy "$MANIFEST_REMOTE_ROOT" "$CHAIN_ROOT" \
    --include '**/manifest.json' \
    --include '**/manifest.json.sha256' \
    --s3-no-check-bucket \
    --s3-no-head \
    --log-level ERROR || fail "failed to download the remote compliance manifest chain"
  local chain_output
  chain_output="$(python3 "$SCRIPT_DIR/compliance_log_archive.py" verify-chain --manifest-root "$CHAIN_ROOT")" || \
    fail "remote compliance manifest chain verification failed"
  CHAIN_COUNT="${chain_output#verified manifests=}"
  [[ "$CHAIN_COUNT" =~ ^[0-9]+$ ]] || fail "invalid compliance archive chain verification output"
  CHAIN_VERIFIED=1
}

exec 9>"$LOCK_FILE"
flock -n 9 || fail "another compliance archive verification is already running"

MANIFEST_LIST="$WORK_DIR/manifests.txt"
SORTED_MANIFEST_LIST="$WORK_DIR/manifests.sorted.txt"
rclone --config /dev/null lsf "$MANIFEST_REMOTE_ROOT" \
  --recursive \
  --files-only \
  --include '**/manifest.json' \
  --s3-no-check-bucket \
  --log-level ERROR > "$MANIFEST_LIST" || fail "failed to list remote compliance manifests"
sort "$MANIFEST_LIST" > "$SORTED_MANIFEST_LIST" || fail "failed to sort remote compliance manifests"
mapfile -t MANIFESTS < "$SORTED_MANIFEST_LIST"
if [ "${#MANIFESTS[@]}" -eq 0 ]; then
  [ "$LATEST_SHA_ONLY" -eq 1 ] || [ "$LATEST_HEAD_ONLY" -eq 1 ] || \
    fail "no remote compliance archive manifests found"
  if [ "$LATEST_HEAD_ONLY" -eq 1 ]; then
    printf '%064d -\n' 0
  else
    printf '%064d\n' 0
  fi
  exit 0
fi

LATEST_RELATIVE="${MANIFESTS[-1]}"
[[ "$LATEST_RELATIVE" =~ ^[0-9]{4}/[0-9]{2}/[0-9]{2}/[0-9]{8}-[0-9]{12}Z-[0-9a-f]{8}/manifest\.json$ ]] || \
  fail "invalid remote compliance manifest path: $LATEST_RELATIVE"
LATEST_ARCHIVE_ID="$(basename "${LATEST_RELATIVE%/manifest.json}")"
LATEST_MANIFEST="$LATEST_ROOT/manifest.json"
LATEST_SIDECAR="$LATEST_ROOT/manifest.json.sha256"
copy_remote "manifests/LosAngeles/$LATEST_RELATIVE" "$LATEST_MANIFEST"
copy_remote "manifests/LosAngeles/${LATEST_RELATIVE}.sha256" "$LATEST_SIDECAR"
(
  cd "$LATEST_ROOT"
  sha256sum -c manifest.json.sha256
) >/dev/null

if [ "$LATEST_SHA_ONLY" -eq 1 ] || [ "$LATEST_HEAD_ONLY" -eq 1 ]; then
  verify_remote_chain
  LATEST_SHA="$(sha256sum "$LATEST_MANIFEST" | awk '{print $1}')"
  if [ "$LATEST_HEAD_ONLY" -eq 1 ]; then
    LATEST_DAY="$(python3 "$SCRIPT_DIR/compliance_log_archive.py" field --manifest "$LATEST_MANIFEST" --name day)"
    printf '%s %s\n' "$LATEST_SHA" "$LATEST_DAY"
  else
    printf '%s\n' "$LATEST_SHA"
  fi
  exit 0
fi

ARCHIVE_RELATIVE="${LATEST_RELATIVE%/manifest.json}"
mapfile -t PARTS < <(
  python3 "$SCRIPT_DIR/compliance_log_archive.py" list-parts --manifest "$LATEST_MANIFEST"
)
[ "${#PARTS[@]}" -gt 0 ] || fail "latest compliance manifest contains no payload parts"
for part in "${PARTS[@]}"; do
  [[ "$part" =~ ^payload\.tar\.gz\.part-[0-9]{5}$ ]] || fail "invalid payload part path: $part"
  copy_remote "payload/LosAngeles/$ARCHIVE_RELATIVE/$part" "$LATEST_ROOT/$part"
done
python3 "$SCRIPT_DIR/compliance_log_archive.py" verify \
  --archive-dir "$LATEST_ROOT" \
  --expected-archive-id "$LATEST_ARCHIVE_ID"

verify_remote_chain

DAY="$(python3 "$SCRIPT_DIR/compliance_log_archive.py" field --manifest "$LATEST_MANIFEST" --name day)"
DAY_TIMESTAMP="$(date -u -d "$DAY 00:00:00" +%s)"
DURATION_SECONDS="$(( $(date +%s) - STARTED_AT ))"
METRIC_TMP="${METRIC_OUT}.tmp"
install -d -m 0755 "$(dirname "$METRIC_OUT")"
{
  echo '# HELP compliance_log_archive_last_success_timestamp Unix timestamp of the latest complete remote compliance archive verification.'
  echo '# TYPE compliance_log_archive_last_success_timestamp gauge'
  printf 'compliance_log_archive_last_success_timestamp %s\n' "$(date +%s)"
  echo '# HELP compliance_log_archive_enabled Whether the immutable compliance archive verification job is enabled.'
  echo '# TYPE compliance_log_archive_enabled gauge'
  echo 'compliance_log_archive_enabled 1'
  echo '# HELP compliance_log_archive_append_only_gateway Whether the archive was verified through the append-only ingest gateway.'
  echo '# TYPE compliance_log_archive_append_only_gateway gauge'
  echo 'compliance_log_archive_append_only_gateway 1'
  echo '# HELP compliance_log_archive_day_timestamp UTC start timestamp of the newest verified archived day.'
  echo '# TYPE compliance_log_archive_day_timestamp gauge'
  printf 'compliance_log_archive_day_timestamp %s\n' "$DAY_TIMESTAMP"
  echo '# HELP compliance_log_archive_verify_duration_seconds Duration of the latest remote archive verification.'
  echo '# TYPE compliance_log_archive_verify_duration_seconds gauge'
  printf 'compliance_log_archive_verify_duration_seconds %s\n' "$DURATION_SECONDS"
  echo '# HELP compliance_log_archive_parts Number of payload parts in the latest verified archive.'
  echo '# TYPE compliance_log_archive_parts gauge'
  printf 'compliance_log_archive_parts %s\n' "${#PARTS[@]}"
  echo '# HELP compliance_log_archive_chain_manifests Number of immutable manifests verified in the hash chain.'
  echo '# TYPE compliance_log_archive_chain_manifests gauge'
  printf 'compliance_log_archive_chain_manifests %s\n' "$CHAIN_COUNT"
} > "$METRIC_TMP"
chmod 0644 "$METRIC_TMP"
mv "$METRIC_TMP" "$METRIC_OUT"

echo "Compliance archive verified: remote=$LATEST_RELATIVE parts=${#PARTS[@]} chain_manifests=$CHAIN_COUNT"
