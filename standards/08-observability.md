# 08 可观测性规范

> 可观测栈部署见 `observability/` 目录。故障排查手册见 `runbooks/`。

## 架构

```
各服务器                          监控服务器 (prod-monitor-01)
┌─────────────┐                  ┌──────────────────────────────┐
│ node_exporter│ ── metrics ──→ │ Prometheus                   │
│ promtail     │ ── logs ────→  │ Loki                         │
└─────────────┘                  │ Grafana (可视化)              │
                                 │ Alertmanager (告警)           │
                                 └──────────────────────────────┘
```

## 指标采集

### node_exporter（所有服务器）

- 端口：9100（仅内网访问）
- 由 Ansible 基线剧本统一安装
- 采集：CPU、内存、磁盘、网络、文件系统

### 应用指标（按需）

| 服务 | Exporter | 端口 |
|------|----------|------|
| MySQL | mysqld_exporter | 9104 |
| Redis | redis_exporter | 9121 |
| Nginx | nginx-prometheus-exporter | 9113 |
| Docker | cadvisor | 8080 |

## 日志采集

### Promtail（所有服务器）

- 采集路径：
  - `/var/log/**/*.log`
  - `/var/log/<服务名>/`
  - Docker 容器日志（通过 Docker SD）
- 推送到 Loki

### 日志标签

| 标签 | 来源 | 示例 |
|------|------|------|
| host | 主机名 | prod-web-01 |
| job | 日志类型 | syslog / nginx / app |
| env | 环境 | prod |

## 告警规则

### 告警分级

| 级别 | 响应 | 通知方式 |
|------|------|----------|
| P1 Critical | 立即处理 | 即时通知（电话/短信） |
| P2 Warning | 30 分钟内 | 即时通知（IM） |
| P3 Info | 工作时间处理 | 每日摘要 |

### 起步告警规则

| 告警 | 条件 | 级别 |
|------|------|------|
| 主机宕机 | up == 0 持续 2 分钟 | P1 |
| 磁盘使用率 | > 85% 持续 10 分钟 | P2 |
| 磁盘使用率 | > 95% 持续 5 分钟 | P1 |
| 内存使用率 | > 90% 持续 10 分钟 | P2 |
| CPU 使用率 | > 90% 持续 15 分钟 | P3 |
| 服务不可达 | probe_success == 0 持续 2 分钟 | P1 |
| SSL 证书过期 | < 30 天 | P2 |
| SSL 证书过期 | < 7 天 | P1 |

规则文件：`observability/prometheus/rules/`

## Grafana Dashboard

起步 Dashboard：

- **Overview**：所有主机状态一览
- **Node Detail**：单主机详细指标
- **Logs Explorer**：Loki 日志查询

## 新服务接入检查清单

- [ ] node_exporter 运行正常（基线剧本已安装）
- [ ] promtail 采集该服务日志
- [ ] 应用 exporter 已部署（如有）
- [ ] Grafana 中可看到该服务数据
- [ ] 关键告警规则已配置

## 排障优先级

1. Grafana Dashboard 看指标趋势
2. Loki 查日志（按 host + job 过滤）
3. SSH 到机器深入排查（runbooks/ 参考）

---

修订记录：

- 2026-07-02 初版
