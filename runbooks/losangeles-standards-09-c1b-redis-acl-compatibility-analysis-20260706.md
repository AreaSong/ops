# LosAngeles standards/09 C1b Redis ACL / 高危命令兼容性分析

日期：2026-07-06
范围：`sub2api-redis`、`sub2api`、`redis-exporter-sub2api`
审计方式：Redis 运行态只读核对 + `sub2api` / `redis_exporter` 源码兼容性分析
运行态影响：无。未修改 Redis ACL、未修改 compose、未重启容器、未读取业务 key/value。

## 1. 结论

本次只读分析确认：Redis 高危命令收紧可以继续推进，但不能使用简单粗暴的 `-@dangerous` 一刀切。

原因：Redis 8 的 `@dangerous` 类别不只包含 `FLUSHALL`、`FLUSHDB`、`SHUTDOWN`、`DEBUG`、`CONFIG SET` 这类破坏性命令，也包含 `INFO`、`CONFIG GET`、`SLOWLOG GET`、`LATENCY LATEST/HISTOGRAM`、`CLIENT LIST`、`KEYS` 等监控或只读诊断命令。当前 `redis_exporter` 默认会尝试使用 `CONFIG GET`、`INFO`、`CLIENT SETNAME` 等命令，直接 `-@dangerous` 会影响监控完整性。

短期推荐策略：

- 不做 `-@dangerous` 类别级禁用。
- 在维护窗口内优先精确禁用明确破坏性 / 高风险管理命令。
- 暂不拆分 Redis ACL 用户，因为当前 `sub2api` Redis 配置仅支持 `host/port/password/db`，未发现 username 配置项。

## 2. 已核实事实

### 2.1 当前 Redis ACL

当前 Redis 仍使用默认用户模型：

- `default` 用户启用。
- 命令权限为 `+@all`。
- key 范围为 `~*`。
- channel 范围为 `&*`。

说明：本报告不记录 Redis 密码、密码 hash 或 Redis URL。

### 2.2 Redis 运行态命令分类

只读核对 `ACL CAT dangerous` 后确认，`@dangerous` 包含以下类型命令：

- 破坏性 / 数据清空：`flushall`、`flushdb`、`swapdb`、`restore`。
- 服务控制：`shutdown`、`debug`、`monitor`、`client|kill`、`client|pause`。
- 配置 / 持久化 / 复制：`config|get`、`config|set`、`config|rewrite`、`save`、`bgsave`、`bgrewriteaof`、`replicaof`、`slaveof`。
- ACL / module / cluster 管理：`acl|*`、`module|*`、`cluster|*`。
- 监控 / 诊断：`info`、`slowlog|get`、`latency|latest`、`latency|histogram`、`client|list`。
- key 扫描类：`keys`、`sort`。

因此，`-@dangerous` 会同时禁掉监控所需命令，不适合作为第一阶段策略。

### 2.3 `sub2api` 源码 Redis 命令需求

基于上游源码 `b650bdd68d25bad3e502b2e34efe775555da2eba` 只读分析，`sub2api` 未发现直接调用：

- `FLUSHALL`
- `FLUSHDB`
- `CONFIG GET` / `CONFIG SET`
- `SHUTDOWN`
- `KEYS`

但 `sub2api` 明确依赖以下能力：

- 基础读写：`GET`、`SET`、`DEL`、`EXPIRE`、`TTL`、`EXISTS`、`MGET`。
- 计数与限流：`INCR`、`DECR`。
- Hash / Set / Sorted Set：`HSET`、`HGETALL`、`SADD`、`SMEMBERS`、`SISMEMBER`、`ZADD`、`ZRANGE`、`ZREM`、`ZREMRANGEBYSCORE`。
- 锁与并发控制：`SETNX`、`EVALSHA` / `EVAL`、pipeline / transaction pipeline。
- 清理与枚举：`SCAN`。
- 进程间通知：`PUBLISH`、`SUBSCRIBE`。

应用中存在多处 `redis.NewScript(...)` Lua 脚本，覆盖限流、并发槽位、配额、分布式锁释放、用户消息队列等逻辑。因此不能禁用 `EVAL`、`EVALSHA` 或 `SCRIPT LOAD`。

### 2.4 `redis_exporter` 兼容性

当前 `redis_exporter` 版本为 `v1.62.0`。

源码核对确认：

- 默认 `config-command` 是 `CONFIG`。
- 默认会尝试 `CONFIG GET *`，用于配置指标和 DB 数量推断。
- 会使用 `INFO` 获取基础指标。
- 默认可能执行 `CLIENT SETNAME redis_exporter`。
- 若开启 key/stream 检查，会使用 `SCAN`、`MEMORY USAGE`、stream 相关命令。

