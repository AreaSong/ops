#!/usr/bin/env bash
set -euo pipefail

OUT="/var/lib/node_exporter/textfile_collector/cloudflare-origin-cert.prom"
TMP="$(mktemp "${OUT}.XXXXXX")"

cleanup() {
  rm -f "$TMP"
}
trap cleanup EXIT

mkdir -p "$(dirname "$OUT")"

python3 - <<'PY' > "$TMP"
from __future__ import annotations

import datetime as dt
import subprocess
import time
from pathlib import Path

CERT_PATH = Path("/etc/ssl/cf/top/origin.pem")
CERT_LABELS = {
    "cert": "areasong-top-origin",
    "domains": "*.areasong.top,areasong.top",
    "path": str(CERT_PATH),
}


def label_text(labels: dict[str, str]) -> str:
    rendered = []
    for key, value in sorted(labels.items()):
        safe = value.replace("\\", "\\\\").replace('"', '\\"')
        rendered.append(f'{key}="{safe}"')
    return "{" + ",".join(rendered) + "}"


def emit_help(name: str, help_text: str) -> None:
    print(f"# HELP {name} {help_text}")
    print(f"# TYPE {name} gauge")


def emit(name: str, value: int | float, labels: dict[str, str] | None = None) -> None:
    print(f"{name}{label_text(labels or {})} {value}")


def openssl(*args: str) -> str:
    return subprocess.check_output(
        ["openssl", "x509", "-in", str(CERT_PATH), *args],
        text=True,
        stderr=subprocess.STDOUT,
        timeout=10,
    ).strip()


def parse_openssl_time(raw: str) -> float:
    # Example: notAfter=Jun 27 23:59:59 2041 GMT
    value = raw.split("=", 1)[1].strip()
    parsed = dt.datetime.strptime(value, "%b %d %H:%M:%S %Y %Z")
    return parsed.replace(tzinfo=dt.timezone.utc).timestamp()


now = time.time()
check_success = 0
not_before = 0.0
not_after = 0.0
days_until_expiry = -1.0

try:
    if not CERT_PATH.exists():
        raise FileNotFoundError(str(CERT_PATH))
    not_before = parse_openssl_time(openssl("-noout", "-startdate"))
    not_after = parse_openssl_time(openssl("-noout", "-enddate"))
    days_until_expiry = (not_after - now) / 86400
    check_success = 1
except Exception:
    check_success = 0

emit_help("cloudflare_origin_cert_metrics_last_run_timestamp", "Unix timestamp of the latest Cloudflare Origin certificate metrics run.")
emit("cloudflare_origin_cert_metrics_last_run_timestamp", int(now))

emit_help("cloudflare_origin_cert_check_success", "Whether the Cloudflare Origin certificate file was readable and parseable.")
emit("cloudflare_origin_cert_check_success", check_success, CERT_LABELS)

emit_help("cloudflare_origin_cert_not_before_timestamp", "Cloudflare Origin certificate notBefore timestamp.")
emit("cloudflare_origin_cert_not_before_timestamp", int(not_before), CERT_LABELS)

emit_help("cloudflare_origin_cert_not_after_timestamp", "Cloudflare Origin certificate notAfter timestamp.")
emit("cloudflare_origin_cert_not_after_timestamp", int(not_after), CERT_LABELS)

emit_help("cloudflare_origin_cert_days_until_expiry", "Cloudflare Origin certificate days until expiry.")
emit("cloudflare_origin_cert_days_until_expiry", f"{days_until_expiry:.6f}", CERT_LABELS)
PY

chmod 0644 "$TMP"
mv "$TMP" "$OUT"
trap - EXIT
