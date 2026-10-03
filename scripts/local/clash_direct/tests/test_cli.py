import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from support import BASE_SCRIPT, TOOL_DIR, Tools, fixture


@unittest.skipUnless(Tools.discover().node and Tools.discover().core, "端到端校验需要本机 Node 和 Mihomo，不会安装")
class CliTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="clash-direct-cli-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.app = fixture(self.root)
        self.state = self.root / "state"

    def run_cli(self, *arguments, text=None):
        command = [str(TOOL_DIR / "clash-direct"), "--app-dir", str(self.app), "--state-dir", str(self.state), *arguments]
        env = dict(os.environ)
        env.pop("PYTHONPATH", None)
        env.pop("PYTHONHOME", None)
        return subprocess.run(command, cwd="/tmp", input=text, text=True, capture_output=True, timeout=30, check=False, env=env)

    def test_help_and_doctor_from_other_working_directory(self):
        help_result = self.run_cli("--help")
        self.assertEqual(help_result.returncode, 0)
        self.assertIn("--json", help_result.stdout)
        result = self.run_cli("--json", "doctor")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        data = json.loads(result.stdout)["data"]
        self.assertTrue(data["ready"])
        self.assertEqual(data["tools"]["auth"], "not_required")
        self.assertFalse(self.state.exists())
        self.assertNotIn("DO_NOT_PRINT_SECRET", result.stdout)

    def test_dry_run_and_confirmation_required(self):
        result = self.run_cli("add", "https://example.com/private?token=DO_NOT_PRINT_SECRET", "--json", "--dry-run")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertFalse(json.loads(result.stdout)["data"]["saved"])
        self.assertFalse(self.state.exists())
        self.assertNotIn("DO_NOT_PRINT_SECRET", result.stdout)
        result = self.run_cli("--json", "add", "example.com")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout)["error"]["code"], "confirmation_required")
        self.assertEqual((self.app / "profiles/owned.js").read_text(encoding="utf-8"), BASE_SCRIPT)

    def test_add_list_export_remove_undo(self):
        added = self.run_cli("add", "https://docs.example.com/path", "--yes", "--json")
        self.assertEqual(added.returncode, 0, added.stderr + added.stdout)
        self.assertTrue(json.loads(added.stdout)["data"]["saved"])
        listed = json.loads(self.run_cli("list", "--json").stdout)["data"]
        self.assertEqual(listed["rules"][0]["rule"], "DOMAIN,docs.example.com,DIRECT")
        self.assertEqual(self.run_cli("export").stdout.strip(), "DOMAIN,docs.example.com,DIRECT")
        removed = self.run_cli("remove", "docs.example.com", "--yes", "--json")
        self.assertEqual(removed.returncode, 0, removed.stderr + removed.stdout)
        self.assertEqual(json.loads(self.run_cli("list", "--json").stdout)["data"]["rules"], [])
        undone = self.run_cli("undo", "--yes", "--json")
        self.assertEqual(undone.returncode, 0, undone.stderr + undone.stdout)
        self.assertEqual(len(json.loads(self.run_cli("list", "--json").stdout)["data"]["rules"]), 1)

    def test_interactive_add_and_delete_by_number(self):
        result = self.run_cli("menu", text="1\nhttps://example.com/path\n\ny\n2\n3\n1\ny\n0\n")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertIn("已保存", result.stdout)
        self.assertEqual(json.loads(self.run_cli("list", "--json").stdout)["data"]["rules"], [])

    def test_invalid_input_json_never_echoes_credentials(self):
        result = self.run_cli("resolve", "https://user:DO_NOT_PRINT_SECRET@example.com", "--json")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(json.loads(result.stdout)["ok"])
        self.assertNotIn("DO_NOT_PRINT_SECRET", result.stdout + result.stderr)
        invalid = self.run_cli("--json", "not-a-command", "DO_NOT_PRINT_SECRET")
        self.assertFalse(json.loads(invalid.stdout)["ok"])
        self.assertNotIn("DO_NOT_PRINT_SECRET", invalid.stdout + invalid.stderr)

    def test_node_check_does_not_execute_existing_script(self):
        effect = self.root / "must-not-exist"
        source = f"function main(config, profileName) {{ require('fs').writeFileSync({json.dumps(str(effect))}, 'bad'); return config; }}\n"
        (self.app / "profiles/owned.js").write_text(source, encoding="utf-8")
        result = self.run_cli("doctor", "--json")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertFalse(effect.exists())

    def test_failed_syntax_check_does_not_leak_or_write(self):
        path = self.app / "profiles/owned.js"
        source = 'function main(config, profileName) { const password="DO_NOT_PRINT_SECRET"; return config;\n'
        path.write_text(source, encoding="utf-8")
        result = self.run_cli("add", "example.com", "--yes", "--json")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(json.loads(result.stdout)["ok"])
        self.assertNotIn("DO_NOT_PRINT_SECRET", result.stdout + result.stderr)
        self.assertEqual(path.read_text(encoding="utf-8"), source)
        self.assertFalse(self.state.exists())

    def test_missing_dependency_is_structured(self):
        result = self.run_cli("--node", str(self.root / "missing-node"), "doctor", "--json")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(json.loads(result.stdout)["data"]["ready"])

    def test_native_rule_checks_cover_suffix_and_ip_addresses(self):
        for value, scope in [("example.com", "suffix"), ("http://127.0.0.1:8080/a", "exact"), ("https://[2001:db8::1]/", "exact")]:
            with self.subTest(value=value):
                result = self.run_cli("add", value, "--scope", scope, "--dry-run", "--json")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue(json.loads(result.stdout)["data"]["preview"]["checks"]["mihomo_rule_syntax"])
        self.assertFalse(self.state.exists())


if __name__ == "__main__":
    unittest.main()
