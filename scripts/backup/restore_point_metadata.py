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


def capture_scoped_configs(manifest: dict, destination: Path) -> None:
    """只生成 configs；runtime 必须等四子回执完成后独立 finalize。"""
    from sub2api_backup import source_bytes
    from sub2api_backup_archive import BackupError, tar_bytes
    contents = {key: source_bytes(manifest, key) for key in CONFIG_MEMBERS["sub2api"]}
    validate_scoped_baseline(manifest["runtime"])
    tar_bytes(destination, {CONFIG_MEMBERS["sub2api"][key]: value for key, value in contents.items()})
    if any(source_bytes(manifest, key) != value for key, value in contents.items()):
        raise BackupError("config_changed")


def validate_scoped_baseline(value: dict) -> None:
    from sub2api_backup_contract import exact, require_hex
    from sub2api_backup_archive import BackupError
    exact(value, {"containers", "database"})
    exact(value["containers"], {"app", "postgres", "redis"})
    for item in value["containers"].values():
        exact(item, {"name", "configured_image", "image_id", "container_id", "version", "revision"})
        if not SAFE_NAME.fullmatch(item["name"]) or not IMAGE_ID.fullmatch(item["image_id"]):
            raise BackupError("runtime_identity")
        require_hex(item["container_id"])
        if not re.fullmatch(r"[A-Za-z0-9_./:@+-]{1,256}", item["configured_image"]) or not SAFE_NAME.fullmatch(item["version"]) or not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", item["revision"]):
            raise BackupError("runtime_identity")
    exact(value["database"], {"user", "database", "migrations"})
    for key in ("user", "database"):
        if not re.fullmatch(r"[A-Za-z0-9_]{1,63}", value["database"][key]):
            raise BackupError("runtime_database")
    if type(value["database"]["migrations"]) is not int or value["database"]["migrations"] < 0:
        raise BackupError("runtime_migrations")


def finalize_scoped_runtime(request, manifest, checksum, destination, items, children, proof):
    from sub2api_backup import validate_subreceipt
    from sub2api_backup_archive import BackupError, read_regular
    from sub2api_backup_contract import ARTIFACTS, JOBS, binding
    if len(items) != 4 or len(children) != 4:
        raise BackupError("runtime_inputs")
    for job, item, child in zip(JOBS, items, children):
        path = destination / (job + ".receipt.json")
        actual = validate_subreceipt(read_regular(path), request, checksum, job, item)
        if child != {"job": job, "path": path.name, "sha256": "sha256:" + actual}:
            raise BackupError("runtime_receipts")
    baseline = manifest["runtime"]
    validate_scoped_baseline(baseline)
    if any(baseline["containers"][key]["container_id"] != value["id"] for key, value in manifest["containers"].items()):
        raise BackupError("runtime_source_identity")
    value = {"schemaVersion": 2, "service": "sub2api", **baseline,
             "backup": {"protocolVersion": 1, **binding(request), "requestDigest": checksum,
                        "artifacts": items, "subreceipts": children, "proof": proof,
                        "consistency": "synthetic-coordinated" if manifest["materialKind"] == "synthetic" else "coordinated"}}
    validate_scoped_runtime(value, items, expected_binding={**binding(request), "requestDigest": checksum})
    return value


