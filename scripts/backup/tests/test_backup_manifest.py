from __future__ import annotations

import datetime as dt
import gzip
import io
import json
import os
import stat
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest import mock

SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import backup_manifest

UTC = dt.timezone.utc


class BackupManifestTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.now = dt.datetime(2026, 7, 16, 4, 0, tzinfo=UTC)
        self._create_required_artifacts()

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _create_required_artifacts(self) -> None:
        timestamp = self.now.timestamp() - 1800
        for index, (_, pattern, archive_type) in enumerate(backup_manifest.ARTIFACT_SPECS):
            relative = pattern.replace("*", "20260716-033000")
            path = self.root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            if archive_type == "gzip":
                with gzip.open(path, "wb") as handle:
                    handle.write(b"CREATE DATABASE example;\n")
            else:
                source = self.root / f"source-{index}.txt"
                source.write_text(f"artifact {index}\n", encoding="utf-8")
                with tarfile.open(path, "w:gz") as archive:
                    archive.add(source, arcname=f"data/file-{index}.txt")
            os.utime(path, (timestamp + index, timestamp + index))

    def config(self) -> backup_manifest.CreateConfig:
        return backup_manifest.CreateConfig(
            backup_root=self.root,
            manifest_dir=self.root / "manifests",
            metric_out=self.root / "metrics" / "backup-set.prom",
            host="LosAngeles",
            now=self.now,
            window_hours=12,
            max_span_hours=3,
        )

    def test_create_and_verify_complete_manifest(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        records = backup_manifest.verify_manifest(self.root, manifest_path)
        self.assertEqual(len(records), len(backup_manifest.ARTIFACT_SPECS))
        self.assertTrue(manifest_path.with_suffix(".json.sha256").is_file())
        latest = (self.root / "manifests" / "latest-manifest.txt").read_text(encoding="utf-8").strip()
        self.assertEqual(latest, f"manifests/{manifest_path.name}")
        metrics = (self.root / "metrics" / "backup-set.prom").read_text(encoding="utf-8")
        self.assertIn("backup_set_artifacts 9", metrics)
        self.assertEqual(stat.S_IMODE(manifest_path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(manifest_path.with_suffix(".json.sha256").stat().st_mode), 0o600)
        self.assertEqual(
            stat.S_IMODE((self.root / "manifests" / "latest-manifest.txt").stat().st_mode),
            0o600,
        )
        self.assertEqual(stat.S_IMODE((self.root / "metrics" / "backup-set.prom").stat().st_mode), 0o644)

    def test_verify_detects_artifact_corruption(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        payload = backup_manifest.load_manifest(manifest_path)
        artifact = self.root / payload["artifacts"][0]["path"]
        artifact.write_bytes(artifact.read_bytes() + b"corrupt")
        with self.assertRaisesRegex(ValueError, "size mismatch"):
            backup_manifest.verify_manifest(self.root, manifest_path)

    def test_verify_detects_same_size_artifact_corruption(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        payload = backup_manifest.load_manifest(manifest_path)
        artifact = self.root / payload["artifacts"][0]["path"]
        content = bytearray(artifact.read_bytes())
        content[len(content) // 2] ^= 0x01
        artifact.write_bytes(content)
        with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
            backup_manifest.verify_manifest(self.root, manifest_path)

    def test_verify_detects_manifest_sidecar_corruption(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        manifest_path.write_text(manifest_path.read_text(encoding="utf-8") + " ", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "manifest SHA-256 mismatch"):
            backup_manifest.verify_manifest(self.root, manifest_path)

    def test_create_fails_when_required_role_is_missing(self) -> None:
        missing = next(self.root.glob("redis/redis-*.tar.gz"))
        missing.unlink()
        with self.assertRaisesRegex(ValueError, "required backup artifact"):
            backup_manifest.create_manifest(self.config(), runtime_inventory=[])

    def test_verify_rejects_path_traversal_even_with_updated_sidecar(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        payload = backup_manifest.load_manifest(manifest_path)
        payload["artifacts"][0]["path"] = "../outside.sql.gz"
        manifest_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        digest = backup_manifest.sha256_file(manifest_path)
        manifest_path.with_suffix(".json.sha256").write_text(
            f"{digest}  {manifest_path.name}\n", encoding="utf-8"
        )
        with self.assertRaisesRegex(ValueError, "unsafe artifact path"):
            backup_manifest.verify_manifest(self.root, manifest_path)

    def test_verify_rejects_duplicate_roles_even_with_updated_sidecar(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        payload = backup_manifest.load_manifest(manifest_path)
        payload["artifacts"][1]["role"] = payload["artifacts"][0]["role"]
        manifest_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        digest = backup_manifest.sha256_file(manifest_path)
        manifest_path.with_suffix(".json.sha256").write_text(
            f"{digest}  {manifest_path.name}\n", encoding="utf-8"
        )
        with self.assertRaisesRegex(ValueError, "exact required artifact roles"):
            backup_manifest.verify_manifest(self.root, manifest_path)

    def test_verify_rejects_role_archive_type_tamper(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        payload = backup_manifest.load_manifest(manifest_path)
        payload["artifacts"][3]["archive_type"] = "gzip"
        manifest_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        digest = backup_manifest.sha256_file(manifest_path)
        manifest_path.with_suffix(".json.sha256").write_text(
            f"{digest}  {manifest_path.name}\n", encoding="utf-8"
        )
        with self.assertRaisesRegex(ValueError, "archive type mismatch"):
            backup_manifest.verify_manifest(self.root, manifest_path)

    def test_verify_rejects_role_path_tamper(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        payload = backup_manifest.load_manifest(manifest_path)
        payload["artifacts"][2]["path"] = payload["artifacts"][0]["path"]
        manifest_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        digest = backup_manifest.sha256_file(manifest_path)
        manifest_path.with_suffix(".json.sha256").write_text(
            f"{digest}  {manifest_path.name}\n", encoding="utf-8"
        )
        with self.assertRaisesRegex(ValueError, "path does not match role"):
            backup_manifest.verify_manifest(self.root, manifest_path)

    def test_safe_extract_tar_extracts_only_requested_regular_files(self) -> None:
        archive_path = self.root / "selected.tar.gz"
        selected_source = self.root / "selected.txt"
        ignored_source = self.root / "ignored.txt"
        selected_source.write_text("selected\n", encoding="utf-8")
        ignored_source.write_text("ignored\n", encoding="utf-8")
        with tarfile.open(archive_path, "w:gz") as archive:
            archive.add(selected_source, arcname="opt/areaforge/docker-compose.prod.yml")
            archive.add(ignored_source, arcname="etc/ignored.txt")

        destination = self.root / "extract-selected"
        backup_manifest.safe_extract_tar(
            archive_path,
            destination,
            {"opt/areaforge/docker-compose.prod.yml"},
        )
        extracted = destination / "opt/areaforge/docker-compose.prod.yml"
        self.assertEqual(extracted.read_text(encoding="utf-8"), "selected\n")
        self.assertFalse((destination / "etc/ignored.txt").exists())
        self.assertEqual(stat.S_IMODE(extracted.stat().st_mode), 0o600)

    def test_safe_extract_tar_rejects_link_members(self) -> None:
        archive_path = self.root / "link.tar.gz"
        with tarfile.open(archive_path, "w:gz") as archive:
            link = tarfile.TarInfo("data/link")
            link.type = tarfile.SYMTYPE
            link.linkname = "../../outside"
            archive.addfile(link)

        with self.assertRaisesRegex(ValueError, "link member"):
            backup_manifest.safe_extract_tar(archive_path, self.root / "extract-link")

    def test_safe_extract_tar_enforces_unpacked_byte_limit(self) -> None:
        archive_path = self.root / "large.tar.gz"
        source = self.root / "large.txt"
        source.write_bytes(b"x" * 32)
        with tarfile.open(archive_path, "w:gz") as archive:
            archive.add(source, arcname="data/large.txt")
        with self.assertRaisesRegex(ValueError, "byte limit exceeded"):
            backup_manifest.safe_extract_tar(
                archive_path,
                self.root / "extract-large",
                max_bytes=16,
            )

    def test_validate_archive_enforces_independent_member_limit(self) -> None:
        archive_path = self.root / "many-members.tar.gz"
        first = self.root / "first.txt"
        second = self.root / "second.txt"
        first.write_text("first\n", encoding="utf-8")
        second.write_text("second\n", encoding="utf-8")
        with tarfile.open(archive_path, "w:gz") as archive:
            archive.add(first, arcname="data/first.txt")
            archive.add(second, arcname="data/second.txt")
        with mock.patch.object(backup_manifest, "MAX_ARCHIVE_MEMBERS", 1):
            with self.assertRaisesRegex(ValueError, "member limit exceeded"):
                backup_manifest.validate_archive(archive_path, "tar")

    def test_validate_archive_rejects_member_below_archive_link(self) -> None:
        archive_path = self.root / "link-traversal.tar.gz"
        payload = b"outside\n"
        with tarfile.open(archive_path, "w:gz") as archive:
            link = tarfile.TarInfo("data")
            link.type = tarfile.SYMTYPE
            link.linkname = "../../outside"
            archive.addfile(link)
            nested = tarfile.TarInfo("data/file.txt")
            nested.size = len(payload)
            archive.addfile(nested, io.BytesIO(payload))

        with self.assertRaisesRegex(ValueError, "traverses an archive link"):
            backup_manifest.validate_archive(archive_path, "tar")

    def test_list_artifacts_filters_roles(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        paths = backup_manifest.list_artifacts(manifest_path, {"postgres-areaforge", "configs"})
        self.assertEqual(len(paths), 2)
        self.assertTrue(any(path.startswith("postgres/areaforge-postgres-") for path in paths))

    def test_verify_can_check_selected_roles_from_a_complete_manifest(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        selected = {"postgres-areaforge", "configs"}
        records = backup_manifest.verify_manifest(self.root, manifest_path, selected)
        self.assertEqual({record.role for record in records}, selected)

    def test_runtime_container_field_returns_recorded_image(self) -> None:
        runtime = [{
            "name": "areaforge-postgres",
            "configured_image": "postgres:16-alpine",
            "image_id": "sha256:recorded",
        }]
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=runtime)
        self.assertEqual(
            backup_manifest.runtime_container_field(manifest_path, "areaforge-postgres", "image_id"),
            "sha256:recorded",
        )

    def test_runtime_container_field_rejects_missing_container(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        with self.assertRaisesRegex(ValueError, "exactly one runtime entry"):
            backup_manifest.runtime_container_field(manifest_path, "areaforge-postgres", "image_id")

    def test_artifact_field_returns_recorded_unpack_size(self) -> None:
        manifest_path = backup_manifest.create_manifest(self.config(), runtime_inventory=[])
        value = backup_manifest.artifact_field(
            manifest_path,
            "volume-areaforge-uploads",
            "unpacked_size_bytes",
        )
        self.assertGreater(int(value), 0)


if __name__ == "__main__":
    unittest.main()
