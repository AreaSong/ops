#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

action="${1:-}"
phase="${2:-}"
operation_dir="${3:-}"
target="${4:-}"
source_dir="${5:-}"
service="${OPS_SERVICE_NAME:-}"
policy_json="${OPS_TRAFFIC_POLICY_JSON:-}"
nginx_executable="${OPS_TRAFFIC_NGINX_EXECUTABLE:-${NGINX_EXECUTABLE:-/usr/sbin/nginx}}"
systemctl_executable="${OPS_TRAFFIC_SYSTEMCTL_EXECUTABLE:-${SYSTEMCTL_EXECUTABLE:-/usr/bin/systemctl}}"

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
[[ -z "$target" && -z "$source_dir" ]] || fail "traffic adapter does not accept target or source directory"

case "$action:$phase" in
  traffic:inspect|traffic-inspect:inspect)
    ;;
  enter-maintenance:preflight|enter-maintenance:enter-maintenance|enter-maintenance:verify|enter-maintenance:health|enter-maintenance:rollback)
    ;;
  drain:preflight|drain:drain|drain:verify|drain:health|drain:rollback|drain-traffic:preflight|drain-traffic:drain-traffic|drain-traffic:verify|drain-traffic:health|drain-traffic:rollback)
    ;;
  resume-traffic:preflight|resume-traffic:resume-traffic|resume-traffic:verify|resume-traffic:health|resume-traffic:rollback)
    ;;
  *)
    fail "unsupported traffic action or phase"
    ;;
esac

[[ -n "$policy_json" ]] || fail "traffic policy is missing"
jq -e 'type == "object"' <<<"$policy_json" >/dev/null 2>&1 || fail "traffic policy is invalid JSON"

policy_keys="$(jq -c 'keys | sort' <<<"$policy_json")"
expected_policy_keys='["adapterPath","drainTimeoutSeconds","hostname","includeFile","maintenanceFile","marker","siteFile"]'
if [[ "$policy_keys" != "$expected_policy_keys" ]]; then
  fail "traffic policy contains missing or unsupported fields"
fi

adapter_path="$(jq -er '.adapterPath | select(type == "string")' <<<"$policy_json")" || fail "traffic adapter path is invalid"
hostname="$(jq -er '.hostname | select(type == "string")' <<<"$policy_json")" || fail "traffic hostname is invalid"
site_file="$(jq -er '.siteFile | select(type == "string")' <<<"$policy_json")" || fail "traffic site path is invalid"
include_file="$(jq -er '.includeFile | select(type == "string")' <<<"$policy_json")" || fail "traffic include path is invalid"
maintenance_file="$(jq -er '.maintenanceFile | select(type == "string")' <<<"$policy_json")" || fail "traffic maintenance path is invalid"
marker="$(jq -er '.marker | select(type == "string")' <<<"$policy_json")" || fail "traffic marker is invalid"
drain_timeout="$(jq -er '.drainTimeoutSeconds | select(type == "number" and floor == .)' <<<"$policy_json")" || fail "traffic drain timeout is invalid"

