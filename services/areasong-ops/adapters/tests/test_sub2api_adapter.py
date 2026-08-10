from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ADAPTER = Path(__file__).resolve().parents[1] / "sub2api.sh"
RELEASES = Path(__file__).resolve().parents[4] / "scripts" / "deploy" / "update-control" / "releases" / "sub2api.json"


class Sub2APIAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.operation = self.root / "operation"
        self.operation.mkdir()
        self.source = self.root / "source"
        self.source.mkdir()
        self.fake_bin = self.root / "bin"
        self.fake_bin.mkdir()

        catalog = json.loads(RELEASES.read_text(encoding="utf-8"))["targets"]["v0.1.168"]
        self.before = {
            key: catalog["expectedBefore"][key]
            for key in ("currentVersion", "currentImage", "currentImageId", "runtimeIdentityHash")
        }
        self._write_fake_docker(catalog["expectedBefore"]["gitCommit"])
        self._write_contracts()

    def _write_fake_docker(self, git_commit: str) -> None:
        script = self.fake_bin / "docker"
        script.write_text(
            f"""#!/usr/bin/env bash
set -eu
case "$*" in
  "inspect --format {{{{.Config.Image}}}} sub2api") printf '%s\\n' '{self.before['currentImage']}' ;;
  "inspect --format {{{{.Image}}}} sub2api") printf '%s\\n' '{self.before['currentImageId']}' ;;
  "image inspect {self.before['currentImageId']}")
    printf '%s\\n' '[{{"Id":"{self.before['currentImageId']}","Config":{{"Labels":{{"org.opencontainers.image.version":"{self.before['currentVersion']}","org.opencontainers.image.revision":"{git_commit}"}}}}}}]'
    ;;
  *) printf 'unexpected docker invocation: %s\\n' "$*" >&2; exit 1 ;;
esac
""",
            encoding="utf-8",
        )
        script.chmod(0o755)

    def _write_contracts(self) -> None:
        current = {
            "schemaVersion": 1,
            "taskId": "11111111-1111-4111-8111-111111111111",
            "actorHash": "a" * 64,
            "service": "sub2api",
            "action": "rollback",
            "target": "22222222-2222-4222-8222-222222222222",
            "expectedBefore": self.before,
        }
        source = {
            "schemaVersion": 1,
            "taskId": "22222222-2222-4222-8222-222222222222",
            "actorHash": "a" * 64,
            "service": "sub2api",
            "action": "update",
            "target": "v0.1.168",
            "expectedBefore": self.before,
        }
        (self.operation / "task-contract.json").write_text(json.dumps(current), encoding="utf-8")
        (self.source / "task-contract.json").write_text(json.dumps(source), encoding="utf-8")
        (self.source / "legacy-request.json").write_text(
            json.dumps({"targetId": "v0.1.168"}),
            encoding="utf-8",
        )

    def run_adapter(self, action: str, phase: str, target: str, source: str = "") -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update({
            "PATH": f"{self.fake_bin}:{environment['PATH']}",
            "SUB2API_OPS_RELEASES": str(RELEASES),
        })
        return subprocess.run(
            [str(ADAPTER), action, phase, str(self.operation), target, source],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )

    def test_rollback_rejects_source_that_is_not_current_release(self) -> None:
        result = self.run_adapter(
            "rollback",
            "preflight",
            self.source.name,
            str(self.source),
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("rollback source is not the currently deployed release", result.stderr)

    def test_completed_release_cannot_be_replayed_as_update(self) -> None:
        result = self.run_adapter("update", "preflight", "v0.1.168")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("target is not present in prepared release catalog", result.stderr)

    def test_dynamic_prepared_release_is_used_by_later_update_phases(self) -> None:
        prepared_dir = self.root / "prepared"
        prepared_dir.mkdir()
        record = json.loads(RELEASES.read_text(encoding="utf-8"))["targets"]["v0.1.168"]
        record["tag"] = "v0.1.173"
        record["status"] = "prepared"
        (prepared_dir / "v0.1.173.json").write_text(json.dumps(record), encoding="utf-8")
        (self.operation / "legacy-request.json").write_text(
            json.dumps({"targetId": "v0.1.173"}), encoding="utf-8"
        )
        legacy = self.root / "legacy.sh"
        legacy.write_text(
            "#!/usr/bin/env bash\n"
            "set -eu\n"
            "jq -e --arg target v0.1.173 '.targets[$target].status == \"prepared\"' \"$SUB2API_UPDATE_CONTROL_RELEASES\" >/dev/null\n"
            "jq -cn --arg phase \"$1\" '{ok:true,phase:$phase,detail:\"accepted\"}'\n",
            encoding="utf-8",
        )
        legacy.chmod(0o755)
        environment = os.environ.copy()
        environment.update({
            "PATH": f"{self.fake_bin}:{environment['PATH']}",
            "SUB2API_OPS_RELEASES": str(RELEASES),
            "SUB2API_OPS_PREPARED_RELEASE_DIR": str(prepared_dir),
            "SUB2API_OPS_UPDATE_ADAPTER": str(legacy),
        })
        result = subprocess.run(
            [str(ADAPTER), "update", "backup", str(self.operation), "v0.1.173", ""],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["summary"], "accepted")


if __name__ == "__main__":
    unittest.main()
