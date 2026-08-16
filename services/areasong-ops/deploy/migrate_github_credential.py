#!/usr/bin/env python3
"""将旧 GitHub Issue 同步凭据安全迁移为 Runner 管理的规范配置。"""

from __future__ import annotations

import argparse
import contextlib
import datetime as dt
import fcntl
import os
import re
import stat
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator


SOURCE_PATH = Path("/etc/ops/alertmanager-github.env")
DESTINATION_PATH = Path("/var/lib/areasong-ops/credentials/alertmanager-github.env")
LEGACY_LOCK_PATH = Path("/run/lock/ops-alertmanager-github-issues.lock")
MANAGED_LOCK_PATH = Path("/var/lib/areasong-ops/run/alertmanager-github-issues.lock")
METRIC_PATH = "/var/lib/node_exporter/textfile_collector/alertmanager-github-issues.prom"
MAX_CREDENTIAL_BYTES = 16 << 10
KEY_PATTERN = re.compile(r"^[A-Z][A-Z0-9_]*$")

LEGACY_KEYS = {
    "ALERTMANAGER_GITHUB_ISSUES_ENABLED",
    "GITHUB_REPOSITORY",
    "GITHUB_TOKEN",
    "GITHUB_TOKEN_EXPIRES_AT",
}
CANONICAL_KEYS = LEGACY_KEYS | {
    "ALERTMANAGER_URL",
    "GITHUB_API_BASE",
    "ALERTMANAGER_GITHUB_METRIC_OUT",
    "ALERTMANAGER_HTTP_TIMEOUT_SECONDS",
}
FIXED_VALUES = {
    "ALERTMANAGER_GITHUB_ISSUES_ENABLED": "true",
    "GITHUB_REPOSITORY": "AreaSong/ops",
    "ALERTMANAGER_URL": "http://127.0.0.1:9093/api/v2/alerts",
    "GITHUB_API_BASE": "https://api.github.com",
    "ALERTMANAGER_GITHUB_METRIC_OUT": METRIC_PATH,
    "ALERTMANAGER_HTTP_TIMEOUT_SECONDS": "15",
}
RENDER_ORDER = (
    "ALERTMANAGER_GITHUB_ISSUES_ENABLED",
    "GITHUB_TOKEN",
    "GITHUB_REPOSITORY",
    "GITHUB_TOKEN_EXPIRES_AT",
    "ALERTMANAGER_URL",
    "GITHUB_API_BASE",
    "ALERTMANAGER_GITHUB_METRIC_OUT",
    "ALERTMANAGER_HTTP_TIMEOUT_SECONDS",
)


class MigrationError(RuntimeError):
    """不包含任何凭据值的安全失败。"""


@dataclass(frozen=True)
class MigrationPaths:
    source: Path = SOURCE_PATH
    destination: Path = DESTINATION_PATH
    legacy_lock: Path = LEGACY_LOCK_PATH
    managed_lock: Path = MANAGED_LOCK_PATH


