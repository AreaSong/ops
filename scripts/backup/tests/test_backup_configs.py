from __future__ import annotations

import json
import os
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "scripts" / "backup" / "backup-configs.sh"


class BackupConfigsTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.source = self.root / "source"
        self.backups = self.root / "backups"
        for path in (
            "etc/nginx/nginx.conf",
            "opt/ops/README.md",
            "etc/ssh/sshd_config",
            "etc/ufw/ufw.conf",
            "etc/sysctl.d/99-ops-baseline.conf",
            "etc/cron.d/ops-backup-postgres",
            "etc/systemd/system/x-ui.service",
            "etc/systemd/system/areasong-ops-runner.service",
            "etc/areasong-ops/services.json",
            "usr/local/libexec/areasong-ops/areasong-ops-runner",
        ):
            target = self.source / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(f"fixture for {path}\n", encoding="utf-8")

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _run(self) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "OPS_BACKUP_JOB_WRAPPED": "1",
                "BACKUP_CONFIG_SOURCE_ROOT": str(self.source),
                "BACKUP_CONFIG_BACKUP_ROOT": str(self.backups),
                "BACKUP_CONFIG_LOG_DIR": str(self.root / "logs"),
            }
        )
        return subprocess.run(
            [str(SCRIPT)],
            text=True,
            capture_output=True,
            check=False,
            env=environment,
            timeout=20,
        )

    def test_archive_contains_host_governance_and_coverage_manifest(self) -> None:
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stderr)
        archive_path = Path(result.stdout.strip())
        with tarfile.open(archive_path, "r:gz") as archive:
            names = set(archive.getnames())
            self.assertIn("etc/cron.d/ops-backup-postgres", names)
            self.assertIn("etc/systemd/system/x-ui.service", names)
            coverage = json.load(archive.extractfile("backup-metadata/config-coverage.json"))
        self.assertEqual(coverage["schema_version"], 1)
        entries = {entry["path"]: entry for entry in coverage["entries"]}
        self.assertEqual(entries["/etc/ssh/sshd_config"]["status"], "included")
        self.assertEqual(entries["/etc/ops/*.env"]["status"], "external-secret-required")
        self.assertEqual(entries["/etc/areasong-ops/web.env"]["status"], "external-secret-required")
        self.assertEqual(
            entries["/var/lib/areasong-ops/credentials/alertmanager-github.env"]["status"],
            "external-secret-required",
        )
        self.assertEqual(entries["/etc/areasong-ops/services.json"]["status"], "included")

    def test_missing_required_config_fails_without_archive(self) -> None:
        (self.source / "etc/ssh/sshd_config").unlink()
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("required config path is missing", result.stderr)
        self.assertEqual(list(self.backups.glob("configs-*.tar.gz")), [])


if __name__ == "__main__":
    unittest.main()
