#!/usr/bin/env python3
"""Create and validate isolated AreaSong Ops SQLite snapshots.

The command is intentionally opt-in.  Importing this module, or invoking it
without a subcommand, never opens or writes the production database.  Snapshot
creation only creates a new destination file and refuses to replace an
existing path; restore-check validates a temporary copy and has no production
database argument by design.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import stat
import sqlite3
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Dict, Iterable, List, Mapping, Optional, Sequence, Set, Tuple
from urllib.parse import quote


DEFAULT_SOURCE = os.environ.get("OPS_DB_PATH", "/var/lib/areasong-ops/ops.db")
IDENTIFIER = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")

# These tables are present in every supported control-plane schema.  Newer
# tables can be requested with --required-table without changing this tool.
DEFAULT_REQUIRED_COLUMNS = {
    "previews": {"id", "service", "action", "confirmation_hash", "created_at"},
    "tasks": {"id", "service", "action", "state", "snapshot_json", "created_at"},
    "events": {"sequence", "task_id", "occurred_at", "level", "message", "data_json"},
    "audit_entries": {"sequence", "occurred_at", "actor_hash", "event", "resource", "outcome"},
    "metadata": {"key", "value"},
}


class SnapshotError(RuntimeError):
    """Raised when a snapshot or validation cannot be completed safely."""


def _regular_file(path: Path, label: str) -> Path:
    """Return an absolute regular path, rejecting symlinks before resolving."""

    try:
        info = path.lstat()
    except FileNotFoundError as error:
        raise SnapshotError(f"{label} 不存在: {path}") from error
    if not stat.S_ISREG(info.st_mode) or path.is_symlink():
        raise SnapshotError(f"{label} 必须是非符号链接普通文件: {path}")
    return path.absolute()


def _directory(path: Path, label: str, create: bool = True) -> Path:
    """Validate or create a private directory without following a symlink."""

    if path.exists() or path.is_symlink():
        if path.is_symlink() or not path.is_dir():
            raise SnapshotError(f"{label} 必须是真实目录: {path}")
    elif create:
        path.mkdir(parents=True, mode=0o700)
    else:
        raise SnapshotError(f"{label} 不存在: {path}")
    os.chmod(path, 0o700)
    return path.absolute()


def _identifier(value: str, label: str) -> str:
    if not IDENTIFIER.fullmatch(value):
        raise SnapshotError(f"{label} 不是安全的 SQLite 标识符: {value!r}")
    return value


def _quoted_identifier(value: str) -> str:
    return '"' + _identifier(value, "SQLite 标识符") + '"'


def _read_only_connection(path: Path) -> sqlite3.Connection:
    # URI mode=ro prevents accidental writes even if a future check adds a
    # mutating pragma.  Do not use immutable mode for a live source database:
    # SQLite may need to read its WAL while taking the consistent backup.
    uri = "file:" + quote(str(path.absolute()), safe="/") + "?mode=ro"
    try:
        connection = sqlite3.connect(uri, uri=True)
        connection.execute("PRAGMA query_only = ON")
        connection.execute("PRAGMA foreign_keys = ON")
        return connection
    except sqlite3.DatabaseError as error:
        raise SnapshotError(f"无法以只读模式打开 SQLite: {path}: {error}") from error


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _parse_required(values: Optional[Sequence[str]]) -> Dict[str, Set[str]]:
    required: Dict[str, Set[str]] = {
        table: set(columns) for table, columns in DEFAULT_REQUIRED_COLUMNS.items()
    }
    for raw in values or ():
        table, separator, columns_text = raw.partition(":")
        table = _identifier(table, "表名")
        if separator:
            columns = {
                _identifier(column, "列名")
                for column in columns_text.split(",")
                if column
            }
            if not columns:
                raise SnapshotError(f"--required-table 的列列表为空: {raw!r}")
            required.setdefault(table, set()).update(columns)
        else:
            required.setdefault(table, set())
    return required


def inspect_database(path: Path, required: Mapping[str, Set[str]]) -> Dict[str, object]:
    """Run read-only integrity, FK, schema and row-count checks."""

    path = _regular_file(path, "SQLite 文件")
    connection = _read_only_connection(path)
    try:
        row = connection.execute("PRAGMA user_version").fetchone()
        schema_version = int(row[0]) if row else 0
        integrity = connection.execute("PRAGMA integrity_check").fetchall()
        if integrity != [("ok",)]:
            detail = "; ".join(str(item[0]) for item in integrity[:5])
            raise SnapshotError(f"SQLite integrity_check 失败: {detail}")
        foreign_keys = connection.execute("PRAGMA foreign_key_check").fetchmany(1)
        if foreign_keys:
            raise SnapshotError("SQLite foreign_key_check 发现不一致")

        tables = {
            str(item[0])
            for item in connection.execute(
                "SELECT name FROM sqlite_master WHERE type = 'table'"
            )
            if not str(item[0]).startswith("sqlite_")
        }
        missing_tables = sorted(set(required) - tables)
        if missing_tables:
            raise SnapshotError("SQLite 缺少关键表: " + ", ".join(missing_tables))

        counts: Dict[str, int] = {}
        for table, columns in sorted(required.items()):
            actual_columns = {
                str(item[1])
                for item in connection.execute(
                    "PRAGMA table_info(" + _quoted_identifier(table) + ")"
                )
            }
            missing_columns = sorted(set(columns) - actual_columns)
            if missing_columns:
                raise SnapshotError(
                    f"SQLite 表 {table} 缺少关键列: {', '.join(missing_columns)}"
                )
            count_row = connection.execute(
                "SELECT COUNT(*) FROM " + _quoted_identifier(table)
            ).fetchone()
            if not count_row or not isinstance(count_row[0], int) or count_row[0] < 0:
                raise SnapshotError(f"SQLite 表 {table} 行数无效")
            counts[table] = int(count_row[0])
        return {
            "path": str(path),
            "schema_version": schema_version,
            "tables": counts,
            "sha256": _sha256(path),
            "size_bytes": path.stat().st_size,
        }
    finally:
        connection.close()


def _new_destination(output: Optional[str], output_dir: Optional[str], source: Path) -> Path:
    if bool(output) == bool(output_dir):
        raise SnapshotError("必须且只能指定 --output 或 --output-dir；不会使用隐式生产路径")
    if output:
        destination = Path(output).absolute()
    else:
        directory = _directory(Path(output_dir), "快照输出目录")
        destination = directory / (
            "ops-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + ".db"
        )
    if destination == source.absolute() or destination.resolve() == source.resolve():
        raise SnapshotError("拒绝把生产 SQLite 当作快照输出；目标必须是新文件")
    parent = _directory(destination.parent, "快照父目录")
    destination = parent / destination.name
    # O_EXCL below is the final race-safe guard.  This early check produces a
    # useful error and, importantly, never permits replacing an existing file.
    if destination.exists() or destination.is_symlink():
        raise SnapshotError(f"快照目标已存在，拒绝覆盖: {destination}")
    return destination


def create_snapshot(
    source: Path,
    output: Optional[str],
    output_dir: Optional[str],
    required: Mapping[str, Set[str]],
) -> Dict[str, object]:
    if bool(output) == bool(output_dir):
        raise SnapshotError("必须且只能指定 --output 或 --output-dir；不会使用隐式生产路径")
    source = _regular_file(source, "生产 SQLite")
    destination = _new_destination(output, output_dir, source)
    source_report = inspect_database(source, required)
    created = False
    try:
        descriptor = os.open(
            str(destination), os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600
        )
        os.close(descriptor)
        created = True
        source_connection = _read_only_connection(source)
        try:
            destination_connection = sqlite3.connect(str(destination))
            try:
                source_connection.backup(destination_connection)
                destination_connection.commit()
            finally:
                destination_connection.close()
        finally:
            source_connection.close()
        os.chmod(destination, 0o600)
        snapshot_report = inspect_database(destination, required)
    except (OSError, sqlite3.DatabaseError, SnapshotError) as error:
        if created:
            try:
                destination.unlink()
            except FileNotFoundError:
                pass
        if isinstance(error, SnapshotError):
            raise
        raise SnapshotError(f"创建 SQLite 快照失败: {error}") from error
    snapshot_report["source_sha256"] = source_report["sha256"]
    snapshot_report["source"] = str(source)
    snapshot_report["snapshot"] = str(destination)
    return snapshot_report


def verify_snapshot(path: Path, required: Mapping[str, Set[str]]) -> Dict[str, object]:
    report = inspect_database(path, required)
    report["snapshot"] = str(path.absolute())
    return report


def restore_check(
    snapshot: Path,
    work_root: Optional[str],
    required: Mapping[str, Set[str]],
) -> Dict[str, object]:
    source = _regular_file(snapshot, "SQLite 快照")
    if work_root:
        root = _directory(Path(work_root), "隔离校验工作目录")
    else:
        root = None
    temporary = tempfile.mkdtemp(prefix="areasong-ops-restore-check-", dir=str(root) if root else None)
    try:
        isolated = Path(temporary) / "ops.db"
        shutil.copyfile(str(source), str(isolated))
        os.chmod(isolated, 0o600)
        report = inspect_database(isolated, required)
        report["snapshot"] = str(source)
        report["isolated_copy"] = True
        return report
    finally:
        shutil.rmtree(temporary, ignore_errors=False)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="AreaSong Ops SQLite 快照与隔离恢复校验（默认不执行任何写入）"
    )
    subparsers = parser.add_subparsers(dest="command")

    snapshot = subparsers.add_parser("snapshot", help="从活跃 DB 创建一个全新快照")
    snapshot.add_argument("--source", "--db", default=DEFAULT_SOURCE)
    snapshot.add_argument("--output", "--destination")
    snapshot.add_argument("--output-dir")
    snapshot.add_argument("--required-table", action="append", default=[])
    snapshot.add_argument("--json", action="store_true")

    for name, help_text in (
        ("verify", "只读验证指定快照"),
        ("restore-check", "复制到临时目录后只读验证，不接触生产 DB"),
    ):
        check = subparsers.add_parser(name, help=help_text)
        check.add_argument("--snapshot", "--input", "--source", required=True)
        check.add_argument("--required-table", action="append", default=[])
        check.add_argument("--work-root")
        check.add_argument("--json", action="store_true")
    return parser


def _print_report(report: Mapping[str, object], as_json: bool) -> None:
    if as_json:
        print(json.dumps(report, ensure_ascii=False, sort_keys=True))
        return
    tables = report.get("tables", {})
    table_text = " ".join(f"{name}={count}" for name, count in sorted(dict(tables).items()))
    location = report.get("snapshot") or report.get("path")
    print(
        "AreaSong Ops SQLite 校验成功: "
        f"path={location} schema={report.get('schema_version')} {table_text}"
    )


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = _parser()
    args = parser.parse_args(argv)
    if not args.command:
        parser.print_help(sys.stderr)
        return 2
    try:
        required = _parse_required(args.required_table)
        if args.command == "snapshot":
            report = create_snapshot(
                Path(args.source), args.output, args.output_dir, required
            )
        elif args.command == "verify":
            report = verify_snapshot(Path(args.snapshot), required)
        else:
            report = restore_check(Path(args.snapshot), args.work_root, required)
        _print_report(report, bool(args.json))
        return 0
    except (OSError, SnapshotError, sqlite3.DatabaseError, ValueError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
