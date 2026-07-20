# LosAngeles 完整备份集与 R2 哈希校验

## 目标

现有 PostgreSQL、Redis、configs 和 volumes 任务继续错峰执行。每天 `04:00 UTC` 将同一受限时间窗口中的九个必需产物固化为一个显式恢复集，`04:15 UTC` 上传 R2 后再完整下载并校验 SHA-256。

该恢复集解决“恢复时分别选择各类最新文件，意外混合不同日期”的问题。它不是数据库和文件系统的原子快照；manifest 会记录每个产物的实际修改时间，并强制整个集合跨度不超过三小时。

## 必需角色

1. `postgres-sub2api`
2. `postgres-account-vault`
3. `postgres-areaforge`
4. `redis`
5. `configs`
6. `volume-sub2api-data`
7. `volume-jadeai-data`
8. `volume-areaforge-uploads`
9. `volume-areaforge-ops-state`

缺少任何角色、文件为空或损坏、tar 成员路径不安全、集合跨度超过三小时，manifest 任务都会失败且不会更新成功指标或 `latest-manifest.txt`。

## 产物

```text
/var/backups/ops/manifests/
├── backup-set-YYYYMMDD-HHMMSS.json
├── backup-set-YYYYMMDD-HHMMSS.json.sha256
└── latest-manifest.txt
```

JSON 记录相对路径、角色、压缩字节数、展开字节数、成员数、SHA-256、修改时间、归档类型以及当时所有 Docker 容器的配置镜像和 image ID。manifest 本身的 sidecar 可检测意外损坏，但在未启用对象锁或签名密钥前，不能抵抗同时篡改 manifest 和 sidecar 的攻击者。

## 手工创建与本机校验

自然任务使用 12 小时选择窗口：

```bash
sudo /opt/ops/scripts/backup/create-backup-manifest.sh
```

历史产物首次纳管时，可临时扩大窗口，但必须检查产物时间和三小时跨度：

```bash
sudo /opt/ops/scripts/backup/create-backup-manifest.sh --window-hours 24
```

校验指定 manifest：

```bash
sudo /opt/ops/scripts/backup/backup_manifest.py verify \
  --backup-root /var/backups/ops \
  --manifest /var/backups/ops/manifests/backup-set-YYYYMMDD-HHMMSS.json
```

## R2 校验

正常的 `sync-r2.sh` 先执行 `rclone copy`，然后调用：

```bash
sudo /opt/ops/scripts/backup/verify-backup-set-r2.sh
```

验证流程会从 R2 下载：

1. `latest-manifest.txt`
2. manifest 和 sidecar
3. manifest 指定的九个对象
4. 对全部对象重新检查大小、SHA-256 和归档可读性

临时目录为 root-only `/var/tmp/ops-r2-verify.*`，无论成功或失败都会清理。验证过程不删除、覆盖或恢复生产数据。

`--skip-verify` 仅用于已经明确接受风险的紧急同步。即使同步成功，`R2BackupSetVerificationStale` 仍会提示没有完成端到端校验。

## 指标和告警

- `backup_set_last_success_timestamp`
- `backup_set_artifacts`
- `backup_set_artifact_span_seconds`
- `backup_set_r2_verify_last_success_timestamp`
- `backup_set_r2_verify_duration_seconds`
- `backup_set_r2_verify_artifacts`
- `BackupSetManifestStale`
- `BackupSetManifestInvalid`
- `R2BackupSetVerificationStale`
- `R2BackupSetVerificationIncomplete`

## 凭据边界

自动 R2 下载校验必须使用独立只读 token，默认 root-only 文件为
`/etc/ops/r2-verify.env`。脚本会拒绝上传凭据文件、指向同一 inode 的别名，以及与上传
凭据相同的 access key ID。凭据文件隔离和不同 key 能阻止误复用，但对象存储侧的只读
策略仍须在 Cloudflare 控制面核验并记录；S3 下载本身无法证明 token 没有写权限。

## 回滚

如 manifest 或 R2 校验任务影响备份窗口：

1. 移除 `/etc/cron.d/ops-backup-manifest`，恢复原有单项备份和 R2 cron，不删除任何现有产物。
2. 使用 `sync-r2.sh --skip-verify` 仅恢复上传能力，并记录风险接受时间。
3. 回滚 `sync-r2.sh`、Prometheus 规则和相关脚本到变更前备份。
4. 确认原有 `backup_last_success_timestamp` 与 `r2_backup_last_success_timestamp` 恢复更新。
