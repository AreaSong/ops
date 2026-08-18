# LosAngeles 统一告警响应

## 目标

为 Prometheus 告警提供统一的首响应入口，明确证据保全、影响确认、根因定位、恢复验证和升级路径。不要仅因为面板恢复为绿色就关闭事件。

## 首响应

1. 打开告警中的 Grafana 链接，确认告警开始时间、标签、当前值和关联变更注释。
2. 在 Prometheus `Alerts` 页面确认状态；从 Grafana 看板的“活动告警”进入 `/alerting/list` 检查分组、静默和抑制关系。
3. 先处理共享依赖或采集链路根因，再处理被其影响的下游告警。
4. 保留相关容器日志、systemd 日志、Prometheus 查询结果和最近部署提交，不删除故障现场。
5. 恢复后至少观察两个采集周期，并确认告警 resolved、关键业务探测成功且没有新错误。

## 专项分流

- 主机不可达或资源耗尽：[`host-unreachable.md`](host-unreachable.md)、[`disk-full.md`](disk-full.md)
- 应用 HTTP、Nginx、业务错误：[`service-5xx.md`](service-5xx.md)
- SLO、Sub2API 容量或账号池：[`sub2api-slo-capacity.md`](sub2api-slo-capacity.md)
- 本地备份、manifest、R2 或恢复演练：[`backup-set-integrity.md`](backup-set-integrity.md)、[`losangeles-r2-backup.md`](losangeles-r2-backup.md)
- 合规日志归档：[`compliance-log-archive.md`](compliance-log-archive.md)
- auditd、SSH、Fail2ban、UFW：[`auditd-security-audit.md`](auditd-security-audit.md)
- 每日运维审计：[`daily-ops-audit.md`](daily-ops-audit.md)
- 外部可用性与证书：[`github-external-uptime.md`](github-external-uptime.md)
- Alertmanager 邮件、凭据和独立出口：[`alertmanager-notification-delivery.md`](alertmanager-notification-delivery.md)
- 应用部署或回滚：[`losangeles-standard-app-deploy.md`](losangeles-standard-app-deploy.md)

## 观测链路根因顺序

1. Prometheus target、配置重载、WAL 和规则计算。
2. Alertmanager API、通知失败和 GitHub 独立 watchdog。
3. node-exporter textfile、Blackbox Exporter、Promtail 和 Loki ingest/WAL。
4. Loki compactor、保留清理和查询链路。
5. 下游业务、日志、安全和备份告警。

## 静默与关闭

- 从看板的“静默管理”进入 `/alerting/silences`；只为已批准的维护窗口创建精确 matcher 静默。
- 静默必须填写负责人、原因和结束时间；优先使用 1 小时、4 小时或已批准的维护窗口，禁止永久静默。
- 不静默共享采集器、通知链路或备份完整性根因告警来隐藏下游噪声。
- 关闭前记录根因、影响、恢复证据和预防动作；达到事故门槛时使用复盘模板。
