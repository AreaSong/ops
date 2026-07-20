# LosAngeles standards09 C6a 容器 no-new-privileges

日期：2026-07-05  
范围：业务与监控 Docker Compose 容器运行时安全参数  
目标：为当前生产容器启用 `no-new-privileges:true`，降低容器内进程通过 setuid/setgid 或文件能力获得额外权限的风险。

## 变更结论

已完成：

- 所有当前业务容器与监控容器 compose 已显式增加：
  - `security_opt: [no-new-privileges:true]`
- 已滚动重建当前运行容器，使运行时 `HostConfig.SecurityOpt` 生效。
- 本次不启用 `read_only`、不批量 `cap_drop`、不强制改非 root 用户，避免在未逐服务验证写目录和权限前造成业务中断。

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

- 所有相关 compose 文件 `docker compose config` 通过。
- 业务容器滚动重建后状态正常。
- `sub2api /health`：HTTP 200。
- `account-vault` 本机入口：HTTP 200。
- `resume-jadeai` 本机入口：HTTP 307，属于应用重定向响应。
- 监控栈 ready 检查：Prometheus、Alertmanager、Loki、Grafana 均通过。
- Prometheus active targets：全部 `up`。
- `docker inspect` 运行时验证：所有覆盖容器均包含 `no-new-privileges:true`。

## 风险与后续

- 本次选择 `no-new-privileges` 作为低风险第一步；未一刀切启用 `read_only` 或 `cap_drop`。
- 后续 C6b 建议逐服务评估：
  - Exporter 类容器优先尝试 `cap_drop: [ALL]`。
  - 只读型容器优先尝试 `read_only: true` + 必要 `tmpfs`。
  - 数据库、Redis、业务应用需先确认写路径和运行用户，再做更强约束。

## 回滚方式

如需回滚：

1. 从对应 compose 中移除 `security_opt: *security-options` 与未被引用的 `x-security-options` anchor。
2. 对受影响服务执行 `docker compose up -d --force-recreate`。
3. 使用 `docker inspect` 验证 `HostConfig.SecurityOpt`。
