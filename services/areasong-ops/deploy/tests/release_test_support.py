from __future__ import annotations

import hashlib
import json
import os
import shlex
import sqlite3
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

DEPLOY = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(DEPLOY))
import release_orchestrator as MODULE
from release_snapshot import ACTIVE_STATES


class ReleaseFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.environment = patch.dict(os.environ, {"OPS_RELEASE_TEST_MODE": "1"})
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.revision, self.old_revision = "a" * 40, "c" * 40
        self.version = "1.1.8"
        self.commands = []
        self.web_running = self.runner_running = True
        self.runner_revision = self.old_revision
        self.maintenance = False
        self.updater_units, self.jobs = [], []
        self.fail_web_once = self.fail_activation = self.fail_stop = False
        self.on_command = None
        self.make_assets()
        self.make_runtime()
        self.metadata = MODULE.parse_manifest(self.manifest)
        self.state = MODULE.State(self.args.state_dir, "ops-test", self.metadata, create=True)
        self.orchestrator = MODULE.Orchestrator(self.args, self.state, self.metadata)

    def make_assets(self):
        stage = self.root / "stage"
        stage.mkdir()
        self.archive = self.root / f"areasong-ops-runner-{self.revision}-linux-amd64.tar.gz"
        with tarfile.open(self.archive, "w:gz") as bundle:
            for name in ("areasong-ops-runner", "areasong-ops-runner-updater"):
                path = stage / name
                path.write_text("candidate-" + name)
                path.chmod(0o755)
                bundle.add(path, arcname=name)
        digest = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        self.manifest = self.root / "manifest.json"
        self.manifest.write_text(json.dumps({
            "schemaVersion": 2, "service": "areasong-ops", "version": self.version,
            "revision": self.revision, "platform": "linux/amd64",
            "web": {"image": f"ghcr.io/areasong/areasong-ops-web:{self.revision}@sha256:{'b' * 64}", "cosign": "keyless"},
            "runner": {"archive": self.archive.name, "sha256": "sha256:" + digest, "cosign": "keyless"},
        }))

    def make_runtime(self):
        self.args = SimpleNamespace(
            repo_root=self.root / "repo", runtime_dir=self.root / "runtime",
            config_dir=self.root / "config", runner_root=self.root / "runner",
            unit_path=self.root / "system/areasong-ops-runner.service",
            updater_unit_path=self.root / "system/areasong-ops-runner-update@.service",
            db_path=self.root / "ops.db", socket_path=self.root / "run/runner.sock",
            container_name="areasong-ops-web", preflight="/bin/true",
            candidate_unit=self.root / "candidate.service", candidate_updater_unit=self.root / "candidate-update.service",
            runner_archive=self.archive, state_dir=self.root / "release-orchestrator",
            boot_id_path=self.root / "boot-id", cgroup_root=self.root / "cgroups",
        )
        for path in (self.args.repo_root, self.args.runtime_dir, self.args.config_dir,
                     self.args.runner_root / "runner", self.args.runner_root / "adapters", self.args.unit_path.parent):
            path.mkdir(parents=True)
        self.args.unit_path.parent.chmod(0o755)
        files = {
            self.args.runtime_dir / ".env": f"OPS_BUILD_VERSION=1.1.5\nOPS_BUILD_REVISION={self.old_revision}\n",
            self.args.runtime_dir / "compose.yml": "services: {}\n",
            self.args.config_dir / "services.json": '{"schemaVersion":4,"fleet":{"allowRemoteRunners":false}}',
            self.args.config_dir / "web.env": "OPS_PUBLIC_ORIGIN=https://ops.example.invalid\n",
            self.args.runner_root / "runner/areasong-ops-runner": "old-runner",
            self.args.runner_root / "areasong-ops-runner-updater": "old-updater",
            self.args.runner_root / "adapters/test.sh": "#!/bin/sh\nexit 0\n",
            self.args.unit_path: "old-unit", self.args.updater_unit_path: "old-updater-unit",
            self.args.candidate_unit: "candidate-unit", self.args.candidate_updater_unit: "candidate-updater-unit",
            self.args.boot_id_path: "12345678-1234-1234-1234-123456789012\n",
        }
        for path, content in files.items():
            path.write_text(content)
            path.chmod(0o600 if path.parent == self.args.config_dir else 0o755)
        with sqlite3.connect(self.args.db_path) as database:
            database.execute("PRAGMA user_version=45")
            for name in ACTIVE_STATES:
                database.execute(f"CREATE TABLE {name}(state TEXT)")
            database.execute("CREATE TABLE kubernetes_operations(state TEXT,finished_at TEXT)")
            database.execute("CREATE TABLE app_state(value TEXT)")
            database.execute("INSERT INTO app_state VALUES ('old')")
        self.args.db_path.chmod(0o600)

    @staticmethod
    def result(output="", code=0):
        return SimpleNamespace(returncode=code, stdout=output, stderr="")

    def fake_run(self, command, **options):
        self.commands.append(command)
        if self.on_command:
            custom = self.on_command(command)
            if custom is not None:
                return custom
        if command[0] == "git":
            return self.result(self.revision + "\n" if "rev-parse" in command else "")
        if command[-1] == "--release-info":
            self.assertFalse(Path(options["env"]["OPS_SERVICE_CATALOG"]).exists())
            return self.result(json.dumps({"releaseProtocol": 1, "revision": self.revision, "version": self.version, "schemaVersion": 47}))
        if command[0] == "systemctl":
            return self.fake_systemctl(command)
        if command[:2] == ["docker", "inspect"]:
            revision = MODULE.read_env(self.args.runtime_dir / ".env")["OPS_BUILD_REVISION"] if hasattr(MODULE, "read_env") else self.old_revision
            return self.result(json.dumps([{
                "Image": "sha256:" + "d" * 64, "Config": {"Labels": {"org.opencontainers.image.revision": revision}},
                "State": {"Running": self.web_running, "Status": "running" if self.web_running else "exited"},
            }]))
        if command[:3] == ["docker", "image", "inspect"]:
            return self.result(json.dumps([{"Os": "linux", "Architecture": "amd64", "Id": "sha256:" + "e" * 64,
                "RepoDigests": ["ghcr.io/areasong/areasong-ops-web@sha256:" + "b" * 64],
                "Config": {"Labels": {"org.opencontainers.image.revision": self.revision}}}]))
        if command[:2] == ["docker", "compose"]:
            if "stop" in command:
                self.web_running = False
            if "up" in command:
                if self.fail_web_once:
                    self.fail_web_once = False
                    return self.result(code=1)
                self.web_running = True
            return self.result()
        if command[0] == "curl":
            return self.result(json.dumps({"ok": True, "revision": self.runner_revision, "releaseMaintenance": self.maintenance,
                "releaseProtocol": 1, "deploymentId": self.state.deployment_id, "schemaVersion": 47}))
        if command[0] == "/bin/true" and command[-1] == "runtime" and self.fail_activation and self.state.data.get("activationStarted"):
            return self.result(code=1)
        return self.result()

    def fake_systemctl(self, command):
        action = command[1]
        if action == "list-units":
            return self.result(json.dumps(self.updater_units))
        if action == "list-jobs":
            self.assertEqual(command, ["systemctl", "list-jobs", "--no-legend", "--plain", "--full", "--no-pager"])
            return self.result("".join(
                f"{index} {job['unit']} {job.get('type', 'start')} {job.get('state', 'waiting')}\n"
                for index, job in enumerate(self.jobs, 1)
            ))
        if action == "show":
            environment = shlex.join([
                f"OPS_STATE_ROOT={self.args.db_path.parent}", f"OPS_SERVICE_CATALOG={self.args.config_dir / 'services.json'}",
                f"OPS_RUNNER_SOCKET={self.args.socket_path}",
            ])
            return self.result("\n".join([
                "LoadState=loaded", "ActiveState=" + ("active" if self.runner_running else "inactive"),
                "SubState=" + ("running" if self.runner_running else "dead"), "MainPID=" + ("123" if self.runner_running else "0"),
                "ControlGroup=", "DropInPaths=", "Environment=" + environment,
                f"ExecStart={{ path={self.args.runner_root / 'runner/areasong-ops-runner'} ; }}", "FragmentPath=" + str(self.args.unit_path),
            ]))
        if action == "stop":
            if self.fail_stop:
                return self.result(code=1)
            self.runner_running = False
        if action in {"start", "restart"}:
            self.runner_running = True
            current = (self.args.runner_root / "runner/areasong-ops-runner").read_text()
            if current == "old-runner":
                self.runner_revision, self.maintenance = self.old_revision, False
            else:
                self.runner_revision = self.revision
                self.maintenance = self.orchestrator.guard.marker.exists()
                with sqlite3.connect(self.args.db_path) as database:
                    database.execute("PRAGMA user_version=47")
                    if not self.maintenance:
                        database.execute("INSERT INTO app_state VALUES ('after-activation')")
        return self.result()

    def deploy(self):
        with patch.object(MODULE, "run", side_effect=self.fake_run):
            self.orchestrator.deploy()

    def schema(self):
        with sqlite3.connect(self.args.db_path) as database:
            return database.execute("PRAGMA user_version").fetchone()[0]
