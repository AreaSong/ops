# RB-02 磁盘空间不足

## 症状

- 告警：DiskUsageHigh / DiskUsageCritical
- 写入失败：No space left on device
- 服务异常：数据库无法写入、日志无法记录

## 快速止血

```bash
# 1. 确认哪个分区满了
df -h

# 2. 快速释放空间（安全操作）
# 清理 journald 旧日志
journalctl --vacuum-size=100M

# 清理 apt/dnf 缓存
apt clean          # Ubuntu/Debian
dnf clean all      # RHEL

# 清理 Docker 未使用的镜像和容器（先列出！）
docker system df
docker image prune --filter "until=168h"   # 7天前的 dangling 镜像
```

## 排查步骤

### 1. 定位大文件/目录

```bash
# 根目录下各目录占用
du -h --max-depth=1 / 2>/dev/null | sort -hr | head -20

# 常见位置
du -sh /var/log/*/
du -sh /data/*/
du -sh /var/lib/docker/
```

### 2. 常见占用来源

| 来源 | 路径 | 处理 |
|------|------|------|
| 应用日志 | /var/log/\<service\>/ | 检查 logrotate 是否生效 |
| 容器日志 | /var/lib/docker/containers/ | 检查 Compose logging 配置 |
| 系统日志 | journald | journalctl --vacuum-size |
| 备份文件 | /data/backup/ 或 /tmp/ | 清理过期备份 |
| Docker 镜像 | /var/lib/docker/ | docker image prune |
| MySQL binlog | /data/mysql/ | 检查 expire_logs_days |

### 3. 检查 logrotate

```bash
# 查看 logrotate 状态
cat /var/lib/logrotate/status | tail -20

# 手动触发
logrotate -f /etc/logrotate.d/<service>
```

### 4. 检查 Docker 日志配置

```bash
# 查看容器日志大小
docker inspect --format='{{.LogPath}}' <container> | xargs ls -lh

# 确认 Compose 有 logging 限制
grep -A3 logging /opt/compose/*/docker-compose.yml
```

## 恢复验证

- [ ] df -h 显示使用率 < 85%
- [ ] 服务恢复正常写入
- [ ] logrotate 配置正确

## 后续

- 填写 postmortem-template.md
- 检查 observability 磁盘告警阈值是否需要调整
- 考虑扩容数据盘或迁移数据
