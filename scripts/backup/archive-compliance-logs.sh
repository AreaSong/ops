#!/usr/bin/env bash
set -euo pipefail

umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${COMPLIANCE_ARCHIVE_ENV:-/etc/ops/compliance-archive.env}"
VERIFY_SCRIPT="$SCRIPT_DIR/verify-compliance-log-archive.sh"
ARCHIVE_ROOT="${COMPLIANCE_ARCHIVE_ROOT:-/var/backups/ops/compliance-logs}"
SOURCE_ROOT="${COMPLIANCE_ARCHIVE_SOURCE_ROOT:-/}"
LOCK_FILE="${COMPLIANCE_ARCHIVE_LOCK_FILE:-/run/lock/ops-compliance-log-archive.lock}"
TARGET_DATE="${COMPLIANCE_ARCHIVE_DATE:-$(date -u -d yesterday +%F)}"
TIMEOUT="${COMPLIANCE_ARCHIVE_TIMEOUT:-30m}"
AUTH_HEADER=""

cleanup() {
  if [ -n "$AUTH_HEADER" ]; then
    rm -f "$AUTH_HEADER"
  fi
}
trap cleanup EXIT INT TERM

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

[ "$(id -u)" -eq 0 ] || fail "archive-compliance-logs.sh must run as root"
for command_name in curl flock python3 sha256sum stat timeout; do
  command -v "$command_name" >/dev/null 2>&1 || fail "missing command: $command_name"
done
[ -x "$VERIFY_SCRIPT" ] || fail "compliance archive verifier is missing or not executable"
[ -r "$CONFIG_FILE" ] || fail "archive ingest config is missing or unreadable: $CONFIG_FILE"
[ "$(stat -c '%u' "$CONFIG_FILE")" -eq 0 ] || fail "archive ingest config must be owned by root"
CONFIG_MODE="$(stat -c '%a' "$CONFIG_FILE")"
(( (8#$CONFIG_MODE & 077) == 0 )) || fail "archive ingest config must not be accessible by group or other"

set -a
# shellcheck disable=SC1090
. "$CONFIG_FILE"
set +a
for required in COMPLIANCE_INGEST_URL COMPLIANCE_INGEST_TOKEN; do
  [ -n "${!required:-}" ] || fail "missing archive ingest config: $required"
done
[[ "$COMPLIANCE_INGEST_URL" =~ ^https://[^/?#]+/?$ ]] || fail "COMPLIANCE_INGEST_URL must be an HTTPS origin without query or fragment"
COMPLIANCE_INGEST_URL="${COMPLIANCE_INGEST_URL%/}"
[[ "$TARGET_DATE" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || fail "invalid compliance archive date"
export COMPLIANCE_ARCHIVE_VERIFY_ENV="${COMPLIANCE_ARCHIVE_VERIFY_ENV:-/etc/ops/compliance-archive-verify.env}"

if [ "${COMPLIANCE_ARCHIVE_TIMEOUT_ACTIVE:-0}" -eq 0 ]; then
  export COMPLIANCE_ARCHIVE_TIMEOUT_ACTIVE=1
  exec timeout --signal=TERM --kill-after=2m "$TIMEOUT" "$0" "$@"
fi

exec 9>"$LOCK_FILE"
flock -n 9 || fail "another compliance archive job is already running"

run_archive() {
  local head_output previous_sha previous_day expected_day archive_dir relative_path archive_id
  head_output="$("$VERIFY_SCRIPT" --latest-head)"
  read -r previous_sha previous_day <<< "$head_output"
  [[ "$previous_sha" =~ ^[0-9a-f]{64}$ ]] || fail "invalid previous remote manifest SHA-256"
  if [ "$previous_day" != "-" ]; then
    [[ "$previous_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || fail "invalid previous remote archive day"
    expected_day="$(date -u -d "$previous_day + 1 day" +%F)"
    [ "$TARGET_DATE" = "$expected_day" ] || \
      fail "archive day must continue the remote chain: expected=$expected_day requested=$TARGET_DATE"
  fi

  archive_dir="$(
    python3 "$SCRIPT_DIR/compliance_log_archive.py" build \
      --date "$TARGET_DATE" \
      --source-root "$SOURCE_ROOT" \
      --output-root "$ARCHIVE_ROOT" \
      --host LosAngeles \
      --previous-manifest-sha256 "$previous_sha"
  )"
  python3 "$SCRIPT_DIR/compliance_log_archive.py" verify --archive-dir "$archive_dir"
  relative_path="${archive_dir#"${ARCHIVE_ROOT%/}"/}"
  [[ "$relative_path" =~ ^[0-9]{4}/[0-9]{2}/[0-9]{2}/([0-9]{8}-[0-9]{12}Z-[0-9a-f]{8})$ ]] || \
    fail "unexpected local compliance archive path: $relative_path"
  archive_id="${BASH_REMATCH[1]}"
  [[ "$archive_id" == "${TARGET_DATE//-/}"-* ]] || fail "archive id does not match requested day"

  AUTH_HEADER="$(mktemp /var/tmp/ops-compliance-auth.XXXXXX)"
  chmod 0600 "$AUTH_HEADER"
  printf 'Authorization: Bearer %s\n' "$COMPLIANCE_INGEST_TOKEN" > "$AUTH_HEADER"

  upload_object() {
    local kind="$1"
    local file="$2"
    local key sha content_type
    key="$kind/LosAngeles/$relative_path/$(basename "$file")"
    sha="$(sha256sum "$file" | awk '{print $1}')"
    content_type="application/octet-stream"
    [[ "$file" == *.json ]] && content_type="application/json"
    [[ "$file" == *.sha256 ]] && content_type="text/plain"
    curl --fail-with-body --silent --show-error \
      --retry 3 \
      --retry-all-errors \
      --request PUT \
      --header "@$AUTH_HEADER" \
      --header "Content-Type: $content_type" \
      --header "X-Content-SHA256: $sha" \
      --data-binary "@$file" \
      "$COMPLIANCE_INGEST_URL/v1/archive/$key" >/dev/null
  }

  while IFS= read -r part; do
    upload_object payload "$archive_dir/$part"
  done < <(python3 "$SCRIPT_DIR/compliance_log_archive.py" list-parts --manifest "$archive_dir/manifest.json")
  upload_object manifests "$archive_dir/manifest.json.sha256"
  upload_object manifests "$archive_dir/manifest.json"
  "$VERIFY_SCRIPT"
  rm -f "$AUTH_HEADER"
  AUTH_HEADER=""
  echo "Compliance log archive completed: day=$TARGET_DATE archive=$archive_id"
}

run_archive
