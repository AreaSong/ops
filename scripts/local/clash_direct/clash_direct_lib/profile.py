from __future__ import annotations

import hashlib
import os
import re
import stat
from dataclasses import dataclass
from pathlib import Path

from .errors import ToolError


DEFAULT_APP_DIR = Path.home() / "Library/Application Support/io.github.clash-verge-rev.clash-verge-rev"
UID = re.compile(r"[A-Za-z0-9_-]+\Z", re.ASCII)
MAX_SCRIPT_BYTES = 1024 * 1024


def digest(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


def read_regular(path: Path, limit: int = MAX_SCRIPT_BYTES) -> tuple[bytes, os.stat_result]:
    try:
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise ToolError("unsafe_path", "目标必须是普通、非链接文件。")
        if info.st_uid != os.getuid():
            raise ToolError("unsafe_owner", "目标不属于当前用户；请勿用 sudo 运行。")
        if info.st_size > limit:
            raise ToolError("too_large", "配置文件过大，拒绝自动处理。")
        flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
        with os.fdopen(os.open(path, flags), "rb") as stream:
            opened = os.fstat(stream.fileno())
            if (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino):
                raise ToolError("conflict", "读取时文件发生变化，请重试。", exit_code=3)
            content = stream.read(limit + 1)
        if len(content) > limit:
            raise ToolError("too_large", "配置文件过大，拒绝自动处理。")
        return content, info
    except OSError as error:
        raise ToolError("file_unavailable", "无法安全读取目标文件，请检查路径和权限。") from error


@dataclass(frozen=True)
class Target:
    app_dir: Path
    profile_id: str
    script_id: str
    path: Path
    source: str
    sha256: str
    mode: int
    active: bool

    def describe(self) -> dict:
        return {"profile": "sub", "profile_id": self.profile_id,
                "script": str(self.path), "active": self.active}

    def identity(self) -> tuple:
        return self.profile_id, self.script_id, self.path, self.sha256


def load_metadata(app_dir: Path) -> dict:
    try:
        import yaml
    except ImportError as error:
        raise ToolError("dependency_missing", "当前 Python 缺少 PyYAML；不会自动安装依赖。") from error
    raw, _ = read_regular(app_dir / "profiles.yaml", 2 * MAX_SCRIPT_BYTES)
    try:
        metadata = yaml.safe_load(raw)
    except (yaml.YAMLError, UnicodeError) as error:
        raise ToolError("invalid_profile", "profiles.yaml 无法安全解析。") from error
    if not isinstance(metadata, dict) or not isinstance(metadata.get("items"), list):
        raise ToolError("invalid_profile", "profiles.yaml 结构不受支持。")
    if not all(isinstance(item, dict) for item in metadata["items"]):
        raise ToolError("invalid_profile", "订阅条目结构不受支持。")
    return metadata


def select_profile(metadata: dict) -> tuple[dict, dict]:
    items = metadata["items"]
    identifiers = [item.get("uid") for item in items]
    if not all(isinstance(uid, str) and UID.fullmatch(uid) for uid in identifiers) or len(set(identifiers)) != len(identifiers):
        raise ToolError("invalid_profile", "订阅标识缺失或重复，拒绝猜测目标。")
    profiles = [item for item in items if item.get("name") == "sub" and item.get("type") in {"remote", "local"}]
    if len(profiles) != 1:
        raise ToolError("ambiguous_profile", "必须恰好存在一个名为 sub 的订阅；不会猜测目标。")
    selected = profiles[0]
    uid = selected.get("uid")
    options = selected.get("option")
    script_id = options.get("script") if isinstance(options, dict) else None
    if not isinstance(uid, str) or not UID.fullmatch(uid) or not isinstance(script_id, str) or not UID.fullmatch(script_id):
        raise ToolError("invalid_profile", "sub 的标识或脚本引用无效。")
    scripts = [item for item in items if item.get("uid") == script_id and item.get("type") == "script"]
    references = [item for item in items if item.get("type") in {"remote", "local"}
                  and isinstance(item.get("option"), dict) and item["option"].get("script") == script_id]
    if script_id == "Script" or len(scripts) != 1 or len(references) != 1:
        raise ToolError("shared_script", "不允许修改全局脚本或被多个订阅共享的脚本。")
    return selected, scripts[0]


def load_target(app_dir: Path = DEFAULT_APP_DIR) -> Target:
    try:
        app_dir = app_dir.expanduser().resolve(strict=True)
    except OSError as error:
        raise ToolError("profile_missing", "未找到 Clash Verge 配置目录。") from error
    metadata = load_metadata(app_dir)
    selected, script = select_profile(metadata)
    filename = script.get("file")
    if (not isinstance(filename, str) or Path(filename).name != filename or "\\" in filename
            or any(ord(char) < 32 or ord(char) == 127 for char in filename)):
        raise ToolError("unsafe_path", "脚本文件名包含不安全路径。")
    if not filename.endswith(".js") or filename == "Script.js":
        raise ToolError("unsafe_path", "不支持该脚本文件。")
    directory = app_dir / "profiles"
    if directory.is_symlink() or not directory.is_dir():
        raise ToolError("unsafe_path", "profiles 必须是实际目录，不能是符号链接。")
    path = directory / filename
    raw, info = read_regular(path)
    check_file_aliases(metadata, script["uid"], directory, info)
    try:
        source = raw.decode("utf-8")
    except UnicodeError as error:
        raise ToolError("encoding", "扩展脚本必须使用 UTF-8。") from error
    return Target(app_dir, selected["uid"], script["uid"], path, source,
                  digest(raw), stat.S_IMODE(info.st_mode), metadata.get("current") == selected["uid"])


def check_file_aliases(metadata: dict, script_id: str, directory: Path, selected: os.stat_result) -> None:
    # 不同脚本标识也可能指向同一文件；仅数 option.script 引用不足以保证隔离。
    for item in metadata["items"]:
        if item.get("type") != "script" or item.get("uid") == script_id:
            continue
        filename = item.get("file")
        if not isinstance(filename, str) or Path(filename).name != filename or "\\" in filename or "\x00" in filename:
            raise ToolError("unsafe_path", "其他脚本路径异常，无法确认 sub 的文件隔离性。")
        try:
            other = (directory / filename).stat()
        except FileNotFoundError:
            continue
        except OSError as error:
            raise ToolError("unsafe_path", "无法确认脚本文件是否被其他入口共享。") from error
        if (other.st_dev, other.st_ino) == (selected.st_dev, selected.st_ino):
            raise ToolError("shared_script", "另一个脚本入口也指向此文件，拒绝修改共享文件。")
