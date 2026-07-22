from __future__ import annotations

import unittest
from pathlib import Path

import yaml


ALERTMANAGER_CONFIG = Path(__file__).resolve().parents[2] / "alertmanager" / "alertmanager.yml"
REPO_ROOT = Path(__file__).resolve().parents[3]
RULES_DIR = REPO_ROOT / "observability" / "prometheus" / "rules"
RUNBOOK_PREFIX = "https://github.com/AreaSong/ops/blob/main/"


class AlertmanagerContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.config = yaml.safe_load(ALERTMANAGER_CONFIG.read_text(encoding="utf-8"))

    def test_root_cause_inhibition_contract(self) -> None:
        expected = [
            (
                ['alertname="BlackboxExporterMetricsTargetDown"'],
                ['alertname=~"AppBlackboxTargetDown|BusinessBlackboxTargetDown|AppHttpProbeFailed|BusinessHttpProbeFailed"'],
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
