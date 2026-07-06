# LosAngeles standards/09 C1g R2 隔离恢复演练记录

更新时间：2026-07-06
服务器：`LosAngeles`
范围：Cloudflare R2、`/var/backups/ops`、临时恢复目录、临时 Redis 容器

## 1. 结论

R2 备份已完成隔离恢复演练。

本轮验证了：

- 备份可以从 R2 拉回。
- Postgres gzip 备份完整。
- Redis、configs、volumes tar 包完整。
- Redis `dump.rdb` 和 `users.acl` 可以一起恢复到临时 Redis。
- Redis ACL 高危命令限制随 `users.acl` 一起恢复。

本轮不覆盖生产数据、不切换业务、不暴露临时服务到公网、不打印数据库内容、Redis key/value、R2 凭据、Redis 密码或 `users.acl` 内容。

## 2. 恢复点

本轮选定恢复点：

| 类型 | 文件 | 大小 |
| --- | --- | ---: |
| Redis | `redis/redis-20260706-023215.tar.gz` | `136204` |
| Postgres / sub2api | `postgres/sub2api-postgres-20260706-021001.sql.gz` | `40727403` |
| Postgres / account-vault | `postgres/account-vault-postgres-1-20260706-021001.sql.gz` | `9420` |
| Configs | `configs/configs-20260706-030001.tar.gz` | `359735` |
| Volumes / jadeai | `volumes/jadeai-data-20260705-033001.tar.gz` | `447939` |
| Volumes / sub2api | `volumes/sub2api-data-20260705-033001.tar.gz` | `8582` |

说明：本机当前没有 2026-07-06 03:30 的 volumes 备份，因此 volumes 使用最新可用的 2026-07-05 03:30 恢复点。

## 3. 执行内容

已完成：

1. 执行 R2 同步，确保本机最新备份进入 R2。
2. 创建 root-only 临时隔离目录。
3. 从 R2 拉回选定恢复点。
4. 对 Postgres `.sql.gz` 执行 `gzip -t`。
5. 对 Redis / configs / volumes `.tar.gz` 执行 `tar -tzf`。
6. 验证 Redis 包包含：

```text
metadata.txt
redis_data/dump.rdb
redis_data/users.acl
```

7. 验证 Redis metadata 包含：

```text
aclfile_included=yes
```

8. 解出 Redis `dump.rdb` 和 `users.acl` 到临时目录。
9. 使用生产 Redis 同镜像启动临时容器，参数为：

```text
--network none
--aclfile /data/users.acl
--dir /data
--dbfilename dump.rdb
```

10. 使用生产 Redis 密码在临时容器内验证认证访问。
11. 验证高危命令 `FLUSHALL` 被 ACL 拒绝。
12. 删除临时 Redis 容器和临时恢复目录。

## 4. 验证结果

备份完整性：

- `postgres/sub2api-postgres-20260706-021001.sql.gz`：gzip OK。
- `postgres/account-vault-postgres-1-20260706-021001.sql.gz`：gzip OK。
- `redis/redis-20260706-023215.tar.gz`：tar OK，`4` 个条目。
- `configs/configs-20260706-030001.tar.gz`：tar OK，`342` 个条目。
- `volumes/jadeai-data-20260705-033001.tar.gz`：tar OK，`4` 个条目。
- `volumes/sub2api-data-20260705-033001.tar.gz`：tar OK，`6` 个条目。

Redis 隔离恢复：

- `dump.rdb` 解出大小：`309827` 字节。
- `users.acl` 解出大小：`280` 字节。
- 临时 Redis 容器启动成功。
- 认证 `PING` 返回成功。
- `DBSIZE=188`。
- `CONFIG GET aclfile` 返回 `/data/users.acl`。
- `FLUSHALL` 返回权限拒绝，符合预期。

生产状态：

- `sub2api-redis`：`running healthy`。
- `sub2api`：`running healthy`。

## 5. 边界

本轮没有：

- 覆盖 `/var/lib/sub2api/redis_data`。
- 覆盖任何生产 Postgres 数据库。
- 启动临时 Postgres 数据库恢复 SQL。
- 打印 Redis key/value。
- 打印 SQL dump 内容。
- 打印 Redis ACL 文件内容。
- 打印任何 R2 / Redis / 数据库凭据。

## 6. 后续项

- 下一步可做 Postgres 临时容器恢复演练，验证 `.sql.gz` 可恢复到隔离数据库。
- 后续跨机器实机恢复仍需要单独临时机器和维护窗口。
- 如果希望恢复演练覆盖同一天完整恢复点，需要等待下一轮 03:30 volumes 备份完成后再执行一次。
