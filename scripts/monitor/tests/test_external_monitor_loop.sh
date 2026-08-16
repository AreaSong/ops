#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LOOP_SCRIPT="$SCRIPT_DIR/../external-monitor-loop.sh"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

cat >"$WORK_DIR/uptime.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
count_file="$LOOP_STATE"
count=0
if [ -f "$count_file" ]; then count="$(cat "$count_file")"; fi
count=$((count + 1))
printf '%s' "$count" >"$count_file"
failed=0
for target in resume account-vault sub2api areaforge grafana log-gateway; do
  if [ "$target" = resume ] && { [ "$count" -eq 2 ] || [ "$count" -eq 3 ]; }; then
    printf 'FAIL\t%s\thttp=503\tcurl_exit=22\terror=test\n' "$target"
    failed=1
  else
    printf 'OK\t%s\t200\t0.010000\n' "$target"
  fi
done
exit "$failed"
EOF
cat >"$WORK_DIR/heartbeat.sh" <<'EOF'
#!/usr/bin/env bash
printf 'heartbeat-ok\n'
EOF
chmod 0755 "$WORK_DIR/uptime.sh" "$WORK_DIR/heartbeat.sh"

output_dir="$WORK_DIR/rounds"
LOOP_STATE="$WORK_DIR/state" \
UPTIME_ROUNDS=4 \
UPTIME_INTERVAL_SECONDS=0 \
FAILURE_THRESHOLD=2 \
UPTIME_CHECK_SCRIPT="$WORK_DIR/uptime.sh" \
HEARTBEAT_CHECK_SCRIPT="$WORK_DIR/heartbeat.sh" \
bash "$LOOP_SCRIPT" "$output_dir"

[ "$(wc -l <"$output_dir/status.tsv" | tr -d ' ')" -eq 8 ]
grep -Fq $'uptime\tfailure' "$output_dir/status.tsv"
grep -Fq 'final_uptime=success' "$output_dir/final.outcome"
grep -Fq 'final_heartbeat=success' "$output_dir/final.outcome"

cat >"$WORK_DIR/always-fail.sh" <<'EOF'
#!/usr/bin/env bash
printf 'FAIL\tresume\thttp=503\tcurl_exit=22\terror=test\n'
for target in account-vault sub2api areaforge grafana log-gateway; do
  printf 'OK\t%s\t200\t0.010000\n' "$target"
done
exit 1
EOF
chmod 0755 "$WORK_DIR/always-fail.sh"
if UPTIME_ROUNDS=1 UPTIME_INTERVAL_SECONDS=0 FAILURE_THRESHOLD=1 \
  UPTIME_CHECK_SCRIPT="$WORK_DIR/always-fail.sh" \
  HEARTBEAT_CHECK_SCRIPT="$WORK_DIR/heartbeat.sh" \
  bash "$LOOP_SCRIPT" "$WORK_DIR/failing"; then
  echo "failing loop unexpectedly succeeded" >&2
  exit 1
fi
grep -Fq 'final_uptime=failure' "$WORK_DIR/failing/final.outcome"

cat >"$WORK_DIR/rotating-fail.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
count_file="$ROTATING_STATE"
count=0
if [ -f "$count_file" ]; then count="$(cat "$count_file")"; fi
count=$((count + 1))
printf '%s' "$count" >"$count_file"
case "$count" in
  1) failed_target=resume ;;
  2) failed_target=sub2api ;;
  *) failed_target=grafana ;;
esac
for target in resume account-vault sub2api areaforge grafana log-gateway; do
  if [ "$target" = "$failed_target" ]; then
    printf 'FAIL\t%s\thttp=503\tcurl_exit=22\terror=test\n' "$target"
  else
    printf 'OK\t%s\t200\t0.010000\n' "$target"
  fi
done
exit 1
EOF
chmod 0755 "$WORK_DIR/rotating-fail.sh"
ROTATING_STATE="$WORK_DIR/rotating-state" \
UPTIME_ROUNDS=3 UPTIME_INTERVAL_SECONDS=0 FAILURE_THRESHOLD=3 \
UPTIME_CHECK_SCRIPT="$WORK_DIR/rotating-fail.sh" \
HEARTBEAT_CHECK_SCRIPT="$WORK_DIR/heartbeat.sh" \
bash "$LOOP_SCRIPT" "$WORK_DIR/rotating"
if grep -Fq $'uptime\tfailure' "$WORK_DIR/rotating/status.tsv"; then
  echo "different target failures were incorrectly combined" >&2
  exit 1
fi
grep -Fq 'final_uptime=pending' "$WORK_DIR/rotating/final.outcome"

echo "external monitor loop: PASS"
