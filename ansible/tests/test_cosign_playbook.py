from __future__ import annotations

import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYBOOK_PATH = REPO_ROOT / "ansible" / "cosign.yml"


class CosignPlaybookTests(unittest.TestCase):
    def test_binary_source_is_versioned_and_checksum_pinned(self) -> None:
        play = yaml.safe_load(PLAYBOOK_PATH.read_text(encoding="utf-8"))[0]
        variables = play["vars"]
        self.assertRegex(variables["cosign_version"], r"^\d+\.\d+\.\d+$")
        self.assertRegex(variables["cosign_sha256"], r"^[0-9a-f]{64}$")
        serialized = yaml.safe_dump(play)
        self.assertIn("https://github.com/sigstore/cosign/releases/download/", serialized)
        self.assertIn("checksum", serialized)
        self.assertIn('ansible_architecture == "x86_64"', serialized)

    def test_installed_binary_is_root_owned_and_not_world_writable(self) -> None:
        play = yaml.safe_load(PLAYBOOK_PATH.read_text(encoding="utf-8"))[0]
        install_task = next(
            task for task in play["tasks"] if task["name"] == "Install the verified cosign binary"
        )
        copy = install_task["ansible.builtin.copy"]
        self.assertEqual(copy["dest"], "/usr/local/bin/cosign")
        self.assertEqual(copy["owner"], "root")
        self.assertEqual(copy["group"], "root")
        self.assertEqual(copy["mode"], "0755")

    def test_attestation_verifier_dependencies_are_installed(self) -> None:
        play = yaml.safe_load(PLAYBOOK_PATH.read_text(encoding="utf-8"))[0]
        package_task = next(
            task for task in play["tasks"] if task["name"] == "Install attestation verification dependencies"
        )
        self.assertIn("jq", package_task["ansible.builtin.package"]["name"])
        verify_task = next(task for task in play["tasks"] if task["name"] == "Verify the jq dependency")
        self.assertEqual(verify_task["ansible.builtin.command"], "jq --version")


if __name__ == "__main__":
    unittest.main()