当前生产 env 只记录到 `REDIS_ADDR`，未发现显式 `REDIS_EXPORTER_CONFIG_COMMAND=-`。因此在未调整 exporter 配置前，不应直接禁用 `CONFIG GET`。

## 3. 推荐维护窗口方案

### 阶段 1：精确禁用明显破坏性命令

建议优先考虑精确禁用以下命令，而不是禁用整个 `@dangerous` 类别：

```text
-flushall
-flushdb
-shutdown
-debug
-monitor
-client|kill
-client|pause
-config|set
-config|rewrite
-save
-bgsave
-bgrewriteaof
-replicaof
-slaveof
-module|load
-module|loadex
-module|unload
-acl|setuser
-acl|deluser
-acl|load
```

说明：

- 保留 `INFO`、`CONFIG GET`、`LATENCY`、`SLOWLOG GET`，避免影响当前 Redis exporter 指标。
- 保留 `EVAL` / `EVALSHA` / `SCRIPT LOAD`，因为 `sub2api` 业务逻辑依赖 Lua。
- 保留 `SCAN`，因为 `sub2api` 清理逻辑和 exporter 可选 key 检查都会使用它。
- 是否禁用 `KEYS` 可以在阶段 1 一并考虑；源码未发现 `sub2api` 使用 `KEYS`，但应先确认人工排障脚本没有依赖。

### 阶段 2：调整 exporter 并进一步收紧

如果希望进一步禁用 `CONFIG GET`：

1. 先给 `redis_exporter` 设置 `REDIS_EXPORTER_CONFIG_COMMAND=-` 或等价启动参数。
2. 验证 Prometheus `redis` target 仍然 up。
3. 验证 `LosAngeles Datastores` Dashboard 的关键 Redis 面板仍有数据。
4. 再考虑禁用 `config|get`。

该阶段可能减少 Redis 配置类指标，应明确接受监控信息变少。

### 阶段 3：分用户 ACL（需要应用侧支持）

理想模型是拆分 Redis 用户：

- `sub2api_app`：只允许业务所需读写、Lua、SCAN、PUB/SUB、hash/set/zset 等命令。
- `redis_exporter`：只允许 `INFO`、`CONFIG GET`、`CLIENT SETNAME`、`LATENCY`、`SLOWLOG GET`、必要的 `SCAN/MEMORY USAGE`。
- `redis_admin`：仅维护窗口使用，用于 ACL、CONFIG、SAVE、故障处置。

但当前 `sub2api` Redis 配置结构未发现 username 字段，只有 `host/port/password/db`。因此分用户 ACL 需要应用侧新增 Redis username 支持，或确认 go-redis 连接串路径可被当前部署安全使用。

## 4. 维护窗口验证清单

实施任何 Redis ACL 收紧前，建议先准备回滚命令和验证清单。

变更前：

- 记录当前 compose 和 Redis ACL 脱敏摘要。
- 确认最新 Redis / Postgres / configs / volumes 备份存在。
- 确认 Grafana / Prometheus / Alertmanager 正常。

变更后立即验证：

- `sub2api` 容器 running / healthy。
- `https://cpa.areasong.top/health` 返回正常。
- Redis healthcheck 正常。
- `redis-exporter-sub2api` running。
- Prometheus `up{job="redis"}` 为 `1`。
- Grafana `LosAngeles Datastores` Redis 面板仍有数据。
- `sub2api` 日志无 Redis `NOPERM`、`ERR unknown command` 或 Lua 脚本失败。

回滚：

- 立即恢复 ACL 为变更前状态。
- 如 Redis 重启策略导致连接抖动，验证 `sub2api` 自动恢复。
- 记录本次命令、影响时间和验证结果。

## 5. 本次未做

- 未执行 `ACL SETUSER`。
- 未执行 `CONFIG SET`。
- 未修改 `/opt/services/sub2api/compose.yml`。
- 未修改 `/opt/ops/services/sub2api/compose.yml`。
- 未修改 `redis_exporter` 配置。
- 未重启 Redis、`sub2api` 或监控容器。
- 未读取 Redis key/value 业务数据。
- 未记录 Redis 密码、密码 hash 或连接串。

## 6. 当前状态

状态：C1b Redis ACL / 高危命令兼容性分析完成；运行态不变；ACL 收紧待维护窗口明确确认后实施。

下一步：如继续推进 Redis 收紧，建议先做阶段 1 的精确命令禁用，并保留 `INFO`、`CONFIG GET`、`EVAL`、`SCAN`、`PUB/SUB`。如果目标是更严格的分用户 ACL，则需要先补应用侧 Redis username 支持。
