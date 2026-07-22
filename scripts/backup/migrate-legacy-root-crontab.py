#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import os
import re
import shlex
import subprocess
import tempfile
from pathlib import Path


MANAGED_LINES = {
    "10 2 * * * /opt/ops/scripts/backup/backup-postgres.sh >> /var/log/backup/postgres.log 2>&1",
    "30 2 * * * /opt/ops/scripts/backup/backup-redis.sh >> /var/log/backup/redis.log 2>&1",
    "0 3 * * * /opt/ops/scripts/backup/backup-configs.sh >> /var/log/backup/configs.log 2>&1",
    "30 3 * * * /opt/ops/scripts/backup/backup-volumes.sh >> /var/log/backup/volumes.log 2>&1",
    "15 4 * * * /opt/ops/scripts/backup/sync-r2.sh >> /var/log/backup/r2.log 2>&1",
    "45 3 * * * /opt/ops/observability/scripts/write-backup-metrics.sh >> /var/log/backup/backup-metrics.log 2>&1",
}
MANAGED_MARKERS = {
    "# BEGIN ops local backups",
    "# END ops local backups",
    "# BEGIN ops offsite backups",
    "# END ops offsite backups",
    "# BEGIN ops observability metrics",
    "# END ops observability metrics",
}


def read_crontab(command: list[str]) -> str:
    result = subprocess.run([*command, "-l"], text=True, capture_output=True, check=False)
    if result.returncode != 0:
        raise RuntimeError(f"cannot read root crontab: {result.stderr.strip()}")
    return result.stdout


def filtered_crontab(content: str) -> tuple[str, int]:
    lines = content.splitlines()
    present = MANAGED_LINES.intersection(lines)
    if present and present != MANAGED_LINES:
        missing = sorted(MANAGED_LINES - present)
        raise RuntimeError(f"legacy backup crontab is only partially present; missing={missing}")
    filtered = [line for line in lines if line not in MANAGED_LINES and line not in MANAGED_MARKERS]
    return "\n".join(filtered).rstrip() + "\n", len(present)


def install_crontab(command: list[str], content: str) -> None:
    descriptor, temporary = tempfile.mkstemp(prefix="ops-root-crontab.")
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(content)
        os.chmod(temporary, 0o600)
        subprocess.run([*command, temporary], check=True, text=True, capture_output=True)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def apply_migration(command: list[str], content: str, filtered: str, backup_dir: Path, release_id: str) -> Path:
    backup_dir.mkdir(parents=True, exist_ok=True)
    os.chmod(backup_dir, 0o700)
    timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%d-%H%M%S")
    backup = backup_dir / f"root-crontab-pre-backup-migration-{timestamp}-{release_id}.txt"
    descriptor = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        handle.write(content)

    try:
        install_crontab(command, filtered)
        if read_crontab(command) != filtered:
            raise RuntimeError("root crontab read-back differs from requested content")
    except Exception:
        install_crontab(command, content)
        raise
    return backup


def main() -> int:
    parser = argparse.ArgumentParser(description="Migrate exact legacy backup jobs out of root crontab.")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check", action="store_true")
    mode.add_argument("--apply", action="store_true")
    parser.add_argument("--backup-dir", default="/var/backups/ops/configs")
    parser.add_argument("--release-id", default="")
    args = parser.parse_args()

    command = shlex.split(os.environ.get("CRONTAB_COMMAND", "/usr/bin/crontab"))
    if not command:
        raise RuntimeError("CRONTAB_COMMAND must not be empty")
    content = read_crontab(command)
    filtered, managed_count = filtered_crontab(content)
    if args.check:
        print(f"legacy_managed_lines={managed_count} migration_required={int(managed_count > 0)}")
        return 0
    if os.geteuid() != 0 and os.environ.get("CRONTAB_MIGRATION_ALLOW_NON_ROOT") != "1":
        raise RuntimeError("root privileges are required to migrate root crontab")
    if not re.fullmatch(r"[0-9a-f]{40}", args.release_id):
        raise RuntimeError("--release-id must be a 40-character lowercase Git commit")
    if managed_count == 0:
        print("legacy backup crontab is already absent")
        return 0
    backup = apply_migration(command, content, filtered, Path(args.backup_dir), args.release_id)
    print(f"migrated_lines={managed_count} backup={backup}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