class CredentialMigrator:
    def __init__(self, paths: MigrationPaths, *, require_root: bool = True) -> None:
        self.paths = paths
        self.require_root = require_root

    def validate_source(self) -> None:
        self._require_root()
        _, content = self._read_and_validate(self.paths.source, allow_legacy=True)
        if not content:
            raise MigrationError("旧凭据文件为空")

    def validate_destination(self) -> None:
        self._require_root()
        values, content = self._read_and_validate(self.paths.destination, allow_legacy=False)
        if content != render_config(values):
            raise MigrationError("目标凭据不是固定顺序的规范 8 键配置")

    def apply(self) -> bool:
        """创建目标时返回 True；目标已完全一致时返回 False。"""
        self._require_root()
        with self._exclusive_locks():
            values, _ = self._read_and_validate(self.paths.source, allow_legacy=True)
            rendered = render_config(values)
            existing = self._read_destination_if_present()
            if existing is not None:
                if existing == rendered:
                    return False
                raise MigrationError("目标凭据已存在且内容不同，拒绝覆盖")
            self._ensure_destination_directory()
            self._atomic_write_new(rendered)
            self.validate_destination()
            return True

    def _require_root(self) -> None:
        if self.require_root and os.geteuid() != 0:
            raise MigrationError("凭据迁移必须由 root 执行")

    def _read_destination_if_present(self) -> bytes | None:
        try:
            values, content = self._read_and_validate(self.paths.destination, allow_legacy=False)
        except FileNotFoundError:
            return None
        if content != render_config(values):
            raise MigrationError("目标凭据已存在但不是规范配置，拒绝覆盖")
        return content

    def _read_and_validate(self, path: Path, *, allow_legacy: bool) -> tuple[dict[str, str], bytes]:
        content = read_secure_file(path, require_root=self.require_root)
        values = parse_config(content)
        validate_values(values, allow_legacy=allow_legacy)
        return values, content

    def _ensure_destination_directory(self) -> None:
        directory = self.paths.destination.parent
        try:
            info = os.lstat(directory)
        except FileNotFoundError:
            os.mkdir(directory, 0o700)
            info = os.lstat(directory)
        if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):
            raise MigrationError("目标凭据目录不是安全的普通目录")
        if stat.S_IMODE(info.st_mode) != 0o700:
            raise MigrationError("目标凭据目录权限必须为 0700")
        if self.require_root and (info.st_uid != 0 or info.st_gid != 0):
            raise MigrationError("目标凭据目录必须由 root:root 拥有")

    def _atomic_write_new(self, content: bytes) -> None:
        directory = self.paths.destination.parent
        descriptor, temporary_name = tempfile.mkstemp(prefix=".credential-", dir=directory)
        temporary = Path(temporary_name)
        try:
            os.fchmod(descriptor, 0o600)
            if self.require_root:
                os.fchown(descriptor, 0, 0)
            with os.fdopen(descriptor, "wb", closefd=True) as stream:
                descriptor = -1
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
            if self.paths.destination.exists() or self.paths.destination.is_symlink():
                raise MigrationError("目标凭据在迁移期间出现，拒绝覆盖")
            os.replace(temporary, self.paths.destination)
            sync_directory(directory)
        finally:
            if descriptor >= 0:
                os.close(descriptor)
            with contextlib.suppress(FileNotFoundError):
                temporary.unlink()

    @contextlib.contextmanager
    def _exclusive_locks(self) -> Iterator[None]:
        descriptors: list[int] = []
        try:
            for path in (self.paths.legacy_lock, self.paths.managed_lock):
                descriptor = open_lock(path, require_root=self.require_root)
                descriptors.append(descriptor)
                try:
                    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError as error:
                    raise MigrationError("GitHub Issue 同步任务正在运行，请稍后重试") from error
            yield
        finally:
            for descriptor in reversed(descriptors):
                with contextlib.suppress(OSError):
                    fcntl.flock(descriptor, fcntl.LOCK_UN)
                os.close(descriptor)


def read_secure_file(path: Path, *, require_root: bool) -> bytes:
    info = os.lstat(path)
    validate_file_metadata(info, path, require_root=require_root)
    flags = os.O_RDONLY | os.O_CLOEXEC | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags)
    try:
        opened = os.fstat(descriptor)
        validate_file_metadata(opened, path, require_root=require_root)
        if (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino):
            raise MigrationError("凭据文件在读取期间发生变化")
        content = os.read(descriptor, MAX_CREDENTIAL_BYTES + 1)
    finally:
        os.close(descriptor)
    if not content or len(content) > MAX_CREDENTIAL_BYTES:
        raise MigrationError("凭据文件大小无效")
    return content


def validate_file_metadata(info: os.stat_result, path: Path, *, require_root: bool) -> None:
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode):
        raise MigrationError(f"凭据文件不是安全的普通文件: {path}")
    if stat.S_IMODE(info.st_mode) != 0o600:
        raise MigrationError(f"凭据文件权限必须为 0600: {path}")
    if require_root and (info.st_uid != 0 or info.st_gid != 0):
        raise MigrationError(f"凭据文件必须由 root:root 拥有: {path}")
    if info.st_size <= 0 or info.st_size > MAX_CREDENTIAL_BYTES:
        raise MigrationError("凭据文件大小无效")


