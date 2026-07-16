from __future__ import annotations

import re
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
ROLE_ROOT = REPO_ROOT / "ansible" / "roles" / "security"


def walk_tasks(tasks: list[dict]):
    for task in tasks:
        yield task
        for section in ("block", "rescue", "always"):
            yield from walk_tasks(task.get(section, []))


class AuditdBaselineTests(unittest.TestCase):
    def setUp(self) -> None:
        self.defaults = yaml.safe_load(
            (ROLE_ROOT / "defaults" / "main.yml").read_text(encoding="utf-8")
        )

    def test_config_thresholds_are_ordered_and_renderable(self) -> None:
        template = (ROLE_ROOT / "templates" / "auditd.conf.j2").read_text(encoding="utf-8")
        rendered = re.sub(
            r"{{\s*([a-zA-Z0-9_]+)\s*}}",
            lambda match: str(self.defaults[match.group(1)]),
            template,
        )
        self.assertNotIn("{{", rendered)
        values = {
            key.strip(): value.strip()
            for line in rendered.splitlines()
            if line and not line.startswith("#")
            for key, value in [line.split("=", 1)]
        }
        space_left = int(values["space_left"])
        admin_space_left = int(values["admin_space_left"])
        self.assertGreater(space_left, admin_space_left)
        self.assertEqual(values["admin_space_left_action"], "SUSPEND")

    def test_rules_have_exact_required_keys_and_are_not_immutable(self) -> None:
        rules = (ROLE_ROOT / "files" / "ops-baseline.rules").read_text(encoding="utf-8")
        keys = set(re.findall(r"(?:-k\s+)([^\s]+)", rules))
        self.assertEqual(keys, set(self.defaults["auditd_required_keys"]))
        self.assertNotRegex(rules, r"(?m)^\s*-e\s+2\s*$")
        for line in rules.splitlines():
            self.assertRegex(line, r"^(-w /[^\s]+ -p [rwaxt]+ -k [^\s]+|-a always,exit .+ -k [^\s]+)$")

    def test_immutable_rule_scan_is_multiline(self) -> None:
        task_file = yaml.safe_load(
            (ROLE_ROOT / "tasks" / "auditd.yml").read_text(encoding="utf-8")
        )
        immutable_scan = next(
            task["ansible.builtin.find"]
            for task in task_file
            if task.get("name") == "Find immutable directives in persisted audit rules"
        )
        pattern = immutable_scan["contains"]
        self.assertTrue(pattern.startswith("(?m)"))
        self.assertRegex("# header\n-e 2\n", pattern)

    def test_deployment_has_snapshot_rollback_and_runtime_gates(self) -> None:
        task_file = yaml.safe_load(
            (ROLE_ROOT / "tasks" / "auditd.yml").read_text(encoding="utf-8")
        )
        tasks = list(walk_tasks(task_file))
        names = {task.get("name") for task in tasks}

        self.assertIn("Snapshot the complete audit configuration", names)
        self.assertIn("Restore auditd.conf from the rollback snapshot", names)
        self.assertIn("Reload restored audit rules", names)
        self.assertIn("Require successful auditd rollback", names)
        self.assertIn("Verify auditd service is active after deployment", names)
        self.assertIn("Verify auditd service is enabled after deployment", names)
        self.assertIn("Require every managed audit rule key", names)

    def test_compliance_audit_requires_all_managed_keys(self) -> None:
        audit_play = yaml.safe_load(
            (REPO_ROOT / "ansible" / "audit.yml").read_text(encoding="utf-8")
        )[0]
        self.assertIn("roles/security/defaults/main.yml", audit_play["vars_files"])
        names = {task.get("name") for task in walk_tasks(audit_play["tasks"])}
        self.assertIn("Require healthy auditd service and kernel state", names)
        self.assertIn("Require every baseline audit key", names)


if __name__ == "__main__":
    unittest.main()
