# LosAngeles standards/09 C1：Redis 持久化与内存边界收敛

更新时间：2026-07-05  
服务器：LosAngeles  
范围：`sub2api-redis`  
风险级别：中；已重建 Redis 容器，`sub2api` Redis 连接有短暂维护窗口

## 1. 背景

C0 审计发现运行中的 `sub2api-redis` 与 compose 文件存在状态不一致：

- compose 中已有 `--appendonly yes`，但运行态为 `appendonly=no` / `aof_enabled=0`。
- 运行态 `maxmemory=0`，未设置 Redis 内存上限。
- Redis 数据目录为 `/var/lib/sub2api/redis_data`，挂载到容器 `/data`。

进一步检查发现，原 compose 中 Redis command 使用多行 shell 字符串，但没有将参数与 `redis-server` 放在同一条 shell 命令中。结果容器实际运行的是裸 `redis-server`，后续 `--appendonly`、`--appendfsync` 等参数没有生效。

## 2. 本批次完成项

### 2.1 变更前备份

已执行：

- Redis `SAVE`。
- 备份 `/opt/services/sub2api/compose.yml`。
- 打包 `/var/lib/sub2api/redis_data`。

备份目录：

- `/root/ops-change-backups/standards09-c1-redis-<timestamp>/`
- `/root/ops-change-backups/standards09-c1-redis-fix-<timestamp>/`

### 2.2 Redis command 修正

已将 Redis command 固化为单条 `exec redis-server ...` 命令，确保参数真正传入 Redis 进程。

当前目标参数：

```bash
--save 60 1
--appendonly yes
--appendfsync everysec
--maxmemory 512mb
--maxmemory-policy noeviction
--requirepass "$REDISCLI_AUTH"
```

策略说明：

- `512mb`：当前 Redis 使用量约数 MB，512 MB 对 3.8 GiB 主机较保守，避免 Redis 异常吃满内存。
- `noeviction`：未知业务语义下不主动淘汰 key，优先保数据一致性；如果未来确认 Redis 只做缓存，可再评估 `allkeys-lru`。
- AOF `everysec`：在性能与可恢复性之间取平衡，通常最多损失约 1 秒写入。

### 2.3 容器重建与验证

已执行：

```bash
cd /opt/services/sub2api
sudo docker compose -f compose.yml up -d redis
```

已验证：

- `redis-cli PING` 返回 `PONG`。
- `appendonly=yes` / `aof_enabled=1`。
- `maxmemory` 不低于 512 MiB。
- `maxmemory-policy=noeviction`。
- `sub2api` 容器恢复为 healthy/running。

## 3. 回滚方式

如需回滚 Redis compose：

```bash
sudo cp /root/ops-change-backups/standards09-c1-redis-fix-<timestamp>/compose.yml.before /opt/services/sub2api/compose.yml
cd /opt/services/sub2api
sudo docker compose -f compose.yml up -d redis
```

如需恢复 Redis 数据目录：

```bash
sudo systemctl stop docker
sudo tar -C /var/lib/sub2api -xzf /root/ops-change-backups/standards09-c1-redis-fix-<timestamp>/redis_data-<timestamp>.tar.gz
sudo systemctl start docker
```

数据目录恢复属于高风险操作，只有在确认 Redis 数据损坏或误变更时才执行。

## 4. 后续建议

下一步建议进入 C2：Postgres 权限拆分预案。

原则：

- 不直接把现有业务初始化用户降权。
- 先新增低权限应用用户。
- 授予指定数据库/schema/table/sequence 必要权限。
- 修改应用连接串前先备份 `.env`，并准备快速回滚。

状态：完成。
