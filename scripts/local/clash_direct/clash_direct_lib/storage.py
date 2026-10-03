from __future__ import annotations

import fcntl
import json
import os
import re
import secrets
import stat
import tempfile
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

from .errors import ToolError
from .profile import Target, digest, read_regular
from .rules import validate_rules


RECORD_ID = re.compile(r"\d{8}T\d{12}Z-[0-9a-f]{8}\Z", re.ASCII)


def atomic_write(path: Path, content: bytes, mode: int) -> None:
    descriptor, temporary = tempfile.mkstemp(prefix=".clash-direct-", dir=str(path.parent))
    try:
        with os.fdopen(descriptor, "wb") as stream:
            os.fchmod(stream.fileno(), mode)
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def exclusive_write(path: Path, content: bytes) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(content)
        stream.flush()
        os.fsync(stream.fileno())


def json_bytes(value: dict) -> bytes:
    return (json.dumps(value, ensure_ascii=True, indent=2) + "\n").encode("utf-8")


class StateStore:
    def __init__(self, directory: Path) -> None:
        self.directory = directory.expanduser().absolute()
        if self.directory in {Path.home().absolute(), Path(self.directory.anchor)}:
            raise ToolError("unsafe_state_dir", "请使用独立的备份目录，不能直接使用主目录或根目录。")

    @staticmethod
    def check_private(path: Path) -> None:
        info = path.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ToolError("unsafe_state_dir", "备份目录必须是当前用户所有的真实私有目录（权限 700）。")

    def root(self, profile_id: str, create: bool = False) -> Path:
        paths = [self.directory, self.directory / profile_id]
        for path in paths:
            if not path.exists() and not path.is_symlink():
                if not create:
                    return paths[-1]
                path.mkdir(mode=0o700, parents=True)
            self.check_private(path)
        return paths[-1]

    def read_json(self, path: Path) -> dict:
        try:
            raw, info = read_regular(path, 256 * 1024)
            if info.st_mode & 0o077:
                raise ToolError("unsafe_state_dir", "备份记录权限过宽，拒绝读取。")
            value = json.loads(raw)
        except (ValueError, UnicodeError) as error:
            raise ToolError("invalid_history", "备份记录不是有效 JSON。") from error
        if not isinstance(value, dict):
            raise ToolError("invalid_history", "备份记录结构错误。")
        return value

    def head(self, profile_id: str) -> Optional[str]:
        path = self.root(profile_id) / "head.json"
        if not path.exists() and not path.is_symlink():
            return None
        value = self.read_json(path)
        record_id = value.get("id")
        if value.get("schema") != 1 or (record_id is not None and not self.valid_id(record_id)):
            raise ToolError("invalid_history", "撤销指针无效，拒绝猜测恢复目标。")
        return record_id

    @staticmethod
    def valid_id(value: str) -> bool:
        return isinstance(value, str) and RECORD_ID.fullmatch(value) is not None

    def load_record(self, target: Target, record_id: str) -> dict:
        if not self.valid_id(record_id):
            raise ToolError("invalid_history", "撤销记录标识无效。")
        root = self.root(target.profile_id)
        self.check_private(root / "journal")
        record = self.read_json(root / "journal" / (record_id + ".json"))
        expected = (1, target.profile_id, target.script_id, str(target.path), "committed")
        actual = tuple(record.get(key) for key in ("schema", "profile_id", "script_id", "script_path", "phase"))
        if actual != expected:
            raise ToolError("invalid_history", "撤销记录不属于当前 sub 脚本或尚未完成。")
        if record.get("id") != record_id:
            raise ToolError("invalid_history", "撤销记录标识与文件名不一致。")
        if record.get("parent") is not None and not self.valid_id(record["parent"]):
            raise ToolError("invalid_history", "撤销记录的父指针无效。")
        validate_rules(record.get("before_rules"))
        validate_rules(record.get("after_rules"))
        return record

    @contextmanager
    def lock(self, profile_id: str):
        root = self.root(profile_id, create=True)
        path = root / "writer.lock"
        flags = os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0)
        descriptor = os.open(path, flags, 0o600)
        with os.fdopen(descriptor, "r+b") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1 or info.st_mode & 0o077:
                raise ToolError("unsafe_state_dir", "写锁不是当前用户的私有普通文件。")
            try:
                fcntl.flock(stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise ToolError("conflict", "另一个规则管理进程正在保存，请稍后重试。", exit_code=3) from error
            try:
                yield
            finally:
                fcntl.flock(stream.fileno(), fcntl.LOCK_UN)

    def prepare_record(self, plan) -> dict:
        target = plan.snapshot.target
        root = self.root(target.profile_id, create=True)
        for name in ("snapshots", "journal"):
            path = root / name
            path.mkdir(mode=0o700, exist_ok=True)
            self.check_private(path)
        record_id = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ") + "-" + secrets.token_hex(4)
        backup = root / "snapshots" / (record_id + ".js")
        exclusive_write(backup, target.source.encode("utf-8"))
        record = {
            "schema": 1, "id": record_id, "profile_id": target.profile_id,
            "script_id": target.script_id, "script_path": str(target.path),
            "parent": plan.expected_head, "operation": plan.operation, "phase": "prepared",
            "before_rules": [rule.line for rule in plan.snapshot.document.rules],
            "after_rules": [rule.line for rule in plan.desired],
            "before_sha256": target.sha256, "after_sha256": digest(plan.source.encode("utf-8")),
            "backup": str(backup),
        }
        exclusive_write(root / "journal" / (record_id + ".json"), json_bytes(record))
        return record

    def finish(self, profile_id: str, record: dict, head: Optional[str]) -> None:
        root = self.root(profile_id)
        record = {**record, "phase": "committed"}
        atomic_write(root / "journal" / (record["id"] + ".json"), json_bytes(record), 0o600)
        atomic_write(root / "head.json", json_bytes({"schema": 1, "id": head}), 0o600)
