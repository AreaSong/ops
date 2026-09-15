#!/usr/bin/env python3
"""为指定恢复点采集、验证和暂存完整元数据；不选择 latest、不清理备份。"""

from __future__ import annotations

import argparse
import io
import json
import os
import re
import shutil
import stat
import subprocess
import tarfile
from pathlib import Path

import backup_manifest
from restore_contract import ContractError, UUID_PATTERN, load_contract, sha256_file, validate_contract


CONFIG_MEMBERS = {
    "areaforge": {
        "controlled": "opt/ops/services/areaforge/compose.yml",
        "runtime": "opt/areaforge/docker-compose.prod.yml",
        "env": "opt/areaforge/.env.production",
    },
    "sub2api": {
        "controlled": "opt/ops/services/sub2api/compose.yml",
        "runtime": "opt/services/sub2api/compose.yml",
        "env": "opt/services/sub2api/.env",
    },
}
DATA_ROLES = {
    "areaforge": ["postgres-areaforge", "volume-areaforge-uploads", "volume-areaforge-ops-state"],
    "sub2api": ["postgres-sub2api", "redis", "volume-sub2api-data"],
}
IMAGE_ID = re.compile(r"^sha256:[0-9a-f]{64}$")
SAFE_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$")


def private_regular(path: Path, *, max_size: int = 8 * 1024 * 1024) -> bytes:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise ContractError(f"unsafe metadata source: {path}")
    if info.st_size <= 0 or info.st_size > max_size:
        raise ContractError(f"invalid metadata source size: {path}")
    return path.read_bytes()


def docker_json(*arguments: str) -> object:
    completed = subprocess.run(
        ["docker", *arguments], capture_output=True, text=True, check=True, timeout=30
    )
    return json.loads(completed.stdout)


def container_snapshot(name: str) -> tuple[dict, dict[str, str]]:
    if not SAFE_NAME.fullmatch(name):
        raise ContractError("invalid container name")
    data = docker_json("inspect", name)[0]
    image_id = data.get("Image", "")
    if not IMAGE_ID.fullmatch(image_id):
        raise ContractError("container image must have a fixed image ID")
    image = docker_json("image", "inspect", image_id)[0]
    labels = image.get("Config", {}).get("Labels") or {}
    result = {
        "name": name, "configured_image": data["Config"]["Image"], "image_id": image_id,
        "container_id": data["Id"],
        "version": labels.get("org.opencontainers.image.version", ""),
        "revision": labels.get("org.opencontainers.image.revision", ""),
    }
    # 不持久化完整环境；只返回非敏感的数据库名称供固定只读查询使用。
    environment = dict(value.split("=", 1) for value in data["Config"].get("Env", []) if "=" in value)
    database = {
        "user": environment.get("POSTGRES_USER", "postgres"),
        "database": environment.get("POSTGRES_DB", environment.get("POSTGRES_USER", "postgres")),
    }
    return result, database


def capture_runtime(arguments: argparse.Namespace) -> dict:
    containers = {}
    database = {}
    for role in ("app", "postgres", "redis"):
        name = getattr(arguments, f"{role}_container", None)
        if not name:
            continue
        containers[role], names = container_snapshot(name)
        if role == "postgres":
            database = names
    if not containers["app"]["version"]:
        raise ContractError("application image has no version identity")
    if arguments.service == "sub2api":
        for value in database.values():
            if not re.fullmatch(r"[A-Za-z0-9_]{1,63}", value):
                raise ContractError("database identity is invalid")
        query = subprocess.run(
            ["docker", "exec", arguments.postgres_container, "psql", "-v", "ON_ERROR_STOP=1",
             "-U", database["user"], "-d", database["database"], "-Atc",
             "SELECT count(*) FROM schema_migrations;"],
            capture_output=True, text=True, check=True, timeout=30,
        ).stdout.strip()
        if not query.isdigit():
            raise ContractError("database migration baseline is invalid")
        database["migrations"] = int(query)
    return {"schemaVersion": 1, "service": arguments.service, "containers": containers, "database": database}


def artifact_record(role: str, path: Path) -> dict:
    return {"role": role, "path": str(path), "sizeBytes": path.stat().st_size, "sha256": sha256_file(path)}


