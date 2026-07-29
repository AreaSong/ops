from __future__ import annotations

import datetime as dt
import hashlib
import json
import os
import subprocess
import tempfile
import unittest
import uuid
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
CONTROL = REPO_ROOT / "scripts" / "deploy" / "update-control.py"
AREAFORGE_ADAPTER = REPO_ROOT / "scripts" / "deploy" / "update-control" / "adapters" / "areaforge.sh"
AREAFORGE_RELEASES = REPO_ROOT / "scripts" / "deploy" / "update-control" / "releases" / "areaforge.json"
SUB2API_ADAPTER = REPO_ROOT / "scripts" / "deploy" / "update-control" / "adapters" / "sub2api.sh"
SUB2API_RELEASES = REPO_ROOT / "scripts" / "deploy" / "update-control" / "releases" / "sub2api.json"
SERVICE_CATALOG = REPO_ROOT / "scripts" / "deploy" / "update-control" / "services.json"


class UpdateControlTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.adapters = self.root / "adapters"
        self.adapters.mkdir()
        self.adapter_log = self.root / "adapter.log"
        self.adapter = self.adapters / "fake.sh"
        self.adapter.write_text(
            """#!/usr/bin/env bash
set -eu
phase="$1"
printf '%s\\n' "$phase" >>"$FAKE_ADAPTER_LOG"
if [ "${FAKE_FAIL_PHASE:-}" = "$phase" ]; then
  echo "forced failure: $phase" >&2
  exit 1
fi
printf '{"ok":true,"phase":"%s","detail":"test"}\\n' "$phase"
""",
            encoding="utf-8",
        )
        self.adapter.chmod(0o755)
        self.catalog = self.root / "services.json"
        self.catalog.write_text(
            json.dumps(
                {
                    "schemaVersion": 1,
                    "services": {
                        "areaforge": {
                            "enabled": True,
                            "adapter": "fake.sh",
                            "actions": ["apply"],
                            "targets": ["v0.1.9"],
                        },
                        "sub2api": {
                            "enabled": False,
                            "adapter": "fake.sh",
                            "actions": ["apply"],
                            "targets": [],
                        },
                    },
                }
            ),
            encoding="utf-8",
        )
        self.state = self.root / "state"

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def request(self, **overrides: object) -> dict[str, object]:
        now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
        request: dict[str, object] = {
            "schemaVersion": 1,
            "id": f"update_{int(now.timestamp() * 1000)}_{uuid.uuid4()}",
            "idempotencyKey": str(uuid.uuid4()),
            "service": "areaforge",
            "action": "apply",
            "status": "queued",
            "requestedAt": now.isoformat().replace("+00:00", "Z"),
            "expiresAt": (now + dt.timedelta(minutes=5)).isoformat().replace("+00:00", "Z"),
            "actorEmailHash": hashlib.sha256(b"operator@example.invalid").hexdigest(),
            "targetId": "v0.1.9",
            "expectedBefore": {
                "currentVersion": "0.1.9",
                "currentImage": "registry.example/app:v0.1.9@sha256:" + "a" * 64,
                "currentImageId": "sha256:" + "b" * 64,
                "runtimeIdentityHash": "sha256:" + "c" * 64,
                "autoApply": "none",
                "signatureRequired": True,
                "rollbackAvailable": False,
                "rollbackTargetVersion": None,
                "rollbackTargetImage": None,
                "rollbackSourceRecordSha256": None,
            },
        }
        request.update(overrides)
        return request

    def run_request(self, request: dict[str, object], **environment: str) -> subprocess.CompletedProcess[str]:
        request_path = self.root / f"request-{uuid.uuid4()}.json"
        request_path.write_text(json.dumps(request), encoding="utf-8")
        request_path.chmod(0o600)
        process_environment = os.environ.copy()
        process_environment.update(
            {
                "OPS_UPDATE_CONTROL_TEST_MODE": "1",
                "OPS_UPDATE_CONTROL_CATALOG": str(self.catalog),
                "OPS_UPDATE_CONTROL_ADAPTER_DIR": str(self.adapters),
                "OPS_UPDATE_CONTROL_STATE_ROOT": str(self.state),
                "FAKE_ADAPTER_LOG": str(self.adapter_log),
            }
        )
        process_environment.update(environment)
        return subprocess.run(
            ["python3", str(CONTROL), str(request_path)],
            text=True,
            capture_output=True,
            env=process_environment,
            check=False,
        )

    def test_success_runs_complete_phase_contract_and_audits(self) -> None:
        result = self.run_request(self.request())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            self.adapter_log.read_text(encoding="utf-8").splitlines(),
            ["preflight", "backup", "migration", "apply", "health", "smoke", "identity"],
        )
        output = json.loads(result.stdout)
        self.assertEqual(output["status"], "succeeded")
        audit = (self.state / "audit.jsonl").read_text(encoding="utf-8").splitlines()
        self.assertEqual(json.loads(audit[0])["event"], "accepted")
        self.assertEqual(json.loads(audit[-1])["event"], "terminal")

    def test_idempotent_success_is_not_executed_twice(self) -> None:
        request = self.request()
        first = self.run_request(request)
        second = self.run_request(request)
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(len(self.adapter_log.read_text(encoding="utf-8").splitlines()), 7)

    def test_idempotency_key_cannot_be_reused_for_other_request(self) -> None:
        request = self.request()
        self.assertEqual(self.run_request(request).returncode, 0)
        changed = self.request(idempotencyKey=request["idempotencyKey"])
        result = self.run_request(changed)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("already used", result.stderr)

    def test_expired_request_is_rejected_before_adapter(self) -> None:
        old = dt.datetime.now(dt.timezone.utc).replace(microsecond=0) - dt.timedelta(hours=1)
        request = self.request(
            requestedAt=old.isoformat().replace("+00:00", "Z"),
            expiresAt=(old + dt.timedelta(minutes=5)).isoformat().replace("+00:00", "Z"),
        )
        result = self.run_request(request)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not currently valid", result.stderr)
        self.assertFalse(self.adapter_log.exists())

    def test_disabled_service_and_unknown_target_fail_closed(self) -> None:
        disabled = self.run_request(self.request(service="sub2api", targetId="v0.1.168"))
        self.assertNotEqual(disabled.returncode, 0)
        self.assertIn("disabled", disabled.stderr)
        unknown = self.run_request(self.request(targetId="arbitrary-image"))
        self.assertNotEqual(unknown.returncode, 0)
        self.assertIn("not allowlisted", unknown.stderr)
        self.assertFalse(self.adapter_log.exists())

    def test_post_apply_failure_invokes_rollback(self) -> None:
        result = self.run_request(self.request(), FAKE_FAIL_PHASE="health")
        self.assertNotEqual(result.returncode, 0)
        output = json.loads(result.stdout)
        self.assertEqual(output["status"], "rolled_back")
        self.assertEqual(
            self.adapter_log.read_text(encoding="utf-8").splitlines(),
            ["preflight", "backup", "migration", "apply", "health", "rollback"],
        )

    def test_request_permissions_are_enforced(self) -> None:
        request_path = self.root / "unsafe.json"
        request_path.write_text(json.dumps(self.request()), encoding="utf-8")
        request_path.chmod(0o644)
        environment = os.environ.copy()
        environment["OPS_UPDATE_CONTROL_TEST_MODE"] = "1"
        result = subprocess.run(
            ["python3", str(CONTROL), str(request_path)],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("mode 0600", result.stderr)


class AreaForgeAdapterTests(unittest.TestCase):
    def test_preflight_builds_valid_strict_v2_guard(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            image = "ghcr.io/areasong/areaforge-web:v0.1.9@sha256:" + "a" * 64
            image_id = "sha256:" + "b" * 64
            identity_hash = "sha256:" + "c" * 64
            (fake_bin / "docker").write_text(
                f"#!/bin/sh\ncase \"$*\" in *Config.Image*) echo '{image}' ;; *) echo '{image_id}' ;; esac\n",
                encoding="utf-8",
            )
            (fake_bin / "curl").write_text(
                "#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"version\":\"0.1.9\",\"runtimeIdentity\":{\"status\":\"verified\",\"identityHash\":\""
                + identity_hash
                + "\"}}'\n",
                encoding="utf-8",
            )
            (fake_bin / "docker").chmod(0o755)
            (fake_bin / "curl").chmod(0o755)
            required = [root / name for name in ("updater", "smoke", "controlled.yml", "runtime.yml", "env")]
            for path in required:
                path.write_text("test\n", encoding="utf-8")
            operation = root / "operation"
            operation.mkdir()
            now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
            request = {
                "schemaVersion": 1,
                "id": f"update_{int(now.timestamp() * 1000)}_{uuid.uuid4()}",
                "idempotencyKey": str(uuid.uuid4()),
                "service": "areaforge",
                "action": "apply",
                "status": "queued",
                "requestedAt": now.isoformat().replace("+00:00", "Z"),
                "expiresAt": (now + dt.timedelta(minutes=5)).isoformat().replace("+00:00", "Z"),
                "actorEmailHash": "d" * 64,
                "targetId": "v0.1.9",
                "expectedBefore": {
                    "currentVersion": "0.1.9",
                    "currentImage": image,
                    "currentImageId": image_id,
                    "runtimeIdentityHash": identity_hash,
                    "autoApply": "none",
                    "signatureRequired": True,
                    "rollbackAvailable": False,
                    "rollbackTargetVersion": None,
                    "rollbackTargetImage": None,
                    "rollbackSourceRecordSha256": None,
                },
            }
            request_path = root / "request.json"
            request_path.write_text(json.dumps(request), encoding="utf-8")
            environment = os.environ.copy()
            environment.update(
                {
                    "PATH": f"{fake_bin}:{environment['PATH']}",
                    "AREAFORGE_UPDATE_CONTROL_RELEASES": str(AREAFORGE_RELEASES),
                    "AREAFORGE_UPDATE_CONTROL_UPDATER": str(required[0]),
                    "AREAFORGE_UPDATE_CONTROL_SMOKE": str(required[1]),
                    "AREAFORGE_UPDATE_CONTROL_CONTROLLED_COMPOSE": str(required[2]),
                    "AREAFORGE_UPDATE_CONTROL_RUNTIME_COMPOSE": str(required[3]),
                    "AREAFORGE_UPDATE_CONTROL_ENV_FILE": str(required[4]),
                }
            )
            result = subprocess.run(
                ["bash", str(AREAFORGE_ADAPTER), "preflight", str(request_path), str(operation)],
                text=True,
                capture_output=True,
                env=environment,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            guard = json.loads((operation / "areaforge-request-v2.json").read_text(encoding="utf-8"))

            def hash_value(value: object) -> str:
                encoded = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
                return "sha256:" + hashlib.sha256(encoded).hexdigest()

            expected_projection = {
                "domain": "areaforge.update-request.expected-before.v2",
                "expectedBefore": guard["expectedBefore"],
            }
            self.assertEqual(guard["expectedBeforeHash"], hash_value(expected_projection))
            semantic_projection = {
                "domain": "areaforge.update-request.semantic.v2",
                "action": guard["action"],
                "params": guard["params"],
                "target": guard["target"],
                "expectedBefore": guard["expectedBefore"],
            }
            self.assertEqual(guard["semanticHash"], hash_value(semantic_projection))
            request_projection = {
                "domain": "areaforge.update-request.v2",
                **{key: guard[key] for key in guard if key != "requestHash"},
            }
            self.assertEqual(guard["requestHash"], hash_value(request_projection))


class Sub2APIAdapterTests(unittest.TestCase):
    def test_preflight_verifies_pinned_identity_and_captures_dependencies(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            fake_bin = root / "bin"
            fake_bin.mkdir()
            old_digest = "ac3013098153fd276e7fb0723bbb7032f290e1de018dd7b21a238c298064ff5d"
            target_digest = "8c94357c48d6cad360159b14a4bee913a6375520845593ec942cdd59506855e0"
            old_image = f"weishaw/sub2api@sha256:{old_digest}"
            fake_docker = fake_bin / "docker"
            fake_docker.write_text(
                f"""#!/usr/bin/env bash
set -eu
if [ "$1" = inspect ] && [ "${{2:-}}" = --format ] && [ "${{4:-}}" = sub2api ]; then
  case "$3" in
    *Config.Image*) printf '%s\\n' '{old_image}' ;;
    *Image*) printf '%s\\n' 'sha256:{old_digest}' ;;
    *) exit 1 ;;
  esac
  exit 0
fi
case "$*" in
  "image inspect sha256:{old_digest}") printf '%s\\n' '[{{"Id":"sha256:{old_digest}","Config":{{"Labels":{{"org.opencontainers.image.version":"0.1.163","org.opencontainers.image.revision":"d0bdd7e771636a8d315f542cafd39484f39bd60c"}}}}}}]' ;;
  "image inspect sha256:{target_digest}") printf '%s\\n' '[{{"Id":"sha256:{target_digest}","Config":{{"Labels":{{"org.opencontainers.image.version":"0.1.168","org.opencontainers.image.revision":"99c8e4bf7564823bafbab369acab6539e734c1bb"}}}}}}]' ;;
  "inspect sub2api-postgres") printf '%s\\n' '[{{"Config":{{"Env":["POSTGRES_USER=sub2api","POSTGRES_DB=sub2api"]}}}}]' ;;
  "exec sub2api-postgres psql -v ON_ERROR_STOP=1 -U sub2api -d sub2api -Atc SELECT count(*) FROM schema_migrations;") printf '229\\n' ;;
  "inspect sub2api-postgres sub2api-redis") printf '%s\\n' '[{{"Name":"/sub2api-postgres","Id":"pg-id","State":{{"StartedAt":"pg-start"}},"Config":{{"Image":"postgres@sha256:test"}}}},{{"Name":"/sub2api-redis","Id":"redis-id","State":{{"StartedAt":"redis-start"}},"Config":{{"Image":"redis@sha256:test"}}}}]' ;;
  "inspect sub2api") printf '%s\\n' '[{{"Name":"/sub2api","Id":"app-id","State":{{"StartedAt":"app-start"}},"Config":{{"Image":"{old_image}"}}}}]' ;;
  *) printf 'unexpected docker invocation: %s\\n' "$*" >&2; exit 1 ;;
esac
""",
                encoding="utf-8",
            )
            fake_docker.chmod(0o755)
            compose = root / "compose.yml"
            compose.write_text(f"services:\n  sub2api:\n    image: {old_image}\n", encoding="utf-8")
            controlled = root / "controlled.yml"
            controlled.write_bytes(compose.read_bytes())
            env_file = root / ".env"
            env_file.write_text("POSTGRES_USER=sub2api\n", encoding="utf-8")
            backup_scripts = [root / name for name in ("backup-postgres", "backup-redis", "backup-volumes")]
            for path in backup_scripts:
                path.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
                path.chmod(0o755)
            operation = root / "operation"
            operation.mkdir()
            request = {
                "targetId": "v0.1.168",
                "expectedBefore": {
                    "currentVersion": "0.1.163",
                    "currentImage": old_image,
                    "currentImageId": f"sha256:{old_digest}",
                    "runtimeIdentityHash": "sha256:c1b5609eca7d08baf453411956236bb9397e0ace72b670ce1447795104132cd1",
                    "autoApply": "none",
                    "signatureRequired": False,
                    "rollbackAvailable": True,
                    "rollbackTargetVersion": "0.1.163",
                    "rollbackTargetImage": old_image,
                    "rollbackSourceRecordSha256": "sha256:c5a0aab13dc4c81a5698b12656a9285f1017984a07c7f76d2d647f4380118ba1",
                },
            }
            request_path = root / "request.json"
            request_path.write_text(json.dumps(request), encoding="utf-8")
            environment = os.environ.copy()
            environment.update(
                {
                    "PATH": f"{fake_bin}:{environment['PATH']}",
                    "SUB2API_UPDATE_CONTROL_RELEASES": str(SUB2API_RELEASES),
                    "SUB2API_UPDATE_CONTROL_CONTROLLED_COMPOSE": str(controlled),
                    "SUB2API_UPDATE_CONTROL_RUNTIME_COMPOSE": str(compose),
                    "SUB2API_UPDATE_CONTROL_ENV_FILE": str(env_file),
                    "SUB2API_UPDATE_CONTROL_BACKUP_POSTGRES": str(backup_scripts[0]),
                    "SUB2API_UPDATE_CONTROL_BACKUP_REDIS": str(backup_scripts[1]),
                    "SUB2API_UPDATE_CONTROL_BACKUP_VOLUMES": str(backup_scripts[2]),
                }
            )
            result = subprocess.run(
                ["bash", str(SUB2API_ADAPTER), "preflight", str(request_path), str(operation)],
                text=True,
                capture_output=True,
                env=environment,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["phase"], "preflight")
            dependencies = json.loads((operation / "dependencies.before.json").read_text(encoding="utf-8"))
            self.assertEqual([item["name"] for item in dependencies], ["/sub2api-postgres", "/sub2api-redis"])

    def test_release_catalog_retains_completed_rehearsal_and_disables_replay(self) -> None:
        releases = json.loads(SUB2API_RELEASES.read_text(encoding="utf-8"))
        self.assertEqual(set(releases["targets"]), {"v0.1.168"})
        target = releases["targets"]["v0.1.168"]
        self.assertEqual(target["status"], "completed")
        self.assertEqual(target["migration"], {"baselineCount": 229, "targetCount": 237, "newCount": 8})
        self.assertEqual(target["rehearsal"]["oldImageOnNewSchema"], "healthy")
        service = json.loads(SERVICE_CATALOG.read_text(encoding="utf-8"))["services"]["sub2api"]
        self.assertFalse(service["enabled"])
        self.assertEqual(service["targets"], [])


if __name__ == "__main__":
    unittest.main()
