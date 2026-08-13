#!/usr/bin/env python3
"""Synchronize active Alertmanager critical alerts with managed GitHub Issues."""

from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import os
import re
import stat
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable


MARKER_PREFIX = "<!-- areasong-alertmanager-critical:v1:"
MARKER_SUFFIX = " -->"
MANAGED_LABEL = "alertmanager-critical"
ALERTMANAGER_API_WATCHDOG_FINGERPRINT = "areasong-alertmanager-api-watchdog-v1"
SAFE_LABELS = ("alertname", "severity", "scope", "service", "instance", "job")
REDACTIONS = (
    (re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}"), "[email]"),
    (re.compile(r"\b(?:\d{1,3}\.){3}\d{1,3}\b"), "[ip]"),
    (re.compile(r"(?i)(bearer\s+)[^\s]+"), r"\1[redacted]"),
)


class SyncError(RuntimeError):
    """Raised when the sync cannot complete safely."""


@dataclass(frozen=True)
class Config:
    enabled: bool
    token: str
    repository: str
    alertmanager_url: str
    github_api_base: str
    metric_out: Path
    token_expires_at: datetime.date | None = None
    timeout: float = 15.0


def redact(value: str, limit: int = 600) -> str:
    result = value
    for pattern, replacement in REDACTIONS:
        result = pattern.sub(replacement, result)
    return result[:limit]