def capture(arguments: argparse.Namespace) -> dict:
    if arguments.operation_dir.is_symlink():
        raise ContractError("operation directory is a symlink")
    operation = arguments.operation_dir.resolve(strict=True)
    if operation == Path("/") or not operation.is_dir() or operation.is_symlink():
        raise ContractError("invalid operation directory")
    contract = load_contract(operation / "task-contract.json")
    if contract.get("service") != arguments.service or contract.get("action") not in {"backup", "update"}:
        raise ContractError("task service does not match metadata capture")
    task_id = contract.get("taskId", "")
    if not UUID_PATTERN.fullmatch(task_id):
        raise ContractError("task identity is invalid")
    root = arguments.backup_root.resolve(strict=True)
    if root == Path("/") or not root.is_dir():
        raise ContractError("backup root is unsafe")
    destination = root / "configs"
    destination.mkdir(mode=0o700, parents=True, exist_ok=True)
    if destination.is_symlink() or destination.stat().st_mode & 0o022:
        raise ContractError("backup metadata directory is unsafe")
    paths = {"controlled": arguments.controlled_compose, "runtime": arguments.runtime_compose, "env": arguments.env_file}
    contents = {key: private_regular(path) for key, path in paths.items()}
    runtime = capture_runtime(arguments)
    before = contract.get("expectedBefore", {})
    app = runtime["containers"]["app"]
    if before.get("currentImage") != app["configured_image"] or before.get("currentImageId") != app["image_id"]:
        raise ContractError("application changed since the approved backup baseline")
    # 文件名不匹配全局 configs-*.tar.gz 索引，避免替换每日完整备份集。
    archive = destination / f"recovery-{arguments.service}-{task_id}.tar.gz"
    with tarfile.open(archive, "x:gz") as bundle:
        for key, content in contents.items():
            entry = tarfile.TarInfo(CONFIG_MEMBERS[arguments.service][key])
            entry.size, entry.mode = len(content), 0o600
            bundle.addfile(entry, io.BytesIO(content))
    archive.chmod(0o600)
    if runtime != capture_runtime(arguments) or any(private_regular(paths[key]) != value for key, value in contents.items()):
        raise ContractError("configuration or runtime changed during backup capture")
    runtime_path = destination / f"recovery-{arguments.service}-{task_id}.json"
    with runtime_path.open("x", encoding="utf-8") as handle:
        json.dump(runtime, handle, sort_keys=True, separators=(",", ":"))
    runtime_path.chmod(0o600)
    return {"artifacts": [artifact_record("configs", archive), artifact_record("runtime-snapshot", runtime_path)]}


def validated_point(arguments: argparse.Namespace) -> dict:
    contract = load_contract(arguments.contract)
    roles = DATA_ROLES[arguments.service] + ["configs", "runtime-snapshot"]
    result = validate_contract(
        contract, arguments.service, arguments.target, roles, arguments.backup_root.resolve(strict=True),
        expected_mode=arguments.expected_mode,
    )
    snapshot = result["runtimeSnapshot"]
    if snapshot.get("schemaVersion") != 1 or snapshot.get("service") != arguments.service:
        raise ContractError("runtime snapshot service or schema mismatch")
    expected_roles = {"app", "postgres"} | ({"redis"} if arguments.service == "sub2api" else set())
    if set(snapshot.get("containers", {})) != expected_roles:
        raise ContractError("runtime snapshot container roles are incomplete")
    for item in snapshot["containers"].values():
        if not IMAGE_ID.fullmatch(item.get("image_id", "")) or not SAFE_NAME.fullmatch(item.get("name", "")):
            raise ContractError("runtime snapshot image or container identity is invalid")
    archive = Path(result["artifacts"]["configs"]["path"])
    backup_manifest.validate_archive(archive, "tar")
    with tarfile.open(archive, "r:*") as bundle:
        if set(bundle.getnames()) != set(CONFIG_MEMBERS[arguments.service].values()):
            raise ContractError("selected recovery point configuration members are incomplete")
    result["runtime"] = snapshot
    return result


