#!/usr/bin/env python3
"""从 inventory/servers.yaml 生成 Ansible inventory 文件。"""

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


def main():
    with open(SERVERS_YAML) as f:
        data = yaml.safe_load(f)

    servers = data.get("servers", [])
    groups = data.get("groups", {})

    lines = ["# 自动生成，请勿手动编辑。源文件: inventory/servers.yaml", "all:"]
    lines.append("  hosts:")

    for srv in servers:
        hostname = srv["hostname"]
        lines.append(f"    {hostname}:")
        lines.append(f"      ansible_host: {srv.get('private_ip', '')}")
        if srv.get("public_ip"):
            lines.append(f"      ansible_host_public: {srv['public_ip']}")
        lines.append(f"      os: {srv.get('os', 'unknown')}")
        lines.append(f"      cloud: {srv.get('cloud', '')}")
        lines.append(f"      region: {srv.get('region', '')}")

    lines.append("  children:")

    for group_name, hostnames in groups.items():
        lines.append(f"    {group_name}:")
        lines.append("      hosts:")
        for hn in hostnames:
            lines.append(f"        {hn}:")

    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    OUTPUT.write_text("\n".join(lines) + "\n")
    print(f"Generated: {OUTPUT}")


if __name__ == "__main__":
    main()
