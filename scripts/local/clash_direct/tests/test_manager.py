import os
import stat
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from support import BASE_SCRIPT, FakeTools, edit_metadata, fixture
from clash_direct_lib.errors import ToolError
from clash_direct_lib.manager import Manager
from clash_direct_lib.menu import run_menu
from clash_direct_lib.profile import load_target
from clash_direct_lib.rules import Rule


class ManagerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="clash-direct-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.app = fixture(self.root)
        self.state = self.root / "private-state"
        self.path = self.app / "profiles/owned.js"
        self.manager = Manager(self.app, self.state, FakeTools())

    def test_dry_preparation_does_not_write(self):
        before = self.path.read_bytes()
        plan = self.manager.add(Rule("example.com"))
        self.assertTrue(plan.changed)
        self.assertEqual(self.path.read_bytes(), before)
        self.assertFalse(self.state.exists())

    def test_add_remove_undo_and_private_backups(self):
        metadata = (self.app / "profiles.yaml").read_bytes()
        other = (self.app / "profiles/other.js").read_bytes()
        saved = self.manager.commit(self.manager.add(Rule("example.com")))
        self.assertTrue(saved["saved"])
        self.assertEqual(Path(saved["backup"]).read_text(encoding="utf-8"), BASE_SCRIPT)
        self.assertEqual(stat.S_IMODE(Path(saved["backup"]).stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.state.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(self.path.stat().st_mode), 0o640)
        self.assertEqual(self.manager.snapshot().document.rules, (Rule("example.com"),))
        self.manager.commit(self.manager.add(Rule("second.example", "suffix")))
        self.manager.commit(self.manager.remove(Rule("example.com")))
        self.manager.commit(self.manager.undo())
        self.assertEqual(len(self.manager.snapshot().document.rules), 2)
        self.manager.commit(self.manager.undo())
        self.assertEqual(self.manager.snapshot().document.rules, (Rule("example.com"),))
        self.manager.commit(self.manager.undo())
        self.assertEqual(self.manager.snapshot().document.rules, ())
        with self.assertRaises(ToolError):
            self.manager.undo()
        self.assertEqual((self.app / "profiles.yaml").read_bytes(), metadata)
        self.assertEqual((self.app / "profiles/other.js").read_bytes(), other)

    def test_duplicate_and_missing_removal_do_not_save(self):
        plan = self.manager.remove(Rule("missing.example"))
        self.assertFalse(self.manager.commit(plan)["saved"])
        self.assertFalse(self.state.exists())
        self.manager.commit(self.manager.add(Rule("example.com")))
        before = self.path.read_bytes()
        head = self.manager.store.head("SUB")
        self.assertFalse(self.manager.commit(self.manager.add(Rule("example.com")))["saved"])
        self.assertEqual(self.path.read_bytes(), before)
        self.assertEqual(self.manager.store.head("SUB"), head)

    def test_external_edit_conflict_is_not_overwritten(self):
        plan = self.manager.add(Rule("example.com"))
        self.path.write_text(BASE_SCRIPT + "// 外部编辑\n", encoding="utf-8")
        with self.assertRaises(ToolError) as raised:
            self.manager.commit(plan)
        self.assertEqual(raised.exception.code, "conflict")
        self.assertTrue(self.path.read_text(encoding="utf-8").endswith("// 外部编辑\n"))

    def test_undo_preserves_unmanaged_base_edits(self):
        self.manager.commit(self.manager.add(Rule("example.com")))
        self.path.write_text(self.path.read_text(encoding="utf-8").replace("return 42", "return 43"), encoding="utf-8")
        self.manager.commit(self.manager.undo())
        self.assertIn("return 43", self.path.read_text(encoding="utf-8"))
        self.assertEqual(self.manager.snapshot().document.rules, ())

    def test_validation_failure_never_writes(self):
        with patch.object(self.manager.tools, "validate", side_effect=ToolError("check_failed", "模拟失败")):
            with self.assertRaises(ToolError):
                self.manager.add(Rule("example.com"))
        self.assertEqual(self.path.read_text(encoding="utf-8"), BASE_SCRIPT)
        self.assertFalse(self.state.exists())

    def test_changed_active_profile_blocks_commit(self):
        plan = self.manager.add(Rule("example.com"))
        edit_metadata(self.app, lambda data: data.update(current="OTHER"))
        with self.assertRaises(ToolError):
            self.manager.commit(plan)
        self.assertEqual(self.path.read_text(encoding="utf-8"), BASE_SCRIPT)

    def test_shared_global_and_ambiguous_profiles_rejected(self):
        changes = [
            lambda data: data["items"][2]["option"].update(script="OWN"),
            lambda data: data["items"][2].update(name="sub"),
            lambda data: data["items"][1].update(file="../../outside.js"),
            lambda data: data["items"][1].update(file="Script.js"),
            lambda data: data["items"][2].update(uid="SUB"),
            lambda data: data["items"][3].update(file="owned.js"),
            lambda data: data["items"][1].update(file="bad\x00.js"),
        ]
        original = (self.app / "profiles.yaml").read_bytes()
        for change in changes:
            (self.app / "profiles.yaml").write_bytes(original)
            edit_metadata(self.app, change)
            with self.subTest(change=change), self.assertRaises(ToolError):
                load_target(self.app)

    def test_symlink_and_hardlink_script_rejected(self):
        outside = self.root / "outside.js"
        self.path.rename(outside)
        self.path.symlink_to(outside)
        with self.assertRaises(ToolError):
            load_target(self.app)
        self.path.unlink()
        os.link(outside, self.path)
        with self.assertRaises(ToolError):
            load_target(self.app)

    def test_cancel_menu_does_not_write(self):
        # 不需 URL 解析即可通过菜单退出；拒绝保存使用已构造的计划测试。
        output = []
        result = run_menu(self.manager, read=lambda _: "0", write=output.append)
        self.assertEqual(result, 0)
        self.assertFalse(self.state.exists())
        from clash_direct_lib.menu import confirm_and_save
        confirm_and_save(self.manager, self.manager.add(Rule("example.com")), read=lambda _: "n", write=output.append)
        self.assertEqual(self.path.read_text(encoding="utf-8"), BASE_SCRIPT)
        self.assertFalse(self.state.exists())

    def test_menu_stops_on_missing_profile(self):
        (self.app / "profiles.yaml").unlink()
        result = run_menu(self.manager, read=lambda _: self.fail("不应反复提示"), write=lambda _: None)
        self.assertNotEqual(result, 0)

    def test_lock_conflict_does_not_write(self):
        plan = self.manager.add(Rule("example.com"))
        with self.manager.store.lock("SUB"):
            with self.assertRaises(ToolError) as raised:
                self.manager.commit(plan)
        self.assertEqual(raised.exception.code, "conflict")
        self.assertEqual(self.path.read_text(encoding="utf-8"), BASE_SCRIPT)

    def test_post_commit_error_reports_saved_not_false_cancellation(self):
        plan = self.manager.add(Rule("example.com"))
        with patch.object(self.manager.store, "finish", side_effect=OSError("模拟磁盘故障")):
            with self.assertRaises(ToolError) as raised:
                self.manager.commit(plan)
        self.assertEqual(raised.exception.code, "saved_but_unfinished")
        self.assertTrue(raised.exception.details["saved"])
        self.assertEqual(self.manager.snapshot().document.rules, (Rule("example.com"),))

    def test_state_directory_symlink_rejected(self):
        external = self.root / "external"
        external.mkdir(mode=0o700)
        self.state.symlink_to(external, target_is_directory=True)
        with self.assertRaises(ToolError):
            self.manager.add(Rule("example.com"))

    def test_other_script_symlink_to_target_is_rejected(self):
        other = self.app / "profiles/other.js"
        other.unlink()
        other.symlink_to(self.path)
        with self.assertRaises(ToolError) as raised:
            load_target(self.app)
        self.assertEqual(raised.exception.code, "shared_script")

    def test_failed_write_before_replacement_reports_unchanged(self):
        plan = self.manager.add(Rule("example.com"))
        with patch.object(self.manager, "write_source", side_effect=OSError("模拟失败")):
            with self.assertRaises(ToolError) as raised:
                self.manager.commit(plan)
        self.assertFalse(raised.exception.details["saved"])
        self.assertEqual(self.path.read_text(encoding="utf-8"), BASE_SCRIPT)

    def test_unknown_write_state_does_not_restore_over_external_content(self):
        plan = self.manager.add(Rule("example.com"))

        def changed_elsewhere(_):
            self.path.write_text("// 外部变更，不能自动覆盖\n", encoding="utf-8")
            raise OSError("模拟并发错误")

        with patch.object(self.manager, "write_source", side_effect=changed_elsewhere):
            with self.assertRaises(ToolError) as raised:
                self.manager.commit(plan)
        self.assertEqual(raised.exception.code, "write_state_unknown")
        self.assertIsNone(raised.exception.details["saved"])
        self.assertIn("外部变更", self.path.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
