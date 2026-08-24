#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 -m unittest discover -s "$SCRIPT_DIR" -p 'test_sqlite_snapshot_verify.py' -v
python3 -m py_compile "$SCRIPT_DIR/../sqlite_snapshot_verify.py"
echo "AreaSong Ops SQLite snapshot/restore checks: PASS"
