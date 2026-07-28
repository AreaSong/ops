#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METRIC_SCRIPT="$SCRIPT_DIR/../write-docker-metrics.sh"
WORK_DIR="$(mktemp -d)"

trap 'rm -rf "$WORK_DIR"' EXIT

mkdir -p "$WORK_DIR/bin"
cat >"$WORK_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [ "${FAKE_DOCKER_MODE:-success}" = failure ]; then
  exit 1
fi

case "${1:-}" in
  ps)
    printf 'sub2api\naccount-vault-web-1\n'
    ;;
  inspect)
    if [ "${FAKE_DOCKER_MODE:-success}" = empty-inspect ]; then
      printf '[]\n'
      exit 0
    fi
    cat <<'JSON'
[{"RestartCount":3,"State":{"Running":true,"OOMKilled":false,"StartedAt":"2026-01-02T03:04:05.000000000Z","Health":{"Status":"healthy"}},"HostConfig":{"Memory":536870912,"NanoCpus":1000000000,"PidsLimit":512}}]
JSON
    ;;
  stats)
    if [ "${FAKE_DOCKER_MODE:-success}" = bad-stats ]; then
      printf '%s\n' '{"CPUPerc":"50.00%","MemUsage":"invalid / 512MiB","Name":"sub2api","PIDs":"128"}'
      exit 0
    fi
    printf '%s\n' \
      '{"CPUPerc":"50.00%","MemUsage":"256MiB / 512MiB","Name":"sub2api","PIDs":"128"}' \
      '{"CPUPerc":"50.00%","MemUsage":"256MiB / 512MiB","Name":"account-vault-web-1","PIDs":"128"}'
    ;;
  system)
    printf '%s\n' \
      '{"Active":"2","Reclaimable":"1.5GB (30%)","Size":"5GB","TotalCount":"4","Type":"Images"}' \
      '{"Active":"2","Reclaimable":"0B (0%)","Size":"20MB","TotalCount":"2","Type":"Containers"}' \
      '{"Active":"1","Reclaimable":"0B (0%)","Size":"100MB","TotalCount":"1","Type":"Local Volumes"}' \
      '{"Active":"0","Reclaimable":"7.069GB","Size":"7.069GB","TotalCount":"118","Type":"Build Cache"}'
    ;;
  *)
    exit 2
    ;;
esac
EOF
chmod 0755 "$WORK_DIR/bin/docker"

success_out="$WORK_DIR/success.prom"
DOCKER_METRIC_OUT="$success_out" DOCKER_EXPECTED_CONTAINERS="sub2api,missing-service,account-vault-web-1" \
  PATH="$WORK_DIR/bin:$PATH" "$METRIC_SCRIPT"
grep -Fq 'docker_metrics_check_success 1' "$success_out"
grep -Fq 'docker_container_running{name="sub2api",service="sub2api"} 1' "$success_out"
grep -Fq 'docker_container_running{name="missing-service",service="missing-service"} 0' "$success_out"
grep -Fq 'docker_container_running{name="account-vault-web-1",service="account-vault"} 1' "$success_out"
grep -Fq 'docker_container_restart_count{name="sub2api",service="sub2api"} 3' "$success_out"
grep -Fq 'docker_container_started_at_timestamp_seconds{name="sub2api",service="sub2api"} 1767323045' "$success_out"
grep -Fq 'docker_container_started_at_timestamp_seconds{name="missing-service",service="missing-service"} 0' "$success_out"
grep -Fq 'docker_container_memory_usage_ratio{name="sub2api",service="sub2api"} 0.5' "$success_out"
grep -Fq 'docker_container_cpu_limit_usage_ratio{name="sub2api",service="sub2api"} 0.5' "$success_out"
grep -Fq 'docker_container_pids_usage_ratio{name="sub2api",service="sub2api"} 0.25' "$success_out"
grep -Fq 'docker_container_health_status{name="sub2api",service="sub2api",status="healthy"} 1' "$success_out"
grep -Fq 'docker_storage_size_bytes{type="build_cache"} 7069000000' "$success_out"
grep -Fq 'docker_storage_reclaimable_bytes{type="images"} 1500000000' "$success_out"
grep -Fq 'docker_storage_total_items{type="build_cache"} 118' "$success_out"

failure_out="$WORK_DIR/failure.prom"
FAKE_DOCKER_MODE=failure DOCKER_METRIC_OUT="$failure_out" \
  DOCKER_EXPECTED_CONTAINERS="sub2api" PATH="$WORK_DIR/bin:$PATH" "$METRIC_SCRIPT"
grep -Fq 'docker_metrics_check_success 0' "$failure_out"
if grep -Fq 'docker_container_running{name="sub2api",service="sub2api"}' "$failure_out"; then exit 1; fi

empty_out="$WORK_DIR/empty-inspect.prom"
FAKE_DOCKER_MODE=empty-inspect DOCKER_METRIC_OUT="$empty_out" \
  DOCKER_EXPECTED_CONTAINERS="sub2api" PATH="$WORK_DIR/bin:$PATH" "$METRIC_SCRIPT"
grep -Fq 'docker_metrics_check_success 0' "$empty_out"
if grep -Fq 'docker_container_running{name="sub2api",service="sub2api"}' "$empty_out"; then exit 1; fi

bad_stats_out="$WORK_DIR/bad-stats.prom"
FAKE_DOCKER_MODE=bad-stats DOCKER_METRIC_OUT="$bad_stats_out" \
  DOCKER_EXPECTED_CONTAINERS="sub2api" PATH="$WORK_DIR/bin:$PATH" "$METRIC_SCRIPT"
grep -Fq 'docker_metrics_check_success 0' "$bad_stats_out"
grep -Fq 'docker_container_running{name="sub2api",service="sub2api"} 1' "$bad_stats_out"
if grep -Fq 'docker_container_memory_usage_ratio{name="sub2api",service="sub2api"}' "$bad_stats_out"; then exit 1; fi

echo "docker metrics: PASS"
