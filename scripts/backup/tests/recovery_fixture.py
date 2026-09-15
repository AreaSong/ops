"""隔离测试夹具；只写调用方传入的临时目录。"""

from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
import tarfile
from datetime import datetime, timedelta, timezone
from pathlib import Path


BEFORE = {
    "currentVersion": "1.0.0",
    "currentImage": "example.invalid/app@sha256:" + "a" * 64,
    "currentImageId": "sha256:" + "a" * 64,
    "runtimeIdentityHash": "sha256:" + "b" * 64,
}


def digest(value: object, *, sorted_keys: bool = False) -> str:
    raw = json.dumps(value, separators=(",", ":"), sort_keys=sorted_keys).encode()
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def archive(path: Path, members: dict[str, bytes]) -> None:
    with tarfile.open(path, "w:gz") as output:
        for name, content in members.items():
            item = tarfile.TarInfo(name)
            item.size, item.mode = len(content), 0o600
            output.addfile(item, io.BytesIO(content))
    path.chmod(0o600)


def write_fixture(root: Path, label: str = "A", service: str = "sub2api", before: dict | None = None, database_name: str | None = None) -> dict:
    before = dict(BEFORE if before is None else before)
    database_name = database_name or service
    if root.resolve() == Path("/"):
        raise ValueError("fixture root cannot be /")
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    postgres = root / f"{service}-postgres-{label}.sql.gz"
    with gzip.open(postgres, "wb") as output:
        output.write(f"SELECT '{label}';\n".encode())
    files = {f"postgres-{service}": postgres}
    if service == "sub2api":
        members = {
            "redis": {"redis_data/dump.rdb": label.encode(), "redis_data/users.acl": b"user default on nopass ~* +@all"},
            "volume-sub2api-data": {"data/item.txt": label.encode()},
        }
        config = {
            "opt/ops/services/sub2api/compose.yml": b"services: {}\n",
            "opt/services/sub2api/compose.yml": b"services: {}\n",
            "opt/services/sub2api/.env": b"POSTGRES_USER=sub2api\nPOSTGRES_DB=sub2api\nPOSTGRES_PASSWORD=fixture-only\n",
        }
    else:
        members = {
            "volume-areaforge-uploads": {"upload.txt": label.encode()},
            "volume-areaforge-ops-state": {"state.json": b"{}\n"},
        }
        config = {
            "opt/ops/services/areaforge/compose.yml": b"services: {}\n",
            "opt/areaforge/docker-compose.prod.yml": b"services: {}\n",
            "opt/areaforge/.env.production": f"POSTGRES_USER={database_name}\nPOSTGRES_DB={database_name}\nPOSTGRES_PASSWORD=fixture-only\n".encode(),
        }
    for role, entries in members.items():
        path = root / f"{role}-{label}.tar.gz"
        archive(path, entries)
        files[role] = path
    configs = root / f"config-{label}.tar.gz"
    archive(configs, config)
    files["configs"] = configs
    containers = {
        "app": {"name": service, "configured_image": before["currentImage"], "image_id": before["currentImageId"],
                "container_id": "app-id", "version": before["currentVersion"], "revision": "c" * 40},
        "postgres": {"name": f"{service}-postgres", "configured_image": "postgres@sha256:" + "d" * 64,
                     "image_id": "sha256:" + "d" * 64, "container_id": "postgres-id"},
    }
    if service == "sub2api":
        containers["redis"] = {"name": "sub2api-redis", "configured_image": "redis@sha256:" + "e" * 64,
                               "image_id": "sha256:" + "e" * 64, "container_id": "redis-id"}
    runtime = {"schemaVersion": 1, "service": service, "containers": containers,
               "database": {"user": database_name, "database": database_name, "migrations": 1}}
    runtime_file = root / f"runtime-{label}.json"
    runtime_file.write_text(json.dumps(runtime), encoding="utf-8")
    files["runtime-snapshot"] = runtime_file
    artifacts = []
    for role, path in sorted(files.items()):
        path.chmod(0o600)
        artifacts.append({"role": role, "path": str(path.resolve()), "sizeBytes": path.stat().st_size,
                          "sha256": "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()})
    return {"artifacts": artifacts, "before": before, "runtime": runtime}


def make_contract(fixture: dict, service: str = "sub2api", *, mode: str = "isolated") -> dict:
    now = datetime.now(timezone.utc)
    point_id = "11111111-1111-4111-8111-111111111111"
    task_id = "55555555-5555-4555-8555-555555555555"
    before = digest(fixture["before"], sorted_keys=True)
    roles = sorted(item["role"] for item in fixture["artifacts"])
    evidence = {
        "schemaVersion": 1, "service": service, "taskId": task_id, "tenantId": "production",
        "serverId": "local", "expectedBeforeDigest": before,
        "createdAt": now.isoformat(), "artifacts": fixture["artifacts"],
    }
    binding = digest({
        "schemaVersion": 1, "service": service, "taskId": task_id, "tenantId": "production",
        "serverId": "local", "expectedBeforeDigest": before, "evidenceDigest": digest(evidence), "requiredRoles": roles,
    })
    # Go 结构中的 bindingDigest 位于 createdAt/artifacts 之前。
    evidence = {**{key: value for key, value in evidence.items() if key not in {"createdAt", "artifacts"}},
                "bindingDigest": binding, "createdAt": evidence["createdAt"], "artifacts": evidence["artifacts"]}
    evidence_digest = digest(evidence)
    point = {
        "id": point_id, "taskId": task_id, "service": service, "status": "verified", "tenantId": "production",
        "serverId": "local", "bindingDigest": binding, "evidenceDigest": evidence_digest,
        "expectedBeforeDigest": before, "expectedBefore": fixture["before"],
        "recoverableUntil": (now + timedelta(hours=1)).isoformat(), "requiredArtifactRoles": roles, "evidence": evidence,
    }
    return {
        "schemaVersion": 1, "taskId": "22222222-2222-4222-8222-222222222222",
        "planId": "33333333-3333-4333-8333-333333333333", "service": service, "mode": mode,
        "tenantId": "production", "serverId": "local", "recoveryPointId": point_id,
        "bindingDigest": binding, "evidenceDigest": evidence_digest, "expectedBeforeDigest": before,
        "revalidatedAt": now.isoformat(), "recoveryPoint": point,
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--label", default="A", choices=("A", "B"))
    parser.add_argument("--service", default="sub2api", choices=("sub2api", "areaforge"))
    args = parser.parse_args()
    print(json.dumps(write_fixture(args.root, args.label, args.service)))
