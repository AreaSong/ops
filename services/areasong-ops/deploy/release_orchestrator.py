#!/usr/bin/env python3
"""控制面唯一发布入口：隔离验收前允许完整回退，激活后禁止快照覆盖。"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from typing import Iterable

from release_common import (
    SAFE_ID, IsolationError, ReleaseError, State, atomic_json, copy_atomic, deployment_id, ensure_dir, fail,
    parse_manifest, regular_file, run, safe_extract_runner, sanitized_container_inspect, sha256_file,
    snapshot_sqlite, update_runtime_env, verify_assets,
)
from release_guard import Guard
from release_runtime import Runtime, RUNNER_UNIT
from release_snapshot import Snapshot, inspect_database, file_evidence


class Orchestrator:
    def __init__(self, args, state: State, metadata: dict):
        self.args, self.state, self.metadata = args, state, metadata
        self.guard = Guard(args, state)
        self.runtime = Runtime(args, lambda command, **options: run(command, **options))
        self.snapshot = Snapshot(args, state.directory)

    def preflight_run(self, mode: str) -> None:
        environment = os.environ.copy()
        environment.update({
            "OPS_PREFLIGHT_REPO_ROOT": str(self.args.repo_root),
            "OPS_PREFLIGHT_RUNTIME_DIR": str(self.args.runtime_dir),
            "OPS_PREFLIGHT_CONFIG_DIR": str(self.args.config_dir),
            "OPS_PREFLIGHT_RUNNER_ROOT": str(self.args.runner_root),
            "OPS_PREFLIGHT_UNIT_PATH": str(self.args.unit_path),
            "OPS_PREFLIGHT_UPDATER_UNIT_PATH": str(self.args.updater_unit_path),
            "OPS_PREFLIGHT_SOCKET_PATH": str(self.args.socket_path),
        })
        self.runtime.checked([str(self.args.preflight), mode], "preflight " + mode, env=environment)

    def validate_source(self) -> None:
        revision = self.runtime.checked(["git", "-C", str(self.args.repo_root), "rev-parse", "HEAD"], "读取源码 revision").strip()
        if revision != self.metadata["revision"]:
            fail("生产源码与批准制品 revision 不一致")
        dirty = self.runtime.checked(
            ["git", "-C", str(self.args.repo_root), "status", "--porcelain=v1", "--untracked-files=normal"],
            "读取源码工作树", env={**os.environ, "GIT_OPTIONAL_LOCKS": "0"},
        )
        if dirty.strip():
            fail("生产源码存在未收口改动；入口不会覆盖或清理")
        catalog = json.loads((Path(self.args.config_dir) / "services.json").read_text(encoding="utf-8"))
        fleet = catalog.get("fleet") or {}
        if fleet.get("allowRemoteRunners") or (fleet.get("remoteWorker") or {}).get("enabled"):
            fail("本机隔离证据不能覆盖远程 Runner；需另行受控部署")

    def prepare(self) -> dict:
        self.validate_source()
        self.preflight_run("runtime")
        self.runtime.assert_unit_binding()
        self.runtime.assert_updaters_idle()
        staging = self.state.directory / "candidate"
        if staging.exists():
            fail("候选暂存已有材料，拒绝自动重试部署")
        safe_extract_runner(Path(self.args.runner_archive), staging)
        candidate = self.runtime.candidate_info(staging, self.metadata)
        inspect_database(Path(self.args.db_path), candidate["schemaVersion"])
        self.runtime.prepare_image(self.metadata)
        self.state.data["candidate"] = candidate
        self.state.data["bootId"] = self.guard.boot_id()
        self.state.save()
        return candidate

    def freeze_and_backup(self, candidate: dict) -> None:
        image = self.runtime.inspect_web()
        self.state.data["quiesceStarted"] = True
        self.state.data["phase"] = "quiesce"
        self.state.save()
        self.runtime.stop_web()
        inspect_database(Path(self.args.db_path), candidate["schemaVersion"])
        self.runtime.stop_runner()
        self.runtime.assert_stopped()
        self.guard.assert_same_boot()
        inspect_database(Path(self.args.db_path), candidate["schemaVersion"])
        self.snapshot.capture(image, candidate["schemaVersion"])
        self.state.data["snapshotSha256"] = sha256_file(self.snapshot.manifest)
        self.state.step("backup", "succeeded")
        self.guard.fence(candidate)

    def install_and_verify(self, candidate: dict) -> None:
        self.guard.verify_fence()
        self.runtime.assert_stopped()
        self.state.data["changed"]["runner"] = True
        self.state.data["candidateFiles"] = self.candidate_files()
        self.state.data["phase"] = "runner"
        self.state.save()
        self.runtime.install_runner(self.state.directory / "candidate")
        self.state.data["candidateMayHaveStarted"] = True
        self.state.save()
        health = self.runtime.start_runner(
            self.metadata["revision"], maintenance=True, deployment_id=self.state.deployment_id,
        )
        if health.get("schemaVersion") != candidate["schemaVersion"]:
            fail("候选 Runner 迁移后 schema 未通过验收")
        self.preflight_run("installed")
        self.state.step("runner", "succeeded")
        self.state.data["changed"]["web"] = True
        self.state.data["phase"] = "web"
        self.state.save()
        update_runtime_env(Path(self.args.runtime_dir) / ".env", self.metadata["version"], self.metadata["revision"])
        self.runtime.start_web()
        self.preflight_run("maintenance")
        self.state.step("maintenance-preflight", "succeeded")
        self.guard.verify_fence()
        self.snapshot.assert_unmanaged_unchanged(self.snapshot.validate())
        inspect_database(Path(self.args.db_path), candidate["schemaVersion"])

    def activate(self) -> None:
        # 先持久化不可回退边界，再放开业务执行；此后不能以快照撤销新状态。
        self.state.data["activationStarted"] = True
        self.state.data["phase"] = "activation"
        self.state.save()
        self.state.event("activation_started")
        self.guard.remove_fence()
        self.runtime.restart_runner()
        self.runtime.wait_runner(self.metadata["revision"], maintenance=False)
        self.preflight_run("runtime")
        self.state.step("runtime-preflight", "succeeded")
        self.state.data["status"] = "succeeded"
        self.state.data["phase"] = "complete"
        self.state.save()
        self.state.event("deploy_succeeded", revision=self.metadata["revision"])
        self.guard.release()

    def mark_attention(self, reason: str) -> None:
        self.state.data["status"] = "needs_attention"
        self.state.data["error"] = reason
        self.state.save()
        self.state.event("needs_attention", reason=reason)

    def candidate_files(self) -> dict:
        sources = {
            "runner": (self.state.directory / "candidate/areasong-ops-runner", 0o755),
            "runner-updater": (self.state.directory / "candidate/areasong-ops-runner-updater", 0o755),
            "runner-unit": (Path(self.args.candidate_unit), 0o644),
            "runner-updater-unit": (Path(self.args.candidate_updater_unit), 0o644),
        }
        return {name: {"sha256": sha256_file(path), "mode": mode, "uid": os.geteuid(), "gid": os.getegid()} for name, (path, mode) in sources.items()}

    def stop_for_attention(self) -> bool:
        stopped = True
        for action in (self.runtime.stop_web, self.runtime.stop_runner):
            try:
                action()
            except (ReleaseError, OSError, ValueError):
                stopped = False
        return stopped

    def fail_closed(self, error: Exception) -> None:
        if isinstance(error, IsolationError):
            self.state.data["isolationUncertain"] = True
            # 停机本身也可能中断；先撤销持久化恢复资格，不能只保存在进程内存中。
            self.mark_attention("候选进程隔离身份不确定，禁止回退数据库")
            self.stop_for_attention()
            return
        if self.state.data.get("activationStarted"):
            if not self.stop_for_attention():
                self.mark_attention("正式激活失败且停机未完全验证；保留所有现场")
                return
            self.mark_attention("正式激活后验证失败；控制面已停止，禁止覆盖快照")
            return
        if self.state.data.get("maintenanceSha256") and self.state.data.get("snapshotSha256"):
            try:
                self.rollback_locked()
            except (ReleaseError, OSError, ValueError):
                self.stop_for_attention()
                self.state.data["rollback"]["status"] = "failed"
                self.mark_attention("隔离回退未完成；不重试写入，保留全部现场")
            return
        if self.state.data.get("quiesceStarted"):
            self.mark_attention("隔离或完整备份未完成；未替换程序，不自动恢复服务")
            return
        self.state.data["status"] = "failed"
        self.state.data["error"] = str(error)
        self.state.save()
        if self.guard.active.exists():
            self.guard.release()

    def deploy(self) -> None:
        with self.guard.locked():
            if self.state.data.get("status") == "succeeded":
                self.guard.finish_completed_claim()
                return
            if self.state.data.get("schemaVersion") != 2 or self.state.data.get("status") != "planned":
                fail("仅新建计划可部署；旧记录或未收口部署禁止自动重放")
            self.state.data["status"] = "running"
            self.state.save()
            try:
                candidate = self.prepare()
                self.guard.claim()
                self.freeze_and_backup(candidate)
                self.install_and_verify(candidate)
                self.activate()
            except (ReleaseError, OSError, ValueError) as error:
                self.fail_closed(error)
                raise

    def rollback_locked(self) -> None:
        self.guard.restore_allowed()
        self.state.data["rollback"]["status"] = "started"
        self.state.save()
        self.runtime.stop_web()
        self.runtime.stop_runner()
        self.runtime.assert_stopped()
        self.guard.restore_allowed()
        self.verify_restore_materials()
        manifest = self.snapshot.restore(self.guard.begin_rollback_activation)
        self.state.step("database-restore", "succeeded", schema=manifest["databaseSchema"])
        self.runtime.checked(["systemctl", "daemon-reload"], "恢复旧 unit")
        self.runtime.assert_unit_binding()
        self.runtime.restore_image(manifest)
        self.guard.rollback_activation(manifest["revision"])
        self.runtime.start_runner(manifest["revision"], maintenance=False)
        self.runtime.start_web()
        self.preflight_run("installed")
        self.preflight_run("runtime")
        self.state.data["rollback"] = {"status": "succeeded", "databaseSchema": manifest["databaseSchema"]}
        self.state.data["status"] = "rolled_back"
        self.state.data["phase"] = "rollback"
        self.state.save()
        self.state.event("rollback_succeeded", schema=manifest["databaseSchema"])
        self.guard.release()

    def rollback(self) -> None:
        with self.guard.locked():
            if self.state.data.get("status") == "rolled_back":
                self.guard.finish_completed_claim()
                return
            if self.state.data.get("rollback", {}).get("status") != "not_started":
                fail("回滚已有未收口结果，禁止自动重试")
            self.guard.restore_allowed()
            try:
                self.rollback_locked()
            except (ReleaseError, OSError, ValueError):
                self.state.data["rollback"]["status"] = "failed"
                self.stop_for_attention()
                self.mark_attention("回退失败；保留当前数据库、快照及程序，等待人工核对")
                raise

    def verify_restore_materials(self) -> None:
        manifest = self.snapshot.validate()
        targets = self.snapshot.targets()
        candidates = self.state.data.get("candidateFiles", {})
        for name in ("runner", "runner-updater", "runner-unit", "runner-updater-unit"):
            expected = [candidates.get(name)]
            if not self.state.data.get("candidateMayHaveStarted"):
                expected.append(manifest["files"][name])
            if file_evidence(targets[name]) not in expected:
                fail("当前程序或 unit 已漂移，无法证明发布隔离，禁止恢复")
        inspect_database(Path(self.args.db_path), self.state.data["candidate"]["schemaVersion"])

def fixed_defaults() -> dict[str, str]:
    return {
        "boot_id_path": "/proc/sys/kernel/random/boot_id",
        "cgroup_root": "/sys/fs/cgroup",
        "repo_root": os.environ.get("OPS_RELEASE_REPO_ROOT", "/opt/ops"),
        "runtime_dir": os.environ.get("OPS_RELEASE_RUNTIME_DIR", "/opt/services/areasong-ops"),
        "config_dir": os.environ.get("OPS_RELEASE_CONFIG_DIR", "/etc/areasong-ops"),
        "runner_root": os.environ.get("OPS_RELEASE_RUNNER_ROOT", "/usr/local/libexec/areasong-ops"),
        "unit_path": os.environ.get("OPS_RELEASE_UNIT_PATH", "/etc/systemd/system/areasong-ops-runner.service"),
        "updater_unit_path": os.environ.get("OPS_RELEASE_UPDATER_UNIT_PATH", "/etc/systemd/system/areasong-ops-runner-update@.service"),
        "db_path": os.environ.get("OPS_RELEASE_DB_PATH", "/var/lib/areasong-ops/ops.db"),
        "socket_path": os.environ.get("OPS_RELEASE_SOCKET_PATH", "/var/lib/areasong-ops/run/runner.sock"),
        "container_name": os.environ.get("OPS_RELEASE_CONTAINER", "areasong-ops-web"),
        "preflight": os.environ.get("OPS_RELEASE_PREFLIGHT", "/opt/ops/services/areasong-ops/deploy/preflight.sh"),
        "candidate_unit": os.environ.get("OPS_RELEASE_CANDIDATE_UNIT", "/opt/ops/services/areasong-ops/deploy/areasong-ops-runner.service"),
        "candidate_updater_unit": os.environ.get("OPS_RELEASE_CANDIDATE_UPDATER_UNIT", "/opt/ops/services/areasong-ops/deploy/areasong-ops-runner-update@.service"),
    }


def parser() -> argparse.ArgumentParser:
    defaults = fixed_defaults()
    root = argparse.ArgumentParser(description="AreaSong Ops 单一控制面发布入口")
    sub = root.add_subparsers(dest="action", required=True)
    for action in ("plan", "deploy"):
        command = sub.add_parser(action)
        command.add_argument("--manifest", required=True, type=Path)
        command.add_argument("--runner-archive", required=True, type=Path)
        command.add_argument("--checksum", required=True, type=Path)
        command.add_argument("--sigstore-bundle", required=True, type=Path)
        command.add_argument("--verifier", type=Path, default=Path(__file__).with_name("verify-release-assets.sh"))
        command.add_argument("--state-dir", type=Path, default=Path(os.environ.get("OPS_RELEASE_STATE_DIR", "/var/lib/areasong-ops/release-orchestrator")))
        command.add_argument("--deployment-id", default="")
        for name, value in defaults.items():
            option = "--" + name.replace("_", "-")
            command.add_argument(option, dest=name, default=value)
    status = sub.add_parser("status")
    status.add_argument("deployment_id")
    status.add_argument("--state-dir", type=Path, default=Path(os.environ.get("OPS_RELEASE_STATE_DIR", "/var/lib/areasong-ops/release-orchestrator")))
    rollback = sub.add_parser("rollback")
    rollback.add_argument("deployment_id")
    rollback.add_argument("--state-dir", type=Path, default=Path(os.environ.get("OPS_RELEASE_STATE_DIR", "/var/lib/areasong-ops/release-orchestrator")))
    for name, value in defaults.items():
        rollback.add_argument("--" + name.replace("_", "-"), dest=name, default=value)
    return root


def require_root(action: str) -> None:
    if action in {"plan", "deploy", "rollback"} and os.geteuid() != 0 and os.environ.get("OPS_RELEASE_TEST_MODE") != "1":
        fail(f"{action} 必须以 root 执行")


def require_production_paths(args: argparse.Namespace) -> None:
    """非测试模式拒绝通过参数或环境变量把发布改指向任意主机路径。"""
    if os.environ.get("OPS_RELEASE_TEST_MODE") == "1":
        return
    if args.action == "plan":
        if str(args.state_dir) != "/var/lib/areasong-ops/release-orchestrator":
            fail("生产发布 state-dir 必须固定在 /var/lib/areasong-ops/release-orchestrator")
        return
    if args.action not in {"deploy", "rollback"}:
        return
    expected = {
        "boot_id_path": "/proc/sys/kernel/random/boot_id",
        "cgroup_root": "/sys/fs/cgroup",
        "repo_root": "/opt/ops",
        "runtime_dir": "/opt/services/areasong-ops",
        "config_dir": "/etc/areasong-ops",
        "runner_root": "/usr/local/libexec/areasong-ops",
        "unit_path": "/etc/systemd/system/areasong-ops-runner.service",
        "updater_unit_path": "/etc/systemd/system/areasong-ops-runner-update@.service",
        "db_path": "/var/lib/areasong-ops/ops.db",
        "socket_path": "/var/lib/areasong-ops/run/runner.sock",
        "container_name": "areasong-ops-web",
        "preflight": "/opt/ops/services/areasong-ops/deploy/preflight.sh",
        "candidate_unit": "/opt/ops/services/areasong-ops/deploy/areasong-ops-runner.service",
        "candidate_updater_unit": "/opt/ops/services/areasong-ops/deploy/areasong-ops-runner-update@.service",
    }
    for name, value in expected.items():
        if str(getattr(args, name)) != value:
            fail(f"生产发布路径固定为 {value}，不允许覆盖 {name}")
    if str(args.state_dir) != "/var/lib/areasong-ops/release-orchestrator":
        fail("生产发布 state-dir 必须固定在 /var/lib/areasong-ops/release-orchestrator")


def prepare_state_root(path: Path, *, strict: bool = False) -> None:
    if strict and path.exists():
        if path.is_symlink():
            fail("生产发布 state-dir 不能是符号链接")
        stat = path.stat()
        if stat.st_uid != 0 or stat.st_mode & 0o777 != 0o700:
            fail("生产发布 state-dir 必须是 root:root 0700")
    ensure_dir(path)
    if not strict or os.environ.get("OPS_RELEASE_TEST_MODE") == "1":
        return
    stat = path.stat()
    if stat.st_uid != 0 or stat.st_mode & 0o777 != 0o700:
        fail("生产发布 state-dir 必须是 root:root 0700")


def main(argv: Iterable[str] | None = None) -> int:
    args = parser().parse_args(list(argv) if argv is not None else None)
    try:
        if args.action in {"status", "rollback"} and not SAFE_ID.fullmatch(args.deployment_id):
            fail("deployment ID 格式无效")
        require_root(args.action)
        require_production_paths(args)
        if args.action == "status":
            path = args.state_dir / "deployments" / args.deployment_id / "state.json"
            regular_file(path, label="deployment state")
            print(path.read_text(encoding="utf-8"), end="")
            return 0
        if args.action == "rollback":
            prepare_state_root(args.state_dir, strict=True)
            path = args.state_dir / "deployments" / args.deployment_id / "state.json"
            if not path.exists():
                fail(f"deployment 不存在: {args.deployment_id}")
            metadata = json.loads(path.read_text(encoding="utf-8"))["input"]
            state = State(args.state_dir, args.deployment_id, metadata, create=False)
            Orchestrator(args, state, metadata).rollback()
            print(json.dumps(state.data, ensure_ascii=False, sort_keys=True))
            return 0
        metadata = verify_assets(args.manifest, args.runner_archive, args.checksum, args.sigstore_bundle, args.verifier)
        deployment = args.deployment_id or deployment_id()
        if not SAFE_ID.fullmatch(deployment):
            fail("deployment ID 格式无效")
        prepare_state_root(args.state_dir, strict=args.action in {"deploy", "rollback"})
        state = State(args.state_dir, deployment, metadata, create=True)
        state.event("plan_created", revision=metadata["revision"], version=metadata["version"])
        if args.action == "plan":
            print(json.dumps({"deploymentId": deployment, "status": state.data["status"], "input": metadata}, ensure_ascii=False, sort_keys=True))
            return 0
        Orchestrator(args, state, metadata).deploy()
        print(json.dumps({"deploymentId": deployment, "status": state.data["status"], "revision": metadata["revision"]}, ensure_ascii=False, sort_keys=True))
        return 0
    except ReleaseError as error:
        print(f"release orchestrator failed: {error}", file=sys.stderr)
        return 1
    except (OSError, json.JSONDecodeError) as error:
        print(f"release orchestrator failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
