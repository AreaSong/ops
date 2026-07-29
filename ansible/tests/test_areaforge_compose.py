from __future__ import annotations

import re
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
COMPOSE_PATH = REPO_ROOT / "services" / "areaforge" / "compose.yml"
DIGEST_IMAGE = re.compile(r"^[^@]+@sha256:[0-9a-f]{64}$")
REQUIRED_IMAGE = "${AREAFORGE_IMAGE:?AREAFORGE_IMAGE is required}"


class AreaForgeComposeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]

    def test_images_are_immutable_and_runtime_users_are_explicit(self) -> None:
        self.assertEqual(self.services["web"]["image"], REQUIRED_IMAGE)
        self.assertRegex(self.services["postgres"]["image"], DIGEST_IMAGE)
        self.assertEqual(self.services["web"]["user"], "nextjs")
        self.assertEqual(self.services["postgres"]["user"], "postgres")

    def test_web_runtime_identity_uses_the_same_required_image(self) -> None:
        self.assertEqual(self.services["web"]["environment"]["AREAFORGE_IMAGE"], REQUIRED_IMAGE)

    def test_both_services_have_the_production_hardening_contract(self) -> None:
        for name, service in self.services.items():
            with self.subTest(service=name):
                self.assertTrue(service["read_only"])
                self.assertEqual(service["cap_drop"], ["ALL"])
                self.assertIn("no-new-privileges:true", service["security_opt"])
                self.assertGreater(service["pids_limit"], 0)
                self.assertTrue(service["mem_limit"])
                self.assertTrue(service["cpus"])
                self.assertEqual(service["logging"]["driver"], "json-file")
                self.assertTrue(service["healthcheck"]["test"])

    def test_web_healthcheck_uses_the_container_hostname(self) -> None:
        healthcheck = self.services["web"]["healthcheck"]["test"]
        command = healthcheck[-1]
        self.assertIn("process.env.HOSTNAME", command)
        self.assertNotIn("127.0.0.1", command)


if __name__ == "__main__":
    unittest.main()
