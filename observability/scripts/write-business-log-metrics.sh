#!/usr/bin/env bash
set -euo pipefail

OUT="/var/lib/node_exporter/textfile_collector/business-log.prom"
TMP="$(mktemp "${OUT}.XXXXXX")"

cleanup() {
  rm -f "$TMP"
}
trap cleanup EXIT

mkdir -p "$(dirname "$OUT")"

python3 - <<'PY' > "$TMP"
from __future__ import annotations

import datetime as dt
import re
import time
from collections import defaultdict
from pathlib import Path

NOW = time.time()
WINDOW_5M = 5 * 60
SLOW_THRESHOLD = 2.0

LOGS = [
    Path("/var/log/nginx/ops-business-access.log"),
    Path("/var/log/nginx/ops-business-access.log.1"),
]

SERVICE_BY_HOST = {
    "resume.areasong.top": "resume-jadeai",
    "sorryiossearch.areasong.top": "account-vault",
    "cpa.areasong.top": "sub2api",
}

SERVICES = ["resume-jadeai", "account-vault", "sub2api"]
STATUS_CLASSES = ["1xx", "2xx", "3xx", "4xx", "5xx"]

LINE_RE = re.compile(
    r'\[(?P<ts>[^\]]+)\]\s+'
    r'"(?P<method>[A-Z]+)\s+(?P<path>[^ ]+)\s+HTTP/[^"]+"\s+'
    r'(?P<status>\d{3})\s+\d+\s+'
    r'"[^"]*"\s+"[^"]*"\s+'
    r'host="(?P<host>[^"]*)"\s+'
    r'request_time=(?P<request_time>[0-9.]+|-)\s+'
    r'upstream_response_time="(?P<upstream_response_time>[^"]*)"'
)


def iter_log_lines(paths: list[Path]):
    for path in paths:
        try:
            with path.open("r", encoding="utf-8", errors="replace") as handle:
                yield from handle
        except FileNotFoundError:
            continue
        except PermissionError:
            continue


def parse_nginx_timestamp(raw: str) -> float | None:
    try:
        return dt.datetime.strptime(raw, "%d/%b/%Y:%H:%M:%S %z").timestamp()
    except ValueError:
        return None


def parse_float(raw: str) -> float | None:
    if raw == "-":
        return None
    try:
        return float(raw)
    except ValueError:
        return None


def emit_metric(name: str, help_text: str, value: int | float, labels: dict[str, str] | None = None) -> None:
    label_text = ""
    if labels:
        rendered = ",".join(f'{key}="{label_value}"' for key, label_value in sorted(labels.items()))
        label_text = f"{{{rendered}}}"
    print(f"# HELP {name} {help_text}")
    print(f"# TYPE {name} gauge")
    print(f"{name}{label_text} {value}")


requests = defaultdict(int)
slow_requests = defaultdict(int)
request_time_max = defaultdict(float)
unmapped_hosts = defaultdict(int)
parse_errors = 0
observed_lines = 0

for line in iter_log_lines(LOGS):
    match = LINE_RE.search(line)
    if not match:
        if "host=" in line or "request_time=" in line:
            parse_errors += 1
        continue

    ts = parse_nginx_timestamp(match.group("ts"))
    if ts is None or NOW - ts > WINDOW_5M or ts - NOW > 60:
        continue

    observed_lines += 1
    host = match.group("host").lower()
    service = SERVICE_BY_HOST.get(host)
    if service is None:
        unmapped_hosts[host or "empty"] += 1
        continue

    status = int(match.group("status"))
    status_class = f"{status // 100}xx"
    if status_class not in STATUS_CLASSES:
        continue

    requests[(service, status_class)] += 1
    request_time = parse_float(match.group("request_time"))
    if request_time is not None:
        request_time_max[service] = max(request_time_max[service], request_time)
        if request_time >= SLOW_THRESHOLD:
            slow_requests[service] += 1

emit_metric("business_log_metrics_last_success_timestamp", "Unix timestamp of the latest successful business log metrics run.", int(NOW))
emit_metric("business_log_observed_lines_last_5m", "Enhanced Nginx business access log lines observed during the last 5 minutes.", observed_lines)
emit_metric("business_log_parse_errors_last_5m", "Enhanced Nginx business access log parse errors observed during the last 5 minutes.", parse_errors)

print("# HELP business_http_requests_last_5m Business HTTP requests observed during the last 5 minutes by service and status class.")
print("# TYPE business_http_requests_last_5m gauge")
for service in SERVICES:
    for status_class in STATUS_CLASSES:
        print(f'business_http_requests_last_5m{{service="{service}",status_class="{status_class}"}} {requests[(service, status_class)]}')

print("# HELP business_http_4xx_last_5m Business HTTP 4xx responses observed during the last 5 minutes by service.")
print("# TYPE business_http_4xx_last_5m gauge")
for service in SERVICES:
    print(f'business_http_4xx_last_5m{{service="{service}"}} {requests[(service, "4xx")]}')

print("# HELP business_http_5xx_last_5m Business HTTP 5xx responses observed during the last 5 minutes by service.")
print("# TYPE business_http_5xx_last_5m gauge")
for service in SERVICES:
    print(f'business_http_5xx_last_5m{{service="{service}"}} {requests[(service, "5xx")]}')

print("# HELP business_http_slow_requests_last_5m Business HTTP requests whose Nginx request_time is at or above the threshold during the last 5 minutes.")
print("# TYPE business_http_slow_requests_last_5m gauge")
for service in SERVICES:
    print(f'business_http_slow_requests_last_5m{{service="{service}",threshold="2s"}} {slow_requests[service]}')

print("# HELP business_http_request_time_max_seconds_last_5m Maximum Nginx request_time observed during the last 5 minutes by service.")
print("# TYPE business_http_request_time_max_seconds_last_5m gauge")
for service in SERVICES:
    print(f'business_http_request_time_max_seconds_last_5m{{service="{service}"}} {request_time_max[service]:.6f}')

print("# HELP business_http_unmapped_host_requests_last_5m Enhanced Nginx access log requests for hosts not mapped to a business service.")
print("# TYPE business_http_unmapped_host_requests_last_5m gauge")
for host in sorted(unmapped_hosts):
    safe_host = host.replace("\\", "\\\\").replace('"', '\\"')
    print(f'business_http_unmapped_host_requests_last_5m{{host="{safe_host}"}} {unmapped_hosts[host]}')
PY

chmod 0644 "$TMP"
mv "$TMP" "$OUT"
trap - EXIT
