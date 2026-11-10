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

# Service configuration (must reference trusted endpoint)
trusted_endpoint_id="$(value '.trustedEndpointId')"
service_uuid="$(value '.serviceUuid')"
health_url="$(value '.healthUrl')"
latest_allowed="$(jq -r '.latestAllowed // false' <<<"$spec")"

# Load trusted endpoints configuration (root-only)
# Allow override for testing, but default to system location
trusted_config="${COOLIFY_TRUSTED_CONFIG:-/etc/areasong-ops/coolify-trusted-endpoints.json}"
[[ -f "$trusted_config" && ! -L "$trusted_config" ]] || fail "trusted endpoints configuration is missing or unsafe"

# Verify trusted config is root-owned and not world-writable
trusted_config_owner="$(stat -c '%u' "$trusted_config" 2>/dev/null || stat -f '%u' "$trusted_config" 2>/dev/null)"
trusted_config_mode="$(stat -c '%a' "$trusted_config" 2>/dev/null || stat -f '%Lp' "$trusted_config" 2>/dev/null)"
[[ "$trusted_config_owner" == "0" ]] || fail "trusted endpoints configuration must be owned by root"
[[ ! "${trusted_config_mode: -1}" =~ [2367] ]] || fail "trusted endpoints configuration must not be world-writable"

# Resolve trusted endpoint
trusted_spec="$(jq -r --arg id "$trusted_endpoint_id" '.trustedEndpoints[$id] // empty' "$trusted_config")"
[[ -n "$trusted_spec" && "$trusted_spec" != "null" ]] || fail "trusted endpoint '$trusted_endpoint_id' not found in configuration"

coolify_endpoint="$(jq -r '.endpoint' <<<"$trusted_spec")"
token_file="$(jq -r '.tokenFile' <<<"$trusted_spec")"
allowed_services="$(jq -r '.allowedServices[]' <<<"$trusted_spec")"

# Verify service is allowed for this endpoint
service_allowed=false
while IFS= read -r allowed; do
  if [[ "$allowed" == "*" || "$allowed" == "$service" ]]; then
    service_allowed=true
    break
  fi
done <<<"$allowed_services"
[[ "$service_allowed" == "true" ]] || fail "service '$service' is not allowed for endpoint '$trusted_endpoint_id'"

# Validate UUID is in allowlist
uuid_service="$(jq -r --arg uuid "$service_uuid" '.allowedServiceUuids[$uuid] // empty' "$trusted_config")"
[[ -n "$uuid_service" ]] || fail "service UUID '$service_uuid' is not in trusted allowlist"
[[ "$uuid_service" == "$service" ]] || fail "service UUID '$service_uuid' is registered to '$uuid_service', not '$service'"

# Validate configuration
[[ "$service_uuid" =~ ^[a-z0-9]{24}$ ]] || fail "service UUID format is invalid"
[[ "$latest_allowed" == "true" || "$latest_allowed" == "false" ]] || fail "latestAllowed must be boolean"

