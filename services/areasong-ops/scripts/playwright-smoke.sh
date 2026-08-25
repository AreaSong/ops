#!/usr/bin/env bash
set -Eeuo pipefail

if ! command -v npx >/dev/null 2>&1; then
  echo "npx is required for the Playwright smoke" >&2
  exit 1
fi

codex_home="${CODEX_HOME:-$HOME/.codex}"
pwcli="${codex_home}/skills/playwright/scripts/playwright_cli.sh"
[[ -x "$pwcli" ]] || {
  echo "Playwright CLI wrapper not found: $pwcli" >&2
  exit 1
}

url="${OPS_PLAYWRIGHT_URL:-http://127.0.0.1:4173}"
output_dir="${OPS_PLAYWRIGHT_OUTPUT:-output/playwright}"
mkdir -p "$output_dir"
snapshot_file="$(mktemp)"
trap 'rm -f "$snapshot_file"' EXIT

"$pwcli" open "$url" >/dev/null
"$pwcli" snapshot >"$snapshot_file"
if ! grep -q "操作总览" "$snapshot_file"; then
  echo "Playwright smoke did not reach the authenticated Ops shell" >&2
  cat "$snapshot_file" >&2
  exit 1
fi
"$pwcli" screenshot --filename "$output_dir/ops-smoke.png" >/dev/null
"$pwcli" close >/dev/null
echo "Playwright smoke passed: $url"
