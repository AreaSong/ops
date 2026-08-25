from __future__ import annotations

import hashlib
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ADAPTER = Path(__file__).resolve().parents[1] / "nginx-traffic.sh"


class NginxTrafficAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.operation = self.root / "operation"
        self.operation.mkdir()
        self.nginx_root = self.root / "nginx-root"
        self.site_dir = self.nginx_root / "etc/nginx/sites-enabled"
        self.snippet_dir = self.nginx_root / "etc/nginx/snippets/areasong-ops"
        self.site_dir.mkdir(parents=True)
        self.snippet_dir.mkdir(parents=True)
        self.site_policy = "/etc/nginx/sites-enabled/demo.conf"
        self.include_policy = "/etc/nginx/snippets/areasong-ops/demo-traffic.conf"
        self.maintenance_policy = "/etc/nginx/snippets/areasong-ops/demo-maintenance.conf"
        self.site = self.nginx_root / self.site_policy.lstrip("/")
        self.include = self.nginx_root / self.include_policy.lstrip("/")
        self.maintenance = self.nginx_root / self.maintenance_policy.lstrip("/")
        self.marker = f"include {self.include_policy};"
        self.site.write_text(
            "server {\n"
            "    listen 443 ssl;\n"
            "    server_name demo.areasong.top alias.areasong.top;\n"
            f"    {self.marker}\n"
            "}\n",
            encoding="utf-8",
        )
        self.include.write_text(
            "# AreaSong Ops managed traffic state: running\n", encoding="utf-8"
        )
        self.maintenance.write_text(
            'default_type text/plain;\nreturn 503 "Maintenance page\\n";\n',
            encoding="utf-8",
        )

        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.call_log = self.root / "calls.log"
        self.nginx = self.bin_dir / "nginx"
        self.systemctl = self.bin_dir / "systemctl"
        self._write_executable(
            self.nginx,
            "#!/bin/sh\n"
            f"printf 'nginx %s\\n' \"$*\" >>'{self.call_log}'\n"
            "[ \"${FAKE_NGINX_FAIL:-0}\" = 1 ] && exit 1\n"
            "[ \"$1\" = -t ]\n",
        )
        self._write_executable(
            self.systemctl,
            "#!/bin/sh\n"
            f"printf 'systemctl %s\\n' \"$*\" >>'{self.call_log}'\n"
            "[ \"${FAKE_SYSTEMCTL_FAIL:-0}\" = 1 ] && exit 1\n"
            "[ \"$1 $2\" = 'reload nginx' ]\n",
        )

    @staticmethod
    def _write_executable(path: Path, content: str) -> None:
        path.write_text(content, encoding="utf-8")
        path.chmod(0o755)

    def policy(self, **overrides: object) -> dict[str, object]:
        value: dict[str, object] = {
            "adapterPath": "/usr/local/libexec/areasong-ops/adapters/nginx-traffic.sh",
            "hostname": "demo.areasong.top",
            "siteFile": self.site_policy,
            "includeFile": self.include_policy,
            "maintenanceFile": self.maintenance_policy,
            "marker": self.marker,
            "drainTimeoutSeconds": 30,
        }
        value.update(overrides)
        return value

    def run_adapter(
        self,
        action: str,
        phase: str,
        *,
        policy: dict[str, object] | None = None,
        extra_environment: dict[str, str] | None = None,
    ) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "OPS_SERVICE_NAME": "demo",
                "OPS_TRAFFIC_POLICY_JSON": json.dumps(policy or self.policy()),
                "OPS_TRAFFIC_TEST_ROOT": str(self.nginx_root),
                "OPS_TRAFFIC_NGINX_EXECUTABLE": str(self.nginx),
                "OPS_TRAFFIC_SYSTEMCTL_EXECUTABLE": str(self.systemctl),
            }
        )
        if extra_environment:
            environment.update(extra_environment)
        return subprocess.run(
            [str(ADAPTER), action, phase, str(self.operation), "", ""],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )

    def payload(self, result: subprocess.CompletedProcess[str]) -> dict[str, object]:
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertEqual(len(result.stdout.strip().splitlines()), 1)
        payload = json.loads(result.stdout)
        self.assertEqual(payload["schemaVersion"], 2)
        self.assertTrue(payload["ok"])
        return payload

    def test_inspect_reports_running_with_exact_v2_contract(self) -> None:
        payload = self.payload(self.run_adapter("traffic", "inspect"))
        self.assertEqual(payload["action"], "traffic")
        self.assertEqual(payload["phase"], "inspect")
        self.assertEqual(payload["data"]["actualState"], "running")
        self.assertEqual(payload["data"]["trafficState"], "running")
        self.assertFalse(payload["data"]["productionChanged"])
        self.assertEqual(self.call_log.exists(), False)

    def test_policy_schema_matches_go_traffic_policy_and_rejects_driver(self) -> None:
        accepted = self.payload(self.run_adapter("traffic", "inspect"))
        self.assertEqual(accepted["data"]["hostname"], "demo.areasong.top")

        rejected = self.run_adapter(
            "traffic", "inspect", policy=self.policy(driver="nginx")
        )
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("missing or unsupported fields", rejected.stderr)
        self.assertFalse(self.call_log.exists())

    def test_three_actions_write_closed_templates_reload_and_verify(self) -> None:
        cases = [
            ("enter-maintenance", "maintenance", "include /etc/nginx/snippets/areasong-ops/demo-maintenance.conf;"),
            ("drain", "drained", "old workers complete naturally"),
            ("resume-traffic", "running", "managed traffic state: running"),
        ]
        for action, expected_state, expected_text in cases:
            with self.subTest(action=action):
                if action != "enter-maintenance":
                    self.operation = self.root / f"operation-{action}"
                    self.operation.mkdir()
                preflight = self.payload(self.run_adapter(action, "preflight"))
                self.assertEqual(preflight["data"]["desiredState"], expected_state)
                mutation = self.payload(self.run_adapter(action, action))
                self.assertEqual(mutation["data"]["trafficState"], expected_state)
                self.assertTrue(mutation["data"]["productionChanged"])
                if action == "drain":
                    self.assertEqual(mutation["data"]["activeConnections"], 0)
                content = self.include.read_text(encoding="utf-8")
                self.assertIn(expected_text, content)
                self.assertEqual(
                    mutation["data"]["includeDigest"],
                    "sha256:" + hashlib.sha256(self.include.read_bytes()).hexdigest(),
                )
                verification = self.payload(self.run_adapter(action, "verify"))
                self.assertEqual(verification["data"]["actualState"], expected_state)

        calls = self.call_log.read_text(encoding="utf-8").splitlines()
        self.assertEqual(calls.count("nginx -t"), 3)
        self.assertEqual(calls.count("systemctl reload nginx"), 3)

    def test_missing_marker_or_hostname_is_rejected_without_commands(self) -> None:
        self.site.write_text(
            "server { server_name wrong.areasong.top; }\n", encoding="utf-8"
        )
        result = self.run_adapter("traffic", "inspect")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exact traffic include marker", result.stderr)
        self.assertFalse(self.call_log.exists())

    def test_nginx_test_failure_restores_previous_include_and_reloads(self) -> None:
        before = self.include.read_bytes()
        marker = self.root / "nginx-failed-once"
        self._write_executable(
            self.nginx,
            "#!/bin/sh\n"
            f"printf 'nginx %s\\n' \"$*\" >>'{self.call_log}'\n"
            f"if [ ! -e '{marker}' ]; then : >'{marker}'; exit 1; fi\n"
            "exit 0\n",
        )
        self.include.write_bytes(before)
        failed = self.run_adapter("enter-maintenance", "enter-maintenance")
        self.assertNotEqual(failed.returncode, 0)
        self.assertIn("previous include was restored and reloaded", failed.stderr)
        self.assertEqual(self.include.read_bytes(), before)
        calls = self.call_log.read_text(encoding="utf-8")
        self.assertEqual(calls.count("nginx -t"), 2)
        self.assertEqual(calls.count("systemctl reload nginx"), 1)

    def test_systemctl_failure_reports_incomplete_rollback(self) -> None:
        before = self.include.read_bytes()
        failed = self.run_adapter(
            "enter-maintenance",
            "enter-maintenance",
            extra_environment={"FAKE_SYSTEMCTL_FAIL": "1"},
        )
        self.assertNotEqual(failed.returncode, 0)
        self.assertIn("rollback could not be completed", failed.stderr)
        self.assertEqual(self.include.read_bytes(), before)
        calls = self.call_log.read_text(encoding="utf-8").splitlines()
        self.assertEqual(calls.count("nginx -t"), 2)
        self.assertEqual(calls.count("systemctl reload nginx"), 2)

    def test_explicit_rollback_restores_snapshot_and_reloads(self) -> None:
        before = self.include.read_bytes()
        changed = self.payload(
            self.run_adapter("enter-maintenance", "enter-maintenance")
        )
        self.assertTrue(changed["data"]["productionChanged"])

        rolled_back = self.payload(
            self.run_adapter("enter-maintenance", "rollback")
        )
        self.assertEqual(rolled_back["data"]["trafficState"], "running")
        self.assertTrue(rolled_back["data"]["productionChanged"])
        self.assertEqual(self.include.read_bytes(), before)
        calls = self.call_log.read_text(encoding="utf-8").splitlines()
        self.assertEqual(calls.count("nginx -t"), 2)
        self.assertEqual(calls.count("systemctl reload nginx"), 2)

    def test_repeating_same_action_is_idempotent_without_reload(self) -> None:
        first = self.payload(
            self.run_adapter("enter-maintenance", "enter-maintenance")
        )
        self.assertTrue(first["data"]["productionChanged"])
        content_after_first = self.include.read_bytes()

        second = self.payload(
            self.run_adapter("enter-maintenance", "enter-maintenance")
        )
        self.assertFalse(second["data"]["productionChanged"])
        self.assertEqual(self.include.read_bytes(), content_after_first)
        calls = self.call_log.read_text(encoding="utf-8").splitlines()
        self.assertEqual(calls.count("nginx -t"), 1)
        self.assertEqual(calls.count("systemctl reload nginx"), 1)

    def test_symlink_and_out_of_scope_paths_are_rejected(self) -> None:
        real_include = self.snippet_dir / "real.conf"
        real_include.write_text(
            "# AreaSong Ops managed traffic state: running\n", encoding="utf-8"
        )
        self.include.unlink()
        self.include.symlink_to(real_include)
        symlink_result = self.run_adapter("traffic", "inspect")
        self.assertNotEqual(symlink_result.returncode, 0)
        self.assertIn("symlink is forbidden", symlink_result.stderr)

        outside_result = self.run_adapter(
            "traffic",
            "inspect",
            policy=self.policy(includeFile="/tmp/demo.conf", marker="include /tmp/demo.conf;"),
        )
        self.assertNotEqual(outside_result.returncode, 0)
        self.assertIn("outside the controlled Nginx directory", outside_result.stderr)

    def test_unmanaged_include_and_contract_mismatch_are_rejected(self) -> None:
        self.include.write_text("return 200;\n", encoding="utf-8")
        unmanaged = self.run_adapter("traffic", "inspect")
        self.assertNotEqual(unmanaged.returncode, 0)
        self.assertIn("managed closed-set template", unmanaged.stderr)

        unsupported = self.run_adapter("traffic", "verify")
        self.assertNotEqual(unsupported.returncode, 0)
        self.assertIn("unsupported traffic action or phase", unsupported.stderr)

    def test_drain_waits_for_old_workers_and_reports_completion(self) -> None:
        worker_state = self.nginx_root / "workers.txt"
        worker_state.write_text("101 102\n", encoding="utf-8")
        self._write_executable(
            self.nginx,
            "#!/bin/sh\n"
            f"printf 'nginx %s\\n' \"$*\" >>'{self.call_log}'\n"
            "[ \"$1\" = -t ]\n",
        )
        result = self.run_adapter(
            "drain",
            "drain",
            extra_environment={
                "OPS_TRAFFIC_TEST_DRAIN_STATE_FILE": str(worker_state),
                "OPS_TRAFFIC_TEST_DRAIN_TIMEOUT_SECONDS": "2",
                "OPS_TRAFFIC_TEST_DRAIN_POLL_SECONDS": "0.01",
            },
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("drain timed out", result.stderr)

    def test_maintenance_template_references_operator_owned_file(self) -> None:
        payload = self.payload(
            self.run_adapter("enter-maintenance", "enter-maintenance")
        )
        self.assertEqual(payload["data"]["trafficState"], "maintenance")
        self.assertIn("maintenanceDigest", payload["data"])


if __name__ == "__main__":
    unittest.main()
