from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path

import yaml


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "generate-ansible-inventory.py"
SPEC = importlib.util.spec_from_file_location("generate_ansible_inventory", SCRIPT_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"cannot load inventory generator: {SCRIPT_PATH}")
GENERATOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GENERATOR)


class GenerateAnsibleInventoryTests(unittest.TestCase):
    def sample(self) -> dict:
        return {
            "servers": [
                {
                    "hostname": "LosAngeles",
                    "public_ip": "23.185.200.12",
                    "private_ip": "",
                    "ansible_user": "as",
                    "ansible_ssh_private_key_file": "~/.ssh/id_ed25519_losangeles",
                    "os": "ubuntu-24.04",
                    "cloud": "provider",
                    "region": "us-west",
                }
            ],
            "groups": {"prod": ["LosAngeles"]},
        }

    def test_public_only_host_keeps_connection_identity(self) -> None:
        host = GENERATOR.build_inventory(self.sample())["all"]["hosts"]["LosAngeles"]
        self.assertEqual(host["ansible_host"], "23.185.200.12")
        self.assertEqual(host["ansible_user"], "as")
        self.assertEqual(
            host["ansible_ssh_private_key_file"],
            "~/.ssh/id_ed25519_losangeles",
        )

    def test_rendered_inventory_round_trips_as_yaml(self) -> None:
        rendered = GENERATOR.render_inventory(self.sample())
        parsed = yaml.safe_load(rendered)
        self.assertEqual(parsed["all"]["children"]["prod"]["hosts"]["LosAngeles"], {})


if __name__ == "__main__":
    unittest.main()
