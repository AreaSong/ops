#!/usr/bin/env python3
"""Publish a root-only LosAngeles dead-man heartbeat to GitHub.

The heartbeat deliberately uses the existing least-privilege GitHub Issues
credential.  It updates one closed, marker-owned Issue body instead of adding
unbounded comments or exposing a new inbound endpoint on the production host.
"""

from __future__ import annotations

import argparse
import datetime as dt
import http.client
import json
import os
import re
import stat
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any


MARKER = "<!-- areasong-external-heartbeat:v1 -->"
TITLE = "[monitor] LosAngeles external heartbeat"
DEFAULT_CONFIG = "/var/lib/areasong-ops/credentials/alertmanager-github.env"
REPOSITORY_PATTERN = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
TIMESTAMP_PATTERN = re.compile(r"Last heartbeat \(UTC\): `([^`]+)`")


class HeartbeatError(RuntimeError):
    """Raised when the heartbeat cannot be published safely."""


@dataclass(frozen=True)
class Config:
    enabled: bool
    token: str
    repository: str
    api_base: str
    timeout: float = 15.0


def read_env_file(path: Path, *, enforce_permissions: bool = True) -> dict[str, str]:
    if enforce_permissions:
        metadata = path.stat()
        if metadata.st_uid != 0 or stat.S_IMODE(metadata.st_mode) != 0o600:
            raise HeartbeatError(f"configuration file must be root:root mode 0600: {path}")
    values: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        key, separator, value = stripped.partition("=")
        if not separator or not re.fullmatch(r"[A-Z][A-Z0-9_]*", key):
            raise HeartbeatError(f"invalid configuration line in {path}")
        values[key] = value
    return values


def load_config(path: Path, *, enforce_permissions: bool = True) -> Config:
    if not path.is_file():
        raise HeartbeatError(f"root-only GitHub configuration is missing: {path}")
    values = read_env_file(path, enforce_permissions=enforce_permissions)
    repository = values.get("GITHUB_REPOSITORY", "AreaSong/ops")
    if not REPOSITORY_PATTERN.fullmatch(repository):
        raise HeartbeatError("GITHUB_REPOSITORY must use owner/name form")
    enabled = values.get("ALERTMANAGER_GITHUB_ISSUES_ENABLED", "false").lower() == "true"
    token = values.get("GITHUB_TOKEN", "")
    if enabled and not token:
        raise HeartbeatError("GITHUB_TOKEN is required when GitHub Issue sync is enabled")
    try:
        timeout = float(values.get("ALERTMANAGER_HTTP_TIMEOUT_SECONDS", "15"))
    except ValueError as error:
        raise HeartbeatError("ALERTMANAGER_HTTP_TIMEOUT_SECONDS must be numeric") from error
    if timeout <= 0:
        raise HeartbeatError("ALERTMANAGER_HTTP_TIMEOUT_SECONDS must be positive")
    return Config(
        enabled=enabled,
        token=token,
        repository=repository,
        api_base=values.get("GITHUB_API_BASE", "https://api.github.com").rstrip("/"),
        timeout=timeout,
    )


