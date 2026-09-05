from __future__ import annotations

import hashlib
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


DEPLOY_DIR = Path(__file__).resolve().parents[1]
VERIFIER = DEPLOY_DIR / "verify-release-assets.sh"


class ReleaseAssetContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.archive = self.root / ("areasong-ops-runner-" + "a" * 40 + "-linux-amd64.tar.gz")
        self.archive.write_bytes(b"signed runner bundle")
        self.digest = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        self.checksum = self.root / f"{self.archive.name}.sha256"
        self.checksum.write_text(f"{self.digest}  {self.archive.name}\n", encoding="utf-8")
        self.bundle = self.root / f"{self.archive.name}.sigstore.json"
        self.bundle.write_text("{}\n", encoding="utf-8")
        self.cosign_log = self.root / "cosign.log"
        self.fake_bin = self.root / "bin"
        self.fake_bin.mkdir()
        fake_cosign = self.fake_bin / "cosign"
        fake_cosign.write_text(
            """#!/bin/sh
printf '%s\\n' "$*" >>"$FAKE_COSIGN_LOG"
if [ "${FAKE_COSIGN_FAIL_MODE:-}" = "$1" ]; then
  exit 1
fi
exit 0
""",
            encoding="utf-8",
        )
        fake_cosign.chmod(0o755)
        self.manifest = self.root / "manifest.json"
        self.write_manifest(f"sha256:{self.digest}")

    def write_manifest(
        self,
        digest: str,
        archive: str | None = None,
        *,
        schema_version: int = 2,
        revision: str | None = None,
        web_image: str | None = None,
    ) -> None:
        manifest_revision = revision or "a" * 40
        self.manifest.write_text(
            json.dumps(
                {
                    "schemaVersion": schema_version,
                    "service": "areasong-ops",
                    "version": "1.1.1",
                    "revision": manifest_revision,
                    "platform": "linux/amd64",
                    "web": {
                        "image": web_image
                        or f"ghcr.io/areasong/areasong-ops-web:{manifest_revision}@sha256:" + "b" * 64,
                        "cosign": "keyless",
                    },
                    "runner": {
                        "archive": archive or self.archive.name,
                        "sha256": digest,
                        "cosign": "keyless",
                    },
                }
            ),
            encoding="utf-8",
        )

    def verify(self, *, cosign_fail_mode: str = "") -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env["PATH"] = f"{self.fake_bin}{os.pathsep}{env['PATH']}"
        env["FAKE_COSIGN_LOG"] = str(self.cosign_log)
        env["FAKE_COSIGN_FAIL_MODE"] = cosign_fail_mode
        return subprocess.run(
            [
                str(VERIFIER),
                str(self.manifest),
                str(self.archive),
                str(self.checksum),
                str(self.bundle),
            ],
            check=False,
            text=True,
            capture_output=True,
            env=env,
        )

    def test_portable_release_assets_pass(self) -> None:
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("release asset verification: PASS", result.stdout)
        calls = self.cosign_log.read_text(encoding="utf-8")
        self.assertIn("verify-blob --bundle", calls)
        self.assertIn("verify --certificate-identity", calls)
        self.assertIn("areasong-ops-web:" + "a" * 40 + "@sha256:", calls)

    def test_absolute_checksum_path_is_rejected(self) -> None:
        self.checksum.write_text(f"{self.digest}  /tmp/{self.archive.name}\n", encoding="utf-8")
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("archive basename", result.stderr)

    def test_manifest_digest_mismatch_is_rejected(self) -> None:
        self.write_manifest("sha256:" + "c" * 64)
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("digests differ", result.stderr)

    def test_manifest_archive_mismatch_is_rejected(self) -> None:
        self.write_manifest(f"sha256:{self.digest}", "other.tar.gz")
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not bound to its revision", result.stderr)

    def test_old_manifest_schema_is_rejected(self) -> None:
        self.write_manifest(f"sha256:{self.digest}", schema_version=1)
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("manifest contract is invalid", result.stderr)

    def test_mutable_web_image_is_rejected(self) -> None:
        self.write_manifest(
            f"sha256:{self.digest}",
            web_image="ghcr.io/areasong/areasong-ops-web:latest",
        )
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not bound to its revision and digest", result.stderr)

    def test_revision_archive_mismatch_is_rejected(self) -> None:
        self.write_manifest(f"sha256:{self.digest}", revision="c" * 40)
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not bound to its revision", result.stderr)

    def test_runner_signature_failure_is_rejected(self) -> None:
        result = self.verify(cosign_fail_mode="verify-blob")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Runner signature verification failed", result.stderr)

    def test_web_signature_failure_is_rejected(self) -> None:
        result = self.verify(cosign_fail_mode="verify")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Web image signature verification failed", result.stderr)


if __name__ == "__main__":
    unittest.main()
