# LosAngeles standards/09 C1 补充：Redis 认证闭环

更新时间：2026-07-05  
服务器：LosAngeles  
范围：`sub2api-redis` 与 `sub2api`  
风险级别：中；已重建 Redis 与应用容器

## 1. 背景

C1 初次收敛后，Redis 的 AOF 与 `maxmemory` 已生效，但验证输出仍出现：

```text
AUTH failed: ERR AUTH <password> called without any password configured for the default user
```

这说明应用侧存在 Redis 密码变量，但 Redis 服务端实际未启用 `requirepass`。进一步检查发现 `.env` 中 `REDIS_PASSWORD` 为空，导致 Redis 容器中的 `REDISCLI_AUTH` 为空，`--requirepass` 实际没有形成有效密码。

## 2. 本批次完成项

### 2.1 变更前备份

已备份：

- `/opt/services/sub2api/.env`
- `/opt/services/sub2api/compose.yml`

备份目录：

- `/root/ops-change-backups/standards09-c1-redis-auth-<timestamp>/`

### 2.2 启用 Redis 密码

已在服务器本地生成随机 Redis 密码，写入：

- `/opt/services/sub2api/.env` 的 `REDIS_PASSWORD`

说明：

- 密码未输出到终端、runbook 或 Git。
- `.env` 权限保持为 `0600`。
- `sub2api` 与 `sub2api-redis` 均从同一变量链路读取该密码。

### 2.3 重建与验证

已执行：

```bash
cd /opt/services/sub2api
sudo docker compose -f compose.yml up -d redis sub2api
```

已验证：

- 认证后的 `redis-cli PING` 返回 `PONG`。
- 未认证请求被拒绝。
- `requirepass` 已存在。
- `appendonly=yes`。
- `maxmemory` 不低于 512 MiB。
- `maxmemory-policy=noeviction`。
- `sub2api` 恢复 healthy/running。

## 3. 回滚方式

如需回滚 `.env` 与 compose：

```bash
sudo cp /root/ops-change-backups/standards09-c1-redis-auth-<timestamp>/env.before /opt/services/sub2api/.env
sudo cp /root/ops-change-backups/standards09-c1-redis-auth-<timestamp>/compose.yml.before /opt/services/sub2api/compose.yml
cd /opt/services/sub2api
sudo docker compose -f compose.yml up -d redis sub2api
```

## 4. 当前结论

Redis C1 已闭环：

- 持久化：AOF enabled。
- 内存边界：512 MiB + noeviction。
- 认证：requirepass enabled，未认证请求被拒绝。
- 业务：sub2api 容器健康。

状态：完成。
