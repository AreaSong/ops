#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import hashlib
import json
import math
import os
import re
import urllib.parse
from dataclasses import dataclass
from pathlib import Path

from daily_ops_audit_common import (
    EXPECTED_BACKUPS,
    SERVICE_BY_HOST,
    SERVICES,
    SLOW_REQUEST_EXCLUDED_PATHS,
    SLOW_THRESHOLD_SECONDS,
    STATUS_CLASSES,
    PrometheusStats,
    RuntimeStats,
    ServiceStats,
    discover_logs,
    get_json,
    iter_log_lines,
    normalize_path,
    parse_system_timestamp,
    run_command,
)

NGINX_RE = re.compile(
    r"^(?P<client>\S+) - \S+ \[(?P<ts>[^\]]+)\]\s+"
    r'"(?P<request>[^"]*)"\s+'
    r"(?P<status>\d{3})\s+(?P<bytes>\d+)\s+"
    r'"[^"]*"\s+"[^"]*"\s+'
    r'host="(?P<host>[^"]*)"\s+'
    r"request_time=(?P<request_time>[0-9.]+|-)\s+"
    r'upstream_response_time="(?P<upstream>[^"]*)"'
)

PROMETHEUS_EXPRESSIONS = {
    "node_exporter_up": 'max(up{job="node"})',
    "network_receive_bytes": (
        'sum(increase(node_network_receive_bytes_total{job="node",device!~"lo|veth.*|br-.*|docker.*"}[1d])) '
        "or vector(0)"
    ),
    "network_transmit_bytes": (
        'sum(increase(node_network_transmit_bytes_total{job="node",device!~"lo|veth.*|br-.*|docker.*"}[1d])) '
        "or vector(0)"
    ),
    "load1_peak": 'max(max_over_time(node_load1{job="node"}[1d])) or vector(0)',
    "memory_used_percent_peak": (
        'max(max_over_time((100 * (1 - node_memory_MemAvailable_bytes{job="node"} '
        '/ node_memory_MemTotal_bytes{job="node"}))[1d:5m])) or vector(0)'
    ),
    "disk_used_percent_peak": (
        'max(max_over_time((100 * (1 - node_filesystem_avail_bytes{job="node",fstype!~"tmpfs|overlay|squashfs"} '
        '/ node_filesystem_size_bytes{job="node",fstype!~"tmpfs|overlay|squashfs"}))[1d:5m])) or vector(0)'
    ),
    "cpu_count": (
        'count(count by (cpu) (node_cpu_seconds_total{job="node",mode="idle"})) or vector(0)'
    ),
    "expected_containers_down": "sum(docker_container_running == 0) or vector(0)",
    "docker_metric_series": "count(docker_container_running) or vector(0)",
}


@dataclass(frozen=True)
class SecurityLogPatterns:
    auth: str = "/var/log/auth.log*"
    fail2ban: str = "/var/log/fail2ban.log*"
    ufw: str = "/var/log/ufw.log*"
    syslog: str = "/var/log/syslog*"


def parse_nginx_timestamp(raw: str) -> float | None:
    try:
        return dt.datetime.strptime(raw, "%d/%b/%Y:%H:%M:%S %z").timestamp()
    except ValueError:
        return None


def _record_nginx_match(
    match: re.Match[str],
    stats: dict[str, ServiceStats],
    client_salt: bytes,
) -> bool:
    service = SERVICE_BY_HOST.get(match.group("host").lower())
    if service is None:
        return False
    status_class = f"{int(match.group('status')) // 100}xx"
    if status_class not in STATUS_CLASSES:
        return True
    item = stats[service]
    item.statuses[status_class] += 1
    item.bytes_sent += int(match.group("bytes"))
    digest = hashlib.blake2b(
        match.group("client").encode("utf-8"), key=client_salt, digest_size=12
    ).hexdigest()
    item.client_hashes.add(digest)
    _record_nginx_request(match, service, item, status_class)
    return True


def _record_nginx_request(
    match: re.Match[str], service: str, item: ServiceStats, status_class: str
) -> None:
    request_parts = match.group("request").split()
    target = request_parts[1] if len(request_parts) >= 2 else "/"
    path = normalize_path(target)
    item.paths[path] += 1
    if status_class == "5xx":
        item.error_paths[path] += 1
    raw_latency = match.group("request_time")
    if (
        raw_latency == "-"
        or match.group("status").startswith("1")
        or (service, path) in SLOW_REQUEST_EXCLUDED_PATHS
    ):
        return
    try:
        latency = float(raw_latency)
    except ValueError:
        return
    item.latencies.append(latency)
    if latency >= SLOW_THRESHOLD_SECONDS:
        item.slow_requests += 1


