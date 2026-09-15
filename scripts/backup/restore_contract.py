#!/usr/bin/env python3
"""Validate an AreaSong Ops production-restore contract and its artifacts."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import stat
from decimal import Decimal
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any


DIGEST_PATTERN = re.compile(r"^sha256:[0-9a-f]{64}$")
UUID_PATTERN = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")


class ContractError(ValueError):
    pass


def parse_time(value: Any, field: str) -> datetime:
    if not isinstance(value, str) or not value:
        raise ContractError(f"{field} is missing")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise ContractError(f"{field} is invalid") from error
    if parsed.tzinfo is None:
        raise ContractError(f"{field} must include a timezone")
    return parsed.astimezone(timezone.utc)


def require_text(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ContractError(f"{field} is missing")
    return value


def require_digest(value: Any, field: str) -> str:
    value = require_text(value, field)
    if not DIGEST_PATTERN.fullmatch(value):
        raise ContractError(f"{field} is invalid")
    return value


def require_object(value: Any, field: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ContractError(f"{field} must be an object")
    return value


def load_contract(path: Path) -> dict[str, Any]:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise ContractError("restore contract is unavailable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise ContractError("restore contract must be a regular non-symlink file")
    if stat.S_IMODE(metadata.st_mode) != 0o600:
        raise ContractError("restore contract mode must be 0600")
    expected_owner = 0 if os.geteuid() == 0 else os.geteuid()
    if metadata.st_uid != expected_owner:
        raise ContractError("restore contract owner is invalid")
    if metadata.st_size <= 0 or metadata.st_size > 256 * 1024:
        raise ContractError("restore contract size is invalid")
    try:
        with path.open("r", encoding="utf-8") as handle:
            value = json.load(handle, parse_int=lambda number: -0.0 if number == "-0" else int(number))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise ContractError("restore contract JSON is invalid") from error
    return require_object(value, "contract")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def json_digest(value: Any, *, sort_keys: bool = False) -> str:
    encoded = go_json(value, sort_keys=sort_keys).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def go_json(value: Any, *, sort_keys: bool = False) -> str:
    """匹配 Go encoding/json 的字符串转义、数字表示及既定结构字段顺序。"""
    if isinstance(value, str):
        return (json.dumps(value, ensure_ascii=False)
                .replace("&", r"\u0026").replace("<", r"\u003c").replace(">", r"\u003e")
                .replace("\u2028", r"\u2028").replace("\u2029", r"\u2029"))
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ContractError("non-finite canonical number")
        text = repr(value)
        if value == 0:
            return "-0" if math.copysign(1, value) < 0 else "0"
        if 1e-6 <= abs(value) < 1e21:
            text = format(Decimal(text), "f")
            return text.rstrip("0").rstrip(".") if "." in text else text
        return re.sub(r"e([+-])0+", r"e\1", text.replace(".0e", "e"))
    if isinstance(value, list):
        return "[" + ",".join(go_json(item, sort_keys=sort_keys) for item in value) + "]"
    if isinstance(value, dict):
        keys = sorted(value) if sort_keys else value
        return "{" + ",".join(go_json(key) + ":" + go_json(value[key], sort_keys=sort_keys) for key in keys) + "}"
    raise ContractError("unsupported canonical JSON value")


def canonical_evidence(value: dict[str, Any], *, unsigned: bool = False) -> dict[str, Any]:
    fields = ("schemaVersion", "service", "taskId", "tenantId", "serverId", "expectedBeforeDigest",
              "bindingDigest", "createdAt", "artifacts")
    if set(value) - set(fields):
        raise ContractError("unsupported recovery evidence fields")
    if type(value.get("schemaVersion")) is not int or value["schemaVersion"] != 1 or not isinstance(value.get("artifacts"), list):
        raise ContractError("recovery evidence schema or artifacts are invalid")
    if any(not isinstance(item, dict) or set(item) != {"role", "path", "sizeBytes", "sha256"} for item in value["artifacts"]):
        raise ContractError("recovery artifact fields are invalid")
    result = {}
    for field in fields:
        item = value.get(field)
        if field in {"tenantId", "serverId", "expectedBeforeDigest", "bindingDigest"} and not item:
            continue
        if unsigned and field == "bindingDigest":
            continue
        if field == "artifacts":
            item = [{key: artifact.get(key) for key in ("role", "path", "sizeBytes", "sha256")} for artifact in item]
        result[field] = item
    return result


def validate_artifact(raw: Any, backup_root: Path) -> dict[str, Any]:
    artifact = require_object(raw, "recoveryPoint.evidence.artifacts[]")
    role = require_text(artifact.get("role"), "artifact.role")
    raw_path = require_text(artifact.get("path"), f"artifact[{role}].path")
    size = artifact.get("sizeBytes")
    if not isinstance(size, int) or isinstance(size, bool) or size <= 0:
        raise ContractError(f"artifact[{role}].sizeBytes is invalid")
    expected_digest = require_digest(artifact.get("sha256"), f"artifact[{role}].sha256")
    path = Path(raw_path)
    if not path.is_absolute():
        raise ContractError(f"artifact[{role}] path must be absolute")
    try:
        metadata = path.lstat()
        resolved = path.resolve(strict=True)
    except OSError as error:
        raise ContractError(f"artifact[{role}] is unavailable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise ContractError(f"artifact[{role}] must be a regular non-symlink file")
    expected_owner = 0 if os.geteuid() == 0 else os.geteuid()
    if metadata.st_uid != expected_owner or stat.S_IMODE(metadata.st_mode) & 0o022:
        raise ContractError(f"artifact[{role}] ownership or mode is unsafe")
    try:
        resolved.relative_to(backup_root)
    except ValueError as error:
        raise ContractError(f"artifact[{role}] is outside the backup root") from error
    if metadata.st_size != size:
        raise ContractError(f"artifact[{role}] size changed")
    if sha256_file(path) != expected_digest:
        raise ContractError(f"artifact[{role}] digest changed")
    return {"role": role, "path": str(resolved), "sizeBytes": size, "sha256": expected_digest}


def validate_contract(
    contract: dict[str, Any], service: str, target: str, required_roles: list[str], backup_root: Path,
    *, expected_mode: str = "production",
) -> dict[str, Any]:
    if expected_mode not in {"production", "isolated"} or type(contract.get("schemaVersion")) is not int or contract.get("schemaVersion") != 1 or contract.get("mode") != expected_mode:
        raise ContractError("contract schema or restore mode is invalid")
    if contract.get("service") != service:
        raise ContractError("contract service does not match the restore hook")
    point_id = require_text(contract.get("recoveryPointId"), "recoveryPointId")
    task_id = require_text(contract.get("taskId"), "taskId")
    plan_id = require_text(contract.get("planId"), "planId")
    if not UUID_PATTERN.fullmatch(point_id) or not UUID_PATTERN.fullmatch(task_id) or not UUID_PATTERN.fullmatch(plan_id):
        raise ContractError("contract task, plan, or recovery point identity is invalid")
    if target != point_id:
        raise ContractError("restore target does not match recoveryPointId")
    binding_digest = require_digest(contract.get("bindingDigest"), "bindingDigest")
    evidence_digest = require_digest(contract.get("evidenceDigest"), "evidenceDigest")
    expected_before = require_digest(contract.get("expectedBeforeDigest"), "expectedBeforeDigest")
    revalidated_at = parse_time(contract.get("revalidatedAt"), "revalidatedAt")
    now = datetime.now(timezone.utc)
    if revalidated_at > now + timedelta(minutes=1) or revalidated_at < now - timedelta(hours=2):
        raise ContractError("restore contract revalidation is stale")

    point = require_object(contract.get("recoveryPoint"), "recoveryPoint")
    if point.get("id") != point_id or point.get("service") != service or point.get("status") != "verified":
        raise ContractError("recovery point identity or status is invalid")
    if point.get("tenantId") != contract.get("tenantId") or point.get("serverId") != contract.get("serverId"):
        raise ContractError("recovery point tenant/server binding changed")
    if point.get("bindingDigest") != binding_digest or point.get("evidenceDigest") != evidence_digest:
        raise ContractError("recovery point digest binding changed")
    if point.get("expectedBeforeDigest") != expected_before:
        raise ContractError("recovery point expected-before binding changed")
    expected_snapshot = require_object(point.get("expectedBefore"), "recoveryPoint.expectedBefore")
    if json_digest(expected_snapshot, sort_keys=True) != expected_before:
        raise ContractError("recovery point expected-before digest is invalid")
    if parse_time(point.get("recoverableUntil"), "recoverableUntil") <= now:
        raise ContractError("recovery point has expired")

    evidence = require_object(point.get("evidence"), "recoveryPoint.evidence")
    for field, expected in (
        ("service", service),
        ("tenantId", contract.get("tenantId")),
        ("serverId", contract.get("serverId")),
        ("expectedBeforeDigest", expected_before),
        ("bindingDigest", binding_digest),
    ):
        if evidence.get(field) != expected:
            raise ContractError(f"recovery point evidence {field} changed")

    unsigned_evidence = canonical_evidence(evidence, unsigned=True)
    unsigned_digest = json_digest(unsigned_evidence)

    declared_roles = point.get("requiredArtifactRoles")
    if not isinstance(declared_roles, list) or any(not isinstance(item, str) for item in declared_roles):
        raise ContractError("requiredArtifactRoles is invalid")
    if sorted(declared_roles) != sorted(required_roles) or len(set(declared_roles)) != len(declared_roles):
        raise ContractError("recovery point required roles do not match the restore hook")
    binding_envelope = {
        "schemaVersion": 1,
        "service": service,
        "taskId": require_text(evidence.get("taskId"), "recoveryPoint.evidence.taskId"),
        "tenantId": contract.get("tenantId"),
        "serverId": contract.get("serverId"),
        "expectedBeforeDigest": expected_before,
        "evidenceDigest": unsigned_digest,
        "requiredRoles": sorted(required_roles),
    }
    if json_digest(binding_envelope) != binding_digest:
        raise ContractError("recovery point binding digest is invalid")
    if json_digest(canonical_evidence(evidence)) != evidence_digest:
        raise ContractError("recovery point evidence digest is invalid")
    artifacts_raw = evidence.get("artifacts")
    if not isinstance(artifacts_raw, list):
        raise ContractError("recovery point artifacts are invalid")
    artifacts: dict[str, dict[str, Any]] = {}
    for raw in artifacts_raw:
        artifact = validate_artifact(raw, backup_root)
        role = artifact["role"]
        if role in artifacts:
            raise ContractError(f"artifact role is duplicated: {role}")
        artifacts[role] = artifact
    if sorted(artifacts) != sorted(required_roles):
        raise ContractError("recovery point artifact roles do not match the restore hook")
    result = {
        "schemaVersion": 1,
        "taskId": task_id,
        "planId": plan_id,
        "service": service,
        "recoveryPointId": point_id,
        "mode": expected_mode,
        "bindingDigest": binding_digest,
        "evidenceDigest": evidence_digest,
        "artifacts": artifacts,
    }
    if "runtime-snapshot" in artifacts:
        snapshot = load_contract(Path(artifacts["runtime-snapshot"]["path"]))
        if snapshot.get("schemaVersion") != 1 or snapshot.get("service") != service:
            raise ContractError("runtime snapshot identity is invalid")
        containers = require_object(snapshot.get("containers"), "runtimeSnapshot.containers")
        expected_roles = {"app", "postgres"} | ({"redis"} if service == "sub2api" else set())
        if set(containers) != expected_roles:
            raise ContractError("runtime snapshot container roles are incomplete")
        for container in containers.values():
            require_digest(require_object(container, "runtime container").get("image_id"), "runtime image ID")
        app = containers["app"]
        if app.get("image_id") != expected_snapshot.get("currentImageId") or app.get("configured_image") != expected_snapshot.get("currentImage"):
            raise ContractError("runtime snapshot differs from the approved backup identity")
        database = require_object(snapshot.get("database"), "runtimeSnapshot.database")
        for key in ("user", "database"):
            if not re.fullmatch(r"[A-Za-z0-9_]{1,63}", require_text(database.get(key), f"runtime database {key}")):
                raise ContractError("runtime database name is invalid")
        if service == "sub2api" and (type(database.get("migrations")) is not int or database["migrations"] < 0):
            raise ContractError("runtime migration baseline is invalid")
        result["runtimeSnapshot"] = snapshot
    return result


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--contract", required=True, type=Path)
    parser.add_argument("--service", required=True)
    parser.add_argument("--target", required=True)
    parser.add_argument("--backup-root", default=os.environ.get("BACKUP_ROOT", "/var/backups/ops"), type=Path)
    parser.add_argument("--required-role", action="append", default=[])
    parser.add_argument("--expected-mode", choices=("production", "isolated"), default="production")
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if not arguments.required_role or len(set(arguments.required_role)) != len(arguments.required_role):
        raise ContractError("required roles are missing or duplicated")
    backup_root = arguments.backup_root.resolve(strict=True)
    if not backup_root.is_dir():
        raise ContractError("backup root is not a directory")
    contract = load_contract(arguments.contract)
    result = validate_contract(
        contract, arguments.service, arguments.target, arguments.required_role, backup_root,
        expected_mode=arguments.expected_mode,
    )
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ContractError as error:
        print(f"ERROR: {error}", file=os.sys.stderr)
        raise SystemExit(1)
