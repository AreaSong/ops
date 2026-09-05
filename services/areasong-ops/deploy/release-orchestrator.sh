#!/usr/bin/env bash
set -Eeuo pipefail

# 唯一发布入口：所有参数和状态机由同目录 Python 编排器实现。
script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
exec python3 "$script_dir/release_orchestrator.py" "$@"
