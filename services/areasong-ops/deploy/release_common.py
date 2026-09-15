#!/usr/bin/env python3
"""AreaSong Ops 控制面发布编排器。

这是生产发布的唯一入口。它只接受 schema 2 签名发布制品，按
preflight -> backup -> Runner -> Web -> runtime preflight 的顺序执行，
每一步都写入持久化状态和脱敏审计；失败时只回滚已经发生的组件。

默认路径是生产路径。单元测试可通过 OPS_RELEASE_TEST_MODE=1 使用临时目录，
但测试模式不会自动放宽生产路径或跳过任何制品校验。
"""

from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
import re
import shutil
import sqlite3
import stat
import subprocess
import sys
import tarfile
import tempfile
import uuid
from pathlib import Path
from typing import Any, Iterable


SHA40 = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^sha256:[0-9a-f]{64}$")
VERSION = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(?:[.-][0-9A-Za-z.-]+)?$")
WEB_IMAGE = re.compile(
    r"^ghcr\.io/areasong/areasong-ops-web:([0-9a-f]{40})@(sha256:[0-9a-f]{64})$"
)
SAFE_ID = re.compile(r"^[A-Za-z0-9_-]{1,100}$")


class ReleaseError(RuntimeError):
    """受控发布失败，消息不会包含命令输出或敏感配置。"""


class IsolationError(ReleaseError):
    """无法证明候选进程处于发布隔离区，不能恢复旧快照。"""


