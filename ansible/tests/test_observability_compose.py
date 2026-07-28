from __future__ import annotations

import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
COMPOSE_PATH = REPO_ROOT / "observability" / "docker-compose.yml"


class ObservabilityComposeTests(unittest.TestCase):
    def test_every_service_has_a_read_only_root_and_drops_all_capabilities(self) -> None:
        services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]
        self.assertEqual(len(services), 10)
        for name, service in services.items():
            with self.subTest(service=name):
                self.assertTrue(service.get("read_only"))
                self.assertEqual(service.get("cap_drop"), ["ALL"])
                self.assertIn("no-new-privileges:true", service.get("security_opt", []))

    def test_every_service_has_a_bounded_healthcheck(self) -> None:
        services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]
        for name, service in services.items():
            with self.subTest(service=name):
                healthcheck = service.get("healthcheck", {})
                self.assertTrue(healthcheck.get("test"))
                self.assertGreater(healthcheck.get("retries", 0), 0)

    def test_distroless_redis_exporter_healthcheck_uses_its_binary(self) -> None:
        services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]
        healthcheck = services["redis-exporter-sub2api"]["healthcheck"]["test"]
        self.assertEqual(healthcheck, ["CMD", "/redis_exporter", "--version"])
        self.assertNotIn("wget", healthcheck)

    def test_metrics_healthchecks_consume_the_complete_response(self) -> None:
        services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]
        for name in (
            "node-exporter",
            "blackbox-exporter",
            "postgres-exporter-sub2api",
            "postgres-exporter-account-vault",
        ):
            with self.subTest(service=name):
                healthcheck = services[name]["healthcheck"]["test"]
                self.assertNotIn("--spider", healthcheck)
                self.assertIn("-O", healthcheck)
                self.assertIn("/dev/null", healthcheck)

    def test_reloadable_configs_use_directory_bind_mounts(self) -> None:
        services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]
        expected_mounts = {
            "prometheus": "/opt/ops/observability/prometheus:/etc/prometheus:ro",
            "loki": "/opt/ops/observability/loki:/etc/loki:ro",
            "promtail": "/opt/ops/observability/promtail:/etc/promtail:ro",
            "blackbox-exporter": (
                "/opt/ops/observability/blackbox:/etc/blackbox_exporter:ro"
            ),
        }
        for name, mount in expected_mounts.items():
            with self.subTest(service=name):
                self.assertIn(mount, services[name]["volumes"])

        self.assertIn(
            "--config.file=/etc/blackbox_exporter/blackbox.yml",
            services["blackbox-exporter"]["command"],
        )

    def test_promtail_can_read_host_adm_logs_without_capabilities(self) -> None:
        services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]
        promtail = services["promtail"]
        self.assertEqual(promtail["group_add"], ["4"])
        self.assertEqual(promtail["cap_drop"], ["ALL"])


if __name__ == "__main__":
    unittest.main()
