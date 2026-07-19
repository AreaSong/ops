from __future__ import annotations

import importlib.util
import argparse
import contextlib
import io
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


MODULE_PATH = __import__("pathlib").Path(__file__).resolve().parents[1] / "alertmanager_github_issues.py"
SPEC = importlib.util.spec_from_file_location("alertmanager_github_issues", MODULE_PATH)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


class FakeGitHub:
    def __init__(self, issues: list[dict[str, object]]) -> None:
        self.issues = issues
        self.created: list[tuple[str, str]] = []
        self.updated: list[tuple[int, str, str]] = []
        self.closed: list[int] = []

    def ensure_label(self) -> None:
        return None

    def open_issues(self) -> list[dict[str, object]]:
        return self.issues

    def create(self, title: str, body: str) -> int:
        self.created.append((title, body))
        return 101

    def update(self, number: int, title: str, body: str) -> None:
        self.updated.append((number, title, body))

    def close(self, number: int) -> None:
        self.closed.append(number)


class AlertmanagerGitHubIssueTests(unittest.TestCase):
    def setUp(self) -> None:
        self.alert = {
            "fingerprint": "fingerprint-1",
            "labels": {
                "alertname": "AccountVaultReleaseFailed",
                "severity": "critical",
                "service": "account-vault",
                "instance": "LosAngeles",
            },
            "annotations": {"summary": "Failure for user@example.com from 192.0.2.10"},
            "status": {"state": "active"},
        }

    def test_filters_only_active_critical_alerts_and_redacts_body(self) -> None:
        alerts = MODULE.critical_alerts(
            [
                self.alert,
                {"labels": {"severity": "warning"}, "status": {"state": "active"}},
                {"labels": {"severity": "critical"}, "status": {"state": "resolved"}},
            ]
        )
        self.assertEqual(alerts, [self.alert])
        body = MODULE.render_body(self.alert, "2026-07-18T00:00:00Z")
        self.assertIn("[email]", body)
        self.assertIn("[ip]", body)
        self.assertNotIn("user@example.com", body)

    def test_failure_creates_one_issue_and_recovery_closes_only_managed_issue(self) -> None:
        github = FakeGitHub([])
        first = MODULE.sync_alerts([self.alert], github, "2026-07-18T00:00:00Z")
        self.assertEqual(first, {"active": 1, "created": 1, "updated": 0, "closed": 0})
        self.assertEqual(len(github.created), 1)
        marker = MODULE.marker_for(self.alert)
        github.issues = [{"number": 42, "body": marker}, {"number": 43, "body": "manual issue"}]
        recovered = MODULE.sync_alerts([], github, "2026-07-18T00:05:00Z")
        self.assertEqual(recovered["closed"], 1)
        self.assertEqual(github.closed, [42])

    def test_duplicate_managed_issues_are_collapsed(self) -> None:
        marker = MODULE.marker_for(self.alert)
        github = FakeGitHub([{"number": 10, "body": marker}, {"number": 11, "body": marker}])
        result = MODULE.sync_alerts([self.alert], github, "2026-07-18T00:00:00Z")
        self.assertEqual(result["updated"], 1)
        self.assertEqual(result["closed"], 1)
        self.assertEqual(github.closed, [11])

    def test_simulation_alert_is_a_stable_managed_critical_alert(self) -> None:
        alert = MODULE.simulation_alert()
        self.assertEqual(alert["labels"]["severity"], "critical")
        self.assertIn("monthly-alertmanager-github-simulation", MODULE.marker_for(alert))

    def test_explicit_validate_config_requires_the_file(self) -> None:
        args = argparse.Namespace(
            config="/definitely/missing/alertmanager-github.env",
            validate_only=True,
            require_enabled=True,
            simulation_mode="none",
        )
        with self.assertRaises(MODULE.SyncError):
            MODULE.load_config(args)

    def test_enabled_sync_failure_keeps_configured_metric_true(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            metric = Path(directory) / "sync.prom"
            config = MODULE.Config(
                enabled=True,
                token="test-token",
                repository="AreaSong/ops",
                alertmanager_url="http://127.0.0.1:9093/api/v2/alerts",
                github_api_base="https://api.github.test",
                metric_out=metric,
            )
            args = argparse.Namespace(config="", validate_only=False, require_enabled=False, simulation_mode="none")
            with (
                mock.patch.object(MODULE, "parse_args", return_value=args),
                mock.patch.object(MODULE, "load_config", return_value=config),
                mock.patch.object(MODULE.AlertmanagerClient, "active_alerts", side_effect=MODULE.SyncError("test failure")),
                contextlib.redirect_stderr(io.StringIO()),
            ):
                self.assertEqual(MODULE.main(), 1)
            content = metric.read_text(encoding="utf-8")
            self.assertIn("alertmanager_github_issue_sync_configured 1", content)
            self.assertIn("alertmanager_github_issue_sync_success 0", content)

    def test_open_issue_listing_is_paginated(self) -> None:
        class FakeHttp:
            def __init__(self) -> None:
                self.pages: list[str] = []

            def request(self, method: str, path: str, payload: object = None) -> list[dict[str, object]]:
                self.pages.append(path)
                if "&page=1" in path:
                    return [{"number": index, "body": "managed"} for index in range(100)]
                return [{"number": 101, "body": "managed"}]

        client = MODULE.GitHubIssueClient.__new__(MODULE.GitHubIssueClient)
        client.path_prefix = "/repos/AreaSong/ops"
        client.http = FakeHttp()
        self.assertEqual(len(client.open_issues()), 101)
        self.assertEqual(len(client.http.pages), 2)


if __name__ == "__main__":
    unittest.main()
