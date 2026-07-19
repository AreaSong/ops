#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import gzip
import hashlib
import json
import os
import re
import shutil
import socket
import tarfile
import tempfile
import uuid
from dataclasses import asdict, dataclass
from pathlib import Path, PurePosixPath

UTC = dt.timezone.utc
SCHEMA_VERSION = 1
DEFAULT_PART_SIZE = 64 * 1024 * 1024
MAX_PAYLOAD_BYTES = 5 * 1024**3
MAX_ARCHIVE_MEMBERS = 10_000
MAX_SOURCE_FILE_BYTES = 1 * 1024**3
MAX_SOURCE_TOTAL_BYTES = 2 * 1024**3
MAX_LOG_LINE_BYTES = 16 * 1024**1024
READ_CHUNK_BYTES = 1024 * 1024
ZERO_SHA256 = "0" * 64
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
ARCHIVE_ID_RE = re.compile(r"^(?P<date>[0-9]{8})-[0-9]{12}Z-[0-9a-f]{8}$")
MANIFEST_PATH_RE = re.compile(
    r"^(?P<year>[0-9]{4})/(?P<month>[0-9]{2})/(?P<day>[0-9]{2})/"
    r"(?P<archive_id>[0-9]{8}-[0-9]{12}Z-[0-9a-f]{8})/manifest\.json$"
)
AUDIT_TIMESTAMP_RE = re.compile(r"audit\((?P<epoch>\d+(?:\.\d+)?):")
NGINX_ACCESS_TIMESTAMP_RE = re.compile(r"\[(?P<value>[^\]]+)\]")
NGINX_ERROR_TIMESTAMP_RE = re.compile(r"^(?P<value>\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})")
SYSLOG_TIMESTAMP_RE = re.compile(r"^(?P<value>[A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2})")
LOCAL_TZ = dt.datetime.now().astimezone().tzinfo or UTC


@dataclass(frozen=True)
class FileRecord:
    path: str
    size_bytes: int
    sha256: str
    line_count: int


