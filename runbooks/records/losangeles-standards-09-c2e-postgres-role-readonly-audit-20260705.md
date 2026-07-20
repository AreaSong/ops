# LosAngeles standards09 C2e Postgres 角色权限只读复核

日期：2026-07-05  
范围：`account-vault-postgres-1`、`sub2api-postgres`、业务容器数据库连接用户  
变更类型：只读审计；未修改数据库、未重启容器、未读取业务表数据、未输出密码。

## 结论

状态：完成。

结论：

- `account-vault` 已使用低权限应用角色 `account_vault_app` 运行。
- `sub2api` 当前仍使用初始化/管理角色 `sub2api` 运行，该角色为 superuser。
- `sub2api_app` 低权限角色已存在，且具备现有 74 张业务表的 DML 权限，但此前 C2b 切换失败，原因是应用启动流程会执行 migration / DDL。
- 当前不建议再次直接强切 `sub2api` 到 `sub2api_app`。后续应先拆分 migration 阶段和 runtime 阶段，或确认应用支持关闭启动自动 migration。

## 审计方式

使用容器内本地连接执行元数据查询：

- 角色权限位：`rolsuper`、`rolcreatedb`、`rolcreaterole`、`rolreplication`、`rolbypassrls`、`rolcanlogin`
- 数据库 owner 名称
- 非系统 schema owner 名称
- 业务容器数据库连接变量的脱敏解析结果
- app 角色 membership、database privilege、schema privilege、table privilege count

本次未输出：

- 数据库密码
- 连接串完整内容
- 表数据
- `.env` 文件内容

## account-vault 结果

业务容器连接脱敏结果：

- `DATABASE_URL` 用户：`account_vault_app`
- host：`postgres:5432`
- db：`accountvault`

角色结果：

- `account_user`：superuser / createdb / createrole / replication / bypassrls 均为 `true`，作为初始化/管理角色存在。
- `account_vault_app`：superuser / createdb / createrole / replication / bypassrls 均为 `false`，可登录。
- `account_vault_app` 不继承 `account_user`。

权限结果：

- database：`CONNECT=true`、`CREATE=false`、`TEMPORARY=true`
- schema `public`：`USAGE=true`、`CREATE=false`
- table privilege count：
  - `SELECT`：6
  - `INSERT`：6
  - `UPDATE`：6
  - `DELETE`：6

判断：

- `account-vault` 数据库运行用户已符合当前最小权限目标。

## sub2api 结果

业务容器连接脱敏结果：

- `DATABASE_USER=sub2api`
- host：`postgres`
- db：`sub2api`

角色结果：

- `sub2api`：superuser / createdb / createrole / replication / bypassrls 均为 `true`，当前仍被业务容器使用。
- `sub2api_app`：superuser / createdb / createrole / replication / bypassrls 均为 `false`，可登录。
- `sub2api_app` 不继承 `sub2api`。

权限结果：

- database：`CONNECT=true`、`CREATE=false`、`TEMPORARY=true`
- schema `public`：`USAGE=true`、`CREATE=false`
- table privilege count：
  - `SELECT`：74
  - `INSERT`：74
  - `UPDATE`：74
  - `DELETE`：74

判断：

- `sub2api_app` 当前 DML 权限完整，但不具备 schema / DDL 权限。
- 结合 `runbooks/records/losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md`，`sub2api` 启动流程存在 migration / DDL 行为，直接切换到纯 DML 用户会导致应用 unhealthy。
- 因此 `sub2api` 当前仍是数据库权限治理的明确剩余风险项。

## 后续建议

推荐路线：

1. 不再直接强切 `sub2api` 到低权限用户。
2. 先研究 `sub2api` 是否支持关闭启动自动 migration，或将 migration 拆成独立维护命令。
3. 理想模型：
   - migration 阶段使用管理角色 `sub2api`
   - runtime 阶段使用低权限角色 `sub2api_app`
4. 如果必须给 `sub2api_app` 补 DDL 权限，应逐条基于失败日志里的具体 SQL 最小化授权，不建议直接授予 broad DDL 或 superuser。
5. 在应用侧能力不明确前，将 `sub2api` 当前 superuser 运行作为风险接受项，并依赖：
   - Postgres 未暴露公网
   - Docker 网络隔离
   - R2 / 本机备份与恢复演练
   - 监控与日志告警

## 回滚

本次为只读审计，无运行配置变更，无需回滚。
