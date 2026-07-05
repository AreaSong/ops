# LosAngeles standards09 R1 备份恢复演练

日期：2026-07-05  
范围：本机备份恢复可用性演练  
目标：验证“备份文件存在”之外，关键备份可以被读取、解压、恢复到临时环境。

## 演练结论

状态：完成。

本次演练未连接或写入生产数据库，未覆盖生产数据，未重启生产业务容器。

已验证：

- Postgres 最新 dump gzip 完整性。
- `sub2api` Postgres dump 可恢复到临时 Postgres 容器。
- `account-vault` Postgres dump 可恢复到临时 Postgres 容器。
- Redis 备份 tar 包可列目录。
- configs 备份 tar 包可列目录。
- sub2api 与 JadeAI volume 备份 tar 包可列目录。
- 演练后生产入口快速健康检查通过。

## 使用的备份文件

- sub2api Postgres：
  - `/var/backups/ops/postgres/sub2api-postgres-20260705-021001.sql.gz`
- account-vault Postgres：
  - `/var/backups/ops/postgres/account-vault-postgres-1-20260705-021001.sql.gz`
- Redis：
  - `/var/backups/ops/redis/redis-20260705-023001.tar.gz`
- configs：
  - `/var/backups/ops/configs/configs-20260705-030001.tar.gz`
- sub2api volume：
  - `/var/backups/ops/volumes/sub2api-data-20260705-033001.tar.gz`
- JadeAI volume：
  - `/var/backups/ops/volumes/jadeai-data-20260705-033001.tar.gz`

## 恢复方式

Postgres：

- 使用与生产容器相同的镜像创建临时容器，并使用 `--network none` 避免临时容器对外联网。
- 将 `pg_dumpall` plain SQL gzip 解压后通过 `psql -v ON_ERROR_STOP=1` 导入临时容器。
- 导入后检查数据库列表和用户表数量。
- 演练完成后销毁临时容器。

Archive：

- 使用 `tar -tzf` 验证包可读并统计目录项。

## 验证结果

Postgres：

- `sub2api`：恢复成功，目标数据库 `sub2api` 存在，用户表数量 `74`。
- `account-vault`：恢复成功，目标数据库 `accountvault` 存在，用户表数量 `6`。

Archive：

- Redis 备份包：可列目录，条目数 `6`。
- configs 备份包：可列目录，条目数 `295`。
- sub2api volume 备份包：可列目录，条目数 `6`。
- JadeAI volume 备份包：可列目录，条目数 `4`。

生产快速健康检查：

- `sub2api /health`：通过。
- `account-vault`：HTTP `200`。
- `resume-jadeai`：HTTP `307`，属于应用重定向响应。
- Prometheus / Alertmanager / Loki / Grafana ready：通过。

## 清理结果

- 临时恢复容器已由脚本 `trap` 清理。
- 复查未发现 `restore-drill-*` 容器残留。

## 注意事项

- 本次是“可恢复性演练”，不是完整 RTO/RPO 压测。
- 未做 Redis 真实启动恢复，未做 volume 全量回放到应用容器。
- 后续可在维护窗口补充：
  - Redis 临时容器加载 RDB/AOF 验证。
  - 业务 volume 恢复到临时应用实例验证。
  - 记录完整恢复耗时与人工步骤，形成 RTO/RPO 基线。
