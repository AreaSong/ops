#!/usr/bin/env python3
"""Fail-closed host control plane for service update requests."""

from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
import re
import stat
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any


REQUEST_KEYS = {
    "schemaVersion",
    "id",
    "idempotencyKey",
    "service",
    "action",
    "status",
    "requestedAt",
    "expiresAt",
    "actorEmailHash",
    "targetId",
    "expectedBefore",
}
EXPECTED_KEYS = {
    "currentVersion",
    "currentImage",
    "currentImageId",
    "runtimeIdentityHash",
    "autoApply",
    "signatureRequired",
    "rollbackAvailable",
    "rollbackTargetVersion",
    "rollbackTargetImage",
    "rollbackSourceRecordSha256",
}
PHASES = ("preflight", "backup", "migration", "apply", "health", "smoke", "identity")
ID_RE = re.compile(r"^update_[0-9]+_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
UUID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
HASH_RE = re.compile(r"^sha256:[0-9a-f]{64}$")


class ControlError(RuntimeError):
    pass


def canonical(value: Any) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def digest(value: Any) -> str:
    return "sha256:" + hashlib.sha256(canonical(value)).hexdigest()


def parse_time(value: Any, field: str) -> dt.datetime:
    if not isinstance(value, str) or not value.endswith("Z"):
        raise ControlError(f"{field} must be an RFC3339 UTC timestamp")
    try:
        parsed = dt.datetime.fromisoformat(value[:-1] + "+00:00")
    except ValueError as error:
        raise ControlError(f"{field} must be an RFC3339 UTC timestamp") from error
    return parsed


def secure_regular_file(path: Path, *, root_required: bool) -> None:
    try:
        info = path.lstat()
    except FileNotFoundError as error:
        raise ControlError(f"request file is missing: {path}") from error
    if not stat.S_ISREG(info.st_mode) or path.is_symlink():
        raise ControlError("request must be a regular non-symlink file")
    if stat.S_IMODE(info.st_mode) != 0o600:
        raise ControlError("request file must use mode 0600")
    if root_required and info.st_uid != 0:
        raise ControlError("request file must be owned by root")


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ControlError(f"invalid JSON file: {path}") from error
    if not isinstance(value, dict):
        raise ControlError(f"JSON root must be an object: {path}")
    return value


def validate_request(request: dict[str, Any], now: dt.datetime) -> None:
    if set(request) != REQUEST_KEYS:
        raise ControlError("request keys do not match schema v1")
    if request["schemaVersion"] != 1 or request["status"] != "queued":
        raise ControlError("request must be queued schema v1")
    if not isinstance(request["id"], str) or not ID_RE.fullmatch(request["id"]):
        raise ControlError("invalid request id")
    key = request["idempotencyKey"]
    if not isinstance(key, str) or not UUID_RE.fullmatch(key):
        raise ControlError("invalid idempotency key")
    for field in ("service", "action", "targetId"):
        value = request[field]
        if not isinstance(value, str) or not re.fullmatch(r"[a-zA-Z0-9._-]{1,80}", value):
            raise ControlError(f"invalid {field}")
    actor_hash = request["actorEmailHash"]
    if not isinstance(actor_hash, str) or not re.fullmatch(r"[0-9a-f]{64}", actor_hash):
        raise ControlError("invalid actorEmailHash")
    expected = request["expectedBefore"]
    if not isinstance(expected, dict) or set(expected) != EXPECTED_KEYS:
        raise ControlError("expectedBefore keys do not match schema v1")
    for field in ("currentVersion", "currentImage", "currentImageId", "runtimeIdentityHash"):
        if not isinstance(expected[field], str) or not expected[field]:
            raise ControlError(f"expectedBefore.{field} must be non-empty")
    if not HASH_RE.fullmatch(expected["currentImageId"]):
        raise ControlError("expectedBefore.currentImageId must be sha256")
    if not HASH_RE.fullmatch(expected["runtimeIdentityHash"]):
        raise ControlError("expectedBefore.runtimeIdentityHash must be sha256")
    if expected["autoApply"] not in ("none", "patch", "minor", "all"):
        raise ControlError("expectedBefore.autoApply is invalid")
    if not isinstance(expected["signatureRequired"], bool) or not isinstance(expected["rollbackAvailable"], bool):
        raise ControlError("expectedBefore boolean policy fields are invalid")
    rollback_fields = ("rollbackTargetVersion", "rollbackTargetImage", "rollbackSourceRecordSha256")
    rollback_values = [expected[field] for field in rollback_fields]
    if expected["rollbackAvailable"] != all(isinstance(value, str) and value for value in rollback_values):
        raise ControlError("expectedBefore rollback metadata is inconsistent")
    if not expected["rollbackAvailable"] and any(value is not None for value in rollback_values):
        raise ControlError("expectedBefore disabled rollback metadata must be null")
    if expected["rollbackAvailable"] and not HASH_RE.fullmatch(expected["rollbackSourceRecordSha256"]):
        raise ControlError("expectedBefore rollback source hash is invalid")
    requested = parse_time(request["requestedAt"], "requestedAt")
    expires = parse_time(request["expiresAt"], "expiresAt")
    if expires < requested or expires - requested > dt.timedelta(minutes=10):
        raise ControlError("request TTL must be between 0 and 600 seconds")
    if requested > now + dt.timedelta(seconds=30) or expires < now - dt.timedelta(seconds=30):
        raise ControlError("request is not currently valid")


def load_service(catalog: dict[str, Any], request: dict[str, Any]) -> dict[str, Any]:
    if catalog.get("schemaVersion") != 1 or set(catalog) != {"schemaVersion", "services"}:
        raise ControlError("invalid service catalog")
    services = catalog.get("services")
    service = services.get(request["service"]) if isinstance(services, dict) else None
    if not isinstance(service, dict):
        raise ControlError("service is not allowlisted")
    if service.get("enabled") is not True:
        raise ControlError("service adapter is disabled")
    if request["action"] not in service.get("actions", []):
        raise ControlError("action is not allowlisted for service")
    if request["targetId"] not in service.get("targets", []):
        raise ControlError("targetId is not allowlisted for service")
    adapter = service.get("adapter")
    if not isinstance(adapter, str) or not re.fullmatch(r"[a-z0-9_-]+\.sh", adapter):
        raise ControlError("catalog adapter name is invalid")
    return service


def ensure_dir(path: Path, mode: int) -> None:
    path.mkdir(parents=True, exist_ok=True, mode=mode)
    if path.is_symlink() or not path.is_dir():
        raise ControlError(f"unsafe state directory: {path}")
    path.chmod(mode)


def atomic_json(path: Path, value: dict[str, Any]) -> None:
    ensure_dir(path.parent, 0o700)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as handle:
            handle.write(canonical(value))
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def append_audit(path: Path, event: dict[str, Any]) -> None:
    ensure_dir(path.parent, 0o700)
    flags = os.O_APPEND | os.O_CREAT | os.O_WRONLY | getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags, 0o600)
    try:
        os.write(fd, canonical(event))
        os.fsync(fd)
    finally:
        os.close(fd)


