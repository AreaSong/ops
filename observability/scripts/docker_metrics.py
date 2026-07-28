#!/usr/bin/env python3
from __future__ import annotations

import json
import os
import re
import subprocess
import tempfile
import time
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

DEFAULT_CONTAINERS = (
    "resume-jadeai-app-1",
    "sub2api",
    "sub2api-redis",
    "sub2api-postgres",
    "account-vault-web-1",
    "account-vault-postgres-1",
    "areaforge-web",
    "areaforge-postgres",
    "prometheus",
    "grafana",
    "alertmanager",
    "loki",
    "promtail",
    "node-exporter",
    "blackbox-exporter",
    "postgres-exporter-sub2api",
    "postgres-exporter-account-vault",
    "redis-exporter-sub2api",
)

CONTAINER_SERVICES = {
    "resume-jadeai-app-1": "resume-jadeai",
    "sub2api": "sub2api",
    "sub2api-redis": "sub2api",
    "sub2api-postgres": "sub2api",
    "postgres-exporter-sub2api": "sub2api",
    "redis-exporter-sub2api": "sub2api",
    "account-vault-web-1": "account-vault",
    "account-vault-postgres-1": "account-vault",
    "postgres-exporter-account-vault": "account-vault",
    "areaforge-web": "areaforge",
    "areaforge-postgres": "areaforge",
    "prometheus": "observability",
    "grafana": "observability",
    "alertmanager": "observability",
    "loki": "observability",
    "promtail": "observability",
    "node-exporter": "observability",
    "blackbox-exporter": "observability",
}

SIZE_RE = re.compile(r"^([0-9]+(?:\.[0-9]+)?)([A-Za-z]+)$")
SIZE_FACTORS = {
    "B": 1,
    "kB": 1000,
    "MB": 1000**2,
    "GB": 1000**3,
    "TB": 1000**4,
    "KiB": 1024,
    "MiB": 1024**2,
    "GiB": 1024**3,
    "TiB": 1024**4,
}


@dataclass
class ContainerMetrics:
    known: bool = False
    stats_valid: bool = False
    running: int = 0
    restart_count: int = 0
    oom_killed: int = 0
    started_at_timestamp_seconds: float = 0
    health: str = "missing"
    memory_limit_configured: float = 0
    cpu_limit_cores: float = 0
    pids_limit: float = 0
    cpu_usage_ratio: float = 0
    cpu_limit_usage_ratio: float = 0
    memory_usage_bytes: float = 0
    memory_limit_bytes: float = 0
    memory_usage_ratio: float = 0
    pids: float = 0
    pids_usage_ratio: float = 0


@dataclass
class DockerStorageMetrics:
    size_bytes: float
    reclaimable_bytes: float
    total_items: float
    active_items: float


def run_docker(arguments: list[str]) -> subprocess.CompletedProcess[str]:
    command = ["docker", *arguments]
    try:
        return subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except FileNotFoundError as exc:
        return subprocess.CompletedProcess(command, 127, "", str(exc))
    except subprocess.TimeoutExpired as exc:
        return subprocess.CompletedProcess(command, 124, exc.stdout or "", exc.stderr or str(exc))


def parse_size(raw: str) -> float:
    match = SIZE_RE.fullmatch(raw.strip())
    if match is None or match.group(2) not in SIZE_FACTORS:
        raise ValueError(f"unsupported Docker size: {raw}")
    return float(match.group(1)) * SIZE_FACTORS[match.group(2)]


def collect_storage_metrics() -> dict[str, DockerStorageMetrics]:
    result = run_docker(["system", "df", "--format", "{{json .}}"])
    if result.returncode != 0:
        raise RuntimeError("docker system df failed")
    storage: dict[str, DockerStorageMetrics] = {}
    type_names = {
        "Images": "images",
        "Containers": "containers",
        "Local Volumes": "local_volumes",
        "Build Cache": "build_cache",
    }
    for line in result.stdout.splitlines():
        row = json.loads(line)
        metric_type = type_names.get(row["Type"])
        if metric_type is None:
            continue
        storage[metric_type] = DockerStorageMetrics(
            size_bytes=parse_size(row["Size"]),
            reclaimable_bytes=parse_size(row["Reclaimable"].split()[0]),
            total_items=float(row["TotalCount"]),
            active_items=float(row["Active"]),
        )
    if "build_cache" not in storage:
        raise RuntimeError("docker system df omitted Build Cache")
    return storage


