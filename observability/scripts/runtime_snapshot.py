#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any

import yaml

DEFAULT_INVENTORY = "/opt/ops/inventory/losangeles-assets.yaml"
DEFAULT_SNAPSHOT = "/var/lib/ops/runtime/losangeles-runtime.json"
DEFAULT_METRICS = "/var/lib/node_exporter/textfile_collector/runtime-snapshot.prom"
PROCESS_RE = re.compile(r'\("([^"\\]+)"')


def run(command: list[str], timeout: int = 30) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except FileNotFoundError as exc:
        return subprocess.CompletedProcess(command, 127, "", str(exc))
    except subprocess.TimeoutExpired as exc:
        return subprocess.CompletedProcess(command, 124, exc.stdout or "", exc.stderr or str(exc))


def atomic_write(path: Path, content: str, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as handle:
        handle.write(content)
        temporary = Path(handle.name)
    temporary.chmod(mode)
    temporary.replace(path)


def escape_label(value: Any) -> str:
    return str(value).replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def labels(**values: Any) -> str:
    return ",".join(f'{key}="{escape_label(value)}"' for key, value in values.items())


def load_inventory(path: Path) -> dict[str, Any]:
    payload = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict) or payload.get("schema_version") != 1:
        raise ValueError("asset inventory must be a schema_version=1 mapping")
    if not isinstance(payload.get("services"), list) or not isinstance(payload.get("routes"), list):
        raise ValueError("asset inventory must contain services and routes lists")
    return payload


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Write a sanitized LosAngeles runtime snapshot.")
    parser.add_argument("--validate-only", action="store_true", help="Validate the asset inventory and exit.")
    return parser.parse_args()


def collect_host() -> dict[str, Any]:
    memory: dict[str, int] = {}
    for line in Path("/proc/meminfo").read_text(encoding="utf-8").splitlines():
        key, raw = line.split(":", 1)
        value = raw.strip().split()[0]
        memory[key] = int(value) * 1024
    disk = shutil.disk_usage("/")
    uptime_seconds = float(Path("/proc/uptime").read_text(encoding="utf-8").split()[0])
    load_1m, load_5m, load_15m = os.getloadavg()
    return {
        "uptime_seconds": uptime_seconds,
        "load": {"1m": load_1m, "5m": load_5m, "15m": load_15m},
        "memory": {
            "total_bytes": memory.get("MemTotal", 0),
            "available_bytes": memory.get("MemAvailable", 0),
        },
        "root_filesystem": {
            "total_bytes": disk.total,
            "used_bytes": disk.used,
            "free_bytes": disk.free,
        },
    }


def split_address(raw: str) -> tuple[str, str]:
    if raw.startswith("[") and "]:" in raw:
        address, port = raw.rsplit("]:", 1)
        return address[1:], port
    if ":" not in raw:
        return raw, ""
    return tuple(raw.rsplit(":", 1))  # type: ignore[return-value]


def parse_listener_line(line: str) -> dict[str, str] | None:
    parts = line.split(maxsplit=6)
    if len(parts) < 6:
        return None
    protocol, state, _, _, local, _ = parts[:6]
    address, port = split_address(local)
    process_field = parts[6] if len(parts) > 6 else ""
    process_names = sorted(set(PROCESS_RE.findall(process_field)))
    return {
        "protocol": protocol,
        "state": state,
        "address": address,
        "port": port,
        "process": ",".join(process_names) or "unknown",
    }


def collect_listeners() -> tuple[list[dict[str, str]], bool]:
    result = run(["ss", "-H", "-lntup"])
    if result.returncode != 0:
        return [], False
    listeners = [item for line in result.stdout.splitlines() if (item := parse_listener_line(line))]
    listeners.sort(key=lambda item: (item["protocol"], item["address"], item["port"]))
    return listeners, True


def collect_systemd(inventory: dict[str, Any]) -> tuple[list[dict[str, str]], bool]:
    unit_to_service: dict[str, str] = {}
    for service in inventory.get("services", []):
        for unit in service.get("runtime", {}).get("units", []) or []:
            unit_to_service[str(unit)] = str(service["service_id"])

    output: list[dict[str, str]] = []
    success = True
    for unit, service_id in sorted(unit_to_service.items()):
        result = run(
            [
                "systemctl",
                "show",
                unit,
                "--property=LoadState,ActiveState,SubState,UnitFileState",
                "--no-pager",
            ]
        )
        values: dict[str, str] = {}
        if result.returncode == 0:
            for line in result.stdout.splitlines():
                if "=" in line:
                    key, value = line.split("=", 1)
                    values[key] = value
        else:
            success = False
        output.append(
            {
                "service_id": service_id,
                "unit": unit,
                "load_state": values.get("LoadState", "unknown"),
                "active_state": values.get("ActiveState", "unknown"),
                "sub_state": values.get("SubState", "unknown"),
                "unit_file_state": values.get("UnitFileState", "unknown"),
            }
        )
    return output, success


