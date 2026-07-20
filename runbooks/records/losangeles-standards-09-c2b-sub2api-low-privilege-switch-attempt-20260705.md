# LosAngeles standards/09 C2b：sub2api 低权限数据库用户切换尝试与回滚

更新时间：2026-07-05  
服务器：LosAngeles  
范围：`sub2api`、`sub2api-postgres`  
状态：未完成；已自动/手动回滚，业务恢复健康

## 1. 目标

尝试将 `sub2api` 应用数据库连接从高权限初始化用户切换到 C2a 预创建的低权限用户：

- `sub2api_app`

## 2. 执行过程

### 2.1 第一次尝试

只更新 `.env` 中应用数据库用户后执行：

```bash
docker compose -f compose.yml up -d sub2api
```

结果：

- Compose 未重建容器。
- 运行时环境仍为 `DATABASE_USER=sub2api`。
- 脚本触发回滚，服务保持 healthy。

结论：普通 `up -d` 不足以让运行时环境变化，需要强制重建。

### 2.2 第二次尝试

使用：

```bash
docker compose -f compose.yml up -d --force-recreate --no-deps sub2api
```

结果：

- 容器被重建。
- 但运行时环境仍为 `DATABASE_USER=sub2api`。
- 原因是 `compose.yml` 中应用环境映射使用的是 `POSTGRES_USER` / `POSTGRES_PASSWORD`，不是独立的应用用户变量。
- 脚本触发回滚，服务恢复 healthy。

### 2.3 第三次尝试

修正应用服务环境映射：

```yaml
- DATABASE_USER=${DATABASE_APP_USER:-sub2api}
- DATABASE_PASSWORD=${DATABASE_APP_PASSWORD}
```

并在 `.env` 中写入：

- `DATABASE_APP_USER=sub2api_app`
- `DATABASE_APP_PASSWORD=<redacted>`

然后强制重建 `sub2api`。

结果：

- Compose 渲染值已变为 `DATABASE_USER=sub2api_app`。
- 应用容器启动后持续 `unhealthy`。
- 日志出现 migration 与 database permission denied 信号。
- 已手动恢复变更前 `.env` 和 `compose.yml`，并强制重建 `sub2api`。

## 3. 失败原因

初步判断：`sub2api` 应用启动流程会执行 migration 或 schema 维护操作，低权限用户只具备 CRUD 与 sequence 权限，不具备 migration 所需的 DDL 权限。

失败日志中出现多次：

- `migration`
- `pq:`
- `permission denied`
- Postgres `ERROR: permission denied`

因此，当前不能直接将应用运行用户切到纯 CRUD 权限模型。

## 4. 当前恢复状态

已回滚：

- `/opt/services/sub2api/.env`
- `/opt/services/sub2api/compose.yml`

已强制重建：

```bash
docker compose -f compose.yml up -d --force-recreate --no-deps sub2api
```

当前验证：

- `sub2api`：healthy
- `sub2api-redis`：healthy
- `sub2api-postgres`：healthy
- `sub2api` 运行时数据库用户恢复为 `sub2api`

## 5. 证据文件

失败与回滚证据留存在服务器临时目录：

- `/tmp/losangeles-09-c2b-sub2api-compose-switch-20260705T042706Z/`
- `/tmp/losangeles-09-c2b-manual-rollback-20260705T043028Z/`
- `/tmp/c2b-doc/failure-signals.txt`

## 6. 后续建议

C2b 暂停，不继续强切。

后续可选路径：

1. 研究 `sub2api` 是否支持关闭启动自动 migration，或把 migration 从应用启动路径拆出来。
2. 保留当前高权限用户用于应用启动与迁移，另建只读/报表/审计账号用于辅助场景。
3. 给 `sub2api_app` 补充特定 DDL 权限，但这会削弱最小权限目标，需要逐条分析日志中的具体 SQL。
4. 将数据库迁移改为独立维护命令：迁移阶段用管理用户，运行阶段用低权限用户。

当前建议：先不要再切 `sub2api` 到低权限用户，转向下一个更可控项，例如 `account-vault` 预创建低权限用户，或继续做容器资源限制。

状态：尝试失败，已回滚，业务健康。
