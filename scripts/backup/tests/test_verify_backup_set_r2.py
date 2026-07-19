from __future__ import annotations

import datetime as dt
import gzip
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parents[1]
REPO_ROOT = SCRIPT_DIR.parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import backup_manifest

UTC = dt.timezone.utc
IMAGE = os.environ.get("R2_VERIFY_TEST_IMAGE", "python:3.12-slim")


class R2VerifierIntegrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        if shutil.which("docker") is None:
            raise unittest.SkipTest("docker is required for the R2 verifier integration tests")

    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.backup_root = self.root / "backup"
        self.remote_root = self.root / "remote"
        self.remote_data_root = self.remote_root / "test-prefix"
        self.output_root = self.root / "output"
        self.fake_bin = self.root / "fake-bin"
        self.config_root = self.root / "config"
        for path in (
            self.backup_root,
            self.remote_data_root,
            self.output_root,
            self.fake_bin,
            self.config_root,
        ):
            path.mkdir(parents=True)
        self.now = dt.datetime.now(UTC).replace(microsecond=0)
        self._create_required_artifacts()
        self.manifest = backup_manifest.create_manifest(
            backup_manifest.CreateConfig(
                backup_root=self.backup_root,
                manifest_dir=self.backup_root / "manifests",
                metric_out=self.root / "local-metrics" / "backup-set.prom",
                host="LosAngeles",
                now=self.now,
                window_hours=12,
                max_span_hours=3,
            ),
            runtime_inventory=[],
        )
        shutil.copytree(self.backup_root, self.remote_data_root, dirs_exist_ok=True)
        self._write_helpers()

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _create_required_artifacts(self) -> None:
        timestamp = self.now.timestamp() - 1800
        for index, (_, pattern, archive_type) in enumerate(backup_manifest.ARTIFACT_SPECS):
            relative = pattern.replace("*", self.now.strftime("%Y%m%d-%H%M%S"))
            path = self.backup_root / relative
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

    def _write_helpers(self) -> None:
        rclone = self.fake_bin / "rclone"
        rclone.write_text(
            """#!/usr/bin/env bash
set -euo pipefail
[ "$1" = "--config" ]
shift 2
[ "$1" = "copyto" ]
source_path="$2"
destination="$3"
relative="${source_path#*:}"
relative="${relative#*/}"
cp "$FAKE_R2_ROOT/$relative" "$destination"
""",
            encoding="utf-8",
        )
        flock = self.fake_bin / "flock"
        flock.write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
        rclone.chmod(0o755)
        flock.chmod(0o755)
        (self.config_root / "r2-verify.env").write_text(
            "\n".join(
                (
                    "R2_BUCKET=test-bucket",
                    "R2_ENDPOINT=https://example.invalid",
                    "R2_PREFIX=test-prefix",
                    "R2_ACCESS_KEY_ID=test-read-access-key",
                    "R2_SECRET_ACCESS_KEY=test-secret-key",
                    "",
                )
            ),
            encoding="utf-8",
        )
        (self.config_root / "r2-upload.env").write_text(
            "\n".join(
                (
                    "R2_BUCKET=test-bucket",
                    "R2_ENDPOINT=https://example.invalid",
                    "R2_PREFIX=test-prefix",
                    "R2_ACCESS_KEY_ID=test-write-access-key",
                    "R2_SECRET_ACCESS_KEY=test-upload-secret-key",
                    "",
                )
            ),
            encoding="utf-8",
        )

    def _run_verifier(
        self,
        verify_env: str = "/run/r2-verify.env",
        upload_env: str = "/run/r2-upload.env",
    ) -> subprocess.CompletedProcess[str]:
        metric_path = self.output_root / "backup-set-r2-verify.prom"
        command = [
            "docker",
            "run",
            "--rm",
            "-e",
            "PATH=/fake-bin:/usr/local/bin:/usr/bin:/bin",
            "-e",
            "FAKE_R2_ROOT=/remote",
            "-e",
            "BACKUP_ROOT=/backup",
            "-e",
            f"R2_VERIFY_ENV={verify_env}",
            "-e",
            f"R2_BACKUP_ENV={upload_env}",
            "-e",
            "R2_VERIFY_METRIC_OUT=/output/backup-set-r2-verify.prom",
            "-e",
            "R2_VERIFY_STATE_OUT=/output/backup-set-r2-verify.state",
            "-e",
            "R2_VERIFY_LOCK_FILE=/tmp/verify.lock",
            "-v",
            f"{REPO_ROOT}:/repo:ro",
            "-v",
            f"{self.backup_root}:/backup:ro",
            "-v",
            f"{self.remote_root}:/remote:ro",
            "-v",
            f"{self.output_root}:/output",
            "-v",
            f"{self.fake_bin}:/fake-bin:ro",
            "-v",
            f"{self.config_root}:/config:ro",
            IMAGE,
            "/bin/bash",
            "-c",
            "install -o root -g root -m 0600 /config/r2-verify.env /run/r2-verify.env && "
            "install -o root -g root -m 0600 /config/r2-upload.env /run/r2-upload.env && "
            "exec /repo/scripts/backup/verify-backup-set-r2.sh",
        ]
        result = subprocess.run(command, capture_output=True, text=True, timeout=120)
        if result.returncode != 0 and metric_path.exists():
            self.fail("failed R2 verification must not leave a success metric")
        return result

    def test_complete_remote_set_is_downloaded_and_verified(self) -> None:
        result = self._run_verifier()
        self.assertEqual(result.returncode, 0, result.stderr)
        metric = (self.output_root / "backup-set-r2-verify.prom").read_text(encoding="utf-8")
        self.assertIn("backup_set_r2_verify_artifacts 9", metric)
        state = (self.output_root / "backup-set-r2-verify.state").read_text(encoding="utf-8")
        self.assertIn(f"manifest_relative=manifests/{self.manifest.name}", state)
        self.assertRegex(state, r"manifest_sha256=[0-9a-f]{64}")
        self.assertRegex(state, r"verified_at=[0-9]+")

    def test_remote_artifact_corruption_fails_without_success_metric(self) -> None:
        payload = backup_manifest.load_manifest(self.manifest)
        artifact = self.remote_data_root / payload["artifacts"][0]["path"]
        content = bytearray(artifact.read_bytes())
        content[len(content) // 2] ^= 0x01
        artifact.write_bytes(content)
        result = self._run_verifier()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SHA-256 mismatch", result.stderr)

    def test_remote_manifest_sidecar_corruption_fails(self) -> None:
        sidecar = self.remote_data_root / self.manifest.relative_to(self.backup_root)
        sidecar = sidecar.with_suffix(".json.sha256")
        sidecar.write_text(f"{'0' * 64}  {self.manifest.name}\n", encoding="utf-8")
        result = self._run_verifier()
        self.assertNotEqual(result.returncode, 0)

    def test_remote_pointer_must_match_local_complete_set(self) -> None:
        pointer = self.remote_data_root / "manifests" / "latest-manifest.txt"
        pointer.write_text("manifests/backup-set-20000101-000000.json\n", encoding="utf-8")
        result = self._run_verifier()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match the local complete set", result.stderr)

    def test_upload_credential_file_cannot_be_reused_for_verification(self) -> None:
        result = self._run_verifier(upload_env="/run/r2-verify.env")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must not reuse the upload credential file", result.stderr)

    def test_upload_access_key_cannot_be_reused_for_verification(self) -> None:
        upload_config = self.config_root / "r2-upload.env"
        upload_config.write_text(
            upload_config.read_text(encoding="utf-8").replace(
                "test-write-access-key", "test-read-access-key"
            ),
            encoding="utf-8",
        )
        result = self._run_verifier()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must use a distinct access key", result.stderr)

    def test_http_r2_endpoint_is_rejected_before_access(self) -> None:
        verify_config = self.config_root / "r2-verify.env"
        verify_config.write_text(
            verify_config.read_text(encoding="utf-8").replace(
                "R2_ENDPOINT=https://", "R2_ENDPOINT=http://"
            ),
            encoding="utf-8",
        )
        result = self._run_verifier()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("R2_ENDPOINT must be an HTTPS origin", result.stderr)


if __name__ == "__main__":
    unittest.main()