@dataclass(frozen=True)
class PartRecord:
    path: str
    size_bytes: int
    sha256: str


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def atomic_write(path: Path, content: bytes, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    with temporary.open("wb") as handle:
        handle.write(content)
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(temporary, mode)
    os.replace(temporary, path)


def iter_log_lines(path: Path, max_bytes: int):
    opener = gzip.open if path.suffix == ".gz" else open
    consumed = 0
    pending = b""
    with opener(path, "rb") as handle:
        while chunk := handle.read(READ_CHUNK_BYTES):
            consumed += len(chunk)
            if consumed > max_bytes:
                raise ValueError(f"uncompressed log source exceeds {max_bytes} bytes: {path}")
            pending += chunk
            while True:
                newline = pending.find(b"\n")
                if newline < 0:
                    if len(pending) > MAX_LOG_LINE_BYTES:
                        raise ValueError(f"log line exceeds {MAX_LOG_LINE_BYTES} bytes: {path}")
                    break
                yield pending[: newline + 1]
                pending = pending[newline + 1 :]
        if pending:
            yield pending


def parse_audit_timestamp(line: str, _target_day: dt.date) -> dt.datetime | None:
    match = AUDIT_TIMESTAMP_RE.search(line)
    if not match:
        return None
    return dt.datetime.fromtimestamp(float(match.group("epoch")), UTC)


def parse_auth_timestamp(line: str, target_day: dt.date) -> dt.datetime | None:
    token = line.split(" ", 1)[0]
    if "T" in token:
        try:
            parsed = dt.datetime.fromisoformat(token.replace("Z", "+00:00"))
        except ValueError:
            parsed = None
        if parsed is not None:
            if parsed.tzinfo is None:
                parsed = parsed.replace(tzinfo=LOCAL_TZ)
            return parsed.astimezone(UTC)

    match = SYSLOG_TIMESTAMP_RE.match(line)
    if not match:
        return None
    try:
        parsed = dt.datetime.strptime(
            f"{target_day.year} {match.group('value')}",
            "%Y %b %d %H:%M:%S",
        )
    except ValueError:
        return None
    return parsed.replace(tzinfo=LOCAL_TZ).astimezone(UTC)


def parse_nginx_access_timestamp(line: str, _target_day: dt.date) -> dt.datetime | None:
    match = NGINX_ACCESS_TIMESTAMP_RE.search(line)
    if not match:
        return None
    try:
        return dt.datetime.strptime(match.group("value"), "%d/%b/%Y:%H:%M:%S %z").astimezone(UTC)
    except ValueError:
        return None


def parse_nginx_error_timestamp(line: str, _target_day: dt.date) -> dt.datetime | None:
    match = NGINX_ERROR_TIMESTAMP_RE.match(line)
    if not match:
        return None
    try:
        return (
            dt.datetime.strptime(match.group("value"), "%Y/%m/%d %H:%M:%S")
            .replace(tzinfo=LOCAL_TZ)
            .astimezone(UTC)
        )
    except ValueError:
        return None


def source_files(pattern: str) -> list[Path]:
    paths = []
    for path in sorted(Path("/").glob(pattern.lstrip("/"))):
        if path.is_symlink():
            raise ValueError(f"log source must not be a symlink: {path}")
        if path.is_file():
            paths.append(path)
    return paths


def rooted_source_files(root: Path, relative_pattern: str) -> list[Path]:
    paths = []
    for path in sorted(root.glob(relative_pattern)):
        if path.is_symlink():
            raise ValueError(f"log source must not be a symlink: {path}")
        if path.is_file():
            paths.append(path)
    return paths


def filter_log_lines(
    paths: list[Path],
    destination: Path,
    parser,
    target_day: dt.date,
    initial_source_bytes: int = 0,
) -> tuple[int, int, int]:
    start = dt.datetime.combine(target_day, dt.time.min, UTC)
    end = start + dt.timedelta(days=1)
    line_count = 0
    source_count = 0
    source_bytes_total = initial_source_bytes
    with destination.open("w", encoding="utf-8") as output:
        for path in paths:
            source_count += 1
            remaining = MAX_SOURCE_TOTAL_BYTES - source_bytes_total
            if remaining <= 0:
                raise ValueError(f"total uncompressed log source exceeds {MAX_SOURCE_TOTAL_BYTES} bytes")
            for raw_line in iter_log_lines(path, min(MAX_SOURCE_FILE_BYTES, remaining)):
                source_bytes_total += len(raw_line)
                line = raw_line.decode("utf-8", errors="replace")
                timestamp = parser(line, target_day)
                if timestamp is None or not start <= timestamp < end:
                    continue
                output.write(line if line.endswith("\n") else f"{line}\n")
                line_count += 1
    os.chmod(destination, 0o600)
    return source_count, line_count, source_bytes_total


def collect_payload(source_root: Path, payload_dir: Path, target_day: dt.date) -> dict[str, dict[str, int]]:
    payload_dir.mkdir(parents=True, mode=0o700, exist_ok=True)
    os.chmod(payload_dir, 0o700)
    audit_paths = rooted_source_files(source_root, "var/log/audit/audit.log*")
    auth_paths = rooted_source_files(source_root, "var/log/auth.log*")
    nginx_access_paths = rooted_source_files(source_root, "var/log/nginx/access*.log*")
    nginx_error_paths = rooted_source_files(source_root, "var/log/nginx/error*.log*")

    results = {}
    source_bytes_total = 0
    for name, paths, parser in (
        ("auditd", audit_paths, parse_audit_timestamp),
        ("auth", auth_paths, parse_auth_timestamp),
        ("nginx-access", nginx_access_paths, parse_nginx_access_timestamp),
        ("nginx-error", nginx_error_paths, parse_nginx_error_timestamp),
    ):
        source_count, line_count, source_bytes_total = filter_log_lines(
            paths,
            payload_dir / f"{name}.log",
            parser,
            target_day,
            source_bytes_total,
        )
        results[name] = {
            "source_files": source_count,
            "records": line_count,
            "source_bytes_total": source_bytes_total,
        }

    report_name = f"daily-ops-audit-{target_day.isoformat()}.md"
    report_source = source_root / "var/log/observability" / report_name
    if not report_source.is_file() or report_source.is_symlink():
        raise ValueError(f"daily operations report is missing: {report_source}")
    report_size = report_source.stat().st_size
    if report_size > MAX_SOURCE_FILE_BYTES:
        raise ValueError(f"daily operations report exceeds {MAX_SOURCE_FILE_BYTES} bytes")
    if source_bytes_total + report_size > MAX_SOURCE_TOTAL_BYTES:
        raise ValueError(f"total uncompressed log source exceeds {MAX_SOURCE_TOTAL_BYTES} bytes")
    report_destination = payload_dir / report_name
    shutil.copyfile(report_source, report_destination)
    os.chmod(report_destination, 0o600)
    with report_destination.open("r", encoding="utf-8", errors="replace") as report_handle:
        report_lines = sum(1 for _ in report_handle)
    results["daily-report"] = {
        "source_files": 1,
        "records": report_lines,
        "source_bytes_total": source_bytes_total + report_size,
    }
    return results


def file_record(path: Path, root: Path) -> FileRecord:
    with path.open("rb") as handle:
        line_count = sum(1 for _ in handle)
    return FileRecord(
        path=path.relative_to(root).as_posix(),
        size_bytes=path.stat().st_size,
        sha256=sha256_file(path),
        line_count=line_count,
    )


def create_payload_archive(payload_dir: Path, archive_path: Path, mtime: int) -> list[FileRecord]:
    files = [path for path in sorted(payload_dir.iterdir()) if path.is_file() and not path.is_symlink()]
    if not files:
        raise ValueError("compliance archive payload is empty")
    records = [file_record(path, payload_dir) for path in files]
    with archive_path.open("wb") as raw_output:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw_output, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w|") as archive:
                for path in files:
                    info = archive.gettarinfo(str(path), arcname=f"payload/{path.name}")
                    info.uid = 0
                    info.gid = 0
                    info.uname = "root"
                    info.gname = "root"
                    info.mode = 0o600
                    info.mtime = mtime
                    with path.open("rb") as source:
                        archive.addfile(info, source)
    os.chmod(archive_path, 0o600)
    return records


def split_archive(archive_path: Path, output_dir: Path, part_size: int) -> list[PartRecord]:
    if part_size <= 0 or part_size > DEFAULT_PART_SIZE:
        raise ValueError(f"part size must be between 1 and {DEFAULT_PART_SIZE} bytes")
    parts = []
    with archive_path.open("rb") as source:
        index = 0
        while chunk := source.read(part_size):
            part_path = output_dir / f"payload.tar.gz.part-{index:05d}"
            atomic_write(part_path, chunk)
            parts.append(
                PartRecord(
                    path=part_path.name,
                    size_bytes=len(chunk),
                    sha256=hashlib.sha256(chunk).hexdigest(),
                )
            )
            index += 1
    if not parts:
        raise ValueError("payload archive produced no upload parts")
    return parts


def parse_utc_datetime(value: str) -> dt.datetime:
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("datetime must include a timezone")
    return parsed.astimezone(UTC)


def build_archive(args: argparse.Namespace) -> Path:
    target_day = dt.date.fromisoformat(args.date)
    created_at = parse_utc_datetime(args.created_at) if args.created_at else dt.datetime.now(UTC)
    previous_sha256 = args.previous_manifest_sha256.lower()
    if not SHA256_RE.fullmatch(previous_sha256):
        raise ValueError("previous manifest SHA-256 must contain 64 lowercase hexadecimal characters")
    archive_id = args.archive_id or (
        f"{target_day:%Y%m%d}-{created_at:%H%M%S%f}Z-{uuid.uuid4().hex[:8]}"
    )
    if not re.fullmatch(r"[0-9]{8}-[0-9]{12}Z-[0-9a-f]{8}", archive_id):
        raise ValueError("invalid archive id")

    archive_dir = Path(args.output_root) / f"{target_day:%Y/%m/%d}" / archive_id
    archive_dir.mkdir(parents=True, mode=0o700)
    os.chmod(archive_dir, 0o700)
    if any(archive_dir.iterdir()):
        raise ValueError(f"archive directory is not empty: {archive_dir}")

    with tempfile.TemporaryDirectory(prefix="payload-", dir=archive_dir) as temporary:
        payload_dir = Path(temporary)
        source_summary = collect_payload(Path(args.source_root), payload_dir, target_day)
        payload_archive = archive_dir / ".payload.tar.gz.tmp"
        end = dt.datetime.combine(target_day + dt.timedelta(days=1), dt.time.min, UTC)
        file_records = create_payload_archive(payload_dir, payload_archive, int(end.timestamp()))
        payload_sha256 = sha256_file(payload_archive)
        payload_size = payload_archive.stat().st_size
        if payload_size > MAX_PAYLOAD_BYTES:
            raise ValueError(f"compliance payload exceeds {MAX_PAYLOAD_BYTES} bytes")
        parts = split_archive(payload_archive, archive_dir, args.part_size_bytes)
        payload_archive.unlink()

    manifest = {
        "schema_version": SCHEMA_VERSION,
        "archive_id": archive_id,
        "host": args.host,
        "day": target_day.isoformat(),
        "range_start": dt.datetime.combine(target_day, dt.time.min, UTC).isoformat(),
        "range_end": dt.datetime.combine(target_day + dt.timedelta(days=1), dt.time.min, UTC).isoformat(),
        "created_at": created_at.isoformat(),
        "previous_manifest_sha256": previous_sha256,
        "payload": {
            "size_bytes": payload_size,
            "sha256": payload_sha256,
            "files": [asdict(record) for record in file_records],
            "parts": [asdict(record) for record in parts],
        },
        "sources": source_summary,
    }
    manifest_path = archive_dir / "manifest.json"
    manifest_bytes = (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode()
    atomic_write(manifest_path, manifest_bytes)
    sidecar = f"{hashlib.sha256(manifest_bytes).hexdigest()}  manifest.json\n".encode()
    atomic_write(archive_dir / "manifest.json.sha256", sidecar)
    return archive_dir


def load_manifest(path: Path) -> dict:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if payload.get("schema_version") != SCHEMA_VERSION:
        raise ValueError("unsupported compliance archive manifest schema")
    if not SHA256_RE.fullmatch(str(payload.get("previous_manifest_sha256", ""))):
        raise ValueError("manifest has an invalid previous SHA-256")
    validate_manifest_fields(payload)
    return payload


def validate_manifest_fields(payload: dict) -> None:
    archive_id = str(payload.get("archive_id", ""))
    archive_match = ARCHIVE_ID_RE.fullmatch(archive_id)
    if archive_match is None:
        raise ValueError("manifest has an invalid archive id")
    try:
        target_day = dt.date.fromisoformat(str(payload["day"]))
        range_start = parse_utc_datetime(str(payload["range_start"]))
        range_end = parse_utc_datetime(str(payload["range_end"]))
        created_at = parse_utc_datetime(str(payload["created_at"]))
    except (KeyError, TypeError, ValueError) as error:
        raise ValueError("manifest has invalid date or timestamp fields") from error

    if archive_match.group("date") != target_day.strftime("%Y%m%d"):
        raise ValueError("manifest archive id does not match its day")
    expected_start = dt.datetime.combine(target_day, dt.time.min, UTC)
    expected_end = expected_start + dt.timedelta(days=1)
    if range_start != expected_start or range_end != expected_end:
        raise ValueError("manifest UTC range does not match its day")
    if not expected_start <= created_at:
        raise ValueError("manifest created_at must not precede its archived day")


def validate_member_name(name: str) -> PurePosixPath:
    path = PurePosixPath(name)
    if path.is_absolute() or ".." in path.parts or len(path.parts) != 2 or path.parts[0] != "payload":
        raise ValueError(f"unsafe compliance archive member: {name}")
    return path


def verify_archive_dir(archive_dir: Path, expected_archive_id: str | None = None) -> dict:
    manifest_path = archive_dir / "manifest.json"
    sidecar_path = archive_dir / "manifest.json.sha256"
    expected_sidecar = f"{sha256_file(manifest_path)}  manifest.json\n"
    if sidecar_path.read_text(encoding="utf-8") != expected_sidecar:
        raise ValueError("compliance archive manifest sidecar mismatch")
    manifest = load_manifest(manifest_path)
    if expected_archive_id is not None and manifest["archive_id"] != expected_archive_id:
        raise ValueError("manifest archive id does not match its directory")

    payload = manifest["payload"]
    if int(payload.get("size_bytes", 0)) <= 0 or int(payload["size_bytes"]) > MAX_PAYLOAD_BYTES:
        raise ValueError("compliance payload size is outside the allowed bound")
    part_paths = []
    for part in payload["parts"]:
        path = archive_dir / part["path"]
        if not path.is_file() or path.is_symlink():
            raise ValueError(f"archive part is missing or unsafe: {part['path']}")
        if path.stat().st_size != part["size_bytes"] or sha256_file(path) != part["sha256"]:
            raise ValueError(f"archive part verification failed: {part['path']}")
        part_paths.append(path)

    with tempfile.NamedTemporaryFile(prefix="compliance-archive-", suffix=".tar.gz") as rebuilt:
        digest = hashlib.sha256()
        total_size = 0
        for part_path in part_paths:
            with part_path.open("rb") as source:
                for chunk in iter(lambda: source.read(1024 * 1024), b""):
                    if total_size + len(chunk) > MAX_PAYLOAD_BYTES:
                        raise ValueError("compliance payload part size limit exceeded")
                    rebuilt.write(chunk)
                    digest.update(chunk)
                    total_size += len(chunk)
        rebuilt.flush()
        if total_size != payload["size_bytes"] or digest.hexdigest() != payload["sha256"]:
            raise ValueError("rebuilt compliance payload does not match the manifest")

        expected_files = {record["path"]: record for record in payload["files"]}
        observed_files = {}
        member_count = 0
        unpacked_size = 0
        with tarfile.open(rebuilt.name, "r|gz") as archive:
            for member in archive:
                member_count += 1
                if member_count > MAX_ARCHIVE_MEMBERS:
                    raise ValueError("compliance archive member limit exceeded")
                member_path = validate_member_name(member.name)
                if not member.isfile() or member.issym() or member.islnk():
                    raise ValueError(f"unsupported compliance archive member: {member.name}")
                relative = member_path.parts[1]
                if relative in observed_files:
                    raise ValueError(f"duplicate compliance archive member: {relative}")
                source = archive.extractfile(member)
                if source is None:
                    raise ValueError(f"unreadable compliance archive member: {relative}")
                digest = hashlib.sha256()
                size = 0
                line_count = 0
                for chunk in iter(lambda: source.read(1024 * 1024), b""):
                    digest.update(chunk)
                    size += len(chunk)
                    unpacked_size += len(chunk)
                    if unpacked_size > MAX_PAYLOAD_BYTES:
                        raise ValueError("compliance archive unpacked size limit exceeded")
                    line_count += chunk.count(b"\n")
                observed_files[relative] = {
                    "path": relative,
                    "size_bytes": size,
                    "sha256": digest.hexdigest(),
                    "line_count": line_count,
                }
        if observed_files != expected_files:
            raise ValueError("compliance payload file inventory does not match the manifest")
    return manifest


def verify_chain(manifest_root: Path) -> int:
    manifests = []
    seen_days = set()
    for path in manifest_root.rglob("manifest.json"):
        relative_path = path.relative_to(manifest_root).as_posix()
        path_match = MANIFEST_PATH_RE.fullmatch(relative_path)
        if path_match is None:
            raise ValueError(f"invalid compliance archive manifest path: {relative_path}")
        sidecar = path.with_name("manifest.json.sha256")
        if not sidecar.is_file():
            raise ValueError(f"manifest sidecar is missing: {path}")
        if sidecar.read_text(encoding="utf-8") != f"{sha256_file(path)}  manifest.json\n":
            raise ValueError(f"manifest sidecar mismatch: {path}")
        payload = load_manifest(path)
        if payload["archive_id"] != path_match.group("archive_id"):
            raise ValueError(f"manifest archive id does not match its path: {path}")
        path_day = dt.date(
            int(path_match.group("year")),
            int(path_match.group("month")),
            int(path_match.group("day")),
        )
        if payload["day"] != path_day.isoformat():
            raise ValueError(f"manifest day does not match its path: {path}")
        if path_day in seen_days:
            raise ValueError(f"duplicate compliance archive day: {path_day}")
        seen_days.add(path_day)
        manifests.append((path_day, path, payload))
    manifests.sort(key=lambda item: item[0])
    if not manifests:
        raise ValueError("no compliance archive manifests found")
    previous = ZERO_SHA256
    previous_day = None
    for current_day, path, payload in manifests:
        if previous_day is not None:
            expected_day = previous_day + dt.timedelta(days=1)
            if current_day != expected_day:
                raise ValueError(
                    f"compliance archive day gap: expected={expected_day} actual={current_day}"
                )
        if payload["previous_manifest_sha256"] != previous:
            raise ValueError(f"compliance archive hash chain is broken at {path}")
        previous = sha256_file(path)
        previous_day = current_day
    return len(manifests)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Build and verify daily compliance log archives.")
    subparsers = parser.add_subparsers(dest="command", required=True)

    build = subparsers.add_parser("build")
    build.add_argument("--date", required=True)
    build.add_argument("--source-root", default="/")
    build.add_argument("--output-root", default="/var/backups/ops/compliance-logs")
    build.add_argument("--host", default=socket.gethostname())
    build.add_argument("--previous-manifest-sha256", default=ZERO_SHA256)
    build.add_argument("--created-at")
    build.add_argument("--archive-id")
    build.add_argument("--part-size-bytes", type=int, default=DEFAULT_PART_SIZE)

    verify = subparsers.add_parser("verify")
    verify.add_argument("--archive-dir", required=True)
    verify.add_argument("--expected-archive-id")

    manifest_sha = subparsers.add_parser("manifest-sha")
    manifest_sha.add_argument("--manifest", required=True)

    list_parts = subparsers.add_parser("list-parts")
    list_parts.add_argument("--manifest", required=True)

    field = subparsers.add_parser("field")
    field.add_argument("--manifest", required=True)
    field.add_argument("--name", choices=("day", "archive_id", "host"), required=True)

    chain = subparsers.add_parser("verify-chain")
    chain.add_argument("--manifest-root", required=True)
    return parser


def main() -> int:
    args = build_parser().parse_args()
    if args.command == "build":
        print(build_archive(args))
    elif args.command == "verify":
        manifest = verify_archive_dir(Path(args.archive_dir), args.expected_archive_id)
        print(f"verified archive={manifest['archive_id']} day={manifest['day']}")
    elif args.command == "manifest-sha":
        print(sha256_file(Path(args.manifest)))
    elif args.command == "list-parts":
        for part in load_manifest(Path(args.manifest))["payload"]["parts"]:
            print(part["path"])
    elif args.command == "field":
        print(load_manifest(Path(args.manifest))[args.name])
    elif args.command == "verify-chain":
        print(f"verified manifests={verify_chain(Path(args.manifest_root))}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
