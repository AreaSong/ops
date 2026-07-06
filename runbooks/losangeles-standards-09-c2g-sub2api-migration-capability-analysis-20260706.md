# LosAngeles standards09 C2g sub2api migration 能力分析

日期：2026-07-06
范围：`sub2api` 上游源码、当前生产 compose 受控副本、迁移启动链路
审计方式：源码只读分析 + 现有运行配置核对
运行态影响：无。未重启容器，未修改数据库、权限、配置或业务数据。

## 1. 结论

按当前可核验证据，`sub2api` 暂不具备安全拆分 migration 用户与 runtime 用户所需的两个关键能力：

- 未发现独立的生产可用 migration-only 命令。
- 未发现关闭启动自动 migration 的环境变量、配置项或命令行参数。

因此，当前不应再次直接把生产业务容器从 `DATABASE_USER=sub2api` 切到 `sub2api_app`。C2f 定位的失败点仍然成立：应用启动初始化数据库时会执行 `CREATE TABLE IF NOT EXISTS schema_migrations`，低权限运行用户没有 `public` schema `CREATE` 权限时会启动失败。

## 2. 已核实证据

上游源码：

- 仓库：`https://github.com/Wei-Shaw/sub2api.git`
- 分支 HEAD：`b650bdd68d25bad3e502b2e34efe775555da2eba`
- 当前生产镜像受控副本：`weishaw/sub2api@sha256:b12017d69050ba83e2a3dfa1fd342c25720912937aee5043d5793c6cce0a459e`

入口参数：

- `backend/cmd/server/main.go` 仅注册 `--setup` 和 `--version`。
- 未发现 `--migrate`、`--migrate-only`、`--skip-migration` 或类似命令。

生产镜像入口：

- `Dockerfile` 使用 `ENTRYPOINT ["/app/docker-entrypoint.sh"]`。
- `Dockerfile` 使用 `CMD ["/app/sub2api"]`。
- `deploy/docker-entrypoint.sh` 只处理 `/app/data` 权限和参数兼容，最终 `exec "$@"`，未提供 migration 分支。

启动链路：

- `backend/cmd/server/main.go` 正常模式调用 `runMainServer()`。
- `runMainServer()` 调用 `initializeApplication(buildInfo)`。
- `backend/cmd/server/wire_gen.go` 中 `initializeApplication()` 调用 `repository.ProvideEnt(configConfig)`。
- `backend/internal/repository/ent.go` 的 `InitEnt()` 在创建 Ent client 前调用 `applyMigrationsFS(migrationCtx, drv.DB(), migrations.FS)`。
- `backend/internal/repository/migrations_runner.go` 中 `applyMigrationsFS()` 每次都会先执行 `schemaMigrationsTableDDL`。

迁移表 DDL：

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    filename   TEXT PRIMARY KEY,
    checksum   TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

文档与 Makefile：

- `backend/migrations/README.md` 提到 `make migrate-up` / `make migrate-down`，但当前根目录 `Makefile` 与 `backend/Makefile` 未提供这些 target。
- `deploy/README.md` 描述 `AUTO_SETUP=true` 场景会在应用启动流程中应用数据库迁移并记录到 `schema_migrations`，不是独立 migration-only 工作流。

## 3. 判断

`sub2api_app` 当前没有 schema `CREATE` 权限不是漏配，而是为了达成“应用运行用户无 DDL”的最小权限目标。

如果直接给 `sub2api_app` 授予 `public` schema `CREATE`，应用启动可以越过当前失败点，但运行用户将重新获得在 `public` schema 创建对象的能力。这会削弱 standards/09 中“应用账号无 DDL”的目标，只能算维护窗口内的明确风险接受，不应作为默认优化方案。

更不建议的方案：

- 把 `sub2api_app` 提升为 superuser。
- 为 `sub2api_app` broad 授予 `CREATE` 后长期运行且不留风险记录。
- 在没有独立 migration 能力证据前再次强切 `DATABASE_USER=sub2api_app`。

## 4. 可选路径

推荐路径：推动或维护应用侧改造。

- 新增 `--migrate-only` 命令，只执行迁移后退出。
- 新增 `DISABLE_STARTUP_MIGRATIONS=true` 或等价配置，让常规业务进程启动时不执行 schema migration。
- 维护窗口中先用管理角色执行 migration，再用 `sub2api_app` 启动业务容器。

保守路径：继续风险接受。

- 生产继续使用当前 `sub2api` 数据库用户。
- 保持数据库不暴露公网、端口收敛、R2 备份、恢复演练、监控告警和镜像 digest 固定等补偿控制。
- 在状态快照和 standards/09 审计矩阵中明确记录该项不是遗漏，而是受应用迁移模型限制。

不推荐路径：直接给运行用户 DDL 权限。

- 只有在维护窗口、明确接受风险、准备回滚，并确认应用启动实际 SQL 后才可考虑。
- 即使临时授权，也应在迁移完成后撤回并复核启动模型。

## 5. 本次未做

- 未修改 `/opt/services/sub2api/compose.yml`。
- 未修改 `/opt/ops/services/sub2api/compose.yml`。
- 未修改 PostgreSQL role、schema、table、default privileges 或密码。
- 未重启、重建或停止任何生产容器。
- 未读取业务表数据。
- 未打印任何 `.env`、数据库密码、Redis 密码或私钥。

## 6. 当前状态

状态：C2g 能力分析完成；运行态不变；`sub2api` runtime 低权限切换继续作为风险接受/应用侧待配合项。

下一步：如果要继续推进该项，应优先在应用源码侧增加 migration-only 与 disable-startup-migration 能力，然后再安排维护窗口切换生产 runtime 用户。