def expected_containers(inventory: dict[str, Any]) -> dict[str, str]:
    mapping: dict[str, str] = {}
    for service in inventory.get("services", []):
        for name in service.get("runtime", {}).get("containers", []) or []:
            mapping[str(name)] = str(service["service_id"])
    return mapping


def port_bindings(payload: dict[str, Any]) -> list[dict[str, str]]:
    bindings: list[dict[str, str]] = []
    for container_port, entries in (payload.get("HostConfig", {}).get("PortBindings") or {}).items():
        port, protocol = container_port.split("/", 1)
        for entry in entries or []:
            bindings.append(
                {
                    "container_port": port,
                    "protocol": protocol,
                    "host_ip": str(entry.get("HostIp") or ""),
                    "host_port": str(entry.get("HostPort") or ""),
                }
            )
    return sorted(bindings, key=lambda item: (item["host_ip"], item["host_port"], item["container_port"]))


def collect_containers(inventory: dict[str, Any]) -> tuple[list[dict[str, Any]], bool]:
    mapping = expected_containers(inventory)
    present_result = run(["docker", "ps", "-a", "--format", "{{.Names}}"])
    if present_result.returncode != 0:
        return [], False
    present = set(present_result.stdout.splitlines())
    output: list[dict[str, Any]] = []
    success = True

    for name, service_id in sorted(mapping.items()):
        if name not in present:
            output.append({"name": name, "service_id": service_id, "present": False})
            success = False
            continue
        result = run(["docker", "inspect", name])
        if result.returncode != 0:
            output.append({"name": name, "service_id": service_id, "present": False})
            success = False
            continue
        try:
            payload = json.loads(result.stdout)[0]
            state = payload.get("State", {})
            host_config = payload.get("HostConfig", {})
            config = payload.get("Config", {})
            compose_labels = config.get("Labels") or {}
            security_opt = host_config.get("SecurityOpt") or []
            cap_drop = host_config.get("CapDrop") or []
            output.append(
                {
                    "name": name,
                    "service_id": service_id,
                    "present": True,
                    "running": bool(state.get("Running")),
                    "health": state.get("Health", {}).get("Status", "none"),
                    "restart_count": int(payload.get("RestartCount", 0)),
                    "oom_killed": bool(state.get("OOMKilled")),
                    "image_ref": str(config.get("Image") or ""),
                    "image_id": str(payload.get("Image") or ""),
                    "user": str(config.get("User") or "root(default)"),
                    "restart_policy": str(host_config.get("RestartPolicy", {}).get("Name") or ""),
                    "security": {
                        "privileged": bool(host_config.get("Privileged")),
                        "read_only_rootfs": bool(host_config.get("ReadonlyRootfs")),
                        "no_new_privileges": "no-new-privileges:true" in security_opt,
                        "cap_drop_all": "ALL" in cap_drop,
                    },
                    "compose": {
                        "project": str(compose_labels.get("com.docker.compose.project") or ""),
                        "config_files": str(compose_labels.get("com.docker.compose.project.config_files") or ""),
                        "working_dir": str(compose_labels.get("com.docker.compose.project.working_dir") or ""),
                    },
                    "port_bindings": port_bindings(payload),
                }
            )
        except (IndexError, KeyError, TypeError, ValueError, json.JSONDecodeError):
            output.append({"name": name, "service_id": service_id, "present": False})
            success = False
    return output, success


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def evaluate_config_pairs(inventory: dict[str, Any]) -> list[dict[str, Any]]:
    output: list[dict[str, Any]] = []
    for pair in inventory.get("config_pairs", []) or []:
        runtime = Path(str(pair["runtime"]))
        controlled = Path(str(pair["controlled"]))
        runtime_hash = sha256_file(runtime) if runtime.is_file() else ""
        controlled_hash = sha256_file(controlled) if controlled.is_file() else ""
        output.append(
            {
                "service_id": str(pair["service_id"]),
                "kind": "file",
                "runtime_path": str(runtime),
                "controlled_path": str(controlled),
                "runtime_sha256": runtime_hash,
                "controlled_sha256": controlled_hash,
                "severity": str(pair.get("severity") or "warning"),
                "drift": not runtime_hash or not controlled_hash or runtime_hash != controlled_hash,
            }
        )
    return output


