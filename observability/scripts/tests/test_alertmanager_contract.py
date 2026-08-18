from __future__ import annotations

import re
import unittest
from pathlib import Path

import yaml


ALERTMANAGER_CONFIG = Path(__file__).resolve().parents[2] / "alertmanager" / "alertmanager.yml"
EMAIL_TEMPLATE = Path(__file__).resolve().parents[2] / "alertmanager" / "templates" / "email.tmpl"
DAILY_AUDIT_TEMPLATE = (
    Path(__file__).resolve().parents[2] / "alertmanager" / "templates" / "daily-audit.tmpl"
)
REPO_ROOT = Path(__file__).resolve().parents[3]
COMPOSE_PATH = REPO_ROOT / "observability" / "docker-compose.yml"
RULES_DIR = REPO_ROOT / "observability" / "prometheus" / "rules"
RUNBOOK_PREFIX = "https://github.com/AreaSong/ops/blob/main/"


class AlertmanagerContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.config = yaml.safe_load(ALERTMANAGER_CONFIG.read_text(encoding="utf-8"))

    def test_root_cause_inhibition_contract(self) -> None:
        expected = [
            (
                ['alertname="BlackboxExporterMetricsTargetDown"'],
                ['alertname=~"AppBlackboxTargetDown|BusinessBlackboxTargetDown|AppHttpProbeFailed|BusinessHttpProbeFailed|AreaSongOpsAccessPolicyProbeFailed|AreaSongOpsAccessProbeTargetDown"'],
            ),
            (
                ['alertname=~"DockerMetricsStale|DockerMetricsCollectionFailed"'],
                ['alertname=~"DockerContainer.*"'],
            ),
            (
                ['alertname="SecurityMetricsStale"'],
                ['alertname=~"Auditd.*|Ufw.*|Fail2ban.*|Ssh.*|Nginx.*"'],
            ),
            (
                ['alertname="BusinessLogMetricsStale"'],
                ['alertname=~"BusinessHttp.*|BusinessSlowRequestsHigh|BusinessLogParseErrorsHigh"'],
            ),
            (
                ['alertname=~"LokiMetricsTargetDown|PromtailMetricsTargetDown"'],
                ['alertname=~"AuditLogPipeline.*"'],
            ),
        ]
        actual = [
            (rule["source_matchers"], rule["target_matchers"])
            for rule in self.config["inhibit_rules"]
        ]
        self.assertEqual(actual, expected)

    def test_inhibition_does_not_require_unstable_shared_labels(self) -> None:
        for rule in self.config["inhibit_rules"]:
            self.assertNotIn("equal", rule)

    def test_notification_failure_uses_only_the_independent_watchdog(self) -> None:
        route = self.config["route"]["routes"][0]
        self.assertEqual(route["receiver"], "notification-failure-watchdog-only")
        self.assertEqual(route["matchers"], ['alertname="AlertmanagerNotificationFailures"'])
        self.assertFalse(route.get("continue", False))

        receivers = {receiver["name"]: receiver for receiver in self.config["receivers"]}
        watchdog = receivers["notification-failure-watchdog-only"]
        self.assertEqual(watchdog, {"name": "notification-failure-watchdog-only"})

    def test_fail2ban_notifies_on_burst_instead_of_normal_banning(self) -> None:
        rules = yaml.safe_load((RULES_DIR / "alerts.yml").read_text(encoding="utf-8"))
        alerts = {
            rule["alert"]: rule
            for group in rules["groups"]
            for rule in group["rules"]
            if "alert" in rule
        }
        self.assertNotIn("Fail2banSshdCurrentlyBanning", alerts)
        burst = alerts["Fail2banSshdBanBurst"]
        self.assertEqual(
            burst["expr"],
            'clamp_min(delta(fail2ban_total_banned{jail="sshd"}[15m]), 0) > 10',
        )
        self.assertEqual(burst["for"], "5m")
        matchers = {
            matcher
            for route in self.config["route"]["routes"]
            for matcher in route.get("matchers", [])
        }
        self.assertIn('alertname="Fail2banSshdBanBurst"', matchers)
        self.assertNotIn('alertname="Fail2banSshdCurrentlyBanning"', matchers)

    def test_alertmanager_retains_state_for_longest_repeat_interval(self) -> None:
        compose = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))
        command = compose["services"]["alertmanager"]["command"]
        retention = next(
            value.removeprefix("--data.retention=")
            for value in command
            if value.startswith("--data.retention=")
        )
        self.assertTrue(retention.endswith("h"))
        retention_hours = int(retention.removesuffix("h"))
        repeat_hours = [
            int(route["repeat_interval"].removesuffix("h"))
            for route in self.config["route"]["routes"]
            if route.get("repeat_interval", "").endswith("h")
        ]
        self.assertGreaterEqual(retention_hours, max(repeat_hours))

    def test_email_templates_are_utf8_and_localized(self) -> None:
        email = EMAIL_TEMPLATE.read_text(encoding="utf-8")
        daily_audit = DAILY_AUDIT_TEMPLATE.read_text(encoding="utf-8")

        self.assertIn('<meta charset="UTF-8">', email)
        self.assertIn('<meta charset="UTF-8">', daily_audit)
        self.assertIn("洛杉矶告警", email)
        self.assertIn("每日运维审计", daily_audit)
        self.assertIn('template "email.status.zh"', email)
        self.assertIn('template "email.severity.zh"', email)

        r2_rules = (RULES_DIR / "alerts.yml").read_text(encoding="utf-8")
        self.assertIn("R2 备份同步接近 RPO 限制", r2_rules)
        self.assertIn("最近一次成功的 R2 同步已超过 20 小时", r2_rules)

    def test_every_alert_message_is_localized(self) -> None:
        for path in sorted(RULES_DIR.glob("*.yml")):
            data = yaml.safe_load(path.read_text(encoding="utf-8"))
            for group in data.get("groups", []):
                for rule in group.get("rules", []):
                    if "alert" not in rule:
                        continue
                    for key in ("summary", "description"):
                        value = rule.get("annotations", {}).get(key, "")
                        with self.subTest(file=path.name, alert=rule["alert"], annotation=key):
                            self.assertRegex(value, re.compile(r"[\u4e00-\u9fff]"))

    def test_every_alert_has_actionable_metadata(self) -> None:
        alerts = []
        for path in sorted(RULES_DIR.glob("*.yml")):
            data = yaml.safe_load(path.read_text(encoding="utf-8"))
            alerts.extend(
                (path.name, rule)
                for group in data.get("groups", [])
                for rule in group.get("rules", [])
                if "alert" in rule
            )

        self.assertGreater(len(alerts), 100)
        for filename, rule in alerts:
            with self.subTest(file=filename, alert=rule["alert"]):
                self.assertEqual(rule.get("labels", {}).get("owner"), "areasong-ops")
                annotations = rule.get("annotations", {})
                for key in ("summary", "description", "grafana_url", "runbook_url"):
                    self.assertTrue(annotations.get(key), key)
                runbook_url = annotations["runbook_url"]
                self.assertTrue(runbook_url.startswith(RUNBOOK_PREFIX), runbook_url)
                local_runbook = REPO_ROOT / runbook_url.removeprefix(RUNBOOK_PREFIX)
                self.assertTrue(local_runbook.is_file(), local_runbook)


if __name__ == "__main__":
    unittest.main()
