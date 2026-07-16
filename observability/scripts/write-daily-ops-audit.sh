#!/usr/bin/env bash
set -euo pipefail

umask 027

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCK_FILE="${DAILY_OPS_AUDIT_LOCK_FILE:-/run/lock/daily-ops-audit.lock}"
TIMEOUT="${DAILY_OPS_AUDIT_TIMEOUT:-30m}"

[ "$(id -u)" -eq 0 ] || {
  echo "write-daily-ops-audit.sh must run as root" >&2
  exit 1
}

mkdir -p /var/log/observability /var/lib/node_exporter/textfile_collector

exec 9>"$LOCK_FILE"
flock -n 9 || {
  echo "another daily operations audit is already running" >&2
  exit 1
}

exec /usr/bin/timeout --signal=TERM --kill-after=2m "$TIMEOUT" \
  /usr/bin/nice -n 10 /usr/bin/python3 "$SCRIPT_DIR/daily_ops_audit.py" "$@"
