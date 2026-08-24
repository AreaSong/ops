from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ADAPTER = Path(__file__).resolve().parents[1] / "compose-service.sh"


class ComposeServiceAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.operation = self.root / "operation"
        self.operation.mkdir()
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.prepared_dir = self.root / "prepared"
        self.prepared_dir.mkdir()

        self.inspect = self.root / "inspect.sh"
        inspect_payload = json.dumps(
            {"ok": True, "summary": "checked", "data": {"currentVersion": "0.1.168"}},
            separators=(",", ":"),
        )
        self.inspect.write_text(
            f"#!/bin/sh\nprintf '%s\\n' '{inspect_payload}'\n",
            encoding="utf-8",
        )
        self.inspect.chmod(0o755)
        self.release_catalog = self.root / "releases.json"
        self.release_catalog.write_text('{"schemaVersion":1,"targets":{}}\n', encoding="utf-8")
        for name in ("controlled.yml", "runtime.yml", ".env"):
            (self.root / name).write_text("test\n", encoding="utf-8")

        runtime = {
            "controlledCompose": str(self.root / "controlled.yml"),
            "runtimeCompose": str(self.root / "runtime.yml"),
            "envFile": str(self.root / ".env"),
            "applicationService": "demo",
            "applicationContainer": "demo",
            "dependencyContainers": [],
            "healthUrl": "http://127.0.0.1:8080/health",
            "releaseRepository": "owner/repository",
            "releaseCatalog": str(self.release_catalog),
            "preparedReleaseDir": str(self.prepared_dir),
            "inspectExecutable": str(self.inspect),
        }
        self.catalog = self.root / "services.json"
        self.catalog.write_text(
            json.dumps(
                {
                    "schemaVersion": 2,
                    "services": {
                        "demo": {
                            "name": "demo",
                            "displayName": "Demo",
                            "description": "test",
                            "template": "compose-service-v1",
                            "adapter": str(ADAPTER),
                            "runtime": runtime,
                            "actions": {},
                        }
                    },
                }
            ),
            encoding="utf-8",
        )
        curl = self.bin_dir / "curl"
        curl.write_text(
            "#!/bin/sh\nout=''\nwhile [ $# -gt 0 ]; do "
            "if [ \"$1\" = -o ]; then out=$2; shift 2; else shift; fi; done\n"
            "printf '%s\\n' '{\"tag_name\":\"v0.1.173\",\"published_at\":\"2026-08-09T08:26:09Z\"}' >\"$out\"\n",
            encoding="utf-8",
        )
        curl.chmod(0o755)

    def run_check(self) -> dict[str, object]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
                "OPS_SERVICE_NAME": "demo",
                "OPS_SERVICE_CATALOG": str(self.catalog),
            }
        )
        result = subprocess.run(
            [str(ADAPTER), "check", "discover", str(self.operation), "", ""],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def _enable_lifecycle_fakes(self) -> Path:
        state = self.root / "app-state"
        state.write_text("running\n", encoding="utf-8")
        catalog = json.loads(self.catalog.read_text(encoding="utf-8"))
        catalog["services"]["demo"]["runtime"]["dependencyContainers"] = ["demo-db"]
        self.catalog.write_text(json.dumps(catalog), encoding="utf-8")

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
                    '  case " $* " in',
                    '    *\ up\ *) printf "running\\n" >"$state" ;;',
                    '    *\ stop\ *) printf "exited\\n" >"$state" ;;',
                    "  esac",
                    "  exit 0",
                    "fi",
                    '[[ "${1:-}" == inspect ]] || { printf "unexpected docker invocation: %s\\n" "$*" >&2; exit 1; }',
                    'if [[ "$*" == *--format* ]]; then',
                    '  container="${!#}"',
                    '  format="${3:-}"',
                    '  if [[ "$container" == demo ]]; then',
                    '    case "$format" in',
                    '      *State.Running*) [[ "$(cat "$state")" == running ]] && printf "true\\n" || printf "false\\n" ;;',
                    '      *State.Status*) cat "$state" ;;',
                    '      *) printf "running\\n" ;;',
                    "    esac",
                    "  else",
                    '    printf "running\\n"',
                    "  fi",
                    "else",
                    '  printf \'[{"Name":"/demo-db","Id":"db-id","State":{"StartedAt":"db-start"},"Image":"postgres@sha256:test"}]\\n\'',
                    "fi",
                ]
            )
            + "\n",
            encoding="utf-8",
        )
        docker.chmod(0o755)
        curl = self.bin_dir / "curl"
        curl.write_text(
            "\n".join(
                [
                    "#!/bin/sh",
                    "out=''",
                    "code=''",
                    "while [ $# -gt 0 ]; do",
                    '  case "$1" in',
                    "    -o) out=$2; shift 2 ;;",
                    "    -w) code=$2; shift 2 ;;",
                    "    *) shift ;;",
                    "  esac",
                    "done",
                    '[ -z "$out" ] || printf \'{"ok":true}\\n\' >"$out"',
                    '[ -z "$code" ] || printf "200\\n"',
                ]
            )
            + "\n",
            encoding="utf-8",
        )
        curl.chmod(0o755)
        return state

    def run_lifecycle(self, action: str, phase: str) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
                "OPS_SERVICE_NAME": "demo",
                "OPS_SERVICE_CATALOG": str(self.catalog),
            }
        )
        return subprocess.run(
            [str(ADAPTER), action, phase, str(self.operation), "", ""],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )

    def test_check_blocks_release_without_preparation_record(self) -> None:
        payload = self.run_check()
        self.assertFalse(payload["data"]["prepared"])
        self.assertTrue(payload["data"]["blockers"])

    def test_check_accepts_matching_preparation_record(self) -> None:
        (self.prepared_dir / "v0.1.173.json").write_text(
            '{"tag":"v0.1.173","status":"prepared"}\n', encoding="utf-8"
        )
        payload = self.run_check()
        self.assertTrue(payload["data"]["prepared"])
        self.assertEqual(payload["data"]["blockers"], [])

    def test_backup_phases_delegate_to_evidence_adapter(self) -> None:
        calls = self.root / "backup-calls.txt"
        evidence = self.root / "backup-evidence.sh"
        evidence.write_text(
            "#!/bin/sh\n"
            f"printf '%s %s\\n' \"$1\" \"$2\" >>'{calls}'\n"
            "printf '{\"schemaVersion\":2,\"action\":\"%s\",\"phase\":\"%s\","
            "\"ok\":true,\"summary\":\"delegated\"}\\n' \"$1\" \"$2\"\n",
            encoding="utf-8",
        )
        evidence.chmod(0o755)
        catalog = json.loads(self.catalog.read_text(encoding="utf-8"))
        catalog["services"]["demo"]["runtime"]["backupEvidenceExecutable"] = str(evidence)
        self.catalog.write_text(json.dumps(catalog), encoding="utf-8")
        environment = os.environ.copy()
        environment.update(
            {"OPS_SERVICE_NAME": "demo", "OPS_SERVICE_CATALOG": str(self.catalog)}
        )
        for phase in ("preflight", "backup", "verify"):
            result = subprocess.run(
                [str(ADAPTER), "backup", phase, str(self.operation), "", ""],
                text=True,
                capture_output=True,
                env=environment,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            payload = json.loads(result.stdout)
            self.assertEqual(payload["phase"], phase)
        self.assertEqual(
            calls.read_text(encoding="utf-8").splitlines(),
            ["backup preflight", "backup backup", "backup verify"],
        )

    def test_start_lifecycle_only_starts_application_and_checks_health(self) -> None:
        state = self._enable_lifecycle_fakes()
        state.write_text("exited\n", encoding="utf-8")
        for phase in ("preflight", "start", "health"):
            result = self.run_lifecycle("start", phase)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["phase"], phase)

        self.assertEqual(state.read_text(encoding="utf-8").strip(), "running")
        calls = (self.root / "docker-calls.txt").read_text(encoding="utf-8")
        self.assertIn("up -d --no-deps demo", calls)
        self.assertNotIn("--force-recreate", calls)
        self.assertNotIn("demo-db", calls.split("up -d", 1)[-1])

    def test_stop_lifecycle_only_stops_application_and_verifies_stopped(self) -> None:
        state = self._enable_lifecycle_fakes()
        for phase in ("preflight", "stop", "health"):
            result = self.run_lifecycle("stop", phase)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["phase"], phase)
        self.assertEqual(state.read_text(encoding="utf-8").strip(), "exited")
        calls = (self.root / "docker-calls.txt").read_text(encoding="utf-8")
        self.assertIn("stop demo", calls)
        self.assertNotIn("stop demo-db", calls)

    def test_traffic_lifecycle_is_explicitly_unsupported(self) -> None:
        self._enable_lifecycle_fakes()
        result = self.run_lifecycle("drain", "preflight")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unsupported traffic action", result.stderr)


if __name__ == "__main__":
    unittest.main()
