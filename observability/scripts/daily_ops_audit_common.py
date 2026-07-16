#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import json
import math
import os
import re
import subprocess
import tempfile
import urllib.parse
import urllib.request
from collections import Counter
from dataclasses import dataclass, field
from pathlib import Path
from typing import Iterable
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

UTC = dt.timezone.utc
SLOW_THRESHOLD_SECONDS = 2.0
SERVICES = (
    "resume-jadeai",
    "account-vault",
    "sub2api",
    "areaforge",
    "ops-log-gateway",
    "grafana",
)
STATUS_CLASSES = ("1xx", "2xx", "3xx", "4xx", "5xx")
SERVICE_BY_HOST = {
    "resume.areasong.top": "resume-jadeai",
    "sorryiossearch.areasong.top": "account-vault",
    "cpa.areasong.top": "sub2api",
    "forge.areasong.top": "areaforge",
    "log.areasong.top": "ops-log-gateway",
    "monitor.areasong.top": "grafana",
}
SLOW_REQUEST_EXCLUDED_PATHS = {("sub2api", "/v1/responses")}
EXPECTED_BACKUPS = frozenset(
    {
        "postgres-sub2api",
        "postgres-account-vault",
        "postgres-areaforge",
        "redis",
        "configs",
        "volume-sub2api-data",
        "volume-jadeai-data",
        "volume-areaforge-uploads",
        "volume-areaforge-ops-state",
    }
)

SAFE_PATH_SEGMENTS = frozenset(
    {
        ".well-known",
        "account",
        "accounts",
        "admin",
        "api",
        "assets",
        "auth",
        "callback",
        "chat",
        "completions",
        "css",
        "dashboard",
        "docs",
        "download",
        "favicon.ico",
        "files",
        "fonts",
        "health",
        "images",
        "img",
        "index",
        "js",
        "login",
        "logout",
        "metrics",
        "models",
        "oauth",
        "openapi.json",
        "password",
        "profile",
        "profiles",
        "register",
        "reset",
        "responses",
        "resume",
        "resumes",
        "robots.txt",
        "search",
        "settings",
        "signin",
        "signup",
        "static",
        "status",
        "upload",
        "uploads",
        "user",
        "users",
        "verify",
    }
)
DYNAMIC_PARENT_SEGMENTS = frozenset(
    {
        "account",
        "accounts",
        "download",
        "files",
        "profile",
        "profiles",
        "resume",
        "resumes",
        "upload",
        "uploads",
        "user",
        "users",
        "verify",
    }
)
VERSION_RE = re.compile(r"v\d+")
ASSET_RE = re.compile(r"[^/]+\.(?P<extension>css|gif|ico|jpe?g|js|json|map|png|svg|webp|woff2?)", re.IGNORECASE)


@dataclass
class AuditWindow:
    report_day: dt.date
    start: dt.datetime
    end: dt.datetime

    @classmethod
    def for_day(cls, report_day: dt.date) -> "AuditWindow":
        start = dt.datetime.combine(report_day, dt.time.min, UTC)
        return cls(report_day=report_day, start=start, end=start + dt.timedelta(days=1))


@dataclass
class ServiceStats:
    statuses: Counter[str] = field(default_factory=Counter)
    bytes_sent: int = 0
    client_hashes: set[str] = field(default_factory=set)
    paths: Counter[str] = field(default_factory=Counter)
    latencies: list[float] = field(default_factory=list)
    slow_requests: int = 0


@dataclass
class PrometheusStats:
    node_exporter_up: float = 0.0
    network_receive_bytes: float = 0.0
    network_transmit_bytes: float = 0.0
    load1_peak: float = 0.0
    memory_used_percent_peak: float = 0.0
    disk_used_percent_peak: float = 0.0
    cpu_count: float = 0.0
    expected_containers_down: float = 0.0
    backup_missing: list[str] = field(default_factory=list)
    backup_stale: list[str] = field(default_factory=list)
    r2_missing: bool = False
    r2_stale: bool = False
    backup_set_missing: bool = False
    backup_set_stale: bool = False
    r2_verify_missing: bool = False
    r2_verify_stale: bool = False


@dataclass
class RuntimeStats:
    systemd_failed: int = 0
    docker_running: int = 0
    docker_unhealthy: int = 0
    ufw_active: int = 0
    active_alerts: list[str] = field(default_factory=list)


@dataclass
class AuditData:
    window: AuditWindow
    services: dict[str, ServiceStats]
    security: dict[str, int]
    prometheus: PrometheusStats
    runtime: RuntimeStats
    system_timezone: str
    nginx_parse_errors: int = 0
    nginx_unmapped: int = 0
    failures: list[str] = field(default_factory=list)


@dataclass(frozen=True)
class Finding:
    severity: str
    message: str


@dataclass
class DeliveryResult:
    attempted: int = 0
    accepted: int = 0
    error: str = ""


