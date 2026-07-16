#!/usr/bin/env bash
set -euo pipefail

OUT="${SECURITY_METRIC_OUT:-/var/lib/node_exporter/textfile_collector/security.prom}"
TMP="$(mktemp "${OUT}.XXXXXX")"

cleanup() {
  rm -f "$TMP"
}
trap cleanup EXIT

mkdir -p "$(dirname "$OUT")"

python3 - <<'PY' > "$TMP"
from __future__ import annotations

import datetime as dt
import json
import os
import re
import subprocess
import time
import urllib.parse
import urllib.request
from pathlib import Path

NOW = time.time()
WINDOW_5M = 5 * 60
WINDOW_24H = 24 * 60 * 60
LOCAL_TZ = dt.datetime.now().astimezone().tzinfo
LOKI_QUERY_URL = os.environ.get(
    "LOKI_QUERY_URL",
    "http://127.0.0.1:3100/loki/api/v1/query_range",
)
AUDIT_PIPELINE_PROBE = "ops-audit-pipeline-probe"

AUTH_LOGS = [Path("/var/log/auth.log"), Path("/var/log/auth.log.1")]
NGINX_LOGS = [Path("/var/log/nginx/access.log"), Path("/var/log/nginx/access.log.1")]

NGINX_RE = re.compile(r'\[(?P<ts>[^\]]+)\]\s+"[^"]*"\s+(?P<status>\d{3})\s')
SYSLOG_TS_RE = re.compile(r"^(?P<ts>[A-Z][a-z]{2}\s+\d{1,2}\s+\d\d:\d\d:\d\d)")


def run_command(command: list[str], timeout: int = 10) -> tuple[int, str]:
    try:
        completed = subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except Exception:
        return 1, ""
    return completed.returncode, completed.stdout + completed.stderr


def iter_log_lines(paths: list[Path]):
    for path in paths:
        try:
            with path.open("r", encoding="utf-8", errors="replace") as handle:
                yield from handle
        except FileNotFoundError:
            continue
        except PermissionError:
            continue


def parse_auth_timestamp(line: str) -> float | None:
    token = line.split(" ", 1)[0]
    if "T" in token:
        try:
            return dt.datetime.fromisoformat(token.replace("Z", "+00:00")).timestamp()
        except ValueError:
            pass

    match = SYSLOG_TS_RE.match(line)
    if not match:
        return None

    year = dt.datetime.now().year
    try:
        parsed = dt.datetime.strptime(f"{year} {match.group('ts')}", "%Y %b %d %H:%M:%S")
    except ValueError:
        return None

    parsed = parsed.replace(tzinfo=LOCAL_TZ)
    if parsed.timestamp() - NOW > 24 * 60 * 60:
        parsed = parsed.replace(year=year - 1)
    return parsed.timestamp()


def parse_nginx_timestamp(raw: str) -> float | None:
    try:
        return dt.datetime.strptime(raw, "%d/%b/%Y:%H:%M:%S %z").timestamp()
    except ValueError:
        return None


def ssh_counts() -> tuple[int, int, int]:
    failed = 0
    invalid_user = 0
    accepted = 0
    for line in iter_log_lines(AUTH_LOGS):
        if "sshd" not in line:
            continue
        ts = parse_auth_timestamp(line)
        if ts is None or NOW - ts > WINDOW_24H or ts - NOW > 60:
            continue
        if "Invalid user" in line:
            invalid_user += 1
        if "Failed password" in line or "maximum authentication attempts exceeded" in line:
            failed += 1
        elif "Accepted publickey" in line or "Accepted password" in line:
            accepted += 1
    return failed, invalid_user, accepted


def nginx_counts() -> dict[str, int]:
    counts = {f"{prefix}xx": 0 for prefix in range(1, 6)}
    for line in iter_log_lines(NGINX_LOGS):
        match = NGINX_RE.search(line)
        if not match:
            continue
        ts = parse_nginx_timestamp(match.group("ts"))
        if ts is None or NOW - ts > WINDOW_5M or ts - NOW > 60:
            continue
        status = int(match.group("status"))
        status_class = f"{status // 100}xx"
        if status_class in counts:
            counts[status_class] += 1
    return counts


