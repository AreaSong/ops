# RB-03 MySQL 慢查询

## 症状

- 应用响应变慢
- 数据库 CPU/IO 飙高
- 告警或用户报告超时

## 快速止血

```bash
# 1. 查看当前正在执行的查询
mysql -u ops_ro -p -e "SHOW FULL PROCESSLIST;"

# 2. 如有明显阻塞查询，kill 它（需批准！）
mysql -u ops_ro -p -e "SELECT id, time, state, LEFT(info, 100) FROM information_schema.processlist WHERE command != 'Sleep' ORDER BY time DESC;"
# mysql -u ops_admin -p -e "KILL <id>;"
```

## 排查步骤

### 1. 确认慢查询

```bash
# 慢查询日志（如已开启）
tail -50 /var/log/mysql/slow.log

# 实时查看长查询
mysql -u ops_ro -p -e "
  SELECT id, user, host, db, command, time, state,
         LEFT(info, 200) AS query
  FROM information_schema.processlist
  WHERE command != 'Sleep' AND time > 5
  ORDER BY time DESC;"
```

### 2. 分析具体 SQL

```bash
# 对可疑 SQL 做 EXPLAIN
mysql -u ops_ro -p -e "EXPLAIN SELECT ... ;"

# 查看表索引
mysql -u ops_ro -p -e "SHOW INDEX FROM <table>;"
```

### 3. 常见原因

| 原因 | 特征 | 处理 |
|------|------|------|
| 缺索引 | EXPLAIN 显示 type=ALL | 添加索引（需批准，先在 test 验证） |
| 锁等待 | state=Waiting for lock | 找到持锁事务并 kill |
| 大表全扫 | rows 数量巨大 | 优化 WHERE 条件或分页 |
| 连接数过多 | Threads_connected 接近 max | 检查连接池配置 |
| 磁盘 IO 瓶颈 | iostat 显示 await 高 | 见 RB-02 或考虑读写分离 |

### 4. 检查数据库状态

```bash
mysql -u ops_ro -p -e "
  SHOW GLOBAL STATUS LIKE 'Threads_connected';
  SHOW GLOBAL STATUS LIKE 'Slow_queries';
  SHOW GLOBAL STATUS LIKE 'Innodb_row_lock%';"

# 连接数趋势（Grafana mysqld_exporter）
```

### 5. 检查是否最近有变更

```bash
# 最近是否有 schema 变更、新部署、数据量突增
cd /opt/ops && git log --oneline -10
```

## 恢复验证

- [ ] PROCESSLIST 无长时间运行的查询
- [ ] 应用响应时间恢复正常
- [ ] 慢查询计数不再增长

## 后续

- 填写 postmortem-template.md
- 确认慢查询日志已开启（long_query_time = 1）
- 考虑添加 MySQL 连接数和慢查询告警
- 对发现的缺索引 SQL 提交优化变更
