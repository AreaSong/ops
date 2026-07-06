# LosAngeles standards/09 C1f R2 异地备份复核实施记录

更新时间：2026-07-06
服务器：`LosAngeles`
范围：`/opt/ops/scripts/backup/sync-r2.sh`、`/var/backups/ops`、Cloudflare R2 bucket `losangeles-ops-backups`

## 1. 结论

R2 异地备份链路已复核并修正 rclone/R2 兼容问题。

当前状态：

- R2 bucket：`losangeles-ops-backups`。
- R2 prefix：`losangeles/`。
- 最新 Redis 备份对象：`redis/redis-20260706-023215.tar.gz`。
- 远端对象大小：`136204` 字节。
- R2 同步指标：`r2_backup_last_success_timestamp{bucket="losangeles-ops-backups",prefix="losangeles/"}` 已刷新。

本轮只验证对象名、对象大小、同步日志和指标，不下载、不展开备份内容，也不打印任何 R2 access key、secret key 或 Redis ACL 内容。

## 2. 发现的问题

首次执行 R2 同步时，备份对象最终已上传成功，且最新 Redis 备份对象可在 R2 上列出。但 `rclone v1.60.1-DEV` 在上传后执行 HEAD 校验时，Cloudflare R2 返回：

```text
NotImplemented: Not Implemented
status code: 501
```

表现为：

- 第一轮上传日志出现 501。
- 远端对象实际已经存在。
- rclone retry 第二轮提示 `There was nothing to transfer`，整体脚本最终成功。

这会造成运维日志误导，因此需要修正同步参数。

## 3. 兼容性探针

使用无敏感内容的小探针对象验证参数行为。

结果：

- baseline：返回 501，但对象实际存在。
- `--s3-disable-checksum`：仍返回 501。
- `--s3-no-system-metadata`：仍返回 501。
- `--s3-disable-checksum --s3-no-system-metadata`：仍返回 501。
- `--s3-no-head`：返回码为 0，日志干净。
- `--s3-use-presigned-request`：返回 400，不适用。

结论：当前最小修正是保留 `rclone copy`，增加 `--s3-no-head`。

## 4. 实施内容

已修改 `scripts/backup/sync-r2.sh`：

```bash
--s3-no-check-bucket
--s3-no-head
--fast-list
```

作用：

- 保持 R2 bucket 不做创建检查。
- 跳过上传后的 HEAD 完整性检查，避免 Cloudflare R2 返回 501 误报。
- 继续使用 copy 语义，不删除远端对象。

未改变：

- R2 bucket / prefix。
- 本机备份目录。
- R2 凭据文件。
- 本机备份生成逻辑。
- R2 lifecycle 规则。

## 5. 验证结果

已验证：

- `sync-r2.sh` 语法检查通过。
- 修正后执行 R2 同步成功。
- 清洁同步日志：`/var/log/backup/r2-sync-manual-clean-20260706-025318.log`。
- 清洁同步日志没有 `NotImplemented`、`status code: 501` 或 `ERROR`。
- 最新 Redis 备份对象在 R2 可见：

```text
redis/redis-20260706-023215.tar.gz
```

- 远端对象大小为 `136204` 字节。
- 本轮诊断探针对象已从 R2 `diagnostics/` 清理。
- R2 同步指标已刷新。
- `sub2api-redis` 状态为 `running healthy`。
- `sub2api` 状态为 `running healthy`。

## 6. 边界与后续项

- 本轮没有下载、解压或打印 R2 备份对象内容。
- 本轮使用 S3/rclone 凭据验证对象存在；未使用 Cloudflare API 读取 R2 lifecycle 规则。
- R2 lifecycle 仍以 Cloudflare 控制台配置为准。
- 后续恢复演练需要从 R2 拉取到临时隔离目录，再验证配置包、Postgres dump、Redis `dump.rdb + users.acl` 的恢复流程。