@dataclass(frozen=True)
class ReportArtifact:
    severity: str
    content: str
    path: Path


def run_command(command: list[str], timeout: int = 20) -> tuple[int, str]:
    try:
        result = subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except Exception as exc:
        return 1, str(exc)
    return result.returncode, result.stdout + result.stderr


def get_json(url: str, data: bytes | None = None) -> object:
    headers = {"Content-Type": "application/json"} if data is not None else {}
    request = urllib.request.Request(url, data=data, headers=headers)
    with urllib.request.urlopen(request, timeout=20) as response:
        body = response.read()
        if data is not None and not body.strip():
            return {}
        return json.loads(body.decode("utf-8"))


def discover_logs(pattern: str) -> list[Path]:
    path = Path(pattern)
    return sorted(path.parent.glob(path.name))


def iter_log_lines(paths: Iterable[Path], source: str, failures: list[str]) -> Iterable[str]:
    import gzip

    found = False
    for path in sorted(set(paths)):
        found = True
        try:
            opener = gzip.open if path.suffix == ".gz" else open
            with opener(path, "rt", encoding="utf-8", errors="replace") as handle:
                yield from handle
        except (PermissionError, OSError):
            failures.append(f"logs:{source}:{path.name}")
    if not found:
        failures.append(f"logs:{source}:missing")


def system_timezone() -> dt.tzinfo:
    candidates = [os.environ.get("TZ", "")]
    try:
        candidates.append(Path("/etc/timezone").read_text(encoding="utf-8").strip())
    except OSError:
        pass
    for candidate in candidates:
        if not candidate:
            continue
        try:
            return ZoneInfo(candidate)
        except ZoneInfoNotFoundError:
            continue
    return dt.datetime.now().astimezone().tzinfo or UTC


def parse_system_timestamp(line: str, reference: dt.datetime, local_tz: dt.tzinfo) -> float | None:
    token = line.split(" ", 1)[0]
    if "T" in token:
        try:
            parsed = dt.datetime.fromisoformat(token.replace("Z", "+00:00"))
            if parsed.tzinfo is None:
                parsed = parsed.replace(tzinfo=local_tz)
            return parsed.timestamp()
        except ValueError:
            pass

    dated = re.match(
        r"^(?P<date>\d{4}-\d{2}-\d{2})[ T](?P<time>\d{2}:\d{2}:\d{2})(?:[.,]\d+)?",
        line,
    )
    if dated:
        try:
            return dt.datetime.strptime(
                f"{dated.group('date')} {dated.group('time')}", "%Y-%m-%d %H:%M:%S"
            ).replace(tzinfo=local_tz).timestamp()
        except ValueError:
            return None

    match = re.match(r"^(?P<ts>[A-Z][a-z]{2}\s+\d{1,2}\s+\d\d:\d\d:\d\d)", line)
    if not match:
        return None
    reference_local = reference.astimezone(local_tz)
    try:
        parsed = dt.datetime.strptime(
            f"{reference_local.year} {match.group('ts')}", "%Y %b %d %H:%M:%S"
        ).replace(tzinfo=local_tz)
    except ValueError:
        return None
    if parsed - reference_local > dt.timedelta(days=2):
        parsed = parsed.replace(year=reference_local.year - 1)
    return parsed.timestamp()


def normalize_path(target: str) -> str:
    try:
        raw_path = urllib.parse.unquote(urllib.parse.urlsplit(target).path, errors="replace")
    except ValueError:
        raw_path = "/"
    normalized: list[str] = []
    previous = ""
    for raw_segment in raw_path.split("/"):
        if not raw_segment:
            normalized.append("")
            continue
        segment = raw_segment.lower()
        asset_match = ASSET_RE.fullmatch(segment)
        if previous in DYNAMIC_PARENT_SEGMENTS:
            safe_segment = ":value"
        elif segment in SAFE_PATH_SEGMENTS or VERSION_RE.fullmatch(segment):
            safe_segment = segment
        elif asset_match:
            safe_segment = f":asset.{asset_match.group('extension').lower()}"
        else:
            safe_segment = ":value"
        normalized.append(safe_segment)
        previous = safe_segment
    result = "/".join(normalized) or "/"
    return result[:160]


def percentile(values: list[float], quantile: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    rank = max(0, math.ceil(quantile * len(ordered)) - 1)
    return ordered[rank]


def human_bytes(value: float) -> str:
    units = ("B", "KiB", "MiB", "GiB", "TiB")
    for unit in units:
        if abs(value) < 1024 or unit == units[-1]:
            return f"{value:.2f} {unit}"
        value /= 1024
    return f"{value:.2f} TiB"


def metric_labels(labels: dict[str, str]) -> str:
    rendered = []
    for key, value in sorted(labels.items()):
        escaped = value.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")
        rendered.append(f'{key}="{escaped}"')
    return "{" + ",".join(rendered) + "}" if rendered else ""


def atomic_write(path: Path, content: str, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(content)
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
