from __future__ import annotations

import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


ADAPTER = Path(__file__).resolve().parents[1] / "automatic-task.sh"


class AutomaticTaskAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.operation = self.root / "operation"
        self.operation.mkdir()
        self.metric = self.root / "var/lib/node_exporter/textfile_collector/runtime-snapshot.prom"
        self.metric.parent.mkdir(parents=True)
        self.write_metric(int(time.time()) - 10)
        cron = self.root / "etc/cron.d/ops-runtime-snapshot"
        cron.parent.mkdir(parents=True)
        cron.write_text("* * * * * root collector\n", encoding="utf-8")
        collector = self.root / "var/lib/ops/observability-host-jobs/current/observability/scripts/runtime_snapshot.py"
        collector.parent.mkdir(parents=True)
        collector.write_text(
            "#!/bin/sh\n"
            f"tmp='{self.metric}.new'\n"
            "printf 'ops_runtime_snapshot_last_success_timestamp %s\\n' \"$(date +%s)\" >\"$tmp\"\n"
            f"mv \"$tmp\" '{self.metric}'\n",
            encoding="utf-8",
        )
        collector.chmod(0o755)
        self.flock = self.root / "flock"
        self.flock.write_text(
            "#!/bin/sh\n"
            "[ \"$1\" = -n ] || exit 2\n"
            "shift 2\n"
            "exec \"$@\"\n",
            encoding="utf-8",
        )
        self.flock.chmod(0o755)

    def write_metric(self, timestamp: int) -> None:
        self.metric.write_text(
            f"ops_runtime_snapshot_last_success_timestamp {timestamp}\n",
            encoding="utf-8",
        )

    def run_adapter(self, action: str, phase: str, *, task: str = "runtime-snapshot", target: str = "") -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "OPS_SERVICE_NAME": task,
                "OPS_AUTOMATIC_TASK_TEST_ROOT": str(self.root),
                "OPS_AUTOMATIC_TASK_TEST_FLOCK": str(self.flock),
            }
        )
        return subprocess.run(
            [str(ADAPTER), action, phase, str(self.operation), target, ""],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )

    def test_inspect_reports_fresh_registered_task(self) -> None:
        result = self.run_adapter("inspect", "inspect")
        self.assertEqual(result.returncode, 0, result.stderr)
        payload = json.loads(result.stdout)
        self.assertEqual(payload["data"]["health"], "healthy")
        self.assertEqual(payload["data"]["objectId"], "automatic-task:runtime-snapshot")

    def test_rerun_requires_preflight_and_publishes_new_evidence(self) -> None:
        result = self.run_adapter("rerun", "preflight")
        self.assertEqual(result.returncode, 0, result.stderr)
        result = self.run_adapter("rerun", "run")
        self.assertEqual(result.returncode, 0, result.stderr)
        result = self.run_adapter("rerun", "verify")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["data"]["health"], "healthy")

    def test_rejects_unregistered_task_and_target(self) -> None:
        self.assertNotEqual(self.run_adapter("inspect", "inspect", task="unknown").returncode, 0)
        self.assertNotEqual(self.run_adapter("inspect", "inspect", target="user-input").returncode, 0)


if __name__ == "__main__":
    unittest.main()
