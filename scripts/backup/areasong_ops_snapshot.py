"""控制面自包含快照的共同契约；导入和 validate 均不写入数据库。"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import sqlite3
import stat
import time
from pathlib import Path


# 仅纳入已用真实迁移库验证的版本；新增迁移必须同步兼容性测试。
BACKUP_SCHEMAS = frozenset({5, 45, 47})
RESTORE_SCHEMAS = BACKUP_SCHEMAS | {4}
BASE_COLUMNS = {
    "previews": {"id", "actor_hash", "service", "action", "confirmation_hash", "created_at", "expires_at"},
    "tasks": {"id", "idempotency_key", "request_hash", "actor_hash", "service", "action", "state", "preview_id", "snapshot_json", "created_at"},
    "events": {"sequence", "task_id", "occurred_at", "level", "message", "data_json"},
    "audit_entries": {"sequence", "occurred_at", "actor_hash", "event", "resource", "outcome", "detail_json"},
    "metadata": {"key", "value"},
}
CREDENTIAL_COLUMNS = {"id", "actor_hash", "credential_type", "target", "state", "fingerprint", "expires_at", "created_at"}
MODERN_COLUMNS = {
    "tasks": {"plan_id", "plan_digest", "production_changed", "recovery_point_id", "finished_at"},
    "release_plans": {"id", "actor_hash", "state", "digest", "approval_summary_json", "task_id", "observation_ends_at", "closure_reason"},
    "recovery_points": {"id", "task_id", "service", "status", "evidence_json", "evidence_digest", "expected_before_digest"},
    "access_policy_snapshots": {"version", "digest", "policy_json", "actor_hash", "created_at"},
    "task_assignments": {"task_id", "runner_id", "state", "generation", "contract_digest"},
}


def snapshot_file(path: Path) -> os.stat_result:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode):
        raise ValueError("SQLite 快照必须是非符号链接普通文件")
    # immutable 只能用于 VACUUM INTO 等产生的独立快照，不能忽略活库 WAL。
    if any(os.path.lexists(str(path) + suffix) for suffix in ("-wal", "-shm", "-journal")):
        raise ValueError("SQLite 快照不是自包含文件：存在 journal/WAL/SHM")
    return info


def inspect_database(path: Path, *, allow_legacy: bool = False) -> dict[str, int]:
    snapshot_file(path)
    connection = sqlite3.connect(path.absolute().as_uri() + "?mode=ro&immutable=1", uri=True)
    try:
        connection.execute("PRAGMA query_only=ON")
        version = connection.execute("PRAGMA user_version").fetchone()[0]
        if version not in (RESTORE_SCHEMAS if allow_legacy else BACKUP_SCHEMAS):
            raise ValueError(f"AreaSong Ops 快照 schema 版本不受支持: {version}")
        if connection.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
            raise ValueError("AreaSong Ops 快照 integrity_check 失败")
        if connection.execute("PRAGMA foreign_key_check").fetchone() is not None:
            raise ValueError("AreaSong Ops 快照外键不一致：foreign_key_check 失败")
        required = {name: set(columns) for name, columns in BASE_COLUMNS.items()}
        if version >= 5:
            required["credential_rotations"] = CREDENTIAL_COLUMNS
        if version in {45, 47}:
            for name, columns in MODERN_COLUMNS.items():
                required.setdefault(name, set()).update(columns)
        if version == 47:
            required["release_plans"].add("approval_policy")
            required["kubernetes_plans"] = {"id", "approval_policy", "plan_digest", "preview_json"}
        return inspect_tables(connection, required)
    finally:
        connection.close()


def inspect_tables(connection: sqlite3.Connection, required: dict) -> dict[str, int]:
    tables = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    if missing := set(required) - tables:
        raise ValueError("AreaSong Ops 快照缺少关键表: " + ", ".join(sorted(missing)))
    counts = {}
    for table, columns in required.items():
        actual = {row[1] for row in connection.execute(f'PRAGMA table_info("{table}")')}
        if missing := columns - actual:
            raise ValueError(f"AreaSong Ops 快照表 {table} 缺少关键列: " + ", ".join(sorted(missing)))
        counts[table] = connection.execute(f'SELECT COUNT(*) FROM "{table}"').fetchone()[0]
    return counts


def copy_snapshot(source: Path, destination: Path, max_age: int) -> None:
    before = snapshot_file(source)
    age = time.time() - before.st_mtime
    if max_age <= 0 or not -60 <= age <= max_age:
        raise ValueError("AreaSong Ops snapshot age is outside the allowed window")
    created = False
    try:
        descriptor = os.open(source, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(descriptor, "rb") as reader:
            if os.fstat(reader.fileno()) != before:
                raise ValueError("SQLite 快照在读取前发生变化")
            with destination.open("xb") as writer:
                created = True
                os.fchmod(writer.fileno(), 0o600)
                shutil.copyfileobj(reader, writer)
                writer.flush()
                os.fsync(writer.fileno())
        after = snapshot_file(source)
        fields = ("st_dev", "st_ino", "st_size", "st_mtime_ns", "st_ctime_ns")
        if any(getattr(before, key) != getattr(after, key) for key in fields):
            raise ValueError("SQLite 快照在复制期间发生变化")
        inspect_database(destination)
    except BaseException:
        if created:
            destination.unlink()
        raise


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    validate = commands.add_parser("validate")
    validate.add_argument("path", type=Path)
    validate.add_argument("--allow-legacy", action="store_true")
    copy = commands.add_parser("copy")
    copy.add_argument("source", type=Path)
    copy.add_argument("destination", type=Path)
    copy.add_argument("max_age", type=int)
    args = parser.parse_args()
    if args.command == "copy":
        copy_snapshot(args.source, args.destination, args.max_age)
    else:
        print(json.dumps(inspect_database(args.path, allow_legacy=args.allow_legacy)))


if __name__ == "__main__":
    main()
