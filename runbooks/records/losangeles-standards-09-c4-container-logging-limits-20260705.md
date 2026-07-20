# LosAngeles standards09 C4 容器日志上限显式化

日期：2026-07-05  
范围：Docker 容器日志驱动与轮转上限  
目标：补齐 standards/09-server-ops-handbook.md 中“容器日志上限”要求，避免 json-file 日志无限增长导致磁盘打满。

## 变更结论

已完成：

- 业务运行 compose 显式增加 `json-file` 日志上限：
  - `/opt/services/sub2api/compose.yml`
  - `/opt/services/account-vault/compose.yml`
  - `/opt/services/resume-jadeai/compose.yml`
- 运维仓库内监控栈 compose 显式增加 `json-file` 日志上限：
  - `/opt/ops/observability/docker-compose.yml`
  - `/opt/ops/observability/promtail/docker-compose.yml`
- 所有当前运行的业务与监控容器已滚动重建或复核，运行时 LogConfig 均为：
  - `driver: json-file`
  - `max-size: 50m`
  - `max-file: 5`

## 已覆盖容器

业务容器：

- `sub2api`
- `sub2api-postgres`
- `sub2api-redis`
- `account-vault-web-1`
- `account-vault-postgres-1`
- `resume-jadeai-app-1`

监控容器：

- `prometheus`
- `alertmanager`
- `grafana`
- `loki`
- `promtail`
- `node-exporter`
- `blackbox-exporter`
- `postgres-exporter-sub2api`
- `postgres-exporter-account-vault`
- `redis-exporter-sub2api`

## 验证结果

已执行：

- `docker compose config`：全部通过。
- 业务容器重建后状态检查：通过。
- `sub2api` 本机 `/health`：HTTP 200。
- `account-vault` 本机入口：HTTP 200。
- `resume-jadeai` 本机入口：HTTP 307，属于应用启动后的重定向响应，服务已运行。
- 监控栈 ready 检查：
  - Prometheus `/-/ready`：HTTP 200。
  - Alertmanager `/-/ready`：HTTP 200。
  - Loki `/ready`：HTTP 200。
  - Grafana `/api/health`：HTTP 200。
- Prometheus active targets：全部 `up`。
- `docker inspect` 运行时日志配置：全部为 `json-file max-size=50m max-file=5`。

## 注意事项

- Docker daemon 默认日志上限已在前序批次配置；本次 C4 是将上限显式写入各 compose，避免后续容器重建或迁移时依赖隐式默认值。
- `/opt/services/*/compose.yml` 是生产运行配置，当前不在 `/opt/ops` Git 跟踪范围内；本次 Git 提交会记录 `/opt/ops/observability/*` 与此 runbook，业务运行 compose 的实际变更已在服务器本机落地。
- 后续新增容器时，应复制同一日志策略，或在服务级 compose 中显式声明。

## 回滚方式

如需回滚本次容器日志显式配置：

1. 从对应 compose 中删除 `x-json-log-options` 与服务下的 `logging: *json-log-options`。
2. 对受影响服务执行 `docker compose up -d --force-recreate`。
3. 用 `docker inspect` 确认运行时 LogConfig。

不涉及数据卷清理、密钥轮换或数据库写变更。
