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
                "ops-runtime-snapshot",
                "ops-business-error-log",
                "ops-cloudflare-ip-metrics",
                "ops-business-log-metrics",
                "ops-cloudflare-origin-cert-metrics",
                "ops-fail2ban-enriched",
                "ops-security-metrics",
                "ops-sub2api-capacity-metrics",
            },
        )
        self.assertEqual(
            set(self.play["vars"]["alertmanager_github_cron_files"]),
            {"ops-alertmanager-github-issues", "ops-alertmanager-github-simulation"},
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
        self.assertLess(
            task_names.index("Require a root-only Alertmanager GitHub Issue sync credential"),
            task_names.index("Install Alertmanager GitHub Issue sync cron jobs"),
        )

    def test_collector_dependencies_exist(self) -> None:
        for item in self.play["vars"]["collector_files"]:
            source = REPO_ROOT / "observability" / "scripts" / item["name"]
            self.assertTrue(source.is_file(), source)
        for item in self.play["vars"]["compliance_archive_files"]:
            source = REPO_ROOT / "scripts" / "backup" / item["name"]
            self.assertTrue(source.is_file(), source)
        for cron_name in self.play["vars"]["cron_files"] + self.play["vars"]["alertmanager_github_cron_files"]:
            source = REPO_ROOT / "observability" / "cron" / cron_name
            self.assertTrue(source.is_file(), source)

    def test_minute_collectors_prevent_overlapping_runs(self) -> None:
        managed = set(self.play["vars"]["cron_files"] + self.play["vars"]["alertmanager_github_cron_files"])
        for cron_name in managed - {"ops-daily-ops-audit"}:
            cron = (REPO_ROOT / "observability" / "cron" / cron_name).read_text(encoding="utf-8")
            self.assertIn("/usr/bin/flock -n /run/lock/", cron, cron_name)

    def test_validated_generation_is_activated_before_cron_installation(self) -> None:
        task_names = [task.get("name") for task in self.play["tasks"]]
        self.assertIn("Require a clean controller Git worktree", task_names)
        self.assertIn("Refuse to overwrite an existing inactive generation", task_names)
        self.assertLess(
            task_names.index("Require a clean controller Git worktree"),
            task_names.index("Stage observability collector generation"),
        )
        activation = task_names.index("Atomically activate the validated host-job generation")
        self.assertLess(task_names.index("Validate staged daily audit Python imports"), activation)
        self.assertLess(task_names.index("Validate staged observability log rotation"), activation)
        self.assertLess(activation, task_names.index("Install observability cron jobs"))
        self.assertLess(activation, task_names.index("Install validated observability log rotation"))
        self.assertIn("/var/lib/ops/observability-host-jobs", self.play["vars"]["host_jobs_root"])

        current = "/var/lib/ops/observability-host-jobs/current/"
        cron_paths = [
            *(REPO_ROOT / "observability" / "cron" / name for name in self.play["vars"]["cron_files"]),
            *(
                REPO_ROOT / "observability" / "cron" / name
                for name in self.play["vars"]["alertmanager_github_cron_files"]
            ),
            *(REPO_ROOT / "scripts" / "backup" / "cron" / name for name in self.play["vars"]["compliance_archive_cron_files"]),
        ]
        for path in cron_paths:
            content = path.read_text(encoding="utf-8")
            self.assertIn(current, content, path.name)
            self.assertNotIn("/opt/ops/observability/scripts/", content, path.name)

    def test_git_identity_checks_run_during_check_mode(self) -> None:
        tasks = {task.get("name"): task for task in self.play["tasks"]}
        for task_name in (
            "Inspect the controller Git worktree before staging host jobs",
            "Read the exact controller Git commit",
        ):
            self.assertIn(task_name, tasks)
            self.assertIs(tasks[task_name].get("check_mode"), False)


if __name__ == "__main__":
    unittest.main()
