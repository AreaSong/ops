from __future__ import annotations

import subprocess
import tempfile
import unittest
from pathlib import Path


DEPLOY_DIR = Path(__file__).resolve().parents[1]
VALIDATOR = DEPLOY_DIR / "validate-runtime-revision.sh"


class RuntimeRevisionContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.repository = Path(self.temporary.name) / "repository"
        self.repository.mkdir()
        self.git("init", "--quiet")
        self.git("config", "user.name", "AreaSong Tests")
        self.git("config", "user.email", "tests@areasong.invalid")
        self.base_revision = self.commit("base")

    def git(self, *arguments: str) -> str:
        return subprocess.run(
            ["git", "-C", str(self.repository), *arguments],
            check=True,
            text=True,
            capture_output=True,
        ).stdout.strip()

    def commit(self, value: str) -> str:
        (self.repository / "state.txt").write_text(value, encoding="utf-8")
        self.git("add", "state.txt")
        self.git("commit", "--quiet", "-m", value)
        return self.git("rev-parse", "HEAD")

    def validate(
        self, source_revision: str, deployed_revision: str
    ) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [str(VALIDATOR), str(self.repository), source_revision, deployed_revision],
            check=False,
            text=True,
            capture_output=True,
        )

    def test_matching_source_and_runtime_revision_passes(self) -> None:
        result = self.validate(self.base_revision, self.base_revision)

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("source/runtime drift: none", result.stdout)

    def test_source_ahead_of_deployed_ancestor_passes_with_drift(self) -> None:
        source_revision = self.commit("source-ahead")

        result = self.validate(source_revision, self.base_revision)

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("source HEAD is ahead", result.stdout)

    def test_missing_deployed_revision_fails(self) -> None:
        result = self.validate(self.base_revision, "0" * 40)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("deployed revision is not present", result.stderr)

    def test_divergent_deployed_revision_fails(self) -> None:
        source_revision = self.commit("source-branch")
        self.git("switch", "--quiet", "--detach", self.base_revision)
        deployed_revision = self.commit("divergent-runtime")

        result = self.validate(source_revision, deployed_revision)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not an ancestor of source HEAD", result.stderr)


if __name__ == "__main__":
    unittest.main()
