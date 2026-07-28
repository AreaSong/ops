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

    def test_redis_host_memory_overcommit_is_persisted(self) -> None:
        tasks = yaml.safe_load(
            (ANSIBLE_ROOT / "roles" / "common" / "tasks" / "main.yml").read_text(
                encoding="utf-8"
            )
        )
        sysctl = next(task for task in tasks if task["name"] == "Configure sysctl baseline")
        self.assertIn("ansible.builtin.lineinfile", sysctl)
        self.assertNotIn("ansible.builtin.copy", sysctl)
        defaults = yaml.safe_load(
            (ANSIBLE_ROOT / "roles" / "common" / "defaults" / "main.yml").read_text(
                encoding="utf-8"
            )
        )
        self.assertIn(
            {"name": "vm.overcommit_memory", "value": 1},
            defaults["ops_sysctl_baseline"],
        )

    def test_losangeles_sysctl_overrides_preserve_runtime_network_controls(self) -> None:
        host_vars = yaml.safe_load(
            (ANSIBLE_ROOT / "host_vars" / "LosAngeles.yml").read_text(encoding="utf-8")
        )
        self.assertEqual(host_vars["ops_sysctl_rp_filter"], 2)
        extras = {item["name"]: item["value"] for item in host_vars["ops_sysctl_extra"]}
        self.assertEqual(extras["net.ipv4.ip_forward"], 1)
        self.assertEqual(extras["net.ipv4.tcp_congestion_control"], "bbr")
        self.assertEqual(extras["net.core.default_qdisc"], "fq")

    def test_time_sync_preserves_existing_service_and_is_check_mode_safe(self) -> None:
        tasks = yaml.safe_load(
            (ANSIBLE_ROOT / "roles" / "common" / "tasks" / "main.yml").read_text(
                encoding="utf-8"
            )
        )
        names = {task["name"] for task in tasks}
        self.assertIn("Gather time synchronization service facts", names)
        self.assertIn("Select the existing time synchronization service", names)
        install = next(
            task
            for task in tasks
            if task["name"] == "Install chrony when no time synchronization service exists"
        )
        self.assertEqual(install["when"], "not ops_time_sync_service_present")
        service = next(
            task
            for task in tasks
            if task["name"] == "Ensure the selected time synchronization service is running"
        )
        self.assertIn("not ansible_check_mode", service["when"])

    def test_ssh_handler_uses_platform_service_name(self) -> None:
        defaults = yaml.safe_load(
            (ANSIBLE_ROOT / "roles" / "security" / "defaults" / "main.yml").read_text(
                encoding="utf-8"
            )
        )
        self.assertEqual(
            defaults["security_ssh_service_name"],
            "{{ 'ssh' if ansible_os_family == 'Debian' else 'sshd' }}",
        )

        handlers = yaml.safe_load(
            (ANSIBLE_ROOT / "roles" / "security" / "handlers" / "main.yml").read_text(
                encoding="utf-8"
            )
        )
        reload_sshd = next(handler for handler in handlers if handler["name"] == "reload sshd")
        self.assertEqual(
            reload_sshd["ansible.builtin.systemd"]["name"],
            "{{ security_ssh_service_name }}",
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
