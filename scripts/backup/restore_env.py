#!/usr/bin/env python3
"""Atomically switch fixed data-location keys in a root-only env file."""

from __future__ import annotations

import argparse
import os
import re
import stat
import tempfile
from pathlib import Path


KEY_PATTERN = re.compile(r"^[A-Z][A-Z0-9_]{1,63}$")
VALUE_PATTERN = re.compile(r"^[A-Za-z0-9_./:@+-]{1,512}$")
ASSIGNMENT_PATTERN = re.compile(r"^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=")
MAX_ENV_BYTES = 1024 * 1024


class EnvSwitchError(ValueError):
    pass


def validate_regular_private(path: Path) -> os.stat_result:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise EnvSwitchError("env file is unavailable") from error
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise EnvSwitchError("env file must be a regular non-symlink file")
    if stat.S_IMODE(metadata.st_mode) != 0o600:
        raise EnvSwitchError("env file mode must be 0600")
    expected_owner = 0 if os.geteuid() == 0 else os.geteuid()
    if metadata.st_uid != expected_owner:
        raise EnvSwitchError("env file owner is invalid")
    if metadata.st_size > MAX_ENV_BYTES:
        raise EnvSwitchError("env file is too large")
    return metadata


def parse_updates(values: list[str]) -> dict[str, str]:
    updates: dict[str, str] = {}
    for raw in values:
        key, separator, value = raw.partition("=")
        if not separator or not KEY_PATTERN.fullmatch(key) or not VALUE_PATTERN.fullmatch(value):
            raise EnvSwitchError("env update is invalid")
        if key in updates:
            raise EnvSwitchError(f"env update key is duplicated: {key}")
        updates[key] = value
    if not updates:
        raise EnvSwitchError("at least one env update is required")
    return updates


def render(content: str, updates: dict[str, str]) -> str:
    seen: set[str] = set()
    output: list[str] = []
    for line in content.splitlines():
        match = ASSIGNMENT_PATTERN.match(line)
        key = match.group(1) if match else ""
        if key in updates:
            if key in seen:
                raise EnvSwitchError(f"env file contains a duplicate managed key: {key}")
            output.append(f"{key}={updates[key]}")
            seen.add(key)
        else:
            output.append(line)
    for key in sorted(updates):
        if key not in seen:
            output.append(f"{key}={updates[key]}")
    return "\n".join(output) + "\n"


def read_value(path: Path, key: str, default: str) -> str:
    if not KEY_PATTERN.fullmatch(key) or (default and not VALUE_PATTERN.fullmatch(default)):
        raise EnvSwitchError("env lookup is invalid")
    if not path.exists():
        return default
    validate_regular_private(path)
    try:
        content = path.read_text(encoding="utf-8")
    except UnicodeError as error:
        raise EnvSwitchError("env file is not valid UTF-8") from error
    found: str | None = None
    for line in content.splitlines():
        match = ASSIGNMENT_PATTERN.match(line)
        if not match or match.group(1) != key:
            continue
        if found is not None:
            raise EnvSwitchError(f"env file contains a duplicate managed key: {key}")
        value = line.split("=", 1)[1].strip()
        if not VALUE_PATTERN.fullmatch(value):
            raise EnvSwitchError(f"env value is invalid: {key}")
        found = value
    return default if found is None else found


def write_exclusive(path: Path, content: bytes) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "wb", closefd=False) as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
    finally:
        os.close(descriptor)


def atomic_replace(path: Path, content: bytes, metadata: os.stat_result) -> None:
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.restore-", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o600)
        os.chown(temporary, metadata.st_uid, metadata.st_gid)
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)


def switch_env(path: Path, backup: Path, updates: dict[str, str]) -> None:
    metadata = validate_regular_private(path)
    if backup.is_symlink() or backup.resolve() == path.resolve():
        raise EnvSwitchError("backup path is unsafe")
    if not backup.parent.is_dir() or backup.parent.is_symlink():
        raise EnvSwitchError("backup directory is unsafe")
    original = path.read_bytes()
    try:
        text = original.decode("utf-8")
    except UnicodeDecodeError as error:
        raise EnvSwitchError("env file is not valid UTF-8") from error
    updated = render(text, updates).encode("utf-8")
    write_exclusive(backup, original)
    try:
        atomic_replace(path, updated, metadata)
    except Exception:
        backup.unlink(missing_ok=True)
        raise


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--file", required=True, type=Path)
    parser.add_argument("--backup", type=Path)
    parser.add_argument("--set", action="append", default=[])
    parser.add_argument("--get")
    parser.add_argument("--default", default="")
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    if arguments.get:
        if arguments.backup is not None or arguments.set:
            raise EnvSwitchError("env lookup cannot include switch arguments")
        print(read_value(arguments.file, arguments.get, arguments.default))
        return 0
    if arguments.backup is None:
        raise EnvSwitchError("env switch requires --backup")
    switch_env(arguments.file, arguments.backup, parse_updates(arguments.set))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except EnvSwitchError as error:
        print(f"ERROR: {error}", file=os.sys.stderr)
        raise SystemExit(1)
