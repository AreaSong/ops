#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import ipaddress
import json
import os
import re
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any

DEFAULT_SOURCES = {
    "resume-jadeai": "resume-jadeai-app-1",
    "account-vault": "account-vault-web-1",
    "sub2api": "sub2api",
    "areaforge": "areaforge-web",
}
ERROR_RE = re.compile(r"\b(error|warn(?:ing)?|fatal|panic|exception|failed?|failure)\b", re.IGNORECASE)
EMAIL_RE = re.compile(r"(?<![\w.+-])[\w.+-]+@[\w.-]+\.[A-Za-z]{2,}(?![\w.-])")
IPV4_RE = re.compile(r"(?<!\d)(?:\d{1,3}\.){3}\d{1,3}(?!\d)")
SENSITIVE_VALUE_RE = re.compile(
    r"(?i)\b(password|passwd|secret|token|authorization|api[_-]?key|cookie|session|user[_-]?id|userid|uuid|phone|address)\b([\s\"'=:\\]+)([^\s,;}]+)"
)
BEARER_RE = re.compile(r"(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+")
QUERY_VALUE_RE = re.compile(r"([?&][A-Za-z0-9_.~-]+)=([^&\s]+)")
LONG_SECRET_RE = re.compile(r"(?<![A-Za-z0-9])[A-Fa-f0-9]{32,}(?![A-Za-z0-9])")
UUID_RE = re.compile(r"(?i)(?<![A-Fa-f0-9])[A-Fa-f0-9]{8}-[A-Fa-f0-9]{4}-[1-5][A-Fa-f0-9]{3}-[89ABab][A-Fa-f0-9]{3}-[A-Fa-f0-9]{12}(?![A-Fa-f0-9])")
JWT_RE = re.compile(r"(?<![A-Za-z0-9_-])[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}(?![A-Za-z0-9_-])")
OPAQUE_SECRET_RE = re.compile(r"(?<![A-Za-z0-9_+/=-])[A-Za-z0-9_+/=-]{32,}(?![A-Za-z0-9_+/=-])")
IPV6_CANDIDATE_RE = re.compile(r"(?<![A-Fa-f0-9:])(?:[A-Fa-f0-9]{0,4}:){2,7}[A-Fa-f0-9]{0,4}(?![A-Fa-f0-9:])")
CONTROL_RE = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Write sanitized warning/error logs for business containers.")
    parser.add_argument("--validate-only", action="store_true")
    return parser.parse_args()


def run_docker(arguments: list[str]) -> subprocess.CompletedProcess[str]:
    command = ["docker", *arguments]
    try:
        return subprocess.run(command, check=False, capture_output=True, text=True, timeout=45)
    except FileNotFoundError as exc:
        return subprocess.CompletedProcess(command, 127, "", str(exc))
    except subprocess.TimeoutExpired as exc:
        return subprocess.CompletedProcess(command, 124, exc.stdout or "", exc.stderr or str(exc))


