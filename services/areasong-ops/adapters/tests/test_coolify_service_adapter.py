from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ADAPTER = Path(__file__).resolve().parents[1] / "coolify-service.sh"


class CoolifyServiceAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.operation = self.root / "operation"
        self.operation.mkdir()
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()

        # Create secure token file
        self.token_file = self.root / "coolify.token"
        self.token_file.write_text("test-token-abc123def456", encoding="utf-8")
        self.token_file.chmod(0o400)

        # Create trusted endpoints configuration
        self.trusted_config = self.root / "coolify-trusted-endpoints.json"
        self.trusted_config.write_text(
            json.dumps(
                {
                    "schemaVersion": 1,
                    "trustedEndpoints": {
                        "test-endpoint": {
                            "id": "test-endpoint",
                            "endpoint": "https://coolify.example.com",
                            "description": "Test Coolify instance",
                            "allowedServices": ["*"],
                            "tokenFile": str(self.token_file),
                        }
                    },
                    "allowedServiceUuids": {
                        "6oqw6kcjzweijv6r9ux71zoe": "areasong-ops"
                    },
                }
            ),
            encoding="utf-8",
        )
        # Make trusted config root-owned (we can't actually change owner in test, so we'll patch stat)

        # Create service catalog with Coolify configuration
        runtime = {
            "trustedEndpointId": "test-endpoint",
            "serviceUuid": "6oqw6kcjzweijv6r9ux71zoe",
            "healthUrl": "http://127.0.0.1:3200/health",
            "latestAllowed": False,
            "dependencyServiceUuids": [],
        }
        self.catalog = self.root / "services.json"
        self.catalog.write_text(
            json.dumps(
                {
                    "schemaVersion": 4,
                    "services": {
                        "areasong-ops": {
                            "name": "areasong-ops",
                            "displayName": "AreaSong Ops",
                            "description": "Ops web service on Coolify",
                            "template": "coolify-service-v1",
                            "adapterRef": "coolify-service-v1",
                            "runtime": runtime,
                            "actions": {},
                        }
                    },
                }
            ),
            encoding="utf-8",
        )

    def _create_mock_curl(self, responses: dict[str, str]) -> None:
        """Create a mock curl that responds to API calls."""
        curl = self.bin_dir / "curl"
        script_lines = [
            "#!/usr/bin/env bash",
            "set -eu",
            "method=''",
            "url=''",
            "out=''",
            "auth=''",
            "while [[ $# -gt 0 ]]; do",
            "  case $1 in",
            "    -X) method=$2; shift 2 ;;",
            "    -H) if [[ $2 == Authorization:* ]]; then auth=$2; fi; shift 2 ;;",
            "    -o) out=$2; shift 2 ;;",
            "    -w) shift 2 ;;",
            "    --max-time|--connect-timeout) shift 2 ;;",
            "    -sSf|-d) shift ;;",
            "    http*) url=$1; shift ;;",
            "    *) shift ;;",
            "  esac",
            "done",
            # Verify authentication
            '[[ "$auth" == "Authorization: Bearer test-token-abc123def456" ]] || { echo "unauthorized" >&2; exit 22; }',
            # Route based on URL pattern
        ]

        for pattern, response in responses.items():
            script_lines.extend(
                [
                    f'if [[ "$url" == *"{pattern}"* ]]; then',
                    f'  [[ -n "$out" ]] && printf \'%s\\n\' \'{response}\' >"$out"',
                    "  printf '200\\n'",
                    "  exit 0",
                    "fi",
                ]
            )

        script_lines.extend(
            [
                'echo "unmatched URL: $url" >&2',
                "exit 1",
            ]
        )

        curl.write_text("\n".join(script_lines) + "\n", encoding="utf-8")
        curl.chmod(0o755)

    def _create_mock_stat(self) -> None:
        """Create a mock stat command that reports root ownership for trusted config."""
        stat = self.bin_dir / "stat"
        stat.write_text(
            "\n".join(
                [
                    "#!/usr/bin/env bash",
                    f"trusted_config={str(self.trusted_config)!r}",
                    'if [[ "$*" == *"$trusted_config"* ]]; then',
                    '  if [[ "$*" == *"%u"* ]]; then',
                    '    echo "0"  # root uid',
                    '  elif [[ "$*" == *"%a"* ]]; then',
                    '    echo "644"  # mode',
                    "  fi",
                    "  exit 0",
                    "fi",
                    # For token file
                    'if [[ "$*" == *"%u"* ]]; then echo "0"; fi',
                    'if [[ "$*" == *"%a"* ]]; then echo "400"; fi',
                ]
            )
            + "\n",
            encoding="utf-8",
        )
        stat.chmod(0o755)

    def run_adapter(
        self, action: str, phase: str, expect_success: bool = True, skip_default_stat: bool = False
    ) -> subprocess.CompletedProcess[str]:
        if not skip_default_stat and not (self.bin_dir / "stat").exists():
            self._create_mock_stat()
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
                "OPS_SERVICE_NAME": "areasong-ops",
                "OPS_SERVICE_CATALOG": str(self.catalog),
                "COOLIFY_TRUSTED_CONFIG": str(self.trusted_config),
            }
        )
        result = subprocess.run(
            [str(ADAPTER), action, phase, str(self.operation), "", ""],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )
        if expect_success:
            self.assertEqual(
                result.returncode, 0, f"stderr: {result.stderr}\nstdout: {result.stdout}"
            )
        return result

    def test_token_file_must_be_secure(self) -> None:
        """Token file must have mode 0[0-7]00 (not readable by group/others)."""
        self.token_file.chmod(0o644)
        # Create custom stat that returns insecure permissions for token file
        stat = self.bin_dir / "stat"
        stat.write_text(
            "\n".join([
                "#!/usr/bin/env bash",
                f"trusted_config={str(self.trusted_config)!r}",
                f"token_file={str(self.token_file)!r}",
                'if [[ "$*" == *"$trusted_config"* ]]; then',
                '  if [[ "$*" == *"%u"* ]]; then echo "0"; fi',
                '  if [[ "$*" == *"%a"* ]]; then echo "644"; fi',
                'elif [[ "$*" == *"$token_file"* ]]; then',
                '  if [[ "$*" == *"%u"* ]]; then echo "0"; fi',  # root
                '  if [[ "$*" == *"%a"* ]]; then echo "644"; fi',  # insecure!
                "fi",
            ]) + "\n",
            encoding="utf-8",
        )
        stat.chmod(0o755)
        self._create_mock_curl({"/api/v1/services/": '{"status":"running"}'})
        result = self.run_adapter("inspect", "inspect", expect_success=False, skip_default_stat=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("token file must have mode", result.stderr)

    def test_token_is_never_leaked_in_errors(self) -> None:
        """Token must be sanitized from error messages."""
        self._create_mock_curl({"/api/v1/services/": '{"error":"test"}'})
        # Intentionally trigger an API error
        curl = self.bin_dir / "curl"
        curl.write_text(
            "#!/bin/sh\necho 'Authorization: Bearer test-token-abc123def456 failed' >&2\nexit 22\n",
            encoding="utf-8",
        )
        curl.chmod(0o755)
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotIn("test-token-abc123def456", result.stderr)
        self.assertIn("[REDACTED]", result.stderr)

    def test_inspect_returns_service_status(self) -> None:
        """Inspect should query Coolify and return service status."""
        self._create_mock_curl(
            {"/api/v1/services/6oqw6kcjzweijv6r9ux71zoe": '{"status":"running","updated_at":"2026-11-10T10:00:00Z"}'}
        )
        result = self.run_adapter("inspect", "inspect")
        payload = json.loads(result.stdout)
        self.assertEqual(payload["action"], "inspect")
        self.assertEqual(payload["phase"], "inspect")
        self.assertTrue(payload["ok"])
        self.assertEqual(payload["data"]["currentStatus"], "running")
        self.assertEqual(payload["data"]["serviceUuid"], "6oqw6kcjzweijv6r9ux71zoe")

    def test_api_timeout_is_enforced(self) -> None:
        """API calls must time out and not hang indefinitely."""
        curl = self.bin_dir / "curl"
        # Simulate timeout by exiting with curl's timeout exit code (28)
        curl.write_text("#!/bin/sh\nexit 28\n", encoding="utf-8")
        curl.chmod(0o755)
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("API call failed", result.stderr)

    def test_restart_phases_trigger_coolify_restart(self) -> None:
        """Restart action should call Coolify API and verify health."""
        call_log = self.root / "api-calls.txt"
        curl = self.bin_dir / "curl"
        curl.write_text(
            "\n".join(
                [
                    "#!/usr/bin/env bash",
                    "set -eu",
                    f"log={str(call_log)!r}",
                    "method=''",
                    "url=''",
                    "out=''",
                    "while [[ $# -gt 0 ]]; do",
                    "  case $1 in",
                    "    -X) method=$2; shift 2 ;;",
                    "    -o) out=$2; shift 2 ;;",
                    "    -w) shift 2 ;;",
                    "    http*) url=$1; shift ;;",
                    "    *) shift ;;",
                    "  esac",
                    "done",
                    # Handle health endpoint separately
                    'if [[ "$url" == *"127.0.0.1:3200"* ]]; then',
                    '  [[ -n "$out" ]] && printf \'{"ok":true}\\n\' >"$out"',
                    "  printf '200\\n'",
                    "  exit 0",
                    "fi",
                    'printf "%s %s\\n" "$method" "$url" >>"$log"',
                    'if [[ "$url" == */restart ]]; then',
                    '  response=\'{"message":"restarting"}\'',
                    'elif [[ "$url" == */services/* ]]; then',
                    '  response=\'{"status":"running"}\'',
                    "else",
                    '  response=\'{"ok":true}\'',
                    "fi",
                    '[[ -n "$out" ]] && printf "%s\\n" "$response" >"$out"',
                    "printf '200\\n'",
                ]
            )
            + "\n",
            encoding="utf-8",
        )
        curl.chmod(0o755)

        for phase in ("preflight", "restart", "health"):
            result = self.run_adapter("restart", phase)
            payload = json.loads(result.stdout)
            self.assertEqual(payload["phase"], phase)

        calls = call_log.read_text(encoding="utf-8").strip().split("\n")
        # Should have: restart call + multiple status checks for wait_service_state
        self.assertTrue(any("POST" in call and "/restart" in call for call in calls))

    def test_start_and_stop_lifecycle(self) -> None:
        """Start and stop should call Coolify API correctly."""
        self._create_mock_curl(
            {
                "/api/v1/services/6oqw6kcjzweijv6r9ux71zoe/start": '{"message":"started"}',
                "/api/v1/services/6oqw6kcjzweijv6r9ux71zoe/stop": '{"message":"stopped"}',
                "/api/v1/services/6oqw6kcjzweijv6r9ux71zoe": '{"status":"running"}',
            }
        )

        # Test start
        result = self.run_adapter("start", "start")
        payload = json.loads(result.stdout)
        self.assertTrue(payload["ok"])
        self.assertTrue(payload["data"]["productionChanged"])

        # Test stop
        self._create_mock_curl(
            {
                "/api/v1/services/6oqw6kcjzweijv6r9ux71zoe/stop": '{"message":"stopped"}',
                "/api/v1/services/6oqw6kcjzweijv6r9ux71zoe": '{"status":"stopped"}',
            }
        )
        result = self.run_adapter("stop", "stop")
        payload = json.loads(result.stdout)
        self.assertTrue(payload["ok"])
        self.assertTrue(payload["data"]["productionChanged"])

    def test_dependency_tracking_prevents_unexpected_changes(self) -> None:
        """Dependencies must remain unchanged during operations."""
        # Add a dependency service
        catalog = json.loads(self.catalog.read_text(encoding="utf-8"))
        catalog["services"]["areasong-ops"]["runtime"]["dependencyServiceUuids"] = [
            "cmajwelgaiyouyev0pzqk2j2"
        ]
        self.catalog.write_text(json.dumps(catalog), encoding="utf-8")

        def create_curl(dep_updated_at: str) -> None:
            curl = self.bin_dir / "curl"
            script = [
                "#!/usr/bin/env bash",
                "set -eu",
                "url=''",
                "out=''",
                "while [[ $# -gt 0 ]]; do",
                "  case $1 in",
                "    -o) out=$2; shift 2 ;;",
                "    http*) url=$1; shift ;;",
                "    *) shift ;;",
                "  esac",
                "done",
                'if [[ "$url" == */6oqw6kcjzweijv6r9ux71zoe/restart ]]; then',
                '  response=\'{"message":"restarting"}\'',
                'elif [[ "$url" == */6oqw6kcjzweijv6r9ux71zoe ]]; then',
                '  response=\'{"status":"running"}\'',
                'elif [[ "$url" == */cmajwelgaiyouyev0pzqk2j2 ]]; then',
                f'  response=\'{{"status":"running","updated_at":"{dep_updated_at}"}}\'',
                "else",
                '  response=\'{"ok":true}\'',
                "fi",
                '[[ -n "$out" ]] && printf "%s\\n" "$response" >"$out"',
                "printf '200\\n'",
            ]
            curl.write_text("\n".join(script) + "\n", encoding="utf-8")
            curl.chmod(0o755)

        # Initial state
        create_curl("2026-11-10T10:00:00Z")

        # Preflight should succeed and record baseline
        result = self.run_adapter("restart", "preflight")
        self.assertTrue(json.loads(result.stdout)["ok"])

        # Restart phase succeeds
        result = self.run_adapter("restart", "restart")
        self.assertTrue(json.loads(result.stdout)["ok"])

        # Change dependency state
        create_curl("2026-11-10T10:01:00Z")

        # Health check should detect dependency change and fail
        result = self.run_adapter("restart", "health", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dependency service state changed", result.stderr)

    def test_unsupported_actions_fail_explicitly(self) -> None:
        """Actions not yet implemented should fail with clear messages."""
        for action in ("prepare", "update", "rollback", "restore", "restore-drill"):
            result = self.run_adapter(action, "preflight", expect_success=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("not yet implemented", result.stderr)

    def test_backup_actions_are_rejected(self) -> None:
        """Backup must go through host-level scripts, not Coolify API."""
        result = self.run_adapter("backup", "preflight", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("backup operations must use host-level scripts", result.stderr)

    def test_invalid_uuid_format_is_rejected(self) -> None:
        """Service UUID must match Coolify's format."""
        catalog = json.loads(self.catalog.read_text(encoding="utf-8"))
        catalog["services"]["areasong-ops"]["runtime"]["serviceUuid"] = "invalid-uuid"
        self.catalog.write_text(json.dumps(catalog), encoding="utf-8")
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        # Can fail on allowlist check or format check
        self.assertTrue(
            "not in trusted allowlist" in result.stderr or
            "service UUID format is invalid" in result.stderr
        )

    def test_http_endpoint_scheme_validation(self) -> None:
        """Coolify endpoint must use https or localhost http."""
        # Try to use ftp:// scheme
        trusted_config = json.loads(self.trusted_config.read_text(encoding="utf-8"))
        trusted_config["trustedEndpoints"]["test-endpoint"]["endpoint"] = "ftp://bad.com"
        self.trusted_config.write_text(json.dumps(trusted_config), encoding="utf-8")
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must be https://", result.stderr)

    def test_wait_service_state_has_bounded_timeout(self) -> None:
        """State waiting must not hang indefinitely."""
        # Service never reaches desired state
        self._create_mock_curl({"/api/v1/services/": '{"status":"starting"}'})
        result = self.run_adapter("start", "health", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("timeout waiting for service", result.stderr)

    def test_ssrf_protection_userinfo_rejected(self) -> None:
        """Endpoint with @ (userinfo) must be rejected to prevent SSRF."""
        # Try to inject userinfo to redirect authorization
        trusted_config = json.loads(self.trusted_config.read_text(encoding="utf-8"))
        trusted_config["trustedEndpoints"]["test-endpoint"]["endpoint"] = \
            "https://attacker.com@victim.com/api"
        self.trusted_config.write_text(json.dumps(trusted_config), encoding="utf-8")
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        # URL validation should reject this (either as invalid format or explicit @ check)
        self.assertTrue(
            "must not contain @" in result.stderr or
            "must be https://" in result.stderr
        )

    def test_trusted_endpoint_must_be_in_allowlist(self) -> None:
        """Service can only use endpoints from trusted configuration."""
        # Try to use non-existent endpoint
        catalog = json.loads(self.catalog.read_text(encoding="utf-8"))
        catalog["services"]["areasong-ops"]["runtime"]["trustedEndpointId"] = "non-existent"
        self.catalog.write_text(json.dumps(catalog), encoding="utf-8")
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not found in configuration", result.stderr)

    def test_uuid_must_be_in_allowlist(self) -> None:
        """Service UUID must be in trusted allowlist."""
        # Try to use UUID not in allowlist
        catalog = json.loads(self.catalog.read_text(encoding="utf-8"))
        catalog["services"]["areasong-ops"]["runtime"]["serviceUuid"] = "abcdefghijklmnopqrstuvwx"
        self.catalog.write_text(json.dumps(catalog), encoding="utf-8")
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not in trusted allowlist", result.stderr)

    def test_service_must_be_allowed_for_endpoint(self) -> None:
        """Service must be in endpoint's allowedServices list."""
        # Create endpoint that doesn't allow this service
        trusted_config = json.loads(self.trusted_config.read_text(encoding="utf-8"))
        trusted_config["trustedEndpoints"]["test-endpoint"]["allowedServices"] = ["other-service"]
        self.trusted_config.write_text(json.dumps(trusted_config), encoding="utf-8")
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("is not allowed for endpoint", result.stderr)

    def test_token_file_owner_must_be_root(self) -> None:
        """Token file must be owned by root (uid 0)."""
        # Create custom stat that returns non-root owner for token file
        stat = self.bin_dir / "stat"
        stat.write_text(
            "\n".join([
                "#!/usr/bin/env bash",
                f"trusted_config={str(self.trusted_config)!r}",
                f"token_file={str(self.token_file)!r}",
                'if [[ "$*" == *"$trusted_config"* ]]; then',
                '  if [[ "$*" == *"%u"* ]]; then echo "0"; fi',  # trusted config: root
                '  if [[ "$*" == *"%a"* ]]; then echo "644"; fi',
                'elif [[ "$*" == *"$token_file"* ]]; then',
                '  if [[ "$*" == *"%u"* ]]; then echo "1000"; fi',  # token file: non-root!
                '  if [[ "$*" == *"%a"* ]]; then echo "400"; fi',
                "fi",
            ]) + "\n",
            encoding="utf-8",
        )
        stat.chmod(0o755)
        self._create_mock_curl({"/api/v1/services/": '{"status":"running"}'})
        result = self.run_adapter("inspect", "inspect", expect_success=False, skip_default_stat=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must be owned by root", result.stderr)

    def test_401_authentication_error_has_clear_message(self) -> None:
        """401 errors should have clear authentication failure message."""
        curl = self.bin_dir / "curl"
        curl.write_text("#!/bin/sh\nexit 22\n", encoding="utf-8")
        curl.chmod(0o755)
        # Create a curl that returns 401
        curl.write_text(
            "\n".join([
                "#!/bin/sh",
                "out=''",
                "while [ $# -gt 0 ]; do",
                "  case $1 in -o) out=$2; shift 2 ;; *) shift ;; esac",
                "done",
                '[ -n "$out" ] && echo \'{"error":"unauthorized"}\' >"$out"',
                "echo '401'",
            ]) + "\n",
            encoding="utf-8",
        )
        curl.chmod(0o755)
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("authentication failed", result.stderr)

    def test_large_error_body_is_truncated(self) -> None:
        """Large error bodies should be truncated to prevent log flooding."""
        curl = self.bin_dir / "curl"
        large_body = "ERROR" * 200  # 1000 characters
        curl.write_text(
            "\n".join([
                "#!/bin/sh",
                "out=''",
                "while [ $# -gt 0 ]; do",
                "  case $1 in -o) out=$2; shift 2 ;; *) shift ;; esac",
                "done",
                f'[ -n "$out" ] && echo \'{large_body}\' >"$out"',
                "echo '500'",
            ]) + "\n",
            encoding="utf-8",
        )
        curl.chmod(0o755)
        result = self.run_adapter("inspect", "inspect", expect_success=False)
        self.assertNotEqual(result.returncode, 0)
        # Error should be truncated
        self.assertIn("[truncated]", result.stderr)


if __name__ == "__main__":
    unittest.main()
