#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import fnmatch
import gzip
import hashlib
import json
import os
import shutil
import socket
import subprocess
import tarfile
import tempfile
from dataclasses import asdict, dataclass
from pathlib import Path, PurePosixPath

UTC = dt.timezone.utc
SCHEMA_VERSION = 2
MAX_ARCHIVE_MEMBERS = 200_000
MAX_UNPACKED_SIZE_BYTES = 50 * 1024**3
ARTIFACT_SPECS = (
    ("postgres-sub2api", "postgres/sub2api-postgres-*.sql.gz", "gzip"),
    ("postgres-account-vault", "postgres/account-vault-postgres-1-*.sql.gz", "gzip"),
    ("postgres-areaforge", "postgres/areaforge-postgres-*.sql.gz", "gzip"),
    ("redis", "redis/redis-*.tar.gz", "tar"),
    ("configs", "configs/configs-*.tar.gz", "tar"),
    ("volume-sub2api-data", "volumes/sub2api-data-*.tar.gz", "tar"),
    ("volume-jadeai-data", "volumes/jadeai-data-*.tar.gz", "tar"),
    ("volume-areaforge-uploads", "volumes/areaforge-uploads-*.tar.gz", "tar"),
    ("volume-areaforge-ops-state", "volumes/areaforge-ops-state-*.tar.gz", "tar"),
    ("volume-areasong-ops-state", "volumes/areasong-ops-state-*.tar.gz", "tar"),
)


@dataclass(frozen=True)
class ArtifactRecord:
    role: str
    path: str
    size_bytes: int
    sha256: str
    modified_at: str
    archive_type: str
    member_count: int
    unpacked_size_bytes: int


@dataclass(frozen=True)
class CreateConfig:
    backup_root: Path
    manifest_dir: Path
    metric_out: Path
    host: str
    now: dt.datetime
    window_hours: float
    max_span_hours: float


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def safe_relative_path(root: Path, relative_path: str) -> Path:
    pure = validate_relative_path(relative_path)
    path = root.joinpath(*pure.parts)
    resolved_root = root.resolve()
    resolved_path = path.resolve()
    if resolved_path == resolved_root or resolved_root not in resolved_path.parents:
        raise ValueError(f"artifact escapes backup root: {relative_path}")
    if path.is_symlink():
        raise ValueError(f"artifact must not be a symlink: {relative_path}")
    return path


def validate_relative_path(relative_path: str) -> PurePosixPath:
    pure = PurePosixPath(relative_path)
    if pure.is_absolute() or ".." in pure.parts or not pure.parts:
        raise ValueError(f"unsafe artifact path: {relative_path}")
    return pure


