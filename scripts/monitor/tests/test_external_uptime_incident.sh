#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INCIDENT_SCRIPT="$SCRIPT_DIR/../external-uptime-incident.sh"
WORK_DIR="$(mktemp -d)"

trap 'rm -rf "$WORK_DIR"' EXIT

mkdir -p "$WORK_DIR/bin"
cat >"$WORK_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%q ' "$@" >>"$GH_LOG"
printf '\n' >>"$GH_LOG"

arguments=("$@")
for index in "${!arguments[@]}"; do
  if [ "${arguments[$index]}" = "--body-file" ]; then
    body_index=$((index + 1))
    printf 'BODY ' >>"$GH_LOG"
    tr '\n' ' ' <"${arguments[$body_index]}" >>"$GH_LOG"
    printf '\n' >>"$GH_LOG"
  fi
done

if [ "${1:-}" = issue ] && [ "${2:-}" = list ]; then
  printf '%s' "${FAKE_ISSUES:-[]}"
fi
EOF
chmod 0755 "$WORK_DIR/bin/gh"
printf 'probe failed\n' >"$WORK_DIR/result.txt"

run_case() {
  local name="$1"
  local outcome="$2"
  local issues="$3"
  local kind="${4:-production}"
  local log="$WORK_DIR/$name.log"
  GH_LOG="$log" FAKE_ISSUES="$issues" PATH="$WORK_DIR/bin:$PATH" \
    "$INCIDENT_SCRIPT" "$outcome" "$WORK_DIR/result.txt" "$kind"
  printf '%s\n' "$log"
}

first_log="$(run_case first failure '[]')"
grep -Fq 'issue create' "$first_log"
grep -Fq 'areasong-external-uptime-managed:v1' "$first_log"
grep -Fq -- '--json number\,body' "$first_log"
if grep -Fq 'issue edit' "$first_log"; then exit 1; fi

duplicate_log="$(run_case duplicate failure '[{"number":41,"body":"<!-- areasong-external-uptime-managed:v1 -->"},{"number":42,"body":"<!-- areasong-external-uptime-managed:v1 -->"},{"number":43,"body":"manual issue"}]')"
grep -Fq 'issue edit 41' "$duplicate_log"
[ "$(grep -c 'issue close' "$duplicate_log")" -eq 1 ]
grep -Fq 'issue close 42' "$duplicate_log"
if grep -Fq 'issue close 43' "$duplicate_log"; then exit 1; fi

recovery_log="$(run_case recovery success '[{"number":51,"body":"<!-- areasong-external-uptime-managed:v1 -->"},{"number":52,"body":"<!-- areasong-external-uptime-managed:v1 -->"},{"number":53,"body":"manual issue"}]')"
[ "$(grep -c 'issue close' "$recovery_log")" -eq 2 ]
grep -Fq 'issue close 51' "$recovery_log"
grep -Fq 'issue close 52' "$recovery_log"
if grep -Fq 'issue close 53' "$recovery_log"; then exit 1; fi

simulation_log="$(run_case simulation failure '[]' simulation)"
grep -Fq 'external-uptime-test' "$simulation_log"
grep -Fq 'areasong-external-uptime-simulation:v1' "$simulation_log"

heartbeat_log="$(run_case heartbeat failure '[]' heartbeat)"
grep -Fq 'external-heartbeat' "$heartbeat_log"
grep -Fq 'areasong-external-heartbeat-incident:v1' "$heartbeat_log"

if GH_LOG="$WORK_DIR/invalid.log" PATH="$WORK_DIR/bin:$PATH" \
  "$INCIDENT_SCRIPT" cancelled "$WORK_DIR/result.txt" >/dev/null 2>&1; then
  echo "invalid outcome unexpectedly succeeded" >&2
  exit 1
fi

if GH_LOG="$WORK_DIR/missing.log" PATH="$WORK_DIR/bin:$PATH" \
  "$INCIDENT_SCRIPT" failure "$WORK_DIR/missing-result.txt" >/dev/null 2>&1; then
  echo "missing failure result unexpectedly succeeded" >&2
  exit 1
fi

echo "external uptime incident handling: PASS"
