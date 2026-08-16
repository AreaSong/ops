#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CHECK_SCRIPT="$SCRIPT_DIR/../external-heartbeat-check.sh"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

mkdir -p "$WORK_DIR/bin"
cat >"$WORK_DIR/bin/gh" <<'FAKE_GH'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" != api ]; then
  exit 2
fi
printf '%s' "$FAKE_ISSUES"
FAKE_GH
chmod 0755 "$WORK_DIR/bin/gh"

run_case() {
  local issues="$1"
  local output="$2"
  FAKE_ISSUES="$issues" PATH="$WORK_DIR/bin:$PATH" \
    GITHUB_REPOSITORY=AreaSong/ops HEARTBEAT_MAX_AGE_SECONDS=600 \
    bash "$CHECK_SCRIPT" >"$output" 2>&1
}

tick="$(printf '\140')"
now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
healthy_json="$(printf '[{"number":71,"body":"<!-- areasong-external-heartbeat:v1 -->\\nLast heartbeat (UTC): %s%s%s"}]' "$tick" "$now" "$tick")"
run_case "$healthy_json" "$WORK_DIR/healthy.txt"
grep -Fq 'HEARTBEAT_OK' "$WORK_DIR/healthy.txt"

stale="$(date -u -v-20M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '20 minutes ago' +%Y-%m-%dT%H:%M:%SZ)"
stale_json="$(printf '[{"number":71,"body":"<!-- areasong-external-heartbeat:v1 -->\\nLast heartbeat (UTC): %s%s%s"}]' "$tick" "$stale" "$tick")"
if run_case "$stale_json" "$WORK_DIR/stale.txt"; then
  echo "stale heartbeat unexpectedly succeeded" >&2
  exit 1
fi
grep -Fq 'HEARTBEAT_FAIL reason=heartbeat_stale' "$WORK_DIR/stale.txt"

if run_case '[]' "$WORK_DIR/missing.txt"; then
  echo "missing heartbeat unexpectedly succeeded" >&2
  exit 1
fi
grep -Fq 'HEARTBEAT_FAIL reason=heartbeat_issue_missing' "$WORK_DIR/missing.txt"

duplicate_json="$(printf '[{"number":71,"body":"<!-- areasong-external-heartbeat:v1 -->\\nLast heartbeat (UTC): %s%s%s"},{"number":72,"body":"<!-- areasong-external-heartbeat:v1 -->\\nLast heartbeat (UTC): %s%s%s"}]' "$tick" "$now" "$tick" "$tick" "$now" "$tick")"
if run_case "$duplicate_json" "$WORK_DIR/duplicate.txt"; then
  echo "duplicate heartbeat unexpectedly succeeded" >&2
  exit 1
fi
grep -Fq 'HEARTBEAT_FAIL reason=heartbeat_issue_duplicate count=2' "$WORK_DIR/duplicate.txt"

echo "external heartbeat checks: PASS"
