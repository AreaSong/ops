from __future__ import annotations

import json
import os
import shutil
import sqlite3
from pathlib import Path
from unittest.mock import patch

from release_snapshot import ACTIVE_STATES, inspect_database
from release_test_support import MODULE, ReleaseFixture


class ReleaseSnapshotTests(ReleaseFixture):
    def capture(self):
        return self.orchestrator.snapshot.capture({"Image": "sha256:" + "d" * 64}, 47)

    def test_real_uncheckpointed_wal_is_not_replayed_into_restored_database(self):
        self.capture()
        source = self.root / "crashed.db"
        shutil.copy2(self.state.directory / "backup/ops.db", source)
        connection = sqlite3.connect(source)
        try:
            connection.execute("PRAGMA journal_mode=WAL")
            connection.execute("PRAGMA wal_autocheckpoint=0")
            connection.execute("PRAGMA user_version=47")
            connection.execute("INSERT INTO app_state VALUES ('new-wal-record')")
            connection.commit()
            for suffix in ("", "-wal", "-shm"):
                shutil.copy2(Path(str(source) + suffix), Path(str(self.args.db_path) + suffix))
        finally:
            connection.close()
        wal = Path(str(self.args.db_path) + "-wal")
        self.assertGreater(wal.stat().st_size, 32)
        self.orchestrator.snapshot.restore()
        self.assertFalse(wal.exists())
        self.assertTrue((self.state.directory / "before-rollback/ops.db-wal.original").is_file())
        self.assertEqual(self.schema(), 45)
        with sqlite3.connect(self.args.db_path) as database:
            self.assertEqual(database.execute("SELECT value FROM app_state").fetchall(), [("old",)])

    def test_sidecar_symlink_prevents_any_restore_write(self):
        self.capture()
        outside = self.root / "outside"
        outside.write_text("not-a-sidecar")
        Path(str(self.args.db_path) + "-wal").symlink_to(outside)
        before = self.args.db_path.read_bytes()
        with self.assertRaises(MODULE.ReleaseError):
            self.orchestrator.snapshot.restore()
        self.assertEqual(self.args.db_path.read_bytes(), before)
        self.assertFalse((self.state.directory / "before-rollback").exists())
        self.assertEqual(outside.read_text(), "not-a-sidecar")

    def test_restore_preserves_parent_modes_and_file_permissions(self):
        self.root.chmod(0o710)
        original = self.args.unit_path.stat().st_mode & 0o777
        self.capture()
        self.args.unit_path.chmod(0o600)
        self.orchestrator.snapshot.restore()
        self.assertEqual(self.root.stat().st_mode & 0o777, 0o710)
        self.assertEqual(self.args.unit_path.parent.stat().st_mode & 0o777, 0o755)
        self.assertEqual(self.args.unit_path.stat().st_mode & 0o777, original)

    def test_database_write_failure_does_not_restore_legacy_entrypoint(self):
        self.capture()
        runner = self.args.runner_root / "runner/areasong-ops-runner"
        runner.write_text("new-runner")
        from release_snapshot import Snapshot
        original = Snapshot.restore_file

        def fail_database(source, target, evidence):
            if target == self.args.db_path:
                raise OSError("injected DB restore failure")
            return original(source, target, evidence)

        with patch.object(Snapshot, "restore_file", side_effect=fail_database), self.assertRaises(OSError):
            self.orchestrator.snapshot.restore()
        self.assertEqual(runner.read_text(), "new-runner")

    def test_entry_switch_boundary_is_after_schema_restore(self):
        self.capture()
        with sqlite3.connect(self.args.db_path) as database:
            database.execute("PRAGMA user_version=47")
        runner = self.args.runner_root / "runner/areasong-ops-runner"
        runner.write_text("new-runner")

        def boundary():
            self.assertEqual(self.schema(), 45)
            self.assertEqual(runner.read_text(), "new-runner")

        self.orchestrator.snapshot.restore(boundary)
        self.assertEqual(runner.read_text(), "old-runner")

    def test_backup_corruption_and_external_config_drift_are_rejected(self):
        self.capture()
        config = self.args.config_dir / "services.json"
        config.write_text('{"external":"change"}')
        before = self.args.db_path.read_bytes()
        with self.assertRaisesRegex(MODULE.ReleaseError, "范围外"):
            self.orchestrator.snapshot.restore()
        self.assertEqual(self.args.db_path.read_bytes(), before)

    def test_every_active_state_blocks_and_missing_table_is_not_idle(self):
        for table, states in ACTIVE_STATES.items():
            for state in states:
                with self.subTest(table=table, state=state):
                    with sqlite3.connect(self.args.db_path) as database:
                        database.execute(f"INSERT INTO {table} (state) VALUES (?)", (state,))
                    with self.assertRaisesRegex(MODULE.ReleaseError, "未收口"):
                        inspect_database(self.args.db_path, 47)
                    with sqlite3.connect(self.args.db_path) as database:
                        database.execute(f"DELETE FROM {table}")
        with sqlite3.connect(self.args.db_path) as database:
            database.execute("DROP TABLE task_assignments")
        with self.assertRaisesRegex(MODULE.ReleaseError, "无法证明"):
            inspect_database(self.args.db_path, 47)

    def test_unknown_schema_is_rejected(self):
        with sqlite3.connect(self.args.db_path) as database:
            database.execute("PRAGMA user_version=999")
        with self.assertRaisesRegex(MODULE.ReleaseError, "升级范围"):
            inspect_database(self.args.db_path, 47)

    def test_state_parent_symlink_is_rejected_before_external_commands(self):
        original = self.args.state_dir
        moved = self.root / "moved"
        original.rename(moved)
        original.symlink_to(moved, target_is_directory=True)
        with self.assertRaises((MODULE.ReleaseError, OSError)):
            self.deploy()
        self.assertEqual(self.commands, [])
