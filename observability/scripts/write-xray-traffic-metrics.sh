#!/usr/bin/env bash
set -euo pipefail

umask 077

OUT="${XRAY_TRAFFIC_METRIC_OUT:-/var/lib/node_exporter/textfile_collector/xray-traffic.prom}"
XRAY_BIN="${XRAY_BIN:-/usr/local/x-ui/bin/xray-linux-amd64}"
XRAY_API_SERVER="${XRAY_API_SERVER:-127.0.0.1:62789}"

install -d -m 0755 "$(dirname "$OUT")"
TMP="$(mktemp "${OUT}.XXXXXX")"
RAW_TMP="$(mktemp)"

cleanup() {
  rm -f "$TMP" "$RAW_TMP"
}
trap cleanup EXIT

check_success=0
if "$XRAY_BIN" api statsquery --server="$XRAY_API_SERVER" >"$RAW_TMP" 2>/dev/null; then
  check_success=1
fi

XRAY_STATS_JSON="$RAW_TMP" XRAY_CHECK_SUCCESS="$check_success" python3 - <<'PY' > "$TMP"
from __future__ import annotations

import json
import os
import time

raw_path = os.environ["XRAY_STATS_JSON"]
check_success = int(os.environ.get("XRAY_CHECK_SUCCESS", "0"))

stats = []
if check_success:
    try:
        with open(raw_path, encoding="utf-8") as handle:
            payload = json.load(handle)
        stats = payload.get("stat") or []
    except (OSError, ValueError):
        check_success = 0

print("# HELP xray_traffic_metrics_last_run_timestamp Unix timestamp of the latest xray traffic metrics run.")
print("# TYPE xray_traffic_metrics_last_run_timestamp gauge")
print(f"xray_traffic_metrics_last_run_timestamp {int(time.time())}")
print("# HELP xray_traffic_metrics_check_success Whether the xray statsquery API call succeeded.")
print("# TYPE xray_traffic_metrics_check_success gauge")
print(f"xray_traffic_metrics_check_success {check_success}")

def escape(value: str) -> str:
    return value.replace("\\", "\\\\").replace('"', '\\"')


outbound_samples: list[str] = []
user_samples: list[str] = []
for entry in stats:
    name = str(entry.get("name") or "")
    parts = name.split(">>>")
    if len(parts) != 4 or parts[2] != "traffic" or parts[3] not in ("uplink", "downlink"):
        continue
    try:
        value = int(entry.get("value") or 0)
    except (TypeError, ValueError):
        continue
    direction = parts[3]
    if parts[0] == "outbound":
        outbound_samples.append(
            f'xray_outbound_traffic_bytes_total{{tag="{escape(parts[1])}",direction="{direction}"}} {value}'
        )
    elif parts[0] == "user":
        user_samples.append(
            f'xray_user_traffic_bytes_total{{email="{escape(parts[1])}",direction="{direction}"}} {value}'
        )

print("# HELP xray_outbound_traffic_bytes_total Xray per-outbound traffic bytes since the xray process started.")
print("# TYPE xray_outbound_traffic_bytes_total counter")
for sample in outbound_samples:
    print(sample)

print("# HELP xray_user_traffic_bytes_total Xray per-user traffic bytes since the xray process started.")
print("# TYPE xray_user_traffic_bytes_total counter")
for sample in user_samples:
    print(sample)
PY

chmod 0644 "$TMP"
mv "$TMP" "$OUT"
trap - EXIT
rm -f "$RAW_TMP"
