# LosAngeles standards/09 C1d Redis ACL 持久化实施记录

更新时间：2026-07-06 02:15 UTC
服务器：`LosAngeles`
范围：`sub2api-redis`、`/opt/services/sub2api/compose.yml`、`/opt/ops/services/sub2api/compose.yml`

## 1. 结论

Redis ACL 阶段 1 已完成持久化。

当前状态：

- Redis 启动参数已增加 `--aclfile /data/users.acl`。
- 宿主机 ACL 文件路径：`/var/lib/sub2api/redis_data/users.acl`。
- 运行副本：`/opt/services/sub2api/compose.yml`。
- Git 受控副本：`/opt/ops/services/sub2api/compose.yml`。
- 两份 compose 已保持一致。

ACL 文件包含 Redis 当前 default 用户规则和密码 hash，属于敏感文件；不进入 Git，不在文档和终端输出中展示内容。

## 2. 实施内容

已完成：

1. 从当前 Redis `ACL LIST` 生成 `users.acl`。
2. 将 `users.acl` 安装为 root-only 文件。
3. 修改 Redis 启动命令，加入：

```bash
--aclfile /data/users.acl
```

4. 使用生产 compose 目录重建 `redis` 服务：

```bash
cd /opt/services/sub2api
sudo docker compose -f compose.yml up -d redis
```

5. Redis 启动后执行 `ACL SAVE`，确认当前 ACL 写入配置的 aclfile。

## 3. 保持的 ACL 策略

仍沿用 C1c 阶段 1 精确收紧策略。

已禁用：

```text
FLUSHALL
FLUSHDB
SHUTDOWN
DEBUG
MONITOR
KEYS
CLIENT KILL
CLIENT PAUSE
CONFIG SET
CONFIG REWRITE
REPLICAOF
SLAVEOF
MODULE LOAD
MODULE LOADEX
MODULE UNLOAD
```

保留：

```text
PING
INFO
CONFIG GET
CLIENT LIST
BGSAVE
SAVE
BGREWRITEAOF
SCAN
EVAL
SCRIPT LOAD
PUB/SUB
ACL SETUSER
```

## 4. 验证结果

已验证：

- Redis 容器重建后状态为 `running healthy`。
- `CONFIG GET aclfile` 返回 `/data/users.acl`。
- ACL deny/allow dry-run 验证通过。
- Redis `PING` 正常。
- Redis 备份脚本成功生成新备份包：`redis-20260706-020651.tar.gz`。
- `sub2api` 容器健康。
- `redis-exporter-sub2api` 运行正常。
- `https://cpa.areasong.top/health` 返回 HTTP 200。
- `sub2api` 和 `redis-exporter-sub2api` 近期日志未发现 Redis 权限拒绝错误。

## 5. 回滚

回滚备份目录：

```text
/root/ops-change-backups/redis-acl-persist-20260706-020640
```

如需回滚：

1. 恢复备份的 compose 文件到 `/opt/services/sub2api/compose.yml` 和 `/opt/ops/services/sub2api/compose.yml`。
2. 移走 `/var/lib/sub2api/redis_data/users.acl`。
3. 重建 Redis 服务：

```bash
cd /opt/services/sub2api
sudo docker compose -f compose.yml up -d redis
```

4. 如仍需保持阶段 1 运行态收紧，可重新执行 C1c deny list。

## 6. 后续项

- 分用户 ACL 仍需等待 `sub2api` 支持 Redis username 后再做。
- C1e 已将 `users.acl` 纳入 Redis 备份包；后续恢复演练按 `dump.rdb` 与 `users.acl` 同步恢复验证。

留痕：`runbooks/losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md`