def evaluate_routes(inventory: dict[str, Any]) -> list[dict[str, Any]]:
    output: list[dict[str, Any]] = []
    for route in inventory.get("routes", []):
        path = Path(str(route["nginx_file"]))
        content = path.read_text(encoding="utf-8") if path.is_file() else ""
        expected = [f"server_name {route['hostname']}", *(route.get("backend_endpoints") or [])]
        desired_origin_policy = route.get("desired_origin_policy", route.get("origin_policy", ""))
        cloudflare_markers = ["cloudflare-real-ip.conf", "cloudflare-origin-only.conf"]
        observed_origin_policy = (
            "cloudflare-only" if all(marker in content for marker in cloudflare_markers) else "direct"
        )
        if desired_origin_policy == "cloudflare-only":
            expected.extend(cloudflare_markers)
        missing = [item for item in expected if item not in content]
        output.append(
            {
                "service_id": str(route["service_id"]),
                "kind": "nginx-route",
                "hostname": str(route["hostname"]),
                "runtime_path": str(path),
                "controlled_path": "inventory/losangeles-assets.yaml",
                "severity": "critical" if desired_origin_policy == "cloudflare-only" else "warning",
                "observed_origin_policy": observed_origin_policy,
                "drift": bool(missing),
                "missing_expectations": missing,
            }
        )
    return output


def render_metrics(
    inventory: dict[str, Any],
    systemd: list[dict[str, str]],
    listeners: list[dict[str, str]],
    containers: list[dict[str, Any]],
    drift: list[dict[str, Any]],
    check_success: bool,
    generated_at: int,
) -> str:
    route_runtime = {
        item.get("hostname", ""): item
        for item in drift
        if item.get("kind") == "nginx-route" and item.get("hostname")
    }
    lines = [
        "# HELP ops_runtime_snapshot_last_success_timestamp Unix timestamp of the latest runtime snapshot run.",
        "# TYPE ops_runtime_snapshot_last_success_timestamp gauge",
        f"ops_runtime_snapshot_last_success_timestamp {generated_at}",
        "# HELP ops_runtime_snapshot_check_success Whether all runtime snapshot collectors succeeded.",
        "# TYPE ops_runtime_snapshot_check_success gauge",
        f"ops_runtime_snapshot_check_success {int(check_success)}",
        "# HELP ops_asset_service_info Static service ownership and runtime mapping from the asset inventory.",
        "# TYPE ops_asset_service_info gauge",
    ]
    for service in inventory.get("services", []):
        runtime = service.get("runtime", {})
        lines.append(
            "ops_asset_service_info{"
            + labels(
                service_id=service["service_id"],
                name=service.get("name", ""),
                owner=service.get("owner", ""),
                lifecycle=service.get("lifecycle", ""),
                runtime_type=runtime.get("type", ""),
                compose_path=runtime.get("runtime_compose", ""),
                units=",".join(runtime.get("units", []) or []),
            )
            + "} 1"
        )
    lines.extend(("# HELP ops_asset_port_info Static host and container port mapping.", "# TYPE ops_asset_port_info gauge"))
    for service in inventory.get("services", []):
        for port in service.get("ports", []) or []:
            lines.append(
                "ops_asset_port_info{"
                + labels(
                    service_id=service["service_id"],
                    bind=port.get("bind", ""),
                    host_port=port.get("host_port", ""),
                    container_port=port.get("container_port", ""),
                    protocol=port.get("protocol", ""),
                    exposure=port.get("exposure", ""),
                )
                + "} 1"
            )
    lines.extend(("# HELP ops_asset_domain_info Static domain to Nginx and backend mapping.", "# TYPE ops_asset_domain_info gauge"))
    for route in inventory.get("routes", []):
        runtime_route = route_runtime.get(route["hostname"], {})
        for backend in route.get("backend_endpoints", []) or [""]:
            lines.append(
                "ops_asset_domain_info{"
                + labels(
                    service_id=route["service_id"],
                    hostname=route["hostname"],
                    cloudflare_mode=route.get("cloudflare_mode", ""),
                    observed_origin_policy=runtime_route.get(
                        "observed_origin_policy", route.get("observed_origin_policy", "")
                    ),
                    desired_origin_policy=route.get("desired_origin_policy", route.get("origin_policy", "")),
                    nginx_file=route.get("nginx_file", ""),
                    backend_endpoint=backend,
                    tls_mode=route.get("tls_mode", ""),
                )
                + "} 1"
            )
    lines.extend(("# HELP ops_systemd_service_active Whether an inventoried systemd service is active.", "# TYPE ops_systemd_service_active gauge"))
    for item in systemd:
        lines.append(
            "ops_systemd_service_active{"
            + labels(
                service_id=item["service_id"],
                unit=item["unit"],
                active_state=item["active_state"],
                sub_state=item["sub_state"],
                unit_file_state=item["unit_file_state"],
            )
            + f"}} {int(item['active_state'] == 'active')}"
        )
    lines.extend(("# HELP ops_listener_info Current sanitized host listener inventory.", "# TYPE ops_listener_info gauge"))
    for item in listeners:
        lines.append("ops_listener_info{" + labels(**item) + "} 1")
    lines.extend(("# HELP ops_container_runtime_info Sanitized Docker runtime and image mapping.", "# TYPE ops_container_runtime_info gauge"))
    for item in containers:
        lines.append(
            "ops_container_runtime_info{"
            + labels(
                service_id=item.get("service_id", ""),
                name=item.get("name", ""),
                image_ref=item.get("image_ref", ""),
                image_id=item.get("image_id", ""),
                user=item.get("user", ""),
                compose_project=item.get("compose", {}).get("project", ""),
                compose_config=item.get("compose", {}).get("config_files", ""),
                health=item.get("health", "missing"),
            )
            + f"}} {int(bool(item.get('present')))}"
        )
    lines.extend(("# HELP ops_container_security_state Sanitized Docker hardening state.", "# TYPE ops_container_security_state gauge"))
    for item in containers:
        security = item.get("security", {})
        lines.append(
            "ops_container_security_state{"
            + labels(
                service_id=item.get("service_id", ""),
                name=item.get("name", ""),
                privileged=str(bool(security.get("privileged"))).lower(),
                read_only_rootfs=str(bool(security.get("read_only_rootfs"))).lower(),
                no_new_privileges=str(bool(security.get("no_new_privileges"))).lower(),
                cap_drop_all=str(bool(security.get("cap_drop_all"))).lower(),
            )
            + "} 1"
        )
    lines.extend(("# HELP ops_config_drift Whether a controlled configuration or route differs from inventory.", "# TYPE ops_config_drift gauge"))
    for item in drift:
        lines.append(
            "ops_config_drift{"
            + labels(
                service_id=item.get("service_id", ""),
                kind=item.get("kind", ""),
                hostname=item.get("hostname", ""),
                runtime_path=item.get("runtime_path", ""),
                controlled_path=item.get("controlled_path", ""),
                severity=item.get("severity", "warning"),
            )
            + f"}} {int(bool(item.get('drift')))}"
        )
    return "\n".join(lines) + "\n"


