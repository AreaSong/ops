from __future__ import annotations

import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYBOOK_PATH = REPO_ROOT / "ansible" / "nginx-cloudflare-origin.yml"
MAP_TEMPLATE = REPO_ROOT / "ansible" / "templates" / "cloudflare-origin-map.conf.j2"
ORIGIN_TEMPLATE = REPO_ROOT / "ansible" / "templates" / "cloudflare-origin-only.conf.j2"


class NginxCloudflareOriginTests(unittest.TestCase):
    def setUp(self) -> None:
        self.play = yaml.safe_load(PLAYBOOK_PATH.read_text(encoding="utf-8"))[0]
        self.tasks = self.play["tasks"]
        self.names = [task["name"] for task in self.tasks]
        deployment = next(task for task in self.tasks if task["name"] == "Apply Cloudflare origin policy with automatic file rollback")
        self.block_names = [task["name"] for task in deployment["block"]]
        self.rescue_names = [task["name"] for task in deployment["rescue"]]

    def test_preflight_runs_before_the_transactional_change(self) -> None:
        deployment_index = self.names.index("Apply Cloudflare origin policy with automatic file rollback")
        self.assertLess(self.names.index("Require both direct DNS-only site files"), deployment_index)
        self.assertLess(self.names.index("Require the standard HTTP-level conf.d include"), deployment_index)
        self.assertLess(self.names.index("Refuse to replace unmanaged enabled site files"), deployment_index)
        self.assertEqual(len(self.play["vars"]["cloudflare_sites"]), 4)
        self.assertEqual(len(self.play["vars"]["cloudflare_hostnames"]), 4)

    def test_change_is_backed_up_validated_and_probed_in_order(self) -> None:
        self.assertLess(
            self.block_names.index("Back up existing available Cloudflare-proxied virtual hosts"),
            self.block_names.index("Install controlled Cloudflare-proxied virtual hosts"),
        )
        self.assertIn("Install Cloudflare original peer allow map", self.block_names)
        self.assertLess(
            self.block_names.index("Validate Nginx configuration before reload"),
            self.block_names.index("Reload the validated Nginx configuration"),
        )
        self.assertLess(
            self.block_names.index("Reload the validated Nginx configuration"),
            self.block_names.index("Probe that direct origin access is rejected locally"),
        )
        self.assertIn("Probe the public routes through Cloudflare", self.block_names)

    def test_origin_policy_uses_the_original_tcp_peer(self) -> None:
        address_map = MAP_TEMPLATE.read_text(encoding="utf-8")
        origin_policy = ORIGIN_TEMPLATE.read_text(encoding="utf-8")
        self.assertIn("geo $realip_remote_addr $cloudflare_origin_allowed", address_map)
        self.assertIn("default 0;", address_map)
        self.assertIn("{{ cidr }} 1;", address_map)
        self.assertIn("if ($cloudflare_origin_allowed = 0)", origin_policy)
        self.assertNotIn("allow {{ cidr }}", origin_policy)

    def test_rescue_restores_files_and_requires_valid_rollback(self) -> None:
        for required in (
            "Restore pre-existing managed Nginx snippets",
            "Remove managed Nginx snippets created by the failed change",
            "Restore pre-existing available Cloudflare-proxied virtual hosts",
            "Remove available virtual hosts created by the failed change",
            "Remove enabled symlinks created by the failed change",
            "Validate restored Nginx configuration",
            "Reload restored Nginx configuration",
            "Require successful Nginx rollback",
            "Report the rolled-back Cloudflare origin deployment",
        ):
            self.assertIn(required, self.rescue_names)


if __name__ == "__main__":
    unittest.main()
