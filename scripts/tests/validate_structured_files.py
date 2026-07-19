#!/usr/bin/env python3
from __future__ import annotations

import json
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[2]


def main() -> int:
    for suffix in ("*.yml", "*.yaml"):
        for path in ROOT.rglob(suffix):
            if ".git" not in path.parts:
                yaml.safe_load(path.read_text(encoding="utf-8"))
    for path in (ROOT / "observability" / "grafana" / "dashboards").glob("*.json"):
        json.loads(path.read_text(encoding="utf-8"))
    print("YAML and Dashboard JSON validation passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
