from __future__ import annotations

import hashlib
import json
import os
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from importlib.util import module_from_spec, spec_from_file_location


SCRIPT = Path(__file__).resolve().parents[1] / "release_orchestrator.py"
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

    def deployment_args(self, state_dir: Path) -> SimpleNamespace:
        paths = {
            "repo_root": self.root / "repo",
            "runtime_dir": self.root / "runtime",
            "config_dir": self.root / "config",
            "runner_root": self.root / "runner",
            "unit_path": self.root / "system/areasong-ops-runner.service",
            "updater_unit_path": self.root / "system/areasong-ops-runner-update@.service",
            "db_path": self.root / "ops.db",
            "socket_path": self.root / "run/runner.sock",
            "container_name": "areasong-ops-web",
            "preflight": "/bin/true",
            "candidate_unit": self.root / "candidate.service",
            "candidate_updater_unit": self.root / "candidate-update.service",
        }
        return SimpleNamespace(
            **paths,
            runner_archive=str(self.archive),
            state_dir=state_dir,
        )

    def prepare_deploy_fixture(self, args: SimpleNamespace) -> None:
        args.repo_root.mkdir()
        (args.repo_root / "services/areasong-ops/deploy").mkdir(parents=True)
        args.runtime_dir.mkdir()
        args.config_dir.mkdir()
        (args.runner_root / "runner").mkdir(parents=True)
        args.unit_path.parent.mkdir(parents=True)
        (args.repo_root / ".git-marker").write_text("source", encoding="utf-8")
        (args.runtime_dir / ".env").write_text("OPS_BUILD_VERSION=old\nOPS_BUILD_REVISION=" + "c" * 40 + "\n", encoding="utf-8")
        (args.runtime_dir / "compose.yml").write_text("services: {}\n", encoding="utf-8")
        (args.config_dir / "web.env").write_text("OPS_PUBLIC_ORIGIN=https://ops.areasong.top\n", encoding="utf-8")
        args.db_path.write_bytes(b"SQLite format 3\x00")
        for path in (
            args.runner_root / "runner/areasong-ops-runner",
            args.runner_root / "areasong-ops-runner-updater",
            args.unit_path,
            args.updater_unit_path,
            args.candidate_unit,
            args.candidate_updater_unit,
        ):
            path.write_text("old", encoding="utf-8")
        # snapshot_sqlite 需要真正可读的 SQLite 数据库。
        args.db_path.unlink()
        import sqlite3

        with sqlite3.connect(args.db_path) as database:
            database.execute("create table state(value text)")
            database.execute("insert into state values ('old')")

    def test_deploy_success_records_order_and_skips_on_replay(self) -> None:
        state_dir = self.root / "state"
        args = self.deployment_args(state_dir)
        self.prepare_deploy_fixture(args)
        metadata = MODULE.verify_assets(self.manifest, self.archive, self.checksum, self.bundle, self.verifier)
        state = MODULE.State(state_dir, "ops-test-success", metadata, create=True)
        calls: list[list[str]] = []
        runner_restarted = False
        web_recreated = False

        def fake_run(command: list[str], **_: object) -> SimpleNamespace:
            nonlocal runner_restarted, web_recreated
            calls.append(command)
            if command[:4] == ["git", "-C", str(args.repo_root), "rev-parse"]:
                return SimpleNamespace(returncode=0, stdout=metadata["revision"] + "\n", stderr="")
            if command and command[0] == "curl":
                if "--unix-socket" in command:
                    revision = metadata["revision"] if runner_restarted else "c" * 40
                    return SimpleNamespace(returncode=0, stdout=json.dumps({"ok": True, "revision": revision}), stderr="")
                revision = metadata["revision"] if web_recreated else "c" * 40
                return SimpleNamespace(returncode=0, stdout=json.dumps({"ok": True, "revision": revision}), stderr="")
            if command[:3] == ["docker", "inspect", args.container_name]:
                revision = metadata["revision"] if web_recreated else "c" * 40
                payload = [{"State": {"Running": True}, "Config": {"Labels": {"org.opencontainers.image.revision": revision}}, "Image": "sha256:" + "d" * 64}]
                return SimpleNamespace(returncode=0, stdout=json.dumps(payload), stderr="")
            if command[:3] == ["docker", "image", "inspect"]:
                payload = [{"Id": "sha256:" + "e" * 64, "RepoDigests": [metadata["web_image"].split("@", 1)[0].rsplit(":", 1)[0] + "@" + metadata["web_image"].split("@", 1)[1]]}]
                return SimpleNamespace(returncode=0, stdout=json.dumps(payload), stderr="")
            if command[:2] == ["systemctl", "restart"]:
                runner_restarted = True
            if command[:2] == ["docker", "compose"] and "up" in command:
                web_recreated = True
            return SimpleNamespace(returncode=0, stdout="", stderr="")

        with patch.object(MODULE, "run", side_effect=fake_run):
            with patch.dict(os.environ, {"OPS_RELEASE_TEST_MODE": "1"}):
                MODULE.Orchestrator(args, state, metadata).deploy()
                first_restart_count = sum(command[:2] == ["systemctl", "restart"] for command in calls)
                first_compose_count = sum(command[:2] == ["docker", "compose"] and "up" in command for command in calls)
                MODULE.Orchestrator(args, state, metadata).deploy()
        self.assertEqual(state.data["status"], "succeeded")
        self.assertEqual(first_restart_count, 1)
        self.assertEqual(first_compose_count, 1)
        self.assertEqual(
            (args.runner_root / "areasong-ops-runner-updater").read_text(encoding="utf-8"),
            "areasong-ops-runner-updater",
        )
        self.assertEqual(
            (state.directory / "backup/runner-updater").read_text(encoding="utf-8"),
            "old",
        )
        self.assertFalse((args.runner_root / "runner/areasong-ops-runner-updater").exists())

    def test_web_failure_rolls_back_changed_components(self) -> None:
        state_dir = self.root / "state"
        args = self.deployment_args(state_dir)
        self.prepare_deploy_fixture(args)
        metadata = MODULE.verify_assets(self.manifest, self.archive, self.checksum, self.bundle, self.verifier)
        state = MODULE.State(state_dir, "ops-test-failure", metadata, create=True)
        runner_restarted = False
        compose_calls = 0

        def fake_run(command: list[str], **_: object) -> SimpleNamespace:
            nonlocal runner_restarted, compose_calls
            if command[:4] == ["git", "-C", str(args.repo_root), "rev-parse"]:
                return SimpleNamespace(returncode=0, stdout=metadata["revision"] + "\n", stderr="")
            if command and command[0] == "curl":
                if "--unix-socket" in command:
                    revision = metadata["revision"] if runner_restarted else "c" * 40
                else:
                    revision = "c" * 40
                return SimpleNamespace(returncode=0, stdout=json.dumps({"ok": True, "revision": revision}), stderr="")
            if command[:3] == ["docker", "inspect", args.container_name]:
                payload = [{"State": {"Running": True}, "Config": {"Labels": {"org.opencontainers.image.revision": "c" * 40}}, "Image": "sha256:" + "d" * 64}]
                return SimpleNamespace(returncode=0, stdout=json.dumps(payload), stderr="")
            if command[:3] == ["docker", "image", "inspect"]:
                payload = [{"Id": "sha256:" + "e" * 64, "RepoDigests": [metadata["web_image"].split("@", 1)[0].rsplit(":", 1)[0] + "@" + metadata["web_image"].split("@", 1)[1]]}]
                return SimpleNamespace(returncode=0, stdout=json.dumps(payload), stderr="")
            if command[:2] == ["systemctl", "restart"]:
                runner_restarted = True
            if command[:2] == ["docker", "compose"] and "up" in command:
                compose_calls += 1
                if compose_calls == 1:
                    return SimpleNamespace(returncode=1, stdout="", stderr="compose failed")
            return SimpleNamespace(returncode=0, stdout="", stderr="")

        with patch.object(MODULE, "run", side_effect=fake_run):
            with patch.dict(os.environ, {"OPS_RELEASE_TEST_MODE": "1"}):
                with self.assertRaises(MODULE.ReleaseError):
                    MODULE.Orchestrator(args, state, metadata).deploy()
        self.assertEqual(state.data["status"], "rolled_back")
        self.assertEqual(state.data["rollback"]["status"], "succeeded")
        self.assertGreaterEqual(compose_calls, 2)
        self.assertEqual(
            (args.runner_root / "areasong-ops-runner-updater").read_text(encoding="utf-8"),
            "old",
        )
        self.assertFalse((args.runner_root / "runner/areasong-ops-runner-updater").exists())


if __name__ == "__main__":
    unittest.main()
