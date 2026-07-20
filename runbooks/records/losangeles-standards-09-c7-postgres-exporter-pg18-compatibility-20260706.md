# LosAngeles standards09 C7 Postgres exporter PostgreSQL 18 兼容性修复

日期：2026-07-06
范围：`postgres-exporter-sub2api`、`postgres-exporter-account-vault`
类型：监控辅助容器兼容性修复

## 1. 背景

C2f 只读分析时发现 `postgres-exporter-sub2api` 持续报错：

```text
collector failed name=stat_bgwriter err="pq: column \"checkpoints_timed\" does not exist"
```

进一步只读核对确认：

- `sub2api-postgres` 为 PostgreSQL 18.3。
- PostgreSQL 18 中 `pg_stat_bgwriter` 不再包含 `checkpoints_timed` 等 checkpoint 字段。
- `account-vault-postgres-1` 为 PostgreSQL 15.18，`pg_stat_bgwriter` 仍包含这些字段，因此同版本 exporter 没有该报错。
- 旧 exporter 为 `quay.io/prometheuscommunity/postgres-exporter:v0.15.0`。
- `v0.19.1` 的帮助输出已确认支持 `--collector.stat_checkpointer`。

## 2. 本次变更

只调整 Postgres exporter 监控辅助容器，未重启业务容器、Postgres 或 Redis。

已修改：

- 将两个 Postgres exporter 镜像升级到：
  - `quay.io/prometheuscommunity/postgres-exporter:v0.19.1@sha256:e96064f876226d94bb6ce48a4c4b3dd76edba91168ec1ab024e5c4b959310b0f`
- 对 `postgres-exporter-sub2api` 增加启动参数：
  - `--no-collector.stat_bgwriter`
  - `--collector.stat_checkpointer`
- `postgres-exporter-account-vault` 保持默认 collector，因为它连接 PostgreSQL 15.18，仍使用 `pg_stat_bgwriter`。

变更文件：

- `observability/docker-compose.yml`

## 3. 验证结果

已验证：

- `docker compose config` 通过。
- `postgres-exporter-sub2api` running。
- `postgres-exporter-account-vault` running。
- 两个 exporter 均运行 `v0.19.1` digest。
- `postgres-exporter-sub2api` 运行参数为 `--no-collector.stat_bgwriter`、`--collector.stat_checkpointer`。
- 重建后 `postgres-exporter-sub2api` 无新的 `checkpoints_timed`、`collector failed`、`pq:` 错误。
- 重建后 `sub2api-postgres` 无新的 `checkpoints_timed` / `pg_stat_bgwriter` 错误。
- Prometheus 查询结果：
  - `up{job="postgres"}`：`sub2api-postgres=1`，`account-vault-postgres=1`
  - `pg_exporter_last_scrape_error{job="postgres"}`：`sub2api-postgres=0`，`account-vault-postgres=0`

## 4. 回滚

如果新 exporter 出现异常，回滚步骤：

1. 将 `observability/docker-compose.yml` 中两个 Postgres exporter 镜像恢复到上一版：
   - `quay.io/prometheuscommunity/postgres-exporter:v0.15.0@sha256:386b12d19eab2a37d7cd8ca8b4c7491cc7a830d9581f49af6c98a393da9605e6`
2. 删除 `postgres-exporter-sub2api` 的 `command` 覆盖。
3. 执行：

```bash
cd /opt/ops
sudo docker compose -f observability/docker-compose.yml up -d --no-deps postgres-exporter-sub2api postgres-exporter-account-vault
```

4. 验证 Prometheus `up{job="postgres"}` 和 exporter 日志。

## 5. 状态

状态：完成。

剩余注意项：后续新增或升级 PostgreSQL 主版本时，需要同步核对 exporter collector 与 PostgreSQL 系统视图兼容性。
