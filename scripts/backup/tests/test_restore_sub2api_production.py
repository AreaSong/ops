from __future__ import annotations

import gzip
import json
import os
import subprocess
import tarfile
import tempfile
import unittest
from io import BytesIO
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "scripts" / "backup" / "restore-sub2api-production.sh"
MANIFEST_TOOL = REPO_ROOT / "scripts" / "backup" / "backup_manifest.py"
ENV_SWITCHER = REPO_ROOT / "scripts" / "backup" / "restore_env.py"


class RestoreSub2APIProductionTests(unittest.TestCase):
    task_id = "22222222-2222-4222-8222-222222222222"
    target = "11111111-1111-4111-8111-111111111111"

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.operation = self.root / self.task_id
        self.operation.mkdir()
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.fake_state = self.root / "docker-state"
        self.fake_state.mkdir()
        self.docker_log = self.root / "docker.log"
        self.data_root = self.root / "sub2api"
        self.old_data = self.data_root / "data"
        self.old_postgres = self.data_root / "postgres_data"
        self.old_redis = self.data_root / "redis_data"
        for path in (self.old_data, self.old_postgres, self.old_redis):
            path.mkdir(parents=True)
        self.work_root = self.root / "restore-work"
        self.env_file = self.root / ".env"
        self.env_file.write_text(self._original_env(), encoding="utf-8")
        self.env_file.chmod(0o600)
        self.controlled_compose = self.root / "controlled-compose.yml"
        self.runtime_compose = self.root / "runtime-compose.yml"
        compose = "services:\n  sub2api: {}\n  postgres: {}\n  redis: {}\n"
        self.controlled_compose.write_text(compose, encoding="utf-8")
        self.runtime_compose.write_text(compose, encoding="utf-8")
        self.backup_root = self.root / "backups"
        for name in ("postgres", "redis", "volumes"):
            (self.backup_root / name).mkdir(parents=True)
        self.postgres_artifact = self.backup_root / "postgres" / "sub2api-postgres-test.sql.gz"
        with gzip.open(self.postgres_artifact, "wb") as handle:
            handle.write(b"-- fake pg_dumpall\n")
        self.redis_artifact = self.backup_root / "redis" / "redis-test.tar.gz"
        self.data_artifact = self.backup_root / "volumes" / "sub2api-data-test.tar.gz"
        self._write_redis_archive()
        self._write_data_archive()
        self._write_contract_files()
        self.contract_validator = self.root / "restore-contract"
        self._write_contract_validator()
        self._write_fakes()

    def _original_env(self) -> str:
        return (
            "POSTGRES_USER=sub2api\n"
            "POSTGRES_PASSWORD=postgres-only\n"
            "POSTGRES_DB=sub2api\n"
            "REDIS_PASSWORD=redis-only\n"
            f"SUB2API_DATA_DIR={self.old_data}\n"
            f"SUB2API_POSTGRES_DATA_DIR={self.old_postgres}\n"
            f"SUB2API_REDIS_DATA_DIR={self.old_redis}\n"
        )

    @staticmethod
    def _add_bytes(archive: tarfile.TarFile, name: str, payload: bytes) -> None:
        info = tarfile.TarInfo(name)
        info.size = len(payload)
        info.mode = 0o600
        archive.addfile(info, BytesIO(payload))

    def _write_data_archive(self) -> None:
        with tarfile.open(self.data_artifact, "w:gz") as archive:
            self._add_bytes(archive, "data/config.yaml", b"mode: production\n")
            self._add_bytes(archive, "data/nested/state.json", b"{}\n")

    def _write_redis_archive(self, *, include_acl: bool = True, include_old_aof: bool = False) -> None:
        with tarfile.open(self.redis_artifact, "w:gz") as archive:
            metadata = b"aclfile_included=yes\n" if include_acl else b"aclfile_included=no\n"
            self._add_bytes(archive, "metadata.txt", metadata)
            self._add_bytes(archive, "redis_data/dump.rdb", b"REDIS0009\n")
            if include_acl:
                self._add_bytes(archive, "redis_data/users.acl", b"user default on nopass ~* +@all\n")
            if include_old_aof:
                self._add_bytes(
                    archive,
                    "redis_data/appendonlydir/appendonly.aof.1.base.rdb",
                    b"old\n",
                )

    def _write_contract_files(self) -> None:
        recovery = self.operation / "recovery-point.json"
        recovery.write_text("{}\n", encoding="utf-8")
        recovery.chmod(0o600)
        task_contract = {
            "schemaVersion": 1,
            "taskId": self.task_id,
            "service": "sub2api",
            "action": "restore",
            "target": self.target,
            "restoreMode": "production",
        }
        contract = self.operation / "task-contract.json"
        contract.write_text(json.dumps(task_contract), encoding="utf-8")
        contract.chmod(0o600)

    def _write_executable(self, name: str, body: str) -> Path:
        path = self.bin_dir / name
        path.write_text(body, encoding="utf-8")
        path.chmod(0o755)
        return path

    def _write_contract_validator(self) -> None:
        self.contract_validator.write_text(
            """#!/usr/bin/env python3
import json
import os

roles = {
    "postgres-sub2api": os.environ["TEST_POSTGRES_ARTIFACT"],
    "redis": os.environ["TEST_REDIS_ARTIFACT"],
    "volume-sub2api-data": os.environ["TEST_DATA_ARTIFACT"],
}
print(json.dumps({
    "schemaVersion": 1,
    "taskId": os.environ["TEST_TASK_ID"],
    "artifacts": {role: {"path": path} for role, path in roles.items()},
}))
""",
            encoding="utf-8",
        )
        self.contract_validator.chmod(0o755)

    def _write_fakes(self) -> None:
        self._write_executable(
            "id",
            """#!/usr/bin/env python3
import os
import sys
if sys.argv[1:] == ["-u"]:
    print(0)
else:
    os.execv("/usr/bin/id", ["id", *sys.argv[1:]])
""",
        )
        self._write_executable(
            "chown",
            """#!/usr/bin/env python3
import os
import sys
from pathlib import Path

with Path(os.environ["FAKE_DOCKER_LOG"]).open("a", encoding="utf-8") as handle:
    handle.write("chown " + " ".join(sys.argv[1:]) + "\\n")
""",
        )
        self._write_executable("sleep", "#!/usr/bin/env python3\nraise SystemExit(0)\n")
        self._write_executable(
            "curl",
            """#!/usr/bin/env python3
import json
import os
import sys
from pathlib import Path

args = sys.argv[1:]
status = Path(os.environ["FAKE_DOCKER_STATE"]) / "status-sub2api"
if not status.exists() or status.read_text(encoding="utf-8").strip() != "running":
    raise SystemExit(22)
print(json.dumps({"status": "ok"}))
""",
        )
        self._write_executable("docker", self._fake_docker_source())

    @staticmethod
    def _fake_docker_source() -> str:
        return """#!/usr/bin/env python3
import json
import os
import sys
from pathlib import Path

args = sys.argv[1:]
state = Path(os.environ["FAKE_DOCKER_STATE"])
log = Path(os.environ["FAKE_DOCKER_LOG"])
with log.open("a", encoding="utf-8") as handle:
    handle.write(" ".join(args) + "\\n")

def status_path(name):
    return state / ("status-" + name)

def container_path(name):
    return state / ("container-" + name)

def get_status(name):
    path = status_path(name)
    return path.read_text(encoding="utf-8").strip() if path.exists() else "running"

def set_status(name, value):
    status_path(name).write_text(value + "\\n", encoding="utf-8")

def read_env(key, default):
    for line in Path(os.environ["SUB2API_RESTORE_ENV_FILE"]).read_text(encoding="utf-8").splitlines():
        if line.startswith(key + "="):
            return line.split("=", 1)[1]
    return default

configured = {
    "sub2api": "weishaw/sub2api@sha256:" + "a" * 64,
    "sub2api-postgres": "postgres:18-alpine@sha256:" + "b" * 64,
    "sub2api-redis": "redis:8-alpine@sha256:" + "c" * 64,
}
image_ids = {
    "sub2api": "sha256:" + "a" * 64,
    "sub2api-postgres": "sha256:" + "b" * 64,
    "sub2api-redis": "sha256:" + "c" * 64,
}

def image_id(reference):
    for name, configured_reference in configured.items():
        if reference in (name, configured_reference, image_ids[name]):
            return image_ids[name]
    return reference

def container_json(name):
    root = os.environ["SUB2API_RESTORE_DATA_ROOT"]
    if name == "sub2api":
        source, destination = read_env("SUB2API_DATA_DIR", root + "/data"), "/app/data"
        env = ["APP_ENV=production"]
    elif name == "sub2api-postgres":
        source = read_env("SUB2API_POSTGRES_DATA_DIR", root + "/postgres_data")
        destination = "/var/lib/postgresql/data"
        env = ["POSTGRES_USER=sub2api", "POSTGRES_PASSWORD=postgres-only", "POSTGRES_DB=sub2api"]
    elif name == "sub2api-redis":
        source, destination = read_env("SUB2API_REDIS_DATA_DIR", root + "/redis_data"), "/data"
        env = ["REDISCLI_AUTH=redis-only"]
    else:
        raise SystemExit(1)
    return [{
        "Config": {"Image": configured[name], "Env": env},
        "Image": image_ids[name],
        "Mounts": [{"Type": "bind", "Source": source, "Destination": destination}],
        "State": {"Status": get_status(name), "Health": {"Status": "healthy"}},
    }]

def parse_volume_source(items, destination):
    for index, item in enumerate(items):
        if item == "--volume" and index + 1 < len(items):
            source, separator, target = items[index + 1].rpartition(":")
            if separator and target == destination:
                return source
    return ""

if not args:
    raise SystemExit(1)
if args[0] == "compose":
    remaining = args[1:]
    command = None
    index = 0
    while index < len(remaining):
        item = remaining[index]
        if item in ("--env-file", "-f", "--project-name"):
            index += 2
            continue
        if item in ("config", "stop", "up"):
            command = item
            remaining = remaining[index + 1:]
            break
        index += 1
    if command == "config":
        raise SystemExit(0)
    if command == "stop":
        mapping = {"sub2api": "sub2api", "postgres": "sub2api-postgres", "redis": "sub2api-redis"}
        for service in remaining:
            if service in mapping:
                set_status(mapping[service], "exited")
        raise SystemExit(0)
    if command == "up":
        if os.environ.get("FAKE_COMPOSE_UP_FAIL") == "1":
            raise SystemExit(44)
        mapping = {"sub2api": "sub2api", "postgres": "sub2api-postgres", "redis": "sub2api-redis"}
        for service in remaining:
            if service not in mapping:
                continue
            set_status(mapping[service], "running")
            if service == "sub2api":
                container_path("mount-sub2api").write_text(
                    read_env("SUB2API_DATA_DIR", os.environ["SUB2API_RESTORE_DATA_ROOT"] + "/data"),
                    encoding="utf-8",
                )
            elif service == "postgres":
                container_path("mount-sub2api-postgres").write_text(
                    read_env("SUB2API_POSTGRES_DATA_DIR", os.environ["SUB2API_RESTORE_DATA_ROOT"] + "/postgres_data"),
                    encoding="utf-8",
                )
            elif service == "redis":
                container_path("mount-sub2api-redis").write_text(
                    read_env("SUB2API_REDIS_DATA_DIR", os.environ["SUB2API_RESTORE_DATA_ROOT"] + "/redis_data"),
                    encoding="utf-8",
                )
            if service == "redis":
                redis_dir = Path(read_env("SUB2API_REDIS_DATA_DIR", os.environ["SUB2API_RESTORE_DATA_ROOT"] + "/redis_data"))
                aof = redis_dir / "appendonlydir"
                aof.mkdir(parents=True, exist_ok=True)
                (aof / "appendonly.aof.1.base.rdb").touch()
        raise SystemExit(0)
    raise SystemExit(1)
if args[0] == "inspect":
    if len(args) > 1 and args[1] == "--format":
        template, name = args[2], args[3]
        if template == "{{.State.Status}}":
            print(get_status(name))
        elif template == "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}":
            print("healthy" if get_status(name) == "running" else "exited")
        elif template == "{{.State.Health.Status}}":
            print("healthy" if get_status(name) == "running" else "unhealthy")
        elif template == "{{.Config.Image}}":
            print(configured[name])
        elif template == "{{.Image}}":
            print(image_ids[name])
        else:
            raise SystemExit(1)
    else:
        name = args[1]
        if name in configured:
            payload = container_json(name)
            override = container_path("mount-" + name)
            if override.exists():
                payload[0]["Mounts"][0]["Source"] = override.read_text(encoding="utf-8")
            elif os.environ.get("FAKE_MOUNT_DRIFT") == name:
                payload[0]["Mounts"][0]["Source"] += "-drifted"
            print(json.dumps(payload))
        elif container_path(name).exists():
            print("[{}]")
        else:
            raise SystemExit(1)
    raise SystemExit(0)
if args[:3] == ["image", "inspect", "--format"]:
    print(image_id(args[4]))
    raise SystemExit(0)
if args[0] == "run":
    name = args[args.index("--name") + 1]
    if container_path(name).exists():
        raise SystemExit(1)
    if "-pg-" in name:
        env_file = Path(args[args.index("--env-file") + 1])
        if "PGDATA=/var/lib/postgresql/data" not in env_file.read_text(encoding="utf-8").splitlines():
            raise SystemExit(2)
    container_path(name).touch()
    postgres_source = parse_volume_source(args, "/var/lib/postgresql/data")
    redis_source = parse_volume_source(args, "/data")
    if postgres_source:
        container_path("mount-" + name).write_text(postgres_source, encoding="utf-8")
    elif redis_source:
        container_path("mount-" + name).write_text(redis_source, encoding="utf-8")
    print("fake-container-id")
    raise SystemExit(0)
if args[0] == "logs":
    if os.environ.get("FAKE_PG_INIT_READY", "1") == "1" and "-pg-" in args[1]:
        print("PostgreSQL init process complete; ready for start up.")
    raise SystemExit(0)
if args[0] == "exec":
    remaining = args[1:]
    if remaining and remaining[0] == "-i":
        remaining = remaining[1:]
    container, command = remaining[0], " ".join(remaining[1:])
    if container == "sub2api" and command == "id -u":
        print(os.environ["HOST_UID"])
    elif container == "sub2api" and command == "id -g":
        print(os.environ["HOST_GID"])
    elif container == "sub2api-postgres" and command == "id -u postgres":
        print(os.environ["HOST_UID"])
    elif container == "sub2api-postgres" and command == "id -g postgres":
        print(os.environ["HOST_GID"])
    elif container == "sub2api-redis" and command == "id -u":
        print(os.environ["HOST_UID"])
    elif container == "sub2api-redis" and command == "id -g":
        print(os.environ["HOST_GID"])
    elif "information_schema.tables" in command:
        print(42)
    elif "schema_migrations" in command:
        print(237)
    elif "select 1" in command:
        print(1)
    elif "redis-cli" in command and "CONFIG GET aclfile" in command:
        print("aclfile")
        print("/data/users.acl")
    elif "redis-cli" in command and "DBSIZE" in command:
        print(188)
    elif "redis-cli" in command and "ping" in command:
        print("PONG")
    elif "psql" in command:
        sys.stdin.buffer.read()
    else:
        raise SystemExit(1)
    raise SystemExit(0)
if args[0] == "stop":
    set_status(args[1], "exited")
    raise SystemExit(0)
if args[0] == "rm":
    container_path(args[1]).unlink(missing_ok=True)
    raise SystemExit(0)
raise SystemExit(1)
"""

    @property
    def staging_root(self) -> Path:
        return self.data_root / "restores" / self.target.replace("-", "")

    def environment(self, **overrides: str) -> dict[str, str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
                "BACKUP_ROOT": str(self.backup_root),
                "RESTORE_CONTRACT_VALIDATOR": str(self.contract_validator),
                "BACKUP_MANIFEST_TOOL": str(MANIFEST_TOOL),
                "RESTORE_ENV_SWITCHER": str(ENV_SWITCHER),
                "SUB2API_RESTORE_ENV_FILE": str(self.env_file),
                "SUB2API_RESTORE_RUNTIME_COMPOSE": str(self.runtime_compose),
                "SUB2API_RESTORE_CONTROLLED_COMPOSE": str(self.controlled_compose),
                "SUB2API_RESTORE_DATA_ROOT": str(self.data_root),
                "SUB2API_RESTORE_WORK_ROOT": str(self.work_root),
                "TEST_POSTGRES_ARTIFACT": str(self.postgres_artifact),
                "TEST_REDIS_ARTIFACT": str(self.redis_artifact),
                "TEST_DATA_ARTIFACT": str(self.data_artifact),
                "TEST_TASK_ID": self.task_id,
                "FAKE_DOCKER_STATE": str(self.fake_state),
                "FAKE_DOCKER_LOG": str(self.docker_log),
                "HOST_UID": str(os.getuid()),
                "HOST_GID": str(os.getgid()),
            }
        )
        environment.update(overrides)
        return environment

    def run_phase(self, phase: str, **overrides: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["bash", str(SCRIPT), "restore", phase, str(self.operation), self.target, ""],
            text=True,
            capture_output=True,
            env=self.environment(**overrides),
            check=False,
        )

    def prepare_quiesced(self) -> None:
        preflight = self.run_phase("preflight")
        self.assertEqual(preflight.returncode, 0, preflight.stderr)
        quiesce = self.run_phase("quiesce")
        self.assertEqual(quiesce.returncode, 0, quiesce.stderr)

    def test_full_restore_preserves_layout_and_resumes_only_after_verify(self) -> None:
        self.prepare_quiesced()
        restored = self.run_phase("restore")
        self.assertEqual(restored.returncode, 0, restored.stderr)
        self.assertTrue(json.loads(restored.stdout)["data"]["productionChanged"])
        self.assertTrue((self.staging_root / "data" / "config.yaml").is_file())
        self.assertFalse((self.staging_root / "data" / "data").exists())
        self.assertTrue((self.staging_root / "redis_data" / "dump.rdb").is_file())
        self.assertTrue((self.staging_root / "redis_data" / "users.acl").is_file())
        self.assertTrue((self.staging_root / "redis_data" / "appendonlydir").is_dir())
        self.assertEqual((self.fake_state / "status-sub2api").read_text().strip(), "exited")
        ownership = self.docker_log.read_text(encoding="utf-8")
        identity = f"{os.getuid()}:{os.getgid()}"
        self.assertIn(f"chown -R {identity} {self.staging_root / 'data'}", ownership)
        self.assertIn(f"chown -R {identity} {self.staging_root / 'postgres_data'}", ownership)
        self.assertIn(f"chown -R {identity} {self.staging_root / 'redis_data'}", ownership)
        switched = self.env_file.read_text(encoding="utf-8")
        self.assertIn(f"SUB2API_DATA_DIR={self.staging_root / 'data'}", switched)
        self.assertIn(f"SUB2API_POSTGRES_DATA_DIR={self.staging_root / 'postgres_data'}", switched)
        self.assertIn(f"SUB2API_REDIS_DATA_DIR={self.staging_root / 'redis_data'}", switched)
        self.assertEqual((self.operation / "sub2api.env.before").read_text(), self._original_env())

        verified = self.run_phase("verify")
        self.assertEqual(verified.returncode, 0, verified.stderr)
        self.assertEqual((self.fake_state / "status-sub2api").read_text().strip(), "exited")
        resumed = self.run_phase("resume")
        self.assertEqual(resumed.returncode, 0, resumed.stderr)
        self.assertEqual((self.fake_state / "status-sub2api").read_text().strip(), "running")

        log = self.docker_log.read_text(encoding="utf-8")
        marker = "logs areasong-restore-sub2api-pg-"
        self.assertIn(marker, log)
        self.assertLess(log.index(marker), log.index("select 1"))
        self.assertLess(log.index("select 1"), log.index("exec -i areasong-restore-sub2api-pg-"))

    def test_existing_staging_path_fails_before_docker_run_or_env_switch(self) -> None:
        self.prepare_quiesced()
        self.staging_root.mkdir(parents=True)
        self.docker_log.write_text("", encoding="utf-8")
        result = self.run_phase("restore")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("staging Sub2API restore directory already exists", result.stderr)
        self.assertEqual(self.env_file.read_text(), self._original_env())
        self.assertNotIn("run --detach", self.docker_log.read_text())

    def test_preflight_rejects_runtime_mount_drift(self) -> None:
        result = self.run_phase("preflight", FAKE_MOUNT_DRIFT="sub2api-postgres")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("PostgreSQL mount differs from its env binding", result.stderr)
        self.assertFalse((self.operation / "sub2api-production-state.json").exists())

    def test_existing_staging_container_is_not_removed(self) -> None:
        self.prepare_quiesced()
        name = f"areasong-restore-sub2api-pg-{self.target.replace('-', '')}"
        marker = self.fake_state / f"container-{name}"
        marker.touch()
        self.docker_log.write_text("", encoding="utf-8")
        result = self.run_phase("restore")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("staging PostgreSQL container already exists", result.stderr)
        self.assertTrue(marker.exists())
        log = self.docker_log.read_text()
        self.assertNotIn("rm ", log)
        self.assertNotIn("run --detach", log)

    def test_missing_redis_acl_is_rejected_before_containers_start(self) -> None:
        self.prepare_quiesced()
        self._write_redis_archive(include_acl=False)
        self.docker_log.write_text("", encoding="utf-8")
        result = self.run_phase("restore")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not declare its ACL file", result.stderr)
        self.assertEqual(self.env_file.read_text(), self._original_env())
        self.assertNotIn("run --detach", self.docker_log.read_text())

    def test_redis_archive_with_old_aof_is_rejected(self) -> None:
        self.prepare_quiesced()
        self._write_redis_archive(include_old_aof=True)
        self.docker_log.write_text("", encoding="utf-8")
        result = self.run_phase("restore")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("legacy AOF", result.stderr)
        self.assertEqual(self.env_file.read_text(), self._original_env())
        self.assertNotIn("run --detach", self.docker_log.read_text())

    def test_postgres_import_waits_for_final_init_marker(self) -> None:
        self.prepare_quiesced()
        result = self.run_phase("restore", FAKE_PG_INIT_READY="0")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("did not reach final ready state", result.stderr)
        self.assertEqual(self.env_file.read_text(), self._original_env())
        log = self.docker_log.read_text()
        self.assertNotIn("exec -i areasong-restore-sub2api-pg-", log)
        self.assertNotIn("run --detach --name areasong-restore-sub2api-redis-", log)

    def test_failure_after_atomic_switch_does_not_restore_old_bindings(self) -> None:
        self.prepare_quiesced()
        result = self.run_phase("restore", FAKE_COMPOSE_UP_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        switched = self.env_file.read_text(encoding="utf-8")
        self.assertIn(f"SUB2API_DATA_DIR={self.staging_root / 'data'}", switched)
        self.assertIn(f"SUB2API_POSTGRES_DATA_DIR={self.staging_root / 'postgres_data'}", switched)
        self.assertIn(f"SUB2API_REDIS_DATA_DIR={self.staging_root / 'redis_data'}", switched)
        self.assertEqual((self.operation / "sub2api.env.before").read_text(), self._original_env())

    def test_verify_rejects_replaced_staging_directory(self) -> None:
        self.prepare_quiesced()
        restored = self.run_phase("restore")
        self.assertEqual(restored.returncode, 0, restored.stderr)
        redis_dir = self.staging_root / "redis_data"
        renamed = self.staging_root / "redis_data-original"
        redis_dir.rename(renamed)
        redis_dir.symlink_to(renamed, target_is_directory=True)
        verified = self.run_phase("verify")
        self.assertNotEqual(verified.returncode, 0)
        self.assertIn("Sub2API data path is unsafe", verified.stderr)
        self.assertFalse((self.operation / "restore-verified").exists())


if __name__ == "__main__":
    unittest.main()
