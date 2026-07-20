# LosAngeles standards09 C2f sub2api migration/runtime 拆分只读分析

日期：2026-07-06
范围：`sub2api`、`sub2api-postgres`
审计方式：只读容器元数据、Postgres 元数据和历史失败日志核对
审计临时文件：`/tmp/codex-sub2api-migration-runtime-audit.out`

## 1. 结论

本次没有修改运行配置、数据库权限、业务数据或容器状态。

只读分析确认：`sub2api` 不能直接从当前 superuser `sub2api` 强切到低权限角色 `sub2api_app`，精确原因是应用启动时会执行：

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
```

即使 `public.schema_migrations` 已存在，PostgreSQL 仍要求执行用户具备 `public` schema 的 `CREATE` 权限。当前 `sub2api_app` 只有业务表 DML 和 sequence 权限，没有 schema `CREATE`，所以 C2b 切换失败是符合预期的权限边界结果。

因此，`sub2api` 数据库最小权限治理不能再走“直接替换运行用户”的路径。正确方向是应用侧支持将 migration 阶段和 runtime 阶段拆开，或者明确支持关闭启动自动 migration。

## 2. 已核实事实

当前运行模型：

- `sub2api` 容器镜像：`weishaw/sub2api@sha256:b12017d69050ba83e2a3dfa1fd342c25720912937aee5043d5793c6cce0a459e`
- 运行配置来源：`/opt/services/sub2api/compose.yml`
- 当前数据库连接用户：`DATABASE_USER=sub2api`
- `sub2api` 容器当前健康：`healthy`

Postgres 元数据：

- 数据库：`sub2api`
- 数据库 owner：`sub2api`
- `public` schema owner：`pg_database_owner`
- `sub2api_app` 对 `public` schema：`USAGE=true`
- `sub2api_app` 对 `public` schema：`CREATE=false`
- `public.schema_migrations` 已存在，owner 为 `sub2api`
- `sub2api_app` 已有 `schema_migrations` 表的 `SELECT`、`INSERT`、`UPDATE`、`DELETE`
- `sub2api_app` 已有当前 74 张业务表的 DML 权限和 60 个 sequence 的使用权限

角色权限位：

| 角色 | superuser | createdb | createrole | replication | bypassrls |
| --- | --- | --- | --- | --- | --- |
| `sub2api` | true | true | true | true | true |
| `sub2api_app` | false | false | false | false | false |

历史失败信号与本次元数据吻合：

- 应用日志：`Failed to initialize application: create schema_migrations: pq: permission denied for schema public`
- Postgres 日志：`CREATE TABLE IF NOT EXISTS schema_migrations (` 被拒绝，错误为 `permission denied for schema public`

## 3. 判断

`sub2api_app` 当前不是“权限漏配”，而是按最小权限目标刻意没有 DDL 能力。

如果为了让启动 migration 通过而直接授予 `public` schema `CREATE`，会让 `sub2api_app` 可以在 `public` schema 下创建任意对象，削弱“运行用户无 DDL”的目标。该方案只能作为维护窗口内的有意识风险接受，不能作为默认修复。

更不建议：

- 把 `sub2api_app` 提升为 superuser。
- 把 `sub2api` 当前 superuser 密码复用给应用低权限路径。
- 在没有应用兼容性证据前直接修改 `compose.yml` 并强制重建。

## 4. 推荐后续路径

优先级从高到低：

1. 确认 `sub2api` 是否支持关闭启动自动 migration，或是否提供独立 migration 命令。
2. 如果支持，采用双阶段模型：
   - migration 阶段：维护窗口内使用管理角色 `sub2api` 执行 schema 变更。
   - runtime 阶段：业务容器使用低权限角色 `sub2api_app`。
3. 如果不支持，继续将 `sub2api` 使用 superuser 作为风险接受项，并依赖公网端口收敛、数据库不暴露公网、备份恢复和监控告警降低风险。
4. 如必须临时授权 DDL，应先列出应用启动实际执行的全部 SQL，再逐条设计最小授权和回滚方案；不建议直接授予 broad `CREATE`。

## 5. 旁支发现

审计 Postgres 日志时发现 `postgres-exporter-sub2api` 持续产生：

```text
ERROR: column "checkpoints_timed" does not exist
STATEMENT: SELECT
```

这不是 `sub2api_app` 权限切换失败的原因。它更像是 Postgres exporter 查询与当前 PostgreSQL 18 指标视图之间的兼容性问题。后续应单独处理，避免日志噪声和部分 exporter collector 失真。

## 6. 本次未做

- 未读取业务表数据。
- 未打印数据库密码、Redis 密码或 `.env` 内容。
- 未修改 `compose.yml`。
- 未修改 Postgres role、schema、table 或 default privileges。
- 未重启或重建任何生产容器。

## 7. 当前状态

状态：只读分析完成；运行态不变；风险接受继续有效。

下一步：先确认应用是否支持独立 migration 或关闭启动 migration；确认前不要再次直接强切 `DATABASE_USER=sub2api_app`。
