"""持久化发布隔离证据；缺失、漂移或跨越激活边界时一律拒绝恢复。"""

from __future__ import annotations

import fcntl
import json
import os
import re
import stat
from contextlib import contextmanager
from pathlib import Path

from release_common import atomic_json, ensure_dir, fail, regular_file, sha256_file, sync_directory


class Guard:
    def __init__(self, args, state):
        self.args = args
        self.state = state
        self.root = Path(args.state_dir)
        self.active = self.root / "active.json"
        self.marker = self.root / "maintenance.json"

    def owner_uid(self) -> int:
        return os.geteuid() if os.environ.get("OPS_RELEASE_TEST_MODE") == "1" else 0

    def private_file(self, path: Path) -> None:
        regular_file(path, label="发布状态文件")
        info = path.stat()
        if info.st_uid != self.owner_uid() or stat.S_IMODE(info.st_mode) != 0o600:
            fail("发布状态文件属主或权限不可信")
        if os.environ.get("OPS_RELEASE_TEST_MODE") != "1" and info.st_gid != 0:
            fail("发布状态文件必须属于 root:root")

    def trusted_paths(self) -> None:
        testing = os.environ.get("OPS_RELEASE_TEST_MODE") == "1"
        anchor = Path(self.args.db_path).parent if testing else Path("/")
        paths = [self.root, self.state.directory, Path(self.args.runtime_dir), Path(self.args.config_dir),
                 Path(self.args.runner_root), Path(self.args.unit_path).parent]
        for child in (self.state.directory / "backup", self.state.directory / "candidate"):
            if os.path.lexists(child):
                paths.append(child)
        for path in paths:
            self.trusted_directory_chain(path, anchor)
        if stat.S_IMODE(self.root.stat().st_mode) != 0o700:
            fail("发布状态目录必须为 0700")

    def trusted_directory_chain(self, path: Path, anchor: Path) -> None:
        current = path
        while True:
            info = current.lstat()
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != self.owner_uid() or info.st_mode & 0o022:
                fail("发布状态目录信任链无效")
            if current == anchor:
                break
            if current == current.parent:
                fail("发布状态目录不在受信根路径内")
            current = current.parent

    @contextmanager
    def locked(self):
        ensure_dir(self.root)
        self.trusted_paths()
        path = self.root / ".lock"
        descriptor = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            self.private_file(path)
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                fail("已有发布或回滚持有全局锁")
            # 调用方在锁外构造的 State 可能过时，决策必须使用锁内重新读取的内容。
            self.private_file(self.state.path)
            fresh = json.loads(self.state.path.read_text(encoding="utf-8"))
            if fresh.get("deploymentId") != self.state.deployment_id or fresh.get("input", {}).get("manifest_sha256") != self.state.data["input"].get("manifest_sha256"):
                fail("锁内发布身份或制品发生漂移")
            self.state.data = fresh
            yield
        finally:
            os.close(descriptor)

    def claim(self) -> None:
        if self.active.exists() or self.active.is_symlink():
            self.assert_owner()
            fail("已有未收口发布，禁止自动重放写入；先核对状态")
        if os.path.lexists(self.marker):
            fail("存在孤立的维护标记，禁止覆盖")
        atomic_json(self.active, {
            "schemaVersion": 1, "deploymentId": self.state.deployment_id,
            "revision": self.state.data["input"]["revision"],
        })

    def assert_owner(self) -> dict:
        self.private_file(self.active)
        owner = json.loads(self.active.read_text(encoding="utf-8"))
        if owner.get("schemaVersion") != 1 or owner.get("deploymentId") != self.state.deployment_id:
            fail("活动发布不属于当前变更，禁止操作")
        return owner

    def boot_id(self) -> str:
        value = Path(self.args.boot_id_path).read_text(encoding="utf-8").strip()
        if not re.fullmatch(r"[a-f0-9-]{36}", value):
            fail("主机启动身份无效")
        return value

    def assert_same_boot(self) -> None:
        if self.state.data.get("bootId") != self.boot_id():
            fail("发布后主机已重启，无法证明持续隔离；禁止恢复")

    def fence(self, candidate: dict) -> None:
        self.assert_owner()
        if os.path.lexists(self.marker):
            fail("维护标记已存在，禁止覆盖")
        payload = {
            "schemaVersion": 1, "deploymentId": self.state.deployment_id,
            "revision": self.state.data["input"]["revision"],
            "runnerSha256": candidate["runnerSha256"],
            "catalogSha256": sha256_file(Path(self.args.config_dir) / "services.json"),
            "bootId": self.state.data["bootId"],
        }
        atomic_json(self.marker, payload)
        self.state.data["maintenanceSha256"] = sha256_file(self.marker)
        self.state.save()

    def verify_fence(self) -> None:
        self.assert_owner()
        self.assert_same_boot()
        self.private_file(self.marker)
        if sha256_file(self.marker) != self.state.data.get("maintenanceSha256"):
            fail("维护标记与持久化证据不一致，禁止恢复")
        payload = json.loads(self.marker.read_text(encoding="utf-8"))
        if payload.get("deploymentId") != self.state.deployment_id or payload.get("revision") != self.state.data["input"]["revision"]:
            fail("维护标记目标不匹配")

    def restore_allowed(self) -> None:
        self.trusted_paths()
        if self.state.data.get("isolationUncertain"):
            fail("候选进程隔离未被证明，禁止恢复快照")
        if self.state.data.get("schemaVersion") != 2:
            fail("旧部署缺少隔离恢复证据，禁止自动回滚")
        if self.state.data.get("activationStarted") or self.state.data.get("rollbackActivationStarted"):
            fail("已经越过正式激活边界；不得用旧快照覆盖后续状态")
        if self.state.data.get("rollback", {}).get("status") not in {"not_started", "started"}:
            fail("回滚已执行或状态未知，禁止自动重试")
        self.verify_fence()
        backup = self.state.directory / "backup/snapshot.json"
        self.private_file(backup)
        if sha256_file(backup) != self.state.data.get("snapshotSha256"):
            fail("备份清单缺少可信摘要")

    def remove_fence(self) -> None:
        self.verify_fence()
        self.marker.unlink()
        sync_directory(self.root)

    def rollback_activation(self, revision: str) -> None:
        self.begin_rollback_activation()
        self.remove_fence()
        atomic_json(self.active, {"schemaVersion": 1, "deploymentId": self.state.deployment_id, "revision": revision})

    def begin_rollback_activation(self) -> None:
        self.state.data["rollbackActivationStarted"] = True
        self.state.save()

    def release(self) -> None:
        self.assert_owner()
        if os.path.lexists(self.marker):
            fail("维护标记未解除，不能释放发布占用")
        self.active.unlink()
        sync_directory(self.root)

    def finish_completed_claim(self) -> None:
        if not os.path.lexists(self.active):
            return
        self.private_file(self.active)
        owner = json.loads(self.active.read_text(encoding="utf-8"))
        if owner.get("deploymentId") == self.state.deployment_id:
            self.release()