def alert_fingerprint(alert: dict[str, Any]) -> str:
    fingerprint = str(alert.get("fingerprint", "")).strip()
    if fingerprint:
        return fingerprint
    labels = alert.get("labels") or {}
    encoded = json.dumps(labels, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest()[:40]


def marker_for(alert: dict[str, Any]) -> str:
    return f"{MARKER_PREFIX}{alert_fingerprint(alert)}{MARKER_SUFFIX}"


def simulation_alert() -> dict[str, Any]:
    return {
        "fingerprint": "areasong-monthly-alertmanager-github-simulation-v1",
        "labels": {
            "alertname": "AlertmanagerGitHubIssueSimulation",
            "severity": "critical",
            "scope": "simulation",
            "service": "ops-alerting",
            "instance": "LosAngeles",
        },
        "annotations": {"summary": "Controlled monthly GitHub Issue failure and recovery simulation"},
        "status": {"state": "active"},
    }


def alertmanager_api_watchdog() -> dict[str, Any]:
    return {
        "fingerprint": ALERTMANAGER_API_WATCHDOG_FINGERPRINT,
        "labels": {
            "alertname": "AlertmanagerApiUnavailableWatchdog",
            "severity": "critical",
            "scope": "alerting",
            "service": "alertmanager",
            "instance": "LosAngeles",
        },
        "annotations": {
            "summary": "Alertmanager API is unavailable; managed alert reconciliation is degraded",
        },
        "status": {"state": "active"},
    }


def critical_alerts(alerts: Iterable[dict[str, Any]]) -> list[dict[str, Any]]:
    result = []
    for alert in alerts:
        labels = alert.get("labels") or {}
        status = alert.get("status") or {}
        if labels.get("severity") == "critical" and status.get("state", "active") in {"active", "firing"}:
            result.append(alert)
    return result


def render_title(alert: dict[str, Any]) -> str:
    labels = alert.get("labels") or {}
    subject = labels.get("service") or labels.get("instance") or "LosAngeles"
    return f"[alert] {redact(str(labels.get('alertname', 'critical alert')))} on {redact(str(subject))}"


def render_body(alert: dict[str, Any], observed_at: str) -> str:
    labels = alert.get("labels") or {}
    annotations = alert.get("annotations") or {}
    lines = [marker_for(alert), "", "Managed Alertmanager critical alert", "", f"Observed at (UTC): {observed_at}"]
    for key in SAFE_LABELS:
        if key in labels:
            lines.append(f"- {key}: {redact(str(labels[key]), 160)}")
    summary = annotations.get("summary") or labels.get("alertname") or "critical alert"
    lines.extend(["", f"Summary: {redact(str(summary))}"])
    return "\n".join(lines) + "\n"


def read_env_file(path: Path) -> dict[str, str]:
    metadata = path.stat()
    if metadata.st_uid != 0 or stat.S_IMODE(metadata.st_mode) != 0o600:
        raise SyncError(f"configuration file must be root:root mode 0600: {path}")
    values: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        key, separator, value = stripped.partition("=")
        if not separator or not re.fullmatch(r"[A-Z][A-Z0-9_]*", key):
            raise SyncError(f"invalid configuration line in {path}")
        values[key] = value
    return values


def load_config(args: argparse.Namespace) -> Config:
    values = dict(os.environ)
    config_path = args.config or os.environ.get(
        "ALERTMANAGER_GITHUB_CONFIG", "/var/lib/areasong-ops/credentials/alertmanager-github.env"
    )
    allow_env = os.environ.get("ALERTMANAGER_GITHUB_ALLOW_ENV_CONFIG") == "true"
    if config_path and Path(config_path).is_file():
        values.update(read_env_file(Path(config_path)))
    elif args.config or args.require_enabled or (not allow_env and not args.validate_only):
        raise SyncError(f"root-only Alertmanager GitHub configuration is missing: {config_path}")

    repository = values.get("GITHUB_REPOSITORY", "AreaSong/ops")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise SyncError("GITHUB_REPOSITORY must use owner/name form")
    token_expires_at_raw = values.get("GITHUB_TOKEN_EXPIRES_AT", "").strip()
    try:
        token_expires_at = datetime.date.fromisoformat(token_expires_at_raw) if token_expires_at_raw else None
    except ValueError as error:
        raise SyncError("GITHUB_TOKEN_EXPIRES_AT must use YYYY-MM-DD form") from error
    config = Config(
        enabled=values.get("ALERTMANAGER_GITHUB_ISSUES_ENABLED", "false").lower() == "true",
        token=values.get("GITHUB_TOKEN", ""),
        repository=repository,
        alertmanager_url=values.get("ALERTMANAGER_URL", "http://127.0.0.1:9093/api/v2/alerts"),
        github_api_base=values.get("GITHUB_API_BASE", "https://api.github.com").rstrip("/"),
        metric_out=Path(values.get("ALERTMANAGER_GITHUB_METRIC_OUT", "/var/lib/node_exporter/textfile_collector/alertmanager-github-issues.prom")),
        token_expires_at=token_expires_at,
        timeout=float(values.get("ALERTMANAGER_HTTP_TIMEOUT_SECONDS", "15")),
    )
    if args.require_enabled and not config.enabled:
        raise SyncError("Alertmanager GitHub Issue sync must be explicitly enabled")
    if config.enabled and not config.token:
        raise SyncError("GITHUB_TOKEN is required when issue sync is enabled")
    return config


class JsonHttpClient:
    def __init__(self, base_url: str, token: str = "", timeout: float = 15.0) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout

    def request(self, method: str, path: str = "", payload: Any = None) -> Any:
        if not path:
            url = self.base_url
        elif path.startswith("/"):
            url = f"{self.base_url}{path}"
        else:
            url = f"{self.base_url}/{path}"
        headers = {"Accept": "application/json", "User-Agent": "areasong-alertmanager-issue-sync/1"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
            headers["X-GitHub-Api-Version"] = "2022-11-28"
        body = None
        if payload is not None:
            body = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(url, data=body, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                raw = response.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as error:
            detail = error.read().decode("utf-8", errors="replace")[:300]
            raise SyncError(f"HTTP {error.code} from {url}: {detail}") from error
        except urllib.error.URLError as error:
            raise SyncError(f"request to {url} failed: {error.reason}") from error


class AlertmanagerClient:
    def __init__(self, config: Config) -> None:
        self.http = JsonHttpClient(config.alertmanager_url, timeout=config.timeout)

    def active_alerts(self) -> list[dict[str, Any]]:
        result = self.http.request("GET")
        if not isinstance(result, list):
            raise SyncError("Alertmanager returned a non-list alert response")
        return critical_alerts(result)


class GitHubIssueClient:
    def __init__(self, config: Config) -> None:
        if not config.token:
            raise SyncError("GITHUB_TOKEN is required when issue sync is enabled")
        owner, repo = config.repository.split("/", 1)
        self.path_prefix = f"/repos/{urllib.parse.quote(owner)}/{urllib.parse.quote(repo)}"
        self.http = JsonHttpClient(config.github_api_base, config.token, config.timeout)

    def ensure_label(self) -> None:
        try:
            self.http.request("POST", f"{self.path_prefix}/labels", {"name": MANAGED_LABEL, "color": "B60205"})
        except SyncError as error:
            if "HTTP 422" not in str(error):
                raise

    def open_issues(self) -> list[dict[str, Any]]:
        issues: list[dict[str, Any]] = []
        for page in range(1, 11):
            result = self.http.request(
                "GET",
                f"{self.path_prefix}/issues?state=open&labels={urllib.parse.quote(MANAGED_LABEL)}&per_page=100&page={page}",
            )
            if not isinstance(result, list):
                raise SyncError("GitHub returned a non-list issue response")
            issues.extend(item for item in result if "pull_request" not in item)
            if len(result) < 100:
                break
        else:
            raise SyncError("more than 1000 managed open Issues require manual cleanup")
        return issues

    def create(self, title: str, body: str) -> int:
        result = self.http.request("POST", f"{self.path_prefix}/issues", {"title": title, "body": body, "labels": [MANAGED_LABEL]})
        return int(result["number"])

    def update(self, number: int, title: str, body: str) -> None:
        self.http.request("PATCH", f"{self.path_prefix}/issues/{number}", {"title": title, "body": body, "state": "open"})

    def close(self, number: int) -> None:
        recovered_at = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        self.http.request(
            "POST",
            f"{self.path_prefix}/issues/{number}/comments",
            {"body": f"Alertmanager no longer reports this critical alert as active. Recovered at {recovered_at}."},
        )
        self.http.request("PATCH", f"{self.path_prefix}/issues/{number}", {"state": "closed"})


def issue_markers(issues: Iterable[dict[str, Any]]) -> dict[str, list[int]]:
    result: dict[str, list[int]] = {}
    for issue in issues:
        body = str(issue.get("body") or "")
        if MARKER_PREFIX not in body:
            continue
        marker = body.split(MARKER_PREFIX, 1)[1].split(MARKER_SUFFIX, 1)[0]
        full_marker = f"{MARKER_PREFIX}{marker}{MARKER_SUFFIX}"
        result.setdefault(full_marker, []).append(int(issue["number"]))
    return result


def sync_alerts(
    alerts: list[dict[str, Any]],
    github: GitHubIssueClient,
    observed_at: str,
    *,
    close_stale: bool = True,
) -> dict[str, int]:
    github.ensure_label()
    existing = issue_markers(github.open_issues())
    active_markers: set[str] = set()
    created = updated = closed = 0
    for alert in alerts:
        marker = marker_for(alert)
        active_markers.add(marker)
        title = render_title(alert)
        body = render_body(alert, observed_at)
        numbers = existing.get(marker, [])
        if not numbers:
            github.create(title, body)
            created += 1
        else:
            github.update(numbers[0], title, body)
            updated += 1
            for duplicate in numbers[1:]:
                github.close(duplicate)
                closed += 1
    if close_stale:
        for marker, numbers in existing.items():
            if marker in active_markers:
                continue
            for number in numbers:
                github.close(number)
                closed += 1
    return {"active": len(alerts), "created": created, "updated": updated, "closed": closed}


def token_expiry_metrics(token_expires_at: datetime.date | None, now: float) -> tuple[int, int, float]:
    if token_expires_at is None:
        return 0, 0, 0.0
    expiry = datetime.datetime.combine(token_expires_at, datetime.time.min, tzinfo=datetime.timezone.utc)
    expiry_timestamp = int(expiry.timestamp())
    return 1, expiry_timestamp, (expiry_timestamp - now) / 86400


def write_metrics(
    path: Path,
    configured: int,
    success: int,
    counts: dict[str, int],
    *,
    alertmanager_available: int,
    token_expires_at: datetime.date | None,
) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    now_float = time.time()
    now = int(now_float)
    expiry_configured, expiry_timestamp, days_until_expiry = token_expiry_metrics(token_expires_at, now_float)
    temporary = path.with_suffix(path.suffix + ".tmp")
    lines = [
        "# HELP alertmanager_github_issue_sync_configured Whether the managed GitHub Issue sync is configured.",
        "# TYPE alertmanager_github_issue_sync_configured gauge",
        f"alertmanager_github_issue_sync_configured {configured}",
        "# HELP alertmanager_github_issue_sync_last_success_timestamp Unix timestamp of the latest sync attempt.",
        "# TYPE alertmanager_github_issue_sync_last_success_timestamp gauge",
        f"alertmanager_github_issue_sync_last_success_timestamp {now}",
        "# HELP alertmanager_github_issue_sync_success Whether the latest sync succeeded.",
        "# TYPE alertmanager_github_issue_sync_success gauge",
        f"alertmanager_github_issue_sync_success {success}",
        "# HELP alertmanager_github_alertmanager_available Whether the Alertmanager API was available during the latest sync.",
        "# TYPE alertmanager_github_alertmanager_available gauge",
        f"alertmanager_github_alertmanager_available {alertmanager_available}",
        "# HELP alertmanager_github_token_expiry_configured Whether GitHub token expiry metadata is configured.",
        "# TYPE alertmanager_github_token_expiry_configured gauge",
        f"alertmanager_github_token_expiry_configured {expiry_configured}",
        "# HELP alertmanager_github_token_expiry_timestamp GitHub token expiry time as a Unix timestamp.",
        "# TYPE alertmanager_github_token_expiry_timestamp gauge",
        f"alertmanager_github_token_expiry_timestamp {expiry_timestamp}",
        "# HELP alertmanager_github_token_days_until_expiry Days remaining until the GitHub token expires.",
        "# TYPE alertmanager_github_token_days_until_expiry gauge",
        f"alertmanager_github_token_days_until_expiry {days_until_expiry:.6f}",
    ]
    for key in ("active", "created", "updated", "closed"):
        lines.extend([f"# TYPE alertmanager_github_issue_sync_{key} gauge", f"alertmanager_github_issue_sync_{key} {counts.get(key, 0)}"])
    temporary.write_text("\n".join(lines) + "\n", encoding="utf-8")
    temporary.chmod(0o644)
    temporary.replace(path)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", default="")
    parser.add_argument("--validate-only", action="store_true")
    parser.add_argument("--require-enabled", action="store_true")
    parser.add_argument("--simulation-mode", choices=("none", "failure", "recovery"), default="none")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    config: Config | None = None
    alertmanager_available = 0
    try:
        config = load_config(args)
        if args.validate_only:
            print("alertmanager GitHub Issue sync configuration syntax: OK")
            return 0
        if not config.enabled:
            write_metrics(
                config.metric_out,
                0,
                1,
                {},
                alertmanager_available=0,
                token_expires_at=config.token_expires_at,
            )
            print("alertmanager GitHub Issue sync is disabled")
            return 0
        close_stale = True
        try:
            alerts = AlertmanagerClient(config).active_alerts()
            alertmanager_available = 1
            if args.simulation_mode == "failure":
                alerts.append(simulation_alert())
        except SyncError as error:
            close_stale = False
            alerts = [alertmanager_api_watchdog()]
            print(f"WARNING: {error}; reconciling only the Alertmanager API watchdog", file=sys.stderr)
        counts = sync_alerts(
            alerts,
            GitHubIssueClient(config),
            time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            close_stale=close_stale,
        )
        write_metrics(
            config.metric_out,
            1,
            1,
            counts,
            alertmanager_available=alertmanager_available,
            token_expires_at=config.token_expires_at,
        )
        print(json.dumps(counts, sort_keys=True))
        return 0
    except (OSError, SyncError, KeyError, ValueError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        try:
            metric_path = config.metric_out if config else Path(
                os.environ.get(
                    "ALERTMANAGER_GITHUB_METRIC_OUT",
                    "/var/lib/node_exporter/textfile_collector/alertmanager-github-issues.prom",
                )
            )
            write_metrics(
                metric_path,
                int(bool(config and config.enabled)),
                0,
                {},
                alertmanager_available=alertmanager_available,
                token_expires_at=config.token_expires_at if config else None,
            )
        except OSError:
            pass
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
