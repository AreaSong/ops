from __future__ import annotations

import datetime as dt
import gzip
import io
import json
import sys
import tempfile
import unittest
import urllib.parse
from contextlib import redirect_stdout
from pathlib import Path
from types import SimpleNamespace
from unittest import mock
from zoneinfo import ZoneInfo

SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import daily_ops_audit as cli
import daily_ops_audit_collectors as collectors
import daily_ops_audit_reporting as reporting
from daily_ops_audit_common import (
    EXPECTED_BACKUPS,
    SERVICES,
    AuditData,
    AuditWindow,
    DeliveryResult,
    PrometheusStats,
    RuntimeStats,
    ServiceStats,
    get_json,
    normalize_path,
    parse_system_timestamp,
)

UTC = dt.timezone.utc


def nginx_line(
    timestamp: str,
    target: str,
    host: str = "resume.areasong.top",
    client: str = "203.0.113.10",
    status: int = 200,
    latency: float = 0.25,
) -> str:
    return (
        f'{client} - - [{timestamp}] "GET {target} HTTP/1.1" {status} 123 "-" "agent" '
        f'host="{host}" request_time={latency} upstream_response_time="{latency}"\n'
    )


def sample_data() -> AuditData:
    window = AuditWindow.for_day(dt.date(2026, 7, 15))
    services = {service: ServiceStats() for service in SERVICES}
    services["resume-jadeai"].statuses["2xx"] = 2
    services["resume-jadeai"].paths["/users/:value"] = 2
    services["resume-jadeai"].latencies = [0.1, 0.5]
    services["resume-jadeai"].client_hashes = {"one", "two"}
    security = {
        "ssh_failed": 1,
        "ssh_invalid_user": 1,
        "ssh_accepted": 1,
        "sudo_commands": 2,
        "fail2ban_bans": 1,
        "fail2ban_unbans": 0,
        "ufw_blocks": 3,
    }
    prom = PrometheusStats(
        node_exporter_up=1,
        network_receive_bytes=1024,
        network_transmit_bytes=2048,
        load1_peak=1.5,
        memory_used_percent_peak=50,
        disk_used_percent_peak=40,
        cpu_count=4,
    )
    return AuditData(
        window=window,
        services=services,
        security=security,
        prometheus=prom,
        runtime=RuntimeStats(docker_running=18, ufw_active=1),
        system_timezone="UTC",
    )


class PathPrivacyTests(unittest.TestCase):
    def test_normalize_path_keeps_only_allowlisted_route_segments(self) -> None:
        cases = {
            "/api/v1/responses?token=secret": "/api/v1/responses",
            "/users/alice@example.com": "/users/:value",
            "/profile/alice": "/profile/:value",
            "/reset/AbCd12": "/reset/:value",
            "/assets/app.a1b2c3.js": "/assets/:asset.js",
            "/%3Cimg%20src=x%20onerror=alert(1)%3E": "/:value",
        }
        for raw, expected in cases.items():
            with self.subTest(raw=raw):
                self.assertEqual(normalize_path(raw), expected)

    def test_report_contains_no_untrusted_path_value(self) -> None:
        data = sample_data()
        report = reporting.build_report(data, reporting.build_findings(data))
        self.assertIn("/users/:value", report)
        self.assertNotIn("alice@example.com", report)
        self.assertIn("白名单规范化路径", report)


class TimestampTests(unittest.TestCase):
    def test_traditional_syslog_uses_host_timezone(self) -> None:
        reference = dt.datetime(2026, 7, 16, tzinfo=UTC)
        parsed = parse_system_timestamp(
            "Jul 15 23:30:00 host sshd[1]: Accepted publickey",
            reference,
            ZoneInfo("Asia/Shanghai"),
        )
        expected = dt.datetime(2026, 7, 15, 15, 30, tzinfo=UTC).timestamp()
        self.assertEqual(parsed, expected)

    def test_traditional_syslog_handles_year_boundary(self) -> None:
        reference = dt.datetime(2027, 1, 1, 1, tzinfo=UTC)
        parsed = parse_system_timestamp(
            "Dec 31 23:59:59 host sshd[1]: Failed password",
            reference,
            UTC,
        )
        self.assertEqual(parsed, dt.datetime(2026, 12, 31, 23, 59, 59, tzinfo=UTC).timestamp())

    def test_fail2ban_timestamp_without_timezone_uses_host_timezone(self) -> None:
        reference = dt.datetime(2026, 7, 16, tzinfo=UTC)
        parsed = parse_system_timestamp(
            "2026-07-15 23:30:00,123 fail2ban.actions [1]: NOTICE [sshd] Ban 203.0.113.9",
            reference,
            ZoneInfo("Asia/Shanghai"),
        )
        expected = dt.datetime(2026, 7, 15, 15, 30, tzinfo=UTC).timestamp()
        self.assertEqual(parsed, expected)


