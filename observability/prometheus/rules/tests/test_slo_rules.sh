#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RULE_FILE="$SCRIPT_DIR/../slo.yml"
IMAGE="prom/prometheus:v2.53.0@sha256:075b1ba2c4ebb04bc3a6ab86c06ec8d8099f8fda1c96ef6d104d9bb1def1d8bc"
WORK_DIR="$(mktemp -d)"

trap 'rm -rf "$WORK_DIR"' EXIT
chmod 0755 "$WORK_DIR"

grep -Fq 'service:synthetic_journey_success:ratio[30d]' "$RULE_FILE"
grep -Fq '/ 172800' "$RULE_FILE"

docker run --rm --entrypoint /bin/promtool \
  -v "$SCRIPT_DIR/..:/rules:ro" \
  "$IMAGE" test rules /rules/tests/slo.test.yml

sed \
  -e 's/\[30d\]/[2h]/g' \
  "$RULE_FILE" >"$WORK_DIR/slo.yml"
cp "$SCRIPT_DIR/slo-scaled.test.yml" "$WORK_DIR/slo.test.yml"

docker run --rm --entrypoint /bin/promtool --workdir /work \
  -v "$WORK_DIR:/work:ro" \
  "$IMAGE" test rules slo.test.yml

echo "scaled SLO recording rules: PASS"
