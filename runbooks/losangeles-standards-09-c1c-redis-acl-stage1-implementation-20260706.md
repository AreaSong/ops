# LosAngeles standards/09 C1c Redis ACL 阶段 1 实施记录

更新时间：2026-07-06 01:50 UTC
服务器：`LosAngeles`
范围：`sub2api-redis`、Redis 本机备份脚本、相关 runbook

## 1. 结论

Redis ACL 阶段 1 已实施并通过验证。

当前 Redis `default` 用户仍保留业务所需能力，但已精确禁用明确破坏性或高风险管理命令：

```text
-flushall
-flushdb
-shutdown
-debug
-monitor
-keys
-client|kill
-client|pause
-config|set
-config|rewrite
-replicaof
-slaveof
-module|load
-module|loadex
-module|unload
```

本次没有使用 `-@dangerous`，也没有拆分 Redis 用户。

关键原因：Redis 8 的 `@dangerous` 包含 `INFO`、`CONFIG GET`、`CLIENT LIST`、`SLOWLOG`、`LATENCY` 等监控/诊断命令；`sub2api` 当前 Redis 配置未发现 username 字段，短期不适合分用户 ACL。

## 2. 实施前预检

已确认：

- `sub2api-redis` 当前 ACL 原始状态为 `default +@all`，未在文档中记录密码或 hash。
- `sub2api` 依赖 Lua、`SCAN`、`PUB/SUB`、hash/set/zset、pipeline 等能力。
- `redis-exporter-sub2api` 需要 `INFO`、`CONFIG GET`、`CLIENT LIST` 等诊断能力。
- Redis 当前 `appendonly=yes`，数据目录为 `/data`，宿主机目录为 `/var/lib/sub2api/redis_data`。
- Redis 当前未配置 `aclfile`，因此本次 ACL 变更为运行态收紧；Redis 容器重启后需要按后续持久化方案重放。

## 3. 备份脚本修复

实施 ACL 验证时发现旧 Redis 备份脚本存在在线 tar AOF 的不稳定问题：

```text
tar: redis_data/appendonlydir/appendonly.aof.2.incr.aof: file changed as we read it
```

已修复 `/opt/ops/scripts/backup/backup-redis.sh`：

- 触发 `BGSAVE`。
- 等待 `rdb_bgsave_in_progress=0` 且 `rdb_last_bgsave_status=ok`。
- 打包稳定的 `dump.rdb` 快照和 `metadata.txt`。
- 保持产物命名为 `redis-*.tar.gz`，兼容现有 backup freshness 指标和 R2 同步。

验证产物：

```text
/var/backups/ops/redis/redis-20260706-014323.tar.gz
```

包内结构：

```text
metadata.txt
redis_data/dump.rdb
```

## 4. 保留能力

为避免影响业务、监控和备份，本次明确保留：

- `INFO`
- `CONFIG GET`
- `CLIENT LIST`
- `BGSAVE`
- `SAVE`
- `BGREWRITEAOF`
- `SCAN`
- `EVAL`
- `SCRIPT LOAD`
- `PUB/SUB`
- 常规读写、hash、set、zset、pipeline 能力
- `ACL SETUSER` 回滚通道

## 5. 验证结果

已完成验证：

- Redis `PING` 返回 `PONG`。
- ACL deny token 已出现在 `ACL LIST`。
- `ACL DRYRUN` 确认 `FLUSHALL`、`FLUSHDB`、`KEYS`、`MONITOR`、`CONFIG SET/REWRITE`、`CLIENT KILL/PAUSE`、`REPLICAOF` 被拒绝。
- `ACL DRYRUN` 确认 `PING`、`INFO`、`CONFIG GET`、`CLIENT LIST`、`BGSAVE`、`SAVE`、`BGREWRITEAOF`、`SCAN`、`EVAL`、`SCRIPT LOAD` 仍允许。
- Redis 备份脚本实际执行成功。
- `sub2api`、`sub2api-redis`、`redis-exporter-sub2api` 均保持运行。
- `https://cpa.areasong.top/health` 返回 HTTP 200。
- `sub2api` 和 `redis-exporter-sub2api` 近期日志未发现 Redis 权限拒绝错误。

## 6. 回滚

如果后续出现 Redis 权限误伤，可在维护终端执行：

```bash
sudo docker exec sub2api-redis redis-cli ACL SETUSER default +@all
```

由于当前未配置 `aclfile`，该回滚同样是运行态变更。

## 7. 后续项

- 为 Redis 增加受控 `aclfile`，使 ACL 收紧在容器重启后仍持久生效；该项需要单独评估和维护窗口。
- 等 `sub2api` 支持 Redis username 后，再拆分 `sub2api_app`、`redis_exporter`、`redis_admin` 等专用 ACL 用户。
- 补充 Redis RDB 快照恢复步骤到后续恢复演练或恢复 runbook。
