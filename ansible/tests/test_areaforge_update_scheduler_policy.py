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
        policy = block["Disable and mask legacy AreaForge updater units"][
            "ansible.builtin.systemd_service"
        ]
        self.assertFalse(policy["enabled"])
        self.assertTrue(policy["masked"])
        self.assertTrue(policy["daemon_reload"])

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
        restore = rescue["Restore legacy AreaForge updater units to their captured state"][
            "ansible.builtin.systemd_service"
        ]
        self.assertIn("LoadState=masked", restore["masked"])
        self.assertIn("UnitFileState=enabled", restore["enabled"])
        self.assertIn("ActiveState=active", restore["state"])
        self.assertIn("ansible.builtin.fail", rescue[
            "Stop after restoring the legacy AreaForge scheduler state"
        ])


if __name__ == "__main__":
    unittest.main()
