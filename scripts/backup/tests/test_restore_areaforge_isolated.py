from __future__ import annotations

import datetime as dt
import gzip
import io
import os
import subprocess
import sys
import tarfile
import tempfile
import textwrap
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))

import backup_manifest


UTC = dt.timezone.utc
RESTORE_SCRIPT = SCRIPT_DIR / "restore-areaforge-isolated.sh"


class RestoreAreaForgeIsolatedTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.backup_root = self.root / "backups"
        self.fake_bin = self.root / "bin"
        self.fake_bin.mkdir()
        self.docker_root = self.root / "docker-root"
        self.docker_root.mkdir()
        self.docker_log = self.root / "docker.log"
        self._write_fake_commands()
        self.manifest = self._create_manifest()

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _write_executable(self, name: str, content: str) -> None:
        path = self.fake_bin / name
        path.write_text(content, encoding="utf-8")
        path.chmod(0o755)

    def _write_fake_commands(self) -> None:
        self._write_executable("id", "#!/usr/bin/env bash\nprintf '0\\n'\n")
        self._write_executable(
            "flock",
            "#!/usr/bin/env bash\n[ \"${FAKE_FLOCK_FAIL:-0}\" != 1 ]\n",
        )
        self._write_executable("rclone", "#!/usr/bin/env bash\nexit 0\n")
        self._write_executable(
            "docker",
            textwrap.dedent(
                """\
                #!/usr/bin/env python3
                import os
                import shlex
                import sys

                args = sys.argv[1:]
                with open(os.environ["FAKE_DOCKER_LOG"], "a", encoding="utf-8") as handle:
                    handle.write(" ".join(shlex.quote(item) for item in args) + "\\n")

                if args[:2] == ["image", "inspect"]:
                    if "--format" in args:
                        print(os.environ.get("FAKE_CONFIGURED_IMAGE_ID", "sha256:recorded"))
                        raise SystemExit(0)
                    if args[-1] == "sha256:recorded" and os.environ.get("FAKE_IMAGE_ID_AVAILABLE") == "0":
                        raise SystemExit(1)
                    raise SystemExit(0)
                if args and args[0] == "inspect":
                    raise SystemExit(0)
                if args and args[0] == "info":
                    print(os.environ["FAKE_DOCKER_ROOT"])
                    raise SystemExit(0)
                if args and args[0] in {"volume", "rm"}:
                    raise SystemExit(0)
                if args and args[0] == "run":
                    print("fake-container")
                    raise SystemExit(0)
                if args and args[0] == "logs":
                    print("PostgreSQL init process complete; ready for start up.")
                    raise SystemExit(0)
                if args and args[0] == "exec":
                    index = 1
                    interactive = args[index] == "-i"
                    if interactive:
                        index += 1
                    container = args[index]
                    command = " ".join(args[index + 1:])
                    if interactive:
                        sys.stdin.buffer.read()
                    if "pg_isready" in command:
                        raise SystemExit(0)
                    production = container == "areaforge-postgres"
                    if "POSTGRES_DB" in command and "pg_" not in command:
                        print(os.environ.get("FAKE_PRODUCTION_DATABASE", "areaforge"))
                    elif "pg_namespace" in command:
                        key = "FAKE_PRODUCTION_SCHEMAS" if production else "FAKE_RESTORED_SCHEMAS"
                        print(os.environ.get(key, "public"))
                    elif "pg_tables" in command:
                        key = "FAKE_PRODUCTION_TABLES" if production else "FAKE_RESTORED_TABLES"
                        print(os.environ.get(key, "public.items"))
                    elif "pg_database_size" in command:
                        key = "FAKE_PRODUCTION_DB_SIZE" if production else "FAKE_RESTORED_DB_SIZE"
                        print(os.environ.get(key, "1048576"))
                    elif "select 1" in command.lower():
                        print("1")
                    raise SystemExit(0)
                raise SystemExit(f"unexpected fake docker command: {args}")
                """
            ),
        )

    def _write_tar(self, path: Path, members: dict[str, bytes]) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(path, "w:gz") as archive:
            for name, payload in members.items():
                info = tarfile.TarInfo(name)
                info.size = len(payload)
                archive.addfile(info, fileobj=io.BytesIO(payload))

    def _create_manifest(self) -> Path:
        now = dt.datetime.now(UTC)
        timestamp = now.timestamp() - 60
        for index, (_, pattern, archive_type) in enumerate(backup_manifest.ARTIFACT_SPECS):
            path = self.backup_root / pattern.replace("*", "20260716-033000")
            path.parent.mkdir(parents=True, exist_ok=True)
            if archive_type == "gzip":
                with gzip.open(path, "wb") as handle:
                    handle.write(b"CREATE DATABASE areaforge;\n")
            elif "configs/configs-" in pattern:
                self._write_tar(path, {
                    "opt/areaforge/docker-compose.prod.yml": b"services: {}\n",
                    "opt/areaforge/.env.production": b"POSTGRES_DB=areaforge\n",
                })
            else:
                self._write_tar(path, {f"data/file-{index}.txt": b"fixture\n"})
            os.utime(path, (timestamp + index, timestamp + index))

        config = backup_manifest.CreateConfig(
            backup_root=self.backup_root,
            manifest_dir=self.backup_root / "manifests",
            metric_out=self.root / "create.prom",
            host="LosAngeles",
            now=now,
            window_hours=12,
            max_span_hours=3,
        )
        runtime = [{
            "name": "areaforge-postgres",
            "configured_image": "postgres:16-alpine",
            "image_id": "sha256:recorded",
        }]
        return backup_manifest.create_manifest(config, runtime_inventory=runtime)

    def _environment(self, **overrides: str) -> dict[str, str]:
        environment = os.environ.copy()
        environment.update({
            "PATH": f"{self.fake_bin}{os.pathsep}{environment['PATH']}",
            "BACKUP_ROOT": str(self.backup_root),
            "FAKE_DOCKER_LOG": str(self.docker_log),
            "FAKE_DOCKER_ROOT": str(self.docker_root),
            "RESTORE_DRILL_LOCK_FILE": str(self.root / "run" / "restore.lock"),
            "RESTORE_DRILL_LOG_DIR": str(self.root / "logs"),
            "RESTORE_DRILL_METRIC_OUT": str(self.root / "metrics" / "restore.prom"),
            "RESTORE_DRILL_WORK_ROOT": str(self.root / "work"),
            "RESTORE_DRILL_MIN_FREE_BYTES": "0",
            "RESTORE_DRILL_SPACE_MULTIPLIER": "1",
        })
        environment.update(overrides)
        return environment

    def _run(self, *arguments: str, **overrides: str) -> subprocess.CompletedProcess[str]:
        relative_manifest = self.manifest.relative_to(self.backup_root).as_posix()
        command = [str(RESTORE_SCRIPT), "--source", "local", "--manifest", relative_manifest, *arguments]
        return subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            env=self._environment(**overrides),
            timeout=30,
        )

    def test_manifest_restore_uses_recorded_image_and_unique_volume(self) -> None:
        work_root = self.root / "work"
        work_root.mkdir()
        work_root.chmod(0o711)
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(work_root.stat().st_mode & 0o777, 0o711)
        docker_log = self.docker_log.read_text(encoding="utf-8")
        self.assertIn("image inspect sha256:recorded", docker_log)
        self.assertIn("--mount type=volume,source=", docker_log)
        self.assertNotIn("--tmpfs", docker_log)
        self.assertIn("inspect areaforge-postgres\n", docker_log)
        metrics = (self.root / "metrics" / "restore.prom").read_text(encoding="utf-8")
        self.assertIn("areaforge_restore_drill_user_tables", metrics)
        self.assertIn("areaforge_restore_drill_database_size_bytes", metrics)

    def test_r2_legacy_artifacts_are_rejected(self) -> None:
        command = [
            str(RESTORE_SCRIPT), "--source", "r2",
            "--postgres-artifact", "postgres/areaforge-postgres-20260716-033000.sql.gz",
            "--configs-artifact", "configs/configs-20260716-033000.tar.gz",
            "--uploads-artifact", "volumes/areaforge-uploads-20260716-033000.tar.gz",
            "--ops-state-artifact", "volumes/areaforge-ops-state-20260716-033000.tar.gz",
            "--postgres-image", "postgres:16-alpine",
        ]
        result = subprocess.run(command, capture_output=True, text=True, env=self._environment(), timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("R2 restores require --manifest", result.stderr)

    def test_r2_manifest_requires_separate_verification_credentials(self) -> None:
        relative_manifest = self.manifest.relative_to(self.backup_root).as_posix()
        command = [str(RESTORE_SCRIPT), "--source", "r2", "--manifest", relative_manifest]
        result = subprocess.run(
            command,
            capture_output=True,
            text=True,
            env=self._environment(R2_VERIFY_ENV=str(self.root / "missing-r2-verify.env")),
            timeout=10,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("R2 config is missing", result.stdout + result.stderr)

    def test_r2_manifest_rejects_upload_config_path(self) -> None:
        config = self.root / "r2.env"
        config.write_text(
            "R2_BUCKET=bucket\nR2_ENDPOINT=https://example.invalid\nR2_PREFIX=backups\n"
            "R2_ACCESS_KEY_ID=id\nR2_SECRET_ACCESS_KEY=secret\n",
            encoding="utf-8",
        )
        relative_manifest = self.manifest.relative_to(self.backup_root).as_posix()
        command = [str(RESTORE_SCRIPT), "--source", "r2", "--manifest", relative_manifest]
        result = subprocess.run(
            command,
            capture_output=True,
            text=True,
            env=self._environment(R2_VERIFY_ENV=str(config), R2_BACKUP_ENV=str(config)),
            timeout=10,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must differ from the upload config", result.stdout + result.stderr)

    def test_lock_failure_does_not_create_work_directory(self) -> None:
        blocked_work = self.root / "blocked-work"
        result = self._run(FAKE_FLOCK_FAIL="1", RESTORE_DRILL_WORK_ROOT=str(blocked_work))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(blocked_work.exists())

    def test_compare_production_rejects_different_table_names(self) -> None:
        result = self._run("--compare-production", FAKE_PRODUCTION_TABLES="public.other")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("table names do not match production", result.stdout + result.stderr)

    def test_volume_content_minimum_is_enforced(self) -> None:
        result = self._run(RESTORE_EXPECT_UPLOAD_FILES_MIN="2")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("uploads archive has fewer files", result.stdout + result.stderr)

    def test_offline_import_does_not_access_production_or_publish_success(self) -> None:
        result = self._run("--no-compare-production")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        docker_log = self.docker_log.read_text(encoding="utf-8")
        self.assertNotIn("inspect areaforge-postgres\n", docker_log)
        self.assertFalse((self.root / "metrics" / "restore.prom").exists())

    def test_manifest_image_tag_must_still_match_recorded_id(self) -> None:
        result = self._run(
            FAKE_IMAGE_ID_AVAILABLE="0",
            FAKE_CONFIGURED_IMAGE_ID="sha256:different",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("configured tag points elsewhere", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
