from __future__ import annotations

import gzip
import hashlib
import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
RELEASE_SCRIPT = REPO_ROOT / "scripts" / "deploy" / "account-vault-release.sh"
RELEASE_VALIDATOR = REPO_ROOT / "scripts" / "deploy" / "account-vault-release-validate.py"
ROLE_PERMISSION_HELPER = REPO_ROOT / "scripts" / "deploy" / "account-vault-role-permissions.sh"
RELEASE_IMAGE = "ghcr.io/areasong/sorryiossearch@sha256:" + "a" * 64
PREVIOUS_IMAGE = "ghcr.io/areasong/sorryiossearch@sha256:" + "b" * 64
LOCAL_IMAGE_ID = "sha256:" + "c" * 64
CANDIDATE_IMAGE_ID = "sha256:" + "2" * 64
GIT_SHA = "d" * 40


class AccountVaultReleaseTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.fake_bin = self.root / "bin"
        self.fake_bin.mkdir()
        self.controlled = self.root / "ops" / "compose.yml"
        self.runtime = self.root / "runtime" / "compose.yml"
        self.env_file = self.root / "account-vault.env"
        self.backup_set_root = self.root / "backups"
        self.backup_root = self.backup_set_root / "postgres"
        self.r2_state = self.root / "state-input" / "backup-set-r2-verify.state"
        self.backup_manifest_tool = self.root / "backup_manifest.py"
        self.release_evidence = self.root / "published-release-manifest.json"
        self.attestation_verifier = self.fake_bin / "verify-attestation"
        self.github_token = self.root / "github-read-token"
        self.registry_auth_root = self.root / "registry-auth"
        self.release_metric = self.root / "metrics" / "account-vault-release.prom"
        self.state_dir = self.root / "state"
        self.docker_log = self.root / "docker.log"
        self.curl_log = self.root / "curl.log"

        self.controlled.parent.mkdir(parents=True)
        self.runtime.parent.mkdir(parents=True)
        self.backup_root.mkdir(parents=True)
        self.r2_state.parent.mkdir(parents=True)
        self.controlled.write_text("services:\n  web: {}\n", encoding="utf-8")
        self.runtime.write_text("services:\n  old: {}\n", encoding="utf-8")
        self.env_file.write_text(
            "\n".join(
                (
                    "POSTGRES_USER=account_user",
                    "POSTGRES_PASSWORD=test-only-admin-password",
                    "DATABASE_APP_USER=account_vault_app",
                    "DATABASE_APP_PASSWORD=test-only-app-password",
                    "RP_ID=sorryiossearch.areasong.top",
                    "ORIGIN=https://sorryiossearch.areasong.top",
                    "SESSION_SECRET=test-session-secret-with-more-than-32-characters",
                    "ENCRYPTION_KEY=" + "0" * 64,
                    "REGISTRATION_TOKEN=test-registration-token-with-32-characters",
                    "",
                )
            ),
            encoding="utf-8",
        )
        self.env_file.chmod(0o600)
        backup = self.backup_root / "account-vault-postgres-1-20260718-000000.sql.gz"
        with gzip.open(backup, "wb") as handle:
            handle.write(b"-- verified test backup\n")
        manifest_dir = self.backup_set_root / "manifests"
        manifest_dir.mkdir()
        manifest = manifest_dir / "backup-set-20260718-000000.json"
        manifest.write_text('{"schema_version": 1}\n', encoding="utf-8")
        (manifest_dir / "latest-manifest.txt").write_text(
            f"manifests/{manifest.name}\n", encoding="utf-8"
        )
        manifest_sha = hashlib.sha256(manifest.read_bytes()).hexdigest()
        self.r2_state.write_text(
            "\n".join(
                (
                    f"manifest_relative=manifests/{manifest.name}",
                    f"manifest_sha256={manifest_sha}",
                    f"verified_at={int(time.time())}",
                    "",
                )
            ),
            encoding="utf-8",
        )
        self.r2_state.chmod(0o600)
        self.github_token.write_text("test-read-packages-token\n", encoding="utf-8")
        self.github_token.chmod(0o600)
        self.registry_auth_root.mkdir()
        self.backup_manifest_tool.write_text(
            """import sys
if sys.argv[1] == "verify":
    raise SystemExit(0)
if sys.argv[1] == "artifact-field":
    print("postgres/account-vault-postgres-1-20260718-000000.sql.gz")
    raise SystemExit(0)
raise SystemExit(2)
""",
            encoding="utf-8",
        )
        self.release_evidence.write_text(
            json.dumps(
                {
                    "stage": "published",
                    "gitSha": GIT_SHA,
                    "tag": f"ghcr.io/areasong/sorryiossearch:sha-{GIT_SHA}",
                    "repositoryDigest": RELEASE_IMAGE,
                    "candidateImageId": CANDIDATE_IMAGE_ID,
                    "sbomSha256": "e" * 64,
                    "trivyReportSha256": "f" * 64,
                    "migrationPolicy": "expand-only",
                    "migrationTreeSha256": "1" * 64,
                    "attestationScheme": "sigstore-keyless-oci-v1",
                    "provenance": "cosign-keyless-slsa-v1",
                    "sbomAttestationPredicate": "https://cyclonedx.org/bom",
                    "trivyAttestationPredicate": "https://areasong.top/attestations/trivy/v1",
                    "attestedSubjectDigest": "sha256:" + "a" * 64,
                    "certificateIdentity": "https://github.com/AreaSong/sorryiosSearch/.github/workflows/ci.yml@refs/heads/main",
                    "certificateOidcIssuer": "https://token.actions.githubusercontent.com",
                    "repository": "AreaSong/sorryiosSearch",
                    "workflowRef": "AreaSong/sorryiosSearch/.github/workflows/ci.yml@refs/heads/main",
                    "runId": "123456789",
                    "runAttempt": "1",
                }
            )
            + "\n",
            encoding="utf-8",
        )
        self.release_evidence.chmod(0o600)
        self._write_fake_commands()

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _write_executable(self, name: str, body: str) -> None:
        path = self.fake_bin / name
        path.write_text(body, encoding="utf-8")
        path.chmod(0o755)

    def _write_fake_commands(self) -> None:
        self._write_executable("id", "#!/bin/sh\n[ \"${1:-}\" = -u ] && { echo 0; exit 0; }\nexec /usr/bin/id \"$@\"\n")
        self._write_executable(
            "stat",
            """#!/bin/sh
case "$*" in
  "-c %a "*) echo 600 ;;
  "-c %U:%G "*) echo root:root ;;
  *) exit 2 ;;
esac
""",
        )
        self._write_executable("flock", "#!/bin/sh\nexit 0\n")
        self._write_executable(
            "cmp",
            "#!/bin/sh\n[ \"${FAKE_CMP_FAIL:-false}\" = true ] && exit 1\nexec /usr/bin/cmp \"$@\"\n",
        )
        self._write_executable(
            "ln",
            "#!/bin/sh\n[ \"${FAKE_STATE_LINK_FAIL:-false}\" = true ] && exit 1\nexec /bin/ln \"$@\"\n",
        )
        self._write_executable(
            "verify-attestation",
            "#!/bin/sh\n[ \"${FAKE_ATTESTATION_FAIL:-false}\" = true ] && exit 1\nprintf '%s\\n' '{\"verified\":true}' >\"$3\"\nchmod 0600 \"$3\"\n",
        )
        self._write_executable("curl", "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$FAKE_CURL_LOG\"\nexit 0\n")
        self._write_executable(
            "docker",
            """#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"

if [[ "$1" == inspect && "$3" == '{{.Config.Image}}' ]]; then
  printf '%s\n' "${FAKE_RUNNING_IMAGE:-local/account-vault:test}"
elif [[ "$1" == inspect && "$3" == '{{.Image}}' ]]; then
  printf '%s\n' "$FAKE_LOCAL_IMAGE_ID"
elif [[ "$1" == inspect && "$3" == '{{.State.Health.Status}}' ]]; then
  if [[ -n "${FAKE_HEALTH_STATUS_FILE:-}" && -r "$FAKE_HEALTH_STATUS_FILE" ]]; then
    cat "$FAKE_HEALTH_STATUS_FILE"
  else
    printf '%s\n' "${FAKE_HEALTH_STATUS:-healthy}"
  fi
elif [[ "$1" == image && "$2" == inspect && "$3" == --format ]]; then
  case "$4" in
    *RepoDigests*) printf '%s\n' "$FAKE_RELEASE_IMAGE" ;;
    '{{.Config.User}}') printf '%s\n' "${FAKE_IMAGE_USER:-node}" ;;
    '{{.Id}}') printf '%s\n' "${FAKE_LOCAL_PULLED_IMAGE_ID:-$FAKE_CANDIDATE_IMAGE_ID}" ;;
    *org.opencontainers.image.revision*) printf '%s\n' "${FAKE_IMAGE_REVISION:-$FAKE_GIT_SHA}" ;;
    *) printf '%s\n' "$FAKE_RELEASE_IMAGE" ;;
  esac
elif [[ "$1" == manifest && "$2" == inspect ]]; then
  printf '{"schemaVersion":2,"config":{"digest":"%s"}}\n' "$FAKE_REGISTRY_CONFIG_DIGEST"
elif [[ "$1" == exec && "$*" == *'verify_contract=1'* ]]; then
  echo '0|0|false|1|true|false|false|false|false|false|0|false'
elif [[ "$1" == compose && "$*" == *' ps -q web' ]]; then
  echo account-vault-web-test
fi
""",
        )

    def _environment(self, **overrides: str) -> dict[str, str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.fake_bin}:{environment['PATH']}",
                "ACCOUNT_VAULT_CONTROLLED_COMPOSE": str(self.controlled),
                "ACCOUNT_VAULT_RUNTIME_COMPOSE": str(self.runtime),
                "ACCOUNT_VAULT_ENV_FILE": str(self.env_file),
                "ACCOUNT_VAULT_RELEASE_STATE_DIR": str(self.state_dir),
                "ACCOUNT_VAULT_BACKUP_SET_ROOT": str(self.backup_set_root),
                "ACCOUNT_VAULT_BACKUP_MANIFEST_TOOL": str(self.backup_manifest_tool),
                "ACCOUNT_VAULT_R2_VERIFY_STATE": str(self.r2_state),
                "ACCOUNT_VAULT_RELEASE_VALIDATOR": str(RELEASE_VALIDATOR),
                "ACCOUNT_VAULT_ATTESTATION_VERIFIER": str(self.attestation_verifier),
                "ACCOUNT_VAULT_GITHUB_TOKEN_FILE": str(self.github_token),
                "ACCOUNT_VAULT_REGISTRY_AUTH_ROOT": str(self.registry_auth_root),
                "ACCOUNT_VAULT_ROLE_PERMISSION_HELPER": str(ROLE_PERMISSION_HELPER),
                "ACCOUNT_VAULT_RELEASE_METRIC_OUT": str(self.release_metric),
                "ACCOUNT_VAULT_RELEASE_LOCK_FILE": str(self.root / "run" / "release.lock"),
                "ACCOUNT_VAULT_RELEASE_BACKUP_DIR": str(self.root / "manual"),
                "ACCOUNT_VAULT_RELEASE_WINDOW_ENFORCED": "false",
                "FAKE_DOCKER_LOG": str(self.docker_log),
                "FAKE_CURL_LOG": str(self.curl_log),
                "FAKE_RELEASE_IMAGE": RELEASE_IMAGE,
                "FAKE_LOCAL_IMAGE_ID": LOCAL_IMAGE_ID,
                "FAKE_CANDIDATE_IMAGE_ID": CANDIDATE_IMAGE_ID,
                "FAKE_LOCAL_PULLED_IMAGE_ID": CANDIDATE_IMAGE_ID,
                "FAKE_REGISTRY_CONFIG_DIGEST": CANDIDATE_IMAGE_ID,
                "FAKE_GIT_SHA": GIT_SHA,
            }
        )
        environment.update(overrides)
        return environment

    def _run(self, *arguments: str, **environment: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["bash", str(RELEASE_SCRIPT), *arguments],
            text=True,
            capture_output=True,
            env=self._environment(**environment),
            check=False,
        )

    def _seed_release_state(self, current_image: str, previous_image: str) -> Path:
        generation = self.state_dir / "generations" / "seed"
        generation.mkdir(parents=True)
        (generation / "current-image").write_text(current_image + "\n", encoding="utf-8")
        (generation / "previous-image").write_text(previous_image + "\n", encoding="utf-8")
        (generation / "current-compose.yml").write_text(
            "services:\n  web: {}\n", encoding="utf-8"
        )
        (generation / "previous-compose.yml").write_text(
            "services:\n  web: {}\n", encoding="utf-8"
        )
        (self.state_dir / "current").symlink_to("generations/seed")
        return generation

    def test_deploy_rejects_mutable_image_tag(self) -> None:
        result = self._run(
            "deploy",
            "ghcr.io/areasong/sorryiossearch:latest",
            "--approve-migration",
            "--change-id",
            "test-001",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("immutable approved GHCR digest", result.stderr)
        self.assertFalse(self.docker_log.exists())

    def test_deploy_requires_the_matching_approval_flag(self) -> None:
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-rollback",
            "--change-id",
            "test-002",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("approval flag for deploy", result.stderr)

    def test_deploy_runs_migration_and_records_content_addressed_state(self) -> None:
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-003",
            "--change-id",
            "test-003",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        current = self.state_dir / "current"
        self.assertEqual((current / "current-image").read_text().strip(), RELEASE_IMAGE)
        self.assertEqual((current / "previous-image").read_text().strip(), LOCAL_IMAGE_ID)
        self.assertEqual(self.runtime.read_text(), self.controlled.read_text())
        self.assertEqual(
            (current / "current-evidence.json").read_text(),
            self.release_evidence.read_text(),
        )
        self.assertTrue((self.state_dir / "archive" / "evidence" / ("a" * 64 + ".json")).is_file())
        self.assertTrue((current / "current-attestation.json").is_file())
        self.assertEqual(
            (current / "last-role-grants-change-id").read_text().strip(),
            "test-role-grants-003",
        )
        docker_log = self.docker_log.read_text()
        self.assertIn("login ghcr.io --username AreaSong --password-stdin", docker_log)
        self.assertIn(f"pull {RELEASE_IMAGE}", docker_log)
        self.assertIn(f"manifest inspect {RELEASE_IMAGE}", docker_log)
        self.assertIn(
            'image inspect --format {{index .Config.Labels "org.opencontainers.image.revision"}}',
            docker_log,
        )
        self.assertNotIn(r'\"org.opencontainers.image.revision\"', docker_log)
        self.assertIn("--profile tools run --rm --no-deps migrate", docker_log)
        self.assertIn("verify_contract=1", docker_log)
        self.assertIn("up -d --no-deps --force-recreate web", docker_log)
        self.assertIn("/ready", self.curl_log.read_text())
        self.assertIn("/api/auth/status", self.curl_log.read_text())
        self.assertIn('account_vault_release_last_success{action="deploy"} 1', self.release_metric.read_text())
        self.assertEqual(list(self.registry_auth_root.iterdir()), [])

    def test_rollback_swaps_current_and_previous_without_migration(self) -> None:
        self._seed_release_state(RELEASE_IMAGE, PREVIOUS_IMAGE)
        evidence_dir = self.state_dir / "archive" / "evidence"
        evidence_dir.mkdir(parents=True)
        current_evidence = '{"release":"current"}\n'
        previous_evidence = '{"release":"previous"}\n'
        (evidence_dir / ("a" * 64 + ".json")).write_text(current_evidence, encoding="utf-8")
        (evidence_dir / ("b" * 64 + ".json")).write_text(previous_evidence, encoding="utf-8")
        attestation_dir = self.state_dir / "archive" / "attestation"
        attestation_dir.mkdir(parents=True)
        current_attestation = '{"attestation":"current"}\n'
        previous_attestation = '{"attestation":"previous"}\n'
        (attestation_dir / ("a" * 64 + ".json")).write_text(current_attestation, encoding="utf-8")
        (attestation_dir / ("b" * 64 + ".json")).write_text(previous_attestation, encoding="utf-8")
        result = self._run(
            "rollback",
            "--approve-rollback",
            "--change-id",
            "test-004",
            FAKE_RUNNING_IMAGE=RELEASE_IMAGE,
            FAKE_RELEASE_IMAGE=PREVIOUS_IMAGE,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        current = self.state_dir / "current"
        self.assertEqual((current / "current-image").read_text().strip(), PREVIOUS_IMAGE)
        self.assertEqual((current / "previous-image").read_text().strip(), RELEASE_IMAGE)
        self.assertEqual((current / "current-evidence.json").read_text(), previous_evidence)
        self.assertEqual((current / "previous-evidence.json").read_text(), current_evidence)
        self.assertEqual((current / "current-attestation.json").read_text(), previous_attestation)
        self.assertEqual((current / "previous-attestation.json").read_text(), current_attestation)
        docker_log = self.docker_log.read_text()
        self.assertNotIn(" migrate", docker_log)
        self.assertIn("up -d --no-deps --force-recreate web", docker_log)

    def test_legacy_image_rollback_uses_the_compatible_health_endpoint(self) -> None:
        self._seed_release_state(RELEASE_IMAGE, LOCAL_IMAGE_ID)
        result = self._run(
            "rollback",
            "--approve-rollback",
            "--change-id",
            "test-legacy",
            FAKE_RUNNING_IMAGE=RELEASE_IMAGE,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("legacy-health-override.yml", self.docker_log.read_text())
        self.assertIn("/health", self.curl_log.read_text())

    def test_deploy_blocks_when_the_local_backup_is_stale(self) -> None:
        backup = next(self.backup_root.iterdir())
        old_timestamp = time.time() - 7200
        os.utime(backup, (old_timestamp, old_timestamp))
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-005",
            "--change-id",
            "test-005",
            ACCOUNT_VAULT_MAX_BACKUP_AGE_MINUTES="30",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("manifest-selected Account Vault backup is stale", result.stderr)
        self.assertFalse(self.docker_log.exists())
        self.assertEqual(list(self.state_dir.glob("pending-attestation-*.json")), [])

    def test_sigterm_after_recreate_restores_runtime_and_records_failure(self) -> None:
        health_status = self.root / "health-status"
        health_status.write_text("starting\n", encoding="utf-8")
        old_runtime = self.runtime.read_text(encoding="utf-8")
        arguments = [
            "bash",
            str(RELEASE_SCRIPT),
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-signal",
            "--change-id",
            "test-signal",
        ]
        process = subprocess.Popen(
            arguments,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=self._environment(FAKE_HEALTH_STATUS_FILE=str(health_status)),
        )
        deadline = time.time() + 10
        while time.time() < deadline:
            if self.docker_log.exists() and "up -d --no-deps --force-recreate web" in self.docker_log.read_text():
                break
            time.sleep(0.05)
        else:
            process.kill()
            self.fail("release did not reach web recreation before the signal test deadline")

        health_status.write_text("healthy\n", encoding="utf-8")
        process.terminate()
        _, stderr = process.communicate(timeout=10)
        self.assertEqual(process.returncode, 143, stderr)
        self.assertEqual(self.runtime.read_text(encoding="utf-8"), old_runtime)
        self.assertIn('account_vault_release_last_success{action="deploy"} 0', self.release_metric.read_text())
        self.assertEqual(list(self.state_dir.glob("pending-attestation-*.json")), [])

    def test_deploy_rejects_evidence_for_another_digest(self) -> None:
        evidence = json.loads(self.release_evidence.read_text(encoding="utf-8"))
        evidence["repositoryDigest"] = PREVIOUS_IMAGE
        self.release_evidence.write_text(json.dumps(evidence) + "\n", encoding="utf-8")
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-evidence",
            "--change-id",
            "test-evidence",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("evidence validation failed", result.stderr)
        self.assertFalse(self.docker_log.exists())

    def test_deploy_fails_closed_when_attestation_verification_fails(self) -> None:
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-attestation",
            "--change-id",
            "test-attestation",
            FAKE_ATTESTATION_FAIL="true",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("keyless OCI attestation verification failed", result.stderr)
        self.assertFalse(self.docker_log.exists())

    def test_deploy_rejects_runtime_management_role_reuse(self) -> None:
        content = self.env_file.read_text(encoding="utf-8").replace(
            "DATABASE_APP_USER=account_vault_app", "DATABASE_APP_USER=account_user"
        )
        self.env_file.write_text(content, encoding="utf-8")
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-reuse",
            "--change-id",
            "test-role",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("runtime database role must differ", result.stderr)
        self.assertFalse(self.docker_log.exists())

    def test_deploy_requires_r2_verification_of_the_same_manifest(self) -> None:
        content = self.r2_state.read_text(encoding="utf-8").replace(
            "manifest_sha256=", f"manifest_sha256={'9' * 64}\nignored="
        )
        self.r2_state.write_text(content, encoding="utf-8")
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-r2",
            "--change-id",
            "test-r2-set",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("manifest hash does not match", result.stderr)
        self.assertFalse(self.docker_log.exists())

    def test_deploy_rejects_image_that_does_not_declare_node_user(self) -> None:
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-user",
            "--change-id",
            "test-user",
            FAKE_IMAGE_USER="root",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must declare the node runtime user", result.stderr)

    def test_deploy_accepts_containerd_manifest_id_when_registry_config_matches(self) -> None:
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-containerd",
            "--change-id",
            "test-containerd",
            FAKE_LOCAL_PULLED_IMAGE_ID="sha256:" + "a" * 64,
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_deploy_rejects_registry_config_digest_that_differs_from_evidence(self) -> None:
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-image-id",
            "--change-id",
            "test-image-id",
            FAKE_REGISTRY_CONFIG_DIGEST="sha256:" + "9" * 64,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("registry image config digest does not match", result.stderr)
        self.assertEqual(list(self.registry_auth_root.iterdir()), [])

    def test_deploy_rejects_nonproduction_origin_before_database_writes(self) -> None:
        content = self.env_file.read_text(encoding="utf-8").replace(
            "ORIGIN=https://sorryiossearch.areasong.top", "ORIGIN=http://127.0.0.1:8392"
        )
        self.env_file.write_text(content, encoding="utf-8")
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-origin",
            "--change-id",
            "test-origin",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("production Account Vault hostname", result.stderr)
        self.assertFalse(self.docker_log.exists())

    def test_failed_web_recreation_restores_compose_and_records_candidate(self) -> None:
        original = self.runtime.read_text(encoding="utf-8")
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-failure",
            "--change-id",
            "test-failure-restore",
            FAKE_HEALTH_STATUS="unhealthy",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.runtime.read_text(encoding="utf-8"), original)
        metric = self.release_metric.read_text(encoding="utf-8")
        self.assertIn('account_vault_release_last_success{action="deploy"} 0', metric)
        self.assertIn(RELEASE_IMAGE, metric)

    def test_deploy_requires_separate_role_grant_approval(self) -> None:
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--change-id",
            "test-role-approval",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--approve-role-grants", result.stderr)
        self.assertFalse(self.docker_log.exists())

    def test_deploy_requires_separate_role_grant_change_id(self) -> None:
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--change-id",
            "test-role-change-id",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--role-grants-change-id", result.stderr)
        self.assertFalse(self.docker_log.exists())

    def test_compose_compare_failure_restores_runtime_snapshot(self) -> None:
        original = self.runtime.read_text(encoding="utf-8")
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-compose",
            "--change-id",
            "test-compose-restore",
            FAKE_CMP_FAIL="true",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.runtime.read_text(encoding="utf-8"), original)

    def test_state_link_failure_keeps_the_previous_generation_authoritative(self) -> None:
        original = self.runtime.read_text(encoding="utf-8")
        self._seed_release_state(LOCAL_IMAGE_ID, PREVIOUS_IMAGE)
        result = self._run(
            "deploy",
            RELEASE_IMAGE,
            "--evidence",
            str(self.release_evidence),
            "--approve-migration",
            "--approve-role-grants",
            "--role-grants-change-id",
            "test-role-grants-state",
            "--change-id",
            "test-state-atomic",
            FAKE_STATE_LINK_FAIL="true",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.state_dir / "current").readlink(), Path("generations/seed"))
        self.assertEqual(
            (self.state_dir / "current" / "current-image").read_text().strip(),
            LOCAL_IMAGE_ID,
        )
        self.assertEqual(self.runtime.read_text(encoding="utf-8"), original)
        self.assertIn(
            'account_vault_release_last_success{action="deploy"} 0',
            self.release_metric.read_text(encoding="utf-8"),
        )


if __name__ == "__main__":
    unittest.main()
