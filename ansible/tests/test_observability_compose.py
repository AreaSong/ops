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


if __name__ == "__main__":
    unittest.main()
