# LosAngeles standards/09 C2d：account-vault 切换到低权限数据库用户

更新时间：2026-07-05  
服务器：LosAngeles  
范围：`account-vault-web-1`、`account-vault-postgres-1`  
风险级别：中；已修改 `/opt/services/account-vault/.env` 和 `/opt/services/account-vault/compose.yml`，并强制重建 `account-vault-web-1`

## 1. 目标

将 `account-vault` 应用数据库连接从高权限初始化用户切换到 C2c 预创建的低权限应用用户：

- `account_vault_app`

## 2. 本批次完成项

### 2.1 变更前备份

已备份：

- `/opt/services/account-vault/.env`
- `/opt/services/account-vault/compose.yml`
- `/etc/account-vault/account-vault.env`

备份目录：

- `/root/ops-change-backups/standards09-c2d-account-vault-switch-fixed-<timestamp>/`

### 2.2 应用连接串切换

已从 root-only 凭据文件读取：

- `/etc/ops-secrets/postgres/account_vault_app.env`

并更新 `/opt/services/account-vault/.env`：

- `DATABASE_APP_USER=account_vault_app`
- `DATABASE_APP_PASSWORD=<redacted>`

同时修正 `/opt/services/account-vault/compose.yml` 的应用 `DATABASE_URL` 映射：

```yaml
- DATABASE_URL=postgresql://${DATABASE_APP_USER:-account_user}:${DATABASE_APP_PASSWORD}@postgres:5432/accountvault?schema=public
```

说明：

- `POSTGRES_USER` / `POSTGRES_PASSWORD` 继续保留为数据库初始化/管理用户。
- 应用运行用户与数据库管理用户已从 compose 层拆分。
- 数据库密码未输出到终端、runbook 或 Git。
- `.env` 权限保持为 `0600`。

### 2.3 应用容器强制重建

已执行：

```bash
cd /opt/services/account-vault
sudo docker compose -f compose.yml up -d --force-recreate --no-deps web
```

只强制重建应用容器，不重建 Postgres。

## 3. 验证结果

已验证：

- 目标低权限用户 `account_vault_app` 可登录 `accountvault` 数据库。
- Compose 渲染后的应用连接串用户名为 `account_vault_app`。
- `account-vault-web-1` 容器启动后状态为 running。
- 本机 HTTP 探测 `http://127.0.0.1:8392/` 返回 200。
- `account-vault-web-1` 运行时 `DATABASE_URL` 用户名为 `account_vault_app`。
- Prisma migration 检查显示没有待应用迁移。
- 切换后的近期应用日志和 Postgres 日志未发现真实权限错误。
- `account-vault-web-1`、`account-vault-postgres-1` 最终状态正常。

注意：Postgres 日志中存在 `FATAL: database "account_user" does not exist`，这是当前 Postgres healthcheck 使用 `pg_isready -U account_user` 时默认尝试连接同名数据库导致的已知噪声，不是本次低权限切换导致的权限失败。

验证输出留存在服务器临时目录：

- `/tmp/losangeles-09-c2d-account-vault-switch-fixed-<timestamp>/`

## 4. 自动回滚机制

执行脚本包含自动回滚：

- 如果 HTTP 探测失败、运行时连接串没有切换到 `account_vault_app`，或日志出现真实权限/Prisma 初始化错误，会恢复变更前 `.env` 和 `compose.yml`。
- 恢复后强制重建 `account-vault-web-1` 并等待 HTTP 200。

本次未触发回滚。

## 5. 后续建议

C2 数据库权限治理阶段当前状态：

- `account-vault` 已成功切换到低权限数据库用户。
- `sub2api` 已预创建低权限用户，但直接切换失败并已回滚，原因是启动 migration 需要 DDL 权限。

下一步建议进入 C3：容器资源限制与服务级 compose 基线。

状态：完成。
