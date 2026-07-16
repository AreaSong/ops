#!/usr/bin/env bash
set -uo pipefail

readonly CURL_TIMEOUT_SECONDS="${CURL_TIMEOUT_SECONDS:-15}"
readonly CURL_CONNECT_TIMEOUT_SECONDS="${CURL_CONNECT_TIMEOUT_SECONDS:-5}"
readonly -a TARGETS=(
  "resume|https://resume.areasong.top/"
  "account-vault|https://sorryiossearch.areasong.top/health"
  "sub2api|https://cpa.areasong.top/health"
  "areaforge|https://forge.areasong.top/"
  "grafana|https://monitor.areasong.top/"
  "log-gateway|https://log.areasong.top/"
)

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

check_target() {
  local index="$1"
  local target="$2"
  local name url result exit_code http_code error_detail error_file
  name="${target%%|*}"
  url="${target#*|}"
  error_file="$WORK_DIR/$index.error"
  if result="$(curl \
    --fail \
    --silent \
    --show-error \
    --location \
    --proto '=https' \
    --tlsv1.2 \
    --retry 1 \
    --retry-delay 2 \
    --retry-all-errors \
    --connect-timeout "$CURL_CONNECT_TIMEOUT_SECONDS" \
    --max-time "$CURL_TIMEOUT_SECONDS" \
    --output /dev/null \
    --write-out '%{http_code}\t%{time_total}' \
    "$url" 2>"$error_file")"; then
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
