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
        self.cosign_log = self.root / "cosign.log"
        self._write_executable("id", '#!/bin/sh\n[ "${1:-}" = -u ] && { echo 0; exit 0; }\nexec /usr/bin/id "$@"\n')
        self._write_executable("stat", "#!/bin/sh\necho '600 root:root'\n")
        self._write_executable(
            "cosign",
            """#!/bin/sh
set -eu
printf 'DOCKER_CONFIG=%s %s\n' "${DOCKER_CONFIG:-}" "$*" >>"$FAKE_COSIGN_LOG"
if [ "${1:-}" = login ]; then
  cat >/dev/null
  exit 0
fi
[ "${1:-}" = verify-attestation ] || exit 2
predicate=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = --type ]; then
    predicate="$2"
    break
  fi
  shift
done
[ -n "$predicate" ]
[ "$predicate" != "${FAKE_COSIGN_FAIL_PREDICATE:-}" ] || exit 1
[ "$predicate" != "${FAKE_COSIGN_MALFORMED_PREDICATE:-}" ] || { printf '{}\n'; exit 0; }
printf '{"payloadType":"application/vnd.in-toto+json","payload":"dGVzdA==","signatures":[{"sig":"%s"}]}\n' "$predicate"
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
                "FAKE_COSIGN_LOG": str(self.cosign_log),
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
        calls = self.cosign_log.read_text(encoding="utf-8")
        self.assertIn("login ghcr.io --username AreaSong --password-stdin", calls)
        for predicate in (
            "slsaprovenance1",
            "cyclonedx",
            "https://areasong.top/attestations/trivy/v1",
        ):
            self.assertIn(f"verify-attestation --type {predicate}", calls)
        for constraint in (
            "--certificate-identity https://github.com/AreaSong/sorryiosSearch/.github/workflows/ci.yml@refs/heads/main",
            "--certificate-oidc-issuer https://token.actions.githubusercontent.com",
            "--certificate-github-workflow-name Account Vault CI",
            "--certificate-github-workflow-ref refs/heads/main",
            "--certificate-github-workflow-repository AreaSong/sorryiosSearch",
            f"--certificate-github-workflow-sha {GIT_SHA}",
            "--certificate-github-workflow-trigger push",
        ):
            self.assertEqual(calls.count(constraint), 3)
        self.assertNotIn("test-read-token", calls)

        receipt = json.loads(self.receipt.read_text(encoding="utf-8"))
        self.assertEqual(receipt["scheme"], "sigstore-keyless-oci-v1")
        self.assertEqual(receipt["image"], IMAGE)
        self.assertEqual(receipt["gitSha"], GIT_SHA)
        self.assertEqual(set(receipt) - {"scheme", "image", "gitSha", "certificateIdentity", "certificateOidcIssuer"}, {"provenance", "sbom", "trivy"})
        self.assertEqual(len(receipt["provenance"]), 1)
        self.assertEqual(receipt["provenance"][0]["payloadType"], "application/vnd.in-toto+json")
        self.assertEqual(self.receipt.stat().st_mode & 0o777, 0o600)

    def test_fails_closed_when_any_required_predicate_is_missing(self) -> None:
        result = self._run(FAKE_COSIGN_FAIL_PREDICATE="cyclonedx")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.receipt.exists())

    def test_fails_closed_when_cosign_output_is_not_a_dsse_envelope(self) -> None:
        result = self._run(FAKE_COSIGN_MALFORMED_PREDICATE="slsaprovenance1")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.receipt.exists())


if __name__ == "__main__":
    unittest.main()
