#!/bin/bash -p
set -Eeuo pipefail

umask 077

action="${1:-}"
phase="${2:-}"
operation_dir="${3:-}"
target="${4:-}"
source_dir="${5:-}"
service="${OPS_SERVICE_NAME:-}"
catalog="${OPS_SERVICE_CATALOG:-/etc/areasong-ops/services.json}"

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

[[ "$service" =~ ^[a-z][a-z0-9-]{1,39}$ ]] || fail "service name is invalid"
[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"

case "$action" in
  inspect|check|backup|restart|start|stop)
    ;;
  prepare|update|rollback|restore|restore-drill)
    fail "unsupported action: $action (not yet implemented for Coolify adapter)"
    ;;
  enter-maintenance|drain|resume-traffic)
    fail "unsupported traffic action: $action (Nginx traffic still managed by host)"
    ;;
  *)
    fail "unsupported action"
    ;;
esac

[[ -f "$catalog" && ! -L "$catalog" ]] || fail "service catalog is unsafe"

spec="$(jq -cer --arg service "$service" '.services[$service] | select(.template == "coolify-service-v1") | .runtime' "$catalog")"
value() { jq -er "$1" <<<"$spec"; }

# Coolify runtime configuration
coolify_endpoint="$(value '.coolifyEndpoint')"
service_uuid="$(value '.serviceUuid')"
token_file="$(value '.tokenFile')"
health_url="$(value '.healthUrl')"
latest_allowed="$(jq -r '.latestAllowed // false' <<<"$spec")"