[[ "$adapter_path" == "/usr/local/libexec/areasong-ops/adapters/nginx-traffic.sh" ]] || fail "traffic adapter path is not trusted"
[[ "$hostname" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ && "$hostname" == *.* ]] || fail "traffic hostname is invalid"
[[ ${#marker} -ge 8 && ${#marker} -le 128 && "$marker" != *$'\n'* && "$marker" != *$'\r'* ]] || fail "traffic marker is invalid"
[[ "$drain_timeout" -ge 10 && "$drain_timeout" -le 3600 ]] || fail "traffic drain timeout is invalid"

validate_policy_path() {
  local path="$1" prefix="$2"
  [[ "$path" == /* && "$path" != *"//"* && "$path" != */./* && "$path" != *"/../"* && "$path" != */.. && "$path" != */. ]] ||
    fail "traffic policy path is not canonical: $path"
  [[ "$path" == "$prefix"*.conf ]] || fail "traffic policy path is outside the controlled Nginx directory: $path"
}

validate_policy_path "$site_file" "/etc/nginx/sites-enabled/"
validate_policy_path "$include_file" "/etc/nginx/snippets/areasong-ops/"
validate_policy_path "$maintenance_file" "/etc/nginx/snippets/areasong-ops/"
[[ "$include_file" != "$maintenance_file" ]] || fail "traffic include and maintenance paths must differ"

test_root="${OPS_TRAFFIC_TEST_ROOT:-}"
if [[ -n "$test_root" ]]; then
  [[ "$EUID" -ne 0 ]] || fail "traffic test root is forbidden for root"
  [[ "$test_root" == /* && -d "$test_root" && ! -L "$test_root" ]] || fail "traffic test root is unsafe"
  test_root="$(cd "$test_root" && pwd -P)"
fi

test_drain_state_file="${OPS_TRAFFIC_TEST_DRAIN_STATE_FILE:-}"
test_drain_timeout="${OPS_TRAFFIC_TEST_DRAIN_TIMEOUT_SECONDS:-}"
test_drain_poll="${OPS_TRAFFIC_TEST_DRAIN_POLL_SECONDS:-}"
if [[ -n "$test_root" ]]; then
  if [[ -n "$test_drain_state_file" ]]; then
    [[ "$test_drain_state_file" == "$test_root"/* && -f "$test_drain_state_file" && ! -L "$test_drain_state_file" ]] ||
      fail "traffic test drain state file is unsafe"
  fi
  [[ -z "$test_drain_timeout" || "$test_drain_timeout" =~ ^[1-9][0-9]*$ ]] || fail "traffic test drain timeout is invalid"
  [[ -z "$test_drain_poll" || "$test_drain_poll" =~ ^0\.[0-9]+$|^[1-9][0-9]*(\.[0-9]+)?$ ]] ||
    fail "traffic test drain poll is invalid"
else
  [[ "$EUID" -eq 0 ]] || fail "traffic adapter requires root"
  [[ -z "$test_drain_state_file$test_drain_timeout$test_drain_poll" ]] || fail "traffic test controls are forbidden in production"
fi

runtime_path() {
  local path="$1"
  if [[ -n "$test_root" ]]; then
    printf '%s%s\n' "${test_root%/}" "$path"
  else
    printf '%s\n' "$path"
  fi
}

site_path="$(runtime_path "$site_file")"
include_path="$(runtime_path "$include_file")"
maintenance_path="$(runtime_path "$maintenance_file")"

assert_safe_chain() {
  local path="$1" require_file="$2" cursor parent boundary
  [[ "$path" == /* ]] || fail "runtime path is not absolute"
  boundary="/"
  [[ -z "$test_root" ]] || boundary="$test_root"
  cursor="$path"
  while [[ "$cursor" != "$boundary" ]]; do
    [[ "$cursor" == "$boundary"/* ]] || fail "runtime path escaped the traffic test root"
    if [[ -L "$cursor" ]]; then
      fail "symlink is forbidden in controlled Nginx path: $cursor"
    fi
    cursor="${cursor%/*}"
    [[ -n "$cursor" ]] || cursor="/"
  done
  [[ ! -L "$boundary" ]] || fail "controlled Nginx path boundary is a symlink"
  parent="${path%/*}"
  [[ -d "$parent" && ! -L "$parent" ]] || fail "controlled Nginx parent directory is unsafe: $parent"
  if [[ "$require_file" == true ]]; then
    [[ -f "$path" && ! -L "$path" ]] || fail "controlled Nginx file is missing or unsafe: $path"
  elif [[ -e "$path" ]]; then
    [[ -f "$path" && ! -L "$path" ]] || fail "controlled Nginx file is unsafe: $path"
  fi
}

assert_safe_chain "$site_path" true
assert_safe_chain "$include_path" false
assert_safe_chain "$maintenance_path" true

assert_root_owned_readonly() {
  local path="$1" uid mode mode_value
  [[ -z "$test_root" ]] || return 0
  uid="$(stat -c %u "$path")" || fail "cannot inspect controlled Nginx owner: $path"
  mode="$(stat -c %a "$path")" || fail "cannot inspect controlled Nginx mode: $path"
  [[ "$uid" == 0 && "$mode" =~ ^[0-7]{3,4}$ ]] || fail "controlled Nginx file ownership is unsafe: $path"
  mode_value=$((8#$mode))
  (( (mode_value & 8#022) == 0 )) || fail "controlled Nginx file is group/other writable: $path"
}

assert_root_owned_readonly "$site_path"
assert_root_owned_readonly "$maintenance_path"
if [[ -e "$include_path" ]]; then
  assert_root_owned_readonly "$include_path"
fi

marker_line="include $include_file;"
[[ "$marker" == "$marker_line" ]] || fail "traffic marker must exactly match the controlled include directive"
marker_count="$(awk -v marker="$marker_line" '
  {
    line=$0
    sub(/^[[:space:]]+/, "", line)
    sub(/[[:space:]]+$/, "", line)
    if (line == marker) count++
  }
  END { print count + 0 }
' "$site_path")"
[[ "$marker_count" -eq 1 ]] || fail "site file does not contain the exact traffic include marker once"

site_contains_hostname() {
  awk -v hostname="$hostname" '
    /^[[:space:]]*#/ { next }
    {
      line=$0
      sub(/[[:space:]]*#.*/, "", line)
      if (line !~ /^[[:space:]]*server_name[[:space:]]+/) next
      sub(/^[[:space:]]*server_name[[:space:]]+/, "", line)
      sub(/[[:space:]]*;[[:space:]]*$/, "", line)
      count=split(line, names, /[[:space:]]+/)
      for (name_index=1; name_index<=count; name_index++) if (names[name_index] == hostname) found=1
    }
    END { exit(found ? 0 : 1) }
  ' "$site_path"
}
site_contains_hostname || fail "site file server_name does not contain the configured hostname"

running_template() {
  printf '%s\n' '# AreaSong Ops managed traffic state: running'
}

maintenance_template() {
	printf '%s\n' \
		'# AreaSong Ops managed traffic state: maintenance' \
		"include $maintenance_file;"
}

drained_template() {
  printf '%s\n' \
    '# AreaSong Ops managed traffic state: drained' \
    '# A graceful Nginx reload blocks new requests; requests already handled by old workers complete naturally.' \
    'default_type text/plain;' \
    'return 503 "Service is draining; no new requests are accepted.\n";'
}

state_for_content() {
  local content="$1"
  if [[ -z "$content" || "$content" == "$(running_template)" ]]; then
    printf '%s\n' running
  elif [[ "$content" == "$(maintenance_template)" ]]; then
    printf '%s\n' maintenance
  elif [[ "$content" == "$(drained_template)" ]]; then
    printf '%s\n' drained
  else
    return 1
  fi
}

read_managed_state() {
  local content
  if [[ ! -e "$include_path" ]]; then
    printf '%s\n' running
    return
  fi
  [[ -f "$include_path" && ! -L "$include_path" ]] || fail "traffic include is unsafe"
  content="$(cat "$include_path")"
  state_for_content "$content" || fail "traffic include content is not a managed closed-set template"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print "sha256:"$1}'
  else
    shasum -a 256 "$1" | awk '{print "sha256:"$1}'
  fi
}

state_data() {
	local state="$1" changed="$2" digest="" maintenance_digest
  if [[ -f "$include_path" ]]; then
    digest="$(sha256_file "$include_path")"
  else
    digest="sha256:$(running_template | { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; } | awk '{print $1}')"
  fi
	maintenance_digest="$(sha256_file "$maintenance_path")"
	jq -cn --arg actualState "$state" --arg trafficState "$state" --arg hostname "$hostname" \
		--arg includeDigest "$digest" --arg maintenanceDigest "$maintenance_digest" \
		--argjson productionChanged "$changed" --argjson drainTimeoutSeconds "$drain_timeout" \
		'{actualState:$actualState,trafficState:$trafficState,hostname:$hostname,
			includeDigest:$includeDigest,maintenanceDigest:$maintenanceDigest,productionChanged:$productionChanged,
			drainTimeoutSeconds:$drainTimeoutSeconds}'
}

