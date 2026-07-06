# LosAngeles standards/09 C1h Postgres 隔离恢复演练记录

更新时间：2026-07-06
服务器：`LosAngeles`
范围：Cloudflare R2、`/var/backups/ops/postgres`、临时恢复目录、临时 Postgres 容器

## 1. 结论

Postgres 备份已完成从 R2 拉回后的隔离恢复演练。

本轮验证了：

- R2 上的 Postgres `.sql.gz` 备份可以拉回。
- `sub2api-postgres` 和 `account-vault-postgres-1` 的 gzip 备份完整。
- 两个 Postgres dump 均可导入同版本临时 Postgres 容器。
- 临时容器使用 `--network none`，不暴露公网，不连接生产网络。
- 演练后生产 Postgres 和 `sub2api` 均保持 `running healthy`。

本轮不覆盖生产数据库、不切换业务、不打印 SQL dump 内容、业务数据、数据库密码、R2 凭据或角色密码 hash。

## 2. 恢复点

本轮选定恢复点：

| 类型 | 文件 | 大小 | 临时恢复镜像 |
| --- | --- | ---: | --- |
| Postgres / sub2api | `postgres/sub2api-postgres-20260706-021001.sql.gz` | `40727403` | `postgres:18-alpine@sha256:54451ecb8ab38c24c3ec123f2fd501303a3a1856a5c66e98cecf2460d5e1e9d7` |
| Postgres / account-vault | `postgres/account-vault-postgres-1-20260706-021001.sql.gz` | `9420` | `postgres:15-alpine@sha256:cd17e2ac98240fce1541ad2a803b34009b4eea5aec8a832363cdc7eca62e722e` |

## 3. 执行内容

已完成：

1. 执行 R2 同步，确认本机最新备份已同步到 R2。
2. 创建 root-only 临时隔离目录。
3. 从 R2 拉回两个 Postgres dump。
4. 对两个 `.sql.gz` 执行 `gzip -t`。
5. 使用生产同款 Postgres 镜像启动临时容器，均设置 `--network none`。
6. 等待官方 Postgres 镜像完成初始化并进入最终可查询状态。
7. 将 dump 导入临时容器内的 `postgres` 数据库。
8. 只读取元数据计数：
   - role 数量
   - 非 template database 数量
   - 可连接 database 数量
   - relation 总数
9. 删除临时 Postgres 容器和临时恢复目录。
10. 验证生产容器健康状态。

## 4. 验证结果

备份完整性：

- `postgres/sub2api-postgres-20260706-021001.sql.gz`：gzip OK。
- `postgres/account-vault-postgres-1-20260706-021001.sql.gz`：gzip OK。

隔离恢复结果：

| 服务 | 恢复结果 | roles | databases | connectable databases | total relations |
| --- | --- | ---: | ---: | ---: | ---: |
| `sub2api-postgres` | PASS | `19` | `2` | `2` | `560` |
| `account-vault-postgres-1` | PASS | `15` | `2` | `2` | `422` |

生产状态：

- `sub2api-postgres`：`running healthy`。
- `account-vault-postgres-1`：`running healthy`。
- `sub2api`：`running healthy`。

演练日志目录：

```text
/var/log/backup/postgres-isolated-restore-20260706-032907
```

## 5. 过程发现

官方 Postgres 镜像首次初始化时会先启动临时 server，再 shutdown，最后正式启动。

初版演练脚本只用 `pg_isready` 判断可用，可能撞到初始化阶段的临时 server，导致后续 `psql` 连接到正在关闭的 socket。修正后改为：

- 等待容器日志出现 `PostgreSQL init process complete; ready for start up.`。
- 再执行 `select 1` 确认最终实例可查询。

该发现只影响演练脚本时序，不影响生产数据库和备份文件本身。

## 6. 边界

本轮没有：

- 覆盖任何生产 Postgres 数据目录。
- 在生产数据库执行恢复 SQL。
- 暴露临时 Postgres 端口。
- 打印 SQL dump 内容。
- 打印业务表内容。
- 打印数据库密码、R2 凭据或 role password hash。

## 7. 后续项

- 跨机器实机恢复仍需要单独临时机器和维护窗口。
- 如果未来业务量增加，建议在新机器上做一次端到端接管演练，记录 RTO / RPO。