def fail(message: str) -> None:
    raise ReleaseError(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def regular_file(path: Path, *, label: str) -> None:
    if not path.is_file() or path.is_symlink() or path.stat().st_nlink != 1:
        fail(f"{label} 不是安全的普通文件: {path}")


def ensure_dir(path: Path, mode: int = 0o700) -> None:
    if path.is_symlink():
        fail(f"目录不能是符号链接: {path}")
    path.mkdir(parents=True, exist_ok=True, mode=mode)
    if not path.is_dir():
        fail(f"不是目录: {path}")


def sync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def atomic_json(path: Path, payload: dict[str, Any]) -> None:
    ensure_dir(path.parent)
    descriptor, name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(name)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        stream.write(json.dumps(payload, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)
    sync_directory(path.parent)


def run(command: list[str], *, env: dict[str, str] | None = None, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    """执行外部命令；调用方只能根据返回码/结构化输出做决策。"""
    try:
        return subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True, check=False, timeout=300)
    except subprocess.TimeoutExpired:
        return subprocess.CompletedProcess(command, 124, "", "command timed out")


def checked(command: list[str], *, env: dict[str, str] | None = None, cwd: Path | None = None, label: str) -> str:
    result = run(command, env=env, cwd=cwd)
    if result.returncode != 0:
        fail(f"{label} 失败 (exit={result.returncode})")
    return result.stdout


def sanitized_container_inspect(raw: str) -> dict[str, Any]:
    """只保留回滚和隔离验收必需的 Docker 字段，绝不持久化 Config.Env。"""
    try:
        records = json.loads(raw)
    except json.JSONDecodeError:
        fail("Web 容器 inspect 不是 JSON")
    if not isinstance(records, list) or len(records) != 1 or not isinstance(records[0], dict):
        fail("Web 容器 inspect 结果不唯一")
    record = records[0]
    config = record.get("Config") if isinstance(record.get("Config"), dict) else {}
    host_config = record.get("HostConfig") if isinstance(record.get("HostConfig"), dict) else {}
    state = record.get("State") if isinstance(record.get("State"), dict) else {}
    mounts: list[dict[str, Any]] = []
    for mount in record.get("Mounts", []):
        if not isinstance(mount, dict):
            continue
        mounts.append(
            {
                "Type": mount.get("Type"),
                "Source": mount.get("Source"),
                "Destination": mount.get("Destination"),
                "RW": mount.get("RW"),
            }
        )
    raw_labels = config.get("Labels") if isinstance(config.get("Labels"), dict) else {}
    labels = {
        key: raw_labels[key]
        for key in ("org.opencontainers.image.revision", "service", "component")
        if key in raw_labels
    }
    health = state.get("Health") if isinstance(state.get("Health"), dict) else None
    health_summary = None
    if health is not None:
        health_summary = {"Status": health.get("Status"), "FailingStreak": health.get("FailingStreak")}
    return {
        "Id": record.get("Id"),
        "Image": record.get("Image"),
        "Config": {
            "Image": config.get("Image"),
            "User": config.get("User"),
            "Labels": labels,
        },
        "HostConfig": {
            "ReadonlyRootfs": host_config.get("ReadonlyRootfs"),
            "NetworkMode": host_config.get("NetworkMode"),
        },
        "State": {"Running": state.get("Running"), "Status": state.get("Status"), "Health": health_summary},
        "Mounts": mounts,
    }


def parse_manifest(path: Path) -> dict[str, Any]:
    regular_file(path, label="发布 manifest")
    try:
        manifest = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        fail(f"发布 manifest 无法解析: {error}")
    if not isinstance(manifest, dict):
        fail("发布 manifest 必须是对象")
    if manifest.get("schemaVersion") != 2 or manifest.get("service") != "areasong-ops":
        fail("发布 manifest schema/service 不符合合同")
    version = manifest.get("version")
    revision = manifest.get("revision")
    platform = manifest.get("platform")
    if not isinstance(version, str) or not VERSION.fullmatch(version):
        fail("发布版本号无效")
    if not isinstance(revision, str) or not SHA40.fullmatch(revision):
        fail("发布 revision 必须是 40 位小写 SHA")
    if platform != "linux/amd64":
        fail("只允许 linux/amd64 发布制品")
    web = manifest.get("web")
    runner = manifest.get("runner")
    if not isinstance(web, dict) or not isinstance(runner, dict):
        fail("发布 manifest 缺少 web/runner")
    image = web.get("image")
    if not isinstance(image, str):
        fail("Web image 缺失")
    image_match = WEB_IMAGE.fullmatch(image)
    if not image_match or image_match.group(1) != revision or web.get("cosign") != "keyless":
        fail("Web image 必须绑定 revision、sha256 digest 和 keyless 签名")
    archive = runner.get("archive")
    runner_digest = runner.get("sha256")
    expected_archive = f"areasong-ops-runner-{revision}-linux-amd64.tar.gz"
    if archive != expected_archive or runner.get("cosign") != "keyless" or not isinstance(runner_digest, str) or not SHA256.fullmatch(runner_digest):
        fail("Runner archive/digest 未绑定批准 revision")
    return {
        "version": version,
        "revision": revision,
        "web_image": image,
        "web_digest": image_match.group(2),
        "runner_archive": archive,
        "runner_digest": runner_digest,
        "manifest_sha256": sha256_file(path),
    }


def verify_assets(manifest: Path, archive: Path, checksum: Path, bundle: Path, verifier: Path) -> dict[str, Any]:
    metadata = parse_manifest(manifest)
    regular_file(archive, label="Runner archive")
    regular_file(checksum, label="Runner checksum")
    regular_file(bundle, label="Runner Sigstore bundle")
    if archive.name != metadata["runner_archive"]:
        fail("Runner archive 文件名与 manifest 不一致")
    expected_line = f"{metadata['runner_digest'][7:]}  {archive.name}\n"
    if checksum.read_text(encoding="utf-8") != expected_line:
        fail("Runner checksum 必须是绑定 basename 的规范两空格格式")
    if sha256_file(archive) != metadata["runner_digest"][7:]:
        fail("Runner archive SHA-256 与 manifest 不一致")
    regular_file(verifier, label="制品验证器")
    result = run([str(verifier), str(manifest), str(archive), str(checksum), str(bundle)])
    if result.returncode != 0:
        fail("签名制品验证失败")
    return metadata


def safe_extract_runner(archive: Path, destination: Path) -> tuple[Path, Path]:
    ensure_dir(destination)
    allowed = {"areasong-ops-runner", "areasong-ops-runner-updater"}
    try:
        with tarfile.open(archive, "r:gz") as bundle:
            members = bundle.getmembers()
            names = {member.name for member in members}
            if names != allowed or len(members) != len(allowed) or any(not member.isfile() for member in members):
                fail("Runner archive 必须只包含两个普通文件")
            for member in members:
                target = (destination / member.name).resolve()
                if destination.resolve() not in target.parents:
                    fail("Runner archive 存在路径穿越")
                with bundle.extractfile(member) as source, target.open("xb") as output:
                    shutil.copyfileobj(source, output)
                    output.flush()
                    os.fsync(output.fileno())
    except (OSError, tarfile.TarError) as error:
        fail(f"Runner archive 无法安全解包: {error}")
    for name in allowed:
        regular_file(destination / name, label="Runner 制品")
        os.chmod(destination / name, 0o755)
    sync_directory(destination)
    return destination / "areasong-ops-runner", destination / "areasong-ops-runner-updater"


def copy_atomic(source: Path, target: Path, mode: int | None = None) -> None:
    regular_file(source, label="备份源")
    if target.is_symlink() or (target.exists() and target.stat().st_nlink != 1):
        fail(f"目标不能是符号链接: {target}")
    ensure_dir(target.parent)
    descriptor, name = tempfile.mkstemp(prefix=f".{target.name}.", dir=target.parent)
    temporary = Path(name)
    try:
        with os.fdopen(descriptor, "wb") as output, source.open("rb") as input_file:
            shutil.copyfileobj(input_file, output)
            output.flush()
            os.fchmod(output.fileno(), mode if mode is not None else stat.S_IMODE(source.stat().st_mode))
            os.fsync(output.fileno())
        os.replace(temporary, target)
        sync_directory(target.parent)
    finally:
        temporary.unlink(missing_ok=True)


def snapshot_sqlite(source: Path, target: Path) -> None:
    regular_file(source, label="SQLite 数据库")
    ensure_dir(target.parent)
    descriptor, name = tempfile.mkstemp(prefix=f".{target.name}.", dir=target.parent)
    os.close(descriptor)
    temporary = Path(name)
    source_db = destination_db = None
    try:
        source_db = sqlite3.connect(source.resolve().as_uri() + "?mode=ro", uri=True, timeout=5)
        destination_db = sqlite3.connect(temporary, timeout=5)
        with destination_db:
            source_db.backup(destination_db)
        destination_db.execute("PRAGMA journal_mode=DELETE")
        if destination_db.execute("PRAGMA quick_check").fetchall() != [("ok",)]:
            fail("SQLite 快照完整性校验失败")
        if destination_db.execute("PRAGMA foreign_key_check").fetchone() is not None:
            fail("SQLite 快照外键校验失败")
        source_db.close()
        destination_db.close()
        os.chmod(temporary, 0o600)
        os.replace(temporary, target)
        sync_directory(target.parent)
    except (OSError, sqlite3.Error) as error:
        if temporary.exists():
            temporary.unlink()
        fail(f"SQLite snapshot 失败: {error}")
    finally:
        if source_db is not None:
            source_db.close()
        if destination_db is not None:
            destination_db.close()
        temporary.unlink(missing_ok=True)


def read_env(path: Path) -> dict[str, str]:
    regular_file(path, label="运行环境文件")
    values: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key in values:
            fail(f"环境文件包含重复键: {key}")
        values[key] = value
    return values


def update_runtime_env(path: Path, version: str, revision: str) -> None:
    values = read_env(path)
    lines = path.read_text(encoding="utf-8").splitlines()
    replacements = {"OPS_BUILD_VERSION": version, "OPS_BUILD_REVISION": revision}
    seen: set[str] = set()
    output: list[str] = []
    for line in lines:
        key = line.split("=", 1)[0].strip() if "=" in line and not line.lstrip().startswith("#") else ""
        if key in replacements:
            output.append(f"{key}={replacements[key]}")
            seen.add(key)
        else:
            output.append(line)
    for key, value in replacements.items():
        if key not in seen:
            output.append(f"{key}={value}")
    descriptor, name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            stream.write("\n".join(output) + "\n")
            stream.flush()
            os.fchmod(stream.fileno(), path.stat().st_mode & 0o777)
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        sync_directory(path.parent)
    finally:
        temporary.unlink(missing_ok=True)


class State:
    def __init__(self, root: Path, deployment_id: str, metadata: dict[str, Any], *, create: bool) -> None:
        self.root = root
        self.deployment_id = deployment_id
        self.directory = root / "deployments" / deployment_id
        self.path = self.directory / "state.json"
        self.audit_path = self.directory / "audit.jsonl"
        if self.path.exists():
            self.data = json.loads(self.path.read_text(encoding="utf-8"))
            if self.data.get("input", {}).get("manifest_sha256") != metadata.get("manifest_sha256"):
                fail("deployment ID 已存在但制品摘要不同，拒绝重放")
        elif create:
            ensure_dir(self.root)
            ensure_dir(self.directory.parent)
            try:
                self.directory.mkdir(mode=0o700)
            except FileExistsError:
                fail("部署目录已有未完成材料，拒绝覆盖")
            ensure_dir(self.directory / "backup")
            self.data = {
                "schemaVersion": 2,
                "activationStarted": False,
                "deploymentId": deployment_id,
                "status": "planned",
                "phase": "plan",
                "createdAt": dt.datetime.now(dt.timezone.utc).isoformat(),
                "input": metadata,
                "steps": {},
                "changed": {"runner": False, "web": False},
                "rollback": {"status": "not_started"},
            }
            self.save()
        else:
            fail(f"deployment 不存在: {deployment_id}")

    def save(self) -> None:
        atomic_json(self.path, self.data)

    def event(self, event: str, **fields: Any) -> None:
        safe = {"timestamp": dt.datetime.now(dt.timezone.utc).isoformat(), "deploymentId": self.deployment_id, "event": event}
        safe.update({key: value for key, value in fields.items() if key not in {"secret", "output", "command"}})
        ensure_dir(self.directory)
        with self.audit_path.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(safe, ensure_ascii=False, sort_keys=True) + "\n")
            stream.flush()
            os.fsync(stream.fileno())

    def step(self, name: str, status: str, **fields: Any) -> None:
        item = self.data.setdefault("steps", {}).setdefault(name, {})
        item.update(fields)
        item["status"] = status
        item["updatedAt"] = dt.datetime.now(dt.timezone.utc).isoformat()
        self.save()
        self.event("step", step=name, status=status, **fields)


def deployment_id() -> str:
    timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    return f"ops-{timestamp}-{uuid.uuid4().hex}"