def validate_archive(path: Path, archive_type: str) -> tuple[int, int]:
    if archive_type == "gzip":
        unpacked_size = 0
        with gzip.open(path, "rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                unpacked_size += len(chunk)
                if unpacked_size > MAX_UNPACKED_SIZE_BYTES:
                    raise ValueError(f"archive unpacked byte limit exceeded: {path.name}")
        return 1, unpacked_size
    if archive_type != "tar":
        raise ValueError(f"unknown archive type: {archive_type}")
    with tarfile.open(path, "r|gz") as archive:
        link_paths: set[PurePosixPath] = set()
        member_count = 0
        unpacked_size = 0
        for member in archive:
            member_path = validate_tar_member_name(member.name)
            if member_path is None:
                continue
            member_count += 1
            if member_count > MAX_ARCHIVE_MEMBERS:
                raise ValueError(f"archive member limit exceeded: {path.name}")
            if member.isfile():
                unpacked_size += member.size
                if unpacked_size > MAX_UNPACKED_SIZE_BYTES:
                    raise ValueError(f"archive unpacked byte limit exceeded: {path.name}")
            if any(link_path in member_path.parents for link_path in link_paths):
                raise ValueError(f"archive member traverses an archive link in {path.name}: {member_path}")
            if member.issym() or member.islnk():
                link_paths.add(member_path)
        return member_count, unpacked_size


def validate_tar_member_name(member_name: str) -> PurePosixPath | None:
    if member_name in ("", ".", "./"):
        return None
    member_path = PurePosixPath(member_name)
    if member_path.is_absolute() or ".." in member_path.parts or not member_path.parts:
        raise ValueError(f"unsafe archive member: {member_name}")
    return member_path


def safe_extract_tar(
    archive_path: Path,
    destination: Path,
    members: set[str] | None = None,
    max_members: int | None = None,
    max_bytes: int | None = None,
) -> None:
    requested = {validate_relative_path(member).as_posix() for member in members or set()}
    destination.mkdir(parents=True, exist_ok=True)
    os.chmod(destination, 0o700)
    extracted: set[str] = set()
    seen: set[str] = set()
    extracted_members = 0
    extracted_bytes = 0
    member_limit = MAX_ARCHIVE_MEMBERS if max_members is None else min(max_members, MAX_ARCHIVE_MEMBERS)
    byte_limit = MAX_UNPACKED_SIZE_BYTES if max_bytes is None else min(max_bytes, MAX_UNPACKED_SIZE_BYTES)

    with tarfile.open(archive_path, "r|gz") as archive:
        for member in archive:
            member_path = validate_tar_member_name(member.name)
            if member_path is None:
                continue
            normalized = member_path.as_posix()
            if normalized in seen:
                raise ValueError(f"duplicate archive member: {normalized}")
            seen.add(normalized)
            if requested and normalized not in requested:
                continue
            extracted_members += 1
            if extracted_members > member_limit:
                raise ValueError("archive extraction member limit exceeded")
            if member.issym() or member.islnk():
                raise ValueError(f"archive link member is not extractable: {normalized}")
            if not member.isdir() and not member.isfile():
                raise ValueError(f"unsupported archive member type: {normalized}")

            output_path = destination.joinpath(*member_path.parts)
            output_path.parent.mkdir(parents=True, exist_ok=True)
            if member.isdir():
                output_path.mkdir(parents=True, exist_ok=True)
                os.chmod(output_path, 0o700)
                extracted.add(normalized)
                continue

            extracted_bytes += member.size
            if extracted_bytes > byte_limit:
                raise ValueError("archive extraction byte limit exceeded")

            source = archive.extractfile(member)
            if source is None:
                raise ValueError(f"archive member has no readable content: {normalized}")
            with source, output_path.open("wb") as output:
                shutil.copyfileobj(source, output)
            os.chmod(output_path, 0o600)
            extracted.add(normalized)

    missing = requested - extracted
    if missing:
        raise ValueError(f"requested archive members are missing: {', '.join(sorted(missing))}")


def latest_artifact(
    root: Path,
    pattern: str,
    earliest: float,
    latest: float,
) -> Path:
    candidates = [
        path
        for path in root.glob(pattern)
        if path.is_file() and not path.is_symlink() and earliest <= path.stat().st_mtime <= latest
    ]
    if not candidates:
        raise ValueError(f"required backup artifact not found in window: {pattern}")
    return max(candidates, key=lambda item: item.stat().st_mtime)


def build_artifact_record(root: Path, role: str, path: Path, archive_type: str) -> ArtifactRecord:
    relative_path = path.relative_to(root).as_posix()
    checked_path = safe_relative_path(root, relative_path)
    member_count, unpacked_size = validate_archive(checked_path, archive_type)
    stat = checked_path.stat()
    return ArtifactRecord(
        role=role,
        path=relative_path,
        size_bytes=stat.st_size,
        sha256=sha256_file(checked_path),
        modified_at=dt.datetime.fromtimestamp(stat.st_mtime, UTC).isoformat(),
        archive_type=archive_type,
        member_count=member_count,
        unpacked_size_bytes=unpacked_size,
    )


def docker_inventory() -> list[dict[str, str]]:
    listed = subprocess.run(
        ["docker", "ps", "-a", "--format", "{{.Names}}"],
        check=True,
        capture_output=True,
        text=True,
        timeout=30,
    )
    inventory: list[dict[str, str]] = []
    for name in sorted(line for line in listed.stdout.splitlines() if line):
        inspected = subprocess.run(
            ["docker", "inspect", "-f", "{{.Config.Image}}|{{.Image}}", name],
            check=True,
            capture_output=True,
            text=True,
            timeout=30,
        )
        configured_image, image_id = inspected.stdout.strip().split("|", 1)
        inventory.append({"name": name, "configured_image": configured_image, "image_id": image_id})
    return inventory


def atomic_write(path: Path, content: str, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(content)
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def write_metrics(config: CreateConfig, artifact_count: int, span_seconds: int) -> None:
    lines = [
        "# HELP backup_set_last_success_timestamp Unix timestamp of the latest complete backup set manifest.",
        "# TYPE backup_set_last_success_timestamp gauge",
        f"backup_set_last_success_timestamp {int(config.now.timestamp())}",
        "# HELP backup_set_artifacts Artifacts in the latest complete backup set.",
        "# TYPE backup_set_artifacts gauge",
        f"backup_set_artifacts {artifact_count}",
        "# HELP backup_set_artifact_span_seconds Seconds between the oldest and newest artifact in the set.",
        "# TYPE backup_set_artifact_span_seconds gauge",
        f"backup_set_artifact_span_seconds {span_seconds}",
    ]
    atomic_write(config.metric_out, "\n".join(lines) + "\n", 0o644)


def create_manifest(
    config: CreateConfig,
    runtime_inventory: list[dict[str, str]] | None = None,
) -> Path:
    now = config.now.astimezone(UTC)
    earliest = now.timestamp() - config.window_hours * 3600
    records = [
        build_artifact_record(
            config.backup_root,
            role,
            latest_artifact(config.backup_root, pattern, earliest, now.timestamp() + 60),
            archive_type,
        )
        for role, pattern, archive_type in ARTIFACT_SPECS
    ]
    mtimes = [dt.datetime.fromisoformat(record.modified_at).timestamp() for record in records]
    span_seconds = int(max(mtimes) - min(mtimes))
    if span_seconds > config.max_span_hours * 3600:
        raise ValueError(f"backup artifact span is too large: {span_seconds} seconds")
    set_id = now.strftime("%Y%m%d-%H%M%S")
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "set_id": set_id,
        "host": config.host,
        "created_at": now.isoformat(),
        "artifact_span_seconds": span_seconds,
        "artifacts": [asdict(record) for record in records],
        "runtime_inventory": runtime_inventory if runtime_inventory is not None else docker_inventory(),
    }
    manifest_path = config.manifest_dir / f"backup-set-{set_id}.json"
    atomic_write(manifest_path, json.dumps(manifest, indent=2, sort_keys=True) + "\n", 0o600)
    digest = sha256_file(manifest_path)
    sidecar_path = manifest_path.with_suffix(".json.sha256")
    atomic_write(sidecar_path, f"{digest}  {manifest_path.name}\n", 0o600)
    relative_manifest = manifest_path.relative_to(config.backup_root).as_posix()
    atomic_write(config.manifest_dir / "latest-manifest.txt", relative_manifest + "\n", 0o600)
    write_metrics(config, len(records), span_seconds)
    return manifest_path


def load_manifest(path: Path) -> dict[str, object]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict) or payload.get("schema_version") != SCHEMA_VERSION:
        raise ValueError("unsupported or invalid backup manifest")
    artifacts = payload.get("artifacts")
    if not isinstance(artifacts, list):
        raise ValueError("manifest artifacts must be a list")
    return payload