def parse_started_at(raw: str) -> float:
    if not raw or raw.startswith("0001-01-01T00:00:00"):
        return 0
    normalized = raw.replace("Z", "+00:00")
    fractional = re.match(r"^(.*\.)(\d+)([+-]\d\d:\d\d)$", normalized)
    if fractional is not None:
        normalized = f"{fractional.group(1)}{fractional.group(2)[:6]}{fractional.group(3)}"
    return datetime.fromisoformat(normalized).timestamp()


def inspect_container(name: str) -> ContainerMetrics:
    result = run_docker(["inspect", name])
    if result.returncode != 0:
        raise RuntimeError(f"docker inspect failed for {name}")
    payload = json.loads(result.stdout)[0]
    state = payload.get("State", {})
    host_config = payload.get("HostConfig", {})
    health = state.get("Health", {}).get("Status", "none")
    return ContainerMetrics(
        known=True,
        running=int(bool(state.get("Running"))),
        restart_count=int(payload.get("RestartCount", 0)),
        oom_killed=int(bool(state.get("OOMKilled"))),
        started_at_timestamp_seconds=parse_started_at(str(state.get("StartedAt", ""))),
        health=str(health),
        memory_limit_configured=float(host_config.get("Memory") or 0),
        cpu_limit_cores=float(host_config.get("NanoCpus") or 0) / 1_000_000_000,
        pids_limit=float(host_config.get("PidsLimit") or 0),
    )


def apply_stats(metrics: dict[str, ContainerMetrics], names: list[str]) -> bool:
    if not names:
        return True
    result = run_docker(["stats", "--no-stream", "--format", "{{json .}}", *names])
    if result.returncode != 0:
        return False
    success = True
    for line in result.stdout.splitlines():
        try:
            row = json.loads(line)
            item = metrics[row["Name"]]
            item.cpu_usage_ratio = float(row["CPUPerc"].rstrip("%")) / 100
            usage_raw, limit_raw = (part.strip() for part in row["MemUsage"].split("/", 1))
            item.memory_usage_bytes = parse_size(usage_raw)
            item.memory_limit_bytes = parse_size(limit_raw)
            if item.memory_limit_bytes > 0:
                item.memory_usage_ratio = item.memory_usage_bytes / item.memory_limit_bytes
            item.pids = float(row["PIDs"])
            if item.pids_limit > 0:
                item.pids_usage_ratio = item.pids / item.pids_limit
            if item.cpu_limit_cores > 0:
                item.cpu_limit_usage_ratio = item.cpu_usage_ratio / item.cpu_limit_cores
            item.stats_valid = True
        except (KeyError, TypeError, ValueError, json.JSONDecodeError):
            success = False
    return success and all(metrics[name].stats_valid for name in names)


def escape_label(value: str) -> str:
    return value.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def container_labels(name: str, status: str | None = None) -> str:
    service = CONTAINER_SERVICES.get(name, name)
    labels = f'name="{escape_label(name)}",service="{escape_label(service)}"'
    if status is not None:
        labels += f',status="{escape_label(status)}"'
    return labels


def emit_family(lines: list[str], name: str, help_text: str, values: list[tuple[str, float]]) -> None:
    lines.extend((f"# HELP {name} {help_text}", f"# TYPE {name} gauge"))
    for labels, value in values:
        lines.append(f"{name}{{{labels}}} {value:.12g}")


