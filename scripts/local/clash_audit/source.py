"""仅读取本地来源；不导入规则管理器，不执行 JavaScript。"""
from __future__ import annotations

import json
import os
import re
import stat
from pathlib import Path


DEFAULT_DIR = Path.home() / "Library/Application Support/io.github.clash-verge-rev.clash-verge-rev"


class AuditError(Exception):
    def __init__(self, code, message):
        self.code, self.message = code, message
        super().__init__(message)


def read_text(path):
    try:
        flags = os.O_RDONLY | os.O_NONBLOCK | getattr(os, "O_NOFOLLOW", 0)
        with os.fdopen(os.open(path, flags), "rb") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size > 16 * 1024 * 1024:
                raise AuditError("file_type", "所需文件非普通文件或超过 16 MiB。")
            raw = stream.read(16 * 1024 * 1024 + 1)
        if len(raw) > 16 * 1024 * 1024:
            raise AuditError("file_size", "所需文件超过读取上限。")
        return raw.decode("utf-8-sig")
    except (OSError, UnicodeError):
        raise AuditError("file_unavailable", "所需文件缺失、不可读、为链接或编码无效。") from None


def read_yaml(path):
    try:
        import yaml
    except ImportError:
        raise AuditError("dependency", "当前 Python 缺少 PyYAML；不会安装依赖。") from None

    class UniqueLoader(yaml.SafeLoader):
        pass

    def mapping(loader, node):
        pairs = loader.construct_pairs(node, deep=True)
        result = {}
        for key, value in pairs:
            if not isinstance(key, str) or key in result:
                raise AuditError("yaml_keys", "YAML 存在重复或非字符串键，无法可靠判断顺序。")
            result[key] = value
        return result

    UniqueLoader.add_constructor("tag:yaml.org,2002:map", mapping)
    raw = read_text(path)
    try:
        value = yaml.load(raw, Loader=UniqueLoader)
    except (yaml.YAMLError, ValueError, TypeError, RecursionError):
        raise AuditError("yaml_parse", "YAML 解析失败；已隐藏原文与解析器异常。") from None
    if not isinstance(value, dict):
        raise AuditError("yaml_shape", "所需 YAML 顶层必须是映射。")
    return value


def strings(value):
    return isinstance(value, list) and all(isinstance(x, str) and x for x in value)


def profile_file(root, item):
    name = item.get("file")
    if (not isinstance(name, str) or not name or Path(name).name != name
            or name in {".", ".."} or "\\" in name or any(ord(x) < 32 for x in name)):
        raise AuditError("profile_path", "索引中的文件名无效，拒绝猜测路径。")
    directory = root / "profiles"
    if directory.is_symlink():
        raise AuditError("profile_path", "profiles 目录不能是符号链接。")
    return directory / name


def select_sources(root):
    index = read_yaml(root / "profiles.yaml")
    items = index.get("items")
    if not isinstance(items, list) or not all(isinstance(x, dict) for x in items):
        raise AuditError("index_shape", "订阅索引 items 结构无效。")
    uids = [x.get("uid") for x in items]
    if not all(isinstance(x, str) and x for x in uids) or len(set(uids)) != len(uids):
        raise AuditError("index_uid", "订阅索引标识缺失或重复。")
    subs = [x for x in items if x.get("name") == "sub" and x.get("type") in {"remote", "local"}]
    if len(subs) != 1:
        raise AuditError("target_unique", "必须存在且仅存在一个名为 sub 的订阅。")
    sub = subs[0]
    if index.get("current") not in uids:
        raise AuditError("current_invalid", "当前启用订阅的索引引用无效。")
    options = sub.get("option")
    if not isinstance(options, dict):
        raise AuditError("source_links", "sub 缺少扩展引用。")
    linked = {}
    for role in ("script", "merge", "rules", "proxies", "groups"):
        matches = [x for x in items if x.get("uid") == options.get(role) and x.get("type") == role]
        if len(matches) != 1:
            raise AuditError("source_links", "sub 的扩展引用缺失、类型错误或不唯一。")
        linked[role] = profile_file(root, matches[0])
    return sub, index["current"] == sub["uid"], linked


def manual_rules(source, uid):
    begin, end = "/* clash-direct:managed:begin v1 */", "/* clash-direct:managed:end v1 */"
    declaration, data_end = "const __clash_direct_data__ = ", "\n/* clash-direct:data:end */"
    markers = (begin, end, declaration, data_end)
    if any(source.count(x) != 1 for x in markers):
        raise AuditError("manual_data", "手动直连管理区缺失或无法唯一识别。")
    start = source.index(declaration) + len(declaration)
    finish = source.index(data_end)
    if not source.index(begin) < start < finish < source.index(end):
        raise AuditError("manual_data", "手动直连管理区边界错误。")
    try:
        encoded = source[start:finish].strip()
        if not encoded.endswith(";"):
            raise ValueError()
        data = json.loads(encoded[:-1])
    except (ValueError, RecursionError):
        raise AuditError("manual_data", "手动直连数据解析失败；未执行脚本。") from None
    if (not isinstance(data, dict) or type(data.get("schema")) is not int
            or data["schema"] != 1 or data.get("profile_id") != uid or not strings(data.get("rules"))):
        raise AuditError("manual_data", "手动直连数据版本、订阅归属或规则结构无效。")
    if any(not re.fullmatch(r"(?:DOMAIN|DOMAIN-SUFFIX|IP-CIDR|IP-CIDR6),[^,]+,DIRECT(?:,no-resolve)?", x)
           for x in data["rules"]):
        raise AuditError("manual_data", "手动直连包含无法识别的规则。")
    return data["rules"]


def load_sources(root):
    sub, active, linked = select_sources(root)
    script = read_text(linked["script"])
    if not script.strip():
        raise AuditError("script_empty", "关联脚本为空。")
    source = read_yaml(profile_file(root, sub))
    extensions = {role: read_yaml(path) for role, path in linked.items() if role != "script"}
    for role in ("rules", "proxies", "groups"):
        value = extensions[role]
        if any(not isinstance(value.get(k), list) for k in ("prepend", "append", "delete")):
            raise AuditError("extension_shape", "规则、节点或组扩展的增删列表结构无效。")
    generated = read_yaml(root / "clash-verge.yaml")
    backup_source = []
    if extensions["proxies"]["append"]:
        index = read_yaml(root / "profiles.yaml")
        eix = [x for x in index["items"] if x.get("name") == "E-IX" and x.get("type") in {"local", "remote"}]
        if len(eix) == 1:
            values = read_yaml(profile_file(root, eix[0])).get("proxies", [])
            if isinstance(values, list):
                backup_source = [x for x in values if isinstance(x, dict) and x.get("name") == "🇸🇬 Singapore 01"]
    return {"active": active, "source": source, "extensions": extensions, "backup_source": backup_source,
            "script": script, "uid": sub["uid"], "generated": generated}
