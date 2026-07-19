#!/usr/bin/env python3
from __future__ import annotations

import datetime as dt
import json
from pathlib import Path

from daily_ops_audit_common import (
    SERVICES,
    STATUS_CLASSES,
    AuditData,
    DeliveryResult,
    Finding,
    get_json,
    human_bytes,
    percentile,
)


def _resource_findings(data: AuditData) -> list[Finding]:
    findings: list[Finding] = []
    prom = data.prometheus
    runtime = data.runtime
    if prom.node_exporter_up < 1:
        findings.append(Finding("critical", "Node Exporter 在审计窗口末端不可用"))
    if runtime.systemd_failed > 0:
        findings.append(Finding("critical", f"systemd failed units={runtime.systemd_failed}"))
    if runtime.docker_unhealthy > 0 or prom.expected_containers_down > 0:
        findings.append(
            Finding(
                "critical",
                f"Docker unhealthy={runtime.docker_unhealthy} expected_down={int(prom.expected_containers_down)}",
            )
        )
    if runtime.ufw_active != 1:
        findings.append(Finding("critical", "UFW 当前未处于 active 状态"))
    cpu_count = max(prom.cpu_count, 1)
    if prom.load1_peak > cpu_count * 2:
        findings.append(Finding("warning", f"峰值 load1={prom.load1_peak:.2f}，CPU={cpu_count:.0f}"))
    if prom.memory_used_percent_peak >= 95 or prom.disk_used_percent_peak >= 90:
        findings.append(Finding("critical", "内存或磁盘峰值达到紧急阈值"))
    elif prom.memory_used_percent_peak >= 85 or prom.disk_used_percent_peak >= 80:
        findings.append(Finding("warning", "内存或磁盘峰值达到预警阈值"))
    return findings


def _backup_findings(data: AuditData) -> list[Finding]:
    prom = data.prometheus
    findings: list[Finding] = []
    if prom.backup_missing:
        findings.append(Finding("critical", f"缺失备份指标/产物：{', '.join(prom.backup_missing)}"))
    if prom.backup_stale:
        findings.append(Finding("critical", f"备份超过 30 小时：{', '.join(prom.backup_stale)}"))
    if prom.r2_missing:
        findings.append(Finding("critical", "R2 同步成功指标缺失"))
    elif prom.r2_stale:
        findings.append(Finding("critical", "R2 同步超过 36 小时"))
    if prom.backup_set_missing:
        findings.append(Finding("critical", "完整备份集 manifest 指标缺失"))
    elif prom.backup_set_stale:
        findings.append(Finding("critical", "完整备份集 manifest 超过 30 小时"))
    if prom.r2_verify_missing:
        findings.append(Finding("critical", "R2 完整备份集校验指标缺失"))
    elif prom.r2_verify_stale:
        findings.append(Finding("critical", "R2 完整备份集校验超过 36 小时"))
    return findings


def _traffic_findings(data: AuditData) -> list[Finding]:
    findings: list[Finding] = []
    for service in SERVICES:
        item = data.services[service]
        total = sum(item.statuses.values())
        errors = item.statuses["5xx"]
        if errors == 0:
            continue
        error_ratio = errors / max(total, 1)
        if total < 1000:
            findings.append(
                Finding(
                    "warning",
                    f"{service} 低流量服务出现 HTTP 5xx：{errors}/{total} ({error_ratio:.3%})",
                )
            )
        elif error_ratio > 0.01:
            findings.append(
                Finding(
                    "critical",
                    f"{service} HTTP 5xx 错误率过高：{errors}/{total} ({error_ratio:.3%})",
                )
            )
        elif error_ratio > 0.001:
            findings.append(
                Finding(
                    "warning",
                    f"{service} HTTP 5xx 错误率偏高：{errors}/{total} ({error_ratio:.3%})",
                )
            )
    if data.security["ssh_failed"] > 100:
        findings.append(Finding("warning", f"SSH 登录失败次数偏高：{data.security['ssh_failed']}"))
    if data.security["ufw_blocks"] > 6000:
        findings.append(Finding("warning", f"UFW 拦截事件超出近期基线：{data.security['ufw_blocks']}"))
    if data.nginx_parse_errors > 0:
        findings.append(Finding("warning", f"Nginx 业务日志解析错误：{data.nginx_parse_errors}"))
    mapped_requests = sum(sum(item.statuses.values()) for item in data.services.values())
    total_requests = mapped_requests + data.nginx_unmapped
    unmapped_ratio = data.nginx_unmapped / max(total_requests, 1)
    if data.nginx_unmapped > 1000 and unmapped_ratio >= 0.05:
        findings.append(Finding("warning", f"存在未映射 host 请求：{data.nginx_unmapped}"))
    return findings