def collect_nginx(
    start: float,
    end: float,
    failures: list[str],
    pattern: str = "/var/log/nginx/ops-business-access.log*",
    client_salt: bytes | None = None,
) -> tuple[dict[str, ServiceStats], int, int]:
    stats = {service: ServiceStats() for service in SERVICES}
    parse_errors = 0
    unmapped = 0
    salt = client_salt or os.urandom(16)
    for line in iter_log_lines(discover_logs(pattern), "nginx-business", failures):
        match = NGINX_RE.search(line)
        if not match:
            if "host=" in line and "request_time=" in line:
                parse_errors += 1
            continue
        timestamp = parse_nginx_timestamp(match.group("ts"))
        if timestamp is None or not start <= timestamp < end:
            continue
        if not _record_nginx_match(match, stats, salt):
            unmapped += 1
    return stats, parse_errors, unmapped


def _count_auth_events(
    lines: list[str], start: float, end: float, reference: dt.datetime, local_tz: dt.tzinfo
) -> dict[str, int]:
    counts = {"ssh_failed": 0, "ssh_invalid_user": 0, "ssh_accepted": 0, "sudo_commands": 0}
    for line in lines:
        timestamp = parse_system_timestamp(line, reference, local_tz)
        if timestamp is None or not start <= timestamp < end:
            continue
        if "sshd" in line:
            counts["ssh_invalid_user"] += int("Invalid user" in line)
            if "Failed password" in line or "maximum authentication attempts exceeded" in line:
                counts["ssh_failed"] += 1
            elif "Accepted publickey" in line or "Accepted password" in line:
                counts["ssh_accepted"] += 1
        counts["sudo_commands"] += int("sudo" in line and "COMMAND=" in line)
    return counts


def _count_simple_events(
    lines: list[str], start: float, end: float, reference: dt.datetime, local_tz: dt.tzinfo
) -> tuple[int, int]:
    bans = 0
    unbans = 0
    for line in lines:
        timestamp = parse_system_timestamp(line, reference, local_tz)
        if timestamp is None or not start <= timestamp < end:
            continue
        bans += int(" Ban " in line)
        unbans += int(" Unban " in line)
    return bans, unbans


def collect_security(
    start: float,
    end: float,
    local_tz: dt.tzinfo,
    failures: list[str],
    patterns: SecurityLogPatterns | None = None,
) -> dict[str, int]:
    patterns = patterns or SecurityLogPatterns()
    reference = dt.datetime.fromtimestamp(end, dt.timezone.utc)
    auth_lines = list(iter_log_lines(discover_logs(patterns.auth), "auth", failures))
    counts = _count_auth_events(auth_lines, start, end, reference, local_tz)
    fail2ban_lines = list(iter_log_lines(discover_logs(patterns.fail2ban), "fail2ban", failures))
    counts["fail2ban_bans"], counts["fail2ban_unbans"] = _count_simple_events(
        fail2ban_lines, start, end, reference, local_tz
    )
    ufw_paths = discover_logs(patterns.ufw)
    ufw_source = "ufw" if ufw_paths else "syslog-ufw"
    ufw_paths = ufw_paths or discover_logs(patterns.syslog)
    counts["ufw_blocks"] = 0
    for line in iter_log_lines(ufw_paths, ufw_source, failures):
        timestamp = parse_system_timestamp(line, reference, local_tz)
        if timestamp is not None and start <= timestamp < end and "[UFW BLOCK]" in line:
            counts["ufw_blocks"] += 1
    return counts


def prom_query_vector(
    base_url: str, expression: str, evaluation_time: float
) -> list[tuple[dict[str, str], float]]:
    query = urllib.parse.urlencode({"query": expression, "time": str(evaluation_time)})
    payload = get_json(f"{base_url.rstrip('/')}/api/v1/query?{query}")
    if not isinstance(payload, dict) or payload.get("status") != "success":
        raise RuntimeError("Prometheus query failed")
    results = payload.get("data", {}).get("result", [])
    values: list[tuple[dict[str, str], float]] = []
    for item in results:
        value = float(item["value"][1])
        if not math.isfinite(value):
            raise RuntimeError("Prometheus query returned a non-finite value")
        values.append((dict(item.get("metric", {})), value))
    return values


def _prom_scalar(base_url: str, expression: str, evaluation_time: float) -> float:
    values = prom_query_vector(base_url, expression, evaluation_time)
    if not values:
        raise RuntimeError("Prometheus query returned no series")
    return sum(value for _, value in values)


