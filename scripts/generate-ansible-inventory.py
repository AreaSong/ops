#!/usr/bin/env python3
"""从 inventory/servers.yaml 生成 Ansible inventory 文件。"""

import argparse
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    print("需要 PyYAML: pip install pyyaml", file=sys.stderr)
    sys.exit(1)

REPO_ROOT = Path(__file__).resolve().parent.parent
SERVERS_YAML = REPO_ROOT / "inventory" / "servers.yaml"
OUTPUT = REPO_ROOT / "ansible" / "inventory" / "hosts.yml"
CONNECTION_FIELDS = ("ansible_user", "ansible_ssh_private_key_file", "ansible_port")


def build_inventory(data: dict) -> dict:
    hosts = {}
    for server in data.get("servers", []):
        hostname = server["hostname"]
        connection_host = server.get("private_ip") or server.get("public_ip") or hostname
        host_vars = {
            "ansible_host": connection_host,
            "os": server.get("os", "unknown"),
            "cloud": server.get("cloud", ""),
            "region": server.get("region", ""),
        }
        if server.get("public_ip"):
            host_vars["ansible_host_public"] = server["public_ip"]
        for field in CONNECTION_FIELDS:
            if server.get(field) not in (None, ""):
                host_vars[field] = server[field]
        hosts[hostname] = host_vars

    children = {
        group_name: {"hosts": {hostname: {} for hostname in hostnames}}
        for group_name, hostnames in data.get("groups", {}).items()
    }
    return {"all": {"hosts": hosts, "children": children}}


def render_inventory(data: dict) -> str:
    rendered = yaml.safe_dump(build_inventory(data), allow_unicode=True, sort_keys=False)
    return "# 自动生成，请勿手动编辑。源文件: inventory/servers.yaml\n" + rendered


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=SERVERS_YAML)
    parser.add_argument("--output", type=Path, default=OUTPUT)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    data = yaml.safe_load(args.source.read_text(encoding="utf-8"))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(render_inventory(data), encoding="utf-8")
    print(f"Generated: {args.output}")


if __name__ == "__main__":
    main()
