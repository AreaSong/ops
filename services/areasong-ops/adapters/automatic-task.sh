#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

action="${1:-}"
phase="${2:-}"
operation_dir="${3:-}"
target="${4:-}"
source_dir="${5:-}"
task_name="${OPS_SERVICE_NAME:-}"
test_root="${OPS_AUTOMATIC_TASK_TEST_ROOT:-}"
flock_bin="/usr/bin/flock"
if [[ -n "$test_root" ]]; then
  flock_bin="${OPS_AUTOMATIC_TASK_TEST_FLOCK:-$flock_bin}"
fi

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

result() {
  local summary="$1"
  local data="${2:-}"
  [[ -n "$data" ]] || data='{}'
  jq -cn --arg action "$action" --arg phase "$phase" --arg summary "$summary" --argjson data "$data" \
    '{schemaVersion:2,action:$action,phase:$phase,ok:true,summary:$summary,data:$data}'
}

rooted() {
  if [[ -n "$test_root" ]]; then
    printf '%s%s\n' "$test_root" "$1"
  else
    printf '%s\n' "$1"
  fi
}

case "$task_name" in
  runtime-snapshot)
    cron_file="$(rooted /etc/cron.d/ops-runtime-snapshot)"
    executable="$(rooted /var/lib/ops/observability-host-jobs/current/observability/scripts/runtime_snapshot.py)"
    metric_file="$(rooted /var/lib/node_exporter/textfile_collector/runtime-snapshot.prom)"
    metric_name="ops_runtime_snapshot_last_success_timestamp"
    lock_file="$(rooted /run/lock/ops-runtime-snapshot.lock)"
    ;;
  docker-metrics)
    cron_file="$(rooted /etc/cron.d/ops-docker-metrics)"
    executable="$(rooted /var/lib/ops/observability-host-jobs/current/observability/scripts/write-docker-metrics.sh)"
    metric_file="$(rooted /var/lib/node_exporter/textfile_collector/docker.prom)"
    metric_name="docker_metrics_last_run_timestamp"
    lock_file="$(rooted /run/lock/ops-docker-metrics.lock)"
    ;;
  *) fail "automatic task is not allowlisted" ;;
esac

[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"
[[ -z "$target" && -z "$source_dir" ]] || fail "automatic task does not accept target or source"

require_regular_file() {
  [[ -f "$1" && ! -L "$1" ]] || fail "required file is missing or unsafe"
}

metric_timestamp() {
  require_regular_file "$metric_file"
  awk -v metric="$metric_name" '$1 == metric && $2 ~ /^[0-9]+([.][0-9]+)?$/ {print int($2); found=1; exit} END {if (!found) exit 1}' "$metric_file"
}

metric_identity() {
  python3 - "$metric_file" <<'PY'
import os
import sys

value = os.stat(sys.argv[1], follow_symlinks=False)
print(f"{value.st_dev}:{value.st_ino}")
PY
}

timestamp_iso() {
  python3 - "$1" <<'PY'
import datetime
import sys

value = datetime.datetime.fromtimestamp(int(sys.argv[1]), datetime.timezone.utc)
print(value.strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
}

inspect_data() {
  local timestamp now age health
  require_regular_file "$cron_file"
  require_regular_file "$executable"
  [[ -x "$executable" ]] || fail "allowlisted collector is not executable"
  timestamp="$(metric_timestamp)" || fail "freshness evidence is unavailable"
  now="$(date +%s)"
  age=$((now - timestamp))
  (( age >= 0 )) || fail "freshness evidence is in the future"
  health="healthy"
  (( age <= 180 )) || health="stale"
  jq -cn --arg objectId "automatic-task:${task_name}" --arg taskName "$task_name" \
    --arg health "$health" --arg lastSuccessAt "$(timestamp_iso "$timestamp")" \
    --argjson ageSeconds "$age" \
    '{objectId:$objectId,taskName:$taskName,scheduleSource:"cron",enabled:true,
      health:$health,lastSuccessAt:$lastSuccessAt,ageSeconds:$ageSeconds}'
}

case "$action:$phase" in
  inspect:inspect)
    result "自动任务状态检查完成" "$(inspect_data)"
    ;;
  rerun:preflight)
    inspect_data >"$operation_dir/status.before.json"
    metric_timestamp >"$operation_dir/timestamp.before"
    metric_identity >"$operation_dir/identity.before"
    result "补跑前任务身份与新鲜度基线已记录"
    ;;
  rerun:run)
    [[ -f "$operation_dir/timestamp.before" && ! -L "$operation_dir/timestamp.before" ]] || fail "preflight evidence is missing"
    mkdir -p "$(dirname "$lock_file")"
    "$flock_bin" -n "$lock_file" "$executable" >/dev/null || fail "collector is already running or failed"
    result "固定采集任务执行完成"
    ;;
  rerun:verify)
    before="$(<"$operation_dir/timestamp.before")"
    [[ "$before" =~ ^[0-9]+$ ]] || fail "preflight timestamp is invalid"
    after="$(metric_timestamp)" || fail "freshness evidence is unavailable after rerun"
    (( after >= before )) || fail "freshness evidence moved backwards"
	before_identity="$(<"$operation_dir/identity.before")"
    after_identity="$(metric_identity)"
	[[ "$after_identity" != "$before_identity" ]] || fail "collector did not publish new evidence"
    data="$(inspect_data)"
    [[ "$(jq -r '.health' <<<"$data")" == healthy ]] || fail "rerun evidence is stale"
    result "补跑结果和新鲜度验证通过" "$data"
    ;;
  *) fail "unsupported automatic task action phase" ;;
esac
