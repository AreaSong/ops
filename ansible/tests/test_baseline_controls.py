from __future__ import annotations

import configparser
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
ANSIBLE_ROOT = REPO_ROOT / "ansible"


class BaselineControlTests(unittest.TestCase):
    def test_ssh_host_key_is_pinned_and_checking_is_enabled(self) -> None:
        config = configparser.ConfigParser()
        config.read(ANSIBLE_ROOT / "ansible.cfg", encoding="utf-8")
        self.assertEqual(config["defaults"]["host_key_checking"], "True")
        self.assertIn("StrictHostKeyChecking=yes", config["ssh_connection"]["ssh_args"])
        self.assertIn("UserKnownHostsFile=known_hosts", config["ssh_connection"]["ssh_args"])
        self.assertIn(
            "/var/lib/ops/ansible-collections",
            config["defaults"]["collections_path"],
        )
        self.assertNotIn("fact_caching", config["defaults"])

        entries = [
            line
            for line in (ANSIBLE_ROOT / "known_hosts").read_text(encoding="utf-8").splitlines()
            if line and not line.startswith("#")
        ]
        self.assertEqual(len(entries), 1)
        self.assertTrue(entries[0].startswith("23.185.200.12 ssh-ed25519 "))

    def test_baseline_does_not_install_a_competing_node_exporter(self) -> None:
        baseline = yaml.safe_load((ANSIBLE_ROOT / "baseline.yml").read_text(encoding="utf-8"))
        self.assertNotIn("node_exporter", baseline[0]["roles"])

        tasks = yaml.safe_load(
            (ANSIBLE_ROOT / "roles" / "node_exporter" / "tasks" / "main.yml").read_text(
                encoding="utf-8"
            )
        )
        download = next(task for task in tasks if task["name"] == "Download node_exporter")
        self.assertEqual(
            download["ansible.builtin.get_url"]["checksum"],
            "sha256:6809dd0b3ec45fd6e992c19071d6b5253aed3ead7bf0686885a51d85c6643c66",
        )

    def test_community_collection_is_versioned_and_checksum_verified(self) -> None:
        requirements = yaml.safe_load(
            (ANSIBLE_ROOT / "requirements.yml").read_text(encoding="utf-8")
        )
        self.assertEqual(
            requirements["collections"],
            [{"name": "community.general", "version": "10.7.9"}],
        )
        installer = (ANSIBLE_ROOT / "install-collections.sh").read_text(encoding="utf-8")
        self.assertIn('VERSION="10.7.9"', installer)
        self.assertRegex(installer, r'SHA256="[0-9a-f]{64}"')
        self.assertIn("sha256sum --check --status", installer)
        self.assertNotIn("curl |", installer)


if __name__ == "__main__":
    unittest.main()