def validate_scoped_runtime(snapshot, artifacts, *, expected_binding=None):
    """只校验 v2 格式/来源绑定；不授予恢复目标或演练成功。"""
    from sub2api_backup_archive import BackupError
    from sub2api_backup_contract import ARTIFACTS, BINDINGS, JOBS, UUID, exact, require_hex
    exact(snapshot, {"schemaVersion", "service", "containers", "database", "backup"})
    if type(snapshot["schemaVersion"]) is not int or snapshot["schemaVersion"] != 2 or snapshot["service"] != "sub2api":
        raise BackupError("runtime_version")
    validate_scoped_baseline({key: snapshot[key] for key in ("containers", "database")})
    b = exact(snapshot["backup"], {"protocolVersion", *BINDINGS, "requestDigest", "artifacts", "subreceipts", "proof", "consistency"})
    if type(b["protocolVersion"]) is not int or b["protocolVersion"] != 1 or b["consistency"] not in {"synthetic-coordinated", "coordinated"}:
        raise BackupError("runtime_protocol")
    for key in BINDINGS[:5]:
        if not isinstance(b[key], str) or not UUID.fullmatch(b[key]):
            raise BackupError("runtime_binding")
    for key in (*BINDINGS[7:], "requestDigest", "proof"):
        require_hex(b[key])
    if expected_binding is not None and any(b[k] != v for k, v in expected_binding.items()):
        raise BackupError("runtime_call")
    if len(b["artifacts"]) != 4 or len(b["subreceipts"]) != 4:
        raise BackupError("runtime_set")
    outer = {a["role"]: a for a in artifacts}
    if len(outer) != len(artifacts) or set(outer) not in ({ARTIFACTS[j][0] for j in JOBS}, {v[0] for v in ARTIFACTS.values()}):
        raise BackupError("runtime_outer_roles")
    for job, item, child in zip(JOBS, b["artifacts"], b["subreceipts"]):
        exact(item, {"role", "path", "format", "sizeBytes", "sha256"})
        role, name, fmt = ARTIFACTS[job]
        if (item["role"], item["path"], item["format"]) != (role, name, fmt) or type(item["sizeBytes"]) is not int or item["sizeBytes"] <= 0 or not re.fullmatch(r"sha256:[0-9a-f]{64}", item["sha256"]):
            raise BackupError("runtime_artifact")
        actual = outer[role]
        if Path(actual["path"]).name != name or any(actual[k] != item[k] for k in ("sizeBytes", "sha256")):
            raise BackupError("runtime_artifact_binding")
        if Path(actual["path"]).is_absolute() and Path(actual["path"]).parts[-3:-1] != (b["taskId"], b["callId"]):
            raise BackupError("runtime_artifact_call_path")
        exact(child, {"job", "path", "sha256"})
        if child["job"] != job or child["path"] != job + ".receipt.json" or not re.fullmatch(r"sha256:[0-9a-f]{64}", child["sha256"]):
            raise BackupError("runtime_subreceipt")
    return snapshot


def require_scoped_restore_target(arguments, result):
    # 公共恢复审批没有可信目标资源映射；CLI 不接受文件/环境自报 target proof。
    verifier = getattr(arguments, "_target_verifier", None)
    if verifier is None:
        raise ContractError("v2_restore_target_unproven")
    from sub2api_backup_contract import exact, require_hex
    from sub2api_backup_archive import absolute, digest, encode
    evidence = verifier(arguments, result)
    exact(evidence, {"taskId", "planId", "sourceDigest", "mapping", "configuration", "bootstrap", "proof"})
    if evidence["taskId"] != result["taskId"] or evidence["planId"] != result["planId"] or evidence["sourceDigest"] != result["runtimeSnapshot"]["backup"]["resourceDigest"] or evidence["configuration"] != "matched" or evidence["bootstrap"] != "compatible":
        raise ContractError("v2_restore_target_mismatch")
    require_hex(evidence["proof"])
    mapping = exact(evidence["mapping"], {"daemonId", "endpoint", "project", "containers", "mounts"})
    if not mapping["endpoint"].startswith("unix:///") or not SAFE_NAME.fullmatch(mapping["daemonId"]) or not SAFE_NAME.fullmatch(mapping["project"]):
        raise ContractError("v2_restore_target_identity")
    absolute(mapping["endpoint"][7:])
    exact(mapping["containers"], {"app", "postgres", "redis"})
    exact(mapping["mounts"], {"app", "postgres", "redis"})
    for role, value in mapping["containers"].items():
        exact(value, {"id", "generation"})
        require_hex(value["id"])
        if not SAFE_NAME.fullmatch(value["generation"]):
            raise ContractError("v2_restore_target_generation")
        absolute(mapping["mounts"][role])
    # 该回调持有本次恢复批准及旧→新转换的核验责任；文件／环境不能装配它。
    result["restoreTargetDigest"] = digest(encode(evidence))