def verify_sidecar(manifest_path: Path) -> None:
    sidecar = manifest_path.with_suffix(".json.sha256")
    if not sidecar.is_file():
        raise ValueError(f"manifest sidecar missing: {sidecar}")
    parts = sidecar.read_text(encoding="utf-8").strip().split()
    if len(parts) != 2 or parts[1] != manifest_path.name:
        raise ValueError("invalid manifest sidecar")
    if parts[0] != sha256_file(manifest_path):
        raise ValueError("manifest SHA-256 mismatch")


def verify_manifest(
    backup_root: Path,
    manifest_path: Path,
    selected_roles: set[str] | None = None,
) -> list[ArtifactRecord]:
    verify_sidecar(manifest_path)
    payload = load_manifest(manifest_path)
    expected_specs = {role: (pattern, archive_type) for role, pattern, archive_type in ARTIFACT_SPECS}
    required_roles = set(expected_specs)
    manifest_roles = [str(item.get("role", "")) for item in payload["artifacts"] if isinstance(item, dict)]
    if len(manifest_roles) != len(set(manifest_roles)) or set(manifest_roles) != required_roles:
        raise ValueError("manifest does not contain the exact required artifact roles")
    roles_to_verify = selected_roles or required_roles
    if not roles_to_verify <= required_roles:
        raise ValueError("unknown selected artifact role")
    records: list[ArtifactRecord] = []
    for item in payload["artifacts"]:
        if not isinstance(item, dict):
            raise ValueError("invalid artifact record")
        record = ArtifactRecord(**item)
        validate_relative_path(record.path)
        expected_pattern, expected_type = expected_specs[record.role]
        if record.archive_type != expected_type:
            raise ValueError(f"artifact archive type mismatch: {record.role}")
        if not fnmatch.fnmatchcase(record.path, expected_pattern):
            raise ValueError(f"artifact path does not match role: {record.role}")
        if record.member_count < 0 or record.unpacked_size_bytes < 0:
            raise ValueError(f"invalid artifact archive metadata: {record.role}")
        if record.role not in roles_to_verify:
            continue
        path = safe_relative_path(backup_root, record.path)
        if not path.is_file() or path.stat().st_size != record.size_bytes:
            raise ValueError(f"artifact size mismatch: {record.path}")
        if sha256_file(path) != record.sha256:
            raise ValueError(f"artifact SHA-256 mismatch: {record.path}")
        member_count, unpacked_size = validate_archive(path, record.archive_type)
        if member_count != record.member_count or unpacked_size != record.unpacked_size_bytes:
            raise ValueError(f"artifact archive metadata mismatch: {record.path}")
        records.append(record)
    if {record.role for record in records} != roles_to_verify:
        raise ValueError("not all selected artifact roles were verified")
    return records