write_atomic() {
  local content="$1" temporary
  temporary="$(mktemp "${include_path}.tmp.XXXXXX")"
  printf '%s\n' "$content" >"$temporary"
  chmod 0644 "$temporary"
  mv -f "$temporary" "$include_path"
}

reload_nginx() {
  [[ -x "$nginx_executable" && ! -L "$nginx_executable" ]] || fail "nginx executable is missing or unsafe"
  [[ -x "$systemctl_executable" && ! -L "$systemctl_executable" ]] || fail "systemctl executable is missing or unsafe"
  "$nginx_executable" -t >/dev/null || return $?
  "$systemctl_executable" reload nginx >/dev/null || return $?
}

snapshot_file="$operation_dir/nginx-traffic.include.before"
snapshot_meta="$operation_dir/nginx-traffic.include.before.json"

capture_snapshot() {
	local current_state="$1" existed=false digest=""
	if [[ -e "$snapshot_file" || -e "$snapshot_meta" ]]; then
		[[ -f "$snapshot_meta" && ! -L "$snapshot_meta" ]] || fail "traffic snapshot metadata is unsafe"
		existed="$(jq -er '.existed | select(type == "boolean")' "$snapshot_meta")" || fail "traffic snapshot metadata is invalid"
		if [[ "$existed" == true ]]; then
			[[ -f "$snapshot_file" && ! -L "$snapshot_file" ]] || fail "traffic snapshot file is unsafe"
			digest="$(jq -er '.digest | select(type == "string")' "$snapshot_meta")" || fail "traffic snapshot digest is invalid"
			[[ "$(sha256_file "$snapshot_file")" == "$digest" ]] || fail "traffic snapshot digest changed"
		else
			[[ ! -e "$snapshot_file" ]] || fail "traffic snapshot metadata disagrees with snapshot file"
		fi
		return 0
	fi
  if [[ -f "$include_path" ]]; then
    cp -p "$include_path" "$snapshot_file"
    chmod 0600 "$snapshot_file"
    existed=true
    digest="$(sha256_file "$snapshot_file")"
  fi
  jq -cn --arg state "$current_state" --arg digest "$digest" --argjson existed "$existed" \
    '{state:$state,existed:$existed,digest:$digest}' >"$snapshot_meta"
  chmod 0600 "$snapshot_meta"
}