def stage(arguments: argparse.Namespace) -> dict:
    result = validated_point(arguments)
    if arguments.operation_dir.is_symlink():
        raise ContractError("operation directory is a symlink")
    parent = arguments.operation_dir.resolve(strict=True)
    destination = parent / "selected-recovery-point"
    destination.mkdir(mode=0o700)
    paths = {}
    for role, artifact in result["artifacts"].items():
        if role.startswith("postgres-"):
            relative = f"postgres/{arguments.service}-postgres-selected.sql.gz"
        elif role == "configs":
            relative = "configs/configs-selected.tar.gz"
        elif role == "runtime-snapshot":
            relative = "configs/runtime-selected.json"
        elif role == "redis":
            relative = "redis/redis-selected.tar.gz"
        else:
            relative = f"volumes/{role.removeprefix('volume-')}-selected.tar.gz"
        target = destination / relative
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with Path(artifact["path"]).open("rb") as source, target.open("xb") as output:
            shutil.copyfileobj(source, output)
        target.chmod(0o600)
        if target.stat().st_size != artifact["sizeBytes"] or sha256_file(target) != artifact["sha256"]:
            raise ContractError("selected artifact changed while staging")
        paths[role] = target
    configs = parent / "selected-configs"
    configs.mkdir(mode=0o700)
    with tarfile.open(paths["configs"], "r:*") as bundle:
        for key, name in CONFIG_MEMBERS[arguments.service].items():
            content = bundle.extractfile(name)
            if content is None:
                raise ContractError("selected configuration is not a regular file")
            path = configs / name
            path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with path.open("xb") as handle:
                shutil.copyfileobj(content, handle)
            path.chmod(0o600)
    result.update({
        "stagedRoot": str(destination),
        "relativeArtifacts": {role: str(path.relative_to(destination)) for role, path in paths.items()},
        "stagedArtifacts": {role: str(path) for role, path in paths.items()},
        "envFile": str(configs / CONFIG_MEMBERS[arguments.service]["env"]),
    })
    return result


def verify_staged(arguments: argparse.Namespace) -> dict:
    current = validated_point(arguments)
    operation = arguments.operation_dir.resolve(strict=True)
    saved = json.loads(private_regular(operation / "selected-point.json"))
    for key in ("recoveryPointId", "bindingDigest", "evidenceDigest", "service", "mode", "runtime", "artifacts"):
        if saved.get(key) != current.get(key):
            raise ContractError("staged recovery point identity changed")
    root = operation / "selected-recovery-point"
    if saved.get("stagedRoot") != str(root) or root.is_symlink():
        raise ContractError("staged root identity changed")
    for role, source in current["artifacts"].items():
        path = Path(saved.get("stagedArtifacts", {}).get(role, ""))
        if not path.is_absolute() or not path.is_relative_to(root) or path.is_symlink():
            raise ContractError("staged artifact path is invalid")
        if not path.resolve(strict=True).is_relative_to(root) or saved.get("relativeArtifacts", {}).get(role) != str(path.relative_to(root)):
            raise ContractError("staged artifact alias or relative path changed")
        if path.stat().st_size != source["sizeBytes"] or sha256_file(path) != source["sha256"]:
            raise ContractError("staged artifact was modified")
    expected_env = operation / "selected-configs" / CONFIG_MEMBERS[arguments.service]["env"]
    if saved.get("envFile") != str(expected_env) or expected_env.is_symlink() or not expected_env.resolve(strict=True).is_relative_to(operation):
        raise ContractError("staged configuration path changed")
    with tarfile.open(saved["stagedArtifacts"]["configs"], "r:*") as bundle:
        member = bundle.extractfile(CONFIG_MEMBERS[arguments.service]["env"])
        if member is None or private_regular(expected_env) != member.read():
            raise ContractError("staged environment file changed")
    return saved


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("capture", "validate", "stage", "verify-staged"))
    parser.add_argument("--service", required=True, choices=tuple(DATA_ROLES))
    parser.add_argument("--backup-root", type=Path, default=Path(os.environ.get("BACKUP_ROOT", "/var/backups/ops")))
    parser.add_argument("--operation-dir", type=Path)
    parser.add_argument("--contract", type=Path)
    parser.add_argument("--target", default="")
    parser.add_argument("--expected-mode", choices=("production", "isolated"), default="production")
    for field in ("controlled-compose", "runtime-compose", "env-file"):
        parser.add_argument("--" + field, type=Path)
    for field in ("app-container", "postgres-container", "redis-container"):
        parser.add_argument("--" + field)
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if arguments.command == "capture":
        if any(getattr(arguments, field) is None for field in (
            "operation_dir", "controlled_compose", "runtime_compose", "env_file", "app_container", "postgres_container"
        )):
            raise ContractError("capture arguments are incomplete")
        if arguments.service == "sub2api" and not arguments.redis_container:
            raise ContractError("Sub2API Redis identity is required")
        output = capture(arguments)
    else:
        if arguments.contract is None or arguments.command in {"stage", "verify-staged"} and arguments.operation_dir is None:
            raise ContractError("restore contract arguments are incomplete")
        handler = {"stage": stage, "verify-staged": verify_staged, "validate": validated_point}[arguments.command]
        output = handler(arguments)
    print(json.dumps(output, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ContractError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(f"ERROR: {error}", file=os.sys.stderr)
        raise SystemExit(1)
