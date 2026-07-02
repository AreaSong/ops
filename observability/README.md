# 可观测栈部署指南

## 架构

单机部署 Prometheus + Grafana + Loki + Alertmanager，承载在 `prod-monitor-01`。

```
各服务器                          prod-monitor-01
┌─────────────┐                  ┌──────────────────────────────┐
│ node_exporter│ ── :9100 ────→ │ Prometheus (:9090)           │
│ promtail     │ ── logs ────→  │ Loki (:3100)                 │
└─────────────┘                  │ Grafana (:3000)              │
                                 │ Alertmanager (:9093)         │
                                 └──────────────────────────────┘
```

## 部署步骤

1. 在 `prod-monitor-01` 上克隆 ops 仓库到 `/opt/ops/`
2. 复制环境变量模板并填写：

```bash
cp observability/.env.example observability/.env
# 编辑 .env，设置 GRAFANA_ADMIN_PASSWORD
```

3. 启动栈：

```bash
cd /opt/compose/observability
docker compose -f /opt/ops/observability/docker-compose.yml up -d
```

4. 访问 Grafana：`http://<monitor-ip>:3000`（默认 admin / .env 中设置的密码）

5. 在各服务器部署 promtail（见 `promtail/` 目录）

## 端口

| 服务 | 端口 | 访问范围 |
|------|------|----------|
| Grafana | 3000 | 内网 / VPN |
| Prometheus | 9090 | 内网 |
| Loki | 3100 | 内网 |
| Alertmanager | 9093 | 内网 |

## 告警通知

编辑 `alertmanager/alertmanager.yml`，配置 webhook 通知渠道（钉钉/企微/Slack）。

## 维护

- Prometheus 数据保留 30 天（`--storage.tsdb.retention.time=30d`）
- Loki 数据保留 14 天
- 定期备份 Grafana Dashboard 配置（`/data/grafana/`）
