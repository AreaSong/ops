# LosAngeles standards/09 C2：Postgres 权限拆分只读审计与方案

更新时间：2026-07-05  
服务器：LosAngeles  
范围：`sub2api-postgres`、`account-vault-postgres-1`  
风险级别：只读审计；未修改数据库、未修改 `.env`、未重启应用

## 1. 审计结论摘要

- sub2api-postgres: login_roles=none
- sub2api-postgres: high_privilege_login_roles=none
- sub2api-postgres: audit_file=/tmp/losangeles-09-c2-postgres-audit-20260705T040201Z/sub2api-postgres-audit.txt
- account-vault-postgres-1: login_roles=none
- account-vault-postgres-1: high_privilege_login_roles=none
- account-vault-postgres-1: audit_file=/tmp/losangeles-09-c2-postgres-audit-20260705T040201Z/account-vault-postgres-1-audit.txt
- app_env_audit=/tmp/losangeles-09-c2-postgres-audit-20260705T040201Z/app-env-audit.txt

## 2. 已检查内容

本次只读检查覆盖：

- Postgres 登录角色与权限位：`superuser`、`createdb`、`createrole`、`replication`、`bypassrls`、`canlogin`。
- 数据库 owner 与数据库大小。
- 非系统 schema owner。
- 非系统 table owner。
- sequence 列表。
- default privileges。
- 应用容器中数据库相关环境变量的变量名和长度，不记录值。

原始检查结果留存在服务器临时目录：

- `/tmp/losangeles-09-c2-postgres-audit-<timestamp>/sub2api-postgres-audit.txt`
- `/tmp/losangeles-09-c2-postgres-audit-<timestamp>/account-vault-postgres-1-audit.txt`
- `/tmp/losangeles-09-c2-postgres-audit-<timestamp>/app-env-audit.txt`

## 3. 当前判断

### 3.1 当前业务初始化用户权限偏高

审计显示当前可登录业务初始化用户具备高权限信号。生产上不建议应用长期使用这类账号直接运行，因为一旦应用被利用，攻击面会扩大到数据库管理能力。

需要拆分为：

- 管理/迁移用户：保留高权限，只用于初始化、迁移、人工维护、备份恢复演练。
- 应用运行用户：低权限，只允许连接指定数据库，访问指定 schema 下业务表和 sequence。

### 3.2 不能直接降权现有用户

不建议直接把当前 `POSTGRES_USER` 降权，原因：

- 容器初始化、备份、恢复、迁移脚本可能依赖当前高权限用户。
- 应用可能在启动时执行自动迁移或初始化逻辑。
- 直接降权一旦遗漏权限，业务会在运行中报错，回滚成本高。

正确做法是新增低权限用户，验证后再切换应用连接串。

## 4. 建议的 C2 执行方案

### 4.1 sub2api

建议新增：

- `sub2api_app`：应用运行用户。
- 保留当前初始化/管理用户：只用于管理、迁移、备份恢复。

拟授权范围：

```sql
CREATE ROLE sub2api_app LOGIN PASSWORD '<server-generated-secret>';
GRANT CONNECT ON DATABASE sub2api TO sub2api_app;
GRANT USAGE ON SCHEMA public TO sub2api_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO sub2api_app;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO sub2api_app;
ALTER DEFAULT PRIVILEGES FOR ROLE sub2api IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO sub2api_app;
ALTER DEFAULT PRIVILEGES FOR ROLE sub2api IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO sub2api_app;
```

切换方式：

1. 备份 `/opt/services/sub2api/.env`。
2. 新增 `DATABASE_APP_USER` / `DATABASE_APP_PASSWORD` 或直接替换 `DATABASE_USER` / `DATABASE_PASSWORD`。
3. 重建 `sub2api` 应用容器。
4. 验证健康检查、登录、关键 API、后台任务、日志无权限错误。
5. 保留管理用户用于迁移和维护。

### 4.2 account-vault

建议新增：

- `account_vault_app`：应用运行用户。
- 保留当前初始化/管理用户：只用于管理、迁移、备份恢复。

拟授权范围：

```sql
CREATE ROLE account_vault_app LOGIN PASSWORD '<server-generated-secret>';
GRANT CONNECT ON DATABASE accountvault TO account_vault_app;
GRANT USAGE ON SCHEMA public TO account_vault_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO account_vault_app;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO account_vault_app;
ALTER DEFAULT PRIVILEGES FOR ROLE account_user IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO account_vault_app;
ALTER DEFAULT PRIVILEGES FOR ROLE account_user IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO account_vault_app;
```

切换方式：

1. 备份 `/opt/services/account-vault/.env` 或实际 compose 使用的环境文件。
2. 新增低权限用户密码。
3. 修改应用连接串为低权限用户。
4. 重建 `account-vault-web-1`。
5. 验证登录、读写、关键页面、日志无权限错误。

## 5. 风险与验证

### 5.1 主要风险

- 应用启动时执行 schema migration，低权限用户可能无法建表或改表。
- 应用可能使用 `CREATE EXTENSION`、`CREATE INDEX`、`TRUNCATE`、`LOCK` 等超出普通 CRUD 的操作。
- 某些后台任务可能访问非 public schema 或执行维护 SQL。

### 5.2 验证清单

切换低权限用户前后必须验证：

- 容器健康检查。
- 应用登录/核心 API。
- 数据写入和读取。
- 定时任务/后台任务日志。
- Postgres 日志中无 `permission denied`。
- 备份脚本仍使用管理用户，且备份成功。

## 6. 回滚思路

如果低权限用户切换后业务异常：

1. 恢复 `.env` 中原 `DATABASE_USER` / `DATABASE_PASSWORD`。
2. 重建对应应用容器。
3. 确认健康检查恢复。
4. 保留新建低权限用户但暂不使用，后续补齐缺失权限后再切换。

## 7. 下一步建议

建议下一步执行 C2a：只对 `sub2api` 创建低权限应用用户，但暂不切换连接串。完成后用该用户做只读/读写探针验证。

状态：审计与方案完成，未执行数据库变更。
