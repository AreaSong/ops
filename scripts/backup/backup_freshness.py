"""仅为每日备份成功发布校验时效；历史恢复验证不受此限制。"""

from __future__ import annotations

import argparse
import datetime as dt
from pathlib import Path

import backup_manifest


MAX_AGE = dt.timedelta(hours=24)
MAX_SPAN = dt.timedelta(hours=3)
CLOCK_SKEW = dt.timedelta(seconds=60)


def timestamp(value: object) -> dt.datetime:
    if not isinstance(value, str):
        raise ValueError("backup freshness: timestamp is missing")
    try:
        result = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise ValueError("backup freshness: invalid timestamp") from error
    if result.tzinfo is None:
        raise ValueError("backup freshness: timestamp requires timezone")
    return result.astimezone(dt.timezone.utc)


def validate(payload: dict, now: dt.datetime | None = None) -> None:
    now = now or dt.datetime.now(dt.timezone.utc)
    created = timestamp(payload.get("created_at"))
    records = payload.get("artifacts", [])
    roles = [item.get("role") for item in records if isinstance(item, dict)]
    required = {role for role, _, _ in backup_manifest.ARTIFACT_SPECS}
    if len(records) != len(required) or len(roles) != len(required) or set(roles) != required:
        raise ValueError("backup freshness: exact artifact roles are required")
    modified = [timestamp(item.get("modified_at")) for item in records]
    for value in [created, *modified]:
        if not -CLOCK_SKEW <= now - value <= MAX_AGE:
            raise ValueError("backup freshness: manifest or artifact is stale or future-dated")
    if max(modified) > created + CLOCK_SKEW:
        raise ValueError("backup freshness: artifact is newer than its manifest")
    span = max(modified) - min(modified)
    recorded = payload.get("artifact_span_seconds")
    if type(recorded) is not int or span > MAX_SPAN or recorded != int(span.total_seconds()):
        raise ValueError("backup freshness: artifact time span is invalid")


def verify(path: Path) -> None:
    backup_manifest.verify_sidecar(path)
    validate(backup_manifest.load_manifest(path))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    verify(parser.parse_args().manifest)


if __name__ == "__main__":
    main()
