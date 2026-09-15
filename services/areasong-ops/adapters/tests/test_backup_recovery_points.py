from __future__ import annotations

import hashlib
import json
import os
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
import uuid
from pathlib import Path


REPO = Path(__file__).resolve().parents[4]
ADAPTERS = Path(__file__).resolve().parents[1]
CONFIG_MEMBERS = {
    "areaforge": {"opt/ops/services/areaforge/compose.yml", "opt/areaforge/docker-compose.prod.yml", "opt/areaforge/.env.production"},
    "sub2api": {"opt/ops/services/sub2api/compose.yml", "opt/services/sub2api/compose.yml", "opt/services/sub2api/.env"},
}
DATA_FILES = {
    "areaforge": {
        "postgres": [("postgres-areaforge", "postgres/areaforge-postgres-fixture.sql.gz")],
        "volumes": [("volume-areaforge-uploads", "volumes/areaforge-uploads-fixture.tar.gz"),
                    ("volume-areaforge-ops-state", "volumes/areaforge-ops-state-fixture.tar.gz")],
    },
    "sub2api": {
        "postgres": [("postgres-sub2api", "postgres/sub2api-postgres-fixture.sql.gz")],
        "redis": [("redis", "redis/redis-fixture.tar.gz")],
        "volumes": [("volume-sub2api-data", "volumes/sub2api-data-fixture.tar.gz")],
    },
}

FAKE_DOCKER = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
with Path(os.environ["FAKE_DOCKER_LOG"]).open("a") as log:
    log.write(json.dumps(args) + "\n")
containers = json.loads(os.environ["FAKE_CONTAINERS"])
if args[:2] == ["image", "inspect"]:
    item = next(value for value in containers.values() if value["Image"] == args[2])
    print(json.dumps([{"Id": args[2], "Config": {"Labels": item["labels"]}}]))
elif args[0] == "inspect":
    item = containers[args[-1]]
    if "--format" not in args:
        print(json.dumps([item]))
    else:
        values = {"{{.Config.Image}}": item["Config"]["Image"], "{{.Image}}": item["Image"],
                  "{{.State.Status}}": "running",
                  "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}": "healthy"}
        print(values[args[2]])
elif args[0] == "exec":
    assert args[1] == "sub2api-postgres" and args[2:] == ["psql", "-v", "ON_ERROR_STOP=1", "-U", "fixture_user", "-d", "fixture_db", "-Atc", "SELECT count(*) FROM schema_migrations;"]
    print("7")
else:
    sys.exit("unexpected Docker invocation")
'''

FAKE_BACKUP = r'''#!/usr/bin/env python3
import gzip, io, json, os, sys, tarfile
from pathlib import Path
group = Path(sys.argv[0]).stem.removeprefix("backup-")
for _, relative in json.loads(os.environ["FAKE_DATA_FILES"])[group]:
    path = Path(os.environ["BACKUP_ROOT"]) / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.name.endswith(".sql.gz"):
        with gzip.open(path, "wb") as output:
            output.write(b"SELECT 'fixture';\n")
    else:
        with tarfile.open(path, "w:gz") as output:
            entry = tarfile.TarInfo("fixture")
            entry.size = 7
            output.addfile(entry, io.BytesIO(b"fixture"))
    path.chmod(0o600)
    print(path)
'''

FAKE_LEGACY = r'''#!/usr/bin/env python3
import json, os, subprocess, sys
from pathlib import Path
assert sys.argv[1] == "backup"
for group in ("postgres", "redis", "volumes"):
    with (Path(sys.argv[3]) / ("backup-" + group + ".log")).open("w") as output:
        subprocess.run([os.environ["SUB2API_OPS_BACKUP_" + group.upper()]], stdout=output, check=True)