def fail2ban_status() -> dict[str, int]:
    values = {
        "success": 0,
        "currently_failed": 0,
        "currently_banned": 0,
        "total_failed": 0,
        "total_banned": 0,
    }
    returncode, output = run_command(["fail2ban-client", "status", "sshd"])
    if returncode != 0:
        return values

    values["success"] = 1
    patterns = {
        "currently_failed": r"Currently failed:\s+(\d+)",
        "currently_banned": r"Currently banned:\s+(\d+)",
        "total_failed": r"Total failed:\s+(\d+)",
        "total_banned": r"Total banned:\s+(\d+)",
    }
    for key, pattern in patterns.items():
        match = re.search(pattern, output)
        if match:
            values[key] = int(match.group(1))
    return values


def ufw_status() -> tuple[int, int]:
    returncode, output = run_command(["ufw", "status"])
    if returncode != 0:
        return 0, 0
    enabled = 1 if re.search(r"^Status:\s+active\s*$", output, re.MULTILINE | re.IGNORECASE) else 0
    return 1, enabled


def auditd_status() -> dict[str, int]:
    required_keys = {
        "identity",
        "sudoers",
        "sshd",
        "systemd",
        "auditconfig",
        "opsconfig",
        "rootcmd",
    }
    values = {
        "success": 0,
        "active": 0,
        "enabled": 0,
        "rules_loaded": 0,
        "required_keys_present": 0,
        "required_keys_expected": len(required_keys),
        "lost": 0,
        "backlog": 0,
        "backlog_limit": 0,
    }

    service_code, service_output = run_command(["systemctl", "is-active", "auditd"])
    values["active"] = int(service_code == 0 and service_output.strip() == "active")
    status_code, status_output = run_command(["auditctl", "-s"])
    rules_code, rules_output = run_command(["auditctl", "-l"])
    if status_code == 0:
        for key in ("enabled", "lost", "backlog", "backlog_limit"):
            match = re.search(rf"^{key}\s+(\d+)\s*$", status_output, re.MULTILINE)
            if match:
                values[key] = int(match.group(1))
    if rules_code == 0:
        rules = [line for line in rules_output.splitlines() if line.strip() and "No rules" not in line]
        values["rules_loaded"] = len(rules)
        loaded_keys = set(re.findall(r"(?:-k\s+|key=)([^\s]+)", rules_output))
        values["required_keys_present"] = len(required_keys & loaded_keys)
    values["success"] = int(service_code == 0 and status_code == 0 and rules_code == 0)
    return values


