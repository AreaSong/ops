# RB-01 服务返回 5xx

## 症状

- 用户报告页面/API 报错
- 监控显示 HTTP 5xx 率上升
- 负载均衡健康检查失败

## 快速止血

```bash
# 1. 确认哪个服务在报错
docker compose -f /opt/compose/<service>/docker-compose.yml ps
docker compose -f /opt/compose/<service>/docker-compose.yml logs --tail 50

# 2. 如果是最近变更导致，回滚
docker compose -f /opt/compose/<service>/docker-compose.yml down
# 恢复上一个版本的 compose 或镜像 tag
docker compose -f /opt/compose/<service>/docker-compose.yml up -d

# 3. 验证恢复
curl -s -o /dev/null -w "%{http_code}" http://localhost:<port>/health
```

## 排查步骤

### 1. 确认影响范围

```bash
# Nginx 错误日志
tail -100 /var/log/nginx/error.log

# 应用日志
docker compose -f /opt/compose/<service>/docker-compose.yml logs --tail 200 --since 30m

# Grafana: 查看 5xx 率和响应时间趋势
```

### 2. 检查依赖服务

```bash
# MySQL 连接
docker compose -f /opt/compose/mysql/docker-compose.yml ps
mysql -h <host> -u ops_ro -p -e "SELECT 1"

# Redis 连接
redis-cli -h <host> ping

# 磁盘/内存
df -h
free -h
```

### 3. 检查最近变更

```bash
cd /opt/ops && git log --oneline -10
docker compose -f /opt/compose/<service>/docker-compose.yml images
```

### 4. 常见原因

| 原因 | 特征 | 处理 |
|------|------|------|
| 应用 OOM | 容器 Exit 137 | 增加内存限制或排查内存泄漏 |
| 数据库连接耗尽 | "too many connections" | 重启连接池或 kill 空闲连接 |
| 磁盘满 | df 显示 100% | 见 RB-02 |
| 配置错误 | 最近改了 nginx/app 配置 | 回滚配置 |
| 依赖服务宕机 | 依赖服务 ps 显示 Exit | 先恢复依赖服务 |

## 恢复验证

- [ ] HTTP 健康检查返回 200
- [ ] 错误日志无新 5xx
- [ ] Grafana 5xx 率恢复正常
- [ ] 业务功能抽测通过

## 后续

- 填写 postmortem-template.md
- 如缺少 5xx 率告警，添加到 observability/prometheus/rules/
