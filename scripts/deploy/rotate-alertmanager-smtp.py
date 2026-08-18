#!/usr/bin/env python3
from __future__ import annotations

import argparse
import getpass
import os
import shutil
import smtplib
import ssl
import stat
import tempfile
from datetime import datetime, timezone
from email.message import EmailMessage
from pathlib import Path
from typing import Callable


DEFAULT_CREDENTIAL = "/etc/observability/alertmanager-smtp-password"
DEFAULT_BACKUP_ROOT = "/var/backups/ops/manual"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate and atomically rotate the root-owned Alertmanager SMTP authorization code."
    )
    parser.add_argument("--credential-path", default=DEFAULT_CREDENTIAL)
    parser.add_argument("--backup-root", default=DEFAULT_BACKUP_ROOT)
    parser.add_argument("--smtp-host", default="smtp.qq.com")
    parser.add_argument("--smtp-port", type=int, default=587)
    parser.add_argument("--username", default="2695266624@qq.com")
    parser.add_argument("--recipient", default="3177348309@qq.com")
    parser.add_argument("--restore-from", type=Path)
    return parser.parse_args()


def validate_authorization_code(value: str) -> str:
    if not 8 <= len(value) <= 128:
        raise ValueError("authorization code length is outside the accepted range")
    if any(character.isspace() for character in value):
        raise ValueError("authorization code must not contain whitespace")
    return value


def require_safe_regular_file(path: Path, expected_uid: int | None = None) -> os.stat_result:
    metadata = path.lstat()
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise ValueError(f"credential must be a regular file without symlinks: {path}")
    if expected_uid is not None and metadata.st_uid != expected_uid:
        raise PermissionError(f"credential owner must be uid {expected_uid}: {path}")
    if stat.S_IMODE(metadata.st_mode) & 0o022:
        raise PermissionError(f"credential must not be group/world writable: {path}")
    return metadata


def verify_smtp_authorization(
    host: str,
    port: int,
    username: str,
    authorization_code: str,
    recipient: str,
    smtp_factory: Callable[..., smtplib.SMTP] = smtplib.SMTP,
) -> None:
    message = EmailMessage()
    message["From"] = username
    message["To"] = recipient
    message["Subject"] = "[AreaSong Ops] SMTP 授权码轮换验证"
    message.set_content(
        "这是一封由 AreaSong Ops 阶段 8 凭据轮换流程发送的验证邮件。\n"
        "收到此邮件表示 QQ SMTP 的 STARTTLS、认证和投递均已通过。\n"
    )

    with smtp_factory(host, port, timeout=20) as client:
        client.ehlo()
        client.starttls(context=ssl.create_default_context())
        client.ehlo()
        # Some QQ edges close the connection instead of returning an actionable
        # rejection for AUTH PLAIN. LOGIN preserves the explicit SMTP status.
        client.user = username
        client.password = authorization_code
        client.auth("LOGIN", client.auth_login, initial_response_ok=False)
        client.send_message(message)


def atomic_replace(path: Path, payload: bytes, mode: int, uid: int, gid: int) -> None:
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(payload)
            handle.flush()
            os.fsync(handle.fileno())
            os.fchmod(handle.fileno(), mode)
            os.fchown(handle.fileno(), uid, gid)
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)


def create_backup(
    path: Path,
    backup_root: Path,
    now: datetime | None = None,
    owner_uid: int = 0,
    owner_gid: int = 0,
) -> Path:
    timestamp = (now or datetime.now(timezone.utc)).strftime("%Y%m%dT%H%M%S.%fZ")
    backup_directory = backup_root / f"alertmanager-smtp-rotation-{timestamp}"
    backup_directory.mkdir(parents=True, mode=0o700)
    backup_directory.chmod(0o700)
    backup = backup_directory / "alertmanager-smtp-password.before"
    shutil.copyfile(path, backup)
    backup.chmod(0o600)
    os.chown(backup, owner_uid, owner_gid)
    return backup


def rotate_credential(
    path: Path,
    backup_root: Path,
    authorization_code: str,
    expected_uid: int = 0,
) -> Path:
    validated = validate_authorization_code(authorization_code)
    metadata = require_safe_regular_file(path, expected_uid=expected_uid)
    backup = create_backup(
        path,
        backup_root,
        owner_uid=metadata.st_uid,
        owner_gid=metadata.st_gid,
    )
    atomic_replace(
        path,
        f"{validated}\n".encode("utf-8"),
        stat.S_IMODE(metadata.st_mode),
        metadata.st_uid,
        metadata.st_gid,
    )
    return backup


def restore_credential(path: Path, backup: Path, expected_uid: int = 0) -> None:
    metadata = require_safe_regular_file(path, expected_uid=expected_uid)
    backup_metadata = require_safe_regular_file(backup, expected_uid=expected_uid)
    if stat.S_IMODE(backup_metadata.st_mode) & 0o077:
        raise PermissionError(f"backup must be root-only: {backup}")
    atomic_replace(
        path,
        backup.read_bytes(),
        stat.S_IMODE(metadata.st_mode),
        metadata.st_uid,
        metadata.st_gid,
    )


def main() -> int:
    args = parse_args()
    if os.geteuid() != 0:
        raise PermissionError("run this credential rotation tool as root")

    credential = Path(args.credential_path)
    if args.restore_from:
        restore_credential(credential, args.restore_from)
        print(f"credential restored atomically from {args.restore_from}")
        print("recreate Alertmanager before considering the rollback complete")
        return 0

    authorization_code = validate_authorization_code(
        getpass.getpass("新的 QQ SMTP 授权码（隐藏输入）: ")
    )
    verify_smtp_authorization(
        args.smtp_host,
        args.smtp_port,
        args.username,
        authorization_code,
        args.recipient,
    )
    backup = rotate_credential(
        credential,
        Path(args.backup_root),
        authorization_code,
    )
    print(f"SMTP validation succeeded; credential switched atomically; backup={backup}")
    print("recreate Alertmanager so the file bind mount uses the new credential inode")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
