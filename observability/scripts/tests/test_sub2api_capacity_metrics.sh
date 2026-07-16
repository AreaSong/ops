#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METRIC_SCRIPT="$SCRIPT_DIR/../write-sub2api-capacity-metrics.sh"
WORK_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

mkdir -p "$WORK_DIR/bin"
cat >"$WORK_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -u

if [ "${FAKE_DOCKER_MODE:-success}" = "failure" ]; then
  exit 1
fi

cat <<'LOGS'
request failed: no available account
healthy request
NO AVAILABLE ACCOUNT for provider
LOGS
EOF
chmod 0755 "$WORK_DIR/bin/docker"

success_out="$WORK_DIR/success.prom"
SUB2API_CAPACITY_METRIC_OUT="$success_out" PATH="$WORK_DIR/bin:$PATH" "$METRIC_SCRIPT"
grep -Fq 'sub2api_log_check_success 1' "$success_out"
grep -Fq 'sub2api_no_available_account_events_last_5m 2' "$success_out"

failure_out="$WORK_DIR/failure.prom"
FAKE_DOCKER_MODE=failure SUB2API_CAPACITY_METRIC_OUT="$failure_out" PATH="$WORK_DIR/bin:$PATH" "$METRIC_SCRIPT"
grep -Fq 'sub2api_log_check_success 0' "$failure_out"
grep -Fq 'sub2api_no_available_account_events_last_5m 0' "$failure_out"

echo "sub2api capacity metrics: PASS"