def run_adapter(adapter: Path, phase: str, request_path: Path, operation_dir: Path) -> dict[str, Any]:
    try:
        result = subprocess.run(
            [str(adapter), phase, str(request_path), str(operation_dir)],
            text=True,
            capture_output=True,
            timeout=3600,
            check=False,
        )
    except subprocess.TimeoutExpired as error:
        raise ControlError(f"adapter phase {phase} timed out") from error
    except OSError as error:
        raise ControlError(f"adapter phase {phase} could not start") from error
    if result.returncode != 0:
        message = result.stderr.strip().splitlines()[-1] if result.stderr.strip() else "adapter failed"
        raise ControlError(f"adapter phase {phase} failed: {message[:500]}")
    try:
        output = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise ControlError(f"adapter phase {phase} returned invalid JSON") from error
    if not isinstance(output, dict) or output.get("ok") is not True or output.get("phase") != phase:
        raise ControlError(f"adapter phase {phase} returned an invalid contract")
    return output


def execute(request_path: Path) -> dict[str, Any]:
    test_mode = os.environ.get("OPS_UPDATE_CONTROL_TEST_MODE") == "1"
    if not test_mode and os.geteuid() != 0:
        raise ControlError("update control must run as root")
    secure_regular_file(request_path, root_required=not test_mode)
    request = load_json(request_path)
    now = dt.datetime.now(dt.timezone.utc)
    validate_request(request, now)

    script_dir = Path(__file__).resolve().parent
    control_dir = script_dir / "update-control"
    catalog_path = Path(os.environ.get("OPS_UPDATE_CONTROL_CATALOG", control_dir / "services.json"))
    adapter_dir = Path(os.environ.get("OPS_UPDATE_CONTROL_ADAPTER_DIR", control_dir / "adapters"))
    state_root = Path(os.environ.get("OPS_UPDATE_CONTROL_STATE_ROOT", "/var/lib/ops/update-control"))
    service = load_service(load_json(catalog_path), request)
    adapter = (adapter_dir / service["adapter"]).resolve()
    if adapter.parent != adapter_dir.resolve() or not adapter.is_file() or not os.access(adapter, os.X_OK):
        raise ControlError("allowlisted adapter is missing or not executable")

    locks = state_root / "locks"
    operations = state_root / "operations"
    idempotency = state_root / "idempotency"
    ensure_dir(locks, 0o700)
    ensure_dir(operations, 0o700)
    ensure_dir(idempotency, 0o700)
    lock_path = locks / f"{request['service']}.lock"
    lock_fd = os.open(lock_path, os.O_CREAT | os.O_RDWR | getattr(os, "O_NOFOLLOW", 0), 0o600)
    try:
        try:
            fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ControlError("another update operation is active for service") from error
        return execute_locked(request, request_path, adapter, state_root, operations, idempotency)
    finally:
        os.close(lock_fd)


