# LosAngeles account-vault 源码与环境文件规范化迁移记录

日期：2026-07-03 17:01 BST
服务器：LosAngeles
类型：服务目录规范化 / build context 与 env_file 迁移

## 1. 结论

`account-vault` 已消除对 `/root/sorryiosSearch` 的 compose 依赖。

当前状态：

- compose 目录：`/opt/services/account-vault`
- build context：`/opt/services/account-vault/app`
- env_file：`/etc/account-vault/account-vault.env`
- project `.env`：`/opt/services/account-vault/.env`，指向 `/etc/account-vault/account-vault.env`，用于 Docker Compose 插值
- 旧目录 `/root/sorryiosSearch` 尚未删除，仅保留为短期回滚来源

## 2. 执行内容

已完成：

1. 创建迁移前手工备份：`/var/backups/ops/manual/account-vault-migration-20260703-170150`。
2. 执行 Postgres 备份，包含 `account-vault-postgres-1`。
3. 将 `/root/sorryiosSearch` 同步到 `/opt/services/account-vault/app`，排除 `.env`、`.git` 和 node_modules。
4. 将 `/root/sorryiosSearch/.env` 复制到 `/etc/account-vault/account-vault.env`，权限为 `0600 root:root`。
5. 将 `/opt/services/account-vault/.env` 指向 `/etc/account-vault/account-vault.env`。
6. 修改 `/opt/services/account-vault/compose.yml`：
   - `build` 改为 `/opt/services/account-vault/app`
   - `env_file` 改为 `/etc/account-vault/account-vault.env`
7. 执行 `docker compose config` 和 `docker compose build web`。
8. 使用 `docker compose up -d --no-deps --force-recreate web` 重建 web 容器。
9. 更新 `backup-configs.sh`，使配置备份覆盖 `/etc/account-vault`。

## 3. 验证结果

已验证：

- `docker compose config --no-interpolate` 通过。
- `docker compose config` 通过。
- `docker compose build web` 通过。
- `account-vault-web-1` 重建后 running。
- `account-vault-postgres-1` 保持 running / healthy。
- `http://127.0.0.1:8392/` 返回 200。
- `https://sorryiossearch.areasong.top/` 返回 200。
- Docker inspect 未发现 account-vault 容器存在 `/root/sorryiosSearch` bind mount。
- 配置备份重新生成成功。

## 4. 回滚方式

如需回滚：

1. 将 `/var/backups/ops/manual/account-vault-migration-20260703-170150/compose.yml.before` 复制回 `/opt/services/account-vault/compose.yml`。
2. 确认 `/root/sorryiosSearch` 和 `/root/sorryiosSearch/.env` 仍存在。
3. 执行：

```bash
cd /opt/services/account-vault
sudo docker compose -f compose.yml up -d --force-recreate web
```

4. 验证 `127.0.0.1:8392` 和 `https://sorryiossearch.areasong.top/`。

迁移日志：`/var/log/ops/account-vault-migration-20260703-170150.log`

## 5. 后续建议

- 观察 account-vault 至少一个备份周期。
- 确认无异常后，`/root/sorryiosSearch` 可按旧目录清理流程归档后删除。
- `/root/JadeAI` 已归档且未发现运行时引用，可单独删除。
