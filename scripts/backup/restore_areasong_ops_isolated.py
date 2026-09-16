#!/usr/bin/env python3
from __future__ import annotations

import argparse
import fcntl
import fnmatch
import os
import shutil
import sqlite3
import sys
import tarfile
import tempfile
import time
from pathlib import Path

import backup_manifest
import areasong_ops_snapshot


ROLE = "volume-areasong-ops-state"
DATABASE_MEMBER = "areasong-ops-state/ops.db"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="在隔离临时目录中验证 AreaSong Ops SQLite 备份。",
    )
    parser.add_argument("--backup-root", default=os.environ.get("BACKUP_ROOT", "/var/backups/ops"))
    parser.add_argument("--manifest", required=True, help="相对于备份根目录的完整 manifest 路径")
    parser.add_argument(
        "--work-root",
        default=os.environ.get("AREASONG_OPS_RESTORE_WORK_ROOT", "/var/tmp"),
    )
    parser.add_argument(
        "--metric-out",
        default=os.environ.get(
            "AREASONG_OPS_RESTORE_METRIC_OUT",
            "/var/lib/node_exporter/textfile_collector/areasong-ops-restore-drill.prom",
        ),
    )
    parser.add_argument(
        "--lock-file",
        default=os.environ.get(
            "AREASONG_OPS_RESTORE_LOCK_FILE",
            "/run/lock/areasong-ops-restore-drill.lock",
        ),
    )
    return parser.parse_args()


def resolve_manifest(backup_root: Path, relative_path: str) -> Path:
    normalized = backup_manifest.validate_relative_path(relative_path).as_posix()
    if not fnmatch.fnmatchcase(normalized, "manifests/backup-set-*.json"):
        raise ValueError("manifest 路径不符合受控命名规则")
    return backup_manifest.safe_relative_path(backup_root, normalized)


def acquire_lock(path: Path):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    flags = os.O_CREAT | os.O_RDWR
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(path, flags, 0o600)
    handle = os.fdopen(descriptor, "w")
    try:
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        handle.close()
        raise RuntimeError("已有 AreaSong Ops 隔离恢复演练正在运行") from None
    return handle


def inspect_database(path: Path) -> dict[str, int]:
    return areasong_ops_snapshot.inspect_database(path, allow_legacy=True)


def require_self_contained_archive(path: Path) -> None:
    sidecars = {
        backup_manifest.validate_relative_path(DATABASE_MEMBER + suffix)
        for suffix in ("-wal", "-shm", "-journal")
    }
    # 选择性解压会隐藏日志文件，必须在提取主库前拒绝，不能把旧主库误报为恢复成功。
    with tarfile.open(path, "r|gz") as archive:
        for member in archive:
            member_path = backup_manifest.validate_tar_member_name(member.name)
            if member_path is not None and (
                member_path in sidecars or any(parent in sidecars for parent in member_path.parents)
            ):
                raise ValueError("AreaSong Ops 归档非自包含：包含数据库 WAL/SHM/journal")


def write_metrics(path: Path, started_at: float, database_path: Path, counts: dict[str, int]) -> None:
    completed_at = int(time.time())
    duration = max(0.0, time.monotonic() - started_at)
    lines = [
        "# HELP areasong_ops_restore_drill_last_success_timestamp_seconds 最近一次成功隔离恢复演练的 Unix 时间戳。",
        "# TYPE areasong_ops_restore_drill_last_success_timestamp_seconds gauge",
        f"areasong_ops_restore_drill_last_success_timestamp_seconds {completed_at}",
        "# HELP areasong_ops_restore_drill_duration_seconds 最近一次成功隔离恢复演练耗时。",
        "# TYPE areasong_ops_restore_drill_duration_seconds gauge",
        f"areasong_ops_restore_drill_duration_seconds {duration:.3f}",
        "# HELP areasong_ops_restore_drill_database_size_bytes 最近一次成功隔离恢复数据库大小。",
        "# TYPE areasong_ops_restore_drill_database_size_bytes gauge",
        f"areasong_ops_restore_drill_database_size_bytes {database_path.stat().st_size}",
        "# HELP areasong_ops_restore_drill_table_rows 最近一次成功隔离恢复的关键表行数。",
        "# TYPE areasong_ops_restore_drill_table_rows gauge",
    ]
    lines.extend(
        f'areasong_ops_restore_drill_table_rows{{table="{table}"}} {counts[table]}'
        for table in counts
    )
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write("\n".join(lines) + "\n")
        os.chmod(temporary, 0o644)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def run(args: argparse.Namespace) -> dict[str, int]:
    started_at = time.monotonic()
    backup_root = Path(args.backup_root)
    if not backup_root.is_dir() or backup_root.is_symlink():
        raise ValueError("备份根目录缺失或不安全")
    manifest_path = resolve_manifest(backup_root, args.manifest)
    records = backup_manifest.verify_manifest(backup_root, manifest_path, {ROLE})
    record = records[0]
    archive_path = backup_manifest.safe_relative_path(backup_root, record.path)
    require_self_contained_archive(archive_path)

    work_root = Path(args.work_root)
    work_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    if not work_root.is_dir() or work_root.is_symlink():
        raise ValueError("隔离恢复工作根目录不安全")
    work_dir = Path(tempfile.mkdtemp(prefix="areasong-ops-restore-", dir=work_root))
    os.chmod(work_dir, 0o700)
    try:
        extract_root = work_dir / "extracted"
        backup_manifest.safe_extract_tar(
            archive_path,
            extract_root,
            {DATABASE_MEMBER},
            max_members=1,
            max_bytes=record.unpacked_size_bytes,
        )
        database_path = extract_root / DATABASE_MEMBER
        counts = inspect_database(database_path)
        write_metrics(Path(args.metric_out), started_at, database_path, counts)
        return counts
    finally:
        shutil.rmtree(work_dir)


def main() -> int:
    args = parse_args()
    try:
        with acquire_lock(Path(args.lock_file)):
            counts = run(args)
    except (OSError, RuntimeError, ValueError, sqlite3.DatabaseError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    summary = " ".join(f"{table}={count}" for table, count in counts.items())
    print(f"AreaSong Ops 隔离恢复演练成功：{summary}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
