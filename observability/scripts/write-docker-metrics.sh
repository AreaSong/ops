#!/usr/bin/env bash
set -euo pipefail

OUT="/var/lib/node_exporter/textfile_collector/docker.prom"
TMP="${OUT}.tmp"

mkdir -p "$(dirname "$OUT")"

containers=(
  resume-jadeai-app-1
  sub2api
  sub2api-redis
  sub2api-postgres
  account-vault-web-1
  account-vault-postgres-1
  prometheus
  grafana
  alertmanager
  loki
  promtail
  node-exporter
  blackbox-exporter
)

{
  echo '# HELP docker_container_running Whether expected Docker container is running.'
  echo '# TYPE docker_container_running gauge'
  for c in "${containers[@]}"; do
    if docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null | grep -qx true; then
      v=1
    else
      v=0
    fi
    printf 'docker_container_running{name="%s"} %s\n' "$c" "$v"
  done
} > "$TMP"

mv "$TMP" "$OUT"
chmod 0644 "$OUT"
