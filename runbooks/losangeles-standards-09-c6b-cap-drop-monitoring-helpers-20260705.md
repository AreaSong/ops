# LosAngeles standards09 C6b 监控辅助容器 cap_drop 试点

日期：2026-07-05  
范围：监控辅助容器能力收敛  
目标：在低风险容器上启用 `cap_drop: [ALL]`，减少容器默认 Linux capabilities 暴露面。

## 变更结论

已完成：

- 在 `/opt/ops/observability/docker-compose.yml` 增加 `x-cap-drop-all` anchor。
- 对以下监控辅助容器启用 `cap_drop: [ALL]`：
  - `alertmanager`
  - `blackbox-exporter`
  - `postgres-exporter-sub2api`
  - `postgres-exporter-account-vault`
  - `redis-exporter-sub2api`
- 已滚动重建上述容器并验证运行时 `HostConfig.CapDrop` 包含 `ALL`。

## 为什么只做这几个

这些容器无业务写入路径，且不需要访问宿主特权能力，适合作为第一批能力收敛试点。

本次未处理：

- `promtail`：需要读取宿主日志与 Docker 容器日志，去除能力后可能受文件权限影响。
- `node-exporter`：读取宿主根文件系统和 `/proc`，需逐 collector 验证。
- `prometheus`、`grafana`、`loki`：有持久化写目录，建议后续单容器窗口验证。
- 业务容器、数据库、Redis：不在本批次范围内。

## 验证结果

已执行：

- `docker compose config` 通过。
- 5 个目标容器滚动重建成功。
- Alertmanager ready：HTTP 200。
- `docker inspect` 验证目标容器均包含：
  - `CapDrop: ALL`
  - `SecurityOpt: no-new-privileges:true`
- Prometheus active targets：全部 `up`。

## 回滚方式

如需回滚：

1. 从目标服务移除 `cap_drop: *cap-drop-all`。
2. 如无服务引用，移除 `x-cap-drop-all` anchor。
3. 执行：
   ```bash
   cd /opt/ops/observability
   docker compose -f docker-compose.yml up -d --force-recreate --no-deps alertmanager blackbox-exporter postgres-exporter-sub2api postgres-exporter-account-vault redis-exporter-sub2api
   ```
4. 使用 `docker inspect` 与 Prometheus targets 验证。