def list_artifacts(manifest_path: Path, roles: set[str] | None = None) -> list[str]:
    payload = load_manifest(manifest_path)
    selected = []
    for item in payload["artifacts"]:
        if not isinstance(item, dict) or not isinstance(item.get("role"), str):
            raise ValueError("invalid artifact record")
        if roles is None or item["role"] in roles:
            selected.append(validate_relative_path(str(item["path"])).as_posix())
    return selected


def runtime_container_field(manifest_path: Path, name: str, field: str) -> str:
    if field not in {"configured_image", "image_id"}:
        raise ValueError(f"unsupported runtime inventory field: {field}")
    payload = load_manifest(manifest_path)
    inventory = payload.get("runtime_inventory")
    if not isinstance(inventory, list):
        raise ValueError("manifest runtime inventory must be a list")
    matches = [item for item in inventory if isinstance(item, dict) and item.get("name") == name]
    if len(matches) != 1:
        raise ValueError(f"manifest must contain exactly one runtime entry for: {name}")
    value = matches[0].get(field)
    if not isinstance(value, str) or not value:
        raise ValueError(f"manifest runtime entry has no {field}: {name}")
    return value


def artifact_field(manifest_path: Path, role: str, field: str) -> str:
    allowed = {"path", "size_bytes", "member_count", "unpacked_size_bytes"}
    if field not in allowed:
        raise ValueError(f"unsupported artifact field: {field}")
    payload = load_manifest(manifest_path)
    matches = [
        item for item in payload["artifacts"]
        if isinstance(item, dict) and item.get("role") == role
    ]
    if len(matches) != 1:
        raise ValueError(f"manifest must contain exactly one artifact role: {role}")
    value = matches[0].get(field)
    if not isinstance(value, (str, int)):
        raise ValueError(f"manifest artifact has no {field}: {role}")
    return str(value)