def _collect_prometheus_scalars(
    base_url: str, end: float, failures: list[str]
) -> tuple[dict[str, float], float]:
    values: dict[str, float] = {}
    docker_metric_series = 0.0
    for name, expression in PROMETHEUS_EXPRESSIONS.items():
        try:
            value = _prom_scalar(base_url, expression, end)
        except Exception:
            failures.append(f"prometheus:{name}")
            value = 0.0
        if name == "docker_metric_series":
            docker_metric_series = value
        else:
            values[name] = value
    return values, docker_metric_series


def _collect_backup_state(
    base_url: str, end: float, failures: list[str]
) -> tuple[list[str], list[str]]:
    try:
        rows = prom_query_vector(base_url, "backup_last_success_timestamp", end)
    except Exception:
        failures.append("prometheus:backups")
        return sorted(EXPECTED_BACKUPS), []
    timestamps = {labels.get("backup", ""): value for labels, value in rows}
    missing = sorted(name for name in EXPECTED_BACKUPS if timestamps.get(name, 0) <= 0)
    stale = sorted(
        name for name in EXPECTED_BACKUPS if timestamps.get(name, 0) > 0 and end - timestamps[name] > 30 * 3600
    )
    return missing, stale


def _collect_timestamp_state(
    base_url: str,
    metric: str,
    end: float,
    max_age_seconds: int,
    failures: list[str],
) -> tuple[bool, bool]:
    try:
        rows = prom_query_vector(base_url, metric, end)
    except Exception:
        failures.append(f"prometheus:{metric}")
        return True, False
    timestamps = [value for _, value in rows if value > 0]
    if not timestamps:
        return True, False
    return False, end - max(timestamps) > max_age_seconds


def collect_prometheus(base_url: str, end: float, failures: list[str]) -> PrometheusStats:
    values, docker_metric_series = _collect_prometheus_scalars(base_url, end, failures)
    if docker_metric_series <= 0:
        failures.append("prometheus:docker-metrics-missing")
    backup_missing, backup_stale = _collect_backup_state(base_url, end, failures)
    r2_missing, r2_stale = _collect_timestamp_state(
        base_url, "r2_backup_last_success_timestamp", end, 36 * 3600, failures
    )
    backup_set_missing, backup_set_stale = _collect_timestamp_state(
        base_url, "backup_set_last_success_timestamp", end, 30 * 3600, failures
    )
    r2_verify_missing, r2_verify_stale = _collect_timestamp_state(
        base_url, "backup_set_r2_verify_last_success_timestamp", end, 36 * 3600, failures
    )
    return PrometheusStats(
        **values,
        backup_missing=backup_missing,
        backup_stale=backup_stale,
        r2_missing=r2_missing,
        r2_stale=r2_stale,
        backup_set_missing=backup_set_missing,
        backup_set_stale=backup_set_stale,
        r2_verify_missing=r2_verify_missing,
        r2_verify_stale=r2_verify_stale,
    )


def _collect_active_alerts(alertmanager_url: str, failures: list[str]) -> list[str]:
    try:
        alerts = get_json(f"{alertmanager_url.rstrip('/')}/api/v2/alerts")
    except Exception:
        failures.append("alertmanager")
        return []
    if not isinstance(alerts, list):
        failures.append("alertmanager:invalid-response")
        return []
    names = {
        str(alert.get("labels", {}).get("alertname", "unknown"))
        for alert in alerts
        if alert.get("status", {}).get("state") == "active"
        and alert.get("labels", {}).get("alertname") != "DailyOpsAuditReport"
    }
    return sorted(names)[:20]


def collect_runtime(alertmanager_url: str, failures: list[str]) -> RuntimeStats:
    runtime = RuntimeStats()
    code, output = run_command(["systemctl", "--failed", "--no-legend", "--plain"])
    if code == 0:
        runtime.systemd_failed = len([line for line in output.splitlines() if line.strip()])
    else:
        failures.append("systemctl")
    code, output = run_command(["docker", "ps", "--format", "{{json .}}"])
    if code == 0:
        for line in output.splitlines():
            try:
                item = json.loads(line)
            except json.JSONDecodeError:
                failures.append("docker:invalid-output")
                continue
            runtime.docker_running += 1
            runtime.docker_unhealthy += int("unhealthy" in str(item.get("Status", "")).lower())
    else:
        failures.append("docker")
    code, output = run_command(["ufw", "status"])
    if code == 0:
        runtime.ufw_active = int(bool(re.search(r"^Status:\s+active$", output, re.MULTILINE | re.IGNORECASE)))
    else:
        failures.append("ufw")
    runtime.active_alerts = _collect_active_alerts(alertmanager_url, failures)
    return runtime
