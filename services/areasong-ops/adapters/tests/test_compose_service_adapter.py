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


if __name__ == "__main__":
    unittest.main()
