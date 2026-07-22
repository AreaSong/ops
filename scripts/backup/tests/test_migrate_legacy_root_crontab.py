from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "scripts" / "backup" / "migrate-legacy-root-crontab.py"


class MigrateLegacyRootCrontabTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.state = self.root / "crontab"
        self.backups = self.root / "backups"
        self.command = self.root / "fake-crontab"
        self.command.write_text(
            "#!/bin/sh\n"
            'if [ "$1" = "-l" ]; then cat "$CRONTAB_STATE"; else cp "$1" "$CRONTAB_STATE"; fi\n',
            encoding="utf-8",
        )
        self.command.chmod(0o755)
        self.state.write_text(
            '13 23 * * * "/root/.acme.sh"/acme.sh --cron --home "/root/.acme.sh" > /dev/null\n'
            "# BEGIN ops local backups\n"
            "10 2 * * * /opt/ops/scripts/backup/backup-postgres.sh >> /var/log/backup/postgres.log 2>&1\n"
            "30 2 * * * /opt/ops/scripts/backup/backup-redis.sh >> /var/log/backup/redis.log 2>&1\n"
            "0 3 * * * /opt/ops/scripts/backup/backup-configs.sh >> /var/log/backup/configs.log 2>&1\n"
            "30 3 * * * /opt/ops/scripts/backup/backup-volumes.sh >> /var/log/backup/volumes.log 2>&1\n"
            "# END ops local backups\n"
            "# BEGIN ops offsite backups\n"
            "15 4 * * * /opt/ops/scripts/backup/sync-r2.sh >> /var/log/backup/r2.log 2>&1\n"
            "# END ops offsite backups\n"
            "# BEGIN ops observability metrics\n"
            "45 3 * * * /opt/ops/observability/scripts/write-backup-metrics.sh >> /var/log/backup/backup-metrics.log 2>&1\n"
            "# END ops observability metrics\n",
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _run(self, *args: str) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "CRONTAB_COMMAND": f"/bin/sh {self.command}",
                "CRONTAB_STATE": str(self.state),
                "CRONTAB_MIGRATION_ALLOW_NON_ROOT": "1",
            }
        )
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            text=True,
            capture_output=True,
            check=False,
            env=environment,
            timeout=10,
        )

    def test_apply_preserves_unrelated_acme_job_and_writes_backup(self) -> None:
        original = self.state.read_text(encoding="utf-8")
        result = self._run(
            "--apply",
            "--backup-dir",
            str(self.backups),
            "--release-id",
            "a" * 40,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        migrated = self.state.read_text(encoding="utf-8")
        self.assertIn("acme.sh", migrated)
        self.assertNotIn("backup-postgres.sh", migrated)
        backups = list(self.backups.glob("root-crontab-pre-backup-migration-*.txt"))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_text(encoding="utf-8"), original)

        repeated = self._run(
            "--apply",
            "--backup-dir",
            str(self.backups),
            "--release-id",
            "a" * 40,
        )
        self.assertEqual(repeated.returncode, 0, repeated.stderr)
        self.assertEqual(len(list(self.backups.glob("*.txt"))), 1)

    def test_partial_legacy_set_fails_closed(self) -> None:
        content = self.state.read_text(encoding="utf-8")
        self.state.write_text(content.replace("10 2 * * * /opt/ops/scripts/backup/backup-postgres.sh >> /var/log/backup/postgres.log 2>&1\n", ""), encoding="utf-8")
        result = self._run("--check")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("partially present", result.stderr)


if __name__ == "__main__":
    unittest.main()
