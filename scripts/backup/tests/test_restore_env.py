from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "restore_env.py"


class RestoreEnvTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env_file = self.root / ".env"
        self.backup = self.root / "env.before"
        self.env_file.write_text("SECRET=unchanged\nDATA_DIR=/old\n", encoding="utf-8")
        self.env_file.chmod(0o600)

    def run_switch(self, *updates: str) -> subprocess.CompletedProcess[str]:
        command = [str(SCRIPT), "--file", str(self.env_file), "--backup", str(self.backup)]
        for update in updates:
            command.extend(["--set", update])
        return subprocess.run(command, text=True, capture_output=True, check=False)

    def test_switches_only_managed_values_and_preserves_backup(self) -> None:
        result = self.run_switch("DATA_DIR=/new", "POSTGRES_VOLUME=restore_pg_1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            self.env_file.read_text(encoding="utf-8"),
            "SECRET=unchanged\nDATA_DIR=/new\nPOSTGRES_VOLUME=restore_pg_1\n",
        )
        self.assertEqual(self.backup.read_text(encoding="utf-8"), "SECRET=unchanged\nDATA_DIR=/old\n")
        self.assertEqual(self.env_file.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.backup.stat().st_mode & 0o777, 0o600)

    def test_duplicate_managed_key_is_rejected_without_backup(self) -> None:
        self.env_file.write_text("DATA_DIR=/one\nDATA_DIR=/two\n", encoding="utf-8")
        result = self.run_switch("DATA_DIR=/new")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("duplicate managed key", result.stderr)
        self.assertFalse(self.backup.exists())

    def test_symlink_and_weak_mode_are_rejected(self) -> None:
        self.env_file.chmod(0o640)
        result = self.run_switch("DATA_DIR=/new")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("0600", result.stderr)
        actual = self.root / "actual.env"
        self.env_file.replace(actual)
        os.symlink(actual, self.env_file)
        result = self.run_switch("DATA_DIR=/new")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("non-symlink", result.stderr)

    def test_existing_backup_is_never_overwritten(self) -> None:
        self.backup.write_text("evidence\n", encoding="utf-8")
        self.backup.chmod(0o600)
        result = self.run_switch("DATA_DIR=/new")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.backup.read_text(encoding="utf-8"), "evidence\n")
        self.assertEqual(self.env_file.read_text(encoding="utf-8"), "SECRET=unchanged\nDATA_DIR=/old\n")

    def test_reads_managed_value_without_sourcing_secrets(self) -> None:
        result = subprocess.run(
            [str(SCRIPT), "--file", str(self.env_file), "--get", "DATA_DIR", "--default", "/fallback"],
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "/old\n")

        result = subprocess.run(
            [str(SCRIPT), "--file", str(self.root / "missing.env"), "--get", "DATA_DIR", "--default", "/fallback"],
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "/fallback\n")


if __name__ == "__main__":
    unittest.main()