def parse_config(content: bytes) -> dict[str, str]:
    try:
        text = content.decode("utf-8")
    except UnicodeDecodeError as error:
        raise MigrationError("凭据文件不是有效 UTF-8") from error
    values: dict[str, str] = {}
    for line in text.splitlines():
        if not line:
            continue
        key, separator, value = line.partition("=")
        if separator != "=" or not KEY_PATTERN.fullmatch(key) or value != value.strip():
            raise MigrationError("凭据文件包含无效配置行")
        if key in values:
            raise MigrationError("凭据文件包含重复配置项")
        values[key] = value
    return values


def validate_values(values: dict[str, str], *, allow_legacy: bool) -> None:
    allowed_sets = (LEGACY_KEYS, CANONICAL_KEYS) if allow_legacy else (CANONICAL_KEYS,)
    if not any(set(values) == expected for expected in allowed_sets):
        raise MigrationError("凭据文件必须是严格 4 键旧配置或规范 8 键配置")
    for key, expected in FIXED_VALUES.items():
        if key in values and values[key] != expected:
            raise MigrationError(f"凭据固定配置不匹配: {key}")
    token = values["GITHUB_TOKEN"]
    if len(token) < 20 or len(token) > 512 or "\x00" in token:
        raise MigrationError("GitHub Token 格式无效")
    expires_at = values["GITHUB_TOKEN_EXPIRES_AT"]
    try:
        parsed_expiry = dt.date.fromisoformat(expires_at)
    except ValueError as error:
        raise MigrationError("GitHub Token 到期日无效") from error
    if parsed_expiry.isoformat() != expires_at:
        raise MigrationError("GitHub Token 到期日必须使用 YYYY-MM-DD")


def render_config(values: dict[str, str]) -> bytes:
    normalized = {**FIXED_VALUES, **values}
    return ("\n".join(f"{key}={normalized[key]}" for key in RENDER_ORDER) + "\n").encode()


def open_lock(path: Path, *, require_root: bool) -> int:
    parent = os.lstat(path.parent)
    if not stat.S_ISDIR(parent.st_mode) or stat.S_ISLNK(parent.st_mode):
        raise MigrationError("互斥锁目录不安全")
    flags = os.O_RDWR | os.O_CREAT | os.O_CLOEXEC | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags, 0o600)
    info = os.fstat(descriptor)
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o022:
        os.close(descriptor)
        raise MigrationError("互斥锁文件不安全")
    if require_root and (info.st_uid != 0 or info.st_gid != 0):
        os.close(descriptor)
        raise MigrationError("互斥锁文件必须由 root:root 拥有")
    os.fchmod(descriptor, 0o600)
    return descriptor


def sync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY | os.O_CLOEXEC)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="安全迁移 GitHub Issue 同步凭据")
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--validate-source", action="store_true")
    action.add_argument("--apply", action="store_true")
    action.add_argument("--validate-destination", action="store_true")
    parser.add_argument(
        "--destination-path",
        type=Path,
        default=DESTINATION_PATH,
        help=argparse.SUPPRESS,
    )
    args = parser.parse_args()
    if args.destination_path != DESTINATION_PATH and not args.validate_destination:
        parser.error("--destination-path 仅用于只读目标验证")
    return args


def main() -> int:
    args = parse_args()
    migrator = CredentialMigrator(MigrationPaths(destination=args.destination_path))
    try:
        if args.validate_source:
            migrator.validate_source()
            print("source credential: PASS")
        elif args.validate_destination:
            migrator.validate_destination()
            print("destination credential: PASS")
        else:
            created = migrator.apply()
            print("credential migration: CREATED" if created else "credential migration: UNCHANGED")
    except (MigrationError, FileNotFoundError, PermissionError, OSError) as error:
        print(f"credential migration failed: {error}", file=os.sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
