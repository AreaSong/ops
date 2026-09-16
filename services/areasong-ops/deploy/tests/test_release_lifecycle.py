from __future__ import annotations

import json
import sqlite3
from unittest.mock import patch

from release_test_support import MODULE, ReleaseFixture


class ReleaseLifecycleTests(ReleaseFixture):
    def stage_maintenance(self):
        with patch.object(MODULE, "run", side_effect=self.fake_run), self.orchestrator.guard.locked():
            self.state.data["status"] = "running"
            self.state.save()
            candidate = self.orchestrator.prepare()
            self.orchestrator.guard.claim()
            self.orchestrator.freeze_and_backup(candidate)
            self.orchestrator.install_and_verify(candidate)

    def test_success_and_completed_replay_do_not_repeat_mutations(self):
        self.deploy()
        self.assertEqual(self.state.data["status"], "succeeded")
        self.assertTrue(self.state.data["activationStarted"])
        self.assertEqual(self.schema(), 47)
        self.assertFalse(self.orchestrator.guard.active.exists())
        self.assertFalse(self.orchestrator.guard.marker.exists())
        previous = list(self.commands)
        self.deploy()
        self.assertEqual(self.commands, previous)

    def test_web_failure_restores_schema_before_starting_old_runner(self):
        self.fail_web_once = True
        old_start_schemas = []

        def inspect_start(command):
            if command[:2] == ["systemctl", "start"] and (self.args.runner_root / "runner/areasong-ops-runner").read_text() == "old-runner":
                old_start_schemas.append(self.schema())

        self.on_command = inspect_start
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertEqual(self.state.data["status"], "rolled_back")
        self.assertEqual(self.state.data["rollback"]["databaseSchema"], 45)
        self.assertEqual(self.schema(), 45)
        self.assertEqual(old_start_schemas, [45])
        self.assertEqual((self.args.runner_root / "areasong-ops-runner-updater").read_text(), "old-updater")
        self.assertEqual(self.args.unit_path.parent.stat().st_mode & 0o777, 0o755)
        with sqlite3.connect(self.state.directory / "before-rollback/ops.db") as database:
            self.assertEqual(database.execute("PRAGMA user_version").fetchone()[0], 47)

    def test_partial_migration_failure_restores_schema_45(self):
        first = True

        def partial_migration(command):
            nonlocal first
            if command[:2] == ["systemctl", "start"] and first:
                first = False
                with sqlite3.connect(self.args.db_path) as database:
                    database.execute("PRAGMA user_version=46")
                return self.result(code=1)

        self.on_command = partial_migration
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertEqual(self.state.data["status"], "rolled_back")
        self.assertEqual(self.schema(), 45)

    def test_post_activation_failure_preserves_new_state_and_stops_controls(self):
        self.fail_activation = True
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertEqual(self.state.data["status"], "needs_attention")
        self.assertFalse(self.runner_running)
        self.assertFalse(self.web_running)
        self.assertEqual(self.schema(), 47)
        with sqlite3.connect(self.args.db_path) as database:
            self.assertIn(("after-activation",), database.execute("SELECT value FROM app_state").fetchall())
        before = len(self.commands)
        with patch.object(MODULE, "run", side_effect=self.fake_run), self.assertRaises(MODULE.ReleaseError):
            self.orchestrator.rollback()
        self.assertEqual(len(self.commands), before)

    def test_tampered_backup_never_starts_old_runner(self):
        self.fail_web_once = True

        def tamper(command):
            if command[:2] == ["docker", "compose"] and "up" in command and self.fail_web_once:
                (self.state.directory / "backup/runner").write_text("tampered")

        self.on_command = tamper
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertEqual(self.state.data["status"], "needs_attention")
        self.assertEqual(self.schema(), 47)
        self.assertNotEqual((self.args.runner_root / "runner/areasong-ops-runner").read_text(), "old-runner")

    def test_reboot_breaks_rollback_proof(self):
        self.fail_web_once = True

        def reboot(command):
            if command[:2] == ["docker", "compose"] and "up" in command and self.fail_web_once:
                self.args.boot_id_path.write_text("87654321-1234-1234-1234-123456789012")

        self.on_command = reboot
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertEqual(self.state.data["status"], "needs_attention")
        self.assertEqual(self.schema(), 47)

    def test_activity_after_web_stop_blocks_snapshot(self):
        def enqueue(command):
            if command[:2] == ["docker", "compose"] and "stop" in command:
                with sqlite3.connect(self.args.db_path) as database:
                    database.execute("INSERT INTO tasks VALUES ('running')")

        self.on_command = enqueue
        with self.assertRaisesRegex(MODULE.ReleaseError, "未收口"):
            self.deploy()
        self.assertFalse(self.orchestrator.snapshot.manifest.exists())
        self.assertEqual((self.args.runner_root / "runner/areasong-ops-runner").read_text(), "old-runner")

    def test_queued_updater_is_rejected_before_stopping_services(self):
        self.jobs = [{"unit": "areasong-ops-runner-update@job.service"}]
        with self.assertRaisesRegex(MODULE.ReleaseError, "排队"):
            self.deploy()
        self.assertTrue(self.runner_running)
        self.assertTrue(self.web_running)
        self.assertEqual(self.schema(), 45)

    def test_unrelated_systemd_job_does_not_prevent_release(self):
        self.jobs = [{"unit": "areaforge-update-agent.service", "type": "start", "state": "running"}]
        self.deploy()
        self.assertEqual(self.state.data["status"], "succeeded")

    def test_malformed_systemd_output_stops_before_service_mutations(self):
        def malformed_jobs(command):
            if command[:2] == ["systemctl", "list-jobs"]:
                return self.result("JOB UNIT TYPE STATE\n")

        self.on_command = malformed_jobs
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertTrue(self.runner_running)
        self.assertTrue(self.web_running)
        self.assertEqual(self.schema(), 45)
        self.assertFalse(any(command[:2] == ["systemctl", "stop"] for command in self.commands))

    def test_unknown_stop_result_never_installs_candidate(self):
        self.fail_stop = True
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertEqual(self.state.data["status"], "needs_attention")
        self.assertEqual((self.args.runner_root / "runner/areasong-ops-runner").read_text(), "old-runner")
        stops = [command for command in self.commands if command[:2] == ["systemctl", "stop"]]
        self.assertEqual(len(stops), 1)

    def test_manual_rollback_reloads_activation_boundary_inside_lock(self):
        self.stage_maintenance()
        from release_guard import fcntl
        original = fcntl.flock

        def concurrent_activation(descriptor, operation):
            original(descriptor, operation)
            saved = json.loads(self.state.path.read_text())
            saved["activationStarted"] = True
            MODULE.atomic_json(self.state.path, saved)

        before = len(self.commands)
        with patch.object(fcntl, "flock", side_effect=concurrent_activation), patch.object(MODULE, "run", side_effect=self.fake_run):
            with self.assertRaisesRegex(MODULE.ReleaseError, "激活"):
                self.orchestrator.rollback()
        self.assertEqual(len(self.commands), before)
        self.assertEqual(self.schema(), 47)

    def test_manual_rollback_uses_same_global_lock(self):
        self.stage_maintenance()
        with self.orchestrator.guard.locked():
            with self.assertRaisesRegex(MODULE.ReleaseError, "全局锁"):
                self.orchestrator.rollback()

    def test_missing_fence_or_foreign_active_owner_prevents_restore(self):
        self.stage_maintenance()
        self.orchestrator.guard.marker.unlink()
        before = len(self.commands)
        with patch.object(MODULE, "run", side_effect=self.fake_run), self.assertRaises(MODULE.ReleaseError):
            self.orchestrator.rollback()
        self.assertEqual(len(self.commands), before)

    def test_normal_release_cannot_be_rolled_back_to_old_snapshot(self):
        self.deploy()
        before = self.args.db_path.read_bytes()
        with self.assertRaisesRegex(MODULE.ReleaseError, "激活"):
            self.orchestrator.rollback()
        self.assertEqual(self.args.db_path.read_bytes(), before)

    def test_rollback_activation_failure_stops_both_sides(self):
        self.fail_web_once = True

        def fail_old_smoke(command):
            if command == ["/bin/true", "runtime"] and self.state.data.get("rollbackActivationStarted"):
                return self.result(code=1)

        self.on_command = fail_old_smoke
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertEqual(self.state.data["status"], "needs_attention")
        self.assertFalse(self.runner_running)
        self.assertFalse(self.web_running)
        self.assertEqual(self.schema(), 45)

    def test_failed_web_stop_still_attempts_runner_stop_once(self):
        self.fail_activation = True

        def fail_web_stop(command):
            if self.state.data.get("activationStarted") and command[:2] == ["docker", "compose"] and "stop" in command:
                return self.result(code=1)

        self.on_command = fail_web_stop
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertFalse(self.runner_running)
        self.assertEqual(self.state.data["status"], "needs_attention")
        self.assertEqual(self.schema(), 47)

    def test_changed_runner_or_unit_prevents_database_restore(self):
        self.stage_maintenance()
        (self.args.runner_root / "runner/areasong-ops-runner").write_text("foreign-program")
        with patch.object(MODULE, "run", side_effect=self.fake_run), self.assertRaisesRegex(MODULE.ReleaseError, "漂移"):
            self.orchestrator.rollback()
        self.assertEqual(self.schema(), 47)
        self.assertFalse((self.state.directory / "before-rollback").exists())

    def test_unexpected_normal_mode_never_restores_snapshot(self):
        def normal_instead_of_fenced(command):
            if command[0] == "curl" and self.state.data.get("candidateMayHaveStarted"):
                return self.result(json.dumps({"ok": True, "revision": self.revision, "releaseMaintenance": False}))

        self.on_command = normal_instead_of_fenced
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertTrue(self.state.data["isolationUncertain"])
        self.assertEqual(self.state.data["status"], "needs_attention")
        self.assertEqual(self.schema(), 47)
        self.assertFalse(self.runner_running)

    def test_nonexistent_cgroup_is_not_proof_of_stopped_children(self):
        original = self.orchestrator.runtime.unit_state

        def missing_group(name):
            state = original(name)
            state["ControlGroup"] = "/system.slice/" + name
            return state

        with patch.object(self.orchestrator.runtime, "unit_state", side_effect=missing_group), self.assertRaisesRegex(MODULE.ReleaseError, "cgroup"):
            self.deploy()
        self.assertEqual((self.args.runner_root / "runner/areasong-ops-runner").read_text(), "old-runner")

    def test_foreign_active_owner_is_not_removed(self):
        foreign = {"schemaVersion": 1, "deploymentId": "another-operation", "revision": "f" * 40}
        MODULE.atomic_json(self.orchestrator.guard.active, foreign)
        with self.assertRaises(MODULE.ReleaseError):
            self.deploy()
        self.assertEqual(json.loads(self.orchestrator.guard.active.read_text()), foreign)
        self.assertTrue(self.web_running)
        self.assertTrue(self.runner_running)

    def test_isolation_loss_is_durable_before_stop_can_be_interrupted(self):
        def wrong_mode(command):
            if command[0] == "curl" and self.state.data.get("candidateMayHaveStarted"):
                return self.result(json.dumps({"ok": True, "revision": self.revision, "releaseMaintenance": False}))

        original_stop = self.orchestrator.runtime.stop_web

        def interrupt_cleanup():
            if self.state.data.get("isolationUncertain"):
                raise KeyboardInterrupt("simulated process termination")
            original_stop()

        self.on_command = wrong_mode
        with patch.object(self.orchestrator.runtime, "stop_web", side_effect=interrupt_cleanup), self.assertRaises(KeyboardInterrupt):
            self.deploy()
        reloaded = MODULE.State(self.args.state_dir, self.state.deployment_id, self.metadata, create=False)
        self.assertTrue(reloaded.data["isolationUncertain"])
        self.assertEqual(reloaded.data["status"], "needs_attention")
        recovered = MODULE.Orchestrator(self.args, reloaded, self.metadata)
        before = len(self.commands)
        with patch.object(MODULE, "run", side_effect=self.fake_run), self.assertRaisesRegex(MODULE.ReleaseError, "隔离"):
            recovered.rollback()
        self.assertEqual(len(self.commands), before)
        self.assertEqual(self.schema(), 47)

    def test_backup_parent_symlink_is_rejected_before_restore_commands(self):
        self.stage_maintenance()
        backup = self.state.directory / "backup"
        alternate = self.state.directory / "other-backup"
        backup.rename(alternate)
        backup.symlink_to(alternate, target_is_directory=True)
        before = len(self.commands)
        with patch.object(MODULE, "run", side_effect=self.fake_run), self.assertRaises(MODULE.ReleaseError):
            self.orchestrator.rollback()
        self.assertEqual(len(self.commands), before)
        self.assertEqual(self.schema(), 47)

    def test_completed_replay_finishes_private_claim_cleanup_without_restart(self):
        self.deploy()
        MODULE.atomic_json(self.orchestrator.guard.active, {
            "schemaVersion": 1, "deploymentId": self.state.deployment_id, "revision": self.revision,
        })
        before = list(self.commands)
        self.deploy()
        self.assertFalse(self.orchestrator.guard.active.exists())
        self.assertEqual(self.commands, before)
