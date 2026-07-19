#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK_SCRIPT="$SCRIPT_DIR/../external-uptime-check.sh"
WORK_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

mkdir -p "$WORK_DIR/bin"
cat > "$WORK_DIR/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -u

url=""
client_id_header=0
client_secret_header=0
for argument in "$@"; do
  url="$argument"
  [[ "$argument" == "CF-Access-Client-Id: test-client-id" ]] && client_id_header=1
  [[ "$argument" == "CF-Access-Client-Secret: test-client-secret" ]] && client_secret_header=1
done

if [[ "$url" == *monitor.areasong.top* && "${EXPECT_ACCESS_HEADERS:-false}" == true ]]; then
  if [[ "$client_id_header" -ne 1 || "$client_secret_header" -ne 1 ]]; then
    printf 'missing Cloudflare Access headers\n' >&2
    exit 2
  fi
fi

case "${FAKE_CURL_MODE:-success}" in
  success)
    printf '200\t0.010000'
    ;;
  one-fail)
    if [[ "$url" == *cpa.areasong.top* ]]; then
      printf '503\t0.020000'
      printf 'server returned 503\n' >&2
      exit 22
    fi
    printf '200\t0.010000'
    ;;
  all-timeout)
    sleep 1
    exit 28
    ;;
  *)
    exit 2
    ;;
esac
FAKE_CURL
chmod 0755 "$WORK_DIR/bin/curl"

run_check() {
  local mode="$1"
  local output="$2"
  FAKE_CURL_MODE="$mode" PATH="$WORK_DIR/bin:$PATH" "$CHECK_SCRIPT" > "$output" 2>&1
}

run_check success "$WORK_DIR/success.txt"
[ "$(grep -c '^OK' "$WORK_DIR/success.txt")" -eq 6 ]

CF_ACCESS_REQUIRED=true \
CF_ACCESS_CLIENT_ID=test-client-id \
CF_ACCESS_CLIENT_SECRET=test-client-secret \
EXPECT_ACCESS_HEADERS=true \
run_check success "$WORK_DIR/access-success.txt"
[ "$(grep -c '^OK' "$WORK_DIR/access-success.txt")" -eq 6 ]

set +e
CF_ACCESS_REQUIRED=true run_check success "$WORK_DIR/access-missing.txt"
access_missing_status=$?
set -e
[ "$access_missing_status" -ne 0 ]
grep -Fq $'FAIL\tgrafana\thttp=000\tcurl_exit=2\terror=Cloudflare Access service token is not configured' \
  "$WORK_DIR/access-missing.txt"

if run_check one-fail "$WORK_DIR/one-fail.txt"; then
  echo "one-fail case unexpectedly succeeded" >&2
  exit 1
fi
[ "$(grep -c '^FAIL' "$WORK_DIR/one-fail.txt")" -eq 1 ]
grep -Fq $'FAIL\tsub2api\thttp=503\tcurl_exit=22\terror=server returned 503 ' "$WORK_DIR/one-fail.txt"

started_at="$(date +%s)"
if run_check all-timeout "$WORK_DIR/all-timeout.txt"; then
  echo "all-timeout case unexpectedly succeeded" >&2
  exit 1
fi
elapsed="$(( $(date +%s) - started_at ))"
[ "$(grep -c '^FAIL' "$WORK_DIR/all-timeout.txt")" -eq 6 ]
if [ "$elapsed" -ge 4 ]; then
  echo "parallel timeout test took too long: ${elapsed}s" >&2
  exit 1
fi

echo "external uptime checks: PASS"
