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
output_file=""
header_file=""
client_id_header=0
client_secret_header=0
cache_control_header=0
pragma_header=0
previous_argument=""
for argument in "$@"; do
  if [[ "$previous_argument" == "--output" ]]; then
    output_file="$argument"
  fi
  if [[ "$previous_argument" == "--dump-header" ]]; then
    header_file="$argument"
  fi
  [[ "$argument" == https://* ]] && url="$argument"
  [[ "$argument" == "CF-Access-Client-Id: test-client-id" ]] && client_id_header=1
  [[ "$argument" == "CF-Access-Client-Secret: test-client-secret" ]] && client_secret_header=1
  [[ "$argument" == "Cache-Control: no-cache, no-store" ]] && cache_control_header=1
  [[ "$argument" == "Pragma: no-cache" ]] && pragma_header=1
  previous_argument="$argument"
done

if [[ -z "$url" || -z "$output_file" || -z "$header_file" ]]; then
  printf 'missing URL, --output path, or --dump-header path\n' >&2
  exit 2
fi
if [[ "${EXPECT_NO_CACHE_HEADERS:-false}" == true ]] && \
  { [[ "$cache_control_header" -ne 1 ]] || [[ "$pragma_header" -ne 1 ]]; }; then
  printf 'missing no-cache request headers\n' >&2
  exit 2
fi

if [[ "${FAKE_CURL_MODE:-success}" == cached ]]; then
  printf 'HTTP/2 200\ncf-cache-status: HIT\n\n' >"$header_file"
else
  printf 'HTTP/2 200\ncf-cache-status: DYNAMIC\n\n' >"$header_file"
fi

if [[ "$url" == *monitor.areasong.top* && "${EXPECT_ACCESS_HEADERS:-false}" == true ]]; then
  if [[ "$client_id_header" -ne 1 || "$client_secret_header" -ne 1 ]]; then
    printf 'missing Cloudflare Access headers\n' >&2
    exit 2
  fi
fi

case "${FAKE_CURL_MODE:-success}" in
  success)
    case "$url" in
      *resume.areasong.top*) printf '<title>JadeAI - AI Resume Builder</title>' >"$output_file" ;;
      *sorryiossearch.areasong.top*) printf '{"ok":true}' >"$output_file" ;;
      *cpa.areasong.top*) printf '{"status":"ok"}' >"$output_file" ;;
      *forge.areasong.top*) printf '{"ok":true,"service":"AreaForge","version":"test"}' >"$output_file" ;;
      *monitor.areasong.top*) printf '{"database":"ok","version":"test"}' >"$output_file" ;;
      *log.areasong.top*) printf '<title>Welcome to nginx</title>' >"$output_file" ;;
      *) exit 2 ;;
    esac
    printf '200\t0.010000'
    ;;
  content-mismatch)
    printf '{"status":"degraded"}' >"$output_file"
    printf '200\t0.010000'
    ;;
  cached)
    case "$url" in
      *resume.areasong.top*) printf '<title>JadeAI - AI Resume Builder</title>' >"$output_file" ;;
      *sorryiossearch.areasong.top*) printf '{"ok":true}' >"$output_file" ;;
      *cpa.areasong.top*) printf '{"status":"ok"}' >"$output_file" ;;
      *forge.areasong.top*) printf '{"ok":true,"service":"AreaForge","version":"test"}' >"$output_file" ;;
      *monitor.areasong.top*) printf '{"database":"ok","version":"test"}' >"$output_file" ;;
      *log.areasong.top*) printf '<title>Welcome to nginx</title>' >"$output_file" ;;
      *) exit 2 ;;
    esac
    printf '200\t0.010000'
    ;;
  one-fail)
    if [[ "$url" == *cpa.areasong.top* ]]; then
      printf '503\t0.020000'
      printf 'server returned 503\n' >&2
      exit 22
    fi
    case "$url" in
      *resume.areasong.top*) printf '<title>JadeAI - AI Resume Builder</title>' >"$output_file" ;;
      *sorryiossearch.areasong.top*) printf '{"ok":true}' >"$output_file" ;;
      *forge.areasong.top*) printf '{"ok":true,"service":"AreaForge","version":"test"}' >"$output_file" ;;
      *monitor.areasong.top*) printf '{"database":"ok","version":"test"}' >"$output_file" ;;
      *log.areasong.top*) printf '<title>Welcome to nginx</title>' >"$output_file" ;;
      *) exit 2 ;;
    esac
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

EXPECT_NO_CACHE_HEADERS=true run_check success "$WORK_DIR/no-cache-headers.txt"
[ "$(grep -c '^OK' "$WORK_DIR/no-cache-headers.txt")" -eq 6 ]

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

if run_check content-mismatch "$WORK_DIR/content-mismatch.txt"; then
  echo "content-mismatch case unexpectedly succeeded" >&2
  exit 1
fi
[ "$(grep -c '^FAIL' "$WORK_DIR/content-mismatch.txt")" -eq 6 ]
grep -Fq $'FAIL\tgrafana\thttp=200\tcurl_exit=0\terror=response content did not match json-grafana' \
  "$WORK_DIR/content-mismatch.txt"

if run_check cached "$WORK_DIR/cached.txt"; then
  echo "cached response case unexpectedly succeeded" >&2
  exit 1
fi
[ "$(grep -c '^FAIL' "$WORK_DIR/cached.txt")" -eq 6 ]
grep -Fq $'FAIL\tresume\thttp=200\tcurl_exit=0\terror=response was served from an intermediary cache' \
  "$WORK_DIR/cached.txt"

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
