# LosAngeles 备份恢复演练记录

日期：2026-07-03 07:32 BST
服务器：LosAngeles
演练类型：本机备份非破坏性恢复验证
演练目录：临时目录 `/tmp/ops-restore-drill-20260703-072221`，演练后已删除

## 1. 结论

本次恢复演练通过。

已验证：

- Postgres 备份可恢复到临时 PostgreSQL 容器。
- Redis `dump.rdb` 可通过 `redis-check-rdb` 校验。
- configs、Redis、volume tar 包可解包到临时目录。
- 演练未覆盖生产数据，未重启生产业务，未对外暴露临时端口。
- 演练后临时容器、临时目录和导入日志均已清理。

## 2. 恢复点

| 类型 | 备份文件 | 备份时间 |
| --- | --- | --- |
| Postgres / account-vault | `/var/backups/ops/postgres/account-vault-postgres-1-20260703-021001.sql.gz` | 2026-07-03 02:10 BST |
| Postgres / sub2api | `/var/backups/ops/postgres/sub2api-postgres-20260703-021001.sql.gz` | 2026-07-03 02:10 BST |
| Redis | `/var/backups/ops/redis/redis-20260703-023001.tar.gz` | 2026-07-03 02:30 BST |
| Configs | `/var/backups/ops/configs/configs-20260703-030001.tar.gz` | 2026-07-03 03:00 BST |
| Volumes / sub2api | `/var/backups/ops/volumes/sub2api-data-20260703-033001.tar.gz` | 2026-07-03 03:30 BST |
| Volumes / jadeai | `/var/backups/ops/volumes/jadeai-data-20260703-033001.tar.gz` | 2026-07-03 03:30 BST |

## 3. Postgres 恢复验证

| 数据库 | 临时镜像 | 恢复结果 | 用时 | 验证结果 |
| --- | --- | --- | --- | --- |
| account-vault | `postgres:15-alpine` | `psql -v ON_ERROR_STOP=1` 返回 0 | 0 秒 | 恢复出 `accountvault`；业务表数量 6 |
| sub2api | `postgres:18-alpine` | `psql -v ON_ERROR_STOP=1` 返回 0 | 23 秒 | 恢复出 `sub2api`；业务表数量 74；`pg_stat_user_tables` 估算行数 640062 |

临时容器：

- `restore-account-vault-20260703-072221`
- `restore-sub2api-20260703-072221`

以上容器演练后均已删除。

## 4. Redis 恢复验证

Redis 备份解包后发现：

- `redis_data/dump.rdb`
- 文件大小：206637 bytes

使用 `redis:8-alpine` 的 `redis-check-rdb` 校验结果：

- `Checksum OK`
- `RDB looks OK`
- keys read：236
- expires：78
- already expired：22

## 5. 配置与 Volume 恢复验证

| 类型 | 结果 |
| --- | --- |
| configs | 解包成功；文件数量 94；覆盖 `etc/nginx`、`etc/x-ui`、`opt/ops`、`opt/services`、`root/sub2api-deploy` 等路径 |
| sub2api volume | 解包成功；文件数量 5；解包体积约 93M |
| jadeai volume | 解包成功；文件数量 3；解包体积约 4.8M |

## 6. RTO / RPO 记录

RTO 观察值：

- account-vault Postgres 临时导入：0 秒级。
- sub2api Postgres 临时导入：23 秒。
- Redis、configs、volume：本次只做解包/格式校验，未做完整业务接管计时。

RPO 观察值：

- 数据库可恢复到 2026-07-03 02:10 BST 的备份点。
- Redis 可恢复到 2026-07-03 02:30 BST 的备份点。
- 配置可恢复到 2026-07-03 03:00 BST 的备份点。
- 非数据库 volume 可恢复到 2026-07-03 03:30 BST 的备份点。

## 7. 未覆盖项

- 未将恢复后的数据挂载回生产业务容器。
- 未执行完整应用级启动验证。
- 未执行异地对象存储恢复，因为对象存储备份尚未接入。
- 未读取或打印 `.env`、私钥、数据库内容或 Redis key 内容。
