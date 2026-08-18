from __future__ import annotations

import datetime as dt
import http.client
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from observability.scripts.github_external_heartbeat import (
    MARKER,
    Config,
    HeartbeatError,
    HeartbeatPublisher,
    JsonHttpClient,
    load_config,
    parse_timestamp,
    render_body,
)


class FakeClient:
    def __init__(self, issues: list[dict] | None = None) -> None:
        self.issues = issues or []
        self.calls: list[tuple[str, str, object | None]] = []

    def request(self, method: str, path: str, payload: object | None = None) -> object:
        self.calls.append((method, path, payload))
        if method == "GET":
            return self.issues
        if method == "POST":
            return {"number": 71}
        if method == "PATCH":
            return {}
        raise AssertionError(method)


class GithubExternalHeartbeatTests(unittest.TestCase):
    def setUp(self) -> None:
        self.config = Config(
            enabled=True,
            token="test-token",
            repository="AreaSong/ops",
            api_base="https://api.github.test",
        )
        self.now = dt.datetime(2026, 8, 16, 6, 0, 1, tzinfo=dt.timezone.utc)

    def test_render_and_parse_timestamp_are_utc_and_marker_owned(self) -> None:
        body = render_body(self.now)
        self.assertIn(MARKER, body)
        self.assertEqual(parse_timestamp(body), self.now.replace(microsecond=0))

    def test_publisher_creates_and_closes_one_issue(self) -> None:
        client = FakeClient()
        issue_number = HeartbeatPublisher(self.config, client).publish(self.now)
        self.assertEqual(issue_number, 71)
        self.assertEqual([call[0] for call in client.calls], ["GET", "POST", "PATCH"])
        self.assertEqual(client.calls[1][1], "/repos/AreaSong/ops/issues")
        self.assertIn(MARKER, str(client.calls[1][2]))
        self.assertEqual(client.calls[2][2], {"state": "closed"})

    def test_publisher_updates_existing_issue_without_comments(self) -> None:
        client = FakeClient([{"number": 71, "body": f"{MARKER}\nold"}])
        HeartbeatPublisher(self.config, client).publish(self.now)
        self.assertEqual([call[0] for call in client.calls], ["GET", "PATCH"])
        self.assertEqual(client.calls[1][1], "/repos/AreaSong/ops/issues/71")
        self.assertEqual(client.calls[1][2]["state"], "closed")  # type: ignore[index]
        self.assertIn("2026-08-16T06:00:01Z", client.calls[1][2]["body"])  # type: ignore[index]

    def test_duplicate_managed_issues_stop_without_mutation(self) -> None:
        client = FakeClient(
            [
                {"number": 71, "body": MARKER},
                {"number": 72, "body": MARKER},
            ]
        )
        with self.assertRaisesRegex(HeartbeatError, "multiple managed"):
            HeartbeatPublisher(self.config, client).publish(self.now)
        self.assertEqual(len(client.calls), 1)

    @mock.patch("urllib.request.urlopen")
    def test_incomplete_github_response_is_a_controlled_error(self, urlopen: mock.Mock) -> None:
        urlopen.side_effect = http.client.IncompleteRead(b"partial", 10)
        client = JsonHttpClient("https://api.github.test", "test-token", 1.0)
        with self.assertRaisesRegex(HeartbeatError, "response was incomplete"):
            client.request("GET", "/repos/AreaSong/ops/issues")

    def test_config_requires_enabled_token_and_repository_shape(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "github.env"
            path.write_text(
                "ALERTMANAGER_GITHUB_ISSUES_ENABLED=true\n"
                "GITHUB_REPOSITORY=AreaSong/ops\n"
                "GITHUB_TOKEN=test-token\n",
                encoding="utf-8",
            )
            config = load_config(path, enforce_permissions=False)
            self.assertTrue(config.enabled)
            self.assertEqual(config.repository, "AreaSong/ops")

            path.write_text(
                "ALERTMANAGER_GITHUB_ISSUES_ENABLED=true\n"
                "GITHUB_REPOSITORY=invalid/repository/path\n"
                "GITHUB_TOKEN=test-token\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(HeartbeatError, "owner/name"):
                load_config(path, enforce_permissions=False)


if __name__ == "__main__":
    unittest.main()
