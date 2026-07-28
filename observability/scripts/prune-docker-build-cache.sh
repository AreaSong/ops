#!/usr/bin/env bash
set -Eeuo pipefail

readonly DOCKER_BIN=/usr/bin/docker
readonly NUMFMT_BIN=/usr/bin/numfmt
readonly RETENTION=336h
readonly METRIC_OUT=/var/lib/node_exporter/textfile_collector/docker-build-cache-prune.prom

if [[ ! -x "$DOCKER_BIN" ]]; then
  echo "docker CLI is unavailable: $DOCKER_BIN" >&2
  exit 1
fi

if [[ ! -x "$NUMFMT_BIN" ]]; then
  echo "numfmt is unavailable: $NUMFMT_BIN" >&2
  exit 1
fi

started_at="$(date +%s)"
set +e
prune_output="$("$DOCKER_BIN" builder prune --all --force --filter "until=${RETENTION}" 2>&1)"
prune_status=$?
set -e
printf '%s\n' "$prune_output"
[[ "$prune_status" -eq 0 ]] || exit "$prune_status"

reclaimed_size="$(awk -F: '/^Total:/ {gsub(/[[:space:]]/, "", $2); print $2}' <<<"$prune_output" | tail -n 1)"
[[ -n "$reclaimed_size" ]] || { echo "Docker prune output omitted the reclaimed total" >&2; exit 1; }
reclaimed_bytes="$("$NUMFMT_BIN" --from=si --suffix=B "$reclaimed_size")"
reclaimed_bytes="${reclaimed_bytes%B}"
completed_at="$(date +%s)"
metric_tmp="${METRIC_OUT}.tmp.$$"
trap 'rm -f "$metric_tmp"' EXIT
install -d -m 0755 "$(dirname "$METRIC_OUT")"
{
  echo '# HELP docker_build_cache_prune_last_success_timestamp Unix timestamp of the latest successful BuildKit cache cleanup.'
  echo '# TYPE docker_build_cache_prune_last_success_timestamp gauge'
  printf 'docker_build_cache_prune_last_success_timestamp %s\n' "$completed_at"
  echo '# HELP docker_build_cache_prune_duration_seconds Duration of the latest successful BuildKit cache cleanup.'
  echo '# TYPE docker_build_cache_prune_duration_seconds gauge'
  printf 'docker_build_cache_prune_duration_seconds %s\n' "$((completed_at - started_at))"
  echo '# HELP docker_build_cache_prune_reclaimed_bytes Bytes reclaimed by the latest successful BuildKit cache cleanup.'
  echo '# TYPE docker_build_cache_prune_reclaimed_bytes gauge'
  printf 'docker_build_cache_prune_reclaimed_bytes %s\n' "$reclaimed_bytes"
} >"$metric_tmp"
chmod 0644 "$metric_tmp"
mv -f "$metric_tmp" "$METRIC_OUT"
trap - EXIT
