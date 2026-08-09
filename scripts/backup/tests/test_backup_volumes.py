from __future__ import annotations

import os
import sqlite3
import subprocess
import tarfile
import tempfile
import time
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "scripts" / "backup" / "backup-volumes.sh"
TABLE_COLUMNS = {
    "previews": ("id", "actor_hash", "service", "action", "confirmation_hash", "created_at", "expires_at"),
    "tasks": ("id", "idempotency_key", "request_hash", "actor_hash", "service", "action", "state", "preview_id", "snapshot_json", "created_at"),
    "events": ("sequence", "task_id", "occurred_at", "level", "message", "data_json"),
    "audit_entries": ("sequence", "occurred_at", "actor_hash", "event", "resource", "outcome", "detail_json"),
    "metadata": ("key", "value"),
}


class BackupVolumesTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.state = self.root / "state"
        self.snapshots = self.state / "snapshots"
        self.operations = self.state / "operations" / "task-safe"
        self.snapshots.mkdir(parents=True)
        self.operations.mkdir(parents=True)
        (self.operations / "task-contract.json").write_text("{}\n", encoding="utf-8")
        self.snapshot = self.snapshots / "ops-20260809T010203Z.db"
        self._create_database(self.snapshot)
        self.fake_bin = self.root / "bin"
        self.fake_bin.mkdir()
        docker = self.fake_bin / "docker"
        docker.write_text("#!/usr/bin/env bash\nexit 1\n", encoding="utf-8")
        docker.chmod(0o755)

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    @staticmethod
    def _create_database(path: Path, omitted: tuple[str, str] | None = None) -> None:
        path.unlink(missing_ok=True)
        connection = sqlite3.connect(path)
        try:
            for table, names in TABLE_COLUMNS.items():
                columns = [name for name in names if omitted != (table, name)]
                definition = ", ".join(f'"{name}" TEXT' for name in columns)
                connection.execute(f'CREATE TABLE "{table}"({definition})')
            connection.commit()
        finally:
            connection.close()

    @staticmethod
    def _add_foreign_key_violation(path: Path) -> None:
        connection = sqlite3.connect(path)
        try:
            connection.execute("PRAGMA foreign_keys = OFF")
            connection.execute("CREATE TABLE fk_parent(id INTEGER PRIMARY KEY)")
            connection.execute("CREATE TABLE fk_child(parent_id INTEGER REFERENCES fk_parent(id))")
            connection.execute("INSERT INTO fk_child(parent_id) VALUES (1)")
            connection.commit()
        finally:
            connection.close()

    def _run(self, max_age: int = 90000) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "OPS_BACKUP_JOB_WRAPPED": "1",
                "BACKUP_VOLUME_BACKUP_ROOT": str(self.root / "backups"),
                "BACKUP_VOLUME_LOG_DIR": str(self.root / "logs"),
                "BACKUP_AREASONG_OPS_STATE_ROOT": str(self.state),
                "BACKUP_AREASONG_OPS_SNAPSHOT_MAX_AGE_SECONDS": str(max_age),
                "PATH": f"{self.fake_bin}:{environment['PATH']}",
            }
        )
        return subprocess.run(
            [str(SCRIPT)], env=environment, text=True, capture_output=True,
            check=False, timeout=20,
        )

    def test_archives_valid_snapshot_and_operation_evidence(self) -> None:
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stderr)
        archive_path = next((self.root / "backups").glob("areasong-ops-state-*.tar.gz"))
        with tarfile.open(archive_path, "r:gz") as archive:
            names = set(archive.getnames())
            self.assertIn("areasong-ops-state/ops.db", names)
            self.assertIn("areasong-ops-state/operations/task-safe/task-contract.json", names)
            restored = self.root / "restored.db"
            restored.write_bytes(archive.extractfile("areasong-ops-state/ops.db").read())
        connection = sqlite3.connect(restored)
        try:
            self.assertEqual(connection.execute("PRAGMA integrity_check").fetchone(), ("ok",))
        finally:
            connection.close()

    def test_rejects_stale_snapshot(self) -> None:
        old = time.time() - 120
        os.utime(self.snapshot, (old, old))
        result = self._run(max_age=60)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("outside the allowed window", result.stderr)
        self.assertEqual(list((self.root / "backups").glob("areasong-ops-state-*.tar.gz")), [])

    def test_rejects_symlink_in_operation_evidence(self) -> None:
        (self.operations / "unsafe").symlink_to(self.root / "outside")
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("contains a symbolic link", result.stderr)

    def test_rejects_snapshot_missing_critical_column(self) -> None:
        self._create_database(self.snapshot, omitted=("metadata", "value"))
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("metadata is missing required columns: value", result.stderr)
        self.assertEqual(list((self.root / "backups").glob("areasong-ops-state-*.tar.gz")), [])

    def test_rejects_snapshot_with_foreign_key_violation(self) -> None:
        self._add_foreign_key_violation(self.snapshot)
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("foreign_key_check failed", result.stderr)
        self.assertEqual(list((self.root / "backups").glob("areasong-ops-state-*.tar.gz")), [])


if __name__ == "__main__":
    unittest.main()
