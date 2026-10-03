# RB-01 服务返回 5xx

本手册中的 `docker compose` 路径是变量示例，不构成授权。先按 `inventory/services.yaml` 和服务器当前状态解析 `COMPOSE_FILE`；AreaForge 使用 `/opt/areaforge/docker-compose.prod.yml`，受控副本不得当作运行文件。止血写操作（down/up、重启、回滚或摘流量）必须先完成五要素批准。

## 症状

- 用户报告页面/API 报错
- 监控显示 HTTP 5xx 率上升
- 负载均衡健康检查失败

## 快速止血

```bash
# 1. 确认哪个服务在报错
docker compose -f "$COMPOSE_FILE" ps
docker compose -f "$COMPOSE_FILE" logs --tail 50

# 2. 如果是最近变更导致，回滚
docker compose -f "$COMPOSE_FILE" down
# 恢复上一个版本的 compose 或镜像 tag
docker compose -f "$COMPOSE_FILE" up -d

# 3. 验证恢复
curl -s -o /dev/null -w "%{http_code}" http://localhost:<port>/health
```

## 排查步骤

### 1. 确认影响范围

```bash
# Nginx 错误日志
tail -100 /var/log/nginx/error.log

# 应用日志
docker compose -f "$COMPOSE_FILE" logs --tail 200 --since 30m

# Grafana: 查看 5xx 率和响应时间趋势
```

### 2. 检查依赖服务

```bash
# 数据库连接：仅对 inventory 中登记的数据库类型执行；当前 LosAngeles 主要使用 PostgreSQL
docker compose -f "$COMPOSE_FILE" ps
# 使用对应服务的只读客户端和凭据，不要在命令行写入密码

# Redis 连接
redis-cli -h <host> ping

# 磁盘/内存
df -h
free -h
```

### 3. 检查最近变更

```bash
cd /opt/ops && git log --oneline -10
docker compose -f "$COMPOSE_FILE" images
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

- 仅对确认有生产影响的 P0/P1 故障填写 postmortem-template.md
- 如缺少 5xx 率告警，添加到 observability/prometheus/rules/
