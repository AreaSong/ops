from __future__ import annotations

import datetime as dt
import gzip
import hashlib
import io
import json
import os
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import backup_manifest


UTC = dt.timezone.utc
RESTORE_SCRIPT = SCRIPT_DIR / "restore_areasong_ops_isolated.py"
TABLE_COLUMNS = {
    "previews": ("id", "actor_hash", "service", "action", "confirmation_hash", "created_at", "expires_at"),
    "tasks": ("id", "idempotency_key", "request_hash", "actor_hash", "service", "action", "state", "preview_id", "snapshot_json", "created_at"),
    "events": ("sequence", "task_id", "occurred_at", "level", "message", "data_json"),
    "audit_entries": ("sequence", "occurred_at", "actor_hash", "event", "resource", "outcome", "detail_json"),
    "credential_rotations": ("id", "actor_hash", "credential_type", "target", "state", "fingerprint", "expires_at", "created_at"),
    "metadata": ("key", "value"),
}


class RestoreAreaSongOpsIsolatedTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.backup_root = self.root / "backups"
        self.work_root = self.root / "work"
        self.metric_out = self.root / "metrics" / "restore.prom"
        self.lock_file = self.root / "run" / "restore.lock"
        self.database = self.root / "ops.db"
        self._create_database(self.database)
        self.manifest = self._create_backup_set()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    @staticmethod
    def _create_database(path: Path, tables: tuple[str, ...] = (
        "previews", "tasks", "events", "audit_entries", "credential_rotations", "metadata",
    ), omitted: tuple[str, str] | None = None, schema_version: int = 5) -> None:
        path.unlink(missing_ok=True)
        connection = sqlite3.connect(path)
        try:
            for table in tables:
                columns = [name for name in TABLE_COLUMNS[table] if omitted != (table, name)]
                definition = ", ".join(f'"{name}" TEXT' for name in columns)
                connection.execute(f'CREATE TABLE "{table}"({definition})')
            if "metadata" in tables:
                connection.execute("INSERT INTO metadata(key, value) VALUES ('schema', '1')")
            connection.execute(f"PRAGMA user_version = {schema_version}")
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

    @staticmethod
    def _write_tar(path: Path, members: dict[str, bytes]) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(path, "w:gz") as archive:
            for name, payload in members.items():
                info = tarfile.TarInfo(name)
                info.size = len(payload)
                archive.addfile(info, io.BytesIO(payload))

    def _create_backup_set(self) -> Path:
        now = dt.datetime.now(UTC)
        timestamp = now.timestamp() - 60
        for index, (role, pattern, archive_type) in enumerate(backup_manifest.ARTIFACT_SPECS):
            path = self.backup_root / pattern.replace("*", "20260809-010203")
            path.parent.mkdir(parents=True, exist_ok=True)
            if archive_type == "gzip":
                with gzip.open(path, "wb") as handle:
                    handle.write(b"fixture\n")
            elif role == "volume-areasong-ops-state":
                self._write_tar(path, {
                    "areasong-ops-state/ops.db": self.database.read_bytes(),
                    "areasong-ops-state/operations/task/contract.json": b"{}\n",
                })
            else:
                self._write_tar(path, {f"fixture/file-{index}.txt": b"fixture\n"})
            os.utime(path, (timestamp + index, timestamp + index))
        return backup_manifest.create_manifest(
            backup_manifest.CreateConfig(
                backup_root=self.backup_root,
                manifest_dir=self.backup_root / "manifests",
                metric_out=self.root / "create.prom",
                host="LosAngeles",
                now=now,
                window_hours=12,
                max_span_hours=3,
            ),
            runtime_inventory=[],
        )

    def _run(self) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable,
                str(RESTORE_SCRIPT),
                "--backup-root", str(self.backup_root),
                "--manifest", self.manifest.relative_to(self.backup_root).as_posix(),
                "--work-root", str(self.work_root),
                "--metric-out", str(self.metric_out),
                "--lock-file", str(self.lock_file),
            ],
            check=False,
            capture_output=True,
            text=True,
            timeout=20,
        )

    def test_restores_database_and_publishes_metrics(self) -> None:
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        metrics = self.metric_out.read_text(encoding="utf-8")
        self.assertIn("areasong_ops_restore_drill_last_success_timestamp_seconds", metrics)
        self.assertIn('areasong_ops_restore_drill_table_rows{table="metadata"} 1', metrics)
        self.assertEqual(list(self.work_root.glob("areasong-ops-restore-*")), [])

    def test_restores_pre_stage6_schema_without_credential_table(self) -> None:
        self._create_database(
            self.database,
            ("previews", "tasks", "events", "audit_entries", "metadata"),
            schema_version=4,
        )
        self.manifest = self._create_backup_set()
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("credential_rotations", self.metric_out.read_text(encoding="utf-8"))

    def test_rejects_unknown_schema_version(self) -> None:
        self._create_database(self.database, schema_version=3)
        self.manifest = self._create_backup_set()
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("schema 版本不受支持", result.stderr)

    def test_incomplete_manifest_is_rejected_and_workdir_is_not_created(self) -> None:
        payload = json.loads(self.manifest.read_text(encoding="utf-8"))
        payload["artifacts"] = payload["artifacts"][:-1]
        self.manifest.write_text(json.dumps(payload), encoding="utf-8")
        sidecar = self.manifest.with_suffix(".json.sha256")
        sidecar.write_text(
            f"{hashlib.sha256(self.manifest.read_bytes()).hexdigest()}  {self.manifest.name}\n",
            encoding="utf-8",
        )
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exact required artifact roles", result.stderr)
        self.assertFalse(self.work_root.exists())

    def test_missing_critical_table_fails_and_cleans_workdir(self) -> None:
        self._create_database(self.database, ("previews", "tasks", "events", "audit_entries"))
        self.manifest = self._create_backup_set()
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("缺少关键表", result.stderr)
        self.assertEqual(list(self.work_root.glob("areasong-ops-restore-*")), [])
        self.assertFalse(self.metric_out.exists())

    def test_invalid_sqlite_preserves_previous_success_metric(self) -> None:
        self.database.write_bytes(b"not a sqlite database")
        self.manifest = self._create_backup_set()
        self.metric_out.parent.mkdir(parents=True)
        self.metric_out.write_text("previous-success\n", encoding="utf-8")
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.metric_out.read_text(encoding="utf-8"), "previous-success\n")
        self.assertEqual(list(self.work_root.glob("areasong-ops-restore-*")), [])

    def test_missing_critical_column_fails_and_preserves_previous_metric(self) -> None:
        self._create_database(self.database, omitted=("tasks", "snapshot_json"))
        self.manifest = self._create_backup_set()
        self.metric_out.parent.mkdir(parents=True)
        self.metric_out.write_text("previous-success\n", encoding="utf-8")
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("表 tasks 缺少关键列: snapshot_json", result.stderr)
        self.assertEqual(self.metric_out.read_text(encoding="utf-8"), "previous-success\n")
        self.assertEqual(list(self.work_root.glob("areasong-ops-restore-*")), [])

    def test_foreign_key_violation_fails_and_preserves_previous_metric(self) -> None:
        self._add_foreign_key_violation(self.database)
        self.manifest = self._create_backup_set()
        self.metric_out.parent.mkdir(parents=True)
        self.metric_out.write_text("previous-success\n", encoding="utf-8")
        result = self._run()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("外键不一致", result.stderr)
        self.assertEqual(self.metric_out.read_text(encoding="utf-8"), "previous-success\n")
        self.assertEqual(list(self.work_root.glob("areasong-ops-restore-*")), [])


if __name__ == "__main__":
    unittest.main()
