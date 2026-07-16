#!/usr/bin/env bash
set -euo pipefail

umask 077

OUT="${SUB2API_CAPACITY_METRIC_OUT:-/var/lib/node_exporter/textfile_collector/sub2api-capacity.prom}"
CONTAINER="${SUB2API_CONTAINER:-sub2api}"
WINDOW="5m"

install -d -m 0755 "$(dirname "$OUT")"
TMP="$(mktemp "${OUT}.XXXXXX")"
LOG_TMP="$(mktemp)"

cleanup() {
  rm -f "$TMP" "$LOG_TMP"
}
trap cleanup EXIT

check_success=0
event_count=0
if docker logs --since "$WINDOW" "$CONTAINER" >"$LOG_TMP" 2>&1; then
  check_success=1
  event_count="$(awk 'index(tolower($0), "no available account") { count++ } END { print count + 0 }' "$LOG_TMP")"
fi

{
  echo '# HELP sub2api_capacity_metrics_last_run_timestamp Unix timestamp of the latest sub2api capacity symptom collection run.'
  echo '# TYPE sub2api_capacity_metrics_last_run_timestamp gauge'
  printf 'sub2api_capacity_metrics_last_run_timestamp %s\n' "$(date +%s)"
  echo '# HELP sub2api_log_check_success Whether recent sub2api Docker logs were read successfully.'
  echo '# TYPE sub2api_log_check_success gauge'
  printf 'sub2api_log_check_success %s\n' "$check_success"
  echo '# HELP sub2api_no_available_account_events_last_5m No-available-account events observed in recent sub2api logs.'
  echo '# TYPE sub2api_no_available_account_events_last_5m gauge'
  printf 'sub2api_no_available_account_events_last_5m %s\n' "$event_count"
} >"$TMP"

chmod 0644 "$TMP"
mv "$TMP" "$OUT"
trap - EXIT
rm -f "$LOG_TMP"
