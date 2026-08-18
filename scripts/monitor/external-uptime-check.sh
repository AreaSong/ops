#!/usr/bin/env bash
set -uo pipefail

readonly CURL_TIMEOUT_SECONDS="${CURL_TIMEOUT_SECONDS:-15}"
readonly CURL_CONNECT_TIMEOUT_SECONDS="${CURL_CONNECT_TIMEOUT_SECONDS:-5}"
# Keep a bounded download while allowing the resume page to include its full HTML.
readonly MAX_RESPONSE_BYTES="${MAX_RESPONSE_BYTES:-262144}"
readonly -a TARGETS=(
  "resume|https://resume.areasong.top/|html-marker|JadeAI - AI Resume Builder"
  "account-vault|https://sorryiossearch.areasong.top/health|json-ok|"
  "sub2api|https://cpa.areasong.top/health|json-status-ok|"
  "areaforge|https://forge.areasong.top/api/health|json-forge|"
  "grafana|https://monitor.areasong.top/api/health|json-grafana|"
  "log-gateway|https://log.areasong.top/|html-marker|Welcome to nginx"
)

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

check_target() {
  local index="$1"
  local target="$2"
  local name url match_type match_value result exit_code http_code error_detail error_file body_file header_file
  local -a curl_args
  IFS='|' read -r name url match_type match_value <<<"$target"
  error_file="$WORK_DIR/$index.error"
  body_file="$WORK_DIR/$index.body"
  header_file="$WORK_DIR/$index.headers"

  curl_args=(
    --fail
    --silent
    --show-error
    --location
    --proto '=https'
    --tlsv1.2
    --retry 1
    --retry-delay 2
    --retry-all-errors
    --header 'Cache-Control: no-cache, no-store'
    --header 'Pragma: no-cache'
    --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS"
    --max-time "$CURL_TIMEOUT_SECONDS"
    --output "$body_file"
    --dump-header "$header_file"
    --max-filesize "$MAX_RESPONSE_BYTES"
  )

  if [[ "$name" == grafana ]]; then
    if [[ -n "${CF_ACCESS_CLIENT_ID:-}" && -n "${CF_ACCESS_CLIENT_SECRET:-}" ]]; then
      curl_args+=(
        --header "CF-Access-Client-Id: ${CF_ACCESS_CLIENT_ID}"
        --header "CF-Access-Client-Secret: ${CF_ACCESS_CLIENT_SECRET}"
      )
    elif [[ "${CF_ACCESS_REQUIRED:-false}" == true ]]; then
      printf 'FAIL\t%s\thttp=000\tcurl_exit=2\terror=Cloudflare Access service token is not configured\n' \
        "$name" > "$WORK_DIR/$index"
      return 1
    fi
  fi

  if result="$(curl "${curl_args[@]}" \
    --write-out '%{http_code}\t%{time_total}' \
    "$url" 2>"$error_file")"; then
    if ! validate_response "$match_type" "$match_value" "$body_file"; then
      printf 'FAIL\t%s\thttp=%s\tcurl_exit=0\terror=response content did not match %s\n' \
        "$name" "${result%%$'\t'*}" "$match_type" > "$WORK_DIR/$index"
      return 1
    fi
    if grep -Eiq '^cf-cache-status:[[:space:]]*(HIT|STALE|UPDATING|REVALIDATED)[[:space:]]*$' "$header_file"; then
      printf 'FAIL\t%s\thttp=%s\tcurl_exit=0\terror=response was served from an intermediary cache\n' \
        "$name" "${result%%$'\t'*}" > "$WORK_DIR/$index"
      return 1
    fi
    printf 'OK\t%s\t%s\n' "$name" "$result" > "$WORK_DIR/$index"
  else
    exit_code=$?
    http_code="${result%%$'\t'*}"
    [[ "$http_code" =~ ^[0-9]{3}$ ]] || http_code="000"
    error_detail="$(tr '\r\n\t' ' ' <"$error_file" | cut -c1-300)"
    [ -n "$error_detail" ] || error_detail="none"
    printf 'FAIL\t%s\thttp=%s\tcurl_exit=%s\terror=%s\n' \
      "$name" "$http_code" "$exit_code" "$error_detail" > "$WORK_DIR/$index"
    return 1
  fi
}

validate_response() {
  local match_type="$1"
  local match_value="$2"
  local body_file="$3"

  case "$match_type" in
    html-marker)
      grep -Fq -- "$match_value" "$body_file"
      ;;
    json-ok)
      jq -e 'type == "object" and .ok == true' "$body_file" >/dev/null 2>&1
      ;;
    json-status-ok)
      jq -e 'type == "object" and .status == "ok"' "$body_file" >/dev/null 2>&1
      ;;
    json-grafana)
      jq -e 'type == "object" and .database == "ok" and (.version | type == "string")' "$body_file" >/dev/null 2>&1
      ;;
    json-forge)
      jq -e 'type == "object" and .ok == true and .service == "AreaForge" and (.version | type == "string")' "$body_file" >/dev/null 2>&1
      ;;
    *)
      return 1
      ;;
  esac
}

pids=()
for index in "${!TARGETS[@]}"; do
  check_target "$index" "${TARGETS[$index]}" &
  pids[index]=$!
done

failed=0
for index in "${!TARGETS[@]}"; do
  if ! wait "${pids[$index]}"; then
    failed=1
  fi
done

printf 'checked_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
for index in "${!TARGETS[@]}"; do
  cat "$WORK_DIR/$index"
done

exit "$failed"
