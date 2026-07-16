#!/usr/bin/env bash
set -euo pipefail

umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCK_FILE="${BACKUP_MANIFEST_LOCK_FILE:-/run/lock/ops-backup-manifest.lock}"
TIMEOUT="${BACKUP_MANIFEST_TIMEOUT:-30m}"

[ "$(id -u)" -eq 0 ] || {
  echo "create-backup-manifest.sh must run as root" >&2
  exit 1
}

exec 9>"$LOCK_FILE"
flock -n 9 || {
  echo "another backup manifest job is already running" >&2
  exit 1
}

exec /usr/bin/timeout --signal=TERM --kill-after=2m "$TIMEOUT" \
  /usr/bin/nice -n 10 /usr/bin/python3 "$SCRIPT_DIR/backup_manifest.py" create "$@"
