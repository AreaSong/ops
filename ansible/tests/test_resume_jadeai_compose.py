from __future__ import annotations

import re
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
COMPOSE_PATH = REPO_ROOT / "services" / "resume-jadeai" / "compose.yml"
DIGEST_IMAGE = re.compile(r"^[^@]+@sha256:[0-9a-f]{64}$")


class ResumeJadeAIComposeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.app = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]["app"]

    def test_image_and_runtime_identity_are_fixed(self) -> None:
        self.assertRegex(self.app["image"], DIGEST_IMAGE)
        self.assertEqual(self.app["user"], "1000:1000")

    def test_runtime_has_a_read_only_root_and_no_capabilities(self) -> None:
        self.assertTrue(self.app["read_only"])
        self.assertEqual(self.app["cap_drop"], ["ALL"])
        self.assertIn("no-new-privileges:true", self.app["security_opt"])
        self.assertTrue(any(value.startswith("/tmp:rw,noexec,nosuid,nodev,") for value in self.app["tmpfs"]))

    def test_chromium_writable_state_is_confined_to_tmp(self) -> None:
        environment = "\n".join(self.app["environment"])
        for value in (
            "HOME=/tmp",
            "TMPDIR=/tmp",
            "XDG_CACHE_HOME=/tmp/.cache",
            "XDG_CONFIG_HOME=/tmp/.config",
        ):
            self.assertIn(value, environment)
        self.assertEqual(self.app["shm_size"], "128m")


if __name__ == "__main__":
    unittest.main()
