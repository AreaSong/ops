#!/usr/bin/env bash
set -euo pipefail

OUT="/var/lib/node_exporter/textfile_collector/security.prom"
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
import subprocess
import time
from pathlib import Path

NOW = time.time()
WINDOW_5M = 5 * 60
WINDOW_24H = 24 * 60 * 60
LOCAL_TZ = dt.datetime.now().astimezone().tzinfo

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

emit_metric("security_metrics_last_success_timestamp", "Unix timestamp of the latest successful security metrics run.", int(NOW))
emit_metric("ssh_failed_login_last_24h", "SSH failed login events observed in auth logs during the last 24 hours.", ssh_failed)
emit_metric("ssh_invalid_user_last_24h", "SSH invalid user events observed in auth logs during the last 24 hours.", ssh_invalid_user)
emit_metric("ssh_accepted_login_last_24h", "SSH accepted login events observed in auth logs during the last 24 hours.", ssh_accepted)
emit_metric("ufw_status_check_success", "Whether the UFW status command succeeded.", ufw_check_success)
emit_metric("ufw_enabled", "Whether UFW reports Status active.", ufw_enabled)
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
