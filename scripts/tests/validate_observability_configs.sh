#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROMETHEUS_IMAGE="prom/prometheus:v2.53.0@sha256:075b1ba2c4ebb04bc3a6ab86c06ec8d8099f8fda1c96ef6d104d9bb1def1d8bc"
LOKI_IMAGE="grafana/loki:3.1.0@sha256:d947e68a84d9e44915dfa08c3bec27e2124efd5ba6c83443eb53578101ec69e3"
PROMTAIL_IMAGE="grafana/promtail:3.1.0@sha256:b3db8e7b1cba0e8c45ce2ae72ebddfd88ebdcae86383f1680edf0074e9010ff6"
BLACKBOX_IMAGE="prom/blackbox-exporter:v0.25.0@sha256:b04a9fef4fa086a02fc7fcd8dcdbc4b7b35cc30cdee860fdc6a19dd8b208d63e"
ALERTMANAGER_IMAGE="prom/alertmanager:v0.27.0@sha256:e13b6ed5cb929eeaee733479dce55e10eb3bc2e9c4586c705a4e8da41e5eacf5"

docker run --rm --entrypoint /bin/promtool \
  -v "$REPO_ROOT/observability/prometheus:/etc/prometheus:ro" \
  "$PROMETHEUS_IMAGE" check config /etc/prometheus/prometheus.yml
docker run --rm \
  -v "$REPO_ROOT/observability/loki/loki.yml:/etc/loki/loki.yml:ro" \
  "$LOKI_IMAGE" -verify-config -config.file=/etc/loki/loki.yml
docker run --rm \
  -v "$REPO_ROOT/observability/promtail/promtail-config.yml:/etc/promtail/config.yml:ro" \
  "$PROMTAIL_IMAGE" -check-syntax -config.file=/etc/promtail/config.yml
docker run --rm \
  -v "$REPO_ROOT/observability/blackbox/blackbox.yml:/etc/blackbox_exporter/config.yml:ro" \
  "$BLACKBOX_IMAGE" --config.file=/etc/blackbox_exporter/config.yml --config.check
docker run --rm --entrypoint /bin/amtool \
  -v "$REPO_ROOT/observability/alertmanager:/etc/alertmanager:ro" \
  "$ALERTMANAGER_IMAGE" check-config /etc/alertmanager/alertmanager.yml
