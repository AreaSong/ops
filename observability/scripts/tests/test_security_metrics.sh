#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
METRIC_SCRIPT="$SCRIPT_DIR/../write-security-metrics.sh"
WORK_DIR="$(mktemp -d)"
LOKI_SERVER_PID=""

cleanup() {
  if [ -n "$LOKI_SERVER_PID" ]; then
    kill "$LOKI_SERVER_PID" 2>/dev/null || true
    wait "$LOKI_SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

mkdir -p "$WORK_DIR/bin"

cat > "$WORK_DIR/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
printf 'active\n'
EOF

cat > "$WORK_DIR/bin/auditctl" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = "-s" ]; then
  cat <<'STATUS'
enabled 1
failure 1
pid 123
rate_limit 0
backlog_limit 8192
lost 0
backlog 0
STATUS
elif [ "${1:-}" = "-l" ]; then
  for key in identity sudoers sshd systemd auditconfig opsconfig rootcmd; do
    printf -- '-w /managed/path -p wa -k %s\n' "$key"
  done
elif [ "${1:-}" = "-m" ] && [ "${2:-}" = "ops-audit-pipeline-probe" ]; then
  exit 0
else
  exit 2
fi
EOF

cat > "$WORK_DIR/bin/fail2ban-client" <<'EOF'
#!/usr/bin/env bash
cat <<'STATUS'
Currently failed: 0
Total failed: 10
Currently banned: 0
Total banned: 2
STATUS
EOF

cat > "$WORK_DIR/bin/ufw" <<'EOF'
#!/usr/bin/env bash
printf 'Status: active\n'
EOF

chmod 0755 "$WORK_DIR/bin/systemctl" "$WORK_DIR/bin/auditctl" \
  "$WORK_DIR/bin/fail2ban-client" "$WORK_DIR/bin/ufw"

cat > "$WORK_DIR/loki_server.py" <<'PY'
from __future__ import annotations

import json
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        query = parse_qs(urlsplit(self.path).query).get("query", [""])[0]
        mode = Path(sys.argv[2]).read_text(encoding="utf-8").strip()
        if 'job="auditd"' not in query or 'host="LosAngeles"' not in query or "ops-audit-pipeline-probe" not in query:
            payload = {"status": "error", "error": "unexpected query"}
        else:
            timestamp = time.time_ns()
            line = "type=USER msg='ops-audit-pipeline-probe'"
            result_type = "streams"
            if mode == "wrong-line":
                line = "type=USER msg='unrelated-event'"
            elif mode == "future":
                timestamp += 10 * 60 * 1_000_000_000
            elif mode == "invalid-schema":
                result_type = "vector"
            payload = {
                "status": "success",
                "data": {
                    "resultType": result_type,
                    "result": [
                        {
                            "stream": {"job": "auditd", "host": "LosAngeles"},
                            "values": [[str(timestamp), line]],
                        }
                    ],
                },
            }
        body = json.dumps(
            payload
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args) -> None:
        return


server = HTTPServer(("127.0.0.1", 0), Handler)
Path(sys.argv[1]).write_text(str(server.server_port), encoding="utf-8")
server.serve_forever()
PY

printf 'healthy\n' > "$WORK_DIR/loki.mode"
python3 "$WORK_DIR/loki_server.py" "$WORK_DIR/loki.port" "$WORK_DIR/loki.mode" &
LOKI_SERVER_PID="$!"
for _ in $(seq 1 50); do
  [ -s "$WORK_DIR/loki.port" ] && break
  sleep 0.1
done
test -s "$WORK_DIR/loki.port"
LOKI_PORT="$(cat "$WORK_DIR/loki.port")"

run_metrics() {
  local mode="$1"
  local output="$2"
  printf '%s\n' "$mode" > "$WORK_DIR/loki.mode"
  SECURITY_METRIC_OUT="$output" \
    LOKI_QUERY_URL="http://127.0.0.1:$LOKI_PORT/loki/api/v1/query_range" \
    PATH="$WORK_DIR/bin:$PATH" \
    "$METRIC_SCRIPT"
}

run_metrics healthy "$WORK_DIR/security.prom"

grep -Fq 'auditd_check_success 1' "$WORK_DIR/security.prom"
grep -Fq 'auditd_service_active 1' "$WORK_DIR/security.prom"
grep -Fq 'auditd_kernel_enabled 1' "$WORK_DIR/security.prom"
grep -Fq 'auditd_required_rule_keys_present 7' "$WORK_DIR/security.prom"
grep -Fq 'auditd_required_rule_keys_expected 7' "$WORK_DIR/security.prom"
grep -Fq 'auditd_lost_events 0' "$WORK_DIR/security.prom"
grep -Fq 'auditd_backlog_limit 8192' "$WORK_DIR/security.prom"
grep -Fq 'audit_log_probe_emit_success 1' "$WORK_DIR/security.prom"
grep -Fq 'audit_log_loki_query_success 1' "$WORK_DIR/security.prom"
grep -Fq 'audit_log_pipeline_check_success 1' "$WORK_DIR/security.prom"
grep -Eq 'audit_log_pipeline_last_event_timestamp_seconds [1-9][0-9]+' "$WORK_DIR/security.prom"
grep -Fq 'ufw_enabled 1' "$WORK_DIR/security.prom"
grep -Fq 'fail2ban_check_success{jail="sshd"} 1' "$WORK_DIR/security.prom"

run_metrics wrong-line "$WORK_DIR/security-wrong-line.prom"
grep -Fq 'audit_log_loki_query_success 1' "$WORK_DIR/security-wrong-line.prom"
grep -Fq 'audit_log_pipeline_check_success 0' "$WORK_DIR/security-wrong-line.prom"
grep -Fq 'audit_log_pipeline_last_event_timestamp_seconds 0' "$WORK_DIR/security-wrong-line.prom"

run_metrics invalid-schema "$WORK_DIR/security-invalid-schema.prom"
grep -Fq 'audit_log_loki_query_success 0' "$WORK_DIR/security-invalid-schema.prom"
grep -Fq 'audit_log_pipeline_check_success 0' "$WORK_DIR/security-invalid-schema.prom"

run_metrics future "$WORK_DIR/security-future.prom"
grep -Fq 'audit_log_loki_query_success 1' "$WORK_DIR/security-future.prom"
grep -Fq 'audit_log_pipeline_check_success 0' "$WORK_DIR/security-future.prom"

echo "security metrics: PASS"
