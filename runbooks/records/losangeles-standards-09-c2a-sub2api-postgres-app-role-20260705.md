# LosAngeles standards/09 C2a：sub2api Postgres 低权限应用用户预创建

更新时间：2026-07-05  
服务器：LosAngeles  
范围：`sub2api-postgres`  
风险级别：中；已修改数据库角色和授权，但未切换业务连接串、未重启应用

## 1. 目标

为 `sub2api` 预创建低权限应用运行用户，作为后续从高权限初始化用户迁移到最小权限模型的准备步骤。

本批次不切换 `sub2api` 当前连接用户，因此业务仍继续使用原 `sub2api` 用户运行。

## 2. 本批次完成项

### 2.1 凭据生成与保存

已在服务器本地生成随机密码，并保存到 root-only 文件：

- `/etc/ops-secrets/postgres/sub2api_app.env`

文件权限：

- `0600 root:root`

说明：

- 密码未输出到终端、runbook 或 Git。
- 该文件用于后续 C2b 切换应用连接串时读取。

### 2.2 数据库角色

已创建或更新角色：

- `sub2api_app`

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
GRANT CONNECT ON DATABASE sub2api TO sub2api_app;
GRANT USAGE ON SCHEMA public TO sub2api_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO sub2api_app;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO sub2api_app;
ALTER DEFAULT PRIVILEGES FOR ROLE sub2api IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO sub2api_app;
ALTER DEFAULT PRIVILEGES FOR ROLE sub2api IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO sub2api_app;
```

## 3. 验证结果

已验证：

- `sub2api_app` 可连接 `sub2api` 数据库。
- `sub2api_app` 不具备 superuser / createdb / createrole / replication / bypassrls 权限。
- 现有 public tables 的 CRUD 授权完整，缺失数为 0。
- 现有 public sequences 的 usage/select/update 授权完整，缺失数为 0。
- 临时表读写探针成功。
- `sub2api` 当前业务连接用户未切换到 `sub2api_app`。
- `sub2api` 容器健康状态未受影响。

验证输出留存在服务器临时目录：

- `/tmp/losangeles-09-c2a-sub2api-pg-app-role-verify-<timestamp>/`

## 4. 回滚方式

如果需要移除预创建用户：

```bash
sudo docker exec -it sub2api-postgres sh
psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"
```

然后执行：

```sql
REVOKE SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public FROM sub2api_app;
REVOKE USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public FROM sub2api_app;
REVOKE USAGE ON SCHEMA public FROM sub2api_app;
REVOKE CONNECT ON DATABASE sub2api FROM sub2api_app;
DROP ROLE IF EXISTS sub2api_app;
```

如需回滚凭据文件，可删除：

```bash
sudo rm -f /etc/ops-secrets/postgres/sub2api_app.env
```

## 5. 下一步建议

下一步为 C2b：在维护窗口切换 `sub2api` 应用连接串到 `sub2api_app`。

建议步骤：

1. 备份 `/opt/services/sub2api/.env`。
2. 从 `/etc/ops-secrets/postgres/sub2api_app.env` 读取新用户和密码。
3. 替换 `DATABASE_USER` / `DATABASE_PASSWORD`。
4. 重建 `sub2api` 应用容器。
5. 验证健康检查、登录、核心 API、后台任务、Postgres 日志无权限错误。
6. 如有权限缺口，恢复原 `.env` 并重建应用。

状态：完成。
