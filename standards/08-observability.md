# 08 可观测性规范

> 可观测栈部署见 `observability/` 目录。故障排查手册见 `runbooks/`。观测、告警、诊断、处置、执行和审计之间的权威边界见 [10 运维观测与控制面职责边界规范](10-operations-control-plane.md)。

## 职责边界

- Prometheus 是指标规则和告警判断的唯一来源。
- Alertmanager 负责告警分组、抑制、静默、通知和恢复。
- Loki 负责日志保存与查询。
- Grafana 负责趋势、日志、活动告警和诊断，不执行生产变更。
- AreaSong Ops 负责预检、计划、批准、执行、验证、恢复和变更审计，不复制观测与告警能力。
- Git 保存非敏感期望配置；cron/systemd 负责周期调度。

新增观测或运维能力前，必须先按职责边界规范确定权威来源和跨系统关联，不得建立第二套告警规则、静默管理或生产执行通道。

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
- LosAngeles 由 `observability/docker-compose.yml` 管理；不得同时启用 systemd 实例竞争 9100 端口
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

- [ ] node_exporter 容器运行正常且 Prometheus `up{job="node"}` 为 1
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

- 2026-08-11 补充观测与控制面职责边界引用。
- 2026-07-02 初版
