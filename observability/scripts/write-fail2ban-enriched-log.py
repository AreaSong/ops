#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import hashlib
import ipaddress
import json
import os
import re
import subprocess
import tempfile
import time
from pathlib import Path

RAW_LOGS = [Path("/var/log/fail2ban.log.1"), Path("/var/log/fail2ban.log")]
OUT = Path("/var/log/security/fail2ban-enriched.log")
CACHE_PATH = Path("/var/lib/observability/fail2ban-ip-cache.json")
STATE_PATH = Path("/var/lib/observability/fail2ban-enriched-state.json")
MAX_SEEN_IDS = 20000
CACHE_TTL_SECONDS = 30 * 24 * 60 * 60

EVENT_RE = re.compile(
    r"^(?P<event_ts>\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2},\d{3})\s+"
    r"fail2ban\.actions\s+\[\d+\]:\s+NOTICE\s+\[(?P<jail>[^\]]+)\]\s+"
    r"(?P<action>Ban|Unban)\s+(?P<ip>\S+)\s*$"
)


def load_json(path: Path, default):
    try:
        with path.open("r", encoding="utf-8") as handle:
            return json.load(handle)
    except FileNotFoundError:
        return default
    except json.JSONDecodeError:
        return default


def atomic_write_json(path: Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(data, handle, ensure_ascii=False, indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(tmp_name, path)
    finally:
        try:
            os.unlink(tmp_name)
        except FileNotFoundError:
            pass


def run_dig(name: str) -> str:
    try:
        completed = subprocess.run(
            ["dig", "+short", "TXT", name],
            check=False,
            capture_output=True,
            text=True,
            timeout=5,
        )
    except Exception:
        return ""
    if completed.returncode != 0:
        return ""
    for line in completed.stdout.splitlines():
        line = line.strip().strip('"')
        if line:
            return line
    return ""


def cymru_origin_query(ip: str) -> dict[str, str]:
    parsed = ipaddress.ip_address(ip)
    if parsed.version != 4 or not parsed.is_global:
        return {
            "asn": "",
            "bgp_prefix": "",
            "country": "",
            "registry": "",
            "allocated": "",
            "as_name": "",
            "lookup_source": "local",
        }

    reversed_ip = ".".join(reversed(ip.split(".")))
    origin = run_dig(f"{reversed_ip}.origin.asn.cymru.com")
    parts = [part.strip() for part in origin.split("|")]
    if len(parts) < 5:
        return {
            "asn": "",
            "bgp_prefix": "",
            "country": "",
            "registry": "",
            "allocated": "",
            "as_name": "",
            "lookup_source": "team-cymru",
        }

    asn, bgp_prefix, country, registry, allocated = parts[:5]
    as_name = ""
    if asn:
        asn_info = run_dig(f"AS{asn}.asn.cymru.com")
        asn_parts = [part.strip() for part in asn_info.split("|")]
        if len(asn_parts) >= 5:
            as_name = asn_parts[4]

    return {
        "asn": asn,
        "bgp_prefix": bgp_prefix,
        "country": country,
        "registry": registry,
        "allocated": allocated,
        "as_name": as_name,
        "lookup_source": "team-cymru",
    }


def enrich_ip(ip: str, cache: dict) -> dict[str, str]:
    now = int(time.time())
    cached = cache.get(ip)
    if cached and now - int(cached.get("cached_at", 0)) < CACHE_TTL_SECONDS:
        return dict(cached.get("data", {}))

    try:
        data = cymru_origin_query(ip)
    except Exception:
        data = {
            "asn": "",
            "bgp_prefix": "",
            "country": "",
            "registry": "",
            "allocated": "",
            "as_name": "",
            "lookup_source": "error",
        }
    cache[ip] = {"cached_at": now, "data": data}
    return dict(data)


def iter_events():
    for path in RAW_LOGS:
        try:
            with path.open("r", encoding="utf-8", errors="replace") as handle:
                for line in handle:
                    line = line.rstrip("\n")
                    match = EVENT_RE.match(line)
                    if not match:
                        continue
                    event = match.groupdict()
                    event["raw_message"] = line
                    event["event_id"] = hashlib.sha256(line.encode("utf-8")).hexdigest()
                    yield event
        except FileNotFoundError:
            continue
        except PermissionError:
            continue


def main() -> int:
    cache = load_json(CACHE_PATH, {})
    state = load_json(STATE_PATH, {"seen_ids": []})
    seen = list(state.get("seen_ids", []))
    seen_set = set(seen)
    new_events = []

    for event in iter_events():
        if event["event_id"] in seen_set:
            continue
        intel = enrich_ip(event["ip"], cache)
        event.update(intel)
        event["observed_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
        new_events.append(event)
        seen.append(event["event_id"])
        seen_set.add(event["event_id"])

    if new_events:
        OUT.parent.mkdir(parents=True, exist_ok=True)
        with OUT.open("a", encoding="utf-8") as handle:
            for event in new_events:
                handle.write(json.dumps(event, ensure_ascii=False, sort_keys=True))
                handle.write("\n")
        os.chmod(OUT, 0o640)

    state["seen_ids"] = seen[-MAX_SEEN_IDS:]
    atomic_write_json(CACHE_PATH, cache)
    atomic_write_json(STATE_PATH, state)
    os.chmod(CACHE_PATH, 0o640)
    os.chmod(STATE_PATH, 0o640)
    print(f"fail2ban_enriched_events_appended={len(new_events)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
