from __future__ import annotations

import re
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
COMPOSE_PATH = REPO_ROOT / "services" / "sub2api" / "compose.yml"
DIGEST_IMAGE = re.compile(r"^[^@]+@sha256:[0-9a-f]{64}$")
SUB2API_IMAGE = (
    "weishaw/sub2api@sha256:"
    "469790e0389bf31379978687149280a4e135393ad98a9a401951b6be9b1df444"
)


class Sub2APIComposeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]

    def test_images_are_immutable_and_release_is_pinned(self) -> None:
        for name, service in self.services.items():
            with self.subTest(service=name):
                self.assertRegex(service["image"], DIGEST_IMAGE)
        self.assertEqual(self.services["sub2api"]["image"], SUB2API_IMAGE)

    def test_application_runs_non_root_with_a_read_only_root(self) -> None:
        app = self.services["sub2api"]
        self.assertEqual(app["user"], "1000:1000")
        self.assertTrue(app["read_only"])
        self.assertEqual(app["cap_drop"], ["ALL"])
        self.assertIn("no-new-privileges:true", app["security_opt"])
        self.assertTrue(any(value.startswith("/tmp:rw,noexec,nosuid,nodev,") for value in app["tmpfs"]))

    def test_url_allowlist_fails_closed(self) -> None:
        environment = "\n".join(self.services["sub2api"]["environment"])
        self.assertIn("SECURITY_URL_ALLOWLIST_ENABLED=${SECURITY_URL_ALLOWLIST_ENABLED:-true}", environment)
        self.assertIn("SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=${SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP:-false}", environment)
        self.assertIn("SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=${SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS:-false}", environment)
        self.assertIn("${SECURITY_URL_ALLOWLIST_UPSTREAM_HOSTS:?", environment)
        self.assertIn("SECURITY_URL_ALLOWLIST_PRICING_HOSTS=", environment)
        self.assertIn("SECURITY_URL_ALLOWLIST_CRS_HOSTS=", environment)

    def test_redis_has_a_bounded_non_root_runtime(self) -> None:
        redis = self.services["redis"]
        self.assertEqual(redis["user"], "999:1000")
        self.assertTrue(redis["read_only"])
        self.assertEqual(redis["cap_drop"], ["ALL"])
        self.assertIn("--maxmemory 64mb", redis["command"])
        self.assertIn("--maxmemory-policy noeviction", redis["command"])
        self.assertIn("--aclfile /data/users.acl", redis["command"])
        self.assertTrue(any(value.startswith("/tmp:rw,noexec,nosuid,nodev,") for value in redis["tmpfs"]))


if __name__ == "__main__":
    unittest.main()