def atomic_write(path: Path, content: str, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as handle:
        handle.write(content)
        temporary = Path(handle.name)
    temporary.chmod(mode)
    temporary.replace(path)


def redact(message: str) -> str:
    output = EMAIL_RE.sub("[email]", message)
    output = IPV4_RE.sub("[ip]", output)
    output = IPV6_CANDIDATE_RE.sub(redact_ipv6, output)
    output = BEARER_RE.sub("Bearer [redacted]", output)
    output = SENSITIVE_VALUE_RE.sub(lambda match: f"{match.group(1)}{match.group(2)}[redacted]", output)
    output = QUERY_VALUE_RE.sub(lambda match: f"{match.group(1)}=[redacted]", output)
    output = UUID_RE.sub("[id]", output)
    output = JWT_RE.sub("[token]", output)
    output = LONG_SECRET_RE.sub("[redacted]", output)
    output = OPAQUE_SECRET_RE.sub("[redacted]", output)
    return CONTROL_RE.sub("", output).strip()[:1000]


def redact_ipv6(match: re.Match[str]) -> str:
    candidate = match.group(0)
    try:
        ipaddress.IPv6Address(candidate)
    except ipaddress.AddressValueError:
        return candidate
    return "[ip]"


def severity(message: str) -> str:
    lowered = message.lower()
    if "fatal" in lowered or "panic" in lowered:
        return "fatal"
    if "exception" in lowered or "error" in lowered or "failed" in lowered or "failure" in lowered:
        return "error"
    return "warning"


def default_since() -> str:
    value = dt.datetime.now(dt.timezone.utc) - dt.timedelta(seconds=90)
    return value.isoformat(timespec="microseconds").replace("+00:00", "Z")


def process_lines(service: str, raw: str, last_timestamp: str) -> tuple[list[str], str, int]:
    records: list[str] = []
    newest = last_timestamp
    matched = 0
    for line in raw.splitlines():
        timestamp, separator, message = line.partition(" ")
        if not separator or timestamp <= last_timestamp:
            continue
        if timestamp > newest:
            newest = timestamp
        if not ERROR_RE.search(message):
            continue
        sanitized = redact(message)
        if not sanitized:
            continue
        records.append(
            json.dumps(
                {
                    "timestamp": timestamp,
                    "service": service,
                    "level": severity(message),
                    "message": sanitized,
                },
                ensure_ascii=True,
                separators=(",", ":"),
            )
        )
        matched += 1
    return records, newest, matched


def load_state(path: Path) -> dict[str, str]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
        if isinstance(payload, dict):
            return {str(key): str(value) for key, value in payload.items()}
    except (OSError, ValueError, TypeError, json.JSONDecodeError):
        pass
    return {}


def append_records(path: Path, records: list[str]) -> None:
    if not records:
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o640)
    with os.fdopen(descriptor, "a", encoding="utf-8") as handle:
        handle.write("\n".join(records) + "\n")
    path.chmod(0o640)


def render_metrics(success: bool, matched: int, failures: int, last_event: int, generated_at: int) -> str:
    return "\n".join(
        [
            "# HELP business_error_log_last_success_timestamp Unix timestamp of the latest collector run.",
            "# TYPE business_error_log_last_success_timestamp gauge",
            f"business_error_log_last_success_timestamp {generated_at}",
            "# HELP business_error_log_check_success Whether all configured Docker log sources were read.",
            "# TYPE business_error_log_check_success gauge",
            f"business_error_log_check_success {int(success)}",
            "# HELP business_error_log_events_last_run Sanitized warning/error events written in the latest run.",
            "# TYPE business_error_log_events_last_run gauge",
            f"business_error_log_events_last_run {matched}",
            "# HELP business_error_log_source_failures Docker log sources that failed in the latest run.",
            "# TYPE business_error_log_source_failures gauge",
            f"business_error_log_source_failures {failures}",
            "# HELP business_error_log_last_event_timestamp Unix timestamp of the latest matching event.",
            "# TYPE business_error_log_last_event_timestamp gauge",
            f"business_error_log_last_event_timestamp {last_event}",
            "",
        ]
    )


def timestamp_to_unix(value: str) -> int:
    if not value:
        return 0
    try:
        normalized = value.replace("Z", "+00:00")
        return int(dt.datetime.fromisoformat(normalized).timestamp())
    except ValueError:
        return 0


def main() -> int:
    args = parse_args()
    if args.validate_only:
        return 0

    log_path = Path(os.environ.get("BUSINESS_ERROR_LOG_OUT", "/var/log/business-errors/business-errors.log"))
    state_path = Path(
        os.environ.get("BUSINESS_ERROR_STATE_OUT", "/var/lib/observability/business-error-log-state.json")
    )
    metric_path = Path(
        os.environ.get("BUSINESS_ERROR_METRIC_OUT", "/var/lib/node_exporter/textfile_collector/business-error-log.prom")
    )
    state = load_state(state_path)
    next_state = dict(state)
    all_records: list[str] = []
    matched = 0
    failures = 0
    last_event_timestamp = 0

    for service, container in DEFAULT_SOURCES.items():
        since = state.get(container, default_since())
        result = run_docker(["logs", "--timestamps", "--since", since, container])
        if result.returncode != 0:
            failures += 1
            continue
        records, newest, count = process_lines(service, result.stdout + result.stderr, since)
        next_state[container] = newest
        all_records.extend(records)
        matched += count
        if records:
            last_event_timestamp = max(last_event_timestamp, timestamp_to_unix(newest))

    append_records(log_path, all_records)
    atomic_write(state_path, json.dumps(next_state, ensure_ascii=True, indent=2, sort_keys=True) + "\n", 0o600)
    generated_at = int(time.time())
    atomic_write(
        metric_path,
        render_metrics(failures == 0, matched, failures, last_event_timestamp, generated_at),
        0o644,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