def main() -> int:
    args = parse_args()
    inventory_path = Path(os.environ.get("ASSET_INVENTORY_PATH", DEFAULT_INVENTORY))
    snapshot_path = Path(os.environ.get("RUNTIME_SNAPSHOT_OUT", DEFAULT_SNAPSHOT))
    metrics_path = Path(os.environ.get("RUNTIME_METRIC_OUT", DEFAULT_METRICS))
    generated_at = int(time.time())
    inventory: dict[str, Any] = {"schema_version": 1, "host": {}, "services": [], "routes": []}
    errors: list[str] = []

    if args.validate_only:
        load_inventory(inventory_path)
        return 0

    try:
        inventory = load_inventory(inventory_path)
    except (OSError, ValueError, yaml.YAMLError) as exc:
        errors.append(f"inventory:{type(exc).__name__}")

    try:
        host = collect_host()
    except (OSError, ValueError) as exc:
        host = {}
        errors.append(f"host:{type(exc).__name__}")
    systemd, systemd_ok = collect_systemd(inventory)
    listeners, listeners_ok = collect_listeners()
    containers, containers_ok = collect_containers(inventory)

    try:
        drift = [*evaluate_config_pairs(inventory), *evaluate_routes(inventory)]
    except OSError as exc:
        drift = []
        errors.append(f"drift:{type(exc).__name__}")

    check_success = not errors and systemd_ok and listeners_ok and containers_ok
    snapshot = {
        "schema_version": 1,
        "generated_at": generated_at,
        "host": inventory.get("host", {}),
        "services": inventory.get("services", []),
        "routes": inventory.get("routes", []),
        "runtime": {
            "host": host,
            "systemd": systemd,
            "listeners": listeners,
            "containers": containers,
            "configuration_drift": drift,
        },
        "check_success": check_success,
        "errors": errors,
    }
    atomic_write(snapshot_path, json.dumps(snapshot, ensure_ascii=True, indent=2, sort_keys=True) + "\n", 0o644)
    atomic_write(
        metrics_path,
        render_metrics(inventory, systemd, listeners, containers, drift, check_success, generated_at),
        0o644,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
