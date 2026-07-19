#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
PROMETHEUS_IMAGE="prom/prometheus:v2.53.0@sha256:075b1ba2c4ebb04bc3a6ab86c06ec8d8099f8fda1c96ef6d104d9bb1def1d8bc"

docker run --rm --entrypoint /bin/promtool \
  -v "$REPO_ROOT/observability/prometheus/rules:/rules:ro" \
  "$PROMETHEUS_IMAGE" \
  test rules /rules/tests/runtime-governance.test.yml

echo "runtime governance rules: PASS"