print(json.dumps({"ok": True, "phase": "backup", "detail": "fixture backup complete"}))
'''


def write_file(path: Path, content: str, mode: int = 0o600) -> Path:
    path.write_text(content, encoding="utf-8")
    path.chmod(mode)
    return path


class BackupProducerFixture:
    def __init__(self, root: Path, service: str, action: str) -> None:
        self.root, self.service, self.action = root, service, action
        self.task_id = str(uuid.uuid4())
        self.operation = root / self.task_id
        self.operation.mkdir(mode=0o700)
        self.backups = root / "backups"
        self.backups.mkdir(mode=0o700)
        self.bin_dir = root / "bin"
        self.bin_dir.mkdir()
        self.image_id = "sha256:" + "a" * 64
        self.image = "registry.invalid/fixture@" + self.image_id
        self.before = {"currentVersion": "1.1.1", "currentImage": self.image, "currentImageId": self.image_id,
                       "runtimeIdentityHash": "sha256:" + "b" * 64, "autoApply": "none", "signatureRequired": True,
                       "rollbackAvailable": False, "rollbackTargetVersion": None, "rollbackTargetImage": None,
                       "rollbackSourceRecordSha256": None}
        self.env_file = write_file(root / ".env", "FIXTURE_ONLY=1\n")
        self.environment = self.prepare_environment()
        write_file(self.operation / "task-contract.json", json.dumps({"schemaVersion": 1, "taskId": self.task_id,
                   "actorHash": "c" * 64, "service": service, "action": action, "expectedBefore": self.before}))

    def prepare_environment(self) -> dict[str, str]:
        prefix = self.service.upper() + "_OPS_"
        environment = dict(os.environ, PATH=str(self.bin_dir) + os.pathsep + os.environ["PATH"],
                           BACKUP_ROOT=str(self.backups), FAKE_DOCKER_LOG=str(self.root / "docker.log"),
                           FAKE_DATA_FILES=json.dumps(DATA_FILES[self.service]), FAKE_EXPECTED_BEFORE=json.dumps(self.before))
        for name in ("stat", "date"):
            executable = shutil.which("g" + name) if sys.platform == "darwin" else shutil.which(name)
            if not executable:
                raise unittest.SkipTest("backup producer regression requires GNU " + name)
            write_file(self.bin_dir / name, "#!/bin/sh\nexec " + shlex.quote(executable) + ' "$@"\n', 0o700)
        write_file(self.bin_dir / "docker", FAKE_DOCKER, 0o700)
        for group in DATA_FILES[self.service]:
            path = write_file(self.bin_dir / ("backup-" + group + ".sh"), FAKE_BACKUP, 0o700)
            environment[prefix + "BACKUP_" + group.upper()] = str(path)
        for field in ("CONTROLLED_COMPOSE", "RUNTIME_COMPOSE"):
            environment[prefix + field] = str(write_file(self.root / field, "services: {}\n"))
        environment[prefix + "ENV_FILE"] = str(self.env_file)
        environment[prefix + "RECOVERY_METADATA"] = str(REPO / "scripts/backup/restore_point_metadata.py")
        environment["FAKE_CONTAINERS"] = json.dumps(self.containers())
        self.prepare_update_dependencies(environment)
        return environment

    def containers(self) -> dict:
        app = "areaforge-web" if self.service == "areaforge" else "sub2api"
        names = [app, self.service + "-postgres"] + (["sub2api-redis"] if self.service == "sub2api" else [])
        return {name: {"Id": "container-" + name, "Image": "sha256:" + char * 64,
                       "Config": {"Image": self.image if index == 0 else "registry.invalid/" + name,
                                  "Env": ["POSTGRES_USER=fixture_user", "POSTGRES_DB=fixture_db", "SECRET=must-not-appear"]},
                       "labels": {"org.opencontainers.image.version": "1.1.1", "org.opencontainers.image.revision": "d" * 40}}
                for index, (name, char) in enumerate(zip(names, "abc"))}

    def prepare_update_dependencies(self, environment: dict[str, str]) -> None:
        updater = write_file(self.bin_dir / "updater", '#!/bin/bash\nload_config() { :; }\nobserved_before_json() { printf "%s\\n" "$FAKE_EXPECTED_BEFORE"; }\n', 0o700)
        environment["AREAFORGE_OPS_UPDATER"] = str(updater)
        health = {"ok": True, "service": "AreaForge", "version": "1.1.1",
                  "runtimeIdentity": {"status": "verified", "identityHash": self.before["runtimeIdentityHash"], "gitCommit": "d" * 40}}
        write_file(self.bin_dir / "curl", "#!/bin/sh\nprintf '%s\\n' " + shlex.quote(json.dumps(health)) + "\n", 0o700)
        releases = {"schemaVersion": 1, "targets": {"v1.2.0": {"status": "prepared", "expectedBefore": self.before}}}
        environment["SUB2API_OPS_RELEASES"] = str(write_file(self.root / "releases.json", json.dumps(releases)))
        environment["SUB2API_OPS_UPDATE_ADAPTER"] = str(write_file(self.bin_dir / "legacy", FAKE_LEGACY, 0o700))

    def run(self) -> subprocess.CompletedProcess[str]:
        return subprocess.run(["bash", str(ADAPTERS / (self.service + ".sh")), self.action, "backup",
                               str(self.operation), "v1.2.0", ""], env=self.environment,
                              text=True, capture_output=True, check=False, timeout=30)


class BackupRecoveryPointTests(unittest.TestCase):
    def test_backup_and_update_producers_return_five_verified_roles(self) -> None:
        for service in DATA_FILES:
            for action in ("backup", "update"):
                with self.subTest(service=service, action=action), tempfile.TemporaryDirectory() as temporary:
                    fixture = BackupProducerFixture(Path(temporary).resolve(), service, action)
                    result = fixture.run()
                    self.assertEqual(result.returncode, 0, result.stderr)
                    point = json.loads(result.stdout)["recoveryPoint"]
                    self.assert_point(fixture, point)

    def assert_point(self, fixture: BackupProducerFixture, point: dict) -> None:
        self.assertEqual(point["service"], fixture.service)
        self.assertEqual(point["taskId"], fixture.task_id)
        self.assertEqual(point, json.loads((fixture.operation / "recovery-point.json").read_text()))
        roles = {role for group in DATA_FILES[fixture.service].values() for role, _ in group}
        self.assertEqual({item["role"] for item in point["artifacts"]}, roles | {"configs", "runtime-snapshot"})
        self.assertEqual(len(point["artifacts"]), 5)
        artifacts = {item["role"]: Path(item["path"]) for item in point["artifacts"]}
        for item in point["artifacts"]:
            path = Path(item["path"])
            self.assertTrue(path.is_relative_to(fixture.backups))
            self.assertEqual(item["sizeBytes"], path.stat().st_size)
            self.assertEqual(item["sha256"], "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest())
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        with tarfile.open(artifacts["configs"]) as archive:
            self.assertEqual(set(archive.getnames()), CONFIG_MEMBERS[fixture.service])
            self.assertTrue(all(member.isfile() and member.mode == 0o600 for member in archive.getmembers()))
        runtime = json.loads(artifacts["runtime-snapshot"].read_text())
        self.assertEqual(runtime["containers"]["app"]["image_id"], fixture.image_id)
        self.assertEqual(runtime["database"]["database"], "fixture_db")
        self.assertNotIn("must-not-appear", json.dumps(runtime))
        if fixture.service == "sub2api":
            self.assertEqual(runtime["database"]["migrations"], 7)
        self.assertEqual(list((fixture.backups / "configs").glob("configs-*.tar.gz")), [])

    def test_incomplete_metadata_never_returns_old_three_role_success(self) -> None:
        for service in DATA_FILES:
            for action in ("backup", "update"):
                for invalid in ("missing-env", "unfixed-image"):
                    with self.subTest(service=service, action=action, invalid=invalid), tempfile.TemporaryDirectory() as temporary:
                        fixture = BackupProducerFixture(Path(temporary).resolve(), service, action)
                        if invalid == "missing-env":
                            fixture.environment[service.upper() + "_OPS_ENV_FILE"] = str(fixture.root / "absent.env")
                        else:
                            containers = json.loads(fixture.environment["FAKE_CONTAINERS"])
                            containers[service + "-postgres"]["Image"] = "postgres:latest"
                            fixture.environment["FAKE_CONTAINERS"] = json.dumps(containers)
                        result = fixture.run()
                        self.assertNotEqual(result.returncode, 0)
                        self.assertNotIn('"ok":true', result.stdout)
                        self.assertFalse((fixture.operation / "recovery-point.json").exists())


if __name__ == "__main__":
    unittest.main()