class LogCollectorTests(unittest.TestCase):
    def test_nginx_reads_rotated_gzip_and_uses_half_open_window(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir) / "ops-business-access.log"
            base.write_text(
                nginx_line("15/Jul/2026:23:59:59 +0000", "/users/alice@example.com")
                + nginx_line("15/Jul/2026:23:59:58 +0000", "/login", host="monitor.areasong.top")
                + nginx_line(
                    "15/Jul/2026:23:59:57 +0000",
                    "/stream/secret",
                    host="log.areasong.top",
                    status=101,
                    latency=300,
                )
                + nginx_line("15/Jul/2026:23:59:56 +0000", "/", host="203.0.113.20")
                + nginx_line("16/Jul/2026:00:00:00 +0000", "/api/v1/responses"),
                encoding="utf-8",
            )
            with gzip.open(f"{base}.2.gz", "wt", encoding="utf-8") as handle:
                handle.write(
                    nginx_line(
                        "15/Jul/2026:00:00:00 +0000",
                        "/v1/responses",
                        host="cpa.areasong.top",
                        client="198.51.100.22",
                        latency=3.0,
                    )
                )
            failures: list[str] = []
            start = dt.datetime(2026, 7, 15, tzinfo=UTC).timestamp()
            end = dt.datetime(2026, 7, 16, tzinfo=UTC).timestamp()
            stats, parse_errors, unmapped = collectors.collect_nginx(
                start,
                end,
                failures,
                pattern=f"{base}*",
                client_salt=b"test-salt",
            )
            self.assertEqual(sum(stats["resume-jadeai"].statuses.values()), 1)
            self.assertEqual(sum(stats["sub2api"].statuses.values()), 1)
            self.assertEqual(sum(stats["grafana"].statuses.values()), 1)
            self.assertEqual(sum(stats["ops-log-gateway"].statuses.values()), 1)
            self.assertEqual(sum(len(item.client_hashes) for item in stats.values()), 4)
            self.assertEqual(stats["resume-jadeai"].paths["/users/:value"], 1)
            self.assertEqual(stats["sub2api"].slow_requests, 0)
            self.assertEqual(stats["ops-log-gateway"].slow_requests, 0)
            self.assertEqual(stats["ops-log-gateway"].latencies, [])
            self.assertEqual((parse_errors, unmapped, failures), (0, 1, []))

    def test_security_collects_gzip_and_ufw_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            (root / "auth.log").write_text(
                "2026-07-15T01:00:00+00:00 host sshd[1]: Failed password\n"
                "2026-07-15T02:00:00+00:00 host sudo: user : COMMAND=/bin/true\n",
                encoding="utf-8",
            )
            with gzip.open(root / "fail2ban.log.2.gz", "wt", encoding="utf-8") as handle:
                handle.write("2026-07-15 03:00:00,123 fail2ban.actions [1]: NOTICE [sshd] Ban 203.0.113.9\n")
            (root / "syslog").write_text(
                "2026-07-15T04:00:00+00:00 host kernel: [UFW BLOCK] IN=eth0\n",
                encoding="utf-8",
            )
            failures: list[str] = []
            counts = collectors.collect_security(
                dt.datetime(2026, 7, 15, tzinfo=UTC).timestamp(),
                dt.datetime(2026, 7, 16, tzinfo=UTC).timestamp(),
                UTC,
                failures,
                collectors.SecurityLogPatterns(
                    auth=str(root / "auth.log*"),
                    fail2ban=str(root / "fail2ban.log*"),
                    ufw=str(root / "ufw.log*"),
                    syslog=str(root / "syslog*"),
                ),
            )
            self.assertEqual(counts["ssh_failed"], 1)
            self.assertEqual(counts["sudo_commands"], 1)
            self.assertEqual(counts["fail2ban_bans"], 1)
            self.assertEqual(counts["ufw_blocks"], 1)
            self.assertEqual(failures, [])


class PrometheusCollectorTests(unittest.TestCase):
    @staticmethod
    def fake_prometheus(url: str, data: bytes | None = None) -> object:
        del data
        expression = urllib.parse.parse_qs(urllib.parse.urlsplit(url).query)["query"][0]
        if expression == "backup_last_success_timestamp":
            names = sorted(EXPECTED_BACKUPS - {"redis"})
            return {
                "status": "success",
                "data": {"result": [{"metric": {"backup": name}, "value": [0, "1784160000"]} for name in names]},
            }
        if expression == "r2_backup_last_success_timestamp":
            return {"status": "success", "data": {"result": [{"metric": {}, "value": [0, "1784160000"]}]}}
        if expression in {"backup_set_last_success_timestamp", "backup_set_r2_verify_last_success_timestamp"}:
            return {"status": "success", "data": {"result": [{"metric": {}, "value": [0, "1784160000"]}]}}
        value = "18" if "count(docker_container_running)" in expression else "1"
        return {"status": "success", "data": {"result": [{"metric": {}, "value": [0, value]}]}}

    def test_missing_backup_is_not_silently_zero(self) -> None:
        failures: list[str] = []
        with mock.patch.object(collectors, "get_json", side_effect=self.fake_prometheus):
            stats = collectors.collect_prometheus("http://prometheus", 1784160000, failures)
        self.assertEqual(stats.backup_missing, ["redis"])
        self.assertFalse(stats.r2_missing)
        self.assertEqual(failures, [])

    def test_non_finite_prometheus_value_is_rejected(self) -> None:
        response = {"status": "success", "data": {"result": [{"metric": {}, "value": [0, "NaN"]}]}}
        with mock.patch.object(collectors, "get_json", return_value=response):
            with self.assertRaises(RuntimeError):
                collectors.prom_query_vector("http://prometheus", "up", 1)


