import os
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import areasong_ops_snapshot as module
from areasong_ops_fixtures import create_database


class SnapshotContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "snapshot.db"
        create_database(self.source, 47)

    def test_known_real_schemas(self):
        for version in (5, 45, 47):
            create_database(self.source, version)
            with self.subTest(version=version):
                module.inspect_database(self.source)

    def test_legacy_schema_is_restore_only(self):
        create_database(self.source, 4)
        module.inspect_database(self.source, allow_legacy=True)
        with self.assertRaisesRegex(ValueError, "schema"):
            module.inspect_database(self.source)

    def test_unknown_schema_and_mislabeled_latest_are_rejected(self):
        for version in (3, 46, 48, 999):
            with sqlite3.connect(self.source) as connection:
                connection.execute(f"PRAGMA user_version={version}")
            with self.subTest(version=version), self.assertRaisesRegex(ValueError, "schema"):
                module.inspect_database(self.source)
        create_database(self.source, 45)
        with sqlite3.connect(self.source) as connection:
            connection.execute("PRAGMA user_version=47")
        with self.assertRaisesRegex(ValueError, "关键列"):
            module.inspect_database(self.source)

    def test_sidecars_and_symlinks_are_rejected(self):
        for suffix in ("-wal", "-shm", "-journal"):
            sidecar = Path(str(self.source) + suffix)
            sidecar.touch()
            with self.subTest(suffix=suffix), self.assertRaisesRegex(ValueError, "自包含"):
                module.inspect_database(self.source)
            sidecar.unlink()
        alias = self.root / "link.db"
        alias.symlink_to(self.source)
        with self.assertRaisesRegex(ValueError, "普通文件"):
            module.inspect_database(alias)

    def test_copy_preserves_existing_destination_and_source(self):
        destination = self.root / "output.db"
        destination.write_bytes(b"existing")
        original = self.source.read_bytes()
        with self.assertRaises(FileExistsError):
            module.copy_snapshot(self.source, destination, 90000)
        self.assertEqual(destination.read_bytes(), b"existing")
        self.assertEqual(self.source.read_bytes(), original)

    def test_invalid_copy_is_removed_without_touching_source(self):
        self.source.write_bytes(b"corrupt")
        destination = self.root / "output.db"
        with self.assertRaises(sqlite3.Error):
            module.copy_snapshot(self.source, destination, 90000)
        self.assertEqual(self.source.read_bytes(), b"corrupt")
        self.assertFalse(destination.exists())

    def test_future_snapshot_is_rejected(self):
        info = self.source.stat()
        os.utime(self.source, (info.st_atime, info.st_mtime + 3600))
        with self.assertRaisesRegex(ValueError, "window"):
            module.copy_snapshot(self.source, self.root / "output.db", 90000)


if __name__ == "__main__":
    unittest.main()
