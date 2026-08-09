from __future__ import annotations

import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).resolve().parents[1] / "runtime_snapshot.py"
SPEC = importlib.util.spec_from_file_location("runtime_snapshot", MODULE_PATH)
assert SPEC and SPEC.loader
runtime_snapshot = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runtime_snapshot)


def missing_runtime(command: list[str], timeout: int = 30) -> subprocess.CompletedProcess[str]:
    if command[0] == "systemctl":
        return subprocess.CompletedProcess(command, 1, "", "unit not found")
    return subprocess.CompletedProcess(command, 0, "", "")


class RuntimeSnapshotTests(unittest.TestCase):
    def test_parse_listener_line(self) -> None:
        parsed = runtime_snapshot.parse_listener_line(
            'tcp LISTEN 0 511 127.0.0.1:3000 0.0.0.0:* users:(("grafana",pid=12,fd=3))'
        )
        self.assertEqual(parsed["protocol"], "tcp")
        self.assertEqual(parsed["address"], "127.0.0.1")
        self.assertEqual(parsed["port"], "3000")
        self.assertEqual(parsed["process"], "grafana")

    def test_route_drift_requires_cloudflare_snippets(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            nginx_file = Path(directory) / "monitor.conf"
            nginx_file.write_text(
                "server_name monitor.areasong.top;\n"
                "proxy_pass http://127.0.0.1:3000;\n",
                encoding="utf-8",
            )
            inventory = {
                "routes": [
                    {
                        "service_id": "observability-stack",
                        "hostname": "monitor.areasong.top",
                        "origin_policy": "cloudflare-only",
                        "nginx_file": str(nginx_file),
                        "backend_endpoints": ["http://127.0.0.1:3000"],
                    }
                ]
            }
            drift = runtime_snapshot.evaluate_routes(inventory)[0]
            self.assertTrue(drift["drift"])
            self.assertEqual(drift["observed_origin_policy"], "direct")
            self.assertIn("cloudflare-origin-only.conf", drift["missing_expectations"])

            nginx_file.write_text(
                "include cloudflare-real-ip.conf;\n"
                "include cloudflare-origin-only.conf;\n"
                "server_name monitor.areasong.top;\n"
                "proxy_pass http://127.0.0.1:3000;\n",
                encoding="utf-8",
            )
            converged = runtime_snapshot.evaluate_routes(inventory)[0]
            self.assertFalse(converged["drift"])
            self.assertEqual(converged["observed_origin_policy"], "cloudflare-only")

    def test_planned_runtime_components_are_observed_but_not_enforced(self) -> None:
        inventory = {
            "services": [
                {
                    "service_id": "demo",
                    "lifecycle": "planned",
                    "runtime": {
                        "units": ["demo.service"],
                        "containers": ["demo-web"],
                    },
                }
            ]
        }

        with mock.patch.object(runtime_snapshot, "run", side_effect=missing_runtime):
            systemd, systemd_ok = runtime_snapshot.collect_systemd(inventory)
            containers, containers_ok = runtime_snapshot.collect_containers(inventory)

        self.assertTrue(systemd_ok)
        self.assertTrue(containers_ok)
        self.assertEqual(systemd[0]["lifecycle"], "planned")
        self.assertEqual(containers[0]["lifecycle"], "planned")
        self.assertFalse(containers[0]["enforced"])
        self.assertFalse(containers[0]["present"])

    def test_production_runtime_components_remain_enforced(self) -> None:
        inventory = {
            "services": [
                {
                    "service_id": "demo",
                    "lifecycle": "production",
                    "runtime": {
                        "units": ["demo.service"],
                        "containers": ["demo-web"],
                    },
                }
            ]
        }

        with mock.patch.object(runtime_snapshot, "run", side_effect=missing_runtime):
            _, systemd_ok = runtime_snapshot.collect_systemd(inventory)
            containers, containers_ok = runtime_snapshot.collect_containers(inventory)

        self.assertFalse(systemd_ok)
        self.assertFalse(containers_ok)
        self.assertTrue(containers[0]["enforced"])

    def test_planned_configuration_and_route_do_not_report_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            controlled = root / "compose.yml"
            controlled.write_text("services: {}\n", encoding="utf-8")
            config_drift = runtime_snapshot.evaluate_config_pairs(
                {
                    "config_pairs": [
                        {
                            "service_id": "demo",
                            "lifecycle": "planned",
                            "runtime": str(root / "missing-runtime.yml"),
                            "controlled": str(controlled),
                        }
                    ]
                }
            )[0]
            route_drift = runtime_snapshot.evaluate_routes(
                {
                    "routes": [
                        {
                            "service_id": "demo",
                            "hostname": "demo.example.com",
                            "lifecycle": "planned",
                            "desired_origin_policy": "cloudflare-only",
                            "nginx_file": str(root / "missing-nginx.conf"),
                            "backend_endpoints": ["http://127.0.0.1:8080"],
                        }
                    ]
                }
            )[0]

        self.assertFalse(config_drift["enforced"])
        self.assertFalse(config_drift["drift"])
        self.assertFalse(route_drift["enforced"])
        self.assertFalse(route_drift["drift"])
        self.assertTrue(route_drift["missing_expectations"])

    def test_render_metrics_contains_static_and_runtime_mappings(self) -> None:
        inventory = {
            "services": [
                {
                    "service_id": "demo",
                    "name": "Demo",
                    "owner": "as",
                    "lifecycle": "production",
                    "runtime": {"type": "docker-compose", "containers": ["demo-1"]},
                    "ports": [{"bind": "127.0.0.1", "host_port": 8080, "protocol": "tcp"}],
                }
            ],
            "routes": [],
        }
        metrics = runtime_snapshot.render_metrics(
            inventory,
            [],
            [],
            [{"name": "demo-1", "service_id": "demo", "present": True, "security": {}}],
            [],
            True,
            123,
        )
        self.assertIn('ops_asset_service_info{service_id="demo"', metrics)
        self.assertIn('ops_asset_port_info{service_id="demo"', metrics)
        self.assertIn('ops_container_runtime_info{service_id="demo",name="demo-1"', metrics)


if __name__ == "__main__":
    unittest.main()