# Validate endpoint URL (strict parsing, no userinfo, no redirects, safe schemes only)
if [[ "$coolify_endpoint" =~ ^https://([a-zA-Z0-9.-]+)(:[0-9]+)?(/.*)?$ ]]; then
  endpoint_host="${BASH_REMATCH[1]}"
  # Reject dangerous patterns
  [[ ! "$endpoint_host" =~ [@] ]] || fail "endpoint URL must not contain @ (userinfo)"
elif [[ "$coolify_endpoint" =~ ^http://127\.0\.0\.1(:[0-9]+)?(/.*)?$ ]] || \
     [[ "$coolify_endpoint" =~ ^http://localhost(:[0-9]+)?(/.*)?$ ]]; then
  # Allow localhost for testing
  endpoint_host="localhost"
else
  fail "endpoint URL must be https:// with valid hostname, or http://127.0.0.1 / http://localhost for testing"
fi

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
  local token mode group_perm other_perm owner
  require_regular_file "$token_file"
  
  # Check file is not a symlink (require_regular_file checks -L but be explicit)
  [[ ! -L "$token_file" ]] || fail "token file must not be a symbolic link"
  
  # Check owner is root (uid 0)
  owner="$(stat -c '%u' "$token_file" 2>/dev/null || stat -f '%u' "$token_file" 2>/dev/null)"
  [[ "$owner" == "0" ]] || fail "token file must be owned by root (currently uid $owner)"
  
  # Check permissions
  mode="$(stat -c '%a' "$token_file" 2>/dev/null || stat -f '%Lp' "$token_file" 2>/dev/null)"
  group_perm="${mode: -2:1}"
  other_perm="${mode: -1}"
  [[ "$group_perm" == "0" && "$other_perm" == "0" ]] || \
    fail "token file must have mode 0[0-7]00 (currently ${mode}, readable by group or others)"
  
  # Read token
  token="$(cat "$token_file")"
  [[ -n "$token" && ! "$token" =~ [[:space:]] ]] || fail "token is empty or contains whitespace"
  printf '%s' "$token"
}

# Sanitize error output to prevent token and sensitive data leakage
sanitize_error() {
  local message="$1"
  # Redact bearer tokens (case insensitive)
  message="$(echo "$message" | sed -E 's/Bearer[[:space:]]+[A-Za-z0-9_-]+/Bearer [REDACTED]/gi')"
  # Redact authorization headers (full header value)
  message="$(echo "$message" | sed -E 's/Authorization:[[:space:]]*[^[:space:]]+/Authorization: [REDACTED]/gi')"
  # Redact cookie headers
  message="$(echo "$message" | sed -E 's/Cookie:[[:space:]]*[^[:space:]]+/Cookie: [REDACTED]/gi')"
  # Limit message length to prevent leaking large upstream bodies
  if [[ "${#message}" -gt 500 ]]; then
    message="${message:0:500}... [truncated]"
  fi
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
  
  # Handle HTTP status
  if [[ ! "$http_code" =~ ^2[0-9][0-9]$ ]]; then
    local error_body="$(cat "$temp_out" 2>/dev/null || echo '{}')"
    rm -f "$temp_out" "$temp_err"
    # For auth errors (401/403), explicitly fail without retry hint
    if [[ "$http_code" =~ ^(401|403)$ ]]; then
      fail "API authentication failed (HTTP $http_code): check token validity and permissions"
    fi
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
# For "running" state, also check service is not in degraded/unhealthy state
wait_service_state() {
  local uuid="$1"
  local desired_state="$2"
  local timeout_seconds="${3:-120}"
  local start_time elapsed current_state status_data health_status
  
  start_time="$(date +%s)"
  while true; do
    elapsed=$(($(date +%s) - start_time))
    if [[ $elapsed -ge $timeout_seconds ]]; then
      fail "timeout waiting for service $uuid to reach state $desired_state after ${elapsed}s"
    fi
    
    status_data="$(get_service_status "$uuid")" || return $?
    current_state="$(jq -r '.status // "unknown"' <<<"$status_data")"
    
    # Check if state matches
    if [[ "$current_state" == "$desired_state" ]]; then
      # For running state, verify service is not unhealthy
      if [[ "$desired_state" == "running" ]]; then
        health_status="$(jq -r '.health_status // "healthy"' <<<"$status_data")"
        if [[ "$health_status" == "unhealthy" ]]; then
          # Service is running but unhealthy, keep waiting
          sleep 3
          continue
        fi
      fi
      return 0
    fi
    
    # Check for error states
    if [[ "$current_state" =~ ^(error|failed)$ ]]; then
      fail "service entered error state: $current_state"
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
    # Coolify API: restart with latest flag
    # latest=false means pull image only if not present (fixed digest behavior)
    restart_body="$(jq -cn --argjson latest "$latest_allowed" '{latest:$latest}')"
    call_coolify_api POST "/api/v1/services/$service_uuid/restart" "$restart_body" 30 >/dev/null || exit $?
    result "Coolify 服务重启已触发（latest=$latest_allowed）" '{"productionChanged":true}'
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
    # After stop, Coolify may delete the container
    # We verify service reached stopped state, but container may not exist
    wait_service_state "$service_uuid" "stopped" 180
    assert_dependencies_unchanged
    result "服务已停止（容器可能已删除）且依赖服务身份未变化"
    ;;
    
  *)
    fail "unsupported action phase: $action:$phase"
    ;;
esac