nginx_worker_pids() {
	local master_pid pid command_line
	if [[ -n "$test_root" ]]; then
		[[ -n "$test_drain_state_file" ]] && tr '\n' ' ' <"$test_drain_state_file"
		return 0
	fi
	[[ -r /run/nginx.pid && ! -L /run/nginx.pid ]] || fail "Nginx master pid file is missing or unsafe"
	master_pid="$(tr -d '[:space:]' </run/nginx.pid)"
	[[ "$master_pid" =~ ^[1-9][0-9]*$ && -d "/proc/$master_pid" ]] || fail "Nginx master pid is invalid"
	[[ -r "/proc/$master_pid/task/$master_pid/children" ]] || fail "Nginx worker list is unavailable"
	for pid in $(<"/proc/$master_pid/task/$master_pid/children"); do
		[[ "$pid" =~ ^[1-9][0-9]*$ && -r "/proc/$pid/cmdline" ]] || continue
		command_line="$(tr '\0' ' ' <"/proc/$pid/cmdline")"
		[[ "$command_line" == nginx:\ worker\ process* ]] && printf '%s ' "$pid"
	done
}

wait_for_old_workers() {
	local old_workers="$1" timeout="$drain_timeout" poll=1 started now current pid remaining
	[[ -n "${old_workers//[[:space:]]/}" ]] || {
		[[ -n "$test_root" ]] && return 0
		fail "no Nginx workers were found before drain"
	}
	[[ -z "$test_drain_timeout" ]] || timeout="$test_drain_timeout"
	[[ -z "$test_drain_poll" ]] || poll="$test_drain_poll"
	started="$(date +%s)"
	while true; do
		current=" $(nginx_worker_pids) "
		remaining=""
		for pid in $old_workers; do
			[[ "$current" == *" $pid "* ]] && remaining+=" $pid"
		done
		[[ -z "$remaining" ]] && return 0
		now="$(date +%s)"
		(( now - started < timeout )) || fail "Nginx drain timed out with old workers:$remaining"
		sleep "$poll"
	done
}

