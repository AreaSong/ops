"""只在控制面已经停稳、仍处于发布隔离区时保存和恢复状态。"""

from __future__ import annotations

import json
import os
import sqlite3
import stat
from pathlib import Path

from release_common import (
    ReleaseError, atomic_json, copy_atomic, ensure_dir, fail, read_env,
    regular_file, sha256_file, snapshot_sqlite, sync_directory,
)


# 固定表名与状态来自 Runner 的恢复协调器；未知 schema/缺表绝不能算作空闲。
ACTIVE_STATES = {
    "tasks": ("waiting_confirmation", "queued", "running", "rolling_back"),
    "release_plans": ("executing", "observing"),
    "task_assignments": ("assigned", "claimed"),
    "batch_jobs": ("running", "observing", "paused", "rolling_back"),
    "batch_items": ("ready", "running"),
    "runner_fleet_update_plans": ("running", "observing", "rolling_back"),
    "runner_fleet_update_items": ("ready", "running", "rollback_ready", "rolling_back"),
    "runner_fleet_update_receipts": ("prepared", "launching", "launched", "needs_attention"),
    "runner_updates": ("prepared", "activating", "needs_attention"),
    "credential_rotations": ("running", "switched_pending_revocation", "revocation_verified", "needs_attention"),
    "terminal_sessions": ("running",),
    "terminal_shell_plans": ("running",),
    "managed_file_proposals": ("applying", "rolling_back"),
    "compose_revisions": ("applying", "rolling_back"),
    "extension_packages": ("staging",),
    "extension_plans": ("running",),
    "kubernetes_plans": ("running",),
}


def sqlite_connection(path: Path) -> sqlite3.Connection:
    regular_file(path, label="SQLite")
    for suffix in ("-wal", "-shm", "-journal"):
        sidecar = Path(str(path) + suffix)
        if os.path.lexists(sidecar):
            regular_file(sidecar, label="SQLite sidecar")
    return sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True, timeout=5)


def inspect_database(path: Path, maximum_schema: int, *, idle: bool = True) -> int:
    connection = None
    try:
        connection = sqlite_connection(path)
        version = connection.execute("PRAGMA user_version").fetchone()[0]
        if not 45 <= version <= maximum_schema:
            fail(f"SQLite schema 不在已验证的升级范围: {version}")
        if connection.execute("PRAGMA quick_check").fetchall() != [("ok",)]:
            fail("SQLite 完整性校验失败")
        if connection.execute("PRAGMA foreign_key_check").fetchone() is not None:
            fail("SQLite 外键校验失败")
        if idle:
            require_idle(connection)
        return version
    except sqlite3.Error as error:
        raise ReleaseError("无法证明 SQLite 结构和活动状态安全") from error
    finally:
        if connection is not None:
            connection.close()


def require_idle(connection: sqlite3.Connection) -> None:
    for table, states in ACTIVE_STATES.items():
        placeholders = ",".join("?" for _ in states)
        count = connection.execute(
            f"SELECT COUNT(*) FROM {table} WHERE state IN ({placeholders})", states,
        ).fetchone()[0]
        if count:
            fail(f"控制面尚有未收口状态: {table} ({count})")
    count = connection.execute(
        "SELECT COUNT(*) FROM kubernetes_operations WHERE state='pending' OR finished_at IS NULL",
    ).fetchone()[0]
    if count:
        fail(f"控制面尚有未收口状态: kubernetes_operations ({count})")


def file_evidence(path: Path) -> dict:
    regular_file(path, label="发布文件")
    info = path.stat()
    return {"sha256": sha256_file(path), "mode": stat.S_IMODE(info.st_mode), "uid": info.st_uid, "gid": info.st_gid}


