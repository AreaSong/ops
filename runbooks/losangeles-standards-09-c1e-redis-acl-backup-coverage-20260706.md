# LosAngeles standards/09 C1e Redis ACL 备份覆盖实施记录

更新时间：2026-07-06
服务器：`LosAngeles`
范围：`sub2api-redis`、`/opt/ops/scripts/backup/backup-redis.sh`、`/var/backups/ops/redis`

## 1. 结论

Redis 备份已覆盖 ACL 持久化文件。

当前状态：

- Redis 数据快照：`redis_data/dump.rdb`。
- Redis ACL 持久化文件：`redis_data/users.acl`。
- Redis 备份包：`/var/backups/ops/redis/redis-*.tar.gz`。
- 新生成的 Redis 备份包权限固定为 `0600`。

`users.acl` 包含 Redis ACL password hash，属于敏感材料。备份验证只检查文件名、权限和 metadata，不在终端或文档中展开文件内容。

## 2. 发现的问题

C1d 完成后，Redis 已通过 `/data/users.acl` 持久化 ACL。但随后只读检查发现，已有 Redis 备份包只包含：

```text
metadata.txt
redis_data/dump.rdb
```

这意味着如果只按旧备份恢复 Redis 数据，ACL 持久化规则需要额外手工重建，不利于灾备一致性。

## 3. 实施内容

已完成：

1. 在 `backup-redis.sh` 中增加 `ACL_FILE="$DATA_DIR/users.acl"`。
2. 当 `users.acl` 存在且非空时复制到临时备份目录。
3. 将临时备份中的 `users.acl` 权限设置为 `0600`。
4. metadata 增加：

```text
aclfile_included=yes/no
```

5. tar 打包范围从单独 `redis_data/dump.rdb` 调整为整个受控临时目录 `redis_data`。
6. 新生成的 Redis 备份包执行 `chmod 0600`。

未改变：

- Redis 备份包命名仍为 `redis-*.tar.gz`。
- 本机保留策略仍为 7 天。
- R2 同步脚本仍按现有路径和文件名匹配。
- Redis 容器、业务容器、数据库和业务数据未重启、未修改。

## 4. 验证结果

已验证：

- `backup-redis.sh` 语法检查通过。
- 手动执行 Redis 备份成功。
- 新备份包权限为 `0600`。
- 新备份包目录包含：

```text
metadata.txt
redis_data/
redis_data/dump.rdb
redis_data/users.acl
```

- metadata 显示 `aclfile_included=yes`。
- 备份指标刷新成功，`backup_last_success_timestamp{job="redis"}` 已更新。
- Redis 容器状态正常。
- `sub2api` 容器状态正常。

## 5. 恢复提示

恢复 Redis 时，`dump.rdb` 和 `users.acl` 应在同一维护窗口内恢复到 Redis 数据目录，并保持 `users.acl` root-only 权限。

恢复演练时只验证 ACL 文件存在、权限、Redis `CONFIG GET aclfile` 和 ACL deny/allow 行为，不打印 `users.acl` 内容。

## 6. 后续项

- 后续跨机恢复演练需要覆盖 `dump.rdb` 与 `users.acl` 同步恢复。
- R2 生命周期策略已配置时，继续保持 Redis 备份对象随统一备份保留策略过期。
- 分用户 ACL 仍等待 `sub2api` 支持 Redis username。
