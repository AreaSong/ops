#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RULES_DIR="$SCRIPT_DIR/.."
IMAGE="prom/prometheus:v2.53.0@sha256:075b1ba2c4ebb04bc3a6ab86c06ec8d8099f8fda1c96ef6d104d9bb1def1d8bc"

docker run --rm --entrypoint /bin/promtool \
  -v "$RULES_DIR:/rules:ro" \
  "$IMAGE" check rules /rules/backup-integrity.yml

docker run --rm --entrypoint /bin/promtool \
  -v "$RULES_DIR:/rules:ro" \
  "$IMAGE" test rules /rules/tests/backup-integrity.test.yml

docker run --rm --entrypoint /bin/promtool \
  -v "$RULES_DIR:/rules:ro" \
  "$IMAGE" test rules /rules/tests/backup-rpo.test.yml

echo "backup integrity rules: PASS"
