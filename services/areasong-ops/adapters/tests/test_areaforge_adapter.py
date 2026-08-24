from __future__ import annotations

import hashlib
import json
import os
import subprocess
import tempfile
import unittest
import uuid
from pathlib import Path


ADAPTER = Path(__file__).resolve().parents[1] / "areaforge.sh"


def canonical(value: object) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def hash_value(value: object) -> str:
    return "sha256:" + hashlib.sha256(canonical(value).encode()).hexdigest()


class AreaForgeAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.operation = self.root / "operation"
        self.operation.mkdir()

        self.old_digest = "a" * 64
        self.old_image = f"ghcr.io/areasong/areaforge-web:v1.1.1@sha256:{self.old_digest}"
        self.old_image_id = f"sha256:{self.old_digest}"
        self.runtime_hash = "sha256:" + "b" * 64
        self.git_commit = "c" * 40
        self.target_digest = "d" * 64
        self.target_image = f"ghcr.io/areasong/areaforge-web:v1.2.0@sha256:{self.target_digest}"
        self.target_identity = {
            "releaseId": 12345,
            "manifestSha256": "sha256:" + "e" * 64,
            "manifestVersion": "1.2.0",
            "webImageDigest": self.target_image,
        }
        self.expected_before = {
            "currentVersion": "1.1.1",
            "currentImage": self.old_image,
            "currentImageId": self.old_image_id,
            "runtimeIdentityHash": self.runtime_hash,
            "autoApply": "none",
            "signatureRequired": True,
            "rollbackAvailable": False,
            "rollbackTargetVersion": None,
            "rollbackTargetImage": None,
            "rollbackSourceRecordSha256": None,
        }
        self.updater_before = {
            key: value
            for key, value in self.expected_before.items()
            if key not in {"currentImageId", "runtimeIdentityHash"}
        }

        self.updater = self.root / "areaforge-updater.sh"
        self.capture_guard = self.root / "captured-guard.json"
        self.update_record = self.root / "update-record.txt"
        self._write_updater()
        self._write_docker()
        self._write_curl()
        self._write_support_files()

    def _write_executable(self, path: Path, content: str) -> None:
        path.write_text(content, encoding="utf-8")
        path.chmod(0o755)

    def _write_updater(self) -> None:
        self._write_executable(
            self.updater,
            """#!/usr/bin/env bash
set -Eeuo pipefail
load_config() { :; }
observed_before_json() { printf '%s\\n' "$FAKE_EXPECTED_BEFORE"; }
latest_update_record() { printf '%s\\n' "$FAKE_UPDATE_RECORD"; }
if [[ "${AREAFORGE_UPDATER_NO_MAIN:-0}" == 1 ]]; then
  return 0 2>/dev/null || exit 0
fi
command_name="${1:-}"
shift || true
identity=""
guard=""
tag=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --identity-json) identity="$2"; shift 2 ;;
    --request-guard) guard="$2"; shift 2 ;;
    --tag) tag="$2"; shift 2 ;;
    --config) shift 2 ;;
    --yes) shift ;;
    *) exit 2 ;;
  esac
done
case "$command_name" in
  check)
    [[ -n "$identity" ]]
    printf '%s\\n' "$FAKE_TARGET_IDENTITY" >"$identity"
    ;;
  apply)
    [[ "$tag" == v1.2.0 && -f "$guard" ]]
    cp "$guard" "$FAKE_CAPTURED_GUARD"
    ;;
  *) exit 2 ;;
esac
""",
        )

    def _write_docker(self) -> None:
        self._write_executable(
            self.bin_dir / "docker",
            f"""#!/usr/bin/env bash
set -eu
case "$*" in
  "inspect --format {{{{.Config.Image}}}} areaforge-web") printf '%s\\n' '{self.old_image}' ;;
  "inspect --format {{{{.Image}}}} areaforge-web") printf '%s\\n' '{self.old_image_id}' ;;
  "inspect --format {{{{.State.Status}}}} areaforge-web") printf 'running\\n' ;;
  inspect*areaforge-postgres*)
    if [[ "$*" == *--format* ]]; then
      printf 'healthy\\n'
    else
      printf '%s\\n' '[{{"Name":"/areaforge-postgres","Id":"pg-id","State":{{"StartedAt":"pg-start"}},"Config":{{"Image":"postgres@sha256:test"}}}}]'
    fi
    ;;
  *) printf 'unexpected docker invocation: %s\\n' "$*" >&2; exit 1 ;;
esac
""",
        )

    def _write_curl(self) -> None:
        health = canonical(
            {
                "ok": True,
                "service": "AreaForge",
                "version": "1.1.1",
                "runtimeIdentity": {
                    "status": "verified",
                    "identityHash": self.runtime_hash,
                    "gitCommit": self.git_commit,
                },
            }
        )
        self._write_executable(
            self.bin_dir / "curl",
            f"#!/bin/sh\nprintf '%s\\n' '{health}'\n",
        )
        self._write_executable(self.bin_dir / "flock", "#!/bin/sh\nexit 0\n")

    def _write_support_files(self) -> None:
        self.controlled = self.root / "controlled.yml"
        self.runtime = self.root / "runtime.yml"
        compose = f"services:\n  web:\n    image: ${{AREAFORGE_IMAGE:-{self.old_image}}}\n"
        self.controlled.write_text(compose, encoding="utf-8")
        self.runtime.write_text(compose, encoding="utf-8")
        self.env_file = self.root / "production.env"
        self.env_file.write_text("APP_VERSION=1.1.1\n", encoding="utf-8")
        self.config = self.root / "updater.env"
        self.config.write_text("AREAFORGE_AUTO_APPLY=none\n", encoding="utf-8")
        self.smoke = self.root / "smoke.sh"
        self.backup_postgres = self.root / "backup-postgres.sh"
        self.backup_volumes = self.root / "backup-volumes.sh"
        for script in (self.smoke, self.backup_postgres, self.backup_volumes):
            self._write_executable(script, "#!/bin/sh\nexit 0\n")
        self.update_record.write_text(
            "\n".join(
                [
                    "status: applied",
                    "releaseTag: v1.2.0",
                    "previousAppVersion: 1.1.1",
                    f"previousImage: {self.old_image}",
                    "targetVersion: 1.2.0",
                    f"targetWebImageDigest: {self.target_image}",
                    "migrationApplied: false",
                ]
            )
            + "\n",
            encoding="utf-8",
        )

    def environment(self) -> dict[str, str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
                "AREAFORGE_OPS_UPDATER": str(self.updater),
                "AREAFORGE_OPS_UPDATER_CONFIG": str(self.config),
                "AREAFORGE_OPS_SMOKE": str(self.smoke),
                "AREAFORGE_OPS_CONTROLLED_COMPOSE": str(self.controlled),
                "AREAFORGE_OPS_RUNTIME_COMPOSE": str(self.runtime),
                "AREAFORGE_OPS_ENV_FILE": str(self.env_file),
                "AREAFORGE_OPS_BACKUP_POSTGRES": str(self.backup_postgres),
                "AREAFORGE_OPS_BACKUP_VOLUMES": str(self.backup_volumes),
                "AREAFORGE_OPS_BASE_URL": "http://127.0.0.1:3020",
                "FAKE_EXPECTED_BEFORE": canonical(self.updater_before),
                "FAKE_TARGET_IDENTITY": canonical(self.target_identity),
                "FAKE_UPDATE_RECORD": str(self.update_record),
                "FAKE_CAPTURED_GUARD": str(self.capture_guard),
            }
        )
        return environment

    def _enable_lifecycle_fakes(self) -> Path:
        state = self.root / "app-state"
        state.write_text("running\n", encoding="utf-8")
        docker = self.bin_dir / "docker"
        docker.write_text(
            "\n".join(
                [
                    "#!/usr/bin/env bash",
                    "set -eu",
                    f"state={str(state)!r}",
                    f"log={str(self.root / 'docker-calls.txt')!r}",
                    'if [[ "${1:-}" == compose ]]; then',
                    '  printf "%s\\n" "$*" >>"$log"',
                    '  case "$*" in',
                    '    *" up "*) printf "running\\n" >"$state" ;;',
                    '    *" stop "*) printf "exited\\n" >"$state" ;;',
                    "  esac",
                    "  exit 0",
                    "fi",
                    '[[ "${1:-}" == inspect ]] || { printf "unexpected docker invocation: %s\\n" "$*" >&2; exit 1; }',
                    'if [[ "$*" == *--format* ]]; then',
                    '  format="${3:-}"',
                    '  container="${!#}"',
                    '  if [[ "$container" == areaforge-web ]]; then',
                    '    case "$format" in',
                    '      *Config.Image*) printf "%s\\n" ' + repr(self.old_image) + ' ;;',
                    '      *State.Running*) [[ "$(cat "$state")" == running ]] && printf "true\\n" || printf "false\\n" ;;',
                    '      *State.Status*) cat "$state" ;;',
                    '      *Image*) printf "%s\\n" ' + repr(self.old_image_id) + ' ;;',
                    '      *) printf "running\\n" ;;',
                    "    esac",
                    '  elif [[ "$container" == areaforge-postgres ]]; then',
                    '    printf "healthy\\n"',
                    "  else",
                    '    printf "running\\n"',
                    "  fi",
                    "else",
                    '  printf \'[{"Name":"/areaforge-postgres","Id":"pg-id","State":{"StartedAt":"pg-start"},"Config":{"Image":"postgres@sha256:test"}}]\\n\'',
                    "fi",
                ]
            )
            + "\n",
            encoding="utf-8",
        )
        docker.chmod(0o755)
        return state

    def run_adapter(
        self,
        action: str,
        phase: str,
        target: str = "",
        source: str = "",
    ) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [str(ADAPTER), action, phase, str(self.operation), target, source],
            text=True,
            capture_output=True,
            env=self.environment(),
            check=False,
        )

    def write_task_contract(self, action: str = "update") -> None:
        contract = {
            "schemaVersion": 1,
            "taskId": str(uuid.uuid4()),
            "actorHash": "f" * 64,
            "service": "areaforge",
            "action": action,
            "target": "v1.2.0",
            "expectedBefore": self.expected_before,
            "createdAt": "2026-08-09T00:00:00Z",
        }
        (self.operation / "task-contract.json").write_text(canonical(contract) + "\n", encoding="utf-8")

    def test_check_discovers_verified_signed_target(self) -> None:
        result = self.run_adapter("check", "discover")
        self.assertEqual(result.returncode, 0, result.stderr)
        payload = json.loads(result.stdout)
        self.assertTrue(payload["ok"])
        self.assertEqual(payload["data"]["currentVersion"], "1.1.1")
        self.assertEqual(payload["data"]["latestTag"], "v1.2.0")
        self.assertTrue(payload["data"]["prepared"])
        self.assertTrue(payload["data"]["updateAvailable"])
        self.assertEqual(payload["data"]["webImageDigest"], self.target_image)

    def test_update_builds_strict_guard_without_sensitive_artifacts(self) -> None:
        self.write_task_contract()
        preflight = self.run_adapter("update", "preflight", "v1.2.0")
        self.assertEqual(preflight.returncode, 0, preflight.stderr)
        apply = self.run_adapter("update", "apply", "v1.2.0")
        self.assertEqual(apply.returncode, 0, apply.stderr)

        guard = json.loads(self.capture_guard.read_text(encoding="utf-8"))
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
            **{key: value for key, value in guard.items() if key != "requestHash"},
        }
        self.assertEqual(guard["requestHash"], hash_value(request_projection))
        self.assertEqual(guard["target"], self.target_identity)
        self.assertEqual(json.loads((self.operation / "update-result.json").read_text())["migrationApplied"], False)

        names = {path.name for path in self.operation.iterdir()}
        self.assertNotIn("sub2api.env.before", names)
        self.assertFalse(any(name.startswith("http-") for name in names))
        self.assertFalse(any(name.startswith(".updater-stderr") for name in names))

    def test_rollback_rejects_source_that_is_not_current_release(self) -> None:
        self.write_task_contract()
        source = self.root / "source-operation"
        source.mkdir()
        (source / "task-contract.json").write_text(
            (self.operation / "task-contract.json").read_text(encoding="utf-8"),
            encoding="utf-8",
        )
        (source / "update-result.json").write_text(
            canonical({
                "releaseTag": "v1.2.0",
                "targetVersion": "1.2.0",
                "targetImage": self.target_image,
            }) + "\n",
            encoding="utf-8",
        )
        for name, source_compose in (
            ("controlled-compose.before.yml", self.controlled),
            ("runtime-compose.before.yml", self.runtime),
        ):
            (source / name).write_bytes(source_compose.read_bytes())

        result = self.run_adapter("rollback", "preflight", "source-task", str(source))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("rollback source is not the currently deployed release", result.stderr)

    def test_start_lifecycle_only_starts_web_and_checks_health(self) -> None:
        state = self._enable_lifecycle_fakes()
        state.write_text("exited\n", encoding="utf-8")
        self.write_task_contract("start")
        for phase in ("preflight", "start", "health"):
            result = self.run_adapter("start", phase)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["phase"], phase)
        self.assertEqual(state.read_text(encoding="utf-8").strip(), "running")
        calls = (self.root / "docker-calls.txt").read_text(encoding="utf-8")
        self.assertIn("up -d --no-deps web", calls)
        self.assertNotIn("--force-recreate", calls)
        self.assertNotIn("areaforge-postgres", calls.split("up -d", 1)[-1])

    def test_stop_lifecycle_only_stops_web_and_verifies_stopped(self) -> None:
        state = self._enable_lifecycle_fakes()
        self.write_task_contract("stop")
        for phase in ("preflight", "stop", "health"):
            result = self.run_adapter("stop", phase)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["phase"], phase)
        self.assertEqual(state.read_text(encoding="utf-8").strip(), "exited")
        calls = (self.root / "docker-calls.txt").read_text(encoding="utf-8")
        self.assertIn("stop web", calls)
        self.assertNotIn("stop areaforge-postgres", calls)

    def test_traffic_lifecycle_is_explicitly_unsupported(self) -> None:
        self._enable_lifecycle_fakes()
        result = self.run_adapter("resume-traffic", "preflight")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unsupported traffic action", result.stderr)


if __name__ == "__main__":
    unittest.main()