def archive_field(path: Path, archive_type: str, field: str) -> str:
    member_count, unpacked_size = validate_archive(path, archive_type)
    values = {
        "member_count": member_count,
        "unpacked_size_bytes": unpacked_size,
    }
    if field not in values:
        raise ValueError(f"unsupported archive field: {field}")
    return str(values[field])


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Create and verify LosAngeles backup set manifests.")
    subparsers = parser.add_subparsers(dest="command", required=True)
    create = subparsers.add_parser("create")
    create.add_argument("--backup-root", default="/var/backups/ops")
    create.add_argument("--manifest-dir")
    create.add_argument("--metric-out", default="/var/lib/node_exporter/textfile_collector/backup-set.prom")
    create.add_argument("--host", default="LosAngeles")
    create.add_argument("--window-hours", type=float, default=12)
    create.add_argument("--max-span-hours", type=float, default=3)
    verify = subparsers.add_parser("verify")
    verify.add_argument("--backup-root", default="/var/backups/ops")
    verify.add_argument("--manifest", required=True)
    verify.add_argument("--role", action="append", default=[])
    listed = subparsers.add_parser("list-artifacts")
    listed.add_argument("--manifest", required=True)
    listed.add_argument("--role", action="append", default=[])
    runtime = subparsers.add_parser("runtime-container")
    runtime.add_argument("--manifest", required=True)
    runtime.add_argument("--name", required=True)
    runtime.add_argument("--field", choices=("configured_image", "image_id"), required=True)
    artifact = subparsers.add_parser("artifact-field")
    artifact.add_argument("--manifest", required=True)
    artifact.add_argument("--role", required=True)
    artifact.add_argument(
        "--field",
        choices=("path", "size_bytes", "member_count", "unpacked_size_bytes"),
        required=True,
    )
    archive = subparsers.add_parser("archive-field")
    archive.add_argument("--archive", required=True)
    archive.add_argument("--type", choices=("gzip", "tar"), required=True)
    archive.add_argument("--field", choices=("member_count", "unpacked_size_bytes"), required=True)
    extract = subparsers.add_parser("extract-tar")
    extract.add_argument("--archive", required=True)
    extract.add_argument("--destination", required=True)
    extract.add_argument("--member", action="append", default=[])
    extract.add_argument("--max-members", type=int)
    extract.add_argument("--max-bytes", type=int)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.command == "create":
        backup_root = Path(args.backup_root)
        manifest_dir = Path(args.manifest_dir) if args.manifest_dir else backup_root / "manifests"
        config = CreateConfig(
            backup_root=backup_root,
            manifest_dir=manifest_dir,
            metric_out=Path(args.metric_out),
            host=args.host,
            now=dt.datetime.now(UTC),
            window_hours=args.window_hours,
            max_span_hours=args.max_span_hours,
        )
        print(create_manifest(config))
    elif args.command == "verify":
        records = verify_manifest(
            Path(args.backup_root),
            Path(args.manifest),
            set(args.role) or None,
        )
        print(f"verified_artifacts={len(records)}")
    elif args.command == "list-artifacts":
        for artifact in list_artifacts(Path(args.manifest), set(args.role) or None):
            print(artifact)
    elif args.command == "runtime-container":
        print(runtime_container_field(Path(args.manifest), args.name, args.field))
    elif args.command == "artifact-field":
        print(artifact_field(Path(args.manifest), args.role, args.field))
    elif args.command == "archive-field":
        print(archive_field(Path(args.archive), args.type, args.field))
    else:
        safe_extract_tar(
            Path(args.archive),
            Path(args.destination),
            set(args.member) or None,
            args.max_members,
            args.max_bytes,
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