def execute_locked(
    request: dict[str, Any],
    request_path: Path,
    adapter: Path,
    state_root: Path,
    operations: Path,
    idempotency_dir: Path,
) -> dict[str, Any]:
    request_hash = digest(request)
    idempotency_path = idempotency_dir / f"{request['idempotencyKey']}.json"
    if idempotency_path.exists():
        if idempotency_path.is_symlink() or not idempotency_path.is_file():
            raise ControlError("idempotency state path is unsafe")
        previous = load_json(idempotency_path)
        if previous.get("requestHash") != request_hash:
            raise ControlError("idempotency key was already used by another request")
        if previous.get("status") == "in_progress":
            raise ControlError("previous operation is incomplete and requires reconciliation")
        return previous

    operation_dir = operations / request["id"]
    ensure_dir(operation_dir, 0o700)
    state: dict[str, Any] = {
        "schemaVersion": 1,
        "requestId": request["id"],
        "requestHash": request_hash,
        "service": request["service"],
        "action": request["action"],
        "targetId": request["targetId"],
        "status": "in_progress",
        "phases": [],
    }
    atomic_json(idempotency_path, state)
    append_audit(state_root / "audit.jsonl", {**state, "event": "accepted"})
    applied = False
    try:
        for phase in PHASES:
            applied = applied or phase == "apply"
            output = run_adapter(adapter, phase, request_path, operation_dir)
            state["phases"].append(output)
            atomic_json(idempotency_path, state)
            append_audit(state_root / "audit.jsonl", {"requestId": request["id"], "event": "phase", **output})
        state["status"] = "succeeded"
    except ControlError as error:
        state["error"] = str(error)
        if applied:
            try:
                rollback = run_adapter(adapter, "rollback", request_path, operation_dir)
                state["phases"].append(rollback)
                state["status"] = "rolled_back"
            except ControlError as rollback_error:
                state["rollbackError"] = str(rollback_error)
                state["status"] = "recovery_uncertain"
        else:
            state["status"] = "failed"
    atomic_json(idempotency_path, state)
    append_audit(state_root / "audit.jsonl", {**state, "event": "terminal"})
    return state


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("request", type=Path)
    arguments = parser.parse_args()
    try:
        result = execute(arguments.request.resolve())
    except ControlError as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0 if result.get("status") == "succeeded" else 1


if __name__ == "__main__":
    raise SystemExit(main())
