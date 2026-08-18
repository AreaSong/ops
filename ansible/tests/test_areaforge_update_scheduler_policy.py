from __future__ import annotations

import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYBOOK = REPO_ROOT / "ansible" / "areaforge-update-scheduler-policy.yml"


class AreaForgeUpdateSchedulerPolicyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.play = yaml.safe_load(PLAYBOOK.read_text(encoding="utf-8"))[0]
        self.tasks = self.play["tasks"]
        self.task_names = [task["name"] for task in self.tasks]

    def test_controlled_timer_is_proven_before_legacy_units_change(self) -> None:
        guard = self.task_names.index(
            "Require the controlled AreaForge update agent before retiring the legacy scheduler"
        )
        retirement = self.task_names.index(
            "Transactionally retire the legacy AreaForge updater scheduler"
        )
        self.assertLess(guard, retirement)

        assertion = self.tasks[guard]["ansible.builtin.assert"]["that"]
        self.assertTrue(any("LoadState=loaded" in expression for expression in assertion))
        self.assertTrue(any("ActiveState=active" in expression for expression in assertion))
        self.assertTrue(any("UnitFileState=enabled" in expression for expression in assertion))

    def test_legacy_units_are_stopped_disabled_masked_and_verified(self) -> None:
        retirement = next(
            task
            for task in self.tasks
            if task["name"] == "Transactionally retire the legacy AreaForge updater scheduler"
        )
        block = {task["name"]: task for task in retirement["block"]}
        stop = block["Stop legacy AreaForge updater units"][
            "ansible.builtin.systemd_service"
        ]
        self.assertEqual(stop["state"], "stopped")
        policy = block["Disable legacy AreaForge updater units"][
            "ansible.builtin.systemd_service"
        ]
        self.assertFalse(policy["enabled"])

        mask = block["Install persistent masks for legacy AreaForge updater units"][
            "ansible.builtin.file"
        ]
        self.assertEqual(mask["src"], "/dev/null")
        self.assertEqual(mask["state"], "link")
        self.assertTrue(mask["force"])

        reload_systemd = block[
            "Reload systemd after installing legacy updater masks"
        ]["ansible.builtin.systemd_service"]
        self.assertTrue(reload_systemd["daemon_reload"])

        assertion = block[
            "Require legacy AreaForge updater units to remain masked and inactive"
        ]["ansible.builtin.assert"]["that"]
        self.assertTrue(any("LoadState=masked" in expression for expression in assertion))
        self.assertTrue(any("ActiveState=inactive" in expression for expression in assertion))
        self.assertTrue(any("UnitFileState=masked" in expression for expression in assertion))

    def test_failure_path_restores_captured_state(self) -> None:
        retirement = next(
            task
            for task in self.tasks
            if task["name"] == "Transactionally retire the legacy AreaForge updater scheduler"
        )
        rescue = {task["name"]: task for task in retirement["rescue"]}
        remove_masks = rescue[
            "Remove masks created for previously unmasked legacy units"
        ]["ansible.builtin.file"]
        self.assertEqual(remove_masks["state"], "absent")

        restore_definitions = rescue[
            "Restore preserved legacy AreaForge unit definitions"
        ]["ansible.builtin.copy"]
        self.assertTrue(restore_definitions["remote_src"])
        self.assertTrue(restore_definitions["force"])

        restore = rescue["Restore legacy AreaForge updater units to their captured state"][
            "ansible.builtin.systemd_service"
        ]
        self.assertIn("LoadState=masked", restore["masked"])
        self.assertIn("UnitFileState=enabled", restore["enabled"])
        self.assertIn("ActiveState=active", restore["state"])
        self.assertIn("ansible.builtin.fail", rescue[
            "Stop after restoring the legacy AreaForge scheduler state"
        ])

    def test_unmasked_unit_definitions_are_backed_up_before_retirement(self) -> None:
        retirement = self.task_names.index(
            "Transactionally retire the legacy AreaForge updater scheduler"
        )
        backup = self.task_names.index(
            "Preserve unmasked legacy AreaForge unit definitions"
        )
        self.assertLess(backup, retirement)

        recovery_dir_task = next(
            task
            for task in self.tasks
            if task["name"] == "Create the root-only legacy unit recovery directory"
        )
        recovery_dir = recovery_dir_task["ansible.builtin.file"]
        self.assertEqual(recovery_dir["mode"], "0700")

        backup_task = self.tasks[backup]
        backup_module = backup_task["ansible.builtin.copy"]
        self.assertTrue(backup_module["remote_src"])
        self.assertEqual(backup_module["mode"], "0600")
        self.assertIn("not ansible_check_mode", backup_task["when"])
        self.assertIn("item.stat.isreg", backup_task["when"])


if __name__ == "__main__":
    unittest.main()
