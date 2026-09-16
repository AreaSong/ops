from __future__ import annotations

import copy
import datetime as dt
import json
import os
import sqlite3
import sys
import tempfile
import unittest
import uuid
from unittest.mock import patch
from pathlib import Path
from contextlib import contextmanager
from types import SimpleNamespace

DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parents[2]
sys.path.insert(0, str(DEPLOY))
sys.path.insert(0, str(REPO / "scripts/backup/tests"))
from areasong_ops_fixtures import create_database
import observation_maintenance as module
from observation_contract import AUDIT_EVENT, digest
from observation_runtime import MaintenanceRuntime, private_file, read_json


class FakeRuntime(MaintenanceRuntime):
    def __init__(self, root):
        super().__init__(SimpleNamespace(db_path=root / "ops.db", config_dir=root,
                                         state_dir=root / "release"), uid=os.getuid())
        self.version = "0.2.1"
        self.boot = "one-boot"
        self.is_stopped = True
        self.stop_checks = 0
        self.fail_check = 0

    def base(self):
        self.no_release()
        catalog = read_json(self.args.config_dir / "services.json", self.uid)
        return catalog, {"host": "LosAngeles", "bootId": self.boot,
                         "database": private_file(self.args.db_path, self.uid), "catalogHash": digest(catalog)}

    def observe(self, service, silence_id):
        return {"containerId": "container", "imageId": "sha256:image", "version": self.version,
                "image": "image@sha256:current", "silence": "expired"}

    def stopped(self):
        self.no_release()
        self.stop_checks += 1
        if not self.is_stopped or self.stop_checks == self.fail_check:
            raise ValueError("控制面尚未停稳")


class ObservationMaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=os.environ.get("OPS_MAINTENANCE_TEST_ROOT", str(Path.home().resolve())))
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        (self.root / "release").mkdir(mode=0o700)
        (self.root / "release/.lock").touch(mode=0o600)
        self.runtime = FakeRuntime(self.root)
        self.actor = "a" * 64
        self.ids = sorted([str(uuid.uuid4()), str(uuid.uuid4())])
        self.policy = {
            "enforced": True, "defaultTenant": "production",
            "principals": {self.actor: {"tenantId": "production", "roles": [], "status": "active"}},
            "roles": {"operator": {"permissions": ["ops.deploy"]}},
            "bindings": [{"id": "binding", "subject": self.actor, "tenantId": "production",
                          "roleId": "operator", "objectIds": ["service:sub2api"]}],
        }
        catalog = {"schemaVersion": 4, "services": {"sub2api": {
            "objectId": "service:sub2api", "tenantId": "production", "serverId": "losangeles",
        }}}
        path = self.root / "services.json"
        path.write_text(json.dumps(catalog))
        path.chmod(0o600)
        self.populate(45)

    def populate(self, version):
        create_database(self.runtime.args.db_path, version)
        self.runtime.args.db_path.chmod(0o600)
        old = (module.now() - dt.timedelta(days=2)).isoformat()
        with self.database() as connection:
            for index, identifier in enumerate(self.ids):
                target, task = f"v0.1.{178 + index * 5}", str(uuid.uuid4())
                values = (identifier, self.actor, "sub2api", "update", target, "high", "observing",
                          "sha256:" + str(index) * 64, "{}", "confirmation", task, old, old, old,
                          "production", "losangeles")
                connection.execute("INSERT INTO release_plans(id,actor_hash,service,action,target,risk,state,digest,"
                                   "approval_summary_json,confirmation_hash,task_id,created_at,updated_at,"
                                   "observation_ends_at,tenant_id,server_id) VALUES(" + ",".join("?" for _ in values) + ")", values)
                values = (task, task, "request", self.actor, "sub2api", "update", target, "high", "succeeded",
                          "preview", "{}", old, old, identifier, "sha256:" + str(index) * 64)
                connection.execute("INSERT INTO tasks(id,idempotency_key,request_hash,actor_hash,service,action,target,"
                                   "risk,state,preview_id,snapshot_json,created_at,finished_at,plan_id,plan_digest) VALUES("
                                   + ",".join("?" for _ in values) + ")", values)
        self.save_policy()

    def save_policy(self):
        with self.database() as connection:
            connection.execute("DELETE FROM access_policy_snapshots")
            connection.execute("INSERT INTO access_policy_snapshots(version,digest,policy_json,actor_hash,created_at) VALUES(1,?,?,?,?)",
                               (digest(self.policy), json.dumps(self.policy), self.actor, module.now().isoformat()))

    def request(self):
        return module.preview(self.runtime, self.actor, self.ids, "OPS-LOCAL-TEST")

    @staticmethod
    def approval(request):
        return {"purpose": module.PURPOSE + ".approval", "approvedAt": module.now().isoformat(),
                **{key: request[key] for key in ("requestDigest", "operationId", "actorHash", "approvalRef", "expiresAt")}}

    @contextmanager
    def database(self, path=None):
        connection = sqlite3.connect(path or self.runtime.args.db_path)
        try:
            with connection:
                yield connection
        finally:
            connection.close()

    def rows(self, table):
        # 断言本身不能给已停稳的 WAL 库创建 sidecar，污染下一步预览条件。
        self.runtime.self_contained()
        connection = sqlite3.connect(self.runtime.args.db_path.as_uri() + "?mode=ro&immutable=1", uri=True)
        try:
            connection.row_factory = sqlite3.Row
            return [dict(row) for row in connection.execute("SELECT * FROM " + table)]
        finally:
            connection.close()

    def tree(self):
        return {str(p.relative_to(self.root)): (p.stat().st_mode, p.read_bytes())
                for p in self.root.rglob("*") if p.is_file()}

    def test_preview_is_read_only(self):
        before = self.tree()
        request = self.request()
        self.assertEqual(module.validate_request(request), self.ids)
        self.assertEqual(self.tree(), before)
        self.assertEqual((self.root / "release/.lock").read_bytes(), b"")

    def test_apply_real_schema45_preserves_history_and_is_idempotent(self):
        self.check_apply()

    def test_apply_real_schema47_without_migration(self):
        self.populate(47)
        self.check_apply()

    def check_apply(self):
        before, tasks = self.rows("release_plans"), self.rows("tasks")
        request = self.request()
        result = module.apply(self.runtime, request, self.approval(request))
        self.assertEqual(result["status"], "escalated")
        self.assertEqual(self.rows("tasks"), tasks)
        for old, current in zip(before, self.rows("release_plans")):
            changed = {k for k in current if current[k] != old[k]}
            self.assertEqual(changed, {"state", "closure_reason", "updated_at"})
            self.assertEqual(current["state"], "needs_attention")
            self.assertIsNone(current["closed_at"])
        audit = [row for row in self.rows("audit_entries") if row["event"] == AUDIT_EVENT]
        self.assertEqual(len(audit), 2)
        self.assertEqual({row["resource"] for row in audit}, set(self.ids))
        backup = Path(result["backup"]["path"])
        self.assertTrue(backup.is_file())
        with self.database(backup) as connection:
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM release_plans WHERE state='observing'").fetchone()[0], 2)
        original = self.tree()
        self.assertEqual(module.apply(self.runtime, request, {})["status"], "already_escalated")
        self.assertEqual(self.tree(), original)

    def test_rejects_unknown_schema_without_writes(self):
        with self.database() as connection:
            connection.execute("PRAGMA user_version=999")
        before = self.tree()
        with self.assertRaisesRegex(ValueError, "schema"):
            self.request()
        self.assertEqual(self.tree(), before)

    def test_missing_database_is_not_created(self):
        self.runtime.args.db_path.unlink()
        with self.assertRaises(FileNotFoundError):
            self.request()
        self.assertFalse(self.runtime.args.db_path.exists())

    def test_permission_rejections_are_read_only(self):
        original = copy.deepcopy(self.policy)
        changes = [lambda p: p.update(enforced=False),
                   lambda p: p["principals"][self.actor].update(status="disabled"),
                   lambda p: p["principals"][self.actor].update(tenantId="other"),
                   lambda p: p["principals"][self.actor].update(jit=True),
                   lambda p: p["principals"][self.actor].update(expiresAt="2020-01-01T00:00:00Z"),
                   lambda p: p["roles"]["operator"].update(permissions=["ops.read"]),
                   lambda p: p["bindings"][0].update(objectIds=["service:other"]),
                   lambda p: p.update(tenants={"production": {"status": "disabled"}})]
        for change in changes:
            self.policy = copy.deepcopy(original)
            change(self.policy)
            self.save_policy()
            before = self.tree()
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.request()
            self.assertEqual(self.tree(), before)

    def test_preview_after_revoke_cannot_be_applied(self):
        request = self.request()
        self.policy["roles"]["operator"]["permissions"] = []
        self.save_policy()
        with self.assertRaisesRegex(ValueError, "权限"):
            module.apply(self.runtime, request, self.approval(request))
        self.assertEqual({r["state"] for r in self.rows("release_plans")}, {"observing"})

    def test_normal_close_identity_must_not_use_exception(self):
        self.runtime.version = "0.1.178"
        with self.assertRaisesRegex(ValueError, "正常收口"):
            self.request()

    def test_bound_approval_and_expiry(self):
        request = self.request()
        approval = self.approval(request)
        approval["requestDigest"] = "sha256:" + "0" * 64
        with self.assertRaisesRegex(ValueError, "审批"):
            module.apply(self.runtime, request, approval)
        request["targetState"] = "completed"
        with self.assertRaises(ValueError):
            module.apply(self.runtime, request, approval)

    def test_runtime_drift_rejects_without_row_changes(self):
        request = self.request()
        self.runtime.version = "0.2.2"
        with self.assertRaisesRegex(ValueError, "变化"):
            module.apply(self.runtime, request, self.approval(request))
        self.assertEqual({r["state"] for r in self.rows("release_plans")}, {"observing"})

    def test_apply_requires_stopped_components(self):
        request = self.request()
        self.runtime.is_stopped = False
        with self.assertRaisesRegex(ValueError, "停稳"):
            module.apply(self.runtime, request, self.approval(request))
        self.assertFalse(list((self.root / "release").glob("observation-*")))

    def test_second_update_failure_rolls_back_all_rows_and_audit(self):
        request = self.request()
        with self.database() as connection:
            connection.execute("CREATE TRIGGER reject_plan BEFORE UPDATE ON release_plans WHEN NEW.id='" + self.ids[1] +
                               "' BEGIN SELECT RAISE(ABORT,'injected update failure'); END")
        before = self.rows("release_plans")
        with self.assertRaises(sqlite3.Error):
            module.apply(self.runtime, request, self.approval(request))
        self.assertEqual(self.rows("release_plans"), before)
        self.assertEqual(self.rows("audit_entries"), [])

    def test_audit_failure_rolls_back_plan_updates(self):
        request = self.request()
        with self.database() as connection:
            connection.execute("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_entries BEGIN SELECT RAISE(ABORT,'audit failed'); END")
        before = self.rows("release_plans")
        with self.assertRaises(sqlite3.Error):
            module.apply(self.runtime, request, self.approval(request))
        self.assertEqual(self.rows("release_plans"), before)

    def test_restart_before_commit_rolls_back_but_preserves_backup(self):
        request = self.request()
        before = self.rows("release_plans")
        original = module.update_rows
        def update(*args):
            original(*args)
            self.runtime.is_stopped = False
        with patch.object(module, "update_rows", side_effect=update):
            with self.assertRaisesRegex(ValueError, "停稳"):
                module.apply(self.runtime, request, self.approval(request))
        self.assertEqual(self.rows("release_plans"), before)
        self.assertEqual(len(list((self.root / "release").glob("observation-*/ops.db"))), 1)

    def test_release_marker_blocks_preview(self):
        (self.root / "release/maintenance.json").write_text("{}")
        before = self.tree()
        with self.assertRaisesRegex(ValueError, "发布"):
            self.request()
        self.assertEqual(self.tree(), before)

    def test_dynamic_roles_are_bound_and_rechecked(self):
        self.policy["bindings"] = []
        self.save_policy()
        with self.database() as connection:
            connection.execute("INSERT INTO roles(id,display_name,permissions_json) VALUES('dynamic','Dynamic','[\"ops.deploy\"]')")
            connection.execute("INSERT INTO role_bindings(id,subject,tenant_id,role_id,object_ids_json,created_at,created_by) "
                               "VALUES('dynamic',?,'production','dynamic','[\"service:sub2api\"]',?,'test')",
                               (self.actor, module.now().isoformat()))
        request = self.request()
        with self.database() as connection:
            connection.execute("UPDATE roles SET permissions_json='[\"ops.deploy\",\"ops.read\"]' WHERE id='dynamic'")
        with self.assertRaisesRegex(ValueError, "变化"):
            module.apply(self.runtime, request, self.approval(request))

    def test_valid_jit_then_expiry_during_backup_is_rejected(self):
        self.policy["principals"][self.actor]["jit"] = True
        self.policy["bindings"][0].update(jit=True, expiresAt=(module.now() + dt.timedelta(minutes=2)).isoformat())
        self.save_policy()
        request = self.request()
        approval = self.approval(request)
        future = module.now() + dt.timedelta(minutes=3)
        original = self.runtime.backup
        def backup(operation):
            result = original(operation)
            clock = patch.object(module, "now", return_value=future)
            clock.start()
            self.addCleanup(clock.stop)
            return result
        with patch.object(self.runtime, "backup", side_effect=backup):
            with self.assertRaisesRegex(ValueError, "JIT"):
                module.apply(self.runtime, request, approval)
        self.assertEqual({r["state"] for r in self.rows("release_plans")}, {"observing"})

    def test_runtime_change_during_backup_is_rejected(self):
        request = self.request()
        original = self.runtime.backup
        def backup(operation):
            result = original(operation)
            self.runtime.version = "0.2.2"
            return result
        with patch.object(self.runtime, "backup", side_effect=backup):
            with self.assertRaisesRegex(ValueError, "变化"):
                module.apply(self.runtime, request, self.approval(request))
        self.assertEqual(self.rows("audit_entries"), [])

    def test_wal_preview_is_rejected_without_file_writes(self):
        writer = sqlite3.connect(self.runtime.args.db_path)
        self.addCleanup(writer.close)
        writer.execute("PRAGMA journal_mode=WAL").fetchall()
        writer.execute("UPDATE release_plans SET closure_reason='new-WAL-row'")
        writer.commit()
        for suffix in ("-wal", "-shm"):
            Path(str(self.runtime.args.db_path) + suffix).chmod(0o600)
        before = self.tree()
        with self.assertRaisesRegex(ValueError, "WAL"):
            self.request()
        self.assertEqual(self.tree(), before)

    def test_missing_wal_peer_is_rejected_without_creation(self):
        Path(str(self.runtime.args.db_path) + "-wal").touch(mode=0o600)
        before = self.tree()
        with self.assertRaisesRegex(ValueError, "WAL"):
            self.request()
        self.assertEqual(self.tree(), before)

    def test_plan_task_binding_drift_blocks_apply(self):
        request = self.request()
        with self.database() as connection:
            connection.execute("UPDATE tasks SET plan_digest='wrong'")
        with self.assertRaisesRegex(ValueError, "绑定"):
            module.apply(self.runtime, request, self.approval(request))

    def test_unrelated_active_task_blocks_preview(self):
        with self.database() as connection:
            connection.execute("UPDATE tasks SET state='queued' WHERE plan_id=?", (self.ids[0],))
        before = self.tree()
        with self.assertRaises(ValueError):
            self.request()
        self.assertEqual(self.tree(), before)

    def test_preview_expiry_and_approval_permissions(self):
        request = self.request()
        future = module.now() + dt.timedelta(minutes=16)
        with patch.object(module, "now", return_value=future), self.assertRaisesRegex(ValueError, "过期"):
            module.apply(self.runtime, request, self.approval(request))
        path = self.root / "approval.json"
        path.write_text(json.dumps(self.approval(request)))
        path.chmod(0o644)
        with self.assertRaisesRegex(ValueError, "权限"):
            read_json(path, os.getuid())

    def test_approval_expiring_during_final_observation_rolls_back(self):
        request = self.request()
        approval = self.approval(request)
        original = self.runtime.observe
        calls = 0
        future = module.now() + dt.timedelta(minutes=16)
        def observe(*args):
            nonlocal calls
            calls += 1
            if calls == 5:  # 两次 collect 后，提交前的第一次服务复验。
                clock = patch.object(module, "now", return_value=future)
                clock.start()
                self.addCleanup(clock.stop)
            return original(*args)
        with patch.object(self.runtime, "observe", side_effect=observe):
            with self.assertRaisesRegex(ValueError, "过期"):
                module.apply(self.runtime, request, approval)
        self.assertEqual({r["state"] for r in self.rows("release_plans")}, {"observing"})
        self.assertEqual(self.rows("audit_entries"), [])

    def test_version_normalization_and_invalid_versions(self):
        for value in ("v0.1.178", "latest"):
            self.runtime.version = value
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.request()

    def test_parent_symlink_and_replaced_approval_are_rejected(self):
        target = self.root / "private"
        target.mkdir(mode=0o700)
        path = target / "approval.json"
        path.write_text("{}")
        path.chmod(0o600)
        alias = self.root / "alias"
        alias.symlink_to(target, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "信任链"):
            read_json(alias / path.name, os.getuid())
        alternate = self.root / "alternate.json"
        alternate.write_text('{"wrong":true}')
        alternate.chmod(0o600)
        original = os.open
        def replaced(name, flags):
            return original(alternate, flags)
        with patch("observation_runtime.os.open", side_effect=replaced):
            with self.assertRaisesRegex(ValueError, "替换"):
                read_json(path, os.getuid())

    def test_command_execution_is_local_and_read_only(self):
        with patch.dict(os.environ, {"DOCKER_HOST": "tcp://remote:2375", "DOCKER_CONTEXT": "remote"}):
            with patch("observation_runtime.subprocess.run") as run:
                MaintenanceRuntime.execute(["docker", "inspect", "example"])
                args, options = run.call_args
                self.assertEqual(args[0][:3], ["/usr/bin/docker", "--host", "unix:///var/run/docker.sock"])
                self.assertNotIn("DOCKER_HOST", options["env"])
                self.assertNotIn("DOCKER_CONTEXT", options["env"])
                MaintenanceRuntime.execute(["systemctl", "show", "unit"])
                self.assertEqual(run.call_args.kwargs["env"]["DBUS_SYSTEM_BUS_ADDRESS"], "unix:path=/run/dbus/system_bus_socket")
            with self.assertRaisesRegex(ValueError, "写动作"):
                MaintenanceRuntime.execute(["docker", "restart", "example"])

    def test_preview_never_creates_missing_lock(self):
        (self.root / "release/.lock").unlink()
        before = self.tree()
        with self.assertRaisesRegex(ValueError, "锁尚未准备"):
            self.request()
        self.assertEqual(self.tree(), before)

    def test_preview_cannot_overlap_apply_lock(self):
        before = self.tree()
        with self.runtime.locked():
            with self.assertRaises(BlockingIOError):
                self.request()
        self.assertEqual(self.tree(), before)

    def test_wal_appearing_during_stop_check_is_not_ignored(self):
        original = self.runtime.stopped
        def stopped():
            original()
            for suffix in ("-wal", "-shm"):
                Path(str(self.runtime.args.db_path) + suffix).touch(mode=0o600)
        with patch.object(self.runtime, "stopped", side_effect=stopped):
            with patch("observation_runtime.sqlite3.connect") as connect:
                with self.assertRaisesRegex(ValueError, "WAL"):
                    self.request()
                connect.assert_not_called()

    @unittest.skipUnless(sys.platform == "linux" and sys.version_info >= (3, 12), "真实 WAL 关闭语义必须在生产对应的 Linux/Python 3.12+ 验证")
    def test_clean_closed_wal_database_can_be_maintained(self):
        connection = sqlite3.connect(self.runtime.args.db_path)
        connection.execute("PRAGMA journal_mode=WAL").fetchall()
        connection.close()
        self.assertFalse(Path(str(self.runtime.args.db_path) + "-wal").exists())
        self.check_apply()

    def test_completed_operation_cannot_be_replayed_with_changed_request(self):
        request = self.request()
        module.apply(self.runtime, request, self.approval(request))
        before = self.tree()
        request["approvalRef"] = "OPS-DIFFERENT"
        request["requestDigest"] = digest({k: v for k, v in request.items() if k != "requestDigest"})
        with self.assertRaisesRegex(ValueError, "其他请求"):
            module.apply(self.runtime, request, self.approval(request))
        self.assertEqual(self.tree(), before)


if __name__ == "__main__":
    unittest.main()
