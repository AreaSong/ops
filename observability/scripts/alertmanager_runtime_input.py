#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


DEFAULT_CONFIG_ROOT = "/opt/ops/observability/alertmanager"
DEFAULT_CREDENTIAL = "/etc/observability/alertmanager-smtp-password"
DEFAULT_METRICS_URL = "http://127.0.0.1:9093/metrics"
DEFAULT_OUTPUT = "/var/lib/node_exporter/textfile_collector/alertmanager-runtime-input.prom"
REQUIRED_RUNTIME_METRICS = {
    "alertmanager_config_last_reload_success_timestamp_seconds",
    "process_start_time_seconds",
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Detect Alertmanager host inputs that the running container has not loaded."
    )
    parser.add_argument("--validate-only", action="store_true")
    return parser.parse_args()


def parse_prometheus_metrics(payload: str) -> dict[str, float]:
    values: dict[str, float] = {}
    for raw_line in payload.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        fields = line.split()
        if len(fields) != 2 or "{" in fields[0]:
            continue
        try:
            values[fields[0]] = float(fields[1])
        except ValueError:
            continue
    return values


def fetch_runtime_metrics(url: str, timeout: int = 10) -> dict[str, float]:
    request = urllib.request.Request(url, headers={"User-Agent": "areasong-ops/1"})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        payload = response.read().decode("utf-8")
    values = parse_prometheus_metrics(payload)
    missing = REQUIRED_RUNTIME_METRICS - values.keys()
    if missing:
        raise ValueError(f"missing Alertmanager runtime metrics: {','.join(sorted(missing))}")
    return values


def latest_config_mtime(root: Path) -> float:
    if not root.is_dir():
        raise FileNotFoundError(root)
    files = [path for path in root.rglob("*") if path.is_file()]
    if not files:
        raise ValueError(f"Alertmanager config tree is empty: {root}")
    return max(path.stat().st_mtime for path in files)


def evaluate_staleness(
    config_mtime: float,
    credential_mtime: float,
    config_reload_timestamp: float,
    process_start_timestamp: float,
    tolerance_seconds: float = 1.0,
) -> dict[str, int]:
    return {
        "config": int(config_mtime > config_reload_timestamp + tolerance_seconds),
        "credential": int(credential_mtime > process_start_timestamp + tolerance_seconds),
    }


def render_metrics(
    checked_at: int,
    check_success: bool,
    input_mtimes: dict[str, float],
    loaded_timestamps: dict[str, float],
    stale: dict[str, int],
) -> str:
    lines = [
        "# HELP alertmanager_runtime_input_last_check_timestamp_seconds Unix timestamp of the latest runtime input check.",
        "# TYPE alertmanager_runtime_input_last_check_timestamp_seconds gauge",
        f"alertmanager_runtime_input_last_check_timestamp_seconds {checked_at}",
        "# HELP alertmanager_runtime_input_check_success Whether the latest runtime input check completed successfully.",
        "# TYPE alertmanager_runtime_input_check_success gauge",
        f"alertmanager_runtime_input_check_success {int(check_success)}",
        "# HELP alertmanager_runtime_input_mtime_seconds Latest host modification timestamp for an Alertmanager input.",
        "# TYPE alertmanager_runtime_input_mtime_seconds gauge",
        "# HELP alertmanager_runtime_input_loaded_timestamp_seconds Runtime timestamp used to prove an input was loaded.",
        "# TYPE alertmanager_runtime_input_loaded_timestamp_seconds gauge",
        "# HELP alertmanager_runtime_input_stale Whether a newer host input has not been loaded by Alertmanager.",
        "# TYPE alertmanager_runtime_input_stale gauge",
    ]
    for kind in ("config", "credential"):
        lines.append(
            f'alertmanager_runtime_input_mtime_seconds{{kind="{kind}"}} '
            f'{input_mtimes.get(kind, 0):.6f}'
        )
        lines.append(
            f'alertmanager_runtime_input_loaded_timestamp_seconds{{kind="{kind}"}} '
            f'{loaded_timestamps.get(kind, 0):.6f}'
        )
        lines.append(f'alertmanager_runtime_input_stale{{kind="{kind}"}} {stale.get(kind, 0)}')
    return "\n".join(lines) + "\n"


def atomic_write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as handle:
        handle.write(content)
        handle.flush()
        os.fsync(handle.fileno())
        temporary = Path(handle.name)
    temporary.chmod(0o644)
    temporary.replace(path)


def main() -> int:
    args = parse_args()
    if args.validate_only:
        return 0

    config_root = Path(os.environ.get("ALERTMANAGER_CONFIG_ROOT", DEFAULT_CONFIG_ROOT))
    credential = Path(os.environ.get("ALERTMANAGER_CREDENTIAL_PATH", DEFAULT_CREDENTIAL))
    metrics_url = os.environ.get("ALERTMANAGER_METRICS_URL", DEFAULT_METRICS_URL)
    output = Path(os.environ.get("ALERTMANAGER_RUNTIME_INPUT_OUT", DEFAULT_OUTPUT))
    checked_at = int(time.time())
    input_mtimes: dict[str, float] = {}
    loaded_timestamps: dict[str, float] = {}
    stale: dict[str, int] = {}
    check_success = False

    try:
        input_mtimes = {
            "config": latest_config_mtime(config_root),
            "credential": credential.stat().st_mtime,
        }
        runtime = fetch_runtime_metrics(metrics_url)
        loaded_timestamps = {
            "config": runtime["alertmanager_config_last_reload_success_timestamp_seconds"],
            "credential": runtime["process_start_time_seconds"],
        }
        stale = evaluate_staleness(
            input_mtimes["config"],
            input_mtimes["credential"],
            loaded_timestamps["config"],
            loaded_timestamps["credential"],
        )
        check_success = True
    except (OSError, ValueError, urllib.error.URLError):
        stale = {"config": 0, "credential": 0}

    atomic_write(
        output,
        render_metrics(
            checked_at,
            check_success,
            input_mtimes,
            loaded_timestamps,
            stale,
        ),
    )
    return 0 if check_success else 1


if __name__ == "__main__":
    raise SystemExit(main())