def build_findings(data: AuditData) -> list[Finding]:
    findings = _resource_findings(data) + _backup_findings(data) + _traffic_findings(data)
    if data.failures:
        failures = ", ".join(sorted(set(data.failures)))
        findings.append(Finding("warning", f"数据源读取失败：{failures}"))
    return findings


def result_severity(findings: list[Finding]) -> str:
    if any(item.severity == "critical" for item in findings):
        return "critical"
    if any(item.severity == "warning" for item in findings):
        return "warning"
    return "info"


def _web_section(data: AuditData) -> list[str]:
    lines = [
        "## Web 流量",
        "",
        "| 服务 | 请求 | 2xx | 3xx | 4xx | 5xx | 发送流量 | 独立客户端 | 慢请求 | P50 | P95 | P99 |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for service in SERVICES:
        item = data.services[service]
        total = sum(item.statuses.values())
        lines.append(
            f"| {service} | {total} | {item.statuses['2xx']} | {item.statuses['3xx']} | "
            f"{item.statuses['4xx']} | {item.statuses['5xx']} | {human_bytes(item.bytes_sent)} | "
            f"{len(item.client_hashes)} | {item.slow_requests} | {percentile(item.latencies, 0.50):.3f}s | "
            f"{percentile(item.latencies, 0.95):.3f}s | {percentile(item.latencies, 0.99):.3f}s |"
        )
    lines.extend(["", f"解析错误：{data.nginx_parse_errors}；未映射 host 请求：{data.nginx_unmapped}"])
    return lines


def _top_paths_section(data: AuditData) -> list[str]:
    lines = ["", "### Top 规范化路径"]
    for service in SERVICES:
        top = ", ".join(
            f"`{path}` ({count})" for path, count in data.services[service].paths.most_common(5)
        ) or "无"
        lines.append(f"- {service}: {top}")
    return lines


def _http_error_section(data: AuditData) -> list[str]:
    lines = [
        "",
        "### HTTP 5xx 错误率",
        "",
        "| 服务 | 5xx | 请求 | 错误率 | Top 规范化错误路径 |",
        "|---|---:|---:|---:|---|",
    ]
    for service in SERVICES:
        item = data.services[service]
        total = sum(item.statuses.values())
        errors = item.statuses["5xx"]
        error_ratio = errors / max(total, 1)
        paths = ", ".join(
            f"`{path}` ({count})" for path, count in item.error_paths.most_common(5)
        ) or "无"
        lines.append(f"| {service} | {errors} | {total} | {error_ratio:.3%} | {paths} |")
    return lines


def _security_section(data: AuditData) -> list[str]:
    security = data.security
    return [
        "",
        "## 安全事件",
        "",
        f"- SSH accepted={security['ssh_accepted']} failed={security['ssh_failed']} invalid_user={security['ssh_invalid_user']}",
        f"- sudo commands={security['sudo_commands']}",
        f"- Fail2ban ban={security['fail2ban_bans']} unban={security['fail2ban_unbans']}",
        f"- UFW blocked={security['ufw_blocks']} current_active={data.runtime.ufw_active}",
        f"- 传统 syslog 解释时区={data.system_timezone}",
    ]


def _host_section(data: AuditData) -> list[str]:
    prom = data.prometheus
    runtime = data.runtime
    missing = ", ".join(prom.backup_missing) or "none"
    stale = ", ".join(prom.backup_stale) or "none"
    alerts = ", ".join(runtime.active_alerts) or "none"
    return [
        "",
        "## 主机、备份与监控",
        "",
        f"- Network RX={human_bytes(prom.network_receive_bytes)} TX={human_bytes(prom.network_transmit_bytes)}",
        f"- Peak load1={prom.load1_peak:.2f} memory={prom.memory_used_percent_peak:.1f}% disk={prom.disk_used_percent_peak:.1f}%",
        f"- Docker running={runtime.docker_running} unhealthy={runtime.docker_unhealthy} expected_down={int(prom.expected_containers_down)}",
        f"- systemd failed={runtime.systemd_failed} node_exporter_up={prom.node_exporter_up:.0f}",
        f"- Backup missing={missing}; stale={stale}; R2 missing={int(prom.r2_missing)} stale={int(prom.r2_stale)}",
        f"- Backup set missing={int(prom.backup_set_missing)} stale={int(prom.backup_set_stale)}; R2 verify missing={int(prom.r2_verify_missing)} stale={int(prom.r2_verify_stale)}",
        f"- Active alerts={alerts}",
    ]


def _findings_section(data: AuditData, findings: list[Finding]) -> list[str]:
    lines = ["", "## 发现与处理", ""]
    if findings:
        lines.extend(f"- [{item.severity.upper()}] {item.message}" for item in findings)
    else:
        lines.append("- 未发现需要处理的异常。")
    if data.failures:
        lines.extend(["", f"数据源失败：{', '.join(sorted(set(data.failures)))}"])
    return lines


def build_report(data: AuditData, findings: list[Finding]) -> str:
    severity = result_severity(findings)
    critical = sum(item.severity == "critical" for item in findings)
    warning = sum(item.severity == "warning" for item in findings)
    lines = [
        f"# LosAngeles 每日运维审计 {data.window.report_day.isoformat()}",
        "",
        f"- 结论：**{severity.upper()}**",
        f"- 审计窗口：`{data.window.start.isoformat()}` 至 `{data.window.end.isoformat()}`",
        f"- 发现：critical={critical} warning={warning}",
        "",
    ]
    lines += _web_section(data) + _top_paths_section(data) + _http_error_section(data)
    lines += _security_section(data) + _host_section(data) + _findings_section(data, findings)
    lines += [
        "",
        "## 隐私边界",
        "",
        "报告仅保存聚合值和白名单规范化路径，不保存完整 IP、用户名、邮箱、查询参数、Cookie、Authorization 或日志原文。",
        "",
    ]
    return "\n".join(lines)


def _metric_header(name: str, help_text: str) -> list[str]:
    return [f"# HELP {name} {help_text}", f"# TYPE {name} gauge"]


def _http_metrics(data: AuditData) -> list[str]:
    lines = _metric_header("daily_ops_audit_http_requests", "HTTP requests in the audit window.")
    for service in SERVICES:
        item = data.services[service]
        for status_class in STATUS_CLASSES:
            lines.append(
                f'daily_ops_audit_http_requests{{service="{service}",status_class="{status_class}"}} '
                f"{item.statuses[status_class]}"
            )
    lines += _metric_header("daily_ops_audit_http_bytes_sent", "Nginx response bytes in the audit window.")
    for service in SERVICES:
        lines.append(f'daily_ops_audit_http_bytes_sent{{service="{service}"}} {data.services[service].bytes_sent}')
    lines += _metric_header(
        "daily_ops_audit_http_error_ratio",
        "HTTP 5xx requests divided by all HTTP requests in the audit window.",
    )
    for service in SERVICES:
        item = data.services[service]
        total = sum(item.statuses.values())
        error_ratio = item.statuses["5xx"] / max(total, 1)
        lines.append(f'daily_ops_audit_http_error_ratio{{service="{service}"}} {error_ratio:.9f}')
    return lines


def _http_quality_metrics(data: AuditData) -> list[str]:
    lines = _metric_header("daily_ops_audit_http_unique_clients", "Unique clients emitted only as an aggregate.")
    for service in SERVICES:
        lines.append(f'daily_ops_audit_http_unique_clients{{service="{service}"}} {len(data.services[service].client_hashes)}')
    lines += _metric_header("daily_ops_audit_http_slow_requests", "Non-streaming requests at or above two seconds.")
    for service in SERVICES:
        lines.append(f'daily_ops_audit_http_slow_requests{{service="{service}",threshold="2s"}} {data.services[service].slow_requests}')
    lines += _metric_header("daily_ops_audit_http_latency_seconds", "Precomputed request latency percentiles in the audit window.")
    for service in SERVICES:
        for label, fraction in (("p50", 0.50), ("p95", 0.95), ("p99", 0.99)):
            value = percentile(data.services[service].latencies, fraction)
            lines.append(f'daily_ops_audit_http_latency_seconds{{service="{service}",percentile="{label}"}} {value:.6f}')
    return lines


def _host_metrics(data: AuditData) -> list[str]:
    prom = data.prometheus
    lines = _metric_header("daily_ops_audit_network_bytes", "Host network bytes in the audit window.")
    lines.append(f'daily_ops_audit_network_bytes{{direction="receive"}} {prom.network_receive_bytes:.0f}')
    lines.append(f'daily_ops_audit_network_bytes{{direction="transmit"}} {prom.network_transmit_bytes:.0f}')
    for name, help_text, value in (
        ("daily_ops_audit_load1_peak", "Peak one-minute load average.", prom.load1_peak),
        ("daily_ops_audit_memory_used_percent_peak", "Peak memory usage percent.", prom.memory_used_percent_peak),
        ("daily_ops_audit_disk_used_percent_peak", "Peak filesystem usage percent.", prom.disk_used_percent_peak),
        ("daily_ops_audit_expected_containers_down", "Expected containers not running.", prom.expected_containers_down),
    ):
        lines += _metric_header(name, help_text)
        lines.append(f"{name} {value:.6f}")
    return lines


def _issue_metrics(data: AuditData, findings: list[Finding]) -> list[str]:
    lines = _metric_header("daily_ops_audit_findings", "Findings in the latest audit by severity.")
    for severity in ("critical", "warning"):
        lines.append(f'daily_ops_audit_findings{{severity="{severity}"}} {sum(item.severity == severity for item in findings)}')
    lines += _metric_header("daily_ops_audit_backup_issues", "Backup and R2 issues in the audit window.")
    issue_values = {
        "missing": len(data.prometheus.backup_missing),
        "stale": len(data.prometheus.backup_stale),
        "r2_missing": int(data.prometheus.r2_missing),
        "r2_stale": int(data.prometheus.r2_stale),
        "set_missing": int(data.prometheus.backup_set_missing),
        "set_stale": int(data.prometheus.backup_set_stale),
        "r2_verify_missing": int(data.prometheus.r2_verify_missing),
        "r2_verify_stale": int(data.prometheus.r2_verify_stale),
    }
    for issue, value in issue_values.items():
        lines.append(f'daily_ops_audit_backup_issues{{type="{issue}"}} {value}')
    lines += _metric_header("daily_ops_audit_data_source_failures", "Data sources that failed during report generation.")
    lines.append(f"daily_ops_audit_data_source_failures {len(set(data.failures))}")
    lines += _metric_header("daily_ops_audit_runtime_issues", "Current runtime issues observed during report generation.")
    runtime_issues = {
        "systemd_failed": data.runtime.systemd_failed,
        "docker_unhealthy": data.runtime.docker_unhealthy,
        "ufw_inactive": int(data.runtime.ufw_active != 1),
        "node_exporter_down": int(data.prometheus.node_exporter_up < 1),
    }
    for issue, value in runtime_issues.items():
        lines.append(f'daily_ops_audit_runtime_issues{{type="{issue}"}} {value}')
    return lines


def build_metrics(
    data: AuditData,
    findings: list[Finding],
    delivery: DeliveryResult,
    generated_at: dt.datetime,
) -> str:
    lines = _metric_header("daily_ops_audit_last_success_timestamp", "Unix timestamp of the latest successful audit generation.")
    lines.append(f"daily_ops_audit_last_success_timestamp {int(generated_at.timestamp())}")
    lines += _metric_header("daily_ops_audit_report_date_timestamp", "Unix timestamp of the reported UTC day.")
    lines.append(f"daily_ops_audit_report_date_timestamp {int(data.window.start.timestamp())}")
    lines += _metric_header("daily_ops_audit_window_timestamp", "Audit window boundary as a Unix timestamp.")
    lines.append(f'daily_ops_audit_window_timestamp{{boundary="start"}} {int(data.window.start.timestamp())}')
    lines.append(f'daily_ops_audit_window_timestamp{{boundary="end"}} {int(data.window.end.timestamp())}')
    lines += _http_metrics(data) + _http_quality_metrics(data)
    lines += _metric_header("daily_ops_audit_security_events", "Security events in the audit window.")
    for event, value in sorted(data.security.items()):
        lines.append(f'daily_ops_audit_security_events{{event="{event}"}} {value}')
    lines += _host_metrics(data) + _issue_metrics(data, findings)
    lines += _metric_header("daily_ops_audit_log_issues", "Nginx parse and mapping issues in the audit window.")
    lines.append(f'daily_ops_audit_log_issues{{type="parse_error"}} {data.nginx_parse_errors}')
    lines.append(f'daily_ops_audit_log_issues{{type="unmapped_host"}} {data.nginx_unmapped}')
    lines += _metric_header("daily_ops_audit_delivery", "Whether Alertmanager delivery was attempted and accepted.")
    lines.append(f'daily_ops_audit_delivery{{state="attempted"}} {delivery.attempted}')
    lines.append(f'daily_ops_audit_delivery{{state="accepted"}} {delivery.accepted}')
    return "\n".join(lines) + "\n"


def send_report(
    alertmanager_url: str,
    report_date: str,
    severity: str,
    report: str,
    report_path: Path,
) -> None:
    now = dt.datetime.now(dt.timezone.utc)
    payload = [
        {
            "labels": {
                "alertname": "DailyOpsAuditReport",
                "host": "LosAngeles",
                "instance": "LosAngeles",
                "report_date": report_date,
            },
            "annotations": {
                "severity": severity,
                "summary": f"LosAngeles daily audit {report_date}: {severity.upper()}",
                "report": report,
                "report_path": str(report_path),
            },
            "startsAt": now.isoformat(),
            "endsAt": (now + dt.timedelta(hours=2)).isoformat(),
        }
    ]
    get_json(
        f"{alertmanager_url.rstrip('/')}/api/v2/alerts",
        json.dumps(payload, ensure_ascii=False).encode("utf-8"),
    )