# Validate configuration
[[ "$service_uuid" =~ ^[a-z0-9]{24}$ ]] || fail "service UUID format is invalid"
[[ "$coolify_endpoint" =~ ^https?:// ]] || fail "Coolify endpoint must use http or https scheme"
[[ "$latest_allowed" == "true" || "$latest_allowed" == "false" ]] || fail "latestAllowed must be boolean"

# Dependency configuration (optional)
dependency_uuids=()
while IFS= read -r uuid; do
  [[ -n "$uuid" ]] && dependency_uuids+=("$uuid")
done < <(jq -r '.dependencyServiceUuids[]?' <<<"$spec")

require_regular_file() {
  [[ -f "$1" && ! -L "$1" ]] || fail "required file is missing or unsafe: $1"
}

# Read token from secure file (never from environment or argv)
read_token() {
  local token mode group_perm other_perm
  require_regular_file "$token_file"
  mode="$(stat -c '%a' "$token_file" 2>/dev/null || stat -f '%Lp' "$token_file" 2>/dev/null)"
  # Extract group and other permissions (last two digits)
  group_perm="${mode: -2:1}"
  other_perm="${mode: -1}"
  # Both must be 0 (no read, write, or execute for group or others)
  [[ "$group_perm" == "0" && "$other_perm" == "0" ]] || \
    fail "token file must have mode 0[0-7]00 (currently ${mode}, readable by group or others)"
  token="$(cat "$token_file")"
  [[ -n "$token" && ! "$token" =~ [[:space:]] ]] || fail "token is empty or contains whitespace"
  printf '%s' "$token"
}

# Sanitize error output to prevent token leakage
sanitize_error() {
  local message="$1"
  # Redact anything that looks like a bearer token
  message="${message//Bearer [A-Za-z0-9_-]*/Bearer [REDACTED]}"
  # Redact authorization headers
  message="${message//Authorization: [^[:space:]]*/Authorization: [REDACTED]}"
  printf '%s' "$message"
}

# Call Coolify API with timeout and error handling
call_coolify_api() {
  local method="$1"
  local path="$2"
  local body="${3:-}"
  local timeout="${4:-30}"
  local token response http_code temp_out temp_err exit_code
  
  # Read token and check if it failed
  token="$(read_token)" || return $?
  [[ -n "$token" ]] || fail "token is empty after read"
  
  temp_out="$(mktemp "$operation_dir/.api-out.XXXXXX")"
  temp_err="$(mktemp "$operation_dir/.api-err.XXXXXX")"
  
  set +e
  if [[ -n "$body" ]]; then
    http_code="$(curl -sS -X "$method" \
      -H "Authorization: Bearer $token" \
      -H "Content-Type: application/json" \
      -H "Accept: application/json" \
      --max-time "$timeout" \
      -d "$body" \
      -o "$temp_out" \
      -w '%{http_code}' \
      "${coolify_endpoint}${path}" 2>"$temp_err")"
    exit_code=$?
  else
    http_code="$(curl -sS -X "$method" \
      -H "Authorization: Bearer $token" \
      -H "Accept: application/json" \
      --max-time "$timeout" \
      -o "$temp_out" \
      -w '%{http_code}' \
      "${coolify_endpoint}${path}" 2>"$temp_err")"
    exit_code=$?
  fi
  set -e
  
  # Clear token from memory
  unset token
  
  if [[ $exit_code -ne 0 ]]; then
    local error_msg="$(cat "$temp_err" 2>/dev/null || echo 'curl failed')"
    rm -f "$temp_out" "$temp_err"
    fail "API call failed: $(sanitize_error "$error_msg")"
  fi
  
  # Handle HTTP status - anything 2xx is success
  if [[ ! "$http_code" =~ ^2[0-9][0-9]$ ]]; then
    local error_body="$(cat "$temp_out" 2>/dev/null || echo '{}')"
    rm -f "$temp_out" "$temp_err"
    fail "API returned HTTP $http_code: $(sanitize_error "$error_body")"
  fi
  
  response="$(cat "$temp_out")"
  rm -f "$temp_out" "$temp_err"
  printf '%s' "$response"
}

# Get service status from Coolify
get_service_status() {
  local uuid="$1"
  local response
  response="$(call_coolify_api GET "/api/v1/services/$uuid" "" 10)" || return $?
  printf '%s' "$response"
}

# Wait for service to reach desired state with bounded timeout
wait_service_state() {
  local uuid="$1"
  local desired_state="$2"
  local timeout_seconds="${3:-120}"
  local start_time elapsed current_state status_data
  
  start_time="$(date +%s)"
  while true; do
    elapsed=$(($(date +%s) - start_time))
    if [[ $elapsed -ge $timeout_seconds ]]; then
      fail "timeout waiting for service $uuid to reach state $desired_state after ${elapsed}s"
    fi
    
    status_data="$(get_service_status "$uuid")" || return $?
    current_state="$(jq -r '.status // "unknown"' <<<"$status_data")"
    if [[ "$current_state" == "$desired_state" ]]; then
      return 0
    fi
    
    sleep 3
  done
}

# Verify health endpoint
verify_health() {
  local timeout=120
  local start_time elapsed http_code temp_out
  
  temp_out="$(mktemp "$operation_dir/.health.XXXXXX")"
  start_time="$(date +%s)"
  
  while true; do
    elapsed=$(($(date +%s) - start_time))
    if [[ $elapsed -ge $timeout ]]; then
      rm -f "$temp_out"
      fail "health check timeout after ${elapsed}s"
    fi
    
    http_code="$(curl -sSf -o "$temp_out" -w '%{http_code}' --max-time 5 "$health_url" 2>/dev/null || echo '000')"
    if [[ "$http_code" == "200" ]]; then
      rm -f "$temp_out"
      return 0
    fi
    
    sleep 2
  done
}

# Snapshot dependency states
snapshot_dependencies() {
  local output="$1"
  local deps=() status_data
  
  for uuid in "${dependency_uuids[@]}"; do
    local status
    status_data="$(get_service_status "$uuid")" || return $?
    status="$(jq -cS '{uuid:$uuid,status:.status,updatedAt:.updated_at}' --arg uuid "$uuid" <<<"$status_data")"
    deps+=("$status")
  done
  
  printf '%s\n' "${deps[@]}" | jq -cS -s '.' >"$output"
}

# Verify dependencies unchanged
assert_dependencies_unchanged() {
  local baseline="$operation_dir/dependencies.before.json"
  [[ -f "$baseline" && ! -L "$baseline" ]] || fail "dependency baseline is missing"
  
  snapshot_dependencies "$operation_dir/dependencies.after.json"
  
  if ! cmp -s "$baseline" "$operation_dir/dependencies.after.json"; then
    fail "dependency service state changed unexpectedly"
  fi
}

case "$action:$phase" in
  inspect:inspect)
    status_data="$(get_service_status "$service_uuid")" || exit $?
    current_status="$(jq -r '.status // "unknown"' <<<"$status_data")"
    result "Coolify 服务状态检查完成" "$(jq -cn --arg status "$current_status" --arg uuid "$service_uuid" \
      '{serviceUuid:$uuid,currentStatus:$status}')"
    ;;
    
  check:discover)
    # For now, discovery is not implemented - Coolify doesn't have a release catalog concept
    # This would need integration with the actual deployment mechanism
    result "Coolify 服务发现检查（未实现完整发布流程）" \
      '{"updateAvailable":false,"message":"Coolify adapter does not support automatic update discovery yet"}'
    ;;
    
  backup:preflight|backup:backup|backup:verify)
    # Backup operations are not supported through Coolify API
    # These would still use the host-level backup scripts
    fail "backup operations must use host-level scripts, not Coolify API"
    ;;
    
  restart:preflight)
    snapshot_dependencies "$operation_dir/dependencies.before.json"
    result "依赖服务基线已记录"
    ;;
    
  restart:restart)
    call_coolify_api POST "/api/v1/services/$service_uuid/restart" "" 30 >/dev/null || exit $?
    result "Coolify 服务重启已触发" '{"productionChanged":true}'
    ;;
    
  restart:health|restart:smoke)
    # Coolify restart is async - need to wait for service to be running
    wait_service_state "$service_uuid" "running" 180
    verify_health
    assert_dependencies_unchanged
    result "服务健康检查与依赖身份检查通过"
    ;;
    
  start:preflight|stop:preflight)
    snapshot_dependencies "$operation_dir/dependencies.before.json"
    result "依赖服务基线已记录"
    ;;
    
  start:start)
    call_coolify_api POST "/api/v1/services/$service_uuid/start" "" 30 >/dev/null || exit $?
    result "Coolify 服务启动已触发" '{"productionChanged":true}'
    ;;
    
  start:health)
    wait_service_state "$service_uuid" "running" 180
    verify_health
    assert_dependencies_unchanged
    result "服务启动后健康检查与依赖身份检查通过"
    ;;
    
  stop:stop)
    call_coolify_api POST "/api/v1/services/$service_uuid/stop" "" 30 >/dev/null || exit $?
    result "Coolify 服务停止已触发" '{"productionChanged":true}'
    ;;
    
  stop:health)
    # After stop, Coolify may delete the container - verify service is not running
    wait_service_state "$service_uuid" "stopped" 180
    assert_dependencies_unchanged
    result "服务已停止且依赖服务身份未变化"
    ;;
    
  *)
    fail "unsupported action phase: $action:$phase"
    ;;
esac
