#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import re
import sys
from pathlib import Path
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from daily_ops_audit_collectors import (
    collect_nginx,
    collect_prometheus,
    collect_runtime,
    collect_security,
)
from daily_ops_audit_common import (
    UTC,
    AuditData,
    AuditWindow,
    DeliveryResult,
    ReportArtifact,
    atomic_write,
    system_timezone,
)
from daily_ops_audit_reporting import (
    build_findings,
    build_metrics,
    build_report,
    result_severity,
    send_report,
)

REPORT_NAME_RE = re.compile(r"^daily-ops-audit-(?P<date>\d{4}-\d{2}-\d{2})\.md$")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Generate the LosAngeles daily operations audit.")
    parser.add_argument("--date", help="UTC report date in YYYY-MM-DD; defaults to yesterday")
    parser.add_argument("--no-email", action="store_true")
    parser.add_argument("--prometheus-url", default="http://127.0.0.1:9090")
    parser.add_argument("--alertmanager-url", default="http://127.0.0.1:9093")
    parser.add_argument("--report-dir", default="/var/log/observability")
    parser.add_argument(
        "--metric-out",
        default="/var/lib/node_exporter/textfile_collector/daily-ops-audit.prom",
    )
    parser.add_argument("--retention-days", type=int, default=180)
    parser.add_argument("--system-timezone", help="IANA timezone for traditional syslog timestamps")
    return parser.parse_args()


def resolve_report_day(raw_date: str | None) -> dt.date:
    today = dt.datetime.now(UTC).date()
    try:
        report_day = dt.date.fromisoformat(raw_date) if raw_date else today - dt.timedelta(days=1)
    except ValueError as exc:
        raise ValueError("--date must use YYYY-MM-DD") from exc
    if report_day >= today:
        raise ValueError("the daily audit requires a completed UTC day")
    return report_day


def resolve_timezone(name: str | None) -> dt.tzinfo:
    if not name:
        return system_timezone()
    try:
        return ZoneInfo(name)
    except ZoneInfoNotFoundError as exc:
        raise ValueError(f"unknown system timezone: {name}") from exc


def collect_data(
    window: AuditWindow,
    prometheus_url: str,
    alertmanager_url: str,
    local_tz: dt.tzinfo,
) -> AuditData:
    failures: list[str] = []
    start = window.start.timestamp()
    end = window.end.timestamp()
    services, parse_errors, unmapped = collect_nginx(start, end, failures)
    security = collect_security(start, end, local_tz, failures)
    prometheus = collect_prometheus(prometheus_url, end, failures)
    runtime = collect_runtime(alertmanager_url, failures)
    timezone_name = getattr(local_tz, "key", None) or str(local_tz)
    return AuditData(
        window=window,
        services=services,
        security=security,
        prometheus=prometheus,
        runtime=runtime,
        system_timezone=timezone_name,
        nginx_parse_errors=parse_errors,
        nginx_unmapped=unmapped,
        failures=failures,
    )


def prune_reports(report_dir: Path, report_day: dt.date, retention_days: int) -> None:
    if retention_days < 1:
        raise ValueError("--retention-days must be positive")
    cutoff = report_day - dt.timedelta(days=retention_days)
    for report_path in report_dir.glob("daily-ops-audit-*.md"):
        match = REPORT_NAME_RE.match(report_path.name)
        if not match:
            continue
        try:
            file_day = dt.date.fromisoformat(match.group("date"))
        except ValueError:
            continue
        if file_day < cutoff:
            report_path.unlink(missing_ok=True)


def deliver_report(
    no_email: bool,
    alertmanager_url: str,
    data: AuditData,
    artifact: ReportArtifact,
) -> DeliveryResult:
    if no_email:
        return DeliveryResult()
    delivery = DeliveryResult(attempted=1)
    try:
        send_report(
            alertmanager_url,
            data.window.report_day.isoformat(),
            artifact.severity,
            artifact.content,
            artifact.path,
        )
        delivery.accepted = 1
    except Exception as exc:
        delivery.error = str(exc)
    return delivery


def run(args: argparse.Namespace) -> int:
    window = AuditWindow.for_day(resolve_report_day(args.date))
    local_tz = resolve_timezone(args.system_timezone)
    data = collect_data(window, args.prometheus_url, args.alertmanager_url, local_tz)
    findings = build_findings(data)
    severity = result_severity(findings)
    report = build_report(data, findings)
    report_path = Path(args.report_dir) / f"daily-ops-audit-{window.report_day.isoformat()}.md"
    atomic_write(report_path, report, 0o640)
    prune_reports(report_path.parent, window.report_day, args.retention_days)
    artifact = ReportArtifact(severity=severity, content=report, path=report_path)
    delivery = deliver_report(
        args.no_email,
        args.alertmanager_url,
        data,
        artifact,
    )
    metrics = build_metrics(data, findings, delivery, dt.datetime.now(UTC))
    atomic_write(Path(args.metric_out), metrics, 0o644)
    critical = sum(item.severity == "critical" for item in findings)
    warning = sum(item.severity == "warning" for item in findings)
    print(f"report={report_path}")
    print(f"severity={severity} critical={critical} warning={warning}")
    print(f"delivery_attempted={delivery.attempted} delivery_accepted={delivery.accepted}")
    if delivery.error:
        print(f"email delivery failed: {delivery.error}", file=sys.stderr)
        return 1
    return 0


def main() -> int:
    try:
        return run(parse_args())
    except (OSError, ValueError) as exc:
        print(f"daily audit failed: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
