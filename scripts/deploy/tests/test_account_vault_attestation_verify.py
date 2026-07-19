from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
VERIFIER = REPO_ROOT / "scripts" / "deploy" / "account-vault-attestation-verify.sh"
IMAGE = "ghcr.io/areasong/sorryiossearch@sha256:" + "a" * 64
GIT_SHA = "b" * 40


class AccountVaultAttestationVerifyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.fake_bin = self.root / "bin"
        self.fake_bin.mkdir()
        self.token = self.root / "github-read-token"
        self.token.write_text("test-read-token\n", encoding="utf-8")
        self.token.chmod(0o600)
        self.receipt = self.root / "receipt.json"
        self.gh_log = self.root / "gh.log"
        self._write_executable("id", '#!/bin/sh\n[ "${1:-}" = -u ] && { echo 0; exit 0; }\nexec /usr/bin/id "$@"\n')
        self._write_executable("stat", "#!/bin/sh\necho '600 root:root'\n")
        self._write_executable(
            "gh",
            """#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_GH_LOG"
predicate=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = --predicate-type ]; then
    predicate="$2"
    break
  fi
  shift
done
[ "$predicate" != "${FAKE_GH_FAIL_PREDICATE:-}" ] || exit 1
printf '[{"verificationResult":{"statement":{"predicateType":"%s"}}}]\n' "$predicate"
""",
        )

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _write_executable(self, name: str, body: str) -> None:
        path = self.fake_bin / name
        path.write_text(body, encoding="utf-8")
        path.chmod(0o755)

    def _run(self, **overrides: str) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.fake_bin}:{environment['PATH']}",
                "ACCOUNT_VAULT_GITHUB_TOKEN_FILE": str(self.token),
                "FAKE_GH_LOG": str(self.gh_log),
            }
        )
        environment.update(overrides)
        return subprocess.run(
            [str(VERIFIER), IMAGE, GIT_SHA, str(self.receipt)],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )

    def test_requires_and_archives_all_three_attestation_predicates(self) -> None:
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.gh_log.read_text(encoding="utf-8")
        for predicate in (
            "https://slsa.dev/provenance/v1",
            "https://cyclonedx.org/bom",
            "https://areasong.top/attestations/trivy/v1",
        ):
            self.assertIn(f"--predicate-type {predicate}", calls)
        self.assertEqual(calls.count("--deny-self-hosted-runners"), 3)
        self.assertEqual(calls.count("--bundle-from-oci"), 3)
        receipt = json.loads(self.receipt.read_text(encoding="utf-8"))
        self.assertEqual(set(receipt), {"provenance", "sbom", "trivy"})
        self.assertEqual(self.receipt.stat().st_mode & 0o777, 0o600)

    def test_fails_closed_when_any_required_predicate_is_missing(self) -> None:
        result = self._run(FAKE_GH_FAIL_PREDICATE="https://cyclonedx.org/bom")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.receipt.exists())


if __name__ == "__main__":
    unittest.main()
