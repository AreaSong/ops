# LosAngeles 完整备份集与 R2 哈希校验

## 目标

现有 PostgreSQL、Redis、configs 和 volumes 任务继续错峰执行。每天 `04:00 UTC` 将同一受限时间窗口中的十个必需产物固化为一个显式恢复集，`04:15 UTC` 上传 R2 后再完整下载并校验 SHA-256。

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
10. `volume-areasong-ops-state`

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
3. manifest 指定的十个对象
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
- `AreaSongOpsRestoreDrillStale`

## 凭据边界

自动 R2 下载校验必须使用独立只读 token，默认 root-only 文件为
`/etc/ops/r2-verify.env`。脚本会拒绝上传凭据文件、指向同一 inode 的别名，以及与上传
凭据相同的 access key ID。凭据文件隔离和不同 key 能阻止误复用，但对象存储侧的只读
策略仍须在 Cloudflare 控制面核验并记录；S3 下载本身无法证明 token 没有写权限。

## AreaSong Ops 隔离恢复演练

从已完成本机校验的 manifest 演练：

```bash
sudo /opt/ops/scripts/backup/restore_areasong_ops_isolated.py \
  --manifest manifests/backup-set-YYYYMMDD-HHMMSS.json
```

脚本先验证 manifest sidecar、精确的十角色集合以及
`volume-areasong-ops-state` 的大小、SHA-256 和 tar 契约，只提取
`areasong-ops-state/ops.db`。恢复副本位于 root-only 临时目录，使用 SQLite 只读
immutable 模式执行 `PRAGMA integrity_check`、`PRAGMA foreign_key_check`、五张关键表
及关键列存在性检查，并记录表行数。成功后原子写入 textfile 指标；成功或失败都会删除恢复副本。

该演练不会覆盖 `/var/lib/areasong-ops`、不会启动 Runner、不会绑定端口、不会恢复业务
数据库，也不能作为生产数据库恢复授权。建议至少每月执行一次，并在更改 SQLite schema、
备份角色或归档结构后立即补做。

## 恢复演练指标边界

恢复成功指标按服务隔离，不能互相替代：

- `areasong_ops_restore_drill_*` 只代表 AreaSong Ops 自身 SQLite 状态的隔离恢复；
- `sub2api_restore_drill_*` 只代表 Sub2API PostgreSQL、Redis、应用卷和镜像兼容性的隔离恢复；
- `areaforge_restore_drill_*` 只代表 AreaForge PostgreSQL、上传卷和控制面的隔离恢复。

一个服务的演练成功只刷新该服务对应的时间戳和相关指标，不会刷新其他服务的恢复状态。Grafana
备份面板因此将三类演练分成独立面板和独立完成注释，避免把单项成功误读为整个恢复体系已验证。

## 回滚

如 manifest 或 R2 校验任务影响备份窗口：

1. 移除 `/etc/cron.d/ops-backup-manifest`，恢复原有单项备份和 R2 cron，不删除任何现有产物。
2. 使用 `sync-r2.sh --skip-verify` 仅恢复上传能力，并记录风险接受时间。
3. 回滚 `sync-r2.sh`、Prometheus 规则和相关脚本到变更前备份。
4. 确认原有 `backup_last_success_timestamp` 与 `r2_backup_last_success_timestamp` 恢复更新。