class Snapshot:
    def __init__(self, args, directory: Path):
        self.args = args
        self.directory = directory
        self.backup = directory / "backup"
        self.manifest = self.backup / "snapshot.json"

    def targets(self) -> dict[str, Path]:
        args = self.args
        paths = {
            "runner": Path(args.runner_root) / "runner/areasong-ops-runner",
            "runner-updater": Path(args.runner_root) / "areasong-ops-runner-updater",
            "runner-unit": Path(args.unit_path),
            "runner-updater-unit": Path(args.updater_unit_path),
            "services-json": Path(args.config_dir) / "services.json",
            "web-env": Path(args.config_dir) / "web.env",
            "compose": Path(args.runtime_dir) / "compose.yml",
            "runtime-env": Path(args.runtime_dir) / ".env",
        }
        adapters = Path(args.runner_root) / "adapters"
        if not adapters.is_dir() or adapters.is_symlink():
            fail("受控适配器目录缺失或身份异常")
        for path in sorted(adapters.iterdir()):
            if path.suffix != ".sh":
                fail("受控适配器目录存在非预期文件")
            regular_file(path, label="适配器")
            paths["adapter-" + path.name] = path
        if not any(name.startswith("adapter-") for name in paths):
            fail("受控适配器目录为空")
        return paths

    def capture(self, image: dict, maximum_schema: int) -> dict:
        if self.backup.exists() and any(self.backup.iterdir()):
            fail("备份目录不为空，拒绝覆盖")
        ensure_dir(self.backup)
        version = inspect_database(Path(self.args.db_path), maximum_schema)
        records = {}
        for name, source in self.targets().items():
            records[name] = file_evidence(source)
            copy_atomic(source, self.backup / name, 0o600)
            if sha256_file(self.backup / name) != records[name]["sha256"] or file_evidence(source) != records[name]:
                fail("备份期间文件发生漂移")
        snapshot_sqlite(Path(self.args.db_path), self.backup / "ops.db")
        database = file_evidence(Path(self.args.db_path))
        database["sha256"] = sha256_file(self.backup / "ops.db")
        manifest = {
            "schemaVersion": 1, "databaseSchema": version, "database": database,
            "files": records, "image": image,
            "revision": read_env(self.backup / "runtime-env").get("OPS_BUILD_REVISION", ""),
        }
        atomic_json(self.manifest, manifest)
        self.validate()
        return manifest

    def validate(self) -> dict:
        regular_file(self.manifest, label="备份清单")
        payload = json.loads(self.manifest.read_text(encoding="utf-8"))
        if payload.get("schemaVersion") != 1 or set(payload.get("files", {})) != set(self.targets()):
            fail("备份集合与固定发布路径不一致")
        for name, evidence in {**payload["files"], "ops.db": payload["database"]}.items():
            path = self.backup / name
            regular_file(path, label="备份文件")
            if sha256_file(path) != evidence["sha256"]:
                fail("备份摘要不匹配，禁止恢复")
            if not isinstance(evidence.get("mode"), int) or not isinstance(evidence.get("uid"), int) or not isinstance(evidence.get("gid"), int):
                fail("备份权限证据缺失")
        version = inspect_database(self.backup / "ops.db", payload["databaseSchema"])
        if version != payload["databaseSchema"]:
            fail("备份 schema 与证据不一致")
        return payload

    def assert_unmanaged_unchanged(self, manifest: dict) -> None:
        # 本入口不修改这些配置；外部并发改动不能被“回滚”静默抹掉。
        for name, path in self.targets().items():
            if name in {"services-json", "web-env", "compose"} or name.startswith("adapter-"):
                if file_evidence(path) != manifest["files"][name]:
                    fail("发布范围外的配置或适配器发生漂移，禁止恢复")

    def restore(self, before_entry_switch=lambda: None) -> dict:
        manifest = self.validate()
        self.assert_unmanaged_unchanged(manifest)
        self.preserve_failed_database()
        self.restore_file(self.backup / "ops.db", Path(self.args.db_path), manifest["database"])
        version = inspect_database(Path(self.args.db_path), manifest["databaseSchema"])
        if version != manifest["databaseSchema"]:
            fail("恢复后的 SQLite schema 不匹配，禁止启动旧 Runner")
        entries = {"runner", "runner-updater", "runner-unit", "runner-updater-unit"}
        for name, target in self.targets().items():
            if name not in entries:
                self.restore_file(self.backup / name, target, manifest["files"][name])
        # 旧 Runner 不理解维护标记；必须先完成状态恢复，并持久化不可重放边界。
        before_entry_switch()
        for name in ("runner-updater-unit", "runner-unit", "runner-updater", "runner"):
            self.restore_file(self.backup / name, self.targets()[name], manifest["files"][name])
        return manifest

    def preserve_failed_database(self) -> None:
        destination = self.directory / "before-rollback"
        if destination.exists():
            fail("失败现场已保存，拒绝重复覆盖或自动重试恢复")
        database = Path(self.args.db_path)
        sources = [database]
        for suffix in ("-wal", "-shm", "-journal"):
            sidecar = Path(str(database) + suffix)
            if os.path.lexists(sidecar):
                regular_file(sidecar, label="待隔离 SQLite sidecar")
                sources.append(sidecar)
        for source in sources:
            regular_file(source, label="失败数据库现场")
        ensure_dir(destination)
        for source in sources:
            copy_atomic(source, destination / source.name, 0o600)
        sync_directory(destination)
        # 已停尽所有连接，且完整保留现场；旧 WAL 绝不能和回退后的主库组合。
        for source in sources[1:]:
            os.replace(source, destination / (source.name + ".original"))
        sync_directory(database.parent)
        sync_directory(destination)

    @staticmethod
    def restore_file(source: Path, target: Path, evidence: dict) -> None:
        copy_atomic(source, target, evidence["mode"])
        if target.stat().st_uid != evidence["uid"] or target.stat().st_gid != evidence["gid"]:
            os.chown(target, evidence["uid"], evidence["gid"])
        if file_evidence(target) != evidence:
            fail("恢复文件的摘要或权限复验失败")
