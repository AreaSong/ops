# LosAngeles sub2api 数据目录迁移记录

日期：2026-07-03 12:22 BST
服务器：LosAngeles
演练/变更类型：生产维护窗口内数据目录规范化迁移
日志：`/var/log/ops/sub2api-data-migration-20260703-122255.log`

## 1. 结论

sub2api 数据目录已从 `/root/sub2api-deploy` 迁移到 `/var/lib/sub2api`。

已迁移的 bind mount：

| 容器 | 原路径 | 新路径 | 容器内路径 |
| --- | --- | --- | --- |
| sub2api | `/root/sub2api-deploy/data` | `/var/lib/sub2api/data` | `/app/data` |
| sub2api-postgres | `/root/sub2api-deploy/postgres_data` | `/var/lib/sub2api/postgres_data` | `/var/lib/postgresql/data` |
| sub2api-redis | `/root/sub2api-deploy/redis_data` | `/var/lib/sub2api/redis_data` | `/data` |

## 2. 执行方式

1. 迁移前运行 Postgres、Redis、volume 本机备份。
2. 创建 `/var/lib/sub2api` 目标目录。
3. 在线预同步数据，减少停机窗口。
4. 停止 sub2api compose stack。
5. 停机最终同步数据。
6. 备份并修改 `/opt/services/sub2api/compose.yml`。
7. 使用 `docker compose up -d --force-recreate` 重建 sub2api 相关容器。
8. 验证容器健康、挂载路径、应用 health、Postgres、Redis、备份与 R2 dry-run。

## 3. 验证结果

- `sub2api`、`sub2api-postgres`、`sub2api-redis` 均恢复运行并 healthy。
- Docker inspect 未再发现 sub2api 容器使用 `/root/sub2api-deploy` 挂载。
- 本地 `http://127.0.0.1:8080/health` 返回 200。
- `pg_isready -U sub2api -d sub2api` 通过。
- `redis-cli ping` 通过。
- `https://log.areasong.top/` 返回 200。
- 迁移后 configs/volumes 备份运行成功。
- R2 dry-run 显示没有待同步内容或无异常。

## 4. 回滚信息

- 原始 compose 已备份到：`/var/backups/ops/manual/sub2api-data-migration-20260703-122255/compose.yml.before`
- 原目录 `/root/sub2api-deploy` 暂未删除，保留作为短期回滚来源。
- 如需回滚，可停 sub2api stack，恢复 compose 备份并重新指向 `/root/sub2api-deploy`，再启动并验证。

## 5. 后续建议

1. 观察至少 24 小时，确认 sub2api、Postgres、Redis、备份和 R2 同步均正常。
2. 观察期后再决定是否归档或删除 `/root/sub2api-deploy`。
3. 归档/删除前建议先确认最新本机备份和 R2 备份均包含 `/var/lib/sub2api`。

## 6. 未覆盖项

- 未删除旧目录 `/root/sub2api-deploy`。
- 未做跨机器恢复。
- 未读取或打印 `.env`、数据库内容、Redis key/value 或密钥。