def render(
    metrics: dict[str, ContainerMetrics],
    storage: dict[str, DockerStorageMetrics],
    check_success: int,
) -> str:
    lines = [
        "# HELP docker_metrics_last_run_timestamp Unix timestamp of the latest Docker metrics collection run.",
        "# TYPE docker_metrics_last_run_timestamp gauge",
        f"docker_metrics_last_run_timestamp {int(time.time())}",
        "# HELP docker_metrics_check_success Whether Docker inventory and runtime stats were collected successfully.",
        "# TYPE docker_metrics_check_success gauge",
        f"docker_metrics_check_success {check_success}",
    ]
    state_definitions = (
        ("docker_container_running", "Whether the expected Docker container is running.", "running"),
        ("docker_container_restart_count", "Docker restart count for the container.", "restart_count"),
        ("docker_container_oom_killed", "Whether the container's current state reports OOMKilled.", "oom_killed"),
        ("docker_container_started_at_timestamp_seconds", "Unix timestamp when the current container instance started.", "started_at_timestamp_seconds"),
        ("docker_container_cpu_limit_cores", "Configured Docker CPU limit in cores.", "cpu_limit_cores"),
        ("docker_container_memory_limit_configured_bytes", "Configured Docker memory limit in bytes.", "memory_limit_configured"),
        ("docker_container_pids_limit", "Configured Docker process limit.", "pids_limit"),
    )
    runtime_definitions = (
        ("docker_container_cpu_usage_ratio", "Current Docker CPU usage where 1.0 is one CPU core.", "cpu_usage_ratio"),
        ("docker_container_cpu_limit_usage_ratio", "Current CPU usage divided by the configured CPU limit.", "cpu_limit_usage_ratio"),
        ("docker_container_memory_usage_bytes", "Current Docker container memory usage in bytes.", "memory_usage_bytes"),
        ("docker_container_memory_limit_bytes", "Runtime memory limit reported by Docker stats in bytes.", "memory_limit_bytes"),
        ("docker_container_memory_usage_ratio", "Current memory usage divided by the runtime memory limit.", "memory_usage_ratio"),
        ("docker_container_pids", "Current process count in the Docker container.", "pids"),
        ("docker_container_pids_usage_ratio", "Current process count divided by the configured process limit.", "pids_usage_ratio"),
    )
    for metric_name, help_text, attribute in state_definitions:
        values = [
            (container_labels(name), float(getattr(item, attribute)))
            for name, item in metrics.items() if item.known
        ]
        emit_family(lines, metric_name, help_text, values)
    for metric_name, help_text, attribute in runtime_definitions:
        values = [
            (container_labels(name), float(getattr(item, attribute)))
            for name, item in metrics.items() if item.stats_valid
        ]
        emit_family(lines, metric_name, help_text, values)
    health_values = [
        (container_labels(name, item.health), 1.0)
        for name, item in metrics.items() if item.known
    ]
    emit_family(lines, "docker_container_health_status", "Current Docker health state as a one-hot series.", health_values)
    storage_definitions = (
        ("docker_storage_size_bytes", "Current Docker storage usage in bytes.", "size_bytes"),
        (
            "docker_storage_reclaimable_bytes",
            "Current Docker storage that can be reclaimed in bytes.",
            "reclaimable_bytes",
        ),
        ("docker_storage_total_items", "Current Docker storage item count.", "total_items"),
        ("docker_storage_active_items", "Current active Docker storage item count.", "active_items"),
    )
    for metric_name, help_text, attribute in storage_definitions:
        emit_family(
            lines,
            metric_name,
            help_text,
            [(f'type="{metric_type}"', getattr(item, attribute)) for metric_type, item in storage.items()],
        )
    return "\n".join(lines) + "\n"


def main() -> int:
    output = Path(os.environ.get("DOCKER_METRIC_OUT", "/var/lib/node_exporter/textfile_collector/docker.prom"))
    configured = os.environ.get("DOCKER_EXPECTED_CONTAINERS", "")
    names = tuple(name for name in configured.split(",") if name) or DEFAULT_CONTAINERS
    metrics = {name: ContainerMetrics() for name in names}
    storage: dict[str, DockerStorageMetrics] = {}
    check_success = 1

    inventory = run_docker(["ps", "-a", "--format", "{{.Names}}"])
    if inventory.returncode != 0:
        check_success = 0
    else:
        present = set(inventory.stdout.splitlines())
        for name in names:
            if name not in present:
                metrics[name].known = True
                continue
            try:
                metrics[name] = inspect_container(name)
            except (RuntimeError, ValueError, KeyError, IndexError, TypeError, json.JSONDecodeError):
                check_success = 0
        running = [name for name, item in metrics.items() if item.running]
        check_success &= int(apply_stats(metrics, running))

    try:
        storage = collect_storage_metrics()
    except (RuntimeError, ValueError, KeyError, TypeError, json.JSONDecodeError):
        check_success = 0

    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=output.parent, delete=False) as handle:
        handle.write(render(metrics, storage, check_success))
        temporary = Path(handle.name)
    temporary.chmod(0o644)
    temporary.replace(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