class ReportingAndCliTests(unittest.TestCase):
    def test_cli_run_writes_report_and_metrics_without_email(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            args = SimpleNamespace(
                date="2026-07-15",
                system_timezone="UTC",
                prometheus_url="http://prometheus",
                alertmanager_url="http://alertmanager",
                report_dir=str(root / "reports"),
                metric_out=str(root / "metrics" / "daily.prom"),
                retention_days=180,
                no_email=True,
            )
            with mock.patch.object(cli, "resolve_report_day", return_value=dt.date(2026, 7, 15)), mock.patch.object(
                cli, "collect_data", return_value=sample_data()
            ):
                with redirect_stdout(io.StringIO()):
                    result = cli.run(args)
            self.assertEqual(result, 0)
            self.assertTrue((root / "reports" / "daily-ops-audit-2026-07-15.md").exists())
            metrics = (root / "metrics" / "daily.prom").read_text(encoding="utf-8")
            self.assertIn('daily_ops_audit_delivery{state="attempted"} 0', metrics)

    def test_metrics_use_stable_series_and_no_email_is_not_accepted(self) -> None:
        data = sample_data()
        findings = reporting.build_findings(data)
        metrics = reporting.build_metrics(
            data,
            findings,
            DeliveryResult(),
            dt.datetime(2026, 7, 16, 0, 20, tzinfo=UTC),
        )
        self.assertIn("daily_ops_audit_last_success_timestamp 1784161200", metrics)
        self.assertNotIn('report_date="', metrics)
        self.assertIn('daily_ops_audit_delivery{state="accepted"} 0', metrics)
        self.assertIn('daily_ops_audit_http_latency_seconds{service="sub2api",percentile="p95"}', metrics)
        self.assertNotIn('quantile="', metrics)

    def test_unmapped_host_warning_requires_material_volume_and_ratio(self) -> None:
        data = sample_data()
        data.nginx_unmapped = 1001
        self.assertTrue(any("未映射 host" in item.message for item in reporting.build_findings(data)))

        data.services["resume-jadeai"].statuses["2xx"] = 100_000
        self.assertFalse(any("未映射 host" in item.message for item in reporting.build_findings(data)))

    def test_ufw_warning_uses_observed_public_host_baseline(self) -> None:
        data = sample_data()
        data.security["ufw_blocks"] = 6000
        self.assertFalse(any("UFW" in item.message for item in reporting.build_findings(data)))
        data.security["ufw_blocks"] = 6001
        self.assertTrue(any("UFW" in item.message for item in reporting.build_findings(data)))

    def test_report_alert_identity_does_not_change_with_severity(self) -> None:
        payloads: list[list[dict[str, object]]] = []

        def capture(url: str, data: bytes | None = None) -> object:
            del url
            payloads.append(json.loads((data or b"[]").decode("utf-8")))
            return {}

        with mock.patch.object(reporting, "get_json", side_effect=capture):
            reporting.send_report("http://alertmanager", "2026-07-15", "critical", "report", Path("/tmp/report"))
            reporting.send_report("http://alertmanager", "2026-07-15", "warning", "report", Path("/tmp/report"))
        first = payloads[0][0]
        second = payloads[1][0]
        self.assertEqual(first["labels"], second["labels"])
        self.assertNotIn("severity", first["labels"])
        self.assertNotEqual(first["annotations"]["severity"], second["annotations"]["severity"])

    def test_post_json_accepts_empty_success_response(self) -> None:
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = b""
        with mock.patch("urllib.request.urlopen", return_value=response):
            self.assertEqual(get_json("http://alertmanager/api/v2/alerts", b"[]"), {})

    def test_prune_reports_removes_only_expired_matching_reports(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            old = root / "daily-ops-audit-2025-01-01.md"
            recent = root / "daily-ops-audit-2026-07-14.md"
            unrelated = root / "notes.md"
            for path in (old, recent, unrelated):
                path.write_text("x", encoding="utf-8")
            cli.prune_reports(root, dt.date(2026, 7, 15), 180)
            self.assertFalse(old.exists())
            self.assertTrue(recent.exists())
            self.assertTrue(unrelated.exists())


if __name__ == "__main__":
    unittest.main()
