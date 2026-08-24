from __future__ import annotations

import gzip
import json
import os
import subprocess
import tarfile
import tempfile
import textwrap
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parents[1]
SCRIPT = SCRIPT_DIR / "restore-areaforge-production.sh"
MANIFEST_TOOL = SCRIPT_DIR / "backup_manifest.py"
ENV_SWITCHER = SCRIPT_DIR / "restore_env.py"
TARGET = "11111111-1111-4111-8111-111111111111"


class RestoreAreaForgeProductionTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.operation = self.root / "operation"
        self.operation.mkdir()
        self.backup_root = self.root / "backups"
        self.backup_root.mkdir()
        self.work_root = self.root / "restore-work"
        self.docker_root = self.root / "docker-root"
        self.docker_root.mkdir()
        self.fake_bin = self.root / "bin"
        self.fake_bin.mkdir()
        self.docker_log = self.root / "docker.log"
        self.container_state = self.root / "containers.json"
        self.container_state.write_text(
            json.dumps({"areaforge-web": "running", "areaforge-postgres": "running"}),
            encoding="utf-8",
        )
        self.env_file = self.root / ".env.production"
        self.env_file.write_text(
            "POSTGRES_USER=areaforge\nPOSTGRES_DB=areaforge\nPOSTGRES_PASSWORD=secret\n",
            encoding="utf-8",
        )
        self.env_file.chmod(0o600)
        self.controlled_compose = self.root / "controlled.yml"
        self.runtime_compose = self.root / "runtime.yml"
        compose = "services:\n  web:\n    image: example.invalid/areaforge@sha256:" + "a" * 64 + "\n"
        self.controlled_compose.write_text(compose, encoding="utf-8")
        self.runtime_compose.write_text(compose, encoding="utf-8")
        (self.operation / "task-contract.json").write_text("{}\n", encoding="utf-8")
        (self.operation / "task-contract.json").chmod(0o600)
        (self.operation / "recovery-point.json").write_text("{}\n", encoding="utf-8")
        (self.operation / "recovery-point.json").chmod(0o600)
        self.postgres_artifact = self.backup_root / "areaforge.sql.gz"
        with gzip.open(self.postgres_artifact, "wb") as handle:
            handle.write(b"select 1;\n")
        self.uploads_artifact = self.backup_root / "uploads.tar.gz"
        self.ops_artifact = self.backup_root / "ops.tar.gz"
        self._write_tar(self.uploads_artifact, {"upload.txt": b"upload\n"})
        self._write_tar(self.ops_artifact, {"state.json": b"{}\n"})
        self.validator = self.root / "validator.py"
        self._write_executable(
            self.validator,
            textwrap.dedent(
                """\
                #!/usr/bin/env python3
                import json
                import os
                import sys

                if os.environ.get("FAKE_CONTRACT_FAIL") == "1":
                    print("ERROR: recovery contract rejected", file=sys.stderr)
                    raise SystemExit(1)
                print(json.dumps({
                    "artifacts": {
                        "postgres-areaforge": {"path": os.environ["FAKE_POSTGRES_ARTIFACT"]},
                        "volume-areaforge-uploads": {"path": os.environ["FAKE_UPLOADS_ARTIFACT"]},
                        "volume-areaforge-ops-state": {"path": os.environ["FAKE_OPS_ARTIFACT"]},
                    }
                }))
                """
            ),
        )
        self._write_fake_commands()

    def _write_tar(self, path: Path, files: dict[str, bytes]) -> None:
        staging = self.root / (path.stem + "-files")
        staging.mkdir()
        for name, content in files.items():
            target = staging / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
        with tarfile.open(path, "w:gz") as archive:
            for target in sorted(staging.rglob("*")):
                archive.add(target, arcname=target.relative_to(staging))

    def _write_executable(self, path: Path, content: str) -> None:
        path.write_text(content, encoding="utf-8")
        path.chmod(0o755)

    def _write_fake_commands(self) -> None:
        self._write_executable(self.fake_bin / "id", "#!/bin/sh\nprintf '0\\n'\n")
        self._write_executable(
            self.fake_bin / "chown",
            "#!/bin/sh\nprintf 'chown %s\\n' \"$*\" >>\"$FAKE_DOCKER_LOG\"\n",
        )
        self._write_executable(
            self.fake_bin / "curl",
            "#!/bin/sh\n[ \"${FAKE_HEALTH_FAIL:-0}\" != 1 ]\n",
        )
        self._write_executable(
            self.fake_bin / "sleep",
            "#!/bin/sh\nexit 0\n",
        )
        self._write_executable(
            self.fake_bin / "docker",
            textwrap.dedent(
                """\
                #!/usr/bin/env python3
                import json
                import os
                import shlex
                import sys
                from pathlib import Path

                args = sys.argv[1:]
                log = Path(os.environ["FAKE_DOCKER_LOG"])
                with log.open("a", encoding="utf-8") as handle:
                    handle.write(" ".join(shlex.quote(value) for value in args) + "\\n")
                state_path = Path(os.environ["FAKE_CONTAINER_STATE"])
                state = json.loads(state_path.read_text(encoding="utf-8"))
                docker_root = Path(os.environ["FAKE_DOCKER_ROOT"])

                def save():
                    state_path.write_text(json.dumps(state), encoding="utf-8")

                def container():
                    return args[-1] if args else ""

                if args[:1] == ["compose"]:
                    if "stop" in args:
                        for name in ("areaforge-web", "areaforge-postgres"):
                            state[name] = "exited"
                        save()
                    elif "up" in args:
                        for name in ("areaforge-web", "areaforge-postgres"):
                            state[name] = "running"
                        save()
                    raise SystemExit(0)
                if args[:2] == ["info", "--format"]:
                    print(docker_root)
                    raise SystemExit(0)
                if args[:2] == ["volume", "create"]:
                    volume = args[-1]
                    (docker_root / "volumes" / volume / "_data").mkdir(parents=True)
                    print(volume)
                    raise SystemExit(0)
                if args[:2] == ["volume", "inspect"]:
                    volume = args[-1]
                    mount = docker_root / "volumes" / volume / "_data"
                    if not mount.is_dir():
                        raise SystemExit(1)
                    if "-f" in args:
                        print(mount)
                    else:
                        print(json.dumps([{"Mountpoint": str(mount)}]))
                    raise SystemExit(0)
                if args[:1] == ["run"]:
                    name = args[args.index("--name") + 1]
                    state[name] = "running"
                    save()
                    print("fake-container-id")
                    raise SystemExit(0)
                if args[:1] == ["logs"]:
                    print("PostgreSQL init process complete; ready for start up.")
                    raise SystemExit(0)
                if args[:1] == ["stop"]:
                    state[container()] = "exited"
                    save()
                    raise SystemExit(0)
                if args[:1] == ["rm"]:
                    state.pop(container(), None)
                    save()
                    raise SystemExit(0)
                if args[:1] == ["exec"]:
                    if "-i" in args:
                        sys.stdin.buffer.read()
                    name_index = 2 if len(args) > 1 and args[1] == "-i" else 1
                    name = args[name_index]
                    command = args[name_index + 1:]
                    if name == "areaforge-web" and command == ["id", "-u"]:
                        print("1001")
                    elif name == "areaforge-web" and command == ["id", "-g"]:
                        print("1002")
                    else:
                        print("1")
                    raise SystemExit(0)
                if args[:1] == ["inspect"]:
                    name = container()
                    if "--format" not in args:
                        if name.startswith("areasong-restore-pg-"):
                            if name not in state:
                                raise SystemExit(1)
                            print(json.dumps([{"Name": name}]))
                        elif name == "areaforge-postgres":
                            print(json.dumps([{"Config": {"Env": [
                                "POSTGRES_USER=areaforge",
                                "POSTGRES_DB=areaforge",
                                "POSTGRES_PASSWORD=secret",
                            ]}}]))
                        else:
                            print(json.dumps([{"Name": name}]))
                        raise SystemExit(0)
                    fmt = args[args.index("--format") + 1]
                    if "Config.Image" in fmt:
                        print("postgres:16-alpine@sha256:" + "b" * 64)
                    elif "State.Health.Status" in fmt:
                        print("healthy" if state.get(name) == "running" else "unhealthy")
                    elif "State.Status" in fmt:
                        print(state.get(name, "missing"))
                    else:
                        print(state.get(name, "missing"))
                    raise SystemExit(0)
                print("unexpected docker invocation: " + " ".join(args), file=sys.stderr)
                raise SystemExit(1)
                """
            ),
        )

    def environment(self, **overrides: str) -> dict[str, str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.fake_bin}:{environment['PATH']}",
                "BACKUP_ROOT": str(self.backup_root),
                "RESTORE_CONTRACT_VALIDATOR": str(self.validator),
                "BACKUP_MANIFEST_TOOL": str(MANIFEST_TOOL),
                "RESTORE_ENV_SWITCHER": str(ENV_SWITCHER),
                "AREAFORGE_RESTORE_ENV_FILE": str(self.env_file),
                "AREAFORGE_RESTORE_RUNTIME_COMPOSE": str(self.runtime_compose),
                "AREAFORGE_RESTORE_CONTROLLED_COMPOSE": str(self.controlled_compose),
                "AREAFORGE_RESTORE_WORK_ROOT": str(self.work_root),
                "FAKE_POSTGRES_ARTIFACT": str(self.postgres_artifact),
                "FAKE_UPLOADS_ARTIFACT": str(self.uploads_artifact),
                "FAKE_OPS_ARTIFACT": str(self.ops_artifact),
                "FAKE_DOCKER_LOG": str(self.docker_log),
                "FAKE_CONTAINER_STATE": str(self.container_state),
                "FAKE_DOCKER_ROOT": str(self.docker_root),
            }
        )
        environment.update(overrides)
        return environment

    def run_phase(self, phase: str, **overrides: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [str(SCRIPT), "restore", phase, str(self.operation), TARGET, ""],
            text=True,
            capture_output=True,
            env=self.environment(**overrides),
            check=False,
        )

    def assert_phase(self, phase: str) -> dict[str, object]:
        result = self.run_phase(phase)
        self.assertEqual(result.returncode, 0, result.stderr)
        payload = json.loads(result.stdout)
        self.assertEqual(payload["phase"], phase)
        return payload

    def test_full_restore_switches_to_new_sources_without_removing_previous_data(self) -> None:
        self.assert_phase("preflight")
        self.assert_phase("quiesce")
        restore = self.assert_phase("restore")
        self.assertTrue(restore["data"]["productionChanged"])
        self.assert_phase("verify")
        self.assert_phase("resume")

        state = json.loads((self.operation / "areaforge-production-state.json").read_text(encoding="utf-8"))
        self.assertEqual(state["recoveryPointId"], TARGET)
        self.assertTrue(state["quiesced"])
        self.assertTrue(state["restored"])
        self.assertTrue(state["verified"])
        self.assertTrue(state["resumed"])
        env = self.env_file.read_text(encoding="utf-8")
        self.assertIn("AREAFORGE_POSTGRES_VOLUME=areasong-restore-areaforge-", env)
        self.assertIn("AREAFORGE_UPLOADS_VOLUME=areasong-restore-uploads-", env)
        self.assertTrue((self.operation / "areaforge.env.before").is_file())
        self.assertIn("POSTGRES_PASSWORD=secret", (self.operation / "areaforge.env.before").read_text(encoding="utf-8"))
        log = self.docker_log.read_text(encoding="utf-8")
        self.assertIn("logs areasong-restore-pg-", log)
        self.assertNotIn("rm -f", log)
        self.assertIn("chown -R 1001:1002", log)

    def test_restore_requires_quiesce_and_preserves_environment(self) -> None:
        self.assert_phase("preflight")
        before = self.env_file.read_text(encoding="utf-8")
        result = self.run_phase("restore")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requires quiesce", result.stderr)
        self.assertEqual(self.env_file.read_text(encoding="utf-8"), before)

    def test_existing_staging_volume_is_not_removed(self) -> None:
        self.assert_phase("preflight")
        self.assert_phase("quiesce")
        volume = self.docker_root / "volumes" / f"areasong-restore-uploads-{TARGET.replace('-', '')}" / "_data"
        volume.mkdir(parents=True)
        marker = volume / "evidence.txt"
        marker.write_text("keep\n", encoding="utf-8")
        result = self.run_phase("restore")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("staging uploads volume already exists", result.stderr)
        self.assertEqual(marker.read_text(encoding="utf-8"), "keep\n")

    def test_verify_failure_does_not_resume_or_restore_old_database(self) -> None:
        self.assert_phase("preflight")
        self.assert_phase("quiesce")
        self.assert_phase("restore")
        result = self.run_phase("verify", FAKE_HEALTH_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.operation / "restore-verified").exists())
        resume = self.run_phase("resume")
        self.assertNotEqual(resume.returncode, 0)
        self.assertIn("verification evidence is missing", resume.stderr)
        log = self.docker_log.read_text(encoding="utf-8")
        self.assertNotIn("areaforge.env.before --set", log)

    def test_contract_rejection_happens_before_docker_mutation(self) -> None:
        result = self.run_phase("preflight", FAKE_CONTRACT_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("恢复合同校验失败", result.stderr)
        self.assertFalse(self.docker_log.exists())


if __name__ == "__main__":
    unittest.main()
