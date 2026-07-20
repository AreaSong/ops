# LosAngeles standards/09 C2c：account-vault Postgres 低权限应用用户预创建

更新时间：2026-07-05  
服务器：LosAngeles  
范围：`account-vault-postgres-1`  
风险级别：中；已修改数据库角色和授权，但未切换业务连接串、未重启应用

## 1. 目标

为 `account-vault` 预创建低权限应用运行用户，作为后续从高权限初始化用户迁移到最小权限模型的准备步骤。

本批次不切换 `account-vault-web-1` 当前连接用户，因此业务仍继续使用原连接串运行。

## 2. 本批次完成项

### 2.1 凭据生成与保存

已在服务器本地生成随机密码，并保存到 root-only 文件：

- `/etc/ops-secrets/postgres/account_vault_app.env`

文件权限：

- `0600 root:root`

说明：

- 密码未输出到终端、runbook 或 Git。
- 该文件用于后续 C2d 切换应用连接串时读取。

### 2.2 数据库角色

已创建或更新角色：

- `account_vault_app`

角色属性：

- `LOGIN`
- `NOSUPERUSER`
- `NOCREATEDB`
- `NOCREATEROLE`
- `NOREPLICATION`
- `NOBYPASSRLS`

### 2.3 授权范围

已授权：

```sql
GRANT CONNECT ON DATABASE accountvault TO account_vault_app;
GRANT USAGE ON SCHEMA public TO account_vault_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO account_vault_app;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO account_vault_app;
ALTER DEFAULT PRIVILEGES FOR ROLE account_user IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO account_vault_app;
ALTER DEFAULT PRIVILEGES FOR ROLE account_user IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO account_vault_app;
```

## 3. 验证结果

已验证：

- `account_vault_app` 可连接 `accountvault` 数据库。
- `account_vault_app` 不具备 superuser / createdb / createrole / replication / bypassrls 权限。
- 现有 public tables 的 CRUD 授权完整，缺失数为 0。
- 现有 public sequences 的 usage/select/update 授权完整，缺失数为 0。
- 使用低权限用户对代表性业务表执行 SELECT 成功。
- 临时表读写探针成功。
- `account-vault-web-1` 当前业务连接串未切换到 `account_vault_app`。
- `account-vault-web-1` 容器健康状态未受影响。

验证输出留存在服务器临时目录：

- `/tmp/losangeles-09-c2c-account-vault-pg-app-role-<timestamp>/`

## 4. 回滚方式

如果需要移除预创建用户：

```bash
sudo docker exec -it account-vault-postgres-1 sh
psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"
```

然后执行：

```sql
REVOKE SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public FROM account_vault_app;
REVOKE USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public FROM account_vault_app;
REVOKE USAGE ON SCHEMA public FROM account_vault_app;
REVOKE CONNECT ON DATABASE accountvault FROM account_vault_app;
DROP ROLE IF EXISTS account_vault_app;
```

如需回滚凭据文件，可删除：

```bash
sudo rm -f /etc/ops-secrets/postgres/account_vault_app.env
```

## 5. 下一步建议

下一步为 C2d：在维护窗口切换 `account-vault-web-1` 应用连接串到 `account_vault_app`。

建议步骤：

1. 备份 `/opt/services/account-vault` 下实际使用的 `.env` / compose。
2. 从 `/etc/ops-secrets/postgres/account_vault_app.env` 读取新用户和密码。
3. 修改 `DATABASE_URL` 或对应数据库连接变量。
4. 重建 `account-vault-web-1` 应用容器。
5. 验证健康检查、登录、核心页面、Postgres 日志无权限错误。
6. 如有权限缺口，恢复原配置并重建应用。

状态：完成。
