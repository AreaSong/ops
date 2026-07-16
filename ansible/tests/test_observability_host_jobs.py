from __future__ import annotations

import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYBOOK = REPO_ROOT / "ansible" / "observability-host-jobs.yml"


class ObservabilityHostJobsTests(unittest.TestCase):
    def setUp(self) -> None:
        plays = yaml.safe_load(PLAYBOOK.read_text(encoding="utf-8"))
        self.play = plays[0]

    def test_all_host_jobs_are_managed(self) -> None:
        self.assertEqual(
            set(self.play["vars"]["cron_files"]),
            {
                "ops-daily-ops-audit",
                "ops-docker-metrics",
                "ops-security-metrics",
                "ops-sub2api-capacity-metrics",
            },
        )
        self.assertEqual(
            set(self.play["vars"]["compliance_archive_cron_files"]),
            {"ops-compliance-log-archive"},
        )
        task_names = [task.get("name") for task in self.play["tasks"]]
        activation_task = "Publish the compliance archive activation gate after successful deployment"
        cron_task = "Install compliance archive cron job"
        self.assertIn(activation_task, task_names)
        self.assertIn("Disable the compliance archive cron when the feature is disabled", task_names)
        self.assertIn(
            "Remove the compliance archive activation gate when the feature is disabled",
            task_names,
        )
        self.assertGreater(task_names.index(activation_task), task_names.index(cron_task))

    def test_collector_dependencies_exist(self) -> None:
        for item in self.play["vars"]["collector_files"]:
            source = REPO_ROOT / "observability" / "scripts" / item["name"]
            self.assertTrue(source.is_file(), source)
        for item in self.play["vars"]["compliance_archive_files"]:
            source = REPO_ROOT / "scripts" / "backup" / item["name"]
            self.assertTrue(source.is_file(), source)


if __name__ == "__main__":
    unittest.main()
