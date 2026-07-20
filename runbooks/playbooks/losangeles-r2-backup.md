# LosAngeles Cloudflare R2 异地备份

更新时间：2026-07-03

## 目标

将 `/var/backups/ops` 下的本机备份同步到 Cloudflare R2，形成服务器外部备份副本。

## 当前配置

- Bucket：`losangeles-ops-backups`
- Endpoint：`https://dca99b3843fe84d89faaf7de67569495.r2.cloudflarestorage.com`
- Prefix：`losangeles/`
- 配置文件：`/etc/ops/r2-backup.env`
- 同步脚本：`/opt/ops/scripts/backup/sync-r2.sh`
- 日志：`/var/log/backup/r2.log`
- 指标文件：`/var/lib/node_exporter/textfile_collector/r2-backup.prom`

## 敏感信息处理

`/etc/ops/r2-backup.env` 由 root 持有，权限应为 `0600`，不得提交到 Git。

文件内包含：

```bash
R2_BUCKET=losangeles-ops-backups
R2_ENDPOINT=https://dca99b3843fe84d89faaf7de67569495.r2.cloudflarestorage.com
R2_PREFIX=losangeles/
R2_ACCESS_KEY_ID=...
R2_SECRET_ACCESS_KEY=...
```

不要在聊天、Git、日志、截图中暴露 `R2_ACCESS_KEY_ID`、`R2_SECRET_ACCESS_KEY` 或 Cloudflare Token。

## 手动同步

```bash
sudo /opt/ops/scripts/backup/sync-r2.sh
```

演练或排查时可以先 dry-run：

```bash
sudo /opt/ops/scripts/backup/sync-r2.sh --dry-run
```

## 定时任务

root crontab 在本机备份和 backup freshness 指标刷新后执行 R2 同步：

```cron
15 4 * * * /opt/ops/scripts/backup/sync-r2.sh >> /var/log/backup/r2.log 2>&1
```

## 验证

验证脚本和 R2 凭据：

```bash
sudo /opt/ops/scripts/backup/sync-r2.sh --dry-run
```

验证远端对象列表时，不要输出密钥内容。可以通过脚本环境临时执行 `rclone lsf`，或在 Cloudflare 控制台查看 `losangeles/` 前缀。

## 恢复思路

恢复时先将需要的对象复制到临时目录，再按对应备份类型恢复：

```bash
sudo mkdir -p /tmp/ops-r2-restore
# 使用已加载的 R2 配置执行 rclone copy 到 /tmp/ops-r2-restore
