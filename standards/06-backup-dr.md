# 06 备份与灾难恢复

## 备份策略

### 数据分类

| 数据类型 | 备份方式 | 频率 | 保留 |
|----------|----------|------|------|
| MySQL | mysqldump（逻辑备份） | 每日 | 日备 7 份、周备 4 份 |
| Redis | RDB 快照 | 每日 | 日备 7 份、周备 4 份 |
| 应用配置 | tar/rsync | 每日 | 日备 7 份 |
| /data 目录 | tar + 对象存储 | 每日 | 日备 7 份、周备 4 份 |

### 异云互备

- 阿里云机器 → 备份到腾讯云 COS
- 腾讯云机器 → 备份到阿里云 OSS

实现跨云容灾，避免单云故障导致备份不可用。

### 备份脚本

统一放 `scripts/backup/`，由 cron 调度：

```
scripts/backup/
├── backup-mysql.sh
├── backup-redis.sh
├── backup-configs.sh
└── README.md
```

修改 crontab 前先 `crontab -l` 备份当前配置。

### cron 示例

```cron
# 每日 02:00 MySQL 备份
0 2 * * * /opt/ops/scripts/backup/backup-mysql.sh >> /var/log/backup/mysql.log 2>&1

# 每日 03:00 Redis 备份
0 3 * * * /opt/ops/scripts/backup/backup-redis.sh >> /var/log/backup/redis.log 2>&1

# 每日 04:00 配置备份
0 4 * * * /opt/ops/scripts/backup/backup-configs.sh >> /var/log/backup/configs.log 2>&1
```

## 备份验证

- 备份脚本执行后检查退出码和日志
- 每周抽查：从对象存储下载一份备份，验证文件完整性（大小、checksum）
- **每季度恢复演练**（见下节）

## 恢复演练

每季度执行一次，验证备份真的能恢复：

### MySQL 恢复演练

```bash
# 1. 下载最近备份
# 2. 在测试环境恢复
mysql -u root -p test_restore < backup.sql
# 3. 验证表数量和数据抽样
mysql -u root -p test_restore -e "SHOW TABLES; SELECT COUNT(*) FROM key_table;"
# 4. 清理测试数据
mysql -u root -p -e "DROP DATABASE test_restore;"
```

### 演练记录

```markdown
## 恢复演练记录
- 日期：YYYY-MM-DD
- 类型：MySQL / Redis / 配置
- 备份日期：YYYY-MM-DD
- 结果：成功 / 失败
- 耗时：X 分钟
- 问题：（如有）
```

## RTO / RPO 目标

| 服务 | RPO（最大数据丢失） | RTO（最大恢复时间） |
|------|---------------------|---------------------|
| MySQL | 24 小时（日备） | 2 小时 |
| Redis | 24 小时 | 30 分钟 |
| 应用配置 | 24 小时 | 30 分钟 |

## 灾难恢复流程

1. 评估影响范围（哪些服务/数据受影响）
2. 选择最近可用备份
3. 在备用机器或原机器恢复
4. 验证数据完整性
5. 恢复服务并验证业务
6. 复盘并改进备份策略

---

修订记录：

- 2026-07-02 初版