def render_body(now: dt.datetime) -> str:
    observed = now.astimezone(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    return "\n".join(
        (
            MARKER,
            "",
            "Managed LosAngeles external heartbeat state",
            "",
            f"Last heartbeat (UTC): `{observed}`",
            "Source: LosAngeles host cron",
            "Purpose: dead-man monitoring only; no service logs or credentials are stored here.",
            "",
        )
    )


def parse_timestamp(body: str) -> dt.datetime:
    match = TIMESTAMP_PATTERN.search(body)
    if not match:
        raise HeartbeatError("heartbeat Issue body has no valid timestamp")
    try:
        parsed = dt.datetime.fromisoformat(match.group(1).replace("Z", "+00:00"))
    except ValueError as error:
        raise HeartbeatError("heartbeat Issue timestamp is invalid") from error
    if parsed.tzinfo is None:
        raise HeartbeatError("heartbeat Issue timestamp must include a timezone")
    return parsed.astimezone(dt.timezone.utc)


class JsonHttpClient:
    def __init__(self, base_url: str, token: str, timeout: float) -> None:
        if not base_url.startswith("https://"):
            raise HeartbeatError("GitHub API base must use https")
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout

    def request(self, method: str, path: str, payload: Any = None) -> Any:
        url = f"{self.base_url}{path if path.startswith('/') else '/' + path}"
        headers = {
            "Accept": "application/vnd.github+json",
            "User-Agent": "areasong-external-heartbeat/1",
            "X-GitHub-Api-Version": "2022-11-28",
            "Authorization": f"Bearer {self.token}",
        }
        body = None
        if payload is not None:
            body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(url, data=body, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                raw = response.read()
        except urllib.error.HTTPError as error:
            # Do not include response bodies in the cron log: GitHub can echo
            # user-controlled issue text and accidentally expose operational data.
            raise HeartbeatError(f"GitHub API returned HTTP {error.code} for {method} {path}") from error
        except urllib.error.URLError as error:
            raise HeartbeatError(f"GitHub API request failed for {method} {path}: {error.reason}") from error
        except http.client.IncompleteRead as error:
            # A truncated GitHub response is a transient API/network failure;
            # keep cron output actionable without emitting a traceback or body.
            raise HeartbeatError(f"GitHub API response was incomplete for {method} {path}") from error
        try:
            return json.loads(raw) if raw else {}
        except json.JSONDecodeError as error:
            raise HeartbeatError(f"GitHub API returned invalid JSON for {method} {path}") from error


class HeartbeatPublisher:
    def __init__(self, config: Config, client: JsonHttpClient | None = None) -> None:
        owner, repository = config.repository.split("/", 1)
        prefix = f"/repos/{urllib.parse.quote(owner)}/{urllib.parse.quote(repository)}"
        self.prefix = prefix
        self.client = client or JsonHttpClient(config.api_base, config.token, config.timeout)

    def _find_managed_issue(self) -> dict[str, Any] | None:
        response = self.client.request(
            "GET",
            f"{self.prefix}/issues?state=all&sort=updated&direction=desc&per_page=100",
        )
        if not isinstance(response, list):
            raise HeartbeatError("GitHub returned a non-list Issue response")
        matches = [
            issue
            for issue in response
            if "pull_request" not in issue and MARKER in str(issue.get("body") or "")
        ]
        if len(matches) > 1:
            raise HeartbeatError("multiple managed heartbeat Issues exist; manual deduplication is required")
        return matches[0] if matches else None

    def publish(self, now: dt.datetime) -> int:
        body = render_body(now)
        issue = self._find_managed_issue()
        if issue is None:
            response = self.client.request(
                "POST",
                f"{self.prefix}/issues",
                {"title": TITLE, "body": body},
            )
            try:
                number = int(response["number"])
            except (KeyError, TypeError, ValueError) as error:
                raise HeartbeatError("GitHub did not return the new heartbeat Issue number") from error
            self.client.request("PATCH", f"{self.prefix}/issues/{number}", {"state": "closed"})
            return number

        try:
            number = int(issue["number"])
        except (KeyError, TypeError, ValueError) as error:
            raise HeartbeatError("managed heartbeat Issue has an invalid number") from error
        self.client.request(
            "PATCH",
            f"{self.prefix}/issues/{number}",
            {"title": TITLE, "body": body, "state": "closed"},
        )
        return number


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path(os.environ.get("ALERTMANAGER_GITHUB_CONFIG", DEFAULT_CONFIG)))
    parser.add_argument("--validate-only", action="store_true")
    parser.add_argument("--require-enabled", action="store_true")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        config = load_config(args.config)
        if args.require_enabled and not config.enabled:
            raise HeartbeatError("GitHub Issue sync must be explicitly enabled")
        if args.validate_only:
            print("github external heartbeat configuration: valid")
            return 0
        if not config.enabled:
            raise HeartbeatError("GitHub Issue sync is disabled")
        issue_number = HeartbeatPublisher(config).publish(dt.datetime.now(dt.timezone.utc))
        print(f"github external heartbeat published issue={issue_number}")
        return 0
    except (HeartbeatError, OSError) as error:
        print(f"github external heartbeat failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
