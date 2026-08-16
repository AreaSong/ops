from __future__ import annotations

import importlib.util
import os
import sys
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
MODULE_PATH = REPO_ROOT / "services" / "areasong-ops" / "deploy" / "migrate_github_credential.py"
SPEC = importlib.util.spec_from_file_location("migrate_github_credential", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("cannot load credential migration module")
MIGRATION = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MIGRATION
SPEC.loader.exec_module(MIGRATION)


class GitHubCredentialMigrationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        lock_root = root / "locks"
        lock_root.mkdir(mode=0o700)
        self.source = root / "legacy.env"
        self.destination = root / "credentials" / "managed.env"
        paths = MIGRATION.MigrationPaths(
            source=self.source,
            destination=self.destination,
            legacy_lock=lock_root / "legacy.lock",
            managed_lock=lock_root / "managed.lock",
        )
        self.migrator = MIGRATION.CredentialMigrator(paths, require_root=False)

    def write_source(self, content: bytes) -> None:
        self.source.write_bytes(content)
        self.source.chmod(0o600)

    def legacy_config(self, token: str = "legacy-token-test-abcdefghijklmnopqrstuvwxyz") -> bytes:
        return (
            "ALERTMANAGER_GITHUB_ISSUES_ENABLED=true\n"
            "GITHUB_REPOSITORY=AreaSong/ops\n"
            f"GITHUB_TOKEN={token}\n"
            "GITHUB_TOKEN_EXPIRES_AT=2027-08-12\n"
        ).encode()

    def test_apply_normalizes_legacy_four_keys_and_is_idempotent(self) -> None:
        self.write_source(self.legacy_config())
        self.assertTrue(self.migrator.apply())
        self.assertEqual(self.destination.read_bytes(), MIGRATION.render_config(
            MIGRATION.parse_config(self.legacy_config())
        ))
        self.assertEqual(self.destination.stat().st_mode & 0o777, 0o600)
        self.assertFalse(self.migrator.apply())
        self.migrator.validate_destination()

    def test_apply_accepts_canonical_source(self) -> None:
        legacy = MIGRATION.parse_config(self.legacy_config())
        canonical = MIGRATION.render_config(legacy)
        self.write_source(canonical)
        self.assertTrue(self.migrator.apply())
        self.assertEqual(self.destination.read_bytes(), canonical)

    def test_existing_different_destination_is_never_overwritten(self) -> None:
        self.write_source(self.legacy_config("legacy-token-source-abcdefghijklmnopqrstuvwxyz"))
        self.destination.parent.mkdir(mode=0o700)
        original = MIGRATION.render_config(MIGRATION.parse_config(
            self.legacy_config("legacy-token-existing-abcdefghijklmnopqrstuvwxyz")
        ))
        self.destination.write_bytes(original)
        self.destination.chmod(0o600)
        with self.assertRaisesRegex(MIGRATION.MigrationError, "拒绝覆盖"):
            self.migrator.apply()
        self.assertEqual(self.destination.read_bytes(), original)

    def test_unknown_duplicate_and_invalid_fixed_keys_are_rejected(self) -> None:
        cases = (
            self.legacy_config() + b"UNEXPECTED=value\n",
            self.legacy_config() + b"GITHUB_REPOSITORY=AreaSong/ops\n",
            self.legacy_config().replace(b"GITHUB_REPOSITORY=AreaSong/ops", b"GITHUB_REPOSITORY=other/repo"),
        )
        for content in cases:
            with self.subTest(content_length=len(content)):
                self.write_source(content)
                with self.assertRaises(MIGRATION.MigrationError):
                    self.migrator.validate_source()

    def test_unsafe_source_mode_and_symlink_are_rejected(self) -> None:
        self.write_source(self.legacy_config())
        self.source.chmod(0o640)
        with self.assertRaisesRegex(MIGRATION.MigrationError, "0600"):
            self.migrator.validate_source()
        self.source.unlink()
        target = self.source.with_name("target.env")
        target.write_bytes(self.legacy_config())
        target.chmod(0o600)
        os.symlink(target, self.source)
        with self.assertRaisesRegex(MIGRATION.MigrationError, "普通文件"):
            self.migrator.validate_source()


if __name__ == "__main__":
    unittest.main()