restore_snapshot() {
  local existed expected_digest
  [[ -f "$snapshot_meta" && ! -L "$snapshot_meta" ]] || return 1
  existed="$(jq -er '.existed | select(type == "boolean")' "$snapshot_meta")" || return 1
  if [[ "$existed" == true ]]; then
    [[ -f "$snapshot_file" && ! -L "$snapshot_file" ]] || return 1
    expected_digest="$(jq -er '.digest | select(type == "string")' "$snapshot_meta")" || return 1
    [[ "$(sha256_file "$snapshot_file")" == "$expected_digest" ]] || return 1
    write_atomic "$(cat "$snapshot_file")"
  else
    [[ ! -e "$snapshot_file" ]] || return 1
    write_atomic "$(running_template)"
  fi
}

desired_state_for_action() {
  case "$action" in
    enter-maintenance) printf '%s\n' maintenance ;;
    drain|drain-traffic) printf '%s\n' drained ;;
    resume-traffic) printf '%s\n' running ;;
    *) return 1 ;;
  esac
}

template_for_state() {
  case "$1" in
    running) running_template ;;
    maintenance) maintenance_template ;;
    drained) drained_template ;;
    *) return 1 ;;
  esac
}

current_state="$(read_managed_state)"

case "$phase" in
  inspect)
    result "Nginx traffic state inspected" "$(state_data "$current_state" false)"
    ;;
  preflight)
    desired_state="$(desired_state_for_action)"
    result "Nginx traffic change preflight passed" "$(state_data "$current_state" false | jq -c --arg desiredState "$desired_state" '. + {desiredState:$desiredState}')"
    ;;
	enter-maintenance|drain|drain-traffic|resume-traffic)
		desired_state="$(desired_state_for_action)"
    if [[ "$current_state" == "$desired_state" ]]; then
      result "Nginx traffic state already matches the request" "$(state_data "$current_state" false)"
      exit 0
    fi
		capture_snapshot "$current_state"
		old_workers=""
		if [[ "$action" == drain || "$action" == drain-traffic ]]; then
			old_workers="$(nginx_worker_pids)"
		fi
    desired_content="$(template_for_state "$desired_state")"
    write_atomic "$desired_content"
    set +e
    reload_nginx
    change_rc=$?
    set -e
    if [[ "$change_rc" -ne 0 ]]; then
      set +e
      restore_snapshot 2>/dev/null
      restore_rc=$?
      if [[ "$restore_rc" -eq 0 ]]; then
        reload_nginx
        rollback_rc=$?
      else
        rollback_rc="$restore_rc"
      fi
      set -e
      if [[ "$rollback_rc" -ne 0 ]]; then
        fail "Nginx traffic change failed and rollback could not be completed"
      fi
      fail "Nginx traffic change failed; previous include was restored and reloaded"
    fi
		current_state="$(read_managed_state)"
		[[ "$current_state" == "$desired_state" ]] || fail "traffic include state changed unexpectedly"
		if [[ "$action" == drain || "$action" == drain-traffic ]]; then
			wait_for_old_workers "$old_workers"
			result "Nginx traffic state changed and existing workers drained" \
				"$(state_data "$current_state" true | jq -c '. + {drainCompleted:true}')"
		else
			result "Nginx traffic state changed" "$(state_data "$current_state" true)"
		fi
    ;;
  verify|health)
    desired_state="$(desired_state_for_action)"
    [[ "$current_state" == "$desired_state" ]] || fail "traffic state verification failed"
    result "Nginx traffic state verified" "$(state_data "$current_state" false)"
    ;;
  rollback)
    restore_snapshot || fail "Nginx traffic rollback snapshot is missing or invalid"
    if ! reload_nginx; then
      fail "Nginx traffic rollback failed; manual recovery is required"
    fi
    current_state="$(read_managed_state)"
    result "Nginx traffic state rolled back" "$(state_data "$current_state" true)"
    ;;
esac
