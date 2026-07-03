# LosAngeles R2 异地备份拉回恢复演练记录

日期：2026-07-03 09:41 BST
服务器：LosAngeles
演练类型：Cloudflare R2 异地备份非破坏性拉回恢复验证
演练目录：临时目录 `/tmp/r2-restore-drill-20260703-094127`，演练后已删除
日志：`/var/log/ops/r2-restore-drill-20260703-094127.log`

## 1. 结论

本次 R2 拉回恢复演练通过。

已验证：

- Cloudflare R2 `losangeles/` 前缀可拉回到本机临时目录。
- `rclone check --size-only --one-way` 通过，远端对象在本地临时副本中均存在且大小一致。
- Postgres 备份可从 R2 拉回副本恢复到临时 PostgreSQL 容器。
- Redis `dump.rdb` 可从 R2 拉回副本解包并通过 `redis-check-rdb` 校验。
- configs、Redis、volume tar 包可从 R2 拉回副本解包。
- 演练未覆盖生产数据，未重启生产业务，未对外暴露临时端口。
- 演练后临时容器和临时目录均已清理。

## 2. R2 拉回核验

| 项目 | 结果 |
| --- | --- |
| Bucket | `losangeles-ops-backups` |
| Prefix | `losangeles/` |
| 远端对象数 | 22 |
| 远端总大小 | 90363919 bytes |
| 本地拉回对象数 | 22 |
| 本地拉回总大小 | 90363919 bytes |
| rclone check | 通过，size-only one-way |

## 3. 恢复点

| 类型 | 备份文件 | 结果 |
| --- | --- | --- |
| Postgres / account-vault | `postgres/account-vault-postgres-1-20260703-021001.sql.gz` | gzip 完整性通过，临时导入通过 |
| Postgres / sub2api | `postgres/sub2api-postgres-20260703-021001.sql.gz` | gzip 完整性通过，临时导入通过 |
| Redis | `redis/redis-20260703-023001.tar.gz` | gzip 完整性通过，`redis-check-rdb` 通过 |
| Configs | `configs/configs-20260703-030001.tar.gz` | gzip 完整性通过，解包通过 |
| Volumes / sub2api | `volumes/sub2api-data-20260703-033001.tar.gz` | gzip 完整性通过，解包通过 |
| Volumes / jadeai | `volumes/jadeai-data-20260703-033001.tar.gz` | gzip 完整性通过，解包通过 |

## 4. Postgres 恢复验证

| 数据库 | 临时镜像 | 恢复结果 | 用时 | 验证结果 |
| --- | --- | --- | --- | --- |
| account-vault | `postgres:15-alpine` | `psql -v ON_ERROR_STOP=1` 返回 0 | 0 秒 | 恢复出 `accountvault`；业务表数量 6 |
| sub2api | `postgres:18-alpine` | `psql -v ON_ERROR_STOP=1` 返回 0 | 22 秒 | 恢复出 `sub2api`；业务表数量 74；`pg_stat_user_tables` 估算行数 640062 |

临时容器：

- `restore-account-vault-r2-20260703-094127`
- `restore-sub2api-r2-20260703-094127`

以上容器演练后均已删除。

## 5. Redis 恢复验证

Redis 备份解包后发现：

- `dump.rdb`
- 文件大小：206637 bytes

`redis-check-rdb` 校验摘要：

`[offset 206637] Checksum OK;[offset 206637] \o/ RDB looks OK! \o/ [info] 236 keys read;[info] 78 expires [info] 22 already expired;[info] 0 subexpires`

## 6. 配置与 Volume 恢复验证

| 类型 | 结果 |
| --- | --- |
| configs | 解包成功；文件数量 94 |
| sub2api volume | 解包成功；文件数量 5；解包体积约 93M |
| jadeai volume | 解包成功；文件数量 3；解包体积约 4.8M |

## 7. 未覆盖项

- 未将恢复后的数据挂载回生产业务容器。
- 未执行完整应用级启动验证。
- 未测试跨机器恢复，仅验证了从 R2 拉回到当前主机后的恢复能力。
- 未读取或打印 `.env`、私钥、数据库内容或 Redis key 内容。
