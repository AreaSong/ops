from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
RUNNER = REPO_ROOT / "scripts" / "backup" / "run-backup-job.sh"


class BackupJobRunnerTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.scripts = self.root / "scripts"
        self.bin_dir = self.root / "bin"
        self.metrics = self.root / "metrics"
        self.locks = self.root / "locks"
        self.scripts.mkdir()
        self.bin_dir.mkdir()
        self._write_test_commands()

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _script(self, job: str, body: str) -> None:
        path = self.scripts / f"backup-{job}.sh"
        path.write_text(f"#!/usr/bin/env bash\nset -eu\n{body}\n", encoding="utf-8")
        path.chmod(0o755)

    def _write_test_commands(self) -> None:
        flock = self.bin_dir / "flock"
        flock.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        flock.chmod(0o755)
        timeout = self.bin_dir / "timeout"
        timeout.write_text(
            f"""#!{sys.executable}
import subprocess
import sys

args = sys.argv[1:]
while args and args[0].startswith("--"):
    args.pop(0)
seconds = int(args.pop(0).removesuffix("s"))
try:
    result = subprocess.run(args, timeout=seconds, check=False)
except subprocess.TimeoutExpired:
    raise SystemExit(124)
raise SystemExit(result.returncode)
""",
            encoding="utf-8",
        )
        timeout.chmod(0o755)

    def _run(self, job: str, **overrides: str) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "BACKUP_JOB_SCRIPT_DIR": str(self.scripts),
                "BACKUP_JOB_METRIC_DIR": str(self.metrics),
                "BACKUP_JOB_LOCK_DIR": str(self.locks),
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
            }
        )
        environment.update(overrides)
        return subprocess.run(
            [str(RUNNER), job],
            text=True,
            capture_output=True,
            check=False,
            env=environment,
            timeout=10,
        )

    def test_success_and_failure_publish_atomic_result_metrics(self) -> None:
        self._script("postgres", "test \"${OPS_BACKUP_JOB_WRAPPED:-}\" = 1")
        result = self._run("postgres")
        self.assertEqual(result.returncode, 0, result.stderr)
        metric = (self.metrics / "backup-job-postgres.prom").read_text(encoding="utf-8")
        self.assertIn('backup_job_last_result{backup_job="postgres"} 1', metric)
        self.assertIn('backup_job_last_attempt_timestamp{backup_job="postgres"}', metric)
        self.assertIn('backup_job_last_duration_seconds{backup_job="postgres"}', metric)

        self._script("postgres", "exit 9")
        result = self._run("postgres")
        self.assertEqual(result.returncode, 9)
        metric = (self.metrics / "backup-job-postgres.prom").read_text(encoding="utf-8")
        self.assertIn('backup_job_last_result{backup_job="postgres"} 0', metric)

    def test_timeout_is_a_failed_attempt(self) -> None:
        self._script("redis", "sleep 3")
        result = self._run("redis", BACKUP_JOB_TIMEOUT_SECONDS="1")
        self.assertEqual(result.returncode, 124)
        metric = (self.metrics / "backup-job-redis.prom").read_text(encoding="utf-8")
        self.assertIn('backup_job_last_result{backup_job="redis"} 0', metric)

    def test_invalid_job_does_not_publish_metrics(self) -> None:
        result = self._run("unknown")
        self.assertEqual(result.returncode, 2)
        self.assertFalse(self.metrics.exists())

    def test_existing_entrypoints_delegate_to_the_runner(self) -> None:
        backup_dir = REPO_ROOT / "scripts" / "backup"
        for job in ("postgres", "redis", "configs", "volumes"):
            content = (backup_dir / f"backup-{job}.sh").read_text(encoding="utf-8")
            self.assertIn('OPS_BACKUP_JOB_WRAPPED:-0', content)
            self.assertIn(f'run-backup-job.sh" {job}', content)


if __name__ == "__main__":
    unittest.main()
