#!/usr/bin/env bash
set -uo pipefail

if [ "$#" -ge 1 ]; then
  output_dir="$1"
else
  output_dir="external-monitor-rounds"
fi
rounds="$(printenv UPTIME_ROUNDS 2>/dev/null || printf '1')"
interval_seconds="$(printenv UPTIME_INTERVAL_SECONDS 2>/dev/null || printf '60')"
failure_threshold="$(printenv FAILURE_THRESHOLD 2>/dev/null || printf '3')"
uptime_check_script="$(printenv UPTIME_CHECK_SCRIPT 2>/dev/null || printf 'scripts/monitor/external-uptime-check.sh')"
heartbeat_check_script="$(printenv HEARTBEAT_CHECK_SCRIPT 2>/dev/null || printf 'scripts/monitor/external-heartbeat-check.sh')"
uptime_targets='resume account-vault sub2api areaforge grafana log-gateway'
if ! [[ "$rounds" =~ ^[1-9][0-9]*$ ]] || ! [[ "$interval_seconds" =~ ^[0-9]+$ ]] || \
  ! [[ "$failure_threshold" =~ ^[1-9][0-9]*$ ]]; then
  echo "invalid UPTIME_ROUNDS, UPTIME_INTERVAL_SECONDS, or FAILURE_THRESHOLD" >&2
  exit 2
fi

mkdir -p "$output_dir"
status_file="$output_dir/status.tsv"
: >"$status_file"

last_uptime_outcome=success
last_heartbeat_outcome=success
heartbeat_failure_streak=0
round=1
while [ "$round" -le "$rounds" ]; do
  uptime_file="$output_dir/round-$round-uptime.txt"
  heartbeat_file="$output_dir/round-$round-heartbeat.txt"

  bash "$uptime_check_script" >"$uptime_file" 2>&1 || true
  if bash "$heartbeat_check_script" >"$heartbeat_file" 2>&1; then
    heartbeat_raw_outcome=success
  else
    heartbeat_raw_outcome=failure
  fi

  uptime_any_failure=0
  uptime_any_pending=0
  for target in $uptime_targets; do
    state_file="$output_dir/uptime-streak-$target"
    target_streak=0
    if [ -f "$state_file" ]; then
      target_streak="$(cat "$state_file")"
    fi
    if grep -Fq "$(printf 'FAIL\t%s\t' "$target")" "$uptime_file"; then
      target_streak=$((target_streak + 1))
      if [ "$target_streak" -ge "$failure_threshold" ]; then
        uptime_any_failure=1
      else
        uptime_any_pending=1
      fi
    elif grep -Fq "$(printf 'OK\t%s\t' "$target")" "$uptime_file"; then
      target_streak=0
    else
      target_streak=$((target_streak + 1))
      if [ "$target_streak" -ge "$failure_threshold" ]; then
        uptime_any_failure=1
      else
        uptime_any_pending=1
      fi
    fi
    printf '%s' "$target_streak" >"$state_file"
  done
  if [ "$heartbeat_raw_outcome" = failure ]; then
    heartbeat_failure_streak=$((heartbeat_failure_streak + 1))
  else
    heartbeat_failure_streak=0
  fi
  if [ "$uptime_any_failure" -eq 1 ]; then
    uptime_notify_outcome=failure
  elif [ "$uptime_any_pending" -eq 1 ]; then
    uptime_notify_outcome=pending
  else
    uptime_notify_outcome=success
  fi
  if [ "$heartbeat_failure_streak" -ge "$failure_threshold" ]; then
    heartbeat_notify_outcome=failure
  elif [ "$heartbeat_raw_outcome" = success ]; then
    heartbeat_notify_outcome=success
  else
    heartbeat_notify_outcome=pending
  fi

  printf '%s\t%s\t%s\n' uptime "$uptime_notify_outcome" "$uptime_file" >>"$status_file"
  printf '%s\t%s\t%s\n' heartbeat "$heartbeat_notify_outcome" "$heartbeat_file" >>"$status_file"
  cat "$uptime_file"
  cat "$heartbeat_file"
  last_uptime_outcome="$uptime_notify_outcome"
  last_heartbeat_outcome="$heartbeat_notify_outcome"

  if [ "$round" -lt "$rounds" ] && [ "$interval_seconds" -gt 0 ]; then
    sleep "$interval_seconds"
  fi
  round=$((round + 1))
done

printf 'final_uptime=%s\nfinal_heartbeat=%s\n' \
  "$last_uptime_outcome" "$last_heartbeat_outcome" >"$output_dir/final.outcome"
if [ "$last_uptime_outcome" = failure ] || [ "$last_heartbeat_outcome" = failure ]; then
  exit 1
fi
