#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import ipaddress
import os
import tempfile
import time
import urllib.request
from pathlib import Path

URLS = {
    "ipv4": "https://www.cloudflare.com/ips-v4",
    "ipv6": "https://www.cloudflare.com/ips-v6",
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Compare managed Cloudflare CIDRs with the official lists.")
    parser.add_argument("--validate-only", action="store_true")
    return parser.parse_args()


def parse_networks(raw: str, family: int) -> tuple[str, ...]:
    networks: list[str] = []
    for line in raw.splitlines():
        value = line.strip()
        if not value:
            continue
        network = ipaddress.ip_network(value, strict=True)
        if network.version != family:
            raise ValueError(f"unexpected IPv{network.version} network in IPv{family} list: {value}")
        networks.append(str(network))
    if not networks:
        raise ValueError(f"IPv{family} network list is empty")
    return tuple(sorted(set(networks)))


def fetch(url: str) -> str:
    request = urllib.request.Request(url, headers={"User-Agent": "AreaSong-LosAngeles-Cloudflare-Audit/1.0"})
    with urllib.request.urlopen(request, timeout=15) as response:
        if response.status != 200:
            raise OSError(f"unexpected HTTP status {response.status}")
        return response.read().decode("utf-8")


def digest(values: tuple[str, ...]) -> str:
    return hashlib.sha256(("\n".join(values) + "\n").encode("utf-8")).hexdigest()


def atomic_write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as handle:
        handle.write(content)
        temporary = Path(handle.name)
    temporary.chmod(0o644)
    temporary.replace(path)


def render_metrics(results: dict[str, dict[str, object]], success: bool, generated_at: int) -> str:
    lines = [
        "# HELP cloudflare_ip_ranges_last_run_timestamp Unix timestamp of the latest Cloudflare CIDR comparison.",
        "# TYPE cloudflare_ip_ranges_last_run_timestamp gauge",
        f"cloudflare_ip_ranges_last_run_timestamp {generated_at}",
        "# HELP cloudflare_ip_ranges_check_success Whether the official Cloudflare CIDRs were fetched and parsed.",
        "# TYPE cloudflare_ip_ranges_check_success gauge",
        f"cloudflare_ip_ranges_check_success {int(success)}",
        "# HELP cloudflare_ip_ranges_match Whether configured and official Cloudflare CIDRs match exactly.",
        "# TYPE cloudflare_ip_ranges_match gauge",
        "# HELP cloudflare_ip_ranges_count Number of configured or official Cloudflare CIDRs.",
        "# TYPE cloudflare_ip_ranges_count gauge",
    ]
    for family in ("ipv4", "ipv6"):
        item = results.get(family, {})
        configured = item.get("configured", ())
        official = item.get("official", ())
        is_match = bool(item.get("match"))
        lines.append(
            f'cloudflare_ip_ranges_match{{family="{family}",configured_sha256="{item.get("configured_sha256", "")}",official_sha256="{item.get("official_sha256", "")}"}} {int(is_match)}'
        )
        lines.append(f'cloudflare_ip_ranges_count{{family="{family}",source="configured"}} {len(configured)}')
        lines.append(f'cloudflare_ip_ranges_count{{family="{family}",source="official"}} {len(official)}')
    return "\n".join(lines) + "\n"


def compare(config_dir: Path) -> tuple[dict[str, dict[str, object]], bool]:
    results: dict[str, dict[str, object]] = {}
    success = True
    for family, url in URLS.items():
        version = 4 if family == "ipv4" else 6
        try:
            configured = parse_networks((config_dir / f"ips-v{version}.txt").read_text(encoding="utf-8"), version)
            official = parse_networks(fetch(url), version)
            results[family] = {
                "configured": configured,
                "official": official,
                "configured_sha256": digest(configured),
                "official_sha256": digest(official),
                "match": configured == official,
            }
        except (OSError, UnicodeError, ValueError) as exc:
            results[family] = {"error": type(exc).__name__, "match": False}
            success = False
    return results, success


def main() -> int:
    args = parse_args()
    config_dir = Path(os.environ.get("CLOUDFLARE_IP_CONFIG_DIR", "/opt/ops/security/cloudflare"))
    if args.validate_only:
        parse_networks((config_dir / "ips-v4.txt").read_text(encoding="utf-8"), 4)
        parse_networks((config_dir / "ips-v6.txt").read_text(encoding="utf-8"), 6)
        return 0

    output = Path(
        os.environ.get("CLOUDFLARE_IP_METRIC_OUT", "/var/lib/node_exporter/textfile_collector/cloudflare-ip-ranges.prom")
    )
    results, success = compare(config_dir)
    atomic_write(output, render_metrics(results, success, int(time.time())))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