def validated_point(arguments: argparse.Namespace) -> dict:
    contract = load_contract(arguments.contract)
    roles = DATA_ROLES[arguments.service] + ["configs", "runtime-snapshot"]
    result = validate_contract(
        contract, arguments.service, arguments.target, roles, arguments.backup_root.resolve(strict=True),
        expected_mode=arguments.expected_mode,
    )
    snapshot = result["runtimeSnapshot"]
    if snapshot.get("schemaVersion") not in (1, 2) or snapshot.get("service") != arguments.service:
        raise ContractError("runtime snapshot service or schema mismatch")
    if snapshot.get("schemaVersion") == 2:
        from sub2api_backup_archive import validate_scoped_archives
        validate_scoped_archives(list(result["artifacts"].values()), CONFIG_MEMBERS["sub2api"].values())
        require_scoped_restore_target(arguments, result)
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


def stage_scoped(arguments, result):
    from sub2api_backup_archive import (BackupError, copy_private, private_dir,
        private_parents, read_regular, write_new, verify_private_tree)
    parent = arguments.operation_dir
    destination = private_dir(parent / "selected-recovery-point")
    paths = {}
    for role, item in result["artifacts"].items():
        target = destination / Path(item["path"]).name
        observed = copy_private(item["path"], target)
        if observed != {k: item[k] for k in ("sizeBytes", "sha256")}:
            raise BackupError("staged_artifact_changed")
        paths[role] = target
    configs = private_dir(parent / "selected-configs")
    with tarfile.open(paths["configs"], "r:gz") as bundle:
        expected = set(CONFIG_MEMBERS["sub2api"].values())
        if set(bundle.getnames()) != expected or len(bundle.getnames()) != len(expected):
            raise BackupError("config_members")
        for name in sorted(expected):
            entry = bundle.getmember(name)
            if not entry.isfile() or entry.mode != 0o600 or entry.size <= 0 or entry.size > 8*1024*1024:
                raise BackupError("config_member_type")
            target_parent = private_parents(configs, str(Path(name).parent))
            write_new(target_parent / Path(name).name, bundle.extractfile(entry).read())
    verify_private_tree(destination)
    verify_private_tree(configs)
    result.update({"stagedRoot": str(destination),
                   "relativeArtifacts": {role: path.name for role, path in paths.items()},
                   "stagedArtifacts": {role: str(path) for role, path in paths.items()},
                   "envFile": str(configs / CONFIG_MEMBERS["sub2api"]["env"])})
    return result


def stage(arguments: argparse.Namespace) -> dict:
    result = validated_point(arguments)
    if result["runtime"]["schemaVersion"] == 2:
        return stage_scoped(arguments, result)
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
    if current["runtime"]["schemaVersion"] == 2:
        from sub2api_backup_archive import read_regular, verify_private_tree
        read_regular(operation / "selected-point.json")
        verify_private_tree(operation / "selected-recovery-point")
        verify_private_tree(operation / "selected-configs")
        if saved.get("restoreTargetDigest") != current.get("restoreTargetDigest"):
            raise ContractError("v2_restore_target_changed")
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
    if current["runtime"]["schemaVersion"] == 2:
        from sub2api_backup_archive import read_regular
        with tarfile.open(saved["stagedArtifacts"]["configs"], "r:gz") as bundle:
            for name in CONFIG_MEMBERS["sub2api"].values():
                item = bundle.extractfile(name)
                if item is None or read_regular(operation / "selected-configs" / name, limit=8*1024*1024) != item.read():
                    raise ContractError("staged_configuration_changed")
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
    except (ContractError, OSError, ValueError, KeyError, TypeError, tarfile.TarError, subprocess.SubprocessError) as error:
        print("ERROR: metadata_contract_rejected", file=os.sys.stderr)
        raise SystemExit(1)
