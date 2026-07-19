# LosAngeles 每日运维审计

## 目标

每天 `00:20 UTC` 审计上一完整 UTC 日，将流量、访问质量、安全事件、备份和主机状态形成可回溯报告，并通过 Alertmanager 专属 receiver 发送一封日报。

## 隐私边界

- 不保存完整 IP、用户名、邮箱、查询参数、Cookie、Authorization 或日志原文。
- 客户端地址仅使用每次运行随机密钥在内存中去重，只有聚合数量进入报告。
- Top 路径只保留白名单路由段；未知或动态段统一变成 `:value`，静态文件名变成 `:asset.<ext>`。
- 报告权限为 `0640`，保留 180 天；任务日志按日轮转并保留 30 份。

## 正常输出

| 输出 | 位置 |
|---|---|
| Markdown 日报 | `/var/log/observability/daily-ops-audit-YYYY-MM-DD.md` |
| 任务日志 | `/var/log/observability/daily-ops-audit.log` |
| Prometheus 指标 | `/var/lib/node_exporter/textfile_collector/daily-ops-audit.prom` |
| Grafana | `LosAngeles Daily Operations Audit` |

## 手工核验

先禁止邮件生成报告：

```bash
sudo /opt/ops/observability/scripts/write-daily-ops-audit.sh --no-email
```

检查报告日期、权限和隐私边界：

```bash
sudo stat -c '%a %U:%G %n' /var/log/observability/daily-ops-audit-*.md
sudo sed -n '1,220p' /var/log/observability/daily-ops-audit-$(date -u -d yesterday +%F).md
```

检查 textfile 指标和 Prometheus 采集：

```bash
curl -fsS http://127.0.0.1:9090/api/v1/query --get --data-urlencode 'query=daily_ops_audit_last_success_timestamp'
curl -fsS http://127.0.0.1:9090/api/v1/query --get --data-urlencode 'query=daily_ops_audit_data_source_failures'
curl -fsS http://127.0.0.1:9090/api/v1/query --get --data-urlencode 'query=daily_ops_audit_http_error_ratio'
```

确认本地报告合理后，执行一次真实邮件投递：

```bash
sudo /opt/ops/observability/scripts/write-daily-ops-audit.sh
```

`delivery accepted=1` 只证明 Alertmanager 接受告警；首次部署和邮件配置变更后仍需人工确认收件箱真实送达。

## 告警处理

### `DailyOpsAuditStale`

1. 检查 `/var/log/observability/daily-ops-audit.log`。
2. 检查 cron 文件、脚本权限和 `flock` 锁。
3. 使用 `--no-email` 手工重跑，先解决数据源或脚本错误。
4. 确认指标更新时间恢复后再发送日报。

### `DailyOpsAuditDataSourceFailure`

报告的“数据源失败”会列出安全名称，不包含日志内容。依次检查对应日志权限、Prometheus/Alertmanager API、Docker、systemd 和 UFW；不要把权限问题用 `chmod 777` 规避。

`UFW` 日告警阈值按 2026-07-12 至 2026-07-15 的公网基线校准为 6000 次；当流量入口、暴露端口或云防火墙策略变化时重新核验。未映射 Host 仅在超过 1000 次且占当日 Nginx 请求至少 5% 时告警，少量裸 IP 和无效 Host 仍保留在报告统计中。

HTTP 5xx 按服务独立评估：日请求少于 1000 时，任意 5xx 为 Warning；日请求不少于 1000 时，错误率超过 0.1% 为 Warning，超过 1% 为 Critical。报告同时列出 5xx 数量、请求量、错误率和规范化错误路径；路径中的动态值不会原样保存。

### `DailyOpsAuditCriticalFindings`

先依据报告中的具体项目进入对应 runbook：5xx 使用 `service-5xx.md`，磁盘使用 `disk-full.md`，机器失联使用 `host-unreachable.md`。生产变更仍按变更管理流程单项执行和验证。

### `DailyOpsAuditDeliveryFailed`

1. 检查 Alertmanager readiness 和日志。
2. 检查日报 route/template 是否通过 `amtool check-config`。
3. 确认 SMTP 密码文件存在且保持 root-only；不得把授权码放进命令行或 Git。
4. 恢复后使用同一报告日重跑。日报 alert identity 固定，不会因严重度变化并存两条告警。

## 回滚

如日报造成明显 I/O 或错误告警，使用 `ansible/observability-host-jobs.yml` 将 `current` 原子切回上一不可变 generation，不直接手改 `/etc/cron.d`；保留脚本、报告和日志作为证据。Alertmanager、Promtail 或 Prometheus 配置回滚到变更前备份并分别重建/reload，确认原有目标和告警均正常后结束回滚。
