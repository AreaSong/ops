from __future__ import annotations

import gzip
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from scripts.backup.tests.recovery_fixture import make_contract, write_fixture


SCRIPT_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPT_DIR))
import restore_point_metadata as metadata


class RestorePointMetadataTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.backups = self.root / "backups"
        self.fixture = write_fixture(self.backups / "A")
        write_fixture(self.backups / "B", "B")
        (self.backups / "latest-manifest.txt").write_text("B", encoding="utf-8")
        self.operation = self.root / "operation"
        self.operation.mkdir(mode=0o700)
        self.contract = make_contract(self.fixture)
        self.contract_path = self.operation / "recovery-point.json"
        self.write_contract()
        self.arguments = SimpleNamespace(service="sub2api", contract=self.contract_path,
                                         target=self.contract["recoveryPointId"], backup_root=self.backups,
                                         operation_dir=self.operation, expected_mode="isolated")

    def write_contract(self) -> None:
        self.contract_path.write_text(json.dumps(self.contract), encoding="utf-8")
        self.contract_path.chmod(0o600)

    def stage(self) -> dict:
        result = metadata.stage(self.arguments)
        receipt = self.operation / "selected-point.json"
        receipt.write_text(json.dumps(result), encoding="utf-8")
        receipt.chmod(0o600)
        return result

    def test_selected_point_does_not_use_latest_or_modify_sources(self) -> None:
        before = {item["path"]: Path(item["path"]).read_bytes() for item in self.fixture["artifacts"]}
        result = self.stage()
        with gzip.open(result["stagedArtifacts"]["postgres-sub2api"], "rt") as restored:
            self.assertIn("'A'", restored.read())
        self.assertEqual(metadata.verify_staged(self.arguments)["recoveryPointId"], self.contract["recoveryPointId"])
        for path, content in before.items():
            self.assertEqual(Path(path).read_bytes(), content)
        self.assertEqual((self.backups / "latest-manifest.txt").read_text(), "B")

    def test_staged_environment_and_artifact_tampering_is_rejected(self) -> None:
        result = self.stage()
        Path(result["envFile"]).write_text("changed", encoding="utf-8")
        with self.assertRaisesRegex(metadata.ContractError, "environment file changed"):
            metadata.verify_staged(self.arguments)

    def test_old_three_role_point_and_wrong_mode_are_rejected(self) -> None:
        legacy = dict(self.fixture)
        legacy["artifacts"] = [item for item in self.fixture["artifacts"] if item["role"] not in {"configs", "runtime-snapshot"}]
        self.contract = make_contract(legacy)
        self.write_contract()
        with self.assertRaisesRegex(metadata.ContractError, "required roles"):
            metadata.stage(self.arguments)
        self.contract = make_contract(self.fixture, mode="production")
        self.write_contract()
        with self.assertRaisesRegex(metadata.ContractError, "restore mode"):
            metadata.stage(self.arguments)

    def test_capture_produces_scoped_configs_and_fixed_runtime(self) -> None:
        task = self.operation / "task-contract.json"
        task.write_text(json.dumps({"taskId": self.contract["taskId"], "service": "sub2api", "action": "backup", "expectedBefore": self.fixture["before"]}))
        task.chmod(0o600)
        paths = []
        for name in ("controlled", "runtime", "env"):
            path = self.root / name
            path.write_text("fixture", encoding="utf-8")
            path.chmod(0o600)
            paths.append(path)
        args = SimpleNamespace(service="sub2api", operation_dir=self.operation, backup_root=self.backups,
                               controlled_compose=paths[0], runtime_compose=paths[1], env_file=paths[2])
        with patch.object(metadata, "capture_runtime", return_value=self.fixture["runtime"]):
            result = metadata.capture(args)
        self.assertEqual({item["role"] for item in result["artifacts"]}, {"configs", "runtime-snapshot"})
        self.assertEqual(list((self.backups / "configs").glob("configs-*.tar.gz")), [])

    def test_real_sub2api_preflight_and_backup_only_consume_selected_point(self) -> None:
        fake_bin = self.root / "bin"
        fake_bin.mkdir()
        for name, script in {
            "id": "#!/bin/sh\nif [ \"$1\" = -u ]; then echo 0; else exec /usr/bin/id \"$@\"; fi\n",
            "docker": "#!/bin/sh\necho 'unexpected Docker invocation' >&2\nexit 91\n",
        }.items():
            path = fake_bin / name
            path.write_text(script)
            path.chmod(0o755)
        environment = dict(os.environ, PATH=str(fake_bin) + os.pathsep + os.environ["PATH"],
                           BACKUP_ROOT=str(self.backups), SUB2API_RESTORE_ENV_FILE="/missing-env",
                           SUB2API_RESTORE_BACKUP_POSTGRES="/must-not-run", SUB2API_RESTORE_BACKUP_REDIS="/must-not-run",
                           SUB2API_RESTORE_BACKUP_VOLUMES="/must-not-run")
        for phase in ("preflight", "backup"):
            result = subprocess.run(
                ["bash", str(SCRIPT_DIR / "restore-sub2api-isolated.sh"), "restore-drill", phase,
                 str(self.operation), self.contract["recoveryPointId"]],
                env=environment, text=True, capture_output=True, check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
        selected = json.loads((self.operation / "sub2api-drill-backups.json").read_text())
        with gzip.open(selected["postgres"]["path"], "rt") as restored:
            self.assertIn("'A'", restored.read())
        wrong = self.backups / "B" / "sub2api-postgres-B.sql.gz"
        selected["postgres"] = {"path": str(wrong), "sha256": "sha256:" + hashlib.sha256(wrong.read_bytes()).hexdigest()}
        (self.operation / "sub2api-drill-backups.json").write_text(json.dumps(selected))
        rejected = subprocess.run(
            ["bash", str(SCRIPT_DIR / "restore-sub2api-isolated.sh"), "restore-drill", "drill",
             str(self.operation), self.contract["recoveryPointId"]],
            env=environment, text=True, capture_output=True, check=False,
        )
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("selected backup inputs differ", rejected.stderr)
        self.assertNotIn("unexpected Docker invocation", rejected.stderr)


if __name__ == "__main__":
    unittest.main()
