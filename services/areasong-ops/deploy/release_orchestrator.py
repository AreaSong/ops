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
SAFE_ID = re.compile(r"^[A-Za-z0-9_.:-]{1,160}$")


class ReleaseError(RuntimeError):
    """受控发布失败，消息不会包含命令输出或敏感配置。"""


def fail(message: str) -> None:
    raise ReleaseError(message)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def regular_file(path: Path, *, label: str) -> None:
    if not path.is_file() or path.is_symlink():
        fail(f"{label} 不是安全的普通文件: {path}")


def ensure_dir(path: Path, mode: int = 0o700) -> None:
    path.mkdir(parents=True, exist_ok=True)
    if path.is_symlink():
        fail(f"目录不能是符号链接: {path}")
    os.chmod(path, mode)


def atomic_json(path: Path, payload: dict[str, Any]) -> None:
    ensure_dir(path.parent)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    with temporary.open("w", encoding="utf-8") as stream:
        stream.write(json.dumps(payload, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)


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
            if names != allowed or any(not member.isfile() for member in members):
                fail("Runner archive 必须只包含两个普通文件")
            for member in members:
                target = (destination / member.name).resolve()
                if destination.resolve() not in target.parents:
                    fail("Runner archive 存在路径穿越")
                bundle.extract(member, destination)
    except (OSError, tarfile.TarError) as error:
        fail(f"Runner archive 无法安全解包: {error}")
    for name in allowed:
        regular_file(destination / name, label="Runner 制品")
        os.chmod(destination / name, 0o755)
    return destination / "areasong-ops-runner", destination / "areasong-ops-runner-updater"


def copy_atomic(source: Path, target: Path, mode: int | None = None) -> None:
    regular_file(source, label="备份源")
    if target.exists() and target.is_symlink():
        fail(f"目标不能是符号链接: {target}")
    ensure_dir(target.parent)
    temporary = target.with_name(f".{target.name}.{os.getpid()}.tmp")
    shutil.copy2(source, temporary)
    if mode is not None:
        os.chmod(temporary, mode)
    os.replace(temporary, target)


def snapshot_sqlite(source: Path, target: Path) -> None:
    regular_file(source, label="SQLite 数据库")
    ensure_dir(target.parent)
    temporary = target.with_name(f".{target.name}.{os.getpid()}.tmp")
    try:
        source_db = sqlite3.connect(f"file:{source}?mode=ro", uri=True)
        destination_db = sqlite3.connect(temporary)
        with destination_db:
            source_db.backup(destination_db)
        source_db.close()
        destination_db.close()
        os.chmod(temporary, 0o600)
        os.replace(temporary, target)
    except (OSError, sqlite3.Error) as error:
        if temporary.exists():
            temporary.unlink()
        fail(f"SQLite snapshot 失败: {error}")


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
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    temporary.write_text("\n".join(output) + "\n", encoding="utf-8")
    os.chmod(temporary, path.stat().st_mode & 0o777)
    os.replace(temporary, path)


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
            ensure_dir(self.directory)
            ensure_dir(self.directory / "backup")
            self.data = {
                "schemaVersion": 1,
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


class Orchestrator:
    def __init__(self, args: argparse.Namespace, state: State, metadata: dict[str, Any]) -> None:
        self.args = args
        self.state = state
        self.metadata = metadata
        self.repo_root = Path(args.repo_root)
        self.runtime_dir = Path(args.runtime_dir)
        self.config_dir = Path(args.config_dir)
        self.runner_root = Path(args.runner_root)
        self.unit_path = Path(args.unit_path)
        self.updater_unit_path = Path(args.updater_unit_path)
        self.db_path = Path(args.db_path)
        self.container = args.container_name
        self.backup = self.state.directory / "backup"
        self.preflight = Path(args.preflight)

    def validate_source_revision(self) -> None:
        """部署前要求生产源码已处于批准 revision，禁止隐式 checkout/pull。"""
        revision = checked(["git", "-C", str(self.repo_root), "rev-parse", "HEAD"], label="读取生产源码 revision").strip()
        if revision != self.metadata["revision"]:
            fail("生产源码 revision 与批准制品不一致；请先在受控窗口同步源码")

    def preflight_run(self, mode: str) -> None:
        environment = os.environ.copy()
        environment.update(
            {
                "OPS_PREFLIGHT_REPO_ROOT": str(self.repo_root),
                "OPS_PREFLIGHT_RUNTIME_DIR": str(self.runtime_dir),
                "OPS_PREFLIGHT_CONFIG_DIR": str(self.config_dir),
                "OPS_PREFLIGHT_RUNNER_ROOT": str(self.runner_root),
                "OPS_PREFLIGHT_UNIT_PATH": str(self.unit_path),
                "OPS_PREFLIGHT_UPDATER_UNIT_PATH": str(self.updater_unit_path),
            }
        )
        checked([str(self.preflight), mode], env=environment, label=f"preflight {mode}")

    def create_backups(self) -> None:
        if self.state.data.get("steps", {}).get("backup", {}).get("status") == "succeeded":
            return
        if self.backup.exists() and any(self.backup.iterdir()):
            fail("备份目录已有内容但未标记完成，拒绝覆盖")
        ensure_dir(self.backup)
        paths = {
            "runner": self.runner_root / "runner/areasong-ops-runner",
            "runner-updater": self.runner_root / "areasong-ops-runner-updater",
            "runner-unit": self.unit_path,
            "runner-updater-unit": self.updater_unit_path,
            "web-env": self.config_dir / "web.env",
            "compose": self.runtime_dir / "compose.yml",
            "runtime-env": self.runtime_dir / ".env",
        }
        for name, source in paths.items():
            target = self.backup / name
            regular_file(source, label=f"备份 {name}")
            copy_atomic(source, target, 0o600)
        image_inspect = checked(["docker", "inspect", self.container], label="保存 Web image inspect")
        sanitized_inspect = sanitized_container_inspect(image_inspect)
        (self.backup / "web-image-inspect.json").write_text(
            json.dumps(sanitized_inspect, ensure_ascii=False, sort_keys=True, indent=2) + "\n",
            encoding="utf-8",
        )
        os.chmod(self.backup / "web-image-inspect.json", 0o600)
        snapshot_sqlite(self.db_path, self.backup / "ops.db")
        self.state.step("backup", "succeeded", files=sorted([path.name for path in self.backup.iterdir()]))

    def deploy_runner(self) -> None:
        if self.state.data.get("steps", {}).get("runner", {}).get("status") == "succeeded":
            return
        self.state.step("runner", "started")
        staging = Path(tempfile.mkdtemp(prefix="areasong-ops-runner-", dir=str(self.state.directory)))
        try:
            runner, updater = safe_extract_runner(Path(self.args.runner_archive), staging)
            # 从第一次写入开始即标记 changed，任何中途失败都必须尝试恢复。
            self.state.data["changed"]["runner"] = True
            self.state.save()
            copy_atomic(runner, self.runner_root / "runner/areasong-ops-runner", 0o755)
            copy_atomic(updater, self.runner_root / "areasong-ops-runner-updater", 0o755)
            candidate_unit = Path(self.args.candidate_unit)
            candidate_updater_unit = Path(self.args.candidate_updater_unit)
            regular_file(candidate_unit, label="候选 Runner unit")
            regular_file(candidate_updater_unit, label="候选 Runner updater unit")
            copy_atomic(candidate_unit, self.unit_path, 0o644)
            copy_atomic(candidate_updater_unit, self.updater_unit_path, 0o644)
            checked(["systemctl", "daemon-reload"], label="systemd daemon-reload")
            # 若上一次进程已完成重启但尚未来得及写 state，健康检查会让重试幂等。
            if not self.runner_health_matches():
                checked(["systemctl", "restart", "areasong-ops-runner.service"], label="重启 Runner")
            self.preflight_run("installed")
            if not self.runner_health_matches():
                fail("Runner health/revision 校验失败")
            self.state.step("runner", "succeeded", revision=self.metadata["revision"])
        finally:
            shutil.rmtree(staging, ignore_errors=True)

    def runner_health_matches(self) -> bool:
        result = run(["curl", "--max-time", "10", "-fsS", "--unix-socket", str(self.args.socket_path), "http://runner/healthz"])
        if result.returncode != 0:
            return False
        try:
            body = json.loads(result.stdout)
        except json.JSONDecodeError:
            return False
        return body.get("ok") is True and body.get("revision") == self.metadata["revision"]

    def deploy_web(self) -> None:
        if self.state.data.get("steps", {}).get("web", {}).get("status") == "succeeded":
            return
        self.state.step("web", "started")
        image = self.metadata["web_image"]
        # 若上一次在 compose 之后中断，先用运行态检查恢复幂等性，避免再次 recreate。
        if self.web_runtime_ready():
            self.preflight_run("runtime")
            self.state.data["changed"]["web"] = True
            self.state.step("web", "succeeded", revision=self.metadata["revision"], resumed=True)
            return
        checked(["docker", "pull", image], label="拉取固定 Web image")
        inspect = checked(["docker", "image", "inspect", image], label="校验 Web image inspect")
        try:
            image_data = json.loads(inspect)
            if not image_data or not isinstance(image_data[0], dict):
                fail("Web image inspect 为空")
            image_id = image_data[0].get("Id", "")
            if not isinstance(image_id, str) or not image_id.startswith("sha256:"):
                fail("Web image ID 无效")
            repo_digests = image_data[0].get("RepoDigests", [])
            if image not in repo_digests:
                fail("Web image inspect 未证明目标 digest")
        except json.JSONDecodeError:
            fail("Web image inspect 不是 JSON")
        checked(["docker", "tag", image, f"areasong-ops-web:{self.metadata['revision']}"], label="绑定本地 immutable Web tag")
        # 从修改运行 env 这一刻开始，失败必须回滚 Web。
        self.state.data["changed"]["web"] = True
        self.state.save()
        update_runtime_env(self.runtime_dir / ".env", self.metadata["version"], self.metadata["revision"])
        checked(
            [
                "docker",
                "compose",
                "--project-directory",
                str(self.runtime_dir),
                "--env-file",
                str(self.runtime_dir / ".env"),
                "-f",
                str(self.runtime_dir / "compose.yml"),
                "up",
                "-d",
                "--force-recreate",
                "--no-deps",
                "web",
            ],
            label="重建 Web",
        )
        self.preflight_run("runtime")
        if not self.web_health_matches():
            fail("Web health/runtime identity 校验失败")
        self.state.step("web", "succeeded", revision=self.metadata["revision"], image_id=image_id)

    def web_health_matches(self) -> bool:
        result = run(["curl", "--max-time", "10", "-fsS", "http://127.0.0.1:3200/healthz"])
        if result.returncode != 0:
            return False
        try:
            body = json.loads(result.stdout)
        except json.JSONDecodeError:
            return False
        return body.get("ok") is True and body.get("revision") == self.metadata["revision"]

    def web_runtime_ready(self) -> bool:
        result = run(["docker", "inspect", self.container])
        if result.returncode != 0:
            return False
        try:
            items = json.loads(result.stdout)
            item = items[0]
            labels = item.get("Config", {}).get("Labels", {})
            return (
                item.get("State", {}).get("Running") is True
                and labels.get("org.opencontainers.image.revision") == self.metadata["revision"]
                and self.web_health_matches()
            )
        except (IndexError, AttributeError, json.JSONDecodeError):
            return False

    def rollback(self) -> None:
        if not any(self.state.data.get("changed", {}).values()):
            self.state.data["rollback"] = {"status": "not_required"}
            self.state.data["status"] = "failed"
            self.state.data["phase"] = "failed"
            self.state.save()
            self.state.event("rollback_not_required")
            return
        self.state.data["rollback"]["status"] = "started"
        self.state.save()
        self.state.event("rollback_started")
        errors: list[str] = []
        if self.state.data.get("changed", {}).get("web"):
            try:
                for name, target in (("runtime-env", self.runtime_dir / ".env"),):
                    copy_atomic(self.backup / name, target, target.stat().st_mode & 0o777)
                old_image = json.loads((self.backup / "web-image-inspect.json").read_text(encoding="utf-8")).get("Image")
                old_revision = read_env(self.runtime_dir / ".env").get("OPS_BUILD_REVISION", "")
                if isinstance(old_image, str) and old_image.startswith("sha256:") and SHA40.fullmatch(old_revision):
                    checked(["docker", "tag", old_image, f"areasong-ops-web:{old_revision}"], label="恢复旧 Web image")
                checked(
                    [
                        "docker", "compose", "--project-directory", str(self.runtime_dir), "--env-file", str(self.runtime_dir / ".env"),
                        "-f", str(self.runtime_dir / "compose.yml"), "up", "-d", "--force-recreate", "--no-deps", "web",
                    ],
                    label="重建旧 Web",
                )
            except (ReleaseError, OSError, json.JSONDecodeError) as error:
                errors.append(f"web:{error}")
        if self.state.data.get("changed", {}).get("runner"):
            try:
                copy_atomic(self.backup / "runner", self.runner_root / "runner/areasong-ops-runner", 0o755)
                copy_atomic(self.backup / "runner-updater", self.runner_root / "areasong-ops-runner-updater", 0o755)
                copy_atomic(self.backup / "runner-unit", self.unit_path, 0o644)
                copy_atomic(self.backup / "runner-updater-unit", self.updater_unit_path, 0o644)
                checked(["systemctl", "daemon-reload"], label="回滚 Runner daemon-reload")
                checked(["systemctl", "restart", "areasong-ops-runner.service"], label="回滚 Runner")
            except (ReleaseError, OSError) as error:
                errors.append(f"runner:{error}")
        if not errors:
            try:
                # 回滚完成后仍必须证明安装态/运行态合同成立；否则状态只能停在 needs_attention。
                self.preflight_run("installed")
                self.preflight_run("runtime")
            except (ReleaseError, OSError) as error:
                errors.append(f"verification:{error}")
        if errors:
            self.state.data["rollback"] = {"status": "failed", "errors": errors}
            self.state.data["status"] = "needs_attention"
            self.state.save()
            self.state.event("rollback_failed", errors=errors)
            return
        self.state.data["rollback"] = {"status": "succeeded"}
        self.state.data["status"] = "rolled_back"
        self.state.data["phase"] = "rollback"
        self.state.save()
        self.state.event("rollback_succeeded")

    def deploy(self) -> None:
        if self.state.data.get("status") == "succeeded":
            self.state.event("deploy_replayed", revision=self.metadata["revision"])
            return
        if self.state.data.get("status") in {"rolled_back", "needs_attention"}:
            fail("该 deployment 已回滚或需要人工关注，必须创建新的 deployment ID")
        ensure_dir(self.state.root)
        lock_path = self.state.root / ".lock"
        with lock_path.open("a+", encoding="utf-8") as lock:
            try:
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except OSError:
                fail("已有另一个发布正在执行")
            self.state.event("deploy_started", revision=self.metadata["revision"], version=self.metadata["version"])
            self.state.data["status"] = "running"
            self.state.data["phase"] = "preflight"
            self.state.save()
            try:
                self.validate_source_revision()
                self.preflight_run("installed")
                self.state.step("preflight", "succeeded", mode="installed")
                self.create_backups()
                self.state.data["phase"] = "runner"
                self.state.save()
                self.deploy_runner()
                self.state.data["phase"] = "web"
                self.state.save()
                self.deploy_web()
                self.preflight_run("runtime")
                self.state.step("runtime-preflight", "succeeded")
                self.state.data["phase"] = "complete"
                self.state.data["status"] = "succeeded"
                self.state.data["completedAt"] = dt.datetime.now(dt.timezone.utc).isoformat()
                self.state.save()
                self.state.event("deploy_succeeded", revision=self.metadata["revision"])
            except (ReleaseError, OSError) as error:
                self.state.data["status"] = "failed"
                self.state.data["error"] = str(error)
                self.state.data["phase"] = "failed"
                self.state.save()
                self.state.event("deploy_failed", error=str(error))
                self.rollback()
                raise


def fixed_defaults() -> dict[str, str]:
    return {
        "repo_root": os.environ.get("OPS_RELEASE_REPO_ROOT", "/opt/ops"),
        "runtime_dir": os.environ.get("OPS_RELEASE_RUNTIME_DIR", "/opt/services/areasong-ops"),
        "config_dir": os.environ.get("OPS_RELEASE_CONFIG_DIR", "/etc/areasong-ops"),
        "runner_root": os.environ.get("OPS_RELEASE_RUNNER_ROOT", "/usr/local/libexec/areasong-ops"),
        "unit_path": os.environ.get("OPS_RELEASE_UNIT_PATH", "/etc/systemd/system/areasong-ops-runner.service"),
        "updater_unit_path": os.environ.get("OPS_RELEASE_UPDATER_UNIT_PATH", "/etc/systemd/system/areasong-ops-runner-update@.service"),
        "db_path": os.environ.get("OPS_RELEASE_DB_PATH", "/var/lib/areasong-ops/ops.db"),
        "socket_path": os.environ.get("OPS_RELEASE_SOCKET_PATH", "/var/lib/areasong-ops/run/runner.sock"),
        "container_name": os.environ.get("OPS_RELEASE_CONTAINER", "areasong-ops-web"),
        "preflight": os.environ.get("OPS_RELEASE_PREFLIGHT", "/opt/ops/services/areasong-ops/deploy/preflight.sh"),
        "candidate_unit": os.environ.get("OPS_RELEASE_CANDIDATE_UNIT", "/opt/ops/services/areasong-ops/deploy/areasong-ops-runner.service"),
        "candidate_updater_unit": os.environ.get("OPS_RELEASE_CANDIDATE_UPDATER_UNIT", "/opt/ops/services/areasong-ops/deploy/areasong-ops-runner-update@.service"),
    }


def parser() -> argparse.ArgumentParser:
    defaults = fixed_defaults()
    root = argparse.ArgumentParser(description="AreaSong Ops 单一控制面发布入口")
    sub = root.add_subparsers(dest="action", required=True)
    for action in ("plan", "deploy"):
        command = sub.add_parser(action)
        command.add_argument("--manifest", required=True, type=Path)
        command.add_argument("--runner-archive", required=True, type=Path)
        command.add_argument("--checksum", required=True, type=Path)
        command.add_argument("--sigstore-bundle", required=True, type=Path)
        command.add_argument("--verifier", type=Path, default=Path(__file__).with_name("verify-release-assets.sh"))
        command.add_argument("--state-dir", type=Path, default=Path(os.environ.get("OPS_RELEASE_STATE_DIR", "/var/lib/areasong-ops/release-orchestrator")))
        command.add_argument("--deployment-id", default="")
        for name, value in defaults.items():
            option = "--" + name.replace("_", "-")
            command.add_argument(option, dest=name, default=value)
    status = sub.add_parser("status")
    status.add_argument("deployment_id")
    status.add_argument("--state-dir", type=Path, default=Path(os.environ.get("OPS_RELEASE_STATE_DIR", "/var/lib/areasong-ops/release-orchestrator")))
    rollback = sub.add_parser("rollback")
    rollback.add_argument("deployment_id")
    rollback.add_argument("--state-dir", type=Path, default=Path(os.environ.get("OPS_RELEASE_STATE_DIR", "/var/lib/areasong-ops/release-orchestrator")))
    for name, value in defaults.items():
        rollback.add_argument("--" + name.replace("_", "-"), dest=name, default=value)
    return root


def require_root(action: str) -> None:
    if action in {"plan", "deploy", "rollback"} and os.geteuid() != 0 and os.environ.get("OPS_RELEASE_TEST_MODE") != "1":
        fail(f"{action} 必须以 root 执行")


def require_production_paths(args: argparse.Namespace) -> None:
    """非测试模式拒绝通过参数或环境变量把发布改指向任意主机路径。"""
    if os.environ.get("OPS_RELEASE_TEST_MODE") == "1":
        return
    if args.action == "plan":
        if str(args.state_dir) != "/var/lib/areasong-ops/release-orchestrator":
            fail("生产发布 state-dir 必须固定在 /var/lib/areasong-ops/release-orchestrator")
        return
    if args.action not in {"deploy", "rollback"}:
        return
    expected = {
        "repo_root": "/opt/ops",
        "runtime_dir": "/opt/services/areasong-ops",
        "config_dir": "/etc/areasong-ops",
        "runner_root": "/usr/local/libexec/areasong-ops",
        "unit_path": "/etc/systemd/system/areasong-ops-runner.service",
        "updater_unit_path": "/etc/systemd/system/areasong-ops-runner-update@.service",
        "db_path": "/var/lib/areasong-ops/ops.db",
        "socket_path": "/var/lib/areasong-ops/run/runner.sock",
        "container_name": "areasong-ops-web",
        "preflight": "/opt/ops/services/areasong-ops/deploy/preflight.sh",
        "candidate_unit": "/opt/ops/services/areasong-ops/deploy/areasong-ops-runner.service",
        "candidate_updater_unit": "/opt/ops/services/areasong-ops/deploy/areasong-ops-runner-update@.service",
    }
    for name, value in expected.items():
        if str(getattr(args, name)) != value:
            fail(f"生产发布路径固定为 {value}，不允许覆盖 {name}")
    if str(args.state_dir) != "/var/lib/areasong-ops/release-orchestrator":
        fail("生产发布 state-dir 必须固定在 /var/lib/areasong-ops/release-orchestrator")


def prepare_state_root(path: Path, *, strict: bool = False) -> None:
    if strict and path.exists():
        if path.is_symlink():
            fail("生产发布 state-dir 不能是符号链接")
        stat = path.stat()
        if stat.st_uid != 0 or stat.st_mode & 0o777 != 0o700:
            fail("生产发布 state-dir 必须是 root:root 0700")
    ensure_dir(path)
    if not strict or os.environ.get("OPS_RELEASE_TEST_MODE") == "1":
        return
    stat = path.stat()
    if stat.st_uid != 0 or stat.st_mode & 0o777 != 0o700:
        fail("生产发布 state-dir 必须是 root:root 0700")


def main(argv: Iterable[str] | None = None) -> int:
    args = parser().parse_args(list(argv) if argv is not None else None)
    try:
        require_root(args.action)
        require_production_paths(args)
        if args.action == "status":
            path = args.state_dir / "deployments" / args.deployment_id / "state.json"
            regular_file(path, label="deployment state")
            print(path.read_text(encoding="utf-8"), end="")
            return 0
        if args.action == "rollback":
            prepare_state_root(args.state_dir, strict=True)
            path = args.state_dir / "deployments" / args.deployment_id / "state.json"
            if not path.exists():
                fail(f"deployment 不存在: {args.deployment_id}")
            metadata = json.loads(path.read_text(encoding="utf-8"))["input"]
            state = State(args.state_dir, args.deployment_id, metadata, create=False)
            Orchestrator(args, state, metadata).rollback()
            print(json.dumps(state.data, ensure_ascii=False, sort_keys=True))
            return 0
        metadata = verify_assets(args.manifest, args.runner_archive, args.checksum, args.sigstore_bundle, args.verifier)
        deployment = args.deployment_id or deployment_id()
        if not SAFE_ID.fullmatch(deployment):
            fail("deployment ID 格式无效")
        prepare_state_root(args.state_dir, strict=args.action in {"deploy", "rollback"})
        state = State(args.state_dir, deployment, metadata, create=True)
        state.event("plan_created", revision=metadata["revision"], version=metadata["version"])
        if args.action == "plan":
            print(json.dumps({"deploymentId": deployment, "status": state.data["status"], "input": metadata}, ensure_ascii=False, sort_keys=True))
            return 0
        Orchestrator(args, state, metadata).deploy()
        print(json.dumps({"deploymentId": deployment, "status": state.data["status"], "revision": metadata["revision"]}, ensure_ascii=False, sort_keys=True))
        return 0
    except ReleaseError as error:
        print(f"release orchestrator failed: {error}", file=sys.stderr)
        return 1
    except (OSError, json.JSONDecodeError) as error:
        print(f"release orchestrator failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
