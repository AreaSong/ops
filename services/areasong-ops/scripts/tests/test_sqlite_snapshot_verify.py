from __future__ import annotations

import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parents[1]
SCRIPT = SCRIPT_DIR / "sqlite_snapshot_verify.py"


TABLES = {
    "previews": (
        "id TEXT PRIMARY KEY",
        "service TEXT",
        "action TEXT",
        "confirmation_hash TEXT",
        "created_at TEXT",
    ),
    "tasks": (
        "id TEXT PRIMARY KEY",
        "service TEXT",
        "action TEXT",
        "state TEXT",
        "snapshot_json TEXT",
        "created_at TEXT",
    ),
    "events": (
        "sequence INTEGER PRIMARY KEY",
        "task_id TEXT",
        "occurred_at TEXT",
        "level TEXT",
        "message TEXT",
        "data_json TEXT",
    ),
    "audit_entries": (
        "sequence INTEGER PRIMARY KEY",
        "occurred_at TEXT",
        "actor_hash TEXT",
        "event TEXT",
        "resource TEXT",
        "outcome TEXT",
    ),
    "metadata": ("key TEXT PRIMARY KEY", "value TEXT"),
}


class SqliteSnapshotVerifyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.source = self.root / "ops.db"
        self.output = self.root / "snapshots" / "ops-copy.db"
        self.work = self.root / "work"
        self._create_database(self.source)

    def tearDown(self) -> None:
        self.temporary.cleanup()

    @staticmethod
    def _create_database(path: Path, *, include_events: bool = True) -> None:
        connection = sqlite3.connect(path)
        try:
            for name, columns in TABLES.items():
                if name == "events" and not include_events:
                    continue
                connection.execute(f'CREATE TABLE "{name}" ({", ".join(columns)})')
            connection.execute("PRAGMA user_version = 5")
            connection.execute(
                "INSERT INTO metadata(key, value) VALUES ('schema', 'test')"
            )
            connection.commit()
        finally:
            connection.close()
        os.chmod(path, 0o600)

    def _run(self, *arguments: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT), *arguments],
            capture_output=True,
            text=True,
            check=False,
            timeout=20,
        )

    def test_no_subcommand_is_safe_and_reports_usage(self) -> None:
        result = self._run()
        self.assertEqual(result.returncode, 2)
        self.assertIn("snapshot", result.stderr)
        self.assertFalse((self.root / "snapshots").exists())

    def test_snapshot_verify_and_restore_check_are_isolated(self) -> None:
        source_bytes = self.source.read_bytes()
        result = self._run(
            "snapshot",
            "--source",
            str(self.source),
            "--output",
            str(self.output),
            "--json",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertEqual(report["schema_version"], 5)
        self.assertTrue(self.output.exists())
        self.assertEqual(self.output.stat().st_mode & 0o777, 0o600)

        verify = self._run("verify", "--snapshot", str(self.output), "--json")
        self.assertEqual(verify.returncode, 0, verify.stderr)
        self.assertEqual(json.loads(verify.stdout)["tables"]["metadata"], 1)

        restore = self._run(
            "restore-check",
            "--snapshot",
            str(self.output),
            "--work-root",
            str(self.work),
            "--json",
        )
        self.assertEqual(restore.returncode, 0, restore.stderr)
        self.assertTrue(json.loads(restore.stdout)["isolated_copy"])
        self.assertEqual(list(self.work.glob("areasong-ops-restore-check-*")), [])
        self.assertEqual(self.source.read_bytes(), source_bytes)

    def test_snapshot_refuses_to_overwrite_existing_destination(self) -> None:
        self.output.parent.mkdir(mode=0o700)
        self.output.write_bytes(b"keep\n")
        result = self._run(
            "snapshot",
            "--source",
            str(self.source),
            "--output",
            str(self.output),
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("拒绝覆盖", result.stderr)
        self.assertEqual(self.output.read_bytes(), b"keep\n")

    def test_snapshot_requires_explicit_destination_and_rejects_production_path(self) -> None:
        missing = self._run("snapshot", "--source", str(self.source))
        self.assertNotEqual(missing.returncode, 0)
        self.assertIn("--output", missing.stderr)

        same = self._run(
            "snapshot",
            "--source",
            str(self.source),
            "--output",
            str(self.source),
        )
        self.assertNotEqual(same.returncode, 0)
        self.assertIn("生产 SQLite", same.stderr)

    def test_corrupt_or_incomplete_database_fails_without_metric_or_copy(self) -> None:
        self.source.write_bytes(b"not sqlite")
        result = self._run("snapshot", "--source", str(self.source), "--output", str(self.output))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())

        self.source.unlink()
        self._create_database(self.source, include_events=False)
        verify = self._run("verify", "--snapshot", str(self.source))
        self.assertNotEqual(verify.returncode, 0)
        self.assertIn("缺少关键表", verify.stderr)

    def test_custom_required_table_is_checked(self) -> None:
        result = self._run(
            "verify",
            "--snapshot",
            str(self.source),
            "--required-table",
            "release_plans:id,state",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("release_plans", result.stderr)


if __name__ == "__main__":
    unittest.main()
