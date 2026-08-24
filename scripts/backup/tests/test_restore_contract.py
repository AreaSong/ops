from __future__ import annotations

import hashlib
import json
import os
import subprocess
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "restore_contract.py"


class RestoreContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.backup_root = self.root / "backups"
        self.backup_root.mkdir()
        self.operation = self.root / "operation"
        self.operation.mkdir()
        self.roles = ["postgres-demo", "volume-demo"]
        artifacts = []
        for role in self.roles:
            path = self.backup_root / f"{role}.backup"
            path.write_bytes((role + "\n").encode())
            artifacts.append(
                {
                    "role": role,
                    "path": str(path),
                    "sizeBytes": path.stat().st_size,
                    "sha256": "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest(),
                }
            )
        now = datetime.now(timezone.utc)
        binding = "sha256:" + "a" * 64
        evidence = "sha256:" + "b" * 64
        before = "sha256:" + "c" * 64
        point_id = "11111111-1111-4111-8111-111111111111"
        backup_task_id = "55555555-5555-4555-8555-555555555555"
        created_at = now.isoformat().replace("+00:00", "Z")
        self.contract = {
            "schemaVersion": 1,
            "taskId": "22222222-2222-4222-8222-222222222222",
            "planId": "33333333-3333-4333-8333-333333333333",
            "service": "demo",
            "mode": "production",
            "tenantId": "production",
            "serverId": "server-a",
            "recoveryPointId": point_id,
            "bindingDigest": binding,
            "evidenceDigest": evidence,
            "expectedBeforeDigest": before,
            "revalidatedAt": now.isoformat().replace("+00:00", "Z"),
            "recoveryPoint": {
                "id": point_id,
                "taskId": backup_task_id,
                "service": "demo",
                "status": "verified",
                "tenantId": "production",
                "serverId": "server-a",
                "bindingDigest": binding,
                "evidenceDigest": evidence,
                "expectedBeforeDigest": before,
                "expectedBefore": {"currentImage": "demo@sha256:" + "d" * 64},
                "recoverableUntil": (now + timedelta(hours=1)).isoformat().replace("+00:00", "Z"),
                "requiredArtifactRoles": self.roles,
                "evidence": {
                    "schemaVersion": 1,
                    "service": "demo",
                    "taskId": backup_task_id,
                    "tenantId": "production",
                    "serverId": "server-a",
                    "expectedBeforeDigest": before,
                    "bindingDigest": binding,
                    "createdAt": created_at,
                    "artifacts": artifacts,
                },
            },
        }
        expected_payload = json.dumps(
            self.contract["recoveryPoint"]["expectedBefore"],
            sort_keys=True,
            separators=(",", ":"),
        ).encode()
        before = "sha256:" + hashlib.sha256(expected_payload).hexdigest()
        self.contract["expectedBeforeDigest"] = before
        self.contract["recoveryPoint"]["expectedBeforeDigest"] = before
        self.contract["recoveryPoint"]["evidence"]["expectedBeforeDigest"] = before
        unsigned = dict(self.contract["recoveryPoint"]["evidence"])
        unsigned["bindingDigest"] = ""
        unsigned_digest = "sha256:" + hashlib.sha256(
            json.dumps(unsigned, separators=(",", ":")).encode()
        ).hexdigest()
        binding_payload = {
            "schemaVersion": 1,
            "service": "demo",
            "taskId": self.contract["recoveryPoint"]["evidence"]["taskId"],
            "tenantId": "production",
            "serverId": "server-a",
            "expectedBeforeDigest": before,
            "evidenceDigest": unsigned_digest,
            "requiredRoles": sorted(self.roles),
        }
        binding = "sha256:" + hashlib.sha256(
            json.dumps(binding_payload, separators=(",", ":")).encode()
        ).hexdigest()
        self.contract["bindingDigest"] = binding
        self.contract["recoveryPoint"]["bindingDigest"] = binding
        self.contract["recoveryPoint"]["evidence"]["bindingDigest"] = binding
        evidence_digest = "sha256:" + hashlib.sha256(
            json.dumps(self.contract["recoveryPoint"]["evidence"], separators=(",", ":")).encode()
        ).hexdigest()
        self.contract["evidenceDigest"] = evidence_digest
        self.contract["recoveryPoint"]["evidenceDigest"] = evidence_digest
        self.path = self.operation / "recovery-point.json"
        self.write_contract()

    def write_contract(self) -> None:
        self.path.write_text(json.dumps(self.contract), encoding="utf-8")
        self.path.chmod(0o600)

    def run_contract(self, *extra: str) -> subprocess.CompletedProcess[str]:
        command = [
            str(SCRIPT),
            "--contract",
            str(self.path),
            "--service",
            "demo",
            "--target",
            self.contract["recoveryPointId"],
            "--backup-root",
            str(self.backup_root),
        ]
        for role in self.roles:
            command.extend(["--required-role", role])
        return subprocess.run(command + list(extra), text=True, capture_output=True, check=False)

    def test_valid_contract_returns_fixed_artifact_map(self) -> None:
        result = self.run_contract()
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads(result.stdout)
        self.assertEqual(sorted(output["artifacts"]), sorted(self.roles))
        self.assertEqual(output["service"], "demo")

    def test_tampered_artifact_is_rejected(self) -> None:
        Path(self.contract["recoveryPoint"]["evidence"]["artifacts"][0]["path"]).write_text(
            "changed\n", encoding="utf-8"
        )
        result = self.run_contract()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("artifact[postgres-demo]", result.stderr)

    def test_tenant_drift_is_rejected(self) -> None:
        self.contract["recoveryPoint"]["tenantId"] = "other"
        self.write_contract()
        result = self.run_contract()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("tenant/server", result.stderr)

    def test_recomputed_binding_and_evidence_digests_are_required(self) -> None:
        self.contract["recoveryPoint"]["evidence"]["taskId"] = "44444444-4444-4444-8444-444444444444"
        self.write_contract()
        result = self.run_contract()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("binding digest", result.stderr)

    def test_contract_symlink_and_weak_mode_are_rejected(self) -> None:
        self.path.chmod(0o644)
        result = self.run_contract()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("0600", result.stderr)
        actual = self.operation / "actual.json"
        self.path.replace(actual)
        os.symlink(actual, self.path)
        result = self.run_contract()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("non-symlink", result.stderr)


if __name__ == "__main__":
    unittest.main()
