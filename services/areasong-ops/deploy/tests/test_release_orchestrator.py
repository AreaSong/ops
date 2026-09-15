from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import sys
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from importlib.util import module_from_spec, spec_from_file_location


SCRIPT = Path(__file__).resolve().parents[1] / "release_orchestrator.py"
sys.path.insert(0, str(SCRIPT.parent))
SPEC = spec_from_file_location("release_orchestrator", SCRIPT)
assert SPEC and SPEC.loader
MODULE = module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ReleaseOrchestratorTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.revision = "a" * 40
        self.archive = self.root / f"areasong-ops-runner-{self.revision}-linux-amd64.tar.gz"
        stage = self.root / "stage"
        stage.mkdir()
        for name in ("areasong-ops-runner", "areasong-ops-runner-updater"):
            path = stage / name
            path.write_text(name, encoding="utf-8")
            path.chmod(0o755)
        with tarfile.open(self.archive, "w:gz") as bundle:
            for name in ("areasong-ops-runner", "areasong-ops-runner-updater"):
                bundle.add(stage / name, arcname=name)
        self.archive_digest = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        self.checksum = self.root / f"{self.archive.name}.sha256"
        self.checksum.write_text(f"{self.archive_digest}  {self.archive.name}\n", encoding="utf-8")
        self.bundle = self.root / "bundle.json"
        self.bundle.write_text("{}\n", encoding="utf-8")
        self.verifier = self.root / "verifier.sh"
        self.verifier.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        self.verifier.chmod(0o755)
        self.manifest = self.root / "manifest.json"
        self.manifest.write_text(
            json.dumps(
                {
                    "schemaVersion": 2,
                    "service": "areasong-ops",
                    "version": "1.1.1",
                    "revision": self.revision,
                    "platform": "linux/amd64",
                    "web": {
                        "image": f"ghcr.io/areasong/areasong-ops-web:{self.revision}@sha256:{'b' * 64}",
                        "cosign": "keyless",
                    },
                    "runner": {
                        "archive": self.archive.name,
                        "sha256": f"sha256:{self.archive_digest}",
                        "cosign": "keyless",
                    },
                }
            ),
            encoding="utf-8",
        )

    def asset_args(self) -> list[str]:
        return [
            "--manifest", str(self.manifest),
            "--runner-archive", str(self.archive),
            "--checksum", str(self.checksum),
            "--sigstore-bundle", str(self.bundle),
            "--verifier", str(self.verifier),
            "--state-dir", str(self.root / "state"),
        ]

    def test_plan_is_replayable_and_binds_manifest_digest(self) -> None:
        test_env = os.environ.copy()
        test_env["OPS_RELEASE_TEST_MODE"] = "1"
        first = subprocess.run([str(SCRIPT), "plan", *self.asset_args()], env=test_env, text=True, capture_output=True)
        self.assertEqual(first.returncode, 0, first.stderr)
        summary = json.loads(first.stdout)
        state_path = self.root / "state" / "deployments" / summary["deploymentId"] / "state.json"
        self.assertTrue(state_path.is_file())
        replay = subprocess.run(
            [str(SCRIPT), "plan", *self.asset_args(), "--deployment-id", summary["deploymentId"]],
            env=test_env,
            text=True,
            capture_output=True,
        )
        self.assertEqual(replay.returncode, 0, replay.stderr)
        self.assertEqual(json.loads(replay.stdout)["deploymentId"], summary["deploymentId"])
        altered = json.loads(self.manifest.read_text(encoding="utf-8"))
        altered["version"] = "1.1.2"
        self.manifest.write_text(json.dumps(altered), encoding="utf-8")
        mismatch = subprocess.run(
            [str(SCRIPT), "plan", *self.asset_args(), "--deployment-id", summary["deploymentId"]],
            env=test_env,
            text=True,
            capture_output=True,
        )
        self.assertNotEqual(mismatch.returncode, 0)
        self.assertIn("制品摘要不同", mismatch.stderr)

    def test_web_digest_contract_rejects_revision_mismatch(self) -> None:
        manifest = json.loads(self.manifest.read_text(encoding="utf-8"))
        manifest["web"]["image"] = f"ghcr.io/areasong/areasong-ops-web:{'c' * 40}@sha256:{'b' * 64}"
        self.manifest.write_text(json.dumps(manifest), encoding="utf-8")
        with self.assertRaises(MODULE.ReleaseError):
            MODULE.parse_manifest(self.manifest)

    def test_archive_path_traversal_is_rejected(self) -> None:
        malicious = self.root / "bad.tar.gz"
        payload = self.root / "payload"
        payload.write_text("bad", encoding="utf-8")
        with tarfile.open(malicious, "w:gz") as bundle:
            bundle.add(payload, arcname="../escaped")
        with self.assertRaises(MODULE.ReleaseError):
            MODULE.safe_extract_runner(malicious, self.root / "extract")

    def test_runtime_env_update_preserves_other_values(self) -> None:
        env = self.root / ".env"
        env.write_text("SECRET_TOKEN=do-not-log\nOPS_BUILD_VERSION=old\nOTHER=value\n", encoding="utf-8")
        MODULE.update_runtime_env(env, "1.1.1", self.revision)
        self.assertEqual(
            env.read_text(encoding="utf-8"),
            f"SECRET_TOKEN=do-not-log\nOPS_BUILD_VERSION=1.1.1\nOTHER=value\nOPS_BUILD_REVISION={self.revision}\n",
        )

    def test_copy_preserves_existing_parent_permissions(self) -> None:
        parent = self.root / "systemd"
        parent.mkdir(mode=0o755)
        source = self.root / "unit"
        source.write_text("unit", encoding="utf-8")
        MODULE.copy_atomic(source, parent / "runner.service", 0o644)
        self.assertEqual(parent.stat().st_mode & 0o777, 0o755)

    def test_container_inspect_backup_is_secret_free(self) -> None:
        raw = json.dumps(
            [
                {
                    "Id": "container-id",
                    "Image": "sha256:" + "d" * 64,
                    "Config": {
                        "Image": "areasong-ops-web:old",
                        "User": "65532:65532",
                        "Env": ["ACCESS_CLIENT_SECRET=must-not-persist"],
                        "Labels": {"org.opencontainers.image.revision": "c" * 40},
                    },
                    "HostConfig": {"ReadonlyRootfs": True, "NetworkMode": "areasong-ops-network"},
                    "State": {"Running": True, "Status": "running"},
                    "Mounts": [],
                }
            ]
        )
        sanitized = MODULE.sanitized_container_inspect(raw)
        serialized = json.dumps(sanitized, sort_keys=True)
        self.assertNotIn("ACCESS_CLIENT_SECRET", serialized)
        self.assertEqual(sanitized["Image"], "sha256:" + "d" * 64)

    def test_deploy_rejects_non_root_without_test_mode(self) -> None:
        env = os.environ.copy()
        env.pop("OPS_RELEASE_TEST_MODE", None)
        result = subprocess.run([str(SCRIPT), "deploy", *self.asset_args()], env=env, text=True, capture_output=True)
        if os.geteuid() != 0:
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("必须以 root 执行", result.stderr)


if __name__ == "__main__":
    unittest.main()
