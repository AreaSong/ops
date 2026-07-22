from __future__ import annotations

import unittest
from pathlib import Path

import yaml


ALERTMANAGER_CONFIG = Path(__file__).resolve().parents[2] / "alertmanager" / "alertmanager.yml"


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


if __name__ == "__main__":
    unittest.main()