def audit_log_pipeline_status() -> dict[str, int]:
    values = {
        "probe_emit_success": 0,
        "query_success": 0,
        "pipeline_success": 0,
        "last_event_timestamp": 0,
    }
    probe_code, _ = run_command(["auditctl", "-m", AUDIT_PIPELINE_PROBE])
    values["probe_emit_success"] = int(probe_code == 0)

    query = '{job="auditd",host="LosAngeles"} |= "' + AUDIT_PIPELINE_PROBE + '"'
    for attempt in range(3):
        end_ns = time.time_ns()
        params = urllib.parse.urlencode(
            {
                "query": query,
                "start": end_ns - (10 * 60 * 1_000_000_000),
                "end": end_ns,
                "limit": 5,
                "direction": "backward",
            }
        )
        try:
            with urllib.request.urlopen(f"{LOKI_QUERY_URL}?{params}", timeout=5) as response:
                payload = json.load(response)
        except Exception:
            payload = None

        data = payload.get("data") if isinstance(payload, dict) else None
        result = data.get("result") if isinstance(data, dict) else None
        valid_response = (
            isinstance(payload, dict)
            and payload.get("status") == "success"
            and isinstance(data, dict)
            and data.get("resultType") == "streams"
            and isinstance(result, list)
        )
        if valid_response:
            values["query_success"] = 1
            timestamps = []
            for stream in result:
                if not isinstance(stream, dict):
                    continue
                labels = stream.get("stream", {})
                if labels.get("job") != "auditd" or labels.get("host") != "LosAngeles":
                    continue
                for entry in stream.get("values", []):
                    if not isinstance(entry, list) or len(entry) < 2 or not str(entry[0]).isdigit():
                        continue
                    line = str(entry[1])
                    if AUDIT_PIPELINE_PROBE not in line:
                        continue
                    if "type=EXECVE" in line or "type=PROCTITLE" in line:
                        continue
                    timestamps.append(int(entry[0]) // 1_000_000_000)
            if timestamps:
                values["last_event_timestamp"] = max(timestamps)
                break
        if attempt < 2:
            time.sleep(1)

    event_age = time.time() - values["last_event_timestamp"]
    values["pipeline_success"] = int(
        values["probe_emit_success"] == 1
        and values["query_success"] == 1
        and values["last_event_timestamp"] > 0
        and 0 <= event_age <= WINDOW_5M
    )
    return values


def emit_metric(name: str, help_text: str, value: int | float, labels: dict[str, str] | None = None) -> None:
    label_text = ""
    if labels:
        rendered = ",".join(f'{key}="{value}"' for key, value in sorted(labels.items()))
        label_text = f"{{{rendered}}}"
    print(f"# HELP {name} {help_text}")
    print(f"# TYPE {name} gauge")
    print(f"{name}{label_text} {value}")


ssh_failed, ssh_invalid_user, ssh_accepted = ssh_counts()
nginx = nginx_counts()
fail2ban = fail2ban_status()
ufw_check_success, ufw_enabled = ufw_status()
auditd = auditd_status()
audit_log_pipeline = audit_log_pipeline_status()

emit_metric("security_metrics_last_success_timestamp", "Unix timestamp of the latest successful security metrics run.", int(NOW))
emit_metric("ssh_failed_login_last_24h", "SSH failed login events observed in auth logs during the last 24 hours.", ssh_failed)
emit_metric("ssh_invalid_user_last_24h", "SSH invalid user events observed in auth logs during the last 24 hours.", ssh_invalid_user)
emit_metric("ssh_accepted_login_last_24h", "SSH accepted login events observed in auth logs during the last 24 hours.", ssh_accepted)
emit_metric("ufw_status_check_success", "Whether the UFW status command succeeded.", ufw_check_success)
emit_metric("ufw_enabled", "Whether UFW reports Status active.", ufw_enabled)
emit_metric("auditd_check_success", "Whether auditd service and auditctl checks succeeded.", auditd["success"])
emit_metric("auditd_service_active", "Whether systemd reports auditd active.", auditd["active"])
emit_metric("auditd_kernel_enabled", "Kernel audit enabled state reported by auditctl.", auditd["enabled"])
emit_metric("auditd_rules_loaded", "Audit rules currently loaded in the kernel.", auditd["rules_loaded"])
emit_metric(
    "auditd_required_rule_keys_present",
    "Required managed audit rule keys currently present.",
    auditd["required_keys_present"],
)
emit_metric(
    "auditd_required_rule_keys_expected",
    "Required managed audit rule keys expected.",
    auditd["required_keys_expected"],
)
emit_metric("auditd_lost_events", "Audit events lost since kernel audit initialization.", auditd["lost"])
emit_metric("auditd_backlog", "Current kernel audit backlog.", auditd["backlog"])
emit_metric("auditd_backlog_limit", "Configured kernel audit backlog limit.", auditd["backlog_limit"])
emit_metric(
    "audit_log_probe_emit_success",
    "Whether the fixed non-sensitive audit pipeline probe was emitted.",
    audit_log_pipeline["probe_emit_success"],
)
emit_metric(
    "audit_log_loki_query_success",
    "Whether the Loki audit probe query completed successfully.",
    audit_log_pipeline["query_success"],
)
emit_metric(
    "audit_log_pipeline_check_success",
    "Whether a fresh audit probe reached Loki end to end.",
    audit_log_pipeline["pipeline_success"],
)
emit_metric(
    "audit_log_pipeline_last_event_timestamp_seconds",
    "Unix timestamp of the newest audit pipeline probe observed in Loki.",
    audit_log_pipeline["last_event_timestamp"],
)
emit_metric("fail2ban_check_success", "Whether fail2ban-client status sshd succeeded.", fail2ban["success"], {"jail": "sshd"})
emit_metric("fail2ban_currently_failed", "Current failed events reported by Fail2ban.", fail2ban["currently_failed"], {"jail": "sshd"})
emit_metric("fail2ban_currently_banned", "Current banned addresses reported by Fail2ban.", fail2ban["currently_banned"], {"jail": "sshd"})
emit_metric("fail2ban_total_failed", "Total failed events reported by Fail2ban since service start.", fail2ban["total_failed"], {"jail": "sshd"})
emit_metric("fail2ban_total_banned", "Total banned events reported by Fail2ban since service start.", fail2ban["total_banned"], {"jail": "sshd"})

print("# HELP nginx_http_requests_last_5m Nginx access log requests observed during the last 5 minutes by status class.")
print("# TYPE nginx_http_requests_last_5m gauge")
for status_class in ["1xx", "2xx", "3xx", "4xx", "5xx"]:
    print(f'nginx_http_requests_last_5m{{status_class="{status_class}"}} {nginx[status_class]}')
PY

chmod 0644 "$TMP"
mv "$TMP" "$OUT"
trap - EXIT
